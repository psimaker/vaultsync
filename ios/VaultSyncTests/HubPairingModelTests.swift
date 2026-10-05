import Foundation
import Testing
@testable import VaultSync

/// The Add Hub flow without the bridge or a network (#174): which Hub a code
/// may go to, what a link may do, how a stale answer, an expired session and
/// an unknown provisioning outcome are handled.
private enum HubFixture {
    static let notes = HubVault(id: "vs-aaaaaaaaaaaa", label: "Notes", files: 3, devices: 1)
    static let work = HubVault(id: "vs-bbbbbbbbbbbb", label: "Work", files: 0, devices: 0)
    static let hello = HubHello(hubName: "Test Hub", hubDeviceID: "P56IOI7-MZJNU2Y-IQGDREY-DM2MGTI-MGL3BXN-PQ6W5BM-TBBZ4TJ-XZWICQ2",
                                catalogAvailable: true, vaults: [notes, work])
    static let kitchen = HubCandidate(address: "192.168.1.20:8390", name: "Kitchen")
    static let office = HubCandidate(address: "192.168.1.30:8390", name: "Office")
}

@MainActor
@Suite("Add Hub flow (#174)")
struct HubPairingModelTests {
    static let notes = HubFixture.notes
    static let work = HubFixture.work
    static let hello = HubFixture.hello
    static let kitchen = HubFixture.kitchen
    static let office = HubFixture.office

    /// Records what the flow asked the bridge for, and answers as told.
    @MainActor
    final class Recorder {
        var discovered: Result<[HubCandidate], HubPairingFailure> = .success([HubFixture.kitchen])
        var handshakeResult: Result<HubHello, HubPairingFailure> = .success(HubFixture.hello)
        var provisionResult: Result<HubVault, HubPairingFailure> = .success(HubFixture.notes)
        var handshakes: [(address: String, code: String)] = []
        var provisions: [String] = []
        var discoveries = 0
        var ended: [String] = []
        var engineRunning = true
        var localFolders: Set<String> = []
        /// When set, the handshake waits for it before answering.
        var handshakeGate: AsyncStream<Void>?
    }

    func makeModel(_ recorder: Recorder) -> HubPairingModel {
        HubPairingModel(environment: HubPairingModel.Environment(
            begin: { "flow-1" },
            end: { recorder.ended.append($0) },
            normalizeCode: { HubPairingLink.Rules.live.normalizeCode($0) },
            discover: { _ in
                await MainActor.run { recorder.discoveries += 1 }
                return await MainActor.run { recorder.discovered }
            },
            handshake: { _, address, code in
                await MainActor.run { recorder.handshakes.append((address, code)) }
                if let gate = await MainActor.run(body: { recorder.handshakeGate }) {
                    for await _ in gate { break }
                }
                return await MainActor.run { recorder.handshakeResult }
            },
            provision: { _, vaultID, _ in
                await MainActor.run { recorder.provisions.append(vaultID) }
                return await MainActor.run { recorder.provisionResult }
            },
            engineRunning: { recorder.engineRunning },
            localFolderIDs: { recorder.localFolders },
            deviceName: { "Test iPhone" }
        ))
    }

    func type(_ model: HubPairingModel, _ first: String, _ second: String, _ number: String) {
        _ = model.update(.first, with: first)
        _ = model.update(.second, with: second)
        _ = model.update(.number, with: number)
    }

    @Test("The only Hub found gets the code; the code is canonical")
    func singleHubFound() async {
        let recorder = Recorder()
        let model = makeModel(recorder)
        model.start()
        await model.runningTask?.value
        #expect(model.discovery == .found([Self.kitchen]))
        #expect(!model.canPair, "no code yet")
        type(model, "tulip", "anchor", "7")
        #expect(model.canonicalCode == "TULIP-ANCHOR-07")
        #expect(model.canPair)
        model.pair()
        await model.runningTask?.value
        #expect(recorder.handshakes.count == 1)
        #expect(recorder.handshakes.first?.address == Self.kitchen.address)
        #expect(recorder.handshakes.first?.code == "TULIP-ANCHOR-07")
        #expect(model.path == [.vault])
    }

