import Foundation
import Testing
@testable import VaultSync

/// An accept still queued in the engine when the bridge's wait ends lands
/// later. The app showed it as a failure — "Could Not Accept Share" — a
/// second tap sent a second accept of the same share, and a landing was
/// never seen as the accept having worked (#341). The bridge now answers
/// such an accept with its own text and keeps it apart until the engine has
/// taken it (`TestIssue341_…` in the bridge suite). Here: the app keeps the
/// share waiting for that accept — no failure anywhere, no second accept —
/// and settles it once the engine has taken it: landed, it is the accept
/// having worked; refused, the share is an ordinary pending one again. The
/// bridge is replaced, never the engine run; the records live in defaults of
/// the test's own or under folder IDs of its own.
@MainActor
@Suite("An accept the engine has not confirmed waits, and is never shown as a failure (#341)")
struct Issue341AcceptNotConfirmedTests {

    private static let notConfirmed = SyncBridgeService.folderAcceptNotConfirmedError

    @MainActor
    private final class Rig {
        let suiteName = "issue341-accept-\(UUID().uuidString)"
        let defaults: UserDefaults
        let folderID = "issue341-\(UUID().uuidString)"
        let manager = SyncthingManager()
        let vaultManager = VaultManager()
        let sandbox: URL
        let root: URL
        /// What the bridge answers the next accept with.
        var answer: String? = SyncBridgeService.folderAcceptNotConfirmedError
        private(set) var acceptsSent = 0
        /// Whether the engine still holds the accept in its queue.
        var inFlight = true
        /// The folders the engine has configured: ID and path.
        var configured: [(id: String, path: String)] = []

        init() throws {
            defaults = UserDefaults(suiteName: suiteName)!
            sandbox = FileManager.default.temporaryDirectory
                .appendingPathComponent("issue341-accept-\(UUID().uuidString)", isDirectory: true)
            root = sandbox.appendingPathComponent("Obsidian", isDirectory: true)
            try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
            manager._testUseRemoval(bridge: { _ in nil }, defaults: defaults)
            manager._testUseAccept { [unowned self] _ in
                self.acceptsSent += 1
                return self.answer
            }
            manager._testUseAcceptInFlight { [unowned self] _ in self.inFlight }
            manager._testUseFolderRead { [unowned self] in self.foldersJSON }
            vaultManager._testSetAccess(accessible: true, detectedVaults: [], obsidianDirectoryURL: root)
        }

        var share: SyncthingManager.PendingFolderInfo {
            SyncthingManager.PendingFolderInfo(id: folderID, label: "Vault", offeredBy: [])
        }

        /// Where the default path puts the share.
        var target: String { root.appendingPathComponent("Vault", isDirectory: true).path }

        private var foldersJSON: String {
            let rows = configured.map {
                "{\"id\":\"\($0.id)\",\"label\":\"Vault\",\"path\":\"\($0.path)\",\"type\":\"sendreceive\",\"paused\":false,\"deviceIDs\":[]}"
            }
            return "[" + rows.joined(separator: ",") + "]"
        }

        var recordedRemoved: Bool { manager.userRemovedFolderIDs.contains(folderID) }

        func tearDown() {
            FolderPathReconciler.removeRel(forFolder: folderID)
            ManualShareTargetStore.forgetConsent(forFolder: folderID)
            defaults.removePersistentDomain(forName: suiteName)
            try? FileManager.default.removeItem(at: sandbox)
        }
    }

    @Test("The note is the bridge's own answer only; a refusal stays a refusal")
    func noteOnlyForTheExportedAnswer() throws {
        let note = try #require(SyncthingManager.acceptNotConfirmedNote(forAnswer: Self.notConfirmed))
        #expect(note == SyncthingManager.acceptNotConfirmedNote)
        #expect(SyncthingManager.acceptNotConfirmedNote(forAnswer: "folder already exists") == nil)
        #expect(SyncthingManager.acceptNotConfirmedNote(forAnswer: "modify config: config commit: timed out after 30s") == nil)
    }

    @Test("The default path: the answer is a waiting accept, nothing a confirmed accept records, and no second accept")
    func defaultPathWaits() throws {
        let rig = try Rig()
        defer { rig.tearDown() }

        let outcome = rig.vaultManager.acceptPendingShare(folder: rig.share, syncthingManager: rig.manager, mergeConfirmed: false)
        #expect(outcome == .notConfirmed(note: SyncthingManager.acceptNotConfirmedNote))
        #expect(rig.manager.acceptsAwaitingConfirmation[rig.folderID].map { SyncthingManager.samePath($0, rig.target) } == true)
        #expect(FolderPathReconciler.loadRel()[rig.folderID] == nil)

        // Tapped again while it is still queued: the same note, and nothing
        // reaches the bridge.
        let again = rig.vaultManager.acceptPendingShare(folder: rig.share, syncthingManager: rig.manager, mergeConfirmed: false)
        #expect(again == .notConfirmed(note: SyncthingManager.acceptNotConfirmedNote))
        #expect(rig.acceptsSent == 1)
    }

