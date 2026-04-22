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
    func checklistTransitionsAcrossCoreRequirements() {
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

        syncthingManager.start()
        defer {
            syncthingManager.stop()
            TestSupport.resetSyncthingState()
        }

        #expect(syncthingManager.isRunning)
        #expect(!syncthingManager.deviceID.isEmpty)

        let addDeviceError = syncthingManager.addDevice(id: TestSupport.samplePeerDeviceID, name: "Desktop")
        #expect(addDeviceError == nil)

        let folderID = "checklist-share-\(UUID().uuidString.prefix(8))"
        let folderPath = FileManager.default.temporaryDirectory
            .appendingPathComponent("vaultsync-tests", isDirectory: true)
            .appendingPathComponent(String(folderID), isDirectory: true)
            .path
        let addFolderError = syncthingManager.addFolder(id: String(folderID), label: "Checklist Share", path: folderPath)
        #expect(addFolderError == nil)

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
        #expect(vaultSyncingItem?.title == L10n.tr("Vault syncing"))
        #expect(vaultSyncingItem?.isComplete == false)
        #expect(vaultSyncingItem?.description == L10n.tr("A vault offer was seen earlier, but no vault is syncing right now."))
        #expect(
            vaultSyncingItem?.remediation
                == L10n.tr("If syncing has not started, share your Obsidian vault again from Syncthing on your computer.")
        )
        #expect(viewModel.completedRequiredCount == 0)
    }
}
