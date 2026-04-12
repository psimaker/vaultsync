import Foundation

enum SyncUserErrorCategory: String, Sendable {
    case network
    case auth
    case config
    case permission
    case validation
    case syncthingNotRunning = "syncthing_not_running"
    case relayUnreachable = "relay_unreachable"
    case relayProvision = "relay_provision"
    case fileAccess = "file_access"
    case unknown
}

struct SyncUserError: Identifiable, Equatable, Sendable {
    let category: SyncUserErrorCategory
    let title: String
    let message: String
    let remediation: String
    let technicalDetails: String?

    var id: String {
        "\(category.rawValue)|\(title)|\(message)|\(remediation)|\(technicalDetails ?? "")"
    }

    var userVisibleDescription: String {
        var lines: [String] = [message]
        if !remediation.isEmpty {
            lines.append(L10n.fmt("How to fix: %@", remediation))
        }
        return lines.joined(separator: "\n\n")
    }

    static func from(rawMessage: String, fallbackTitle: String = L10n.tr("Sync Error")) -> SyncUserError {
        let normalized = rawMessage.lowercased()

        if normalized.contains("syncthing not running") || normalized.contains("not running") {
            return SyncUserError(
                category: .syncthingNotRunning,
                title: L10n.tr("Sync Engine Not Running"),
                message: L10n.tr("VaultSync cannot talk to Syncthing right now."),
                remediation: L10n.tr("Keep the app open for a moment and retry. If this persists, restart VaultSync."),
                technicalDetails: rawMessage
            )
        }

        if isRelayConnectivityError(normalized) {
            return SyncUserError(
                category: .relayUnreachable,
                title: L10n.tr("Relay Unreachable"),
                message: L10n.tr("VaultSync could not reach the Cloud Relay service."),
                remediation: L10n.tr("Check your internet connection and try the relay health check again in Settings."),
                technicalDetails: rawMessage
            )
        }

        if isNetworkError(normalized) {
            return SyncUserError(
                category: .network,
                title: L10n.tr("Network Error"),
                message: L10n.tr("VaultSync could not complete the request due to a network issue."),
                remediation: L10n.tr("Check your connection and retry."),
                technicalDetails: rawMessage
            )
        }

        if isPermissionError(normalized) {
            return SyncUserError(
                category: .permission,
                title: L10n.tr("Permission Required"),
                message: L10n.tr("VaultSync does not have the required permission for this action."),
                remediation: L10n.tr("Open iOS Settings -> VaultSync and check that all permissions are enabled, then retry."),
                technicalDetails: rawMessage
            )
        }

        if isAuthError(normalized) {
            return SyncUserError(
                category: .auth,
                title: L10n.tr("Authentication Error"),
                message: L10n.tr("VaultSync could not verify this request."),
                remediation: L10n.tr("Check your subscription status in Settings and retry. If this persists, restart VaultSync."),
                technicalDetails: rawMessage
            )
        }

        if isValidationError(normalized) {
            return SyncUserError(
                category: .validation,
                title: L10n.tr("Invalid Input"),
                message: L10n.tr("Some entered data is invalid or incomplete."),
                remediation: L10n.tr("Review the value and try again."),
                technicalDetails: rawMessage
            )
        }

        if isConfigError(normalized) {
            return SyncUserError(
                category: .config,
                title: L10n.tr("Configuration Error"),
                message: L10n.tr("VaultSync found a configuration problem."),
                remediation: L10n.tr("Review the affected device/folder setup in the app and retry."),
                technicalDetails: rawMessage
            )
        }

        return SyncUserError(
            category: .unknown,
            title: fallbackTitle,
            message: L10n.tr("VaultSync reported an unexpected error."),
            remediation: L10n.tr("Retry the action. If it keeps failing, restart the app and check Settings diagnostics."),
            technicalDetails: rawMessage
        )
    }

    static func from(error: any Error, fallbackTitle: String = L10n.tr("Sync Error")) -> SyncUserError {
        from(rawMessage: error.localizedDescription, fallbackTitle: fallbackTitle)
    }

    static func fromFolderStatus(
        reason: String?,
        message: String?,
        path: String?
    ) -> SyncUserError {
        let normalizedReason = (reason ?? "").lowercased()
        let detail = message ?? L10n.tr("Folder is currently in an error state.")
        let pathHint = path.map { L10n.fmt(" (%@)", $0) } ?? ""

        switch normalizedReason {
        case "permission_denied":
            return SyncUserError(
                category: .permission,
                title: L10n.tr("Folder Permission Error"),
                message: L10n.fmt("VaultSync cannot access this folder%@.", pathHint),
                remediation: L10n.tr("Reconnect Obsidian access or adjust folder permissions on the host device."),
                technicalDetails: detail
            )
        case "folder_path_missing":
            return SyncUserError(
                category: .config,
                title: L10n.tr("Folder Path Missing"),
                message: L10n.fmt("The folder path no longer exists%@.", pathHint),
                remediation: L10n.tr("Recreate or reselect the folder, then trigger a rescan."),
                technicalDetails: detail
            )
        case "folder_not_found":
            return SyncUserError(
                category: .config,
                title: L10n.tr("Folder Not Configured"),
                message: L10n.tr("Syncthing no longer has this folder configured."),
                remediation: L10n.tr("Remove and re-share the folder from your desktop device."),
                technicalDetails: detail
            )
        default:
            return SyncUserError(
                category: .config,
                title: L10n.tr("Folder Sync Error"),
                message: detail,
                remediation: L10n.tr("Check folder sharing, connectivity, and permissions, then retry."),
                technicalDetails: detail
            )
        }
    }

