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
}
