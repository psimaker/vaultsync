import Foundation
import Testing
@testable import VaultSync

/// The completion branch of `updateWidgetSyncMetrics` used to derive its tier
/// while the widget sync session was still open (`activeWidgetSyncStart`
/// non-nil), so it persisted a stale `.syncing` snapshot that the poll-end
/// write immediately replaced — two snapshot writes and widget reloads per
/// sync completion (#77).
@Suite("Widget completion write persists the settled tier (#77)")
struct WidgetCompletionWriteTests {
    @MainActor
    private func makeManager() -> SyncthingManager {
        let defaults = TestSupport.makeIsolatedDefaults(label: "WidgetCompletionWrite")
        let store = SyncHistoryStore(defaults: defaults, storageKey: "history")
        // A recent last sync keeps the stale-sync warning out of the cascade —
        // these tests pin the syncing → settled transition, nothing else.
        store.save(globalLastSync: Date(), lastSyncByFolder: [:])
        let manager = SyncthingManager(syncHistoryStore: store)
        manager._testSetLastBackgroundSyncOutcome(nil)
        manager._testSetRunning(true)
        manager._testSetFolders([
            SyncthingManager.FolderInfo(
                id: "vault-a",
                label: "Vault A",
                path: "/tmp/widget77/vault-a",
                type: "sendreceive",
                paused: false,
                deviceIDs: []
            ),
        ])
        return manager
    }

    private func status(state: String) -> SyncthingManager.FolderStatusInfo {
        SyncthingManager.FolderStatusInfo(payload: .init(
            state: state,
            stateChanged: "2026-07-07T10:00:00Z",
            completionPct: 100,
            globalBytes: 0,
            globalFiles: 0,
            localBytes: 0,
            localFiles: 0,
            needBytes: 0,
            needFiles: 0,
            inProgressBytes: 0,
            errorReason: nil,
            errorMessage: nil,
            errorPath: nil,
            errorChanged: nil
        ))
    }

    @MainActor
    @Test("Completion persists the settled tier, never a stale .syncing")
    func completionPersistsSettledTier() {
        let manager = makeManager()
        let syncing = ["vault-a": status(state: "syncing")]
        let idle = ["vault-a": status(state: "idle")]

        manager._testUpdateWidgetSyncMetrics(previousStatuses: [:], newStatuses: syncing)
        manager._testUpdateWidgetSyncMetrics(previousStatuses: syncing, newStatuses: idle)

        #expect(manager._testLastWrittenWidgetSnapshot()?.status == SyncStatus.synced.wireValue)
    }

    @MainActor
    @Test("The poll-end write after a completion dedupes to a no-op")
    func pollEndWriteDedupes() {
        let manager = makeManager()
        let syncing = ["vault-a": status(state: "syncing")]
        let idle = ["vault-a": status(state: "idle")]

        manager._testUpdateWidgetSyncMetrics(previousStatuses: [:], newStatuses: syncing)
        manager._testUpdateWidgetSyncMetrics(previousStatuses: syncing, newStatuses: idle)
        let afterCompletion = manager._testLastWrittenWidgetSnapshot()
        #expect(afterCompletion != nil)

        // The poll publishes the fresh statuses right after the metrics
        // update, then writes without an override — the write that used to
        // correct the stale .syncing. It must now find an identical snapshot
        // and skip (one write, one widget reload per completion).
        manager._testSetFolderStatuses(idle)
        manager._testWriteWidgetSnapshot()
        #expect(manager._testLastWrittenWidgetSnapshot() == afterCompletion)
    }
}
