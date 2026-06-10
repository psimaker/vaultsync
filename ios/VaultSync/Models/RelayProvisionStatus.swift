import Foundation

enum RelayProvisionStatus: Equatable, Sendable {
    case notAttempted
    case inProgress
    case provisioned
    case failed(reason: String)

    var stateKey: String {
        switch self {
        case .notAttempted:
            return "not_attempted"
        case .inProgress:
            return "in_progress"
        case .provisioned:
            return "provisioned"
        case .failed:
            return "failed"
        }
    }

    var summary: String {
        switch self {
        case .notAttempted:
            return L10n.tr("Not attempted")
        case .inProgress:
            return L10n.tr("In progress")
        case .provisioned:
            return L10n.tr("Provisioned")
        case .failed:
            return L10n.tr("Failed")
        }
    }

    var failureReason: String? {
        guard case .failed(let reason) = self else { return nil }
        return reason
    }
}

enum APNsRegistrationStatus: Equatable, Sendable {
    case notAttempted
    case registered
    case failed(reason: String)

    var summary: String {
        switch self {
        case .notAttempted:
            return L10n.tr("Not attempted")
        case .registered:
            return L10n.tr("Registered")
        case .failed:
            return L10n.tr("Failed")
        }
    }
}

enum APNsRegistrationStore {
    private static let statusKey = "apns-registration-status"
    private static let reasonKey = "apns-registration-failure-reason"
    private static let updatedAtKey = "apns-registration-updated-at"
    private static let lastSuccessAtKey = "apns-registration-last-success-at"
    private static let lastFailureAtKey = "apns-registration-last-failure-at"

    static let statusDidChangeNotification = Notification.Name("APNsRegistrationStatusDidChange")
    static let tokenDidChangeNotification = Notification.Name("APNsDeviceTokenDidChange")
    
    struct Snapshot: Equatable, Sendable {
        let updatedAt: Date?
        let lastSuccessAt: Date?
        let lastFailureAt: Date?
    }

    static func current() -> APNsRegistrationStatus {
        let defaults = UserDefaults.standard
        let status = defaults.string(forKey: statusKey) ?? "not_attempted"
        switch status {
        case "registered":
            return .registered
        case "failed":
            let reason = defaults.string(forKey: reasonKey) ?? L10n.tr("Unknown APNs registration error")
            return .failed(reason: reason)
        default:
            return .notAttempted
        }
    }
    
    static func snapshot() -> Snapshot {
        let defaults = UserDefaults.standard
        return Snapshot(
            updatedAt: defaults.object(forKey: updatedAtKey) as? Date,
            lastSuccessAt: defaults.object(forKey: lastSuccessAtKey) as? Date,
            lastFailureAt: defaults.object(forKey: lastFailureAtKey) as? Date
        )
    }

    static func markRegistered() {
        let defaults = UserDefaults.standard
        let now = Date()
        defaults.set("registered", forKey: statusKey)
        defaults.removeObject(forKey: reasonKey)
        defaults.set(now, forKey: updatedAtKey)
        defaults.set(now, forKey: lastSuccessAtKey)
        postStatusUpdate()
    }

    static func markFailed(reason: String) {
        let defaults = UserDefaults.standard
        let now = Date()
        defaults.set("failed", forKey: statusKey)
        defaults.set(reason, forKey: reasonKey)
        defaults.set(now, forKey: updatedAtKey)
        defaults.set(now, forKey: lastFailureAtKey)
        postStatusUpdate()
    }

    static func markNotAttempted() {
        let defaults = UserDefaults.standard
        defaults.set(Date(), forKey: updatedAtKey)
        defaults.set("not_attempted", forKey: statusKey)
        defaults.removeObject(forKey: reasonKey)
        postStatusUpdate()
    }
    
    static func postTokenDidChange() {
        NotificationCenter.default.post(name: tokenDidChangeNotification, object: nil)
    }

    private static func postStatusUpdate() {
        NotificationCenter.default.post(name: statusDidChangeNotification, object: nil)
    }
}

enum RelayTriggerStore {
    private static let lastReceivedAtKey = "relay-last-trigger-received-at"
    private static let receivedHistoryKey = "relay-trigger-received-history-v1"
    /// Rolling cap on stored arrival timestamps. 200 entries cover weeks of
    /// realistic delivery volume while keeping the UserDefaults blob tiny.
    private static let historyLimit = 200
    static let triggerDidChangeNotification = Notification.Name("RelayTriggerDidChange")

    /// A real wake-up reached this device — the silent push the relay delivers
    /// when the server helper triggers it (a vault change, or the helper's
    /// startup-announce). This is the single signal that drives
    /// `relayDeliveryConfirmed` ("wake-ups are being delivered"). It is the ONLY
    /// delivery path: the app itself no longer sends any trigger, so every silent
    /// push is a genuine delivery and needs no attribution heuristic.
    static func markReceived(date: Date = Date()) {
        UserDefaults.standard.set(date, forKey: lastReceivedAtKey)
        var history = receivedHistory()
        history.append(date)
        if history.count > historyLimit {
            history.removeFirst(history.count - historyLimit)
        }
        UserDefaults.standard.set(history, forKey: receivedHistoryKey)
        NotificationCenter.default.post(name: triggerDidChangeNotification, object: nil)
    }

    static func lastReceivedAt() -> Date? {
        UserDefaults.standard.object(forKey: lastReceivedAtKey) as? Date
    }

    /// All recorded wake-up arrivals, oldest first, capped at `historyLimit`.
    /// Purely local diagnostics — nothing is reported anywhere.
    static func receivedHistory() -> [Date] {
        UserDefaults.standard.array(forKey: receivedHistoryKey) as? [Date] ?? []
    }

    /// Number of wake-ups that arrived within the trailing interval. Drives the
    /// diagnostics counter that makes delivery (or its absence) visible —
    /// "Never"/"3 days ago" alone hides how much iOS is actually letting through.
    static func receivedCount(within interval: TimeInterval, now: Date = Date()) -> Int {
        let cutoff = now.addingTimeInterval(-interval)
        return receivedHistory().filter { $0 >= cutoff }.count
    }
}
