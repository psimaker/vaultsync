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

    private var syncthingItem: ChecklistItem {
        if syncthingManager.isRunning, !syncthingManager.deviceID.isEmpty {
            return ChecklistItem(
                requirement: .syncthingRunning,
                title: L10n.tr("Syncthing engine started"),
                description: L10n.tr("Device ID is available and Syncthing is running."),
                remediation: "",
                isOptional: false,
                isComplete: true
            )
        }

        let message = syncthingManager.userError?.message ?? L10n.tr("VaultSync is still starting Syncthing.")
        let remediation = syncthingManager.userError?.remediation
            ?? L10n.tr("Keep the app open for a moment. If this persists, restart VaultSync.")
        return ChecklistItem(
            requirement: .syncthingRunning,
            title: L10n.tr("Syncthing engine started"),
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
                title: L10n.tr("Desktop device paired"),
                description: countDescription(count, singular: L10n.tr("device configured"), plural: L10n.tr("devices configured")),
                remediation: "",
                isOptional: false,
                isComplete: true
            )
        }

        return ChecklistItem(
            requirement: .desktopDeviceAdded,
            title: L10n.tr("Desktop device paired"),
            description: L10n.tr("No desktop or laptop Syncthing device configured yet."),
            remediation: L10n.tr("Add a device from the main screen using its Syncthing Device ID."),
            isOptional: false,
            isComplete: false
        )
    }

    private var obsidianItem: ChecklistItem {
        if vaultManager.isAccessible {
            let vaultCount = vaultManager.detectedVaults.count
            let detail = vaultCount > 0
                ? L10n.fmt("Connected. %@", countDescription(vaultCount, singular: L10n.tr("vault detected"), plural: L10n.tr("vaults detected")))
                : L10n.tr("Connected. Waiting for vault folders to appear.")
            return ChecklistItem(
                requirement: .obsidianConnected,
                title: L10n.tr("Obsidian connected"),
                description: detail,
                remediation: "",
                isOptional: false,
                isComplete: true
            )
        }

        let issue = vaultManager.accessIssue
        return ChecklistItem(
            requirement: .obsidianConnected,
            title: L10n.tr("Obsidian connected"),
            description: issue?.message ?? L10n.tr("VaultSync does not have access to your Obsidian directory."),
            remediation: issue?.remediation ?? L10n.tr("Connect the Obsidian folder from the main screen."),
            isOptional: false,
            isComplete: false
        )
    }

    private var firstShareItem: ChecklistItem {
        if !syncthingManager.folders.isEmpty {
            return ChecklistItem(
                requirement: .firstShareDetectedOrAccepted,
                title: L10n.tr("First share detected"),
                description: countDescription(syncthingManager.folders.count, singular: L10n.tr("shared folder active"), plural: L10n.tr("shared folders active")),
                remediation: "",
                isOptional: false,
                isComplete: true
            )
        }

        if !syncthingManager.pendingFolders.isEmpty {
            return ChecklistItem(
                requirement: .firstShareDetectedOrAccepted,
                title: L10n.tr("First share detected"),
                description: countDescription(syncthingManager.pendingFolders.count, singular: L10n.tr("pending share found"), plural: L10n.tr("pending shares found")),
                remediation: L10n.tr("Open Pending Shares in VaultSync and accept one to start syncing."),
                isOptional: false,
                isComplete: true
            )
        }

        if syncthingManager.hasSeenPendingFolderOffer {
            return ChecklistItem(
                requirement: .firstShareDetectedOrAccepted,
                title: L10n.tr("First share detected"),
                description: L10n.tr("A share was detected earlier, but there is no active folder yet."),
                remediation: L10n.tr("If syncing has not started, reshare a vault from desktop Syncthing."),
                isOptional: false,
                isComplete: false
            )
        }

        return ChecklistItem(
            requirement: .firstShareDetectedOrAccepted,
            title: L10n.tr("First share detected"),
            description: L10n.tr("No folder share from your desktop has been detected yet."),
            remediation: L10n.tr("From desktop Syncthing, share one vault to this iPhone Device ID."),
            isOptional: false,
            isComplete: false
        )
    }

    private var relayItem: ChecklistItem {
        if subscriptionManager.isRelaySubscribed {
            return ChecklistItem(
                requirement: .relayConfigured,
                title: L10n.tr("Cloud Relay configured (optional)"),
                description: L10n.tr("Instant sync via Cloud Relay is active."),
                remediation: "",
                isOptional: true,
                isComplete: true
            )
        }

        return ChecklistItem(
            requirement: .relayConfigured,
            title: L10n.tr("Cloud Relay configured (optional)"),
            description: L10n.tr("Cloud Relay is off."),
            remediation: L10n.tr("You can enable it later in Settings for instant push-based sync."),
            isOptional: true,
            isComplete: false
        )
    }

    private func countDescription(_ count: Int, singular: String, plural: String) -> String {
        L10n.fmt("%d %@.", count, count == 1 ? singular : plural)
    }
}
