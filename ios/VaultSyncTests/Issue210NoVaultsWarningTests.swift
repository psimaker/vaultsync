import Foundation
import Testing
@testable import VaultSync

/// #210: a background run that found no vaults left "Background Sync Found No
/// Vaults" on top of an up-to-date vault for good — nothing ever checked the
/// folder list again. The warning now goes once a fresh read of the running
/// engine, taken after the outcome, lists vaults; nothing else retires it.
@Suite("A resolved background no-vaults warning goes away (#210)")
struct Issue210NoVaultsWarningTests {
    private static let outcomeTime = Date(timeIntervalSince1970: 1_800_000_000)

    private static func outcome(
        _ result: BackgroundSyncService.SyncResult,
        at timestamp: Date = outcomeTime
    ) -> BackgroundSyncService.SyncOutcome {
        BackgroundSyncService.SyncOutcome(
            timestamp: timestamp,
            triggerReason: "app-refresh",
            result: result,
            detail: nil,
            localDataProgressObserved: false
        )
    }

    private static let oneVaultJSON = """
    [{"id":"vault-a","label":"Vault A","path":"/tmp/issue210/vault-a","type":"sendreceive","paused":false,"deviceIDs":[]}]
    """

    private static let vaultA = SyncthingManager.FolderInfo(
        id: "vault-a",
        label: "Vault A",
        path: "/tmp/issue210/vault-a",
        type: "sendreceive",
        paused: false,
        deviceIDs: []
    )

    @MainActor
    private func makeManager(
        outcome: BackgroundSyncService.SyncOutcome?
    ) -> SyncthingManager {
        let defaults = TestSupport.makeIsolatedDefaults(label: "Issue210")
        let store = SyncHistoryStore(defaults: defaults, storageKey: "history")
        store.save(globalLastSync: Date(), lastSyncByFolder: [:])
        let manager = SyncthingManager(syncHistoryStore: store)
        // Init restores the outcome from UserDefaults.standard, which parallel
        // suites share — set it explicitly.
        manager._testSetLastBackgroundSyncOutcome(outcome)
        manager._testSetRunning(true)
        return manager
    }

    @MainActor
    private func backgroundIssue(_ manager: SyncthingManager) -> SyncthingManager.SyncIssueItem? {
        manager.unresolvedIssues.first { $0.kind == .backgroundSync }
    }

    /// A read time on the monotonic clock, relative to when the manager
    /// first held its current outcome.
    @MainActor
    private static func seen(_ manager: SyncthingManager, _ offset: TimeInterval) -> TimeInterval {
        manager._testBackgroundOutcomeSeenAt() + offset
    }

    // MARK: - The manager

    @MainActor
    @Test("Empty, then a vault: a fresh read after the outcome retires the warning")
    func freshReadWithVaultsRetiresTheWarning() {
        let manager = makeManager(outcome: Self.outcome(.noFoldersConfigured))
        #expect(backgroundIssue(manager)?.title == L10n.tr("Background Sync Found No Vaults"))

        manager._testApplyPolledFolderRead(
            json: Self.oneVaultJSON,
            readStartedAt: Self.seen(manager, 5),
            engineRunningAcrossRead: true
        )

        #expect(backgroundIssue(manager) == nil)
        #expect(manager.folders.map(\.id) == ["vault-a"])
    }

    @MainActor
    @Test("The outcome itself stays as it was: activity and diagnostics keep it")
    func resolutionKeepsTheOutcome() {
        let original = Self.outcome(.noFoldersConfigured)
        let manager = makeManager(outcome: original)

        manager._testApplyPolledFolderRead(
            json: Self.oneVaultJSON,
            readStartedAt: Self.seen(manager, 5),
            engineRunningAcrossRead: true
        )

        #expect(manager.lastBackgroundSyncOutcome == original)
        #expect(manager.lastBackgroundSyncOutcome?.result == .noFoldersConfigured)
        #expect(BackgroundSyncService.SyncResult.noFoldersConfigured.isSuccessful == false)
    }

