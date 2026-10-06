import Foundation
import Testing
import UIKit
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
        var registerResult: Result<Void, HubPairingFailure> = .success(())
        var handshakes: [(address: String, code: String)] = []
        var provisions: [String] = []
        var registrations = 0
        var discoveries = 0
        var ended: [String] = []
        var engineRunning = true
        var localFolders: Set<String> = []
        /// When set, the call waits for it before answering.
        var handshakeGate: AsyncStream<Void>?
        var discoveryGate: AsyncStream<Void>?
        var provisionGate: AsyncStream<Void>?
    }

    func makeModel(_ recorder: Recorder) -> HubPairingModel {
        HubPairingModel(environment: HubPairingModel.Environment(
            begin: { "flow-1" },
            end: { recorder.ended.append($0) },
            normalizeCode: { HubPairingLink.Rules.live.normalizeCode($0) },
            discover: { _ in
                await MainActor.run { recorder.discoveries += 1 }
                if let gate = await MainActor.run(body: { recorder.discoveryGate }) {
                    for await _ in gate { break }
                }
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
                if let gate = await MainActor.run(body: { recorder.provisionGate }) {
                    for await _ in gate { break }
                }
                return await MainActor.run { recorder.provisionResult }
            },
            register: { _, _ in
                await MainActor.run { recorder.registrations += 1 }
                return await MainActor.run { recorder.registerResult }
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
        #expect(!model.isWorking)
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

    @Test("An unknown provisioning outcome moves to the hand-off and is never asked again")
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
        #expect(model.provisionUncertain)
        #expect(model.provisioned == Self.notes)
        #expect(model.path == [.vault, .done])
        #expect(!model.canSync)
        model.syncSelectedVault()
        await model.runningTask?.value
        #expect(recorder.provisions.count == 1, "a lost answer is never followed by a second request")
    }

    @Test("A Hub address scanned during a search wins over the search's late answer")
    func scannedAddressBeatsLateSearch() async {
        let recorder = Recorder()
        let (gate, open) = AsyncStream<Void>.makeStream()
        recorder.discoveryGate = gate
        let model = makeModel(recorder)
        model.start()
        #expect(model.discovery == .searching)
        model.apply(link: HubPairingLink(code: "OTTER-PIANO-07", hubAddress: "10.0.0.5:8390"))
        #expect(model.discovery == .fromLink("10.0.0.5:8390"))
        model.pair() // waits for the search still in flight
        open.yield()
        open.finish()
        await model.runningTask?.value
        #expect(model.discovery == .fromLink("10.0.0.5:8390"), "the late search answer must not replace the scanned Hub")
        #expect(recorder.handshakes.map(\.address) == ["10.0.0.5:8390"])
    }

    @Test("A scanned code without an address lets the running search finish")
    func scannedCodeKeepsSearch() async {
        let recorder = Recorder()
        let (gate, open) = AsyncStream<Void>.makeStream()
        recorder.discoveryGate = gate
        let model = makeModel(recorder)
        model.start()
        model.apply(link: HubPairingLink(code: "OTTER-PIANO-07", hubAddress: nil))
        open.yield()
        open.finish()
        await model.runningTask?.value
        #expect(recorder.discoveries == 1)
        #expect(model.discovery == .found([Self.kitchen]))
        #expect(model.canonicalCode == "OTTER-PIANO-07")
    }

    @Test("Back during provisioning changes nothing, and the answer never rebuilds the navigation")
    func backDuringProvisioning() async {
        let recorder = Recorder()
        recorder.localFolders = [Self.work.id]
        let (gate, open) = AsyncStream<Void>.makeStream()
        let model = makeModel(recorder)
        model.start()
        await model.runningTask?.value
        type(model, "TULIP", "ANCHOR", "42")
        model.pair()
        await model.runningTask?.value
        recorder.provisionGate = gate
        model.syncSelectedVault()
        #expect(model.isWorking)
        model.path = [] // the sheet hides Back meanwhile; the model must not rely on it
        model.returnedToCode()
        #expect(model.hello != nil, "nothing is torn down while the Hub is being asked")
        open.yield()
        open.finish()
        await model.runningTask?.value
        #expect(model.path.isEmpty, "a late answer must not push the hand-off from elsewhere")
        #expect(model.provisioned == Self.notes)
    }

    @Test("Every vault already here: reconnect registers once and ends on the hand-off")
    func reconnectWhenEverythingIsHere() async {
        let recorder = Recorder()
        recorder.localFolders = [Self.notes.id, Self.work.id]
        let model = makeModel(recorder)
        model.start()
        await model.runningTask?.value
        type(model, "TULIP", "ANCHOR", "42")
        model.pair()
        await model.runningTask?.value
        #expect(model.everyVaultIsHere)
        #expect(!model.canSync)
        #expect(model.canReconnect)
        model.reconnect()
        await model.runningTask?.value
        #expect(recorder.registrations == 1)
        #expect(model.reconnected)
        #expect(!model.reconnectUncertain)
        #expect(model.path == [.vault, .done])
        #expect(!model.canReconnect)
    }

    @Test("A reconnect whose answer was lost ends on the hand-off and is never asked again")
    func reconnectOutcomeUnknownIsFinal() async {
        let recorder = Recorder()
        recorder.localFolders = [Self.notes.id, Self.work.id]
        recorder.registerResult = .failure(HubPairingFailure(kind: .outcomeUnknown, message: "reset"))
        let model = makeModel(recorder)
        model.start()
        await model.runningTask?.value
        type(model, "TULIP", "ANCHOR", "42")
        model.pair()
        await model.runningTask?.value
        model.reconnect()
        await model.runningTask?.value
        #expect(model.reconnectUncertain)
        #expect(model.path == [.vault, .done])
        #expect(model.failure == nil)
        #expect(!model.canReconnect)
        model.reconnect()
        await model.runningTask?.value
        #expect(recorder.registrations == 1, "a lost answer is never followed by a second request")
    }

    @Test("A Hub QR scanned in Add Device waits in the router for the Add Hub sheet")
    func scannedLinkIsQueued() {
        let router = HubLinkRouter()
        let link = HubPairingLink(code: "OTTER-PIANO-07", hubAddress: "10.0.0.5:8390")
        router.queue(link)
        #expect(router.pending == .pair(link))
        #expect(router.take() == .pair(link))
        #expect(router.pending == nil)
    }

    /// A child screen (a tab, a pushed view) presents its dialogs from its own
    /// controller; the root's `presentedViewController` stays nil meanwhile.
    @Test("A dialog presented anywhere below the root blocks a waiting link")
    func presentationProbeWalksChildren() {
        final class PresentingController: UIViewController {
            var presenting: UIViewController?
            override var presentedViewController: UIViewController? { presenting }
        }
        let root = UIViewController()
        let tab = UIViewController()
        let screen = PresentingController()
        root.addChild(tab)
        tab.addChild(screen)
        #expect(!HubLinkGate.hasPresentation(in: root))
        screen.presenting = UIViewController()
        #expect(HubLinkGate.hasPresentation(in: root), "a child's removal confirmation must hold the link back")
        #expect(!HubLinkGate.hasPresentation(in: nil))
    }

    @Test("A waiting link presents only after the screen stayed free for the grace period")
    func linkGate() {
        var gate = HubLinkGate()
        let t0 = Date(timeIntervalSince1970: 1_000)
        let steps: [(blocked: Bool, at: TimeInterval, presents: Bool)] = [
            (true, 0, false),
            (false, 0, false),
            (false, 0.5, false),
            (true, 0.6, false), // a dialog appearing resets the wait
            (false, 0.7, false),
            (false, 1.4, false),
            (false, 1.6, true),
        ]
        for step in steps {
            let presents = gate.shouldPresent(blocked: step.blocked, now: t0.addingTimeInterval(step.at))
            #expect(presents == step.presents, "at \(step.at)s")
        }
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
