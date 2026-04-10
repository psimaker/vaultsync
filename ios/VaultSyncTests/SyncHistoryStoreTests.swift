import Foundation
import Testing
@testable import VaultSync

@Suite("Sync History Persistence")
struct SyncHistoryStoreTests {
    @Test("Persists and reloads sync history snapshot")
    func saveAndLoadRoundTrip() {
        let defaults = TestSupport.makeIsolatedDefaults(label: "sync-history-roundtrip")
        let storageKey = "sync-history-roundtrip-key"
        let store = SyncHistoryStore(defaults: defaults, storageKey: storageKey)

        let now = Date()
        let folderDate = now.addingTimeInterval(-60)
        store.save(
            globalLastSync: now,
            lastSyncByFolder: [
                "vault-a": folderDate,
                "vault-b": now,
            ]
        )

        let loaded = store.load()
        #expect(loaded.globalLastSync == now)
        #expect(loaded.lastSyncByFolder["vault-a"] == folderDate)
        #expect(loaded.lastSyncByFolder["vault-b"] == now)
    }

    @Test("Falls back to empty snapshot for malformed persisted data")
    func loadFallsBackOnMalformedData() {
        let defaults = TestSupport.makeIsolatedDefaults(label: "sync-history-malformed")
        let storageKey = "sync-history-malformed-key"
        defaults.set(Data("not-json".utf8), forKey: storageKey)

        let store = SyncHistoryStore(defaults: defaults, storageKey: storageKey)
        let loaded = store.load()

        #expect(loaded.globalLastSync == nil)
        #expect(loaded.lastSyncByFolder.isEmpty)
    }
}
