import Foundation
import Testing
@testable import VaultSync

@Suite("Selection advisory classification (#95)")
struct SelectionAdvisoryClassificationTests {
    @Test("iCloud root wins over vault-as-root — re-selecting On My iPhone fixes both")
    func iCloudWins() {
        #expect(VaultManager.selectionAdvisoryKind(isUbiquitous: true, pickedFolderIsVault: true) == .iCloudRoot)
        #expect(VaultManager.selectionAdvisoryKind(isUbiquitous: true, pickedFolderIsVault: false) == .iCloudRoot)
    }

    @Test("Vault-as-root advisory unchanged when the root is local")
    func vaultAsRootUnchanged() {
        #expect(VaultManager.selectionAdvisoryKind(isUbiquitous: false, pickedFolderIsVault: true) == .rootIsVault)
        #expect(VaultManager.selectionAdvisoryKind(isUbiquitous: false, pickedFolderIsVault: false) == nil)
    }

    @Test("Path heuristic flags iCloud Drive containers only")
    func pathHeuristic() {
        #expect(VaultManager.pathLooksUbiquitous("/private/var/mobile/Library/Mobile Documents/iCloud~md~obsidian/Documents"))
        #expect(VaultManager.pathLooksUbiquitous("/var/mobile/Library/Mobile Documents/com~apple~CloudDocs/Obsidian"))
        #expect(!VaultManager.pathLooksUbiquitous("/var/mobile/Containers/Data/Application/ABC/Documents/Obsidian"))
        #expect(!VaultManager.pathLooksUbiquitous("/var/mobile/Containers/Shared/Mobile Documentsish/Obsidian"))
    }
}

@Suite("Share refusal alert gate (#95)")
struct ShareRefusalAlertGateTests {
    @Test("The same refusal reason alerts only once per folder")
    func sameReasonSuppressed() {
        let shown = ["folder-1": "no safe location"]
        #expect(!ShareRefusalAlertStore.shouldPresent(folderID: "folder-1", reason: "no safe location", shown: shown))
    }

    @Test("A new refusal reason for the same folder alerts again")
    func newReasonAlerts() {
        let shown = ["folder-1": "no safe location"]
        #expect(ShareRefusalAlertStore.shouldPresent(folderID: "folder-1", reason: "target overlaps another vault", shown: shown))
    }

    @Test("An unseen folder always alerts")
    func unseenFolderAlerts() {
        #expect(ShareRefusalAlertStore.shouldPresent(folderID: "folder-2", reason: "anything", shown: [:]))
        #expect(ShareRefusalAlertStore.shouldPresent(folderID: "folder-2", reason: "anything", shown: ["folder-1": "anything"]))
    }
}

@MainActor
@Suite("Coordinator gates the automatic refusal modal, not the inline record (#95)")
struct CoordinatorRefusalModalGateTests {
    @Test("A suppressed reason records the failure but raises no modal; the record is marked only when presented")
    func suppressedReasonRecordsWithoutModal() {
        var marked: [String] = []
        let env = ShareAcceptCoordinator.Environment(
            settled: { true },
            vaultAccessible: { true },
            pendingFolders: { [SyncthingManager.PendingFolderInfo(id: "f1", label: "F1", offeredBy: [])] },
            autoAcceptEligible: { [SyncthingManager.PendingFolderInfo(id: "f1", label: "F1", offeredBy: [])] },
            accept: { _, _ in .refused(message: "no safe location") },
            acceptIntoTarget: { _, _ in nil },
            unignorePendingFolder: { _ in },
            ignorePendingFolder: { _ in },
            shouldPresentRefusalAlert: { _, _ in false },
            markRefusalAlertPresented: { id, _ in marked.append(id) }
        )
        let c = ShareAcceptCoordinator(environment: env)
        c.runAutomaticPass()
        #expect(c.pendingShareFailures["f1"] != nil) // inline honesty preserved (002)
        #expect(c.alertMessage == nil)               // modal gated
        #expect(marked.isEmpty)                      // only marked when actually presented
    }

    @Test("A fresh reason presents the modal and marks the record")
    func freshReasonPresentsAndMarks() {
        var marked: [(String, String)] = []
        let env = ShareAcceptCoordinator.Environment(
            settled: { true },
            vaultAccessible: { true },
            pendingFolders: { [SyncthingManager.PendingFolderInfo(id: "f1", label: "F1", offeredBy: [])] },
            autoAcceptEligible: { [SyncthingManager.PendingFolderInfo(id: "f1", label: "F1", offeredBy: [])] },
            accept: { _, _ in .refused(message: "no safe location") },
            acceptIntoTarget: { _, _ in nil },
            unignorePendingFolder: { _ in },
            ignorePendingFolder: { _ in },
            shouldPresentRefusalAlert: { _, _ in true },
            markRefusalAlertPresented: { marked.append(($0, $1)) }
        )
        let c = ShareAcceptCoordinator(environment: env)
        c.runAutomaticPass()
        #expect(c.alertMessage != nil)
        #expect(marked.count == 1)
        #expect(marked.first?.0 == "f1")
        #expect(marked.first?.1 == "no safe location")
    }

