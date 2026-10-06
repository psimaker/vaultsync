import Foundation
import Observation
import os
#if canImport(UIKit)
import UIKit
#endif

private let logger = Logger(subsystem: "eu.vaultsync.app", category: "hub-pairing")

/// The Add Hub flow (#174): the code with a discovery running beside it, the
/// vault choice, and the hand-off to the pending-share flow. All effects are
/// injected (`Environment`) so every state and every guard is unit-testable
/// without the bridge or the network — `HubPairingModelTests`; house pattern:
/// `ShareAcceptCoordinator`.
///
/// Rules the flow keeps (decision 045):
/// - The code is tried against one Hub only — the one the link named, or the
///   one the user picked: a code is never spent on a Hub the user did not
///   choose (each wrong try counts against that Hub's code).
/// - A link only prefills; pairing always takes the user's tap.
/// - Provisioning asks the Hub to share; it never accepts. The share arrives
///   as a pending folder and the existing accept flow takes it, with all of
///   its guards (decisions 001, 007, 008).
/// - A provision whose outcome is unknown is never retried automatically.
/// - The pairing code is never logged.
@MainActor
@Observable
final class HubPairingModel {

    // MARK: - Types

    struct Environment {
        var begin: @MainActor () -> String
        var end: @MainActor (_ flow: String) -> Void
        var normalizeCode: @MainActor (_ raw: String) -> String?
        var discover: (_ flow: String) async -> Result<[HubCandidate], HubPairingFailure>
        var handshake: (_ flow: String, _ address: String, _ code: String) async -> Result<HubHello, HubPairingFailure>
        var provision: (_ flow: String, _ vaultID: String, _ deviceName: String) async -> Result<HubVault, HubPairingFailure>
        /// Registers this iPhone without asking for a vault — for a Hub whose
        /// vaults are all here already.
        var register: (_ flow: String, _ deviceName: String) async -> Result<Void, HubPairingFailure>
        var engineRunning: @MainActor () -> Bool
        /// Folder IDs configured on this iPhone — a Hub vault with one of
        /// them is already here.
        var localFolderIDs: @MainActor () -> Set<String>
        var deviceName: @MainActor () -> String

        /// How long one discovery listens for answers.
        static let discoveryWaitMillis = 3_000

        static func live(syncthingManager: SyncthingManager) -> Environment {
            Environment(
                begin: { SyncBridgeService.hubPairingBegin() },
                end: { SyncBridgeService.hubPairingEnd(flow: $0) },
                normalizeCode: { SyncBridgeService.hubPairingNormalizeCode($0) },
                discover: { flow in
                    await Task.detached(priority: .userInitiated) {
                        SyncBridgeService.hubPairingDiscover(flow: flow, waitMillis: discoveryWaitMillis)
                    }.value
                },
                handshake: { flow, address, code in
                    await Task.detached(priority: .userInitiated) {
                        SyncBridgeService.hubPairingHandshake(flow: flow, address: address, code: code)
                    }.value
                },
                provision: { flow, vaultID, deviceName in
                    await Task.detached(priority: .userInitiated) {
                        SyncBridgeService.hubPairingProvision(flow: flow, vaultID: vaultID, deviceName: deviceName)
                    }.value
                },
                register: { flow, deviceName in
                    await Task.detached(priority: .userInitiated) {
                        SyncBridgeService.hubPairingRegister(flow: flow, deviceName: deviceName)
                    }.value
                },
                engineRunning: { syncthingManager.isRunning },
                localFolderIDs: { Set(syncthingManager.folders.map(\.id)) },
                deviceName: {
                    #if canImport(UIKit)
                    UIDevice.current.name
                    #else
                    "iPhone"
                    #endif
                }
            )
        }
    }

    enum Discovery: Equatable {
        case idle
        case searching
        /// At least one Hub answered.
        case found([HubCandidate])
        /// Nobody answered within the wait.
        case noneFound
        /// The search itself failed (no network, or Local Network denied).
        case failed(HubPairingFailureKind)
        /// The link named the Hub's address; no search ran.
        case fromLink(String)
    }

    enum Step: Hashable {
        case vault
        case done
    }

    // MARK: - State

