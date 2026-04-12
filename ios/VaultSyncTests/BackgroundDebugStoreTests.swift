import Foundation
import Testing
@testable import VaultSync

@Suite("Background Debug Persistence")
struct BackgroundDebugStoreTests {
    @Test("Keeps newest entries within limit")
    func keepsNewestEntriesWithinLimit() {
        let defaults = TestSupport.makeIsolatedDefaults(label: "background-debug-limit")
        let store = BackgroundDebugStore(
            defaults: defaults,
            storageKey: "background-debug-limit-key",
            limit: 3
        )

        store.record(area: "push", message: "one", at: Date(timeIntervalSince1970: 1))
        store.record(area: "background", message: "two", at: Date(timeIntervalSince1970: 2))
        store.record(area: "background", message: "three", at: Date(timeIntervalSince1970: 3))
        store.record(area: "push", message: "four", at: Date(timeIntervalSince1970: 4))

        let entries = store.entries()
        #expect(entries.count == 3)
        #expect(entries.map(\.message) == ["two", "three", "four"])
    }

    @Test("Clear removes persisted entries")
    func clearRemovesEntries() {
        let defaults = TestSupport.makeIsolatedDefaults(label: "background-debug-clear")
        let store = BackgroundDebugStore(
            defaults: defaults,
            storageKey: "background-debug-clear-key"
        )

        store.record(area: "push", message: "received")
        #expect(!store.entries().isEmpty)

        store.clear()
        #expect(store.entries().isEmpty)
    }
}