    @MainActor
    @Test("Cached folders without a fresh read never retire it")
    func cachedFoldersDoNotRetireIt() {
        let manager = makeManager(outcome: Self.outcome(.noFoldersConfigured))
        manager._testSetFolders([Self.vaultA])
        #expect(backgroundIssue(manager) != nil)
    }

    @MainActor
    @Test("A read that started before the outcome never retires it")
    func readOlderThanTheOutcomeDoesNotRetireIt() {
        let manager = makeManager(outcome: Self.outcome(.noFoldersConfigured))
        manager._testApplyPolledFolderRead(
            json: Self.oneVaultJSON,
            readStartedAt: Self.seen(manager, -1),
            engineRunningAcrossRead: true
        )
        #expect(backgroundIssue(manager) != nil)
        manager._testApplyPolledFolderRead(
            json: Self.oneVaultJSON,
            readStartedAt: Self.seen(manager, 0),
            engineRunningAcrossRead: true
        )
        #expect(backgroundIssue(manager) != nil)
    }

    @MainActor
    @Test("An empty read, an unreadable read or a stopped engine never retires it")
    func emptyUnreadableOrStoppedReadsDoNotRetireIt() {
        let manager = makeManager(outcome: Self.outcome(.noFoldersConfigured))
        manager._testApplyPolledFolderRead(json: "[]", readStartedAt: Self.seen(manager, 5), engineRunningAcrossRead: true)
        #expect(backgroundIssue(manager) != nil)
        manager._testApplyPolledFolderRead(json: "not json", readStartedAt: Self.seen(manager, 5), engineRunningAcrossRead: true)
        #expect(backgroundIssue(manager) != nil)
        manager._testApplyPolledFolderRead(json: "", readStartedAt: Self.seen(manager, 5), engineRunningAcrossRead: true)
        #expect(backgroundIssue(manager) != nil)
        manager._testApplyPolledFolderRead(
            json: Self.oneVaultJSON,
            readStartedAt: Self.seen(manager, 5),
            engineRunningAcrossRead: false
        )
        #expect(backgroundIssue(manager) != nil)
    }

    @MainActor
    @Test("A newer no-vaults outcome surfaces again until a read after it")
    func newerNoVaultsOutcomeSurfacesAgain() {
        let manager = makeManager(outcome: Self.outcome(.noFoldersConfigured))
        manager._testApplyPolledFolderRead(json: Self.oneVaultJSON, readStartedAt: Self.seen(manager, 5), engineRunningAcrossRead: true)
        #expect(backgroundIssue(manager) == nil)

        let newer = Self.outcome(.noFoldersConfigured, at: Self.outcomeTime.addingTimeInterval(60))
        manager._testSetLastBackgroundSyncOutcome(newer)
        // Folders are still in the cache from the earlier read: they prove
        // nothing about the newer outcome.
        #expect(backgroundIssue(manager) != nil)

        // A read that began before the newer outcome was seen does not
        // answer it.
        manager._testApplyPolledFolderRead(
            json: Self.oneVaultJSON,
            readStartedAt: Self.seen(manager, -1),
            engineRunningAcrossRead: true
        )
        #expect(backgroundIssue(manager) != nil)

        manager._testApplyPolledFolderRead(
            json: Self.oneVaultJSON,
            readStartedAt: Self.seen(manager, 1),
            engineRunningAcrossRead: true
        )
        #expect(backgroundIssue(manager) == nil)
    }

    @MainActor
    @Test("A newer failure of another kind surfaces in full")
    func newerFailureSurfaces() {
        let manager = makeManager(outcome: Self.outcome(.noFoldersConfigured))
        manager._testApplyPolledFolderRead(json: Self.oneVaultJSON, readStartedAt: Self.seen(manager, 5), engineRunningAcrossRead: true)

        manager._testSetLastBackgroundSyncOutcome(
            Self.outcome(.bridgeStartFailed, at: Self.outcomeTime.addingTimeInterval(60))
        )
        manager._testApplyPolledFolderRead(
            json: Self.oneVaultJSON,
            readStartedAt: Self.seen(manager, 5),
            engineRunningAcrossRead: true
        )
        let issue = backgroundIssue(manager)
        #expect(issue?.severity == .critical)
        #expect(issue?.title == L10n.tr("Background Sync Could Not Start"))
    }