    var fields = HubCodeFields()
    private(set) var discovery: Discovery = .idle
    /// The Hub the code goes to when the search found several.
    var chosenHub: HubCandidate?
    private(set) var isWorking = false
    /// The last failure, shown on the step it happened on.
    private(set) var failure: HubPairingFailure?
    /// The link's address did not answer: offer a search instead.
    private(set) var linkAddressUnreachable = false
    private(set) var hello: HubHello?
    var selectedVaultID: String?
    private(set) var provisioned: HubVault?
    /// The Hub may have shared, but its answer was lost: the hand-off shows
    /// what arrives; nothing is asked again in this session.
    private(set) var provisionUncertain = false
    /// Every vault was here already; the Hub only registered this iPhone.
    private(set) var reconnected = false
    /// The reconnect's answer was lost: it may or may not have happened.
    private(set) var reconnectUncertain = false
    /// The sheet's navigation path after the code step.
    var path: [Step] = []

    private var flow: String?
    private let environment: Environment
    /// The step running in the background, for tests to await.
    @ObservationIgnored private(set) var runningTask: Task<Void, Never>?
    /// The search in flight. A newer search or a scanned address supersedes
    /// it: its answer is dropped when `searchToken` moved on.
    @ObservationIgnored private var searchTask: Task<Void, Never>?
    @ObservationIgnored private var searchToken = 0

    init(environment: Environment) {
        self.environment = environment
    }

    // MARK: - Lifecycle

    /// Starts the flow when the sheet appears. A link prefills the code; its
    /// address replaces the search.
    func start(link: HubPairingLink? = nil) {
        guard flow == nil else { return }
        flow = environment.begin()
        if let link {
            fields = HubCodeFields(canonical: link.code)
            if let address = link.hubAddress {
                discovery = .fromLink(address)
                return
            }
        }
        search()
    }

    /// A link scanned inside the sheet: prefills the code; its address, when it
    /// has one, replaces the search. Never pairs by itself.
    func apply(link: HubPairingLink) {
        guard !isWorking else { return }
        fields = HubCodeFields(canonical: link.code)
        failure = nil
        linkAddressUnreachable = false
        if let address = link.hubAddress {
            // The scanned address wins over a search still in flight.
            searchToken += 1
            discovery = .fromLink(address)
            chosenHub = nil
            return
        }
        switch discovery {
        case .found, .searching:
            break // keep what the search found, or let it finish
        default:
            search()
        }
    }

    /// Ends the flow (sheet dismissed). What the Hub already did stays done;
    /// an answer still on its way is dropped (its flow is gone).
    func stop() {
        guard let flow else { return }
        runningTask?.cancel()
        runningTask = nil
        searchToken += 1
        isWorking = false
        environment.end(flow)
        self.flow = nil
    }

    // MARK: - Step 1: code and Hub

    /// The canonical code, once all three fields hold a valid one.
    var canonicalCode: String? {
        guard fields.isComplete else { return nil }
        return environment.normalizeCode(fields.joined)
    }

    /// All three fields are filled but do not form a code.
    var codeLooksWrong: Bool {
        fields.isComplete && canonicalCode == nil
    }

    /// Where the code goes: the link's address, the only Hub found, or the
    /// one the user chose among several.
    var targetAddress: String? {
        switch discovery {
        case .fromLink(let address):
            return address
        case .found(let hubs):
            if hubs.count == 1 { return hubs[0].address }
            return chosenHub.flatMap { chosen in hubs.contains(chosen) ? chosen.address : nil }
        default:
            return nil
        }
    }

    /// The found Hub's name when exactly one answered (or was chosen).
    var targetName: String? {
        guard case .found(let hubs) = discovery else { return nil }
        if hubs.count == 1 { return hubs[0].name }
        return chosenHub?.name
    }

    var canPair: Bool {
        flow != nil && !isWorking && canonicalCode != nil && targetAddress != nil && environment.engineRunning()
    }

    /// Takes the text of one field: a full code pasted anywhere fills all
    /// three; a separator moves on to the next field. Returns the field that
    /// should have focus next, if it changes.
    func update(_ field: HubCodeField, with text: String) -> HubCodeField? {
        if text.count > 3, let code = environment.normalizeCode(text) {
            fields = HubCodeFields(canonical: code)
            return HubCodeField.none
        }
        switch field {
        case .first, .second:
            let (word, advance) = HubCodeFields.wordInput(text)
            if field == .first { fields.first = word } else { fields.second = word }
            return advance ? (field == .first ? .second : .number) : nil
        case .number:
            fields.number = HubCodeFields.numberInput(text)
            return nil
        case .none:
            return nil
        }
    }

