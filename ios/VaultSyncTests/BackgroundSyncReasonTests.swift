import Testing
@testable import VaultSync

@Suite("Background Sync Reason Codes")
struct BackgroundSyncReasonTests {
    @Test("Successful outcomes do not surface unresolved issues")
    func successfulOutcomesStaySilent() {
        #expect(BackgroundSyncService.SyncResult.synced.isSuccessful)
        #expect(BackgroundSyncService.SyncResult.alreadyIdle.isSuccessful)
        #expect(!BackgroundSyncService.SyncResult.synced.shouldSurfaceIssue)
        #expect(!BackgroundSyncService.SyncResult.alreadyIdle.shouldSurfaceIssue)
    }

    @Test("Reasoned failure outcomes surface issue metadata")
    func failureOutcomesExposeIssueContract() {
        let failures: [BackgroundSyncService.SyncResult] = [
            .noBookmarkAccess,
            .noFoldersConfigured,
            .bridgeStartFailed,
            .notIdleBeforeDeadline,
            .failed,
        ]

        for result in failures {
            #expect(!result.isSuccessful)
            #expect(result.shouldSurfaceIssue)
            #expect(!result.issueTitle.isEmpty)
            #expect(!result.issueMessage.isEmpty)
            #expect(!result.remediation.isEmpty)
        }
    }

    @Test("Specific reason code copy remains actionable")
    func specificReasonCodeCopy() {
        #expect(BackgroundSyncService.SyncResult.noBookmarkAccess.issueTitle == L10n.tr("Background Sync Could Not Access Obsidian"))
        #expect(BackgroundSyncService.SyncResult.noFoldersConfigured.issueTitle == L10n.tr("Background Sync Found No Vaults"))
        #expect(BackgroundSyncService.SyncResult.bridgeStartFailed.issueTitle == L10n.tr("Background Sync Could Not Start"))
        #expect(BackgroundSyncService.SyncResult.notIdleBeforeDeadline.issueTitle == L10n.tr("Background Sync Timed Out"))
        #expect(
            BackgroundSyncService.SyncResult.noBookmarkAccess.remediation
                == L10n.tr("Reconnect your Obsidian folder access in VaultSync, then run a foreground rescan.")
        )
    }

    @Test("Silent push restart requires real sync progress before success")
    func silentPushRestartRequiresMeaningfulProgress() {
        var tracker = BackgroundSyncService.SilentPushProgressTracker(lastEventID: 10)
        tracker.requiresMeaningfulProgress = true

        let nonProgressSnapshot = tracker.observe([
            .init(id: 11, type: "DeviceConnected", data: nil),
            .init(id: 12, type: "StateChanged", data: ["from": "idle", "to": "idle"]),
        ])

        #expect(nonProgressSnapshot.requiresMeaningfulProgress)
        #expect(!nonProgressSnapshot.sawMeaningfulProgress)
        #expect(nonProgressSnapshot.lastEventID == 12)

        let progressSnapshot = tracker.observe([
            .init(id: 13, type: "RemoteIndexUpdated", data: nil),
        ])

        #expect(!progressSnapshot.requiresMeaningfulProgress)
        #expect(progressSnapshot.sawMeaningfulProgress)
        #expect(progressSnapshot.lastEventID == 13)
    }
}
