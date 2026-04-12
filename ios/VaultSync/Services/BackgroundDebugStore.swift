import Foundation

struct BackgroundDebugStore {
    struct Entry: Codable, Equatable, Identifiable, Sendable {
        let timestamp: Date
        let area: String
        let message: String

        var id: String {
            "\(timestamp.timeIntervalSince1970)|\(area)|\(message)"
        }
    }

    private let defaults: UserDefaults
    private let storageKey: String
    private let limit: Int

    static let didChangeNotification = Notification.Name("BackgroundDebugStoreDidChange")

    init(
        defaults: UserDefaults = .standard,
        storageKey: String = "background-debug-log.v1",
        limit: Int = 80
    ) {
        self.defaults = defaults
        self.storageKey = storageKey
        self.limit = max(1, limit)
    }

    func entries() -> [Entry] {
        guard let data = defaults.data(forKey: storageKey),
              let decoded = try? JSONDecoder().decode([Entry].self, from: data) else {
            return []
        }
        return decoded
    }

    func record(area: String, message: String, at timestamp: Date = Date()) {
        var existing = entries()
        existing.append(Entry(timestamp: timestamp, area: area, message: message))
        if existing.count > limit {
            existing.removeFirst(existing.count - limit)
        }
        guard let data = try? JSONEncoder().encode(existing) else { return }
        defaults.set(data, forKey: storageKey)
        NotificationCenter.default.post(name: Self.didChangeNotification, object: nil)
    }

    func clear() {
        defaults.removeObject(forKey: storageKey)
        NotificationCenter.default.post(name: Self.didChangeNotification, object: nil)
    }
}
