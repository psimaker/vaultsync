import Foundation
import Testing
@testable import VaultSync

/// A removal whose configuration commit outlasted the bridge's wait went
/// ahead in the engine, but the app took the failed answer for a removal
/// that had not happened and kept no record of it: the vault's share,
/// offered again, was then accepted by the automatic pass — the removal
/// undone (#343, decision 006). The bridge is replaced here, never the
/// engine run; the record lives in defaults of the test's own.
@MainActor
@Suite("A removal the engine has not confirmed is still recorded (#343)")
struct Issue343RemovalRecordTests {

    private static let recordKey = "syncthing.userRemovedFolderIDs"

    @MainActor
    private final class Rig {
        let suiteName = "issue343-\(UUID().uuidString)"
        let defaults: UserDefaults
        let folderID = "issue343-\(UUID().uuidString)"
        let manager = SyncthingManager()
        var answer: String?
        private(set) var asked: [String] = []
        /// The record as it stood in the defaults when the bridge was asked.
        private(set) var recordWhenAsked: [String]?
        /// The folder's consent as it stood when the bridge was asked.
        private(set) var consentWhenAsked: String??

        init(answer: String?) {
            self.answer = answer
            defaults = UserDefaults(suiteName: suiteName)!
            manager._testUseRemoval(bridge: { [unowned self] id in
                self.asked.append(id)
                self.recordWhenAsked = self.defaults.stringArray(forKey: Issue343RemovalRecordTests.recordKey)
                self.consentWhenAsked = .some(ManualShareTargetStore.consentRoot(forFolder: id))
                return self.answer
            }, defaults: defaults)
        }

        var offer: SyncthingManager.PendingFolderInfo {
            SyncthingManager.PendingFolderInfo(id: folderID, label: "Vault", offeredBy: [])
        }

        var recorded: [String] {
            defaults.stringArray(forKey: Issue343RemovalRecordTests.recordKey) ?? []
        }

        /// Paths settled, as after the launch reconcile.
        func settle() {
            let token = manager._testBeginReconcile()
            manager._testFinishReconcile(token, finding: FolderPathReconciler.Outcome())
        }

        /// The shares the automatic pass accepts while `manager` holds them.
        func automaticallyAccepted(by manager: SyncthingManager) -> [String] {
            var accepted: [String] = []
            let coordinator = ShareAcceptCoordinator(environment: ShareAcceptCoordinator.Environment(
                settled: { true },
                vaultAccessible: { true },
                pendingFolders: { manager.pendingFolders },
                autoAcceptEligible: { manager.autoAcceptEligiblePendingFolders },
                accept: { folder, _ in
                    accepted.append(folder.id)
                    return .accepted
                },
                acceptIntoTarget: { _, _ in nil },
                unignorePendingFolder: { _ in },
                ignorePendingFolder: { _ in }
            ))
            coordinator.runAutomaticPass()
            return accepted
        }

        func tearDown() {
            FolderPathReconciler.removeRel(forFolder: folderID)
            ManualShareTargetStore.forgetConsent(forFolder: folderID)
            defaults.removePersistentDomain(forName: suiteName)
        }
    }

    @Test("A removal the engine has not confirmed is recorded, and its share offered again is not accepted by itself")
    func notConfirmedRemovalIsRecorded() {
        let rig = Rig(answer: SyncBridgeService.folderRemovalNotConfirmedError)
        defer { rig.tearDown() }

        #expect(rig.manager.removeFolder(id: rig.folderID) == SyncBridgeService.folderRemovalNotConfirmedError)
        #expect(rig.manager.userRemovedFolderIDs.contains(rig.folderID))
        #expect(rig.recorded.contains(rig.folderID))

        // The removal lands after the answer, and the peer offers the share
        // again: in this session, or at the next start.
        rig.manager._testSetPendingFolders([rig.offer])
        #expect(rig.automaticallyAccepted(by: rig.manager).isEmpty)

        let afterRestart = SyncthingManager()
        afterRestart._testUseRemoval(bridge: { _ in nil }, defaults: rig.defaults)
        afterRestart._testSetPendingFolders([rig.offer])
        #expect(rig.automaticallyAccepted(by: afterRestart).isEmpty)
        // It stays under Pending Shares, for an explicit tap.
        #expect(afterRestart.pendingFolders.map(\.id) == [rig.folderID])
    }

    @Test("The record is written before the engine is asked")
    func recordedBeforeTheEngineIsAsked() {
        let rig = Rig(answer: nil)
        defer { rig.tearDown() }

        #expect(rig.manager.removeFolder(id: rig.folderID) == nil)
        #expect(rig.asked == [rig.folderID])
        #expect(rig.recordWhenAsked?.contains(rig.folderID) == true)
    }