    @Test("The picker path: the note, no chosen name recorded, and no second accept")
    func pickerPathWaits() throws {
        let rig = try Rig()
        defer { rig.tearDown() }

        let shown = rig.vaultManager.acceptPendingShare(folder: rig.share, intoTargetNamed: "Chosen", syncthingManager: rig.manager)
        #expect(shown == SyncthingManager.acceptNotConfirmedNote)
        #expect(ManualShareTargetStore.target(forFolder: rig.folderID) == nil)
        #expect(FolderPathReconciler.loadRel()[rig.folderID] == nil)
        #expect(rig.manager.acceptsAwaitingConfirmation[rig.folderID] != nil)

        let again = rig.vaultManager.acceptPendingShare(folder: rig.share, intoTargetNamed: "Other", syncthingManager: rig.manager)
        #expect(again == SyncthingManager.acceptNotConfirmedNote)
        #expect(rig.acceptsSent == 1)
    }

    @Test("Seen landed by the poll: the accept is done, and the removal's record is lifted")
    func landingLiftsTheRecord() throws {
        let rig = try Rig()
        defer { rig.tearDown() }
        #expect(rig.manager.removeFolder(id: rig.folderID) == nil)
        #expect(rig.recordedRemoved)

        _ = rig.vaultManager.acceptPendingShare(folder: rig.share, syncthingManager: rig.manager, mergeConfirmed: false)
        // Not confirmed: the record stays, so the automatic pass leaves the
        // share alone meanwhile.
        #expect(rig.recordedRemoved)

        rig.configured = [(rig.folderID, rig.target)]
        rig.manager._testApplyPolledFolderRead(json: "[{\"id\":\"\(rig.folderID)\",\"label\":\"Vault\",\"path\":\"\(rig.target)\",\"type\":\"sendreceive\",\"paused\":false,\"deviceIDs\":[]}]", readStartedAt: 0, engineRunningAcrossRead: true)
        let settled = rig.manager.settleAcceptsAwaitingConfirmation(stillQueued: [], checked: [rig.folderID])
        #expect(settled[rig.folderID] == .landed)
        #expect(rig.manager.acceptsAwaitingConfirmation.isEmpty)
        #expect(!rig.recordedRemoved)
    }

    @Test("Seen refused by the poll: an ordinary pending share again, the removal's record kept")
    func refusalReturnsTheShare() throws {
        let rig = try Rig()
        defer { rig.tearDown() }
        #expect(rig.manager.removeFolder(id: rig.folderID) == nil)
        _ = rig.vaultManager.acceptPendingShare(folder: rig.share, syncthingManager: rig.manager, mergeConfirmed: false)

        // Still queued at the poll's read: nothing changes.
        #expect(rig.manager.settleAcceptsAwaitingConfirmation(stillQueued: [rig.folderID], checked: [rig.folderID]).isEmpty)
        #expect(rig.manager.acceptsAwaitingConfirmation[rig.folderID] != nil)

        // Taken and refused: not configured.
        let settled = rig.manager.settleAcceptsAwaitingConfirmation(stillQueued: [], checked: [rig.folderID])
        #expect(settled[rig.folderID] == .gone)
        #expect(rig.manager.acceptsAwaitingConfirmation.isEmpty)
        #expect(rig.recordedRemoved)
    }

    @Test("A tap after the accept landed is that accept having worked, not a refusal")
    func tapAfterTheLandingIsDone() throws {
        let rig = try Rig()
        defer { rig.tearDown() }
        #expect(rig.manager.removeFolder(id: rig.folderID) == nil)
        _ = rig.vaultManager.acceptPendingShare(folder: rig.share, syncthingManager: rig.manager, mergeConfirmed: false)

        // It landed before the poll saw it.
        rig.inFlight = false
        rig.configured = [(rig.folderID, rig.target)]
        let outcome = rig.vaultManager.acceptPendingShare(folder: rig.share, syncthingManager: rig.manager, mergeConfirmed: false)
        #expect(outcome == .accepted)
        #expect(rig.acceptsSent == 1)
        #expect(!rig.recordedRemoved)
        #expect(rig.manager.acceptsAwaitingConfirmation.isEmpty)
    }

    @Test("Landed between the settling and the bridge's answer: the folder at the accept's own path is the accept")
    func folderAlreadyExistsAtTheAcceptsPathIsDone() throws {
        let rig = try Rig()
        defer { rig.tearDown() }
        #expect(rig.manager.acceptPendingFolder(folderID: rig.folderID, label: "Vault", path: rig.target, root: rig.root.path, allowNonEmpty: false) == Self.notConfirmed)

        rig.answer = SyncthingManager.folderAlreadyExists
        rig.configured = [(rig.folderID, rig.target)]
        #expect(rig.manager.acceptPendingFolder(folderID: rig.folderID, label: "Vault", path: rig.target, root: rig.root.path, allowNonEmpty: false) == nil)
        #expect(rig.manager.acceptsAwaitingConfirmation.isEmpty)

        // Elsewhere it is another folder of the ID, and stays a refusal.
        let other = try Rig()
        defer { other.tearDown() }
        #expect(other.manager.acceptPendingFolder(folderID: other.folderID, label: "Vault", path: other.target, root: other.root.path, allowNonEmpty: false) == Self.notConfirmed)
        other.answer = SyncthingManager.folderAlreadyExists
        other.configured = [(other.folderID, other.root.appendingPathComponent("Elsewhere").path)]
        #expect(other.manager.acceptPendingFolder(folderID: other.folderID, label: "Vault", path: other.target, root: other.root.path, allowNonEmpty: false) == SyncthingManager.folderAlreadyExists)
    }

