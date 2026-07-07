import Foundation
import Testing
@testable import VaultSync

/// `BackgroundSyncService.completeSync` used to persist "idle"/"error"
/// directly — the attention tier from #73 existed only on the foreground
/// path, so a successful background run overwrote an honest attention
/// snapshot (parked share, disconnected required peer, collision) with a
/// green idle (#76).
@Suite("Background completion widget status derives from result + issue floor (#76)")
struct BackgroundCompletionWidgetStatusTests {
    private static let allResults: [BackgroundSyncService.SyncResult] = [
        .synced, .alreadyIdle, .noBookmarkAccess, .noFoldersConfigured,
        .bridgeStartFailed, .notIdleBeforeDeadline, .failed, .settledWithFolderError,
    ]

    @Test("A clean success with no recorded issues stays green")
    func cleanSuccessIsSynced() {
        #expect(BackgroundSyncService.backgroundCompletionWidgetStatus(
            result: .synced, issueFloor: .none
        ) == .synced)
        #expect(BackgroundSyncService.backgroundCompletionWidgetStatus(
            result: .alreadyIdle, issueFloor: .none
        ) == .synced)
    }

    // The reported lie: a successful background run must not clear an
    // attention state it cannot have resolved (parked share, disconnected
    // required peer, collision — all foreground-recorded).
    @Test("A success never overwrites a recorded issue floor with green")
    func successRespectsIssueFloor() {
        #expect(BackgroundSyncService.backgroundCompletionWidgetStatus(
            result: .synced, issueFloor: .warning
        ) == .attention)
        #expect(BackgroundSyncService.backgroundCompletionWidgetStatus(
            result: .synced, issueFloor: .critical
        ) == .attention)
        #expect(BackgroundSyncService.backgroundCompletionWidgetStatus(
            result: .alreadyIdle, issueFloor: .warning
        ) == .attention)
    }

    // Deliberate tier change, matching the header cascade (decision 012):
    // a failed background run surfaces in-app as an attention-tier issue,
    // so the widget shows the same amber, no longer a red "Sync Error".
    @Test("Every non-successful result maps to attention")
    func failuresMapToAttention() {
        for result in Self.allResults where !result.isSuccessful {
            #expect(BackgroundSyncService.backgroundCompletionWidgetStatus(
                result: result, issueFloor: .none
            ) == .attention)
        }
    }

    @Test("No result can reach green once a floor is recorded")
    func noGreenAboveFloor() {
        for result in Self.allResults {
            for floor in [WidgetSnapshotStore.IssueFloor.warning, .critical] {
                #expect(BackgroundSyncService.backgroundCompletionWidgetStatus(
                    result: result, issueFloor: floor
                ) != .synced)
            }
        }
    }

    // End-to-end wire trip, same guarantee #73 pinned for the foreground
    // write: the persisted value can never decode into the green branch.
    @Test("Persisted wire value decodes back to the same tier in the widget")
    func wireRoundTrip() {
        let attention = BackgroundSyncService.backgroundCompletionWidgetStatus(
            result: .synced, issueFloor: .warning
        )
        #expect(SyncStatus.fromWire(attention.wireValue) == .attention)

        let clean = BackgroundSyncService.backgroundCompletionWidgetStatus(
            result: .synced, issueFloor: .none
        )
        #expect(SyncStatus.fromWire(clean.wireValue) == .synced)
    }
}

/// `completeSync` used to read the folder count and synced-file events after
/// `cleanupBackgroundManaged` had already stopped the bridge (it reports an
/// empty folder list once stopped → the widget rendered "Vaults: 0"), and it
/// stamped the outcome timestamp as "last sync" for failed runs too.
@Suite("Background completion snapshot carries honest metrics (#76 follow-up)")
struct BackgroundCompletionSnapshotTests {
    private let previous = WidgetSnapshotStore.Snapshot(
        lastSyncTime: "2026-07-06T21:00:00Z",
        lastSyncDuration: 42,
        status: SyncStatus.synced.wireValue,
        filesSynced: 17,
        folderCount: 3
    )
    private let startedAt = Date(timeIntervalSince1970: 1_783_000_000)
    private let completedAt = Date(timeIntervalSince1970: 1_783_000_012)

