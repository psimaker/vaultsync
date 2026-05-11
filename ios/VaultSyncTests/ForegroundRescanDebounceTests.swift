import Foundation
import Testing
@testable import VaultSync

@Suite("Foreground rescan debounce")
struct ForegroundRescanDebounceTests {
    @Test("Skips when the app was never backgrounded in this session")
    func skipsWhenLastBackgroundedAtIsNil() {
        let now = Date(timeIntervalSince1970: 1_000)
        #expect(
            BackgroundSyncService.shouldRescanOnForeground(
                now: now,
                lastBackgroundedAt: nil,
                threshold: 5
            ) == false
        )
    }

    @Test("Fires when backgrounded longer than the threshold")
    func firesAboveThreshold() {
        let now = Date(timeIntervalSince1970: 1_010)
        let backgroundedAt = Date(timeIntervalSince1970: 1_000)
        #expect(
            BackgroundSyncService.shouldRescanOnForeground(
                now: now,
                lastBackgroundedAt: backgroundedAt,
                threshold: 5
            ) == true
        )
    }

    @Test("Skips when backgrounded shorter than the threshold")
    func skipsBelowThreshold() {
        let now = Date(timeIntervalSince1970: 1_002)
        let backgroundedAt = Date(timeIntervalSince1970: 1_000)
        #expect(
            BackgroundSyncService.shouldRescanOnForeground(
                now: now,
                lastBackgroundedAt: backgroundedAt,
                threshold: 5
            ) == false
        )
    }

    @Test("Fires exactly at the threshold boundary")
    func firesAtThresholdBoundary() {
        let now = Date(timeIntervalSince1970: 1_005)
        let backgroundedAt = Date(timeIntervalSince1970: 1_000)
        #expect(
            BackgroundSyncService.shouldRescanOnForeground(
                now: now,
                lastBackgroundedAt: backgroundedAt,
                threshold: 5
            ) == true
        )
    }
}
