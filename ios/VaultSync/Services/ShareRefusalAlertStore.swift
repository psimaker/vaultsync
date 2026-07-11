import Foundation

/// Sidecar: which share-refusal modal alerts have already been presented,
/// keyed by Syncthing folder ID → the refusal reason last alerted (#95).
/// A share that stays refused (e.g. no safe location under a vault-as-root
/// container, #45 follow-up) is re-refused by the automatic accept pass on
/// every launch — without this record the same modal re-fired each start.
/// Only the MODAL is gated: the inline share row keeps showing the failure
/// every session (doctrine 002 — pause, explain, let the user act). A new
/// reason alerts again (the situation changed), and a successful accept
/// clears the record. The reason key is the refusal message string, so a
/// language switch re-alerts once — accepted as harmless. Same
/// serialized-queue UserDefaults pattern as ManualShareTargetStore; entries
/// are never pruned (one string per persistently refused share, ever).
enum ShareRefusalAlertStore {
    private static let storeKey = "vaultsync.shareRefusalAlertsShown"
    private static let queue = DispatchQueue(label: "eu.vaultsync.sharerefusalalerts.sidecar")

    /// Pure core (unit-testable): present only when this exact reason has
    /// not been alerted for this folder yet.
    nonisolated static func shouldPresent(
        folderID: String,
        reason: String,
        shown: [String: String]
    ) -> Bool {
        shown[folderID] != reason
    }

    static func shouldPresentAlert(folderID: String, reason: String) -> Bool {
        queue.sync {
            shouldPresent(folderID: folderID, reason: reason, shown: load())
        }
    }

    /// Atomic read-modify-write.
    static func markPresented(folderID: String, reason: String) {
        queue.sync {
            var map = load()
            guard map[folderID] != reason else { return }
            map[folderID] = reason
            UserDefaults.standard.set(map, forKey: storeKey)
        }
    }

    /// A successful accept clears the record — a future refusal is a new
    /// situation and may alert again.
    static func clear(folderID: String) {
        queue.sync {
            var map = load()
            guard map.removeValue(forKey: folderID) != nil else { return }
            UserDefaults.standard.set(map, forKey: storeKey)
        }
    }

    private static func load() -> [String: String] {
        UserDefaults.standard.dictionary(forKey: storeKey) as? [String: String] ?? [:]
    }
}
