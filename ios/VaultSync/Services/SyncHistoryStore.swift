import Foundation

struct SyncHistoryStore {
    struct Snapshot: Codable, Sendable {
        var globalLastSync: Date?
        var lastSyncByFolder: [String: Date]
    }

    private let defaults: UserDefaults
    private let storageKey: String

    init(
        defaults: UserDefaults = .standard,
        storageKey: String = "syncthing.syncHistory.v1"
    ) {
        self.defaults = defaults
        self.storageKey = storageKey
    }

    func load() -> Snapshot {
        guard let data = defaults.data(forKey: storageKey),
              let decoded = try? JSONDecoder().decode(Snapshot.self, from: data) else {
            return Snapshot(globalLastSync: nil, lastSyncByFolder: [:])
        }
        return decoded
    }

    func save(globalLastSync: Date?, lastSyncByFolder: [String: Date]) {
        let snapshot = Snapshot(
            globalLastSync: globalLastSync,
            lastSyncByFolder: lastSyncByFolder
        )
        guard let data = try? JSONEncoder().encode(snapshot) else { return }
        defaults.set(data, forKey: storageKey)
    }
}