    @Test("Several Hubs found: the code goes nowhere until the user picks one")
    func severalHubsNeedAChoice() async {
        let recorder = Recorder()
        recorder.discovered = .success([Self.kitchen, Self.office])
        let model = makeModel(recorder)
        model.start()
        await model.runningTask?.value
        type(model, "TULIP", "ANCHOR", "42")
        #expect(model.targetAddress == nil)
        #expect(!model.canPair)
        model.pair()
        #expect(recorder.handshakes.isEmpty, "a code must never be tried on a Hub the user did not choose")
        model.chosenHub = Self.office
        #expect(model.canPair)
        model.pair()
        await model.runningTask?.value
        #expect(recorder.handshakes.map(\.address) == [Self.office.address])
    }

    @Test("A link prefills the code and replaces the search, but never pairs by itself")
    func linkPrefillsOnly() async {
        let recorder = Recorder()
        let model = makeModel(recorder)
        model.start(link: HubPairingLink(code: "OTTER-PIANO-07", hubAddress: "10.0.0.5:8390"))
        await model.runningTask?.value
        #expect(recorder.discoveries == 0)
        #expect(model.discovery == .fromLink("10.0.0.5:8390"))
        #expect(model.fields == HubCodeFields(first: "OTTER", second: "PIANO", number: "07"))
        #expect(recorder.handshakes.isEmpty, "a link only prefills")
        #expect(model.canPair)
    }

    @Test("A link address that does not answer offers the search instead")
    func unreachableLinkOffersSearch() async {
        let recorder = Recorder()
        recorder.handshakeResult = .failure(HubPairingFailure(kind: .unreachable, message: "dial"))
        let model = makeModel(recorder)
        model.start(link: HubPairingLink(code: "OTTER-PIANO-07", hubAddress: "10.0.0.5:8390"))
        model.pair()
        await model.runningTask?.value
        #expect(model.linkAddressUnreachable)
        #expect(model.failure?.kind == .unreachable)
        #expect(recorder.handshakes.count == 1, "no automatic second try anywhere")
        model.search()
        await model.runningTask?.value
        #expect(recorder.discoveries == 1)
        #expect(model.discovery == .found([Self.kitchen]))
        #expect(!model.linkAddressUnreachable)
    }

    @Test("A full code pasted into any field fills all three")
    func pasteFillsAllFields() {
        let model = makeModel(Recorder())
        let focus = model.update(.second, with: "tulip anchor 42")
        #expect(model.fields == HubCodeFields(first: "TULIP", second: "ANCHOR", number: "42"))
        #expect(focus == HubCodeField.none)
        let next = model.update(.first, with: "OTTER ")
        #expect(next == .second)
        #expect(model.fields.first == "OTTER")
    }

    @Test("A wrong code is reported, the flow stays on the code")
    func wrongCode() async {
        let recorder = Recorder()
        recorder.handshakeResult = .failure(HubPairingFailure(kind: .codeRejected, message: "x"))
        let model = makeModel(recorder)
        model.start()
        await model.runningTask?.value
        type(model, "TULIP", "ANCHOR", "42")
        model.pair()
        await model.runningTask?.value
        #expect(model.failure?.kind == .codeRejected)
        #expect(model.path.isEmpty)
        #expect(model.hello == nil)
    }

    @Test("An answer for a flow that ended changes nothing")
    func staleAnswerIsDropped() async {
        let recorder = Recorder()
        let (gate, open) = AsyncStream<Void>.makeStream()
        recorder.handshakeGate = gate
        let model = makeModel(recorder)
        model.start()
        await model.runningTask?.value
        type(model, "TULIP", "ANCHOR", "42")
        model.pair()
        let task = model.runningTask
        model.stop()
        open.yield()
        open.finish()
        await task?.value
        #expect(recorder.ended == ["flow-1"])
        #expect(model.path.isEmpty, "a late handshake must not move an ended flow on")
        #expect(model.hello == nil)
    }

    @Test("Vaults already on this iPhone cannot be chosen; the only other one is preselected")
    func vaultChoice() async {
        let recorder = Recorder()
        recorder.localFolders = [Self.work.id]
        let model = makeModel(recorder)
        model.start()
        await model.runningTask?.value
        type(model, "TULIP", "ANCHOR", "42")
        model.pair()
        await model.runningTask?.value
        #expect(model.selectableVaults == [Self.notes])
        #expect(model.isAlreadyHere(Self.work))
        #expect(model.selectedVaultID == Self.notes.id)
        model.selectedVaultID = Self.work.id
        #expect(model.selectedVault == nil)
        #expect(!model.canSync)
    }

