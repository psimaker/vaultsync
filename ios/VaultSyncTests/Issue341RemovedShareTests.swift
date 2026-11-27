import Foundation
import Testing
@testable import VaultSync

/// Since #341 the share of a removed vault is pending again the moment its
/// removal lands, while a device that shares it is connected: the engine
/// writes the offer from that device's last cluster config (decision 088,
/// shown against a real peer in `TestIssue341_…` of the bridge suite). What
/// the app does with that offer is shown here: it is listed when the removal
/// returns, and only an explicit accept takes it — the automatic pass never
/// does, in this session or a later one (doctrine 002, decision 006). The
/// bridge is replaced, never the engine run; the record lives in defaults of
/// the test's own.
@MainActor
@Suite("A removed vault's share that is back at once waits for a tap (#341)")
struct Issue341RemovedShareTests {

    @MainActor
    private final class Rig {
        let suiteName = "issue341-\(UUID().uuidString)"
        let defaults: UserDefaults
        let folderID = "issue341-\(UUID().uuidString)"
        let manager = SyncthingManager()
        /// What the engine lists as pending; empty until the removal lands.
        var pendingJSON = "[]"

        init() {
            defaults = UserDefaults(suiteName: suiteName)!
            manager._testUseRemoval(bridge: { [unowned self] _ in
                // The removal lands, and inside its commit the engine makes
                // the share pending again from the peer's announcement.
                self.pendingJSON = self.offerJSON
                return nil
            }, defaults: defaults)
            manager._testUsePendingRead { [unowned self] in self.pendingJSON }
        }

        var offerJSON: String {
            "[{\"id\":\"\(folderID)\",\"label\":\"Vault\",\"offeredBy\":[{\"deviceID\":\"PEER\",\"name\":\"Hub\",\"time\":\"2026-11-27T12:00:00Z\"}]}]"
        }

        /// The shares the automatic pass accepts while `manager` holds them.
        func automaticallyAccepted(by manager: SyncthingManager) -> [String] {
            var accepted: [String] = []
            let coordinator = ShareAcceptCoordinator(environment: ShareAcceptCoordinator.Environment(
                settled: { true },
                vaultAccessible: { true },
                pendingFolders: { manager.pendingFolders },
                autoAcceptEligible: { manager.autoAcceptEligiblePendingFolders },
                accept: { folder, _ in
                    accepted.append(folder.id)
                    return .accepted
                },
                acceptIntoTarget: { _, _ in nil },
                unignorePendingFolder: { _ in },
                ignorePendingFolder: { _ in }
            ))
            coordinator.runAutomaticPass()
            return accepted
        }

        func tearDown() {
            FolderPathReconciler.removeRel(forFolder: folderID)
            ManualShareTargetStore.forgetConsent(forFolder: folderID)
            defaults.removePersistentDomain(forName: suiteName)
        }
    }

    @Test("The offer is listed when the removal returns, and the automatic pass leaves it")
    func listedAtOnceAndLeftAlone() {
        let rig = Rig()
        defer { rig.tearDown() }

        #expect(rig.manager.removeFolder(id: rig.folderID) == nil)
        // Listed without waiting for a poll: the manager read the pending
        // shares again once the removal had returned.
        #expect(rig.manager.pendingFolders.map(\.id) == [rig.folderID])
        #expect(rig.manager.actionablePendingFolders.map(\.id) == [rig.folderID])
        #expect(rig.manager.autoAcceptEligiblePendingFolders.isEmpty)
        #expect(rig.automaticallyAccepted(by: rig.manager).isEmpty)
    }

    @Test("A later start finds the offer waiting for a tap too")
    func laterStartLeavesItAlone() {
        let rig = Rig()
        defer { rig.tearDown() }

        #expect(rig.manager.removeFolder(id: rig.folderID) == nil)
        // The app is ended and opened again: the record is read from the
        // defaults, and the engine lists the offer as before.
        let later = SyncthingManager()
        later._testUseRemoval(bridge: { _ in nil }, defaults: rig.defaults)
        later._testSetPendingFolders(rig.manager.pendingFolders)
        #expect(later.pendingFolders.map(\.id) == [rig.folderID])
        #expect(later.userRemovedFolderIDs.contains(rig.folderID))
        #expect(later.autoAcceptEligiblePendingFolders.isEmpty)
        #expect(rig.automaticallyAccepted(by: later).isEmpty)
    }
}
