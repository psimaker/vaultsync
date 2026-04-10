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
            lines.append("How to fix: \(remediation)")
        }
        return lines.joined(separator: "\n\n")
    }

    static func from(rawMessage: String, fallbackTitle: String = "Sync Error") -> SyncUserError {
        let normalized = rawMessage.lowercased()

        if normalized.contains("syncthing not running") || normalized.contains("not running") {
            return SyncUserError(
                category: .syncthingNotRunning,
                title: "Sync Engine Not Running",
                message: "VaultSync cannot talk to Syncthing right now.",
                remediation: "Keep the app open for a moment and retry. If this persists, restart VaultSync.",
                technicalDetails: rawMessage
            )
        }

        if isRelayConnectivityError(normalized) {
            return SyncUserError(
                category: .relayUnreachable,
                title: "Relay Unreachable",
                message: "VaultSync could not reach the Cloud Relay service.",
                remediation: "Check your internet connection and try the relay health check again in Settings.",
                technicalDetails: rawMessage
            )
        }

        if isNetworkError(normalized) {
            return SyncUserError(
                category: .network,
                title: "Network Error",
                message: "VaultSync could not complete the request due to a network issue.",
                remediation: "Check your connection and retry.",
                technicalDetails: rawMessage
            )
        }

        if isPermissionError(normalized) {
            return SyncUserError(
                category: .permission,
                title: "Permission Required",
                message: "VaultSync does not have the required permission for this action.",
                remediation: "Grant folder/notification permissions and retry.",
                technicalDetails: rawMessage
            )
        }

        if isAuthError(normalized) {
            return SyncUserError(
                category: .auth,
                title: "Authentication Error",
                message: "VaultSync could not verify this request.",
                remediation: "Retry and ensure certificates/subscription state are valid.",
                technicalDetails: rawMessage
            )
        }

        if isValidationError(normalized) {
            return SyncUserError(
                category: .validation,
                title: "Invalid Input",
                message: "Some entered data is invalid or incomplete.",
                remediation: "Review the value and try again.",
                technicalDetails: rawMessage
            )
        }

        if isConfigError(normalized) {
            return SyncUserError(
                category: .config,
                title: "Configuration Error",
                message: "VaultSync found a configuration problem.",
                remediation: "Review the affected device/folder setup in the app and retry.",
                technicalDetails: rawMessage
            )
        }

        return SyncUserError(
            category: .unknown,
            title: fallbackTitle,
            message: "VaultSync reported an unexpected error.",
            remediation: "Retry the action. If it keeps failing, restart the app and check Settings diagnostics.",
            technicalDetails: rawMessage
        )
    }

    static func from(error: any Error, fallbackTitle: String = "Sync Error") -> SyncUserError {
        from(rawMessage: error.localizedDescription, fallbackTitle: fallbackTitle)
    }

    static func fromFolderStatus(
        reason: String?,
        message: String?,
        path: String?
    ) -> SyncUserError {
        let normalizedReason = (reason ?? "").lowercased()
        let detail = message ?? "Folder is currently in an error state."
        let pathHint = path.map { " (\($0))" } ?? ""

        switch normalizedReason {
        case "permission_denied":
            return SyncUserError(
                category: .permission,
                title: "Folder Permission Error",
                message: "VaultSync cannot access this folder\(pathHint).",
                remediation: "Reconnect Obsidian access or adjust folder permissions on the host device.",
                technicalDetails: detail
            )
        case "folder_path_missing":
            return SyncUserError(
                category: .config,
                title: "Folder Path Missing",
                message: "The folder path no longer exists\(pathHint).",
                remediation: "Recreate or reselect the folder, then trigger a rescan.",
                technicalDetails: detail
            )
        case "folder_not_found":
            return SyncUserError(
                category: .config,
                title: "Folder Not Configured",
                message: "Syncthing no longer has this folder configured.",
                remediation: "Remove and re-share the folder from your desktop device.",
                technicalDetails: detail
            )
        default:
            return SyncUserError(
                category: .config,
                title: "Folder Sync Error",
                message: detail,
                remediation: "Check folder sharing, connectivity, and permissions, then retry.",
                technicalDetails: detail
            )
        }
    }

    static func relayProvisionFailed(reason: String) -> SyncUserError {
        let normalized = reason.lowercased()
        if isRelayConnectivityError(normalized) {
            return SyncUserError(
                category: .relayUnreachable,
                title: "Relay Unreachable",
                message: "Cloud Relay provisioning could not contact the relay backend.",
                remediation: "Check internet connectivity and retry provisioning.",
                technicalDetails: reason
            )
        }
        if normalized.contains("429") || normalized.contains("rate") {
            return SyncUserError(
                category: .relayProvision,
                title: "Relay Rate Limited",
                message: "Cloud Relay provisioning is temporarily rate limited.",
                remediation: "Wait a moment and retry.",
                technicalDetails: reason
            )
        }
        return SyncUserError(
            category: .relayProvision,
            title: "Relay Provisioning Failed",
            message: "Cloud Relay provisioning did not complete.",
            remediation: "Retry provisioning from Settings. If this persists, verify subscription status.",
            technicalDetails: reason
        )
    }

    static func apnsRegistrationFailed(reason: String) -> SyncUserError {
        SyncUserError(
            category: .permission,
            title: "Push Registration Failed",
            message: "iOS did not provide a push token required for instant sync.",
            remediation: "Enable notifications for VaultSync in iOS Settings and retry APNs registration.",
            technicalDetails: reason
        )
    }

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
        normalized.contains("invalid")
            || normalized.contains("required")
    }

    private static func isConfigError(_ normalized: String) -> Bool {
        normalized.contains("already exists")
            || normalized.contains("cannot add own")
            || normalized.contains("folder not found")
            || normalized.contains("path")
            || normalized.contains("config")
    }
}
