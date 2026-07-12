import Foundation
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

    @Test("Silent push restart requires fresh local data progress before success")
    func silentPushRestartRequiresLocalDataProgress() {
        let startedAt = SyncBridgeService.parseBridgeTimestamp("2027-01-15T08:00:00.100Z")!
        var tracker = BackgroundSyncService.SilentPushProgressTracker(
            lastEventID: 10,
            startedAt: startedAt
        )
        tracker.requiresLocalDataProgress = true

        let nonProgressSnapshot = tracker.observe([
            .init(id: 11, type: "DeviceConnected", time: "2027-01-15T08:00:01Z", data: nil),
            .init(id: 12, type: "StateChanged", time: "2027-01-15T08:00:01Z", data: ["from": "idle", "to": "idle"]),
            .init(id: 13, type: "StateChanged", time: "2027-01-15T08:00:01Z", data: ["from": "idle", "to": "scanning"]),
            .init(id: 14, type: "LocalIndexUpdated", time: "2027-01-15T08:00:01Z", data: nil),
            .init(id: 15, type: "RemoteIndexUpdated", time: "2027-01-15T08:00:01Z", data: nil),
        ])

        #expect(nonProgressSnapshot.requiresLocalDataProgress)
        #expect(!nonProgressSnapshot.sawLocalDataProgress)
        #expect(nonProgressSnapshot.lastEventID == 15)

        let progressSnapshot = tracker.observe([
            .init(
                id: 16,
                type: "ItemFinished",
                time: "2027-01-15T08:00:01.123456789Z",
                data: ["folder": "folder-a", "type": "file", "action": "update", "error": ""]
            ),
        ])

        #expect(!progressSnapshot.requiresLocalDataProgress)
        #expect(progressSnapshot.sawLocalDataProgress)
        #expect(progressSnapshot.lastEventID == 16)
    }

    @Test("Failed, directory, and metadata item events are not local data progress")
    func weakItemEventsDoNotCountAsDataProgress() {
        let startedAt = SyncBridgeService.parseBridgeTimestamp("2027-01-15T08:00:00Z")!
        var tracker = BackgroundSyncService.SilentPushProgressTracker(
            lastEventID: 20,
            startedAt: startedAt
        )
        tracker.requiresLocalDataProgress = true

        let snapshot = tracker.observe([
            .init(id: 21, type: "ItemFinished", time: "2027-01-15T08:00:01Z", data: ["folder": "a", "type": "file", "action": "update", "error": "denied"]),
            .init(id: 22, type: "ItemFinished", time: "2027-01-15T08:00:01Z", data: ["folder": "a", "type": "dir", "action": "update"]),
            .init(id: 23, type: "ItemFinished", time: "2027-01-15T08:00:01Z", data: ["folder": "a", "type": "file", "action": "metadata"]),
        ])

        #expect(snapshot.requiresLocalDataProgress)
        #expect(!snapshot.sawLocalDataProgress)
    }

    @Test("Cursor and production time must both be fresh")
    func cursorAndEventTimeGateProgress() {
        let startedAt = SyncBridgeService.parseBridgeTimestamp("2027-01-15T08:00:00.500Z")!
        var tracker = BackgroundSyncService.SilentPushProgressTracker(
            lastEventID: 30,
            startedAt: startedAt
        )

        let stale = tracker.observe([
            .init(
                id: 31,
                type: "ItemFinished",
                time: "2027-01-15T08:00:00.499999999Z",
                data: ["folder": "a", "type": "file", "action": "update"]
            ),
        ])
        #expect(!stale.sawLocalDataProgress)

        let fresh = tracker.observe([
            .init(
                id: 31,
                type: "ItemFinished",
                time: "2027-01-15T08:00:00.600Z",
                data: ["folder": "a", "type": "file", "action": "update"]
            ),
            .init(
                id: 32,
                type: "ItemFinished",
                time: "2027-01-15T08:00:00.600Z",
                data: ["folder": "a", "type": "file", "action": "update"]
            ),
        ])
        #expect(fresh.sawLocalDataProgress)
        #expect(fresh.lastEventID == 32)
    }

    @Test("Legacy background detail is never surfaced or copied")
    func legacyOutcomeIsSanitized() throws {
        let defaults = TestSupport.makeIsolatedDefaults(label: "legacy-background-outcome")
        let legacy = BackgroundSyncService.SyncOutcome(
            timestamp: Date(timeIntervalSince1970: 1_800_000_000),
            triggerReason: "/private/vault/device-id",
            result: .bridgeStartFailed,
            detail: "token jws /private/vault/path",
            localDataProgressObserved: nil
        )
        defaults.set(try JSONEncoder().encode(legacy), forKey: "background-sync-last-outcome-v1")

        let sanitized = BackgroundSyncService.lastSyncOutcome(defaults: defaults)

        #expect(sanitized?.timestamp == legacy.timestamp)
        #expect(sanitized?.result == legacy.result)
        #expect(sanitized?.triggerReason == "background")
        #expect(sanitized?.detail == nil)
        #expect(sanitized?.localDataProgressObserved == nil)
        #expect(defaults.data(forKey: "background-sync-last-outcome-v1") == nil)
        let migratedData = try #require(defaults.data(forKey: "background-sync-last-outcome-v2"))
        let migrated = try JSONDecoder().decode(BackgroundSyncService.SyncOutcome.self, from: migratedData)
        #expect(migrated == sanitized)
        #expect(!String(decoding: migratedData, as: UTF8.self).contains("/private/vault"))
        #expect(!String(decoding: migratedData, as: UTF8.self).lowercased().contains("jws"))

        defaults.set(try JSONEncoder().encode(legacy), forKey: "background-sync-last-outcome-v1")
        #expect(BackgroundSyncService.lastSyncOutcome(defaults: defaults) == migrated)
        #expect(defaults.data(forKey: "background-sync-last-outcome-v1") == nil)
    }
}
