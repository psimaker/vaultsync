import Foundation
import Testing
@testable import VaultSync

@MainActor
@Suite("Setup Checklist Contracts", .serialized)
struct SetupChecklistViewModelTests {
    @Test("Checklist starts with required items incomplete before setup")
    func checklistStartsIncomplete() {
        TestSupport.resetSyncthingState()
        TestSupport.resetRelayState()

        let syncthingManager = SyncthingManager()
        let vaultManager = VaultManager()
        let subscriptionManager = SubscriptionManager()
        let viewModel = SetupChecklistViewModel(
            syncthingManager: syncthingManager,
            vaultManager: vaultManager,
            subscriptionManager: subscriptionManager
        )

        #expect(viewModel.totalRequiredCount == 4)
        #expect(viewModel.completedRequiredCount == 0)
        #expect(!viewModel.isReadyToFinish)

        let incomplete = Set(viewModel.incompleteRequiredItems.map(\.requirement))
        #expect(incomplete.contains(.syncthingRunning))
        #expect(incomplete.contains(.desktopDeviceAdded))
        #expect(incomplete.contains(.obsidianConnected))
        #expect(incomplete.contains(.firstShareDetectedOrAccepted))
    }

    @Test("Checklist transitions when syncthing/device/share states become complete")
    func checklistTransitionsAcrossCoreRequirements() async {
        TestSupport.resetSyncthingState()
        TestSupport.resetRelayState()

        let syncthingManager = SyncthingManager()
        let vaultManager = VaultManager()
        let subscriptionManager = SubscriptionManager()
        let viewModel = SetupChecklistViewModel(
            syncthingManager: syncthingManager,
            vaultManager: vaultManager,
            subscriptionManager: subscriptionManager
        )

        await syncthingManager.start()
        defer {
            syncthingManager.stop()
            TestSupport.resetSyncthingState()
        }

        #expect(syncthingManager.isRunning)
        #expect(!syncthingManager.deviceID.isEmpty)

        let addDeviceError = syncthingManager.addDevice(id: TestSupport.samplePeerDeviceID, name: "Desktop")
        #expect(addDeviceError == nil)

        syncthingManager._testSetFolders([
            .init(
                id: "checklist-send-only",
                label: "Checklist Share",
                path: "/synthetic/checklist",
                type: "sendonly",
                paused: false,
                deviceIDs: [TestSupport.samplePeerDeviceID]
            ),
        ])

        let stateByRequirement = Dictionary(
            uniqueKeysWithValues: viewModel.items.map { ($0.requirement, $0.isComplete) }
        )
        #expect(stateByRequirement[.syncthingRunning] == true)
        #expect(stateByRequirement[.desktopDeviceAdded] == true)
        #expect(stateByRequirement[.firstShareDetectedOrAccepted] == true)
        #expect(stateByRequirement[.obsidianConnected] == false)
        #expect(viewModel.completedRequiredCount == 3)
        #expect(!viewModel.isReadyToFinish)
    }

    @Test("Checklist keeps vault syncing incomplete when only a prior share offer was seen")
    func checklistKeepsVaultSyncingIncompleteAfterSeenOffer() {
        TestSupport.resetSyncthingState()
        TestSupport.resetRelayState()
        UserDefaults.standard.set(true, forKey: "syncthing.hasSeenPendingFolderOffer")
        defer {
            TestSupport.resetSyncthingState()
        }

        let syncthingManager = SyncthingManager()
        let vaultManager = VaultManager()
        let subscriptionManager = SubscriptionManager()
        let viewModel = SetupChecklistViewModel(
            syncthingManager: syncthingManager,
            vaultManager: vaultManager,
            subscriptionManager: subscriptionManager
        )

        let vaultSyncingItem = viewModel.items.first { $0.requirement == .firstShareDetectedOrAccepted }
        #expect(vaultSyncingItem != nil)
        #expect(vaultSyncingItem?.title == L10n.tr("Vault setup"))
        #expect(vaultSyncingItem?.isComplete == false)
        #expect(vaultSyncingItem?.description == L10n.tr("A vault offer was seen earlier, but no vault is configured right now."))
        #expect(
            vaultSyncingItem?.remediation
                == L10n.tr("New share acceptance is unavailable in this version.")
        )
        #expect(viewModel.completedRequiredCount == 0)
    }

    @Test("Pending offers stay inspection-only throughout the checklist (#150)")
    func pendingOffersStayInspectionOnlyIssue150() {
        TestSupport.resetSyncthingState()
        TestSupport.resetRelayState()
        defer { TestSupport.resetSyncthingState() }

        let syncthingManager = SyncthingManager()
        let viewModel = SetupChecklistViewModel(
            syncthingManager: syncthingManager,
            vaultManager: VaultManager(),
            subscriptionManager: SubscriptionManager()
        )
        syncthingManager._testSetPendingFolders([
            .init(id: "issue-150-offer", label: "Synthetic Offer", offeredBy: []),
        ])

        var item = viewModel.items.first { $0.requirement == .firstShareDetectedOrAccepted }
        #expect(item?.isComplete == false)
        #expect(item?.description == L10n.tr("A vault offer is available for inspection."))
        #expect(item?.remediation == L10n.tr("Open Pending Shares to inspect the offer details. This version cannot accept it."))

        syncthingManager.ignorePendingFolder(id: "issue-150-offer")
        item = viewModel.items.first { $0.requirement == .firstShareDetectedOrAccepted }
        #expect(item?.isComplete == false)
        #expect(item?.description == L10n.tr("An ignored vault offer remains stored on this iPhone."))
        #expect(item?.remediation == L10n.tr("Open Pending Shares to inspect its details. No action is available in this version."))

        let rendered = "\(item?.description ?? "")|\(item?.remediation ?? "")".lowercased()
        for forbidden in ["accept", "retry", "restore", "ready", "automatically"] {
            #expect(!rendered.contains(forbidden))
        }
    }

    @Test("Relay checklist state covers all three branches")
    func relayChecklistStateTransitions() {
        // Not subscribed → notSubscribed regardless of delivery signal.
        #expect(SetupChecklistViewModel.relayChecklistState(isSubscribed: false, isDelivering: false) == .notSubscribed)
        #expect(SetupChecklistViewModel.relayChecklistState(isSubscribed: false, isDelivering: true) == .notSubscribed)
        // Subscribed but no fresh wake-up → awaitingDelivery (server setup pending/stale).
        #expect(SetupChecklistViewModel.relayChecklistState(isSubscribed: true, isDelivering: false) == .awaitingDelivery)
        // Subscribed and a recent wake-up confirmed → delivering.
        #expect(SetupChecklistViewModel.relayChecklistState(isSubscribed: true, isDelivering: true) == .delivering)
    }
}
