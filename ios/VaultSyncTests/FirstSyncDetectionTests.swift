import Foundation
import Testing
@testable import VaultSync

@Suite("First-sync detection requires peer evidence (#94)")
struct FirstSyncDetectionTests {
    private func makeStatus(
        state: String,
        stateChanged: String = "2026-07-11T10:00:00Z",
        globalFiles: Int = 0,
        localFiles: Int = 0,
        needFiles: Int = 0,
        errorMessage: String? = nil
    ) -> SyncthingManager.FolderStatusInfo {
        SyncthingManager.FolderStatusInfo(payload: .init(
            state: state,
            stateChanged: stateChanged,
            completionPct: 100,
            globalBytes: 0,
            globalFiles: globalFiles,
            localBytes: 0,
            localFiles: localFiles,
            needBytes: 0,
            needFiles: needFiles,
            inProgressBytes: 0,
            errorReason: nil,
            errorMessage: errorMessage,
            errorPath: nil,
            errorChanged: nil
        ))
    }

    @Test("Reported repro: empty accepted share, peer offline — scan-to-idle is not a sync")
    func emptyScanWithoutPeerIsNotASync() {
        let status = makeStatus(state: "idle")
        #expect(!SyncthingManager.didTransitionToSuccessfulIdle(
            previousState: "scanning",
            status: status,
            hasConnectedPeer: false
        ))
        #expect(!SyncthingManager.shouldTreatIdleStateAsSuccess(
            status: status,
            stateChangedAt: Date(),
            existingDate: nil,
            hasConnectedPeer: false
        ))
    }

    @Test("Idle after activity with a connected peer and nothing to fetch counts — including a genuinely empty vault")
    func idleWithConnectedPeerCounts() {
        let emptyVault = makeStatus(state: "idle")
        #expect(SyncthingManager.didTransitionToSuccessfulIdle(
            previousState: "syncing",
            status: emptyVault,
            hasConnectedPeer: true
        ))
        #expect(SyncthingManager.shouldTreatIdleStateAsSuccess(
            status: emptyVault,
            stateChangedAt: Date(),
            existingDate: nil,
            hasConnectedPeer: true
        ))
    }

    @Test("Peer evidence never overrides the existing guards")
    func peerEvidenceDoesNotOverrideGuards() {
        // Still fetching.
        #expect(!SyncthingManager.didTransitionToSuccessfulIdle(
            previousState: "syncing",
            status: makeStatus(state: "idle", needFiles: 3),
            hasConnectedPeer: true
        ))
        // Folder error.
        #expect(!SyncthingManager.didTransitionToSuccessfulIdle(
            previousState: "syncing",
            status: makeStatus(state: "idle", errorMessage: "boom"),
            hasConnectedPeer: true
        ))
        // No observed prior state / non-active prior state.
        #expect(!SyncthingManager.didTransitionToSuccessfulIdle(
            previousState: nil,
            status: makeStatus(state: "idle"),
            hasConnectedPeer: true
        ))
        #expect(!SyncthingManager.didTransitionToSuccessfulIdle(
            previousState: "idle",
            status: makeStatus(state: "idle"),
            hasConnectedPeer: true
        ))
        // Backfill: unparseable date and non-advancing date still refuse.
        #expect(!SyncthingManager.shouldTreatIdleStateAsSuccess(
            status: makeStatus(state: "idle"),
            stateChangedAt: nil,
            existingDate: nil,
            hasConnectedPeer: true
        ))
        let existing = Date()
        #expect(!SyncthingManager.shouldTreatIdleStateAsSuccess(
            status: makeStatus(state: "idle"),
            stateChangedAt: existing,
            existingDate: existing,
            hasConnectedPeer: true
        ))
    }

    @Test("Connected-peer evidence is computed per folder from remote device IDs")
    func evidenceIsPerFolder() {
        let folders = [
            SyncthingManager.FolderInfo(
                id: "vault-a", label: "A", path: "/a",
                type: "sendreceive", paused: false, deviceIDs: ["PEER-ONLINE"]
            ),
            SyncthingManager.FolderInfo(
                id: "vault-b", label: "B", path: "/b",
                type: "sendreceive", paused: false, deviceIDs: ["PEER-OFFLINE"]
            ),
            SyncthingManager.FolderInfo(
                id: "vault-c", label: "C", path: "/c",
                type: "sendreceive", paused: false, deviceIDs: []
            ),
        ]
        let evidence = SyncthingManager.foldersWithConnectedPeer(
            folders: folders,
            connectedDeviceIDs: ["PEER-ONLINE", "PEER-UNRELATED"]
        )
        #expect(evidence == ["vault-a"])
    }

    @Test("No connected devices means no folder has evidence")
    func noConnectionsNoEvidence() {
        let folders = [
            SyncthingManager.FolderInfo(
                id: "vault-a", label: "A", path: "/a",
                type: "sendreceive", paused: false, deviceIDs: ["PEER1", "PEER2"]
            ),
        ]
        #expect(SyncthingManager.foldersWithConnectedPeer(
            folders: folders,
            connectedDeviceIDs: []
        ).isEmpty)
    }
}

@Suite("Relay upsell gate (#94)")
struct RelayUpsellGateTests {
    @Test("Legacy bogus persisted last sync: no pitch while nothing has actually synced")
    func blocksWithoutSyncedContent() {
        #expect(!RelayUpsellGate.shouldPresent(
            isSubscribed: false,
            hasSyncFolders: true,
            hasCompletedFirstSync: true,
            hasAnySyncedContent: false,
            alreadyShown: false
        ))
    }

    @Test("Pitches exactly once at the real aha moment")
    func presentsAtAhaMoment() {
        #expect(RelayUpsellGate.shouldPresent(
            isSubscribed: false,
            hasSyncFolders: true,
            hasCompletedFirstSync: true,
            hasAnySyncedContent: true,
            alreadyShown: false
        ))
        #expect(!RelayUpsellGate.shouldPresent(
            isSubscribed: false,
            hasSyncFolders: true,
            hasCompletedFirstSync: true,
            hasAnySyncedContent: true,
            alreadyShown: true
        ))
    }

    @Test("Each remaining precondition blocks on its own")
    func eachGuardBlocks() {
        #expect(!RelayUpsellGate.shouldPresent(
            isSubscribed: true, hasSyncFolders: true,
            hasCompletedFirstSync: true, hasAnySyncedContent: true, alreadyShown: false
        ))
        #expect(!RelayUpsellGate.shouldPresent(
            isSubscribed: false, hasSyncFolders: false,
            hasCompletedFirstSync: true, hasAnySyncedContent: true, alreadyShown: false
        ))
        #expect(!RelayUpsellGate.shouldPresent(
            isSubscribed: false, hasSyncFolders: true,
            hasCompletedFirstSync: false, hasAnySyncedContent: true, alreadyShown: false
        ))
    }
}
