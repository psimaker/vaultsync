import Foundation
import Observation

@MainActor
@Observable
final class SetupChecklistViewModel {
    enum Requirement: String, CaseIterable, Hashable, Identifiable {
        case syncthingRunning
        case desktopDeviceAdded
        case obsidianConnected
        case firstShareDetectedOrAccepted
        case relayConfigured

        var id: String { rawValue }
    }

    struct ChecklistItem: Identifiable, Hashable {
        let requirement: Requirement
        let title: String
        let description: String
        let remediation: String
        let isOptional: Bool
        let isComplete: Bool

        var id: Requirement { requirement }
    }

    private let syncthingManager: SyncthingManager
    private let vaultManager: VaultManager
    private let subscriptionManager: SubscriptionManager

    init(
        syncthingManager: SyncthingManager,
        vaultManager: VaultManager,
        subscriptionManager: SubscriptionManager
    ) {
        self.syncthingManager = syncthingManager
        self.vaultManager = vaultManager
        self.subscriptionManager = subscriptionManager
    }

    var items: [ChecklistItem] {
        [
            syncthingItem,
            desktopDeviceItem,
            obsidianItem,
            firstShareItem,
            relayItem
        ]
    }

    var requiredItems: [ChecklistItem] {
        items.filter { !$0.isOptional }
    }

    var completedRequiredCount: Int {
        requiredItems.filter(\.isComplete).count
    }

    var totalRequiredCount: Int {
        requiredItems.count
    }

    var completionProgress: Double {
        guard totalRequiredCount > 0 else { return 0 }
        return Double(completedRequiredCount) / Double(totalRequiredCount)
    }

    var incompleteRequiredItems: [ChecklistItem] {
        requiredItems.filter { !$0.isComplete }
    }

    var isReadyToFinish: Bool {
        incompleteRequiredItems.isEmpty
    }

    var continueWarningText: String {
        if incompleteRequiredItems.isEmpty {
            return "Setup is complete."
        }

        let titles = incompleteRequiredItems.map(\.title).joined(separator: ", ")
        return "You can continue, but syncing may fail until you finish: \(titles)."
    }

    private var syncthingItem: ChecklistItem {
        if syncthingManager.isRunning, !syncthingManager.deviceID.isEmpty {
            return ChecklistItem(
                requirement: .syncthingRunning,
                title: "Syncthing engine started",
                description: "Device ID is available and Syncthing is running.",
                remediation: "",
                isOptional: false,
                isComplete: true
            )
        }

        let message = syncthingManager.userError?.message ?? "VaultSync is still starting Syncthing."
        let remediation = syncthingManager.userError?.remediation
            ?? "Keep the app open for a moment. If this persists, restart VaultSync."
        return ChecklistItem(
            requirement: .syncthingRunning,
            title: "Syncthing engine started",
            description: message,
            remediation: remediation,
            isOptional: false,
            isComplete: false
        )
    }

    private var desktopDeviceItem: ChecklistItem {
        let count = syncthingManager.devices.count
        if count > 0 {
            return ChecklistItem(
                requirement: .desktopDeviceAdded,
                title: "Desktop device paired",
                description: countDescription(count, singular: "device configured", plural: "devices configured"),
                remediation: "",
                isOptional: false,
                isComplete: true
            )
        }

        return ChecklistItem(
            requirement: .desktopDeviceAdded,
            title: "Desktop device paired",
            description: "No desktop or laptop Syncthing device configured yet.",
            remediation: "Enter your desktop's Device ID in the pairing step above.",
            isOptional: false,
            isComplete: false
        )
    }

    private var obsidianItem: ChecklistItem {
        if vaultManager.isAccessible {
            let vaultCount = vaultManager.detectedVaults.count
            let detail = vaultCount > 0
                ? "Connected. \(countDescription(vaultCount, singular: "vault detected", plural: "vaults detected"))"
                : "Connected. Waiting for vault folders to appear."
            return ChecklistItem(
                requirement: .obsidianConnected,
                title: "Obsidian connected",
                description: detail,
                remediation: "",
                isOptional: false,
                isComplete: true
            )
        }

        let issue = vaultManager.accessIssue
        return ChecklistItem(
            requirement: .obsidianConnected,
            title: "Obsidian connected",
            description: issue?.message ?? "VaultSync does not have access to your Obsidian directory.",
            remediation: issue?.remediation ?? "Reconnect the Obsidian folder from the picker.",
            isOptional: false,
            isComplete: false
        )
    }

    private var firstShareItem: ChecklistItem {
        if !syncthingManager.folders.isEmpty {
            return ChecklistItem(
                requirement: .firstShareDetectedOrAccepted,
                title: "First share detected",
                description: countDescription(syncthingManager.folders.count, singular: "shared folder active", plural: "shared folders active"),
                remediation: "",
                isOptional: false,
                isComplete: true
            )
        }

        if !syncthingManager.pendingFolders.isEmpty {
            return ChecklistItem(
                requirement: .firstShareDetectedOrAccepted,
                title: "First share detected",
                description: countDescription(syncthingManager.pendingFolders.count, singular: "pending share found", plural: "pending shares found"),
                remediation: "Open Pending Shares in VaultSync and accept one to start syncing.",
                isOptional: false,
                isComplete: true
            )
        }

        if syncthingManager.hasSeenPendingFolderOffer {
            return ChecklistItem(
                requirement: .firstShareDetectedOrAccepted,
                title: "First share detected",
                description: "A share was detected earlier, but there is no active folder yet.",
                remediation: "If syncing has not started, reshare a vault from desktop Syncthing.",
                isOptional: false,
                isComplete: false
            )
        }

        return ChecklistItem(
            requirement: .firstShareDetectedOrAccepted,
            title: "First share detected",
            description: "No folder share from your desktop has been detected yet.",
            remediation: "From desktop Syncthing, share one vault to this iPhone Device ID.",
            isOptional: false,
            isComplete: false
        )
    }

    private var relayItem: ChecklistItem {
        if subscriptionManager.isRelaySubscribed {
            return ChecklistItem(
                requirement: .relayConfigured,
                title: "Cloud Relay configured (optional)",
                description: "Instant sync via Cloud Relay is active.",
                remediation: "",
                isOptional: true,
                isComplete: true
            )
        }

        return ChecklistItem(
            requirement: .relayConfigured,
            title: "Cloud Relay configured (optional)",
            description: "Cloud Relay is off.",
            remediation: "You can enable it later in Settings for instant push-based sync.",
            isOptional: true,
            isComplete: false
        )
    }

    private func countDescription(_ count: Int, singular: String, plural: String) -> String {
        "\(count) \(count == 1 ? singular : plural)."
    }
}