    @Test(
        "A removal the engine refused or has not confirmed keeps the record, and nothing says the folder is gone",
        arguments: ["syncthing not running", "modify config: refused", "not confirmed"]
    )
    func answerWithoutTheFolderGoneKeepsTheRecord(answer: String) {
        let text = answer == "not confirmed" ? SyncBridgeService.folderRemovalNotConfirmedError : answer
        let rig = Rig(answer: text)
        defer { rig.tearDown() }
        rig.manager._testSetFoldersWithoutTheirStorage([rig.folderID])
        FolderPathReconciler.setRel("Vault", forFolder: rig.folderID)

        #expect(rig.manager.removeFolder(id: rig.folderID) == text)
        #expect(rig.recorded.contains(rig.folderID))
        // The folder may still be configured: what describes it stays.
        #expect(rig.manager.foldersWithoutTheirStorage == [rig.folderID])
        #expect(FolderPathReconciler.loadRel()[rig.folderID] == "Vault")
    }

    @Test("Asked again once the removal has landed, the bridge finds no folder: the removal is done")
    func folderNotFoundCompletesTheRemoval() {
        let rig = Rig(answer: "folder not found")
        defer { rig.tearDown() }
        rig.manager._testSetFoldersWithoutTheirStorage([rig.folderID])
        FolderPathReconciler.setRel("Vault", forFolder: rig.folderID)

        #expect(rig.manager.removeFolder(id: rig.folderID) == nil)
        #expect(rig.recorded.contains(rig.folderID))
        #expect(rig.manager.foldersWithoutTheirStorage.isEmpty)
        #expect(FolderPathReconciler.loadRel()[rig.folderID] == nil)
    }

    @Test("A confirmed removal records the folder and forgets what described it")
    func confirmedRemoval() {
        let rig = Rig(answer: nil)
        defer { rig.tearDown() }
        rig.manager._testSetFoldersWithoutTheirStorage([rig.folderID])
        FolderPathReconciler.setRel("Vault", forFolder: rig.folderID)

        #expect(rig.manager.removeFolder(id: rig.folderID) == nil)
        #expect(rig.recorded.contains(rig.folderID))
        #expect(rig.manager.foldersWithoutTheirStorage.isEmpty)
        #expect(FolderPathReconciler.loadRel()[rig.folderID] == nil)
    }

    @Test("With a copy found in the vault's place, the remembered folder name stops standing for consent before the engine is asked")
    func copyFoundDropsTheConsentFirst() {
        let rig = Rig(answer: SyncBridgeService.folderRemovalNotConfirmedError)
        defer { rig.tearDown() }
        rig.settle()
        #expect(rig.manager.pathSettlement.settled)
        rig.manager._testSetFoldersWithoutTheirStorage([rig.folderID])
        ManualShareTargetStore.setTarget("Vault", forFolder: rig.folderID, underRoot: "/issue343-root")

        _ = rig.manager.removeFolder(id: rig.folderID)
        #expect(rig.consentWhenAsked == .some(nil))
        #expect(ManualShareTargetStore.consentRoot(forFolder: rig.folderID) == nil)
        #expect(ManualShareTargetStore.target(forFolder: rig.folderID) == "Vault")
    }

    @Test("On settled paths without a copy found, the remembered folder name keeps its consent (#52)")
    func settledWithoutACopyKeepsTheConsent() {
        let rig = Rig(answer: SyncBridgeService.folderRemovalNotConfirmedError)
        defer { rig.tearDown() }
        rig.settle()
        ManualShareTargetStore.setTarget("Vault", forFolder: rig.folderID, underRoot: "/issue343-root")

        _ = rig.manager.removeFolder(id: rig.folderID)
        #expect(ManualShareTargetStore.consentRoot(forFolder: rig.folderID) == "/issue343-root")
    }

    @Test("While the paths are not settled, the consent is dropped before the engine is asked")
    func unsettledDropsTheConsentFirst() {
        let rig = Rig(answer: SyncBridgeService.folderRemovalNotConfirmedError)
        defer { rig.tearDown() }
        #expect(!rig.manager.pathSettlement.settled)
        ManualShareTargetStore.setTarget("Vault", forFolder: rig.folderID, underRoot: "/issue343-root")

        _ = rig.manager.removeFolder(id: rig.folderID)
        #expect(rig.consentWhenAsked == .some(nil))
        #expect(ManualShareTargetStore.consentRoot(forFolder: rig.folderID) == nil)
    }
}
