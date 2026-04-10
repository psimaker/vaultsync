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
        #expect(BackgroundSyncService.SyncResult.noBookmarkAccess.issueTitle == "Background Sync Could Not Access Obsidian")
        #expect(BackgroundSyncService.SyncResult.noFoldersConfigured.issueTitle == "Background Sync Found No Vaults")
        #expect(BackgroundSyncService.SyncResult.bridgeStartFailed.issueTitle == "Background Sync Could Not Start")
        #expect(BackgroundSyncService.SyncResult.notIdleBeforeDeadline.issueTitle == "Background Sync Timed Out")
        #expect(BackgroundSyncService.SyncResult.noBookmarkAccess.remediation.contains("Reconnect your Obsidian folder"))
    }
}