    @MainActor
    @Test("Only a no-vaults outcome is retired by a read; other warnings stay")
    func otherOutcomesAreNotRetired() {
        for result: BackgroundSyncService.SyncResult in [.notIdleBeforeDeadline, .bridgeStartFailed, .noBookmarkAccess, .failed] {
            let manager = makeManager(outcome: Self.outcome(result))
            manager._testApplyPolledFolderRead(json: Self.oneVaultJSON, readStartedAt: Self.seen(manager, 5), engineRunningAcrossRead: true)
            #expect(backgroundIssue(manager) != nil, "result \(result.rawValue)")
        }
    }

    @MainActor
    @Test("After a relaunch the warning shows until the new engine's first fresh read")
    func relaunchShowsTheWarningUntilAFreshRead() {
        let persisted = Self.outcome(.noFoldersConfigured)
        let first = makeManager(outcome: persisted)
        first._testApplyPolledFolderRead(json: Self.oneVaultJSON, readStartedAt: Self.seen(first, 5), engineRunningAcrossRead: true)
        #expect(backgroundIssue(first) == nil)

        // The resolution lives in memory only: the persisted outcome is the
        // same, and a new process has read nothing yet.
        let relaunched = makeManager(outcome: persisted)
        relaunched._testSetFolders([Self.vaultA])
        #expect(backgroundIssue(relaunched) != nil)

        relaunched._testApplyPolledFolderRead(
            json: Self.oneVaultJSON,
            readStartedAt: Self.seen(relaunched, 5),
            engineRunningAcrossRead: true
        )
        #expect(backgroundIssue(relaunched) == nil)
    }

    @MainActor
    @Test("Other issues stay after the warning is retired")
    func otherIssuesStay() {
        let manager = makeManager(outcome: Self.outcome(.noFoldersConfigured))
        manager._testApplyPolledFolderRead(json: Self.oneVaultJSON, readStartedAt: Self.seen(manager, 5), engineRunningAcrossRead: true)
        manager._testSetFolderStatuses(["vault-a": SyncthingManager.FolderStatusInfo(payload: .init(
            state: "error",
            stateChanged: "2026-11-15T10:00:00Z",
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
        ))])

        #expect(backgroundIssue(manager) == nil)
        #expect(manager.unresolvedIssues.contains { $0.severity == .critical })
        manager._testWriteWidgetSnapshot()
        // A critical issue is the cascade's "Sync Issue" tier (decision 012).
        #expect(manager._testLastWrittenWidgetSnapshot()?.status == SyncStatus.attention.wireValue)
        #expect(manager._testLastWrittenIssueFloor() == .critical)
    }

    @MainActor
    @Test("The foreground widget follows the retired warning")
    func foregroundWidgetFollows() {
        let manager = makeManager(outcome: Self.outcome(.noFoldersConfigured))
        manager._testSetFolders([Self.vaultA])
        manager._testWriteWidgetSnapshot()
        #expect(manager._testLastWrittenWidgetSnapshot()?.status == SyncStatus.attention.wireValue)

        manager._testApplyPolledFolderRead(json: Self.oneVaultJSON, readStartedAt: Self.seen(manager, 5), engineRunningAcrossRead: true)
        manager._testWriteWidgetSnapshot()
        #expect(manager._testLastWrittenWidgetSnapshot()?.status == SyncStatus.synced.wireValue)
        // The background-sync kind never reached the floor; it still does not.
        #expect(manager._testLastWrittenIssueFloor() == WidgetSnapshotStore.IssueFloor.none)
    }