    // MARK: - What the person sees

    @MainActor
    private final class Recorder {
        var accepts: [String] = []
    }

    private static func coordinator(
        pending: [SyncthingManager.PendingFolderInfo],
        awaiting: @escaping @MainActor (String) -> Bool,
        recorder: Recorder,
        outcome: PendingShareAcceptOutcome,
        intoTarget: @escaping @MainActor () -> String? = { nil }
    ) -> ShareAcceptCoordinator {
        var environment = ShareAcceptCoordinator.Environment(
            settled: { true },
            vaultAccessible: { true },
            pendingFolders: { pending },
            autoAcceptEligible: { pending },
            accept: { folder, _ in
                recorder.accepts.append(folder.id)
                return outcome
            },
            acceptIntoTarget: { _, _ in intoTarget() },
            unignorePendingFolder: { _ in },
            ignorePendingFolder: { _ in }
        )
        environment.acceptAwaitingConfirmation = awaiting
        return ShareAcceptCoordinator(environment: environment)
    }

    @Test("A tap answered not confirmed gets the note under its own title, and no failure is recorded")
    func tapGetsATitledNote() {
        let recorder = Recorder()
        let share = SyncthingManager.PendingFolderInfo(id: "issue341-tap", label: "Vault", offeredBy: [])
        let c = Self.coordinator(pending: [share], awaiting: { _ in false }, recorder: recorder,
                                 outcome: .notConfirmed(note: SyncthingManager.acceptNotConfirmedNote))
        c.accept(share, source: .manual)
        #expect(c.noteAlert == ShareAcceptNote(title: SyncthingManager.acceptNotConfirmedTitle, message: SyncthingManager.acceptNotConfirmedNote))
        #expect(c.alertMessage == nil)
        #expect(c.pendingShareFailures[share.id] == nil)
    }

    @Test("The automatic pass leaves a share whose accept is queued, and takes it again once the engine refused it")
    func automaticPassWaitsThenResumes() {
        let recorder = Recorder()
        let share = SyncthingManager.PendingFolderInfo(id: "issue341-auto", label: "Vault", offeredBy: [])
        var awaiting = true
        let c = Self.coordinator(pending: [share], awaiting: { _ in awaiting }, recorder: recorder, outcome: .accepted)
        c.runAutomaticPass()
        #expect(recorder.accepts.isEmpty)
        awaiting = false
        c.runAutomaticPass()
        #expect(recorder.accepts == [share.id])
    }

    @Test("The picker closes on a queued accept and the share waits; a refusal stays in the picker")
    func pickerClosesOnAQueuedAccept() {
        let recorder = Recorder()
        let share = SyncthingManager.PendingFolderInfo(id: "issue341-picker", label: "Vault", offeredBy: [])
        var awaiting = false
        let c = Self.coordinator(pending: [share], awaiting: { _ in awaiting }, recorder: recorder, outcome: .accepted,
                                 intoTarget: {
                                     awaiting = true
                                     return SyncthingManager.acceptNotConfirmedNote
                                 })
        #expect(c.acceptManually(folder: share, intoTargetNamed: "Chosen") == nil)
        #expect(c.noteAlert?.title == SyncthingManager.acceptNotConfirmedTitle)

        let refusing = Self.coordinator(pending: [share], awaiting: { _ in false }, recorder: recorder, outcome: .accepted,
                                        intoTarget: { "The folder \"Chosen\" already contains files." })
        #expect(refusing.acceptManually(folder: share, intoTargetNamed: "Chosen") == "The folder \"Chosen\" already contains files.")
        #expect(refusing.noteAlert == nil)
    }

    @Test("The Hub's hand-off shows a queued accept as being added, not as a decision")
    func hubHandoffShowsAccepting() {
        let status = HubShareHandoff.status(
            folderID: "vs-a",
            configuredFolderIDs: [],
            pendingFolderIDs: ["vs-a"],
            autoAcceptEligibleIDs: [],
            recordedFailureIDs: ["vs-a"],
            awaitingConfirmationIDs: ["vs-a"],
            obsidianConnected: true
        )
        #expect(status == .accepting)
    }

    @Test("A row whose accept is queued offers neither Ignore nor another folder")
    func waitingRowOffersNoWayOut() {
        #expect(!PendingSharesView.offersIgnoreAndChooseVault(awaiting: true))
        #expect(PendingSharesView.offersIgnoreAndChooseVault(awaiting: false))
    }
}
