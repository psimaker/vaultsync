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
