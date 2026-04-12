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
        let status: APNsRegistrationStatus
        let failureReason: String?
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
        let status = current()
        let failureReason: String?
        if case .failed(let reason) = status {
            failureReason = reason
        } else {
            failureReason = nil
        }
        return Snapshot(
            status: status,
            failureReason: failureReason,
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
    static let triggerDidChangeNotification = Notification.Name("RelayTriggerDidChange")

    static func markReceived(date: Date = Date()) {
        UserDefaults.standard.set(date, forKey: lastReceivedAtKey)
        NotificationCenter.default.post(name: triggerDidChangeNotification, object: nil)
    }

    static func lastReceivedAt() -> Date? {
        UserDefaults.standard.object(forKey: lastReceivedAtKey) as? Date
    }
}