    @Test("Two refusals in one pass: only the displayed alert is marked as presented — the second alerts on a later pass")
    func twoRefusalsInOnePassMarkOnlyTheDisplayedOne() {
        var marked: [String] = []
        let f1 = SyncthingManager.PendingFolderInfo(id: "f1", label: "F1", offeredBy: [])
        let f2 = SyncthingManager.PendingFolderInfo(id: "f2", label: "F2", offeredBy: [])
        let env = ShareAcceptCoordinator.Environment(
            settled: { true },
            vaultAccessible: { true },
            pendingFolders: { [f1, f2] },
            autoAcceptEligible: { [f1, f2] },
            accept: { _, _ in .refused(message: "no safe location") },
            acceptIntoTarget: { _, _ in nil },
            unignorePendingFolder: { _ in },
            ignorePendingFolder: { _ in },
            shouldPresentRefusalAlert: { _, _ in true },
            markRefusalAlertPresented: { id, _ in marked.append(id) }
        )
        let c = ShareAcceptCoordinator(environment: env)
        c.runAutomaticPass()
        // Only f1 claimed the one-shot slot; f2 must NOT be marked presented,
        // because its alert never rendered — it alerts on a later pass.
        #expect(c.alertMessage?.contains("F1") == true)
        #expect(marked == ["f1"])
        // Both failures stay visible inline regardless (doctrine 002).
        #expect(c.pendingShareFailures.count == 2)
    }

    @Test("A successful accept clears the refusal record")
    func acceptClearsRecord() {
        var cleared: [String] = []
        let env = ShareAcceptCoordinator.Environment(
            settled: { true },
            vaultAccessible: { true },
            pendingFolders: { [SyncthingManager.PendingFolderInfo(id: "f1", label: "F1", offeredBy: [])] },
            autoAcceptEligible: { [SyncthingManager.PendingFolderInfo(id: "f1", label: "F1", offeredBy: [])] },
            accept: { _, _ in .accepted },
            acceptIntoTarget: { _, _ in nil },
            unignorePendingFolder: { _ in },
            ignorePendingFolder: { _ in },
            clearRefusalAlertRecord: { cleared.append($0) }
        )
        let c = ShareAcceptCoordinator(environment: env)
        c.runAutomaticPass()
        #expect(cleared == ["f1"])
    }
}

@Suite("Header opens checklist (#95)")
struct SyncHeaderOpensChecklistTests {
    @Test("Only Finish Setup and Action Needed make the header tappable")
    func gating() {
        #expect(SyncHeaderModel.opensChecklist(titleKey: "Finish Setup"))
        #expect(SyncHeaderModel.opensChecklist(titleKey: "Action Needed"))
        for key in ["Error", "Starting…", "Sync Issue", "Syncing…", "All Synced", "Ready", "No Vaults Yet"] {
            #expect(!SyncHeaderModel.opensChecklist(titleKey: key), "\(key) must not open the checklist")
        }
    }
}

@MainActor
@Suite("Checklist ignored-offer remediation (#95)", .serialized)
struct ChecklistIgnoredOfferTests {
    @Test("An ignored-only offer points at Restore Share, not at re-sharing from the desktop")
    func ignoredOnlyOfferBranch() {
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

        syncthingManager._testSetPendingFolders([
            SyncthingManager.PendingFolderInfo(id: "offer-95", label: "Life Notes", offeredBy: [])
        ])
        syncthingManager.ignorePendingFolder(id: "offer-95")
        defer { TestSupport.resetSyncthingState() } // clears syncthing.ignoredPendingFolderIDs

        let item = viewModel.items.first { $0.requirement == .firstShareDetectedOrAccepted }
        #expect(item?.isComplete == false)
        #expect(item?.remediation.contains("Restore Share") == true)
        // The dead-end advice must be gone in this state:
        #expect(item?.remediation.contains("share your Obsidian vault again") != true)
    }

    @Test("An actionable offer still wins over an ignored one")
    func actionableWinsOverIgnored() {
        TestSupport.resetSyncthingState()
        TestSupport.resetRelayState()

        let syncthingManager = SyncthingManager()
        let viewModel = SetupChecklistViewModel(
            syncthingManager: syncthingManager,
            vaultManager: VaultManager(),
            subscriptionManager: SubscriptionManager()
        )
        syncthingManager._testSetPendingFolders([
            SyncthingManager.PendingFolderInfo(id: "offer-a", label: "A", offeredBy: []),
            SyncthingManager.PendingFolderInfo(id: "offer-b", label: "B", offeredBy: [])
        ])
        syncthingManager.ignorePendingFolder(id: "offer-b")
        defer { TestSupport.resetSyncthingState() }

        let item = viewModel.items.first { $0.requirement == .firstShareDetectedOrAccepted }
        #expect(item?.description.contains("waiting") == true) // "A vault offer is waiting to be accepted."
    }
}
