import Foundation
import Testing
@testable import VaultSync

extension EngineBridgeSuites {
    /// Since #316 the share of a removed vault is back among the pending
    /// offers while the app runs, where the automatic accept pass looks every
    /// time the list changes. Whether the peer offers it again is the
    /// engine's part and is shown against a real peer in the bridge suite
    /// (`TestIssue316_…`); what the app does with the offer is shown here.
    @Suite("A removed vault's share that is offered again waits for a tap (#316)")
    struct Issue316RemovedShareTests {

        @MainActor
        @Test("The offer is listed, and only an explicit accept takes it")
        func theReturningOfferWaits() async throws {
            TestSupport.resetSyncthingState()
            BackgroundSyncService.lifecycleLock.withLock { $0.releaseForeground() }
            let removedKey = "syncthing.userRemovedFolderIDs"
            UserDefaults.standard.removeObject(forKey: removedKey)
            #expect(SyncBridgeService.startSyncthing(configDir: TestSupport.syncthingConfigPath()) == nil)
            let manager = SyncthingManager()
            #expect(manager.adoptRunningEngine())
            let sandbox = FileManager.default.temporaryDirectory
                .appendingPathComponent("issue316-\(UUID().uuidString)", isDirectory: true)
            defer {
                manager.stop()
                TestSupport.resetSyncthingState()
                UserDefaults.standard.removeObject(forKey: removedKey)
                try? FileManager.default.removeItem(at: sandbox)
            }

            let folderID = "issue316-\(UUID().uuidString)"
            let own = sandbox.appendingPathComponent("selected/Vault", isDirectory: true)
            #expect(manager.addFolder(id: folderID, label: "Vault", path: own.path) == nil)
            #expect(manager.removeFolder(id: folderID) == nil)

            // The peer announces the share again, and the poll hands it to
            // the manager while the app is still open.
            let offer = SyncthingManager.PendingFolderInfo(
                id: folderID,
                label: "Vault",
                offeredBy: [.init(deviceID: "PEER", name: "Hub", time: "2026-11-14T12:00:00Z")]
            )
            manager._testSetPendingFolders([offer])

            #expect(manager.actionablePendingFolders == [offer])
            #expect(manager.autoAcceptEligiblePendingFolders.isEmpty)
            // It also waits there for an app that starts later.
            #expect(SyncthingManager().autoAcceptEligiblePendingFolders.isEmpty)
            #expect(SyncthingManager().userRemovedFolderIDs.contains(folderID))

            // The tap: the vault gets a folder of its own, and the share is
            // an ordinary one again from then on.
            let again = sandbox.appendingPathComponent("selected/Vault again", isDirectory: true)
            #expect(manager.acceptPendingFolder(
                folderID: folderID,
                label: "Vault",
                path: again.path,
                root: again.deletingLastPathComponent().path,
                allowNonEmpty: false
            ) == nil)
            #expect(!manager.userRemovedFolderIDs.contains(folderID))
        }
    }
}
