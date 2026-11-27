import Foundation
import Testing
@testable import VaultSync

/// An accept still queued in the engine when the bridge's wait ends lands
/// later. The app showed it as a failure — "Could Not Accept Share" — and a
/// second tap sent a second accept of the same share (#341). The bridge now
/// answers such an accept, and every further accept of that share until it
/// lands, with its own text (`TestIssue341_…` in the bridge suite); the app
/// tells it in a note, records nothing that only a confirmed accept records,
/// and keeps the record of a removal, so the automatic pass leaves the share
/// alone. The bridge is replaced, never the engine run.
@MainActor
@Suite("An accept the engine has not confirmed is told in a note, never as a failure (#341)")
struct Issue341AcceptNotConfirmedTests {

    private static let notConfirmed = SyncBridgeService.folderAcceptNotConfirmedError

    private static func offer(_ id: String) -> SyncthingManager.PendingFolderInfo {
        SyncthingManager.PendingFolderInfo(id: id, label: "Vault", offeredBy: [])
    }

    @MainActor
    private final class Recorder {
        var accepts: [String] = []
    }

    private static func coordinator(
        pending: [SyncthingManager.PendingFolderInfo],
        recorder: Recorder,
        outcome: PendingShareAcceptOutcome
    ) -> ShareAcceptCoordinator {
        ShareAcceptCoordinator(environment: ShareAcceptCoordinator.Environment(
            settled: { true },
            vaultAccessible: { true },
            pendingFolders: { pending },
            autoAcceptEligible: { pending },
            accept: { folder, _ in
                recorder.accepts.append(folder.id)
                return outcome
            },
            acceptIntoTarget: { _, _ in nil },
            unignorePendingFolder: { _ in },
            ignorePendingFolder: { _ in }
        ))
    }

    @Test("The note is the bridge's own answer only; a refusal stays a refusal")
    func noteOnlyForTheExportedAnswer() throws {
        let note = try #require(SyncthingManager.acceptNotConfirmedNote(forAnswer: Self.notConfirmed))
        #expect(!note.isEmpty)
        #expect(SyncthingManager.acceptNotConfirmedNote(forAnswer: "folder already exists") == nil)
        #expect(SyncthingManager.acceptNotConfirmedNote(forAnswer: "modify config: config commit: timed out after 30s") == nil)
    }

    @Test("A tap on a share whose accept is not confirmed gets the note, not a failure")
    func manualTapGetsTheNote() throws {
        let note = try #require(SyncthingManager.acceptNotConfirmedNote(forAnswer: Self.notConfirmed))
        let recorder = Recorder()
        let share = Self.offer("issue341-tap")
        let c = Self.coordinator(pending: [share], recorder: recorder, outcome: .notConfirmed(note: note))

        c.accept(share, source: .manual)
        #expect(c.alertMessage == note)
        let row = try #require(c.pendingShareFailures[share.id])
        #expect(row.message == note)
        #expect(row.title != L10n.tr("Could Not Accept Share"))
    }

    @Test("The automatic pass does not send the accept of a share again while it is not confirmed")
    func automaticPassDoesNotSendItAgain() throws {
        let note = try #require(SyncthingManager.acceptNotConfirmedNote(forAnswer: Self.notConfirmed))
        let recorder = Recorder()
        let share = Self.offer("issue341-auto")
        let c = Self.coordinator(pending: [share], recorder: recorder, outcome: .notConfirmed(note: note))

        c.runAutomaticPass()
        c.runAutomaticPass()
        c.runAutomaticPass()
        #expect(recorder.accepts == [share.id])
        // A pass is no tap: the row says it, no alert.
        #expect(c.alertMessage == nil)
        #expect(c.pendingShareFailures[share.id]?.message == note)
    }

    @Test("A removed vault's record stays until its accept is confirmed")
    func removalRecordStaysUntilConfirmed() {
        let suiteName = "issue341-accept-\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suiteName)!
        defer { defaults.removePersistentDomain(forName: suiteName) }
        let folderID = "issue341-\(UUID().uuidString)"
        let manager = SyncthingManager()
        manager._testUseRemoval(bridge: { _ in nil }, defaults: defaults)
        defer {
            FolderPathReconciler.removeRel(forFolder: folderID)
            ManualShareTargetStore.forgetConsent(forFolder: folderID)
        }
        #expect(manager.removeFolder(id: folderID) == nil)
        #expect(manager.userRemovedFolderIDs.contains(folderID))

        var answer: String? = Self.notConfirmed
        manager._testUseAccept { _ in answer }
        #expect(manager.acceptPendingFolder(folderID: folderID, label: "Vault", path: "/nowhere/Vault", root: "/nowhere", allowNonEmpty: false) == Self.notConfirmed)
        #expect(manager.userRemovedFolderIDs.contains(folderID))

        // Confirmed, the explicit accept lifts the record as before (#52).
        answer = nil
        #expect(manager.acceptPendingFolder(folderID: folderID, label: "Vault", path: "/nowhere/Vault", root: "/nowhere", allowNonEmpty: false) == nil)
        #expect(!manager.userRemovedFolderIDs.contains(folderID))
    }
}
