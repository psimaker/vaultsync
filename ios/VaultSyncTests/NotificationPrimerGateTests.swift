import Testing
import UserNotifications
@testable import VaultSync

@Suite("Notification primer gate (#69)")
struct NotificationPrimerGateTests {
    @Test("Primes only after the first completed sync with folders")
    func requiresFirstSync() {
        #expect(NotificationPrimerGate.shouldCheck(
            alreadyHandled: false,
            hasSyncFolders: true,
            hasCompletedFirstSync: true,
            otherCardVisible: false
        ))
        #expect(!NotificationPrimerGate.shouldCheck(
            alreadyHandled: false,
            hasSyncFolders: true,
            hasCompletedFirstSync: false,
            otherCardVisible: false
        ))
        #expect(!NotificationPrimerGate.shouldCheck(
            alreadyHandled: false,
            hasSyncFolders: false,
            hasCompletedFirstSync: true,
            otherCardVisible: false
        ))
    }

    @Test("Never asks twice")
    func asksOnce() {
        #expect(!NotificationPrimerGate.shouldCheck(
            alreadyHandled: true,
            hasSyncFolders: true,
            hasCompletedFirstSync: true,
            otherCardVisible: false
        ))
    }

    // One ask at a time: while the relay upsell is on the dashboard the
    // primer waits (it re-checks when the upsell is dismissed).
    @Test("Waits while another dashboard ask is visible")
    func waitsForOtherCard() {
        #expect(!NotificationPrimerGate.shouldCheck(
            alreadyHandled: false,
            hasSyncFolders: true,
            hasCompletedFirstSync: true,
            otherCardVisible: true
        ))
    }

    // The system prompt only appears for .notDetermined — for every prior
    // decision (granted, denied, provisional) the primer must retire instead
    // of promising a dialog iOS will never show.
    @Test("Presents only while permission is undecided")
    func presentsOnlyWhenUndecided() {
        #expect(NotificationPrimerGate.shouldPresent(authorizationStatus: .notDetermined))
        #expect(!NotificationPrimerGate.shouldPresent(authorizationStatus: .authorized))
        #expect(!NotificationPrimerGate.shouldPresent(authorizationStatus: .denied))
        #expect(!NotificationPrimerGate.shouldPresent(authorizationStatus: .provisional))
        #expect(!NotificationPrimerGate.shouldPresent(authorizationStatus: .ephemeral))
    }
}