    @MainActor
    @Test("A wall clock set back does not hold the warning: the order is the monotonic clock's")
    func wallClockSetBackDoesNotHoldTheWarning() {
        // Recorded while the clock ran a day ahead, read after it was set back.
        let manager = makeManager(outcome: Self.outcome(.noFoldersConfigured, at: Date().addingTimeInterval(86_400)))
        manager._testApplyPolledFolderRead(
            json: Self.oneVaultJSON,
            readStartedAt: Self.seen(manager, 1),
            engineRunningAcrossRead: true
        )
        #expect(backgroundIssue(manager) == nil)
    }

    // MARK: - The pure rule

    @Test("The rule: a no-vaults outcome, a running engine, vaults, a read after the outcome")
    func pureRule() {
        let noVaults = Self.outcome(.noFoldersConfigured)
        func resolves(
            _ outcome: BackgroundSyncService.SyncOutcome = noVaults,
            readAt: TimeInterval = 105,
            running: Bool = true,
            count: Int = 1
        ) -> Bool {
            SyncthingManager.freshFolderReadResolves(
                outcome: outcome,
                outcomeSeenAt: 100,
                readStartedAt: readAt,
                engineRunningAcrossRead: running,
                folderCount: count
            )
        }
        #expect(resolves())
        #expect(!resolves(count: 0))
        #expect(!resolves(running: false))
        #expect(!resolves(readAt: 100))
        #expect(!resolves(readAt: 40))
        for other: BackgroundSyncService.SyncResult in [
            .synced, .alreadyIdle, .noBookmarkAccess, .bridgeStartFailed,
            .notIdleBeforeDeadline, .failed, .settledWithFolderError,
        ] {
            #expect(!resolves(Self.outcome(other)), "result \(other.rawValue)")
        }
    }

    // MARK: - The background run's side

    @Test("A background folder read tells no vaults from a stopped engine and an unreadable list")
    func backgroundFolderAvailability() {
        typealias Availability = BackgroundSyncService.FolderAvailability
        let classify = BackgroundSyncService.folderAvailability(foldersJSON:engineRunningAcrossRead:)
        #expect(classify(#"[{"id":"vault-a"}]"#, true) == Availability.configured)
        // A non-empty list only comes from an engine that was up for the read.
        #expect(classify(#"[{"id":"vault-a"}]"#, false) == Availability.configured)
        #expect(classify("[]", true) == Availability.verifiedEmpty)
        #expect(classify("[]", false) == Availability.engineStopped)
        #expect(classify("not json", true) == Availability.unreadable)
        #expect(classify("", true) == Availability.unreadable)
        #expect(classify("not json", false) == Availability.engineStopped)
    }

    @Test("Each way of finding no vaults keeps the no-vaults result and says which it was")
    func backgroundOutcomeDetails() {
        typealias Availability = BackgroundSyncService.FolderAvailability
        #expect(Availability.configured.noVaultsDetail == nil)
        #expect(Availability.verifiedEmpty.noVaultsDetail == L10n.tr("No folders were available for background sync."))
        #expect(Availability.engineStopped.noVaultsDetail == L10n.tr("The sync engine stopped before its vaults could be read."))
        #expect(Availability.unreadable.noVaultsDetail == L10n.tr("The sync engine's vault list could not be read."))
    }

    @Test("The remediation holds for every way of finding no vaults")
    func remediationHoldsForEveryWay() {
        #expect(
            BackgroundSyncService.SyncResult.noFoldersConfigured.remediation
                == L10n.tr("If this iPhone has no vault yet, accept or create a shared vault. If your vaults are there, open VaultSync — this goes away once it lists them.")
        )
    }

    @Test("Background completion severity and the issue floor are unchanged")
    func backgroundCompletionSeverityIsUnchanged() {
        #expect(
            BackgroundSyncService.backgroundCompletionWidgetStatus(result: .noFoldersConfigured, issueFloor: .none)
                == .attention
        )
        #expect(
            SyncthingManager.durableIssueFloor(
                issues: [(kind: .backgroundSync, severity: .warning)],
                hasUnreachableFolders: false
            ) == WidgetSnapshotStore.IssueFloor.none
        )
    }
}