    private func snapshot(
        result: BackgroundSyncService.SyncResult,
        bridgeRunning: Bool,
        previous: WidgetSnapshotStore.Snapshot?
    ) -> WidgetSnapshotStore.Snapshot {
        BackgroundSyncService.backgroundCompletionSnapshot(
            result: result,
            issueFloor: .none,
            completedAt: completedAt,
            startedAt: startedAt,
            bridgeRunning: bridgeRunning,
            liveFolderCount: 2,
            liveSyncedFiles: 5,
            previous: previous
        )
    }

    @Test("A successful run stamps its own last-sync triple and live counts")
    func successStampsFreshValues() {
        let snap = snapshot(result: .synced, bridgeRunning: true, previous: previous)
        #expect(snap.lastSyncTime == WidgetSnapshotStore.iso8601String(from: completedAt))
        #expect(snap.lastSyncDuration == 12)
        #expect(snap.filesSynced == 5)
        #expect(snap.folderCount == 2)
    }

    @Test("A failed run keeps the last real sync's triple, never its own timestamp")
    func failureCarriesPreviousTriple() {
        for result in [BackgroundSyncService.SyncResult.bridgeStartFailed, .notIdleBeforeDeadline, .settledWithFolderError] {
            let snap = snapshot(result: result, bridgeRunning: false, previous: previous)
            #expect(snap.lastSyncTime == previous.lastSyncTime)
            #expect(snap.lastSyncDuration == previous.lastSyncDuration)
            #expect(snap.filesSynced == previous.filesSynced)
        }
    }

    @Test("A failed run with no previous snapshot reports no sync, not a fake one")
    func failureWithoutPreviousIsEmpty() {
        let snap = snapshot(result: .failed, bridgeRunning: false, previous: nil)
        #expect(snap.lastSyncTime.isEmpty)
        #expect(snap.lastSyncDuration == 0)
        #expect(snap.filesSynced == 0)
    }

    // The reported symptom: a background-owned run stops the bridge, the
    // bridge then reports zero folders, and the widget showed "Vaults: 0".
    @Test("A stopped bridge never zeroes the folder count")
    func stoppedBridgeKeepsPreviousFolderCount() {
        let failed = snapshot(result: .failed, bridgeRunning: false, previous: previous)
        #expect(failed.folderCount == 3)
        let synced = snapshot(result: .synced, bridgeRunning: false, previous: previous)
        #expect(synced.folderCount == 3)
        let running = snapshot(result: .synced, bridgeRunning: true, previous: previous)
        #expect(running.folderCount == 2)
    }

    @Test("The status tier still derives from result and floor")
    func statusStillDerived() {
        let snap = BackgroundSyncService.backgroundCompletionSnapshot(
            result: .synced,
            issueFloor: .warning,
            completedAt: completedAt,
            startedAt: startedAt,
            bridgeRunning: true,
            liveFolderCount: 2,
            liveSyncedFiles: 5,
            previous: previous
        )
        #expect(snap.status == SyncStatus.attention.wireValue)
        #expect(snapshot(result: .failed, bridgeRunning: false, previous: previous).status == SyncStatus.attention.wireValue)
        #expect(snapshot(result: .synced, bridgeRunning: true, previous: previous).status == SyncStatus.synced.wireValue)
    }
}

@Suite("Durable widget issue floor (#76)")
struct DurableIssueFloorTests {
    private func floor(
        _ issues: [(kind: SyncthingManager.SyncIssueItem.Kind, severity: SyncthingManager.SyncIssueSeverity)],
        hasUnreachableFolders: Bool = false
    ) -> WidgetSnapshotStore.IssueFloor {
        SyncthingManager.durableIssueFloor(
            issues: issues,
            hasUnreachableFolders: hasUnreachableFolders
        )
    }

    @Test("No issues, no unreachable folders → none")
    func emptyIsNone() {
        #expect(floor([]) == .none)
    }

    @Test("Durable warning and critical kinds carry their severity")
    func durableKindsCarrySeverity() {
        #expect(floor([(.pendingShares, .warning)]) == .warning)
        #expect(floor([(.disconnectedPeers, .warning)]) == .warning)
        #expect(floor([(.conflicts, .warning)]) == .warning)
        #expect(floor([(.pathCollision, .critical)]) == .critical)
        #expect(floor([(.nestedFolders, .critical)]) == .critical)
        #expect(floor([(.folderErrors, .critical)]) == .critical)
        #expect(floor([(.pendingShares, .warning), (.pathCollision, .critical)]) == .critical)
    }

