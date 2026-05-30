import Foundation
import Testing
@testable import VaultSync

@Suite("Conflict notification suppression")
struct ConflictNotificationActionTests {

    @Test("First conflicts (none surfaced yet) alert")
    func firstConflictsAlert() {
        #expect(BackgroundSyncService.conflictNotificationAction(currentCount: 28, lastNotifiedCount: 0) == .alert)
        #expect(BackgroundSyncService.conflictNotificationAction(currentCount: 1, lastNotifiedCount: 0) == .alert)
    }

    @Test("Unchanged count is suppressed — the core of issue #10")
    func unchangedCountSuppressed() {
        #expect(BackgroundSyncService.conflictNotificationAction(currentCount: 28, lastNotifiedCount: 28) == .suppress)
        #expect(BackgroundSyncService.conflictNotificationAction(currentCount: 1, lastNotifiedCount: 1) == .suppress)
    }

    @Test("A rising count alerts (genuinely new conflicts)")
    func risingCountAlerts() {
        #expect(BackgroundSyncService.conflictNotificationAction(currentCount: 29, lastNotifiedCount: 28) == .alert)
    }

    @Test("A falling-but-nonzero count refreshes quietly")
    func fallingCountUpdatesQuietly() {
        #expect(BackgroundSyncService.conflictNotificationAction(currentCount: 25, lastNotifiedCount: 28) == .updateQuiet)
    }

    @Test("No conflicts left clears the banner")
    func zeroClears() {
        #expect(BackgroundSyncService.conflictNotificationAction(currentCount: 0, lastNotifiedCount: 28) == .clear)
        #expect(BackgroundSyncService.conflictNotificationAction(currentCount: 0, lastNotifiedCount: 0) == .clear)
    }

    @Test("Negative/garbage current count is treated as cleared, never as a post")
    func negativeCurrentClears() {
        #expect(BackgroundSyncService.conflictNotificationAction(currentCount: -3, lastNotifiedCount: 5) == .clear)
    }
}

@Suite("Folder settlement classification")
struct FolderSettlementTests {

    @Test("Idle with no pending work is idle")
    func idleNoWork() {
        #expect(BackgroundSyncService.folderSettlement(state: "idle", needFiles: 0, needBytes: 0, inProgressBytes: 0) == .idle)
    }

    @Test("Idle but with pending work is still active (the scan→sync gap)")
    func idleWithPendingIsActive() {
        #expect(BackgroundSyncService.folderSettlement(state: "idle", needFiles: 3, needBytes: 0, inProgressBytes: 0) == .active)
        #expect(BackgroundSyncService.folderSettlement(state: "idle", needFiles: 0, needBytes: 4096, inProgressBytes: 0) == .active)
        #expect(BackgroundSyncService.folderSettlement(state: "idle", needFiles: 0, needBytes: 0, inProgressBytes: 512) == .active)
    }

    @Test("Scanning and syncing are active")
    func scanningSyncingActive() {
        #expect(BackgroundSyncService.folderSettlement(state: "scanning", needFiles: 0, needBytes: 0, inProgressBytes: 0) == .active)
        #expect(BackgroundSyncService.folderSettlement(state: "syncing", needFiles: 0, needBytes: 0, inProgressBytes: 0) == .active)
    }

    @Test("Error is terminal even with outstanding work — lets the deadline loop break early")
    func errorIsTerminal() {
        #expect(BackgroundSyncService.folderSettlement(state: "error", needFiles: 0, needBytes: 0, inProgressBytes: 0) == .errored)
        #expect(BackgroundSyncService.folderSettlement(state: "error", needFiles: 12, needBytes: 9000, inProgressBytes: 1) == .errored)
    }
}
