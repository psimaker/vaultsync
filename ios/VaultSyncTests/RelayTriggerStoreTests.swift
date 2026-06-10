import Foundation
import Testing
@testable import VaultSync

@Suite("Relay Trigger History", .serialized)
struct RelayTriggerStoreTests {
    @Test("markReceived records history and receivedCount windows it")
    func historyAndWindowedCount() {
        TestSupport.resetRelayState()

        let now = Date()
        RelayTriggerStore.markReceived(date: now.addingTimeInterval(-8 * 24 * 60 * 60))
        RelayTriggerStore.markReceived(date: now.addingTimeInterval(-3 * 24 * 60 * 60))
        RelayTriggerStore.markReceived(date: now.addingTimeInterval(-60))

        #expect(RelayTriggerStore.receivedHistory().count == 3)
        // The 8-day-old arrival falls outside the 7-day window.
        #expect(RelayTriggerStore.receivedCount(within: 7 * 24 * 60 * 60, now: now) == 2)
        #expect(RelayTriggerStore.lastReceivedAt() != nil)

        TestSupport.resetRelayState()
        #expect(RelayTriggerStore.receivedHistory().isEmpty)
        #expect(RelayTriggerStore.receivedCount(within: 7 * 24 * 60 * 60, now: now) == 0)
    }

    @Test("history is capped to the rolling limit, dropping oldest entries")
    func historyCap() {
        TestSupport.resetRelayState()

        let base = Date(timeIntervalSince1970: 1_000_000)
        for offset in 0..<205 {
            RelayTriggerStore.markReceived(date: base.addingTimeInterval(TimeInterval(offset)))
        }

        let history = RelayTriggerStore.receivedHistory()
        #expect(history.count == 200)
        #expect(history.first == base.addingTimeInterval(5))
        #expect(history.last == base.addingTimeInterval(204))

        TestSupport.resetRelayState()
    }
}
