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

    /// An in-app action that directly advances a checklist item, so remediations
    /// are a tappable button instead of prose-only "go to the home screen and…"
    /// directions. Only items with a real in-app entry point carry one.
    enum ChecklistAction: Hashable {
        case connectObsidian
        case addDevice
        case openRelayTab
    }

    struct ChecklistItem: Identifiable, Hashable {
        let requirement: Requirement
        let title: String
        let description: String
        let remediation: String
        let isOptional: Bool
        let isComplete: Bool
        var action: ChecklistAction? = nil

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
            obsidianItem,
            desktopDeviceItem,
            firstShareItem,
            syncthingItem,
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
                title: L10n.tr("Sync engine running"),
                description: L10n.tr("VaultSync’s sync engine is running."),
                remediation: "",
                isOptional: false,
                isComplete: true
            )
        }

        return ChecklistItem(
            requirement: .syncthingRunning,
            title: L10n.tr("Sync engine running"),
            description: L10n.tr("VaultSync’s sync engine is still starting or unavailable."),
            remediation: L10n.tr("If this stays unavailable, restart VaultSync and check the home screen for issues."),
            isOptional: false,
            isComplete: false
        )
    }

    private var desktopDeviceItem: ChecklistItem {
        let count = syncthingManager.devices.count
        if count > 0 {
            return ChecklistItem(
                requirement: .desktopDeviceAdded,
                title: L10n.tr("Computer or server added"),
                description: L10n.tr("Your iPhone is paired with at least one Syncthing device."),
                remediation: "",
                isOptional: false,
                isComplete: true
            )
        }

        return ChecklistItem(
            requirement: .desktopDeviceAdded,
            title: L10n.tr("Computer or server added"),
            description: L10n.tr("Your iPhone is not paired with a Syncthing device yet."),
            remediation: L10n.tr("Add your computer or server from the Devices section on the home screen."),
            isOptional: false,
            isComplete: false,
            action: .addDevice
        )
    }

    private var obsidianItem: ChecklistItem {
        if vaultManager.isAccessible {
            return ChecklistItem(
                requirement: .obsidianConnected,
                title: L10n.tr("Obsidian folder connected"),
                description: L10n.tr("VaultSync can access your local Obsidian folder."),
                remediation: "",
                isOptional: false,
                isComplete: true
            )
        }

        return ChecklistItem(
            requirement: .obsidianConnected,
            title: L10n.tr("Obsidian folder connected"),
            description: L10n.tr("VaultSync cannot access your local Obsidian folder."),
            remediation: L10n.tr("Connect your Obsidian folder from the VaultSync home screen."),
            isOptional: false,
            isComplete: false,
            action: .connectObsidian
        )
    }

    private var firstShareItem: ChecklistItem {
        if !syncthingManager.folders.isEmpty {
            return ChecklistItem(
                requirement: .firstShareDetectedOrAccepted,
                title: L10n.tr("Vault syncing"),
                description: L10n.tr("At least one Obsidian vault is active in VaultSync."),
                remediation: "",
                isOptional: false,
                isComplete: true
            )
        }

        if !syncthingManager.actionablePendingFolders.isEmpty {
            return ChecklistItem(
                requirement: .firstShareDetectedOrAccepted,
                title: L10n.tr("Vault syncing"),
                description: L10n.tr("A vault offer is waiting to be accepted."),
                remediation: L10n.tr("A vault offer is waiting. Accept it from Pending Shares on the home screen."),
                isOptional: false,
                isComplete: false
            )
        }

        if syncthingManager.hasSeenPendingFolderOffer {
            return ChecklistItem(
                requirement: .firstShareDetectedOrAccepted,
                title: L10n.tr("Vault syncing"),
                description: L10n.tr("A vault offer was seen earlier, but no vault is syncing right now."),
                remediation: L10n.tr("If syncing has not started, share your Obsidian vault again from Syncthing on your computer."),
                isOptional: false,
                isComplete: false
            )
        }

        return ChecklistItem(
            requirement: .firstShareDetectedOrAccepted,
            title: L10n.tr("Vault syncing"),
            description: L10n.tr("No Obsidian vault is active in VaultSync yet."),
            remediation: L10n.tr("Share your Obsidian vault from Syncthing on your computer."),
            isOptional: false,
            isComplete: false
        )
    }

    enum RelayChecklistState {
        case notSubscribed
        case awaitingDelivery
        case delivering
    }

    /// Pure decision for the relay checklist state, unit-tested independently of
    /// SubscriptionManager so the three-state honesty logic can't silently regress.
    static func relayChecklistState(isSubscribed: Bool, isDelivering: Bool) -> RelayChecklistState {
        if !isSubscribed { return .notSubscribed }
        return isDelivering ? .delivering : .awaitingDelivery
    }

    private var relayItem: ChecklistItem {
        // "Delivering" requires a *recent* wake-up — relayDeliveryConfirmed applies
        // a 48h freshness window — so the checklist agrees with Settings and Relay
        // Diagnostics instead of staying green after the helper has stopped.
        switch Self.relayChecklistState(
            isSubscribed: subscriptionManager.isRelaySubscribed,
            isDelivering: subscriptionManager.relayDeliveryConfirmed
        ) {
        case .notSubscribed:
            return ChecklistItem(
                requirement: .relayConfigured,
                title: L10n.tr("Cloud Relay"),
                description: L10n.tr("Cloud Relay is not enabled. Without it, incoming changes arrive when you open VaultSync."),
                remediation: L10n.tr("Enable Cloud Relay on the Relay tab if you want changes pushed the moment they happen."),
                isOptional: true,
                isComplete: false,
                action: .openRelayTab
            )
        case .awaitingDelivery:
            return ChecklistItem(
                requirement: .relayConfigured,
                title: L10n.tr("Cloud Relay — finish server setup"),
                description: L10n.tr("You’re subscribed, but no recent wake-up has arrived. Make sure the vaultsync-notify helper is running on your server."),
                remediation: L10n.tr("Set up the server helper from the Relay tab → Set Up Your Server."),
                isOptional: true,
                isComplete: false,
                action: .openRelayTab
            )
        case .delivering:
            return ChecklistItem(
                requirement: .relayConfigured,
                title: L10n.tr("Cloud Relay active"),
                description: L10n.tr("Wake-ups are being delivered — incoming changes sync the moment they happen."),
                remediation: "",
                isOptional: true,
                isComplete: true
            )
        }
    }
}