    // A successful background run resolves staleness by definition, and the
    // completion write knows the fresh background outcome — recording either
    // would stick a false amber only a foreground open could clear.
    @Test("Stale-sync and background-outcome issues never enter the floor")
    func selfResolvingKindsAreExcluded() {
        #expect(floor([(.staleSync, .warning)]) == .none)
        #expect(floor([(.backgroundSync, .critical)]) == .none)
        #expect(floor([(.staleSync, .warning), (.backgroundSync, .critical)]) == .none)
        #expect(floor([(.staleSync, .warning), (.pendingShares, .warning)]) == .warning)
    }

    @Test("Unreachable folders count as critical, mirroring the header")
    func unreachableFoldersAreCritical() {
        #expect(floor([], hasUnreachableFolders: true) == .critical)
    }

    @Test("Persisted floor decodes conservatively")
    func decodeIsConservative() {
        #expect(WidgetSnapshotStore.IssueFloor.decode(nil) == .none)
        #expect(WidgetSnapshotStore.IssueFloor.decode("none") == .none)
        #expect(WidgetSnapshotStore.IssueFloor.decode("warning") == .warning)
        #expect(WidgetSnapshotStore.IssueFloor.decode("critical") == .critical)
        // Unknown must never silently read as "no issues".
        #expect(WidgetSnapshotStore.IssueFloor.decode("future-tier") == .warning)
    }
}

/// The manager persists the floor alongside every snapshot write, so the
/// static background context always reads the foreground's latest view.
@Suite("Manager persists the issue floor with the snapshot write (#76)")
struct IssueFloorWiringTests {
    @MainActor
    private func makeManager(lastSync: Date) -> SyncthingManager {
        let defaults = TestSupport.makeIsolatedDefaults(label: "IssueFloorWiring")
        let store = SyncHistoryStore(defaults: defaults, storageKey: "history")
        store.save(globalLastSync: lastSync, lastSyncByFolder: [:])
        let manager = SyncthingManager(syncHistoryStore: store)
        manager._testSetLastBackgroundSyncOutcome(nil)
        manager._testSetRunning(true)
        manager._testSetFolders([
            SyncthingManager.FolderInfo(
                id: "vault-a",
                label: "Vault A",
                path: "/tmp/issuefloor/vault-a",
                type: "sendreceive",
                paused: false,
                deviceIDs: []
            ),
        ])
        return manager
    }

    private func errorStatus() -> SyncthingManager.FolderStatusInfo {
        SyncthingManager.FolderStatusInfo(payload: .init(
            state: "error",
            stateChanged: "2026-07-07T10:00:00Z",
            completionPct: 0,
            globalBytes: 0,
            globalFiles: 0,
            localBytes: 0,
            localFiles: 0,
            needBytes: 0,
            needFiles: 0,
            inProgressBytes: 0,
            errorReason: "unknown_error",
            errorMessage: "database is locked",
            errorPath: nil,
            errorChanged: nil
        ))
    }

    @MainActor
    @Test("A folder error is recorded as a critical floor")
    func folderErrorRecordsCriticalFloor() {
        let manager = makeManager(lastSync: Date())
        manager._testSetFolderStatuses(["vault-a": errorStatus()])
        manager._testWriteWidgetSnapshot()
        #expect(manager._testLastWrittenIssueFloor() == .critical)
    }

    @MainActor
    @Test("A stale-sync warning colors the snapshot but never the floor")
    func staleSyncStaysOutOfTheFloor() {
        let manager = makeManager(lastSync: Date(timeIntervalSinceNow: -13 * 3600))
        manager._testWriteWidgetSnapshot()
        // The foreground write is honest about staleness…
        #expect(manager._testLastWrittenWidgetSnapshot()?.status == SyncStatus.attention.wireValue)
        // …but a successful background sync resolves it, so it must not
        // survive into the floor the background write folds in.
        #expect(manager._testLastWrittenIssueFloor() == WidgetSnapshotStore.IssueFloor.none)
    }

    @MainActor
    @Test("A clean state records an empty floor")
    func cleanStateRecordsNoFloor() {
        let manager = makeManager(lastSync: Date())
        manager._testWriteWidgetSnapshot()
        #expect(manager._testLastWrittenIssueFloor() == WidgetSnapshotStore.IssueFloor.none)
        #expect(manager._testLastWrittenWidgetSnapshot()?.status == SyncStatus.synced.wireValue)
    }
}