    static func relayProvisionFailed(reason: String) -> SyncUserError {
        let normalized = reason.lowercased()
        if isRelayConnectivityError(normalized) {
            return SyncUserError(
                category: .relayUnreachable,
                title: L10n.tr("Relay Unreachable"),
                message: L10n.tr("Cloud Relay provisioning could not contact the relay backend."),
                remediation: L10n.tr("Check internet connectivity and retry provisioning."),
                technicalDetails: reason
            )
        }
        if normalized.contains("429") || normalized.contains("rate") {
            return SyncUserError(
                category: .relayProvision,
                title: L10n.tr("Relay Rate Limited"),
                message: L10n.tr("Cloud Relay provisioning is temporarily rate limited."),
                remediation: L10n.tr("Wait a moment and retry."),
                technicalDetails: reason
            )
        }
        return SyncUserError(
            category: .relayProvision,
            title: L10n.tr("Relay Provisioning Failed"),
            message: L10n.tr("Cloud Relay provisioning did not complete."),
            remediation: L10n.tr("Retry provisioning from Settings. If this persists, verify subscription status."),
            technicalDetails: reason
        )
    }

    static func apnsRegistrationFailed(reason: String) -> SyncUserError {
        SyncUserError(
            category: .permission,
            title: L10n.tr("Push Registration Failed"),
            message: L10n.tr("iOS did not provide a push token required for instant sync."),
            remediation: L10n.tr("Enable notifications for VaultSync in iOS Settings -> Notifications -> VaultSync, then restart the app."),
            technicalDetails: reason
        )
    }

    // MARK: - Troubleshooting URL Routing

    private static let troubleshootingBaseURL = "https://github.com/psimaker/vaultsync/blob/main/docs/troubleshooting.md"

    /// Map an error to its most relevant troubleshooting documentation anchor.
    static func troubleshootingURL(for error: SyncUserError) -> URL? {
        let details = "\(error.message) \(error.remediation) \(error.technicalDetails ?? "")".lowercased()
        let anchor: String
        switch error.category {
        case .syncthingNotRunning:
            anchor = "syncthing-not-running"
        case .relayUnreachable, .relayProvision, .network:
            anchor = "relay-unreachable"
        case .auth:
            anchor = "wrong-syncthing-api-key-in-notify"
        case .permission, .fileAccess:
            if details.contains("apns") || details.contains("notification") || details.contains("push") {
                anchor = "apns-not-registered"
            } else {
                anchor = "bookmark-access-expired"
            }
        case .config, .validation:
            if details.contains("pending") || details.contains("share") {
                anchor = "no-pending-shares-appear"
            } else if details.contains("background") {
                anchor = "background-sync-not-working"
            } else {
                anchor = "obsidian-folder-not-found"
            }
        case .unknown:
            anchor = "background-sync-not-working"
        }
        return URL(string: "\(troubleshootingBaseURL)#\(anchor)")
    }

    /// Convenience: map a raw error string to a troubleshooting URL.
    static func troubleshootingURL(forRawError rawError: String) -> URL? {
        troubleshootingURL(for: from(rawMessage: rawError))
    }

    /// Direct anchor-based troubleshooting URL for callers that already know the target section.
    static func troubleshootingURL(anchor: String) -> URL? {
        URL(string: "\(troubleshootingBaseURL)#\(anchor)")
    }

    // MARK: - Error Classification

    private static func isNetworkError(_ normalized: String) -> Bool {
        normalized.contains("network")
            || normalized.contains("connection refused")
            || normalized.contains("timed out")
            || normalized.contains("timeout")
            || normalized.contains("offline")
            || normalized.contains("host down")
            || normalized.contains("dns")
            || normalized.contains("unreachable")
    }

    private static func isRelayConnectivityError(_ normalized: String) -> Bool {
        normalized.contains("relay")
            && (normalized.contains("network")
                || normalized.contains("unreachable")
                || normalized.contains("timed out")
                || normalized.contains("timeout")
                || normalized.contains("connection refused"))
    }

    private static func isAuthError(_ normalized: String) -> Bool {
        normalized.contains("certificate")
            || normalized.contains("x509")
            || normalized.contains("tls")
            || normalized.contains("unauthorized")
            || normalized.contains("forbidden")
            || normalized.contains("authentication")
    }

    private static func isPermissionError(_ normalized: String) -> Bool {
        normalized.contains("permission denied")
            || normalized.contains("operation not permitted")
            || normalized.contains("access denied")
            || normalized.contains("not authorized")
            || normalized.contains("security scoped")
    }

    private static func isValidationError(_ normalized: String) -> Bool {
        normalized.contains("invalid input")
            || normalized.contains("invalid value")
            || normalized.contains("invalid format")
            || normalized.contains("is required")
            || normalized.contains("field required")
            || normalized.contains("must be")
    }

    private static func isConfigError(_ normalized: String) -> Bool {
        normalized.contains("already exists")
            || normalized.contains("cannot add own")
            || normalized.contains("folder not found")
            || normalized.contains("path")
            || normalized.contains("config")
    }
}