    /// Searches the network for Hubs (a new search replaces the last result).
    func search() {
        guard let flow, !isWorking else { return }
        linkAddressUnreachable = false
        failure = nil
        discovery = .searching
        chosenHub = nil
        searchToken += 1
        let token = searchToken
        let previous = searchTask
        searchTask = Task {
            // One bridge call per flow at a time: a superseded search ends
            // first (its answer is dropped by the token).
            await previous?.value
            let result = await environment.discover(flow)
            guard self.flow == flow, searchToken == token else { return }
            switch result {
            case .success(let hubs):
                discovery = hubs.isEmpty ? .noneFound : .found(hubs)
            case .failure(let failure):
                switch failure.kind {
                case .staleFlow, .cancelled:
                    return
                case .inProgress:
                    discovery = .idle
                default:
                    logger.error("Hub discovery failed: \(failure.kind.rawValue, privacy: .public) \(failure.message, privacy: .private)")
                    discovery = .failed(failure.kind)
                }
            }
        }
        runningTask = searchTask
    }

    /// Exchanges the code with the target Hub. On success the vault step
    /// follows.
    func pair() {
        guard canPair, let flow, let code = canonicalCode, let address = targetAddress else { return }
        isWorking = true
        failure = nil
        linkAddressUnreachable = false
        let fromLink: Bool
        if case .fromLink = discovery { fromLink = true } else { fromLink = false }
        logger.info("Pairing with a Hub (address from \(fromLink ? "link" : "search", privacy: .public)) \(address, privacy: .private)")
        let search = searchTask
        runningTask = Task {
            // A search still in flight (superseded by a scanned address)
            // ends first: the bridge runs one call per flow at a time.
            await search?.value
            let result = await environment.handshake(flow, address, code)
            guard self.flow == flow else { return }
            isWorking = false
            switch result {
            case .success(let hello):
                self.hello = hello
                provisioned = nil
                provisionUncertain = false
                reconnected = false
                let selectable = selectableVaults
                selectedVaultID = selectable.count == 1 ? selectable[0].id : nil
                path = [.vault]
                logger.info("Paired with Hub \(hello.hubName, privacy: .private): \(hello.vaults.count) vaults")
            case .failure(let failure):
                guard failure.kind != .staleFlow, failure.kind != .cancelled else { return }
                logger.error("Hub pairing failed: \(failure.kind.rawValue, privacy: .public) \(failure.message, privacy: .private)")
                if fromLink, failure.kind == .unreachable {
                    linkAddressUnreachable = true
                }
                self.failure = failure
            }
        }
    }

    // MARK: - Step 2: vault

    /// Vaults this iPhone can join: not configured here yet.
    var selectableVaults: [HubVault] {
        guard let hello else { return [] }
        let local = environment.localFolderIDs()
        return hello.vaults.filter { !local.contains($0.id) }
    }

    func isAlreadyHere(_ vault: HubVault) -> Bool {
        environment.localFolderIDs().contains(vault.id)
    }

    var selectedVault: HubVault? {
        guard let id = selectedVaultID else { return nil }
        return selectableVaults.first { $0.id == id }
    }

    /// The Hub has vaults, and all of them are on this iPhone already.
    var everyVaultIsHere: Bool {
        guard let hello, hello.catalogAvailable, !hello.vaults.isEmpty else { return false }
        return selectableVaults.isEmpty
    }

    /// Nothing is asked of the Hub twice in one session: after an answer —
    /// or a lost one — the flow only moves on.
    private var provisioningDone: Bool {
        provisioned != nil || reconnected
    }

    var canSync: Bool {
        flow != nil && !isWorking && !provisioningDone && selectedVault != nil && environment.engineRunning()
    }

    var canReconnect: Bool {
        flow != nil && !isWorking && !provisioningDone && everyVaultIsHere && environment.engineRunning()
    }