    @Test("Two choosable vaults: nothing is preselected")
    func noPreselectionAmongSeveral() async {
        let recorder = Recorder()
        let model = makeModel(recorder)
        model.start()
        await model.runningTask?.value
        type(model, "TULIP", "ANCHOR", "42")
        model.pair()
        await model.runningTask?.value
        #expect(model.selectedVaultID == nil)
        #expect(!model.canSync)
    }

    @Test("Provisioning sends the vault's ID and ends on the hand-off")
    func provisionSucceeds() async {
        let recorder = Recorder()
        recorder.localFolders = [Self.work.id]
        let model = makeModel(recorder)
        model.start()
        await model.runningTask?.value
        type(model, "TULIP", "ANCHOR", "42")
        model.pair()
        await model.runningTask?.value
        model.syncSelectedVault()
        await model.runningTask?.value
        #expect(recorder.provisions == [Self.notes.id])
        #expect(model.provisioned == Self.notes)
        #expect(model.path == [.vault, .done])
    }

    @Test("An unknown provisioning outcome is reported and never retried")
    func outcomeUnknownIsNotRetried() async {
        let recorder = Recorder()
        recorder.localFolders = [Self.work.id]
        recorder.provisionResult = .failure(HubPairingFailure(kind: .outcomeUnknown, message: "reset"))
        let model = makeModel(recorder)
        model.start()
        await model.runningTask?.value
        type(model, "TULIP", "ANCHOR", "42")
        model.pair()
        await model.runningTask?.value
        model.syncSelectedVault()
        await model.runningTask?.value
        #expect(model.failure?.kind == .outcomeUnknown)
        #expect(recorder.provisions.count == 1)
        #expect(model.path == [.vault])
    }

    @Test("An expired session returns to the code, which stays filled in")
    func expiredSessionReturnsToCode() async {
        let recorder = Recorder()
        recorder.localFolders = [Self.work.id]
        recorder.provisionResult = .failure(HubPairingFailure(kind: .sessionExpired, message: "x"))
        let model = makeModel(recorder)
        model.start()
        await model.runningTask?.value
        type(model, "TULIP", "ANCHOR", "42")
        model.pair()
        await model.runningTask?.value
        model.syncSelectedVault()
        await model.runningTask?.value
        #expect(model.path.isEmpty)
        #expect(model.hello == nil)
        #expect(model.failure?.kind == .sessionExpired)
        #expect(model.canonicalCode == "TULIP-ANCHOR-42")
        #expect(model.canPair)
    }

    @Test("Nothing pairs while the engine is not running")
    func engineMustRun() async {
        let recorder = Recorder()
        recorder.engineRunning = false
        let model = makeModel(recorder)
        model.start()
        await model.runningTask?.value
        type(model, "TULIP", "ANCHOR", "42")
        #expect(!model.canPair)
    }

    @Test("A failed search says so instead of looking like an empty network")
    func searchFailures() async {
        let recorder = Recorder()
        recorder.discovered = .failure(HubPairingFailure(kind: .noNetwork, message: "x"))
        let model = makeModel(recorder)
        model.start()
        await model.runningTask?.value
        #expect(model.discovery == .failed(.noNetwork))
        recorder.discovered = .success([])
        model.search()
        await model.runningTask?.value
        #expect(model.discovery == .noneFound)
    }

    @Test("The hand-off reports where the Hub's share stands")
    func handoffStatus() {
        func status(configured: Set<String> = [], pending: Set<String> = [], eligible: Set<String> = [],
                    failures: Set<String> = [], obsidian: Bool = true) -> HubShareHandoff {
            HubShareHandoff.status(folderID: "vs-a", configuredFolderIDs: configured, pendingFolderIDs: pending,
                                   autoAcceptEligibleIDs: eligible, recordedFailureIDs: failures, obsidianConnected: obsidian)
        }
        #expect(status() == .waitingForShare)
        #expect(status(pending: ["vs-a"], eligible: ["vs-a"]) == .accepting)
        #expect(status(pending: ["vs-a"], eligible: ["vs-a"], obsidian: false) == .waitingForObsidian)
        #expect(status(pending: ["vs-a"], eligible: ["vs-a"], failures: ["vs-a"]) == .needsDecision)
        #expect(status(pending: ["vs-a"]) == .needsDecision, "a share the user removed earlier is never auto-accepted")
        #expect(status(configured: ["vs-a"], pending: ["vs-a"]) == .syncing)
    }
}