    /// Asks the Hub to share the chosen vault with this iPhone.
    func syncSelectedVault() {
        guard canSync, let flow, let vault = selectedVault else { return }
        isWorking = true
        failure = nil
        runningTask = Task {
            let result = await environment.provision(flow, vault.id, environment.deviceName())
            guard self.flow == flow else { return }
            isWorking = false
            switch result {
            case .success(let shared):
                provisioned = shared
                showHandoff()
                logger.info("Hub shares vault \(shared.label, privacy: .private) with this iPhone")
            case .failure(let failure):
                guard failure.kind != .staleFlow, failure.kind != .cancelled else { return }
                logger.error("Hub provisioning failed: \(failure.kind.rawValue, privacy: .public) \(failure.message, privacy: .private)")
                switch failure.kind {
                case .sessionExpired, .noSession:
                    // The Hub forgot the session: back to the code, which
                    // stays filled in — one tap pairs again.
                    hello = nil
                    selectedVaultID = nil
                    path = []
                    self.failure = HubPairingFailure(kind: .sessionExpired, message: failure.message)
                case .outcomeUnknown:
                    // The request may have reached the Hub. Never asked
                    // again: the hand-off shows whether the share arrives.
                    provisioned = vault
                    provisionUncertain = true
                    showHandoff()
                default:
                    self.failure = failure
                }
            }
        }
    }

    /// Every vault is here already: registers this iPhone with the Hub again
    /// (and the Hub as a device here, should it have been removed).
    func reconnect() {
        guard canReconnect, let flow else { return }
        isWorking = true
        failure = nil
        runningTask = Task {
            let result = await environment.register(flow, environment.deviceName())
            guard self.flow == flow else { return }
            isWorking = false
            switch result {
            case .success:
                reconnected = true
                showHandoff()
            case .failure(let failure):
                guard failure.kind != .staleFlow, failure.kind != .cancelled else { return }
                logger.error("Hub registration failed: \(failure.kind.rawValue, privacy: .public) \(failure.message, privacy: .private)")
                switch failure.kind {
                case .sessionExpired, .noSession:
                    hello = nil
                    path = []
                    self.failure = HubPairingFailure(kind: .sessionExpired, message: failure.message)
                case .outcomeUnknown:
                    // As with a share: a lost answer is never asked again.
                    reconnected = true
                    reconnectUncertain = true
                    showHandoff()
                default:
                    self.failure = failure
                }
            }
        }
    }

    /// The hand-off follows the vault step — and only from there: an answer
    /// that arrives elsewhere never rebuilds the navigation.
    private func showHandoff() {
        if path == [.vault] {
            path = [.vault, .done]
        }
    }

    /// Leaving the vault step by Back: the session is not reused — pairing
    /// again starts a fresh one. Never while the Hub is being asked (the
    /// vault step hides Back meanwhile).
    func returnedToCode() {
        guard !isWorking else { return }
        if path.isEmpty, hello != nil, !provisioningDone {
            hello = nil
            selectedVaultID = nil
            failure = nil
        }
    }

    #if DEBUG
    /// DEBUG only: the design-preview fixture shows a step without a network.
    func _previewSet(fields: HubCodeFields, discovery: Discovery, hello: HubHello?, selectedVaultID: String?, provisioned: HubVault?, path: [Step]) {
        searchToken += 1
        flow = "design-preview"
        self.fields = fields
        self.discovery = discovery
        self.hello = hello
        self.selectedVaultID = selectedVaultID
        self.provisioned = provisioned
        self.path = path
    }
    #endif
}

/// The three code fields, for focus.
enum HubCodeField: Hashable {
    case first
    case second
    case number
    /// No field (focus leaves the code entry).
    case none
}

/// Where a vault the Hub shares stands on this iPhone — the done step reads
/// the live pending-share state instead of promising an outcome (#174).
enum HubShareHandoff: Equatable {
    /// The share has not arrived yet.
    case waitingForShare
    /// It arrived; the accept flow cannot run until Obsidian is connected.
    case waitingForObsidian
    /// It arrived and the automatic accept pass will take it.
    case accepting
    /// It arrived and waits for the user (a folder with files, an earlier
    /// removal, an ignored share, a refusal) — on the Sync tab.
    case needsDecision
    /// The vault is configured on this iPhone.
    case syncing

    static func status(
        folderID: String,
        configuredFolderIDs: Set<String>,
        pendingFolderIDs: Set<String>,
        autoAcceptEligibleIDs: Set<String>,
        recordedFailureIDs: Set<String>,
        obsidianConnected: Bool
    ) -> HubShareHandoff {
        if configuredFolderIDs.contains(folderID) { return .syncing }
        guard pendingFolderIDs.contains(folderID) else { return .waitingForShare }
        if recordedFailureIDs.contains(folderID) || !autoAcceptEligibleIDs.contains(folderID) {
            return .needsDecision
        }
        return obsidianConnected ? .accepting : .waitingForObsidian
    }
}
