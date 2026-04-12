import BackgroundTasks
import Foundation
import UIKit
import UserNotifications
import os

private let logger = Logger(subsystem: "eu.vaultsync.app", category: "background")

/// Wrapper to pass non-Sendable BGTask types across concurrency boundaries.
/// Safe because BGTask ownership is transferred from the system exclusively to our handler.
private struct UnsafeSendable<T>: @unchecked Sendable {
    let value: T
}

/// Tracks whether the foreground (SyncthingManager) owns the Syncthing lifecycle.
/// Both SyncthingManager and BackgroundSyncService coordinate through this lock
/// to prevent races where background stops a foreground-managed instance or vice versa.
struct SyncLifecycleState: Sendable {
    var foregroundActive = false
}

/// Manages background sync via BGAppRefreshTask and BGContinuedProcessingTask.
enum BackgroundSyncService {

    static let appRefreshIdentifier = "eu.vaultsync.app.sync-refresh"
    static let continuedProcessingIdentifier = "eu.vaultsync.app.sync-continued"

    /// Shared lock coordinating Syncthing bridge start/stop across foreground and background.
    static let lifecycleLock = OSAllocatedUnfairLock(initialState: SyncLifecycleState())

    /// Guards the UIApplication background-task assertion used for the
    /// scene-phase grace period after the app leaves the foreground.
    private static let backgroundAssertionLock = OSAllocatedUnfairLock(
        initialState: UIBackgroundTaskIdentifier.invalid
    )

    /// Whether each background task type was successfully registered.
    /// Written once during app launch, read-only afterwards — safe without synchronization.
    nonisolated(unsafe) private(set) static var appRefreshRegistered = false
    nonisolated(unsafe) private(set) static var continuedProcessingRegistered = false
    private static let lastSyncOutcomeStorageKey = "background-sync-last-outcome-v1"
    static let lastSyncOutcomeDidChangeNotification = Notification.Name("BackgroundSyncLastOutcomeDidChange")

    struct SyncOutcome: Codable, Equatable, Sendable {
        let timestamp: Date
        let triggerReason: String
        let result: SyncResult
        let detail: String?
    }

    // MARK: - Registration

    /// Register background task handlers. Must be called before app finishes launching.
    /// Registration may fail in the Simulator (ESRCH) — failures are non-fatal.
    static func registerTasks() {
        appRefreshRegistered = BGTaskScheduler.shared.register(
            forTaskWithIdentifier: appRefreshIdentifier,
            using: nil
        ) { task in
            guard let refreshTask = task as? BGAppRefreshTask else { return }
            let wrapped = UnsafeSendable(value: refreshTask)
            Task { await handleAppRefresh(task: wrapped.value) }
        }

        if !appRefreshRegistered {
            logger.warning("Failed to register app refresh task — background refresh unavailable")
        }

        continuedProcessingRegistered = BGTaskScheduler.shared.register(
            forTaskWithIdentifier: continuedProcessingIdentifier,
            using: nil
        ) { task in
            guard let processingTask = task as? BGContinuedProcessingTask else { return }
            let wrapped = UnsafeSendable(value: processingTask)
            Task { await handleContinuedProcessing(task: wrapped.value) }
        }

        if !continuedProcessingRegistered {
            logger.warning("Failed to register continued processing task — continued processing unavailable (expected in Simulator)")
        }

        logger.info("Background task registration complete (refresh=\(appRefreshRegistered), continued=\(continuedProcessingRegistered))")
    }

    // MARK: - Lifecycle lock / background assertion

    /// Release the foreground lifecycle lock. Call when the scene leaves the
    /// foreground so that silent-push and BGAppRefresh handlers can manage
    /// the Syncthing bridge. Without this, `performBackgroundSync` stays in
    /// `backgroundManaged=false` mode and never reconnects dead sockets.
    static func releaseForegroundLifecycleLock() {
        lifecycleLock.withLock { $0.foregroundActive = false }
        logger.info("Foreground lifecycle lock released")
    }

    /// Begin a UIApplication background-task assertion so iOS grants up to
    /// ~30 seconds of continued execution after the app is backgrounded.
    /// Without this the system can suspend the process within ~5s, severing
    /// Syncthing's peer connections before any scheduled BG task runs.
    /// Safe to call more than once — ends any previous assertion first.
    static func beginBackgroundAssertion() {
        endBackgroundAssertion()
        let newID = UIApplication.shared.beginBackgroundTask(withName: "VaultSync-Background-Grace") {
            endBackgroundAssertion()
        }
        backgroundAssertionLock.withLock { $0 = newID }
        if newID == .invalid {
            logger.warning("beginBackgroundTask returned .invalid — iOS denied the assertion")
        } else {
            logger.info("Background assertion acquired (id=\(newID.rawValue))")
        }
    }

    /// End the UIApplication background-task assertion if one is active.
    /// Call when the scene becomes active again so iOS can reclaim resources.
    static func endBackgroundAssertion() {
        let previous = backgroundAssertionLock.withLock { current -> UIBackgroundTaskIdentifier in
            let value = current
            current = .invalid
            return value
        }
        guard previous != .invalid else { return }
        UIApplication.shared.endBackgroundTask(previous)
        logger.info("Background assertion released (id=\(previous.rawValue))")
    }

    // MARK: - Scheduling

    /// Schedule periodic background refresh (~15 min). Skips if no vaults configured.
    static func scheduleAppRefresh() {
        guard appRefreshRegistered else { return }

        let hasAccess = BookmarkService.resolveBookmark(identifier: "obsidian-root") != nil
        guard hasAccess else {
            logger.info("No Obsidian directory configured, skipping refresh schedule")
            return
        }

        let request = BGAppRefreshTaskRequest(identifier: appRefreshIdentifier)
        request.earliestBeginDate = Date(timeIntervalSinceNow: 15 * 60)

        do {
            try BGTaskScheduler.shared.submit(request)
            logger.info("App refresh scheduled")
        } catch {
            logger.error("Could not schedule app refresh: \(error)")
        }
    }

    /// Submit continued processing for active sync. Call from foreground only.
    static func submitContinuedProcessing() {
        guard continuedProcessingRegistered else { return }

        let request = BGContinuedProcessingTaskRequest(
            identifier: continuedProcessingIdentifier,
            title: "Syncing Vault",
            subtitle: "Synchronizing your Obsidian vault..."
        )

        do {
            try BGTaskScheduler.shared.submit(request)
            logger.info("Continued processing submitted")
        } catch {
            logger.error("Could not submit continued processing: \(error)")
        }
    }

    /// Cancel pending continued processing task.
    static func cancelContinuedProcessing() {
        guard continuedProcessingRegistered else { return }
        BGTaskScheduler.shared.cancel(taskRequestWithIdentifier: continuedProcessingIdentifier)
    }

    // MARK: - Notifications

    /// Request notification permission for background conflict alerts.
    @discardableResult
    static func requestNotificationPermission() async -> Bool {
        do {
            return try await UNUserNotificationCenter.current()
                .requestAuthorization(options: [.alert, .sound, .badge])
        } catch {
            logger.error("Notification permission failed: \(error)")
            return false
        }
    }

    // MARK: - Shared Sync Logic

    /// Result of a background sync operation.
    enum SyncResult: String, Codable, Sendable {
        case synced
        case alreadyIdle
        case noBookmarkAccess
        case noFoldersConfigured
        case bridgeStartFailed
        case notIdleBeforeDeadline
        case failed

        var isSuccessful: Bool {
            switch self {
            case .synced, .alreadyIdle:
                return true
            case .noBookmarkAccess, .noFoldersConfigured, .bridgeStartFailed, .notIdleBeforeDeadline, .failed:
                return false
            }
        }

        var shouldSurfaceIssue: Bool {
            switch self {
            case .synced, .alreadyIdle:
                return false
            case .noBookmarkAccess, .noFoldersConfigured, .bridgeStartFailed, .notIdleBeforeDeadline, .failed:
                return true
            }
        }

        var issueTitle: String {
            switch self {
            case .noBookmarkAccess:
                return "Background Sync Could Not Access Obsidian"
            case .noFoldersConfigured:
                return "Background Sync Found No Vaults"
            case .bridgeStartFailed:
                return "Background Sync Could Not Start"
            case .notIdleBeforeDeadline:
                return "Background Sync Timed Out"
            case .failed:
                return "Background Sync Failed"
            case .synced, .alreadyIdle:
                return "Background Sync Completed"
            }
        }

        var issueMessage: String {
            switch self {
            case .noBookmarkAccess:
                return "VaultSync could not restore bookmark access for the Obsidian folder during a background run."
            case .noFoldersConfigured:
                return "No Syncthing folders were available to sync in the background."
            case .bridgeStartFailed:
                return "The embedded Syncthing bridge did not start for a background sync."
            case .notIdleBeforeDeadline:
                return "Background sync did not reach an idle folder state before the iOS deadline."
            case .failed:
                return "Background sync ended with an unexpected failure."
            case .synced:
                return "Background sync completed and reached idle."
            case .alreadyIdle:
                return "Background sync ran, but folders were already idle."
            }
        }

        var remediation: String {
            switch self {
            case .noBookmarkAccess:
                return "Reconnect your Obsidian folder access in VaultSync, then run a foreground rescan."
            case .noFoldersConfigured:
                return "Accept or create a shared vault before relying on background sync."
            case .bridgeStartFailed:
                return "Open VaultSync once to restart Syncthing, then retry."
            case .notIdleBeforeDeadline:
                return "Open VaultSync to allow a longer foreground sync session."
            case .failed:
                return "Retry from the app and review relay/background diagnostics in Settings."
            case .synced, .alreadyIdle:
                return "No action needed."
            }
        }
    }

    static func lastSyncOutcome() -> SyncOutcome? {
        guard let data = UserDefaults.standard.data(forKey: lastSyncOutcomeStorageKey),
              let decoded = try? JSONDecoder().decode(SyncOutcome.self, from: data) else {
            return nil
        }
        return decoded
    }

    /// Perform a background sync cycle. Manages Syncthing lifecycle if not already running.
    /// Used by BGAppRefreshTask handler and Silent Push handler.
    static func performBackgroundSync(
        reason: String,
        maxDuration: TimeInterval = 25
    ) async -> SyncResult {
        logger.info("Background sync starting (reason=\(reason))")
        trace("Starting background sync (reason=\(reason), maxDuration=\(Int(maxDuration))s).")
        var telemetryEventCursor = latestBridgeEventID()

        // Silent pushes arrive after iOS has suspended the process. The Go
        // bridge's cached state still reports isRunning=true, but the TCP
        // sockets to peers have been torn down by the kernel. Trigger a
        // rescan on each known folder — this causes Go's peer dialer to
        // notice the dead sockets and re-establish connections, without the
        // 5-15s cost of a full stopSyncthing + startSyncthing cycle.
        let alreadyRunning = SyncBridgeService.isRunning()
        trace("Bridge state before sync: running=\(alreadyRunning).")
        if reason == "silent-push" && alreadyRunning {
            logger.info("Silent push: triggering folder rescans to wake Syncthing peer dialer")
            if let rescanCount = requestFolderRescans() {
                trace("Silent push fast path: rescanning \(rescanCount) folder(s).")
            } else {
                trace("Silent push fast path: folder decode failed before rescan.")
            }
            traceRelevantBridgeEvents(since: &telemetryEventCursor, label: "after-fast-path-rescan")
        }

        // Check lifecycle lock: skip start/stop if foreground owns the instance.
        let foregroundOwns = lifecycleLock.withLock { $0.foregroundActive }
        var ownsLifecycle = !alreadyRunning && !foregroundOwns
        trace("Lifecycle ownership: foregroundOwns=\(foregroundOwns), backgroundOwns=\(ownsLifecycle).")

        var managedURLs: [URL] = []
        if ownsLifecycle {
            managedURLs = restoreBookmarkAccess()
            guard !managedURLs.isEmpty else {
                logger.info("No bookmarks restored")
                trace("Bookmark restore failed: no security-scoped access available.")
                return completeSync(
                    reason: reason,
                    result: .noBookmarkAccess,
                    detail: "No security-scoped bookmark access was available."
                )
            }
            trace("Restored bookmark access for \(managedURLs.count) URL(s).")
            traceManagedRoots(managedURLs, label: "restored-bookmarks")
            traceVaultSnapshot(label: "restored-bookmarks")

            let configDir = syncthingConfigDir()
            let err = SyncBridgeService.startSyncthing(configDir: configDir)
            if let err, !err.isEmpty, !SyncBridgeService.isRunning() {
                logger.error("Background start failed: \(err)")
                trace("Bridge start failed: \(err)")
                releaseAccess(managedURLs)
                return completeSync(
                    reason: reason,
                    result: .bridgeStartFailed,
                    detail: err
                )
            }
            trace("Bridge start requested for background-owned sync.")
        }

        let hasFolders = await waitForAnyFolders(maxWait: 3)
        trace("Folder availability check completed: hasFolders=\(hasFolders).")
        traceFolderStatuses(label: "post-folder-availability")
        guard hasFolders else {
            if ownsLifecycle {
                cleanupBackgroundManaged(managedURLs)
            }
            return completeSync(
                reason: reason,
                result: .noFoldersConfigured,
                detail: "No folders were available for background sync."
            )
        }

        var progressTracker = reason == "silent-push"
            ? SilentPushProgressTracker(lastEventID: latestBridgeEventID())
            : nil
        if let progressTracker {
            trace("Silent push progress tracking started at event id \(progressTracker.lastEventID).")
        }
        var forcedRestartPerformed = false

        if reason == "silent-push" {
            let sawWakeEvidence = await waitForSilentPushWakeEvidence(maxWait: 4)
            trace("Silent push wake evidence: \(sawWakeEvidence).")
            traceRelevantBridgeEvents(since: &telemetryEventCursor, label: "post-wake-evidence-window")
            traceFolderStatuses(label: "post-wake-evidence-window")
            if !sawWakeEvidence && !foregroundOwns {
                logger.warning("Silent push showed no peer/sync activity after rescan — forcing Syncthing restart")
                trace("No wake evidence after rescan. Forcing Syncthing restart.")
                let restart = await forceRestartForSilentPush(managedURLs: managedURLs)
                guard restart.success else {
                    trace("Forced restart failed: \(restart.errorDetail ?? "unknown error").")
                    if restart.ownsLifecycle {
                        cleanupBackgroundManaged(restart.managedURLs)
                    }
                    return completeSync(
                        reason: reason,
                        result: .bridgeStartFailed,
                        detail: restart.errorDetail ?? "Forced silent-push restart failed."
                    )
                }

                managedURLs = restart.managedURLs
                ownsLifecycle = restart.ownsLifecycle
                forcedRestartPerformed = true
                progressTracker?.requiresMeaningfulProgress = true
                progressTracker?.lastEventID = latestBridgeEventID()
                trace("Forced restart succeeded. Background now owns lifecycle=\(ownsLifecycle).")
                if let progressTracker {
                    trace("Silent push progress tracking reset after forced restart at event id \(progressTracker.lastEventID).")
                }

                let recoveredFolders = await waitForAnyFolders(maxWait: 3)
                trace("Post-restart folder availability: \(recoveredFolders).")
                traceManagedRoots(managedURLs, label: "post-forced-restart")
                traceVaultSnapshot(label: "post-forced-restart")
                traceFolderStatuses(label: "post-forced-restart")
                guard recoveredFolders else {
                    if ownsLifecycle {
                        cleanupBackgroundManaged(managedURLs)
                    }
                    return completeSync(
                        reason: reason,
                        result: .noFoldersConfigured,
                        detail: "No folders were available after forced silent-push restart."
                    )
                }

                if let rescanCount = requestFolderRescans() {
                    trace("Post-restart local rescan requested for \(rescanCount) folder(s).")
                } else {
                    trace("Post-restart local rescan skipped because folder decode failed.")
                }
                traceRelevantBridgeEvents(since: &telemetryEventCursor, label: "after-post-restart-rescan")
            }
        }

        _ = progressTracker?.poll()

        if allFoldersIdle() && !(progressTracker?.requiresMeaningfulProgress == true) {
            trace("Folders already idle after setup checks.")
            if ownsLifecycle {
                cleanupBackgroundManaged(managedURLs)
            }
            return completeSync(reason: reason, result: .alreadyIdle, detail: nil)
        }

        let deadline = Date(timeIntervalSinceNow: maxDuration)
        trace("Waiting for idle state until deadline.")
        while SyncBridgeService.isRunning() && Date() < deadline {
            if Task.isCancelled { break }
            try? await Task.sleep(for: .milliseconds(500))
            let idle = allFoldersIdle()
            if var tracker = progressTracker {
                let snapshot = tracker.poll()
                progressTrackerTraceIfNeeded(snapshot)
                if idle && !snapshot.requiresMeaningfulProgress {
                    progressTracker = tracker
                    break
                }
                progressTracker = tracker
            } else if idle {
                break
            }
        }

        let idle = allFoldersIdle()
        let progressSnapshot = progressTracker?.poll()
        if let progressSnapshot {
            progressTrackerTraceIfNeeded(progressSnapshot)
        }
        trace("Deadline reached or idle observed. idle=\(idle).")
        traceRelevantBridgeEvents(since: &telemetryEventCursor, label: "pre-completion")
        traceFolderStatuses(label: "pre-completion")
        traceVaultSnapshot(label: "pre-completion")
        if idle {
            await notifyConflictsIfAny()
        }

        // Only stop if we started it and foreground hasn't taken over.
        if ownsLifecycle {
            cleanupBackgroundManaged(managedURLs)
        }

        let result: SyncResult
        let detail: String?
        if let progressSnapshot, forcedRestartPerformed, progressSnapshot.requiresMeaningfulProgress {
            result = .failed
            detail = "Silent push restarted Syncthing, but no real sync progress was observed before the app returned to idle."
        } else if idle {
            result = .synced
            detail = nil
        } else {
            result = .notIdleBeforeDeadline
            detail = "Sync did not reach idle before \(Int(maxDuration))s deadline."
        }
        return completeSync(reason: reason, result: result, detail: detail)
    }

    // MARK: - BGAppRefreshTask Handler

    private static func handleAppRefresh(task: BGAppRefreshTask) async {
        logger.info("Background refresh starting")
        scheduleAppRefresh()

        task.expirationHandler = {
            let shouldStop = lifecycleLock.withLock { !$0.foregroundActive }
            if shouldStop {
                SyncBridgeService.stopSyncthing()
                logger.info("Background refresh expired — Syncthing stopped")
            } else {
                logger.info("Background refresh expired — skipped stop (foreground active)")
            }
        }

        let result = await performBackgroundSync(reason: "app-refresh")
        task.setTaskCompleted(success: result.isSuccessful)
    }

    // MARK: - BGContinuedProcessingTask Handler

    private static func handleContinuedProcessing(task: BGContinuedProcessingTask) async {
        logger.info("Continued processing starting")

        let expired = OSAllocatedUnfairLock(initialState: false)

        task.expirationHandler = {
            expired.withLock { $0 = true }
            let shouldStop = lifecycleLock.withLock { !$0.foregroundActive }
            if shouldStop {
                SyncBridgeService.stopSyncthing()
                logger.info("Continued processing expired — Syncthing stopped")
            } else {
                logger.info("Continued processing expired — skipped stop (foreground active)")
            }
        }

        let progress = task.progress
        progress.totalUnitCount = 100

        while !expired.withLock({ $0 }) {
            try? await Task.sleep(for: .seconds(1))
            guard !expired.withLock({ $0 }) else { break }

            let idle = allFoldersIdle()
            if !SyncBridgeService.isRunning() || idle {
                if idle {
                    await notifyConflictsIfAny()
                }
                progress.completedUnitCount = 100
                task.setTaskCompleted(success: true)
                logger.info("Continued processing completed")
                return
            }

            let pct = averageFolderCompletion()
            progress.completedUnitCount = Int64(pct)
            task.updateTitle("Syncing Vault", subtitle: "\(Int(pct))% complete")
        }

        task.setTaskCompleted(success: false)
        logger.info("Continued processing expired")
    }

    // MARK: - Helpers

    private static func waitForAnyFolders(maxWait: TimeInterval) async -> Bool {
        let deadline = Date(timeIntervalSinceNow: maxWait)
        while Date() < deadline {
            if hasConfiguredFolders() {
                return true
            }
            if !SyncBridgeService.isRunning() {
                break
            }
            try? await Task.sleep(for: .milliseconds(250))
        }
        return hasConfiguredFolders()
    }

    private static func hasConfiguredFolders() -> Bool {
        let json = SyncBridgeService.getFoldersJSON()
        guard let data = json.data(using: .utf8),
              let folders = try? JSONDecoder().decode([FolderStub].self, from: data) else {
            return false
        }
        return !folders.isEmpty
    }

    private static func cleanupBackgroundManaged(_ managedURLs: [URL]) {
        let shouldStop = lifecycleLock.withLock { !$0.foregroundActive }
        if shouldStop && SyncBridgeService.isRunning() {
            SyncBridgeService.stopSyncthing()
        }
        releaseAccess(managedURLs)
    }

    @discardableResult
    private static func completeSync(
        reason: String,
        result: SyncResult,
        detail: String?
    ) -> SyncResult {
        let outcome = SyncOutcome(
            timestamp: Date(),
            triggerReason: reason,
            result: result,
            detail: detail
        )
        persistSyncOutcome(outcome)
        if let detail, !detail.isEmpty {
            trace("Completed with result=\(result.rawValue): \(detail)")
        } else {
            trace("Completed with result=\(result.rawValue).")
        }
        logger.info("Background sync completed (reason=\(reason), result=\(result.rawValue))")
        return result
    }

    private static func persistSyncOutcome(_ outcome: SyncOutcome) {
        guard let data = try? JSONEncoder().encode(outcome) else { return }
        UserDefaults.standard.set(data, forKey: lastSyncOutcomeStorageKey)
        NotificationCenter.default.post(name: lastSyncOutcomeDidChangeNotification, object: nil)
    }

    private static func allFoldersIdle() -> Bool {
        let json = SyncBridgeService.getFoldersJSON()
        guard let data = json.data(using: .utf8),
              let folders = try? JSONDecoder().decode([FolderStub].self, from: data),
              !folders.isEmpty else {
            // Empty folder list means Syncthing hasn't populated yet — not idle
            return false
        }

        for folder in folders {
            let statusJSON = SyncBridgeService.getFolderStatusJSON(folderID: folder.id)
            guard let statusData = statusJSON.data(using: .utf8),
                  let status = try? JSONDecoder().decode(StatusStub.self, from: statusData) else {
                // Decode failure means we can't confirm state — treat as not idle
                return false
            }
            // A folder is truly idle only when Syncthing is neither scanning
            // nor syncing AND has no outstanding work. The state field alone
            // is not enough: Syncthing briefly reports `idle` between scan
            // and sync phases while needBytes/needFiles still await a pull.
            // Treating that window as "done" caused background-sync handlers
            // to shut Syncthing down before any file was actually pulled.
            let hasPendingWork = status.needFiles > 0
                || status.needBytes > 0
                || status.inProgressBytes > 0
            if status.state != "idle" || hasPendingWork {
                return false
            }
        }
        return true
    }

    private static func averageFolderCompletion() -> Double {
        let json = SyncBridgeService.getFoldersJSON()
        guard let data = json.data(using: .utf8),
              let folders = try? JSONDecoder().decode([FolderStub].self, from: data),
              !folders.isEmpty else {
            return 0
        }

        var total: Double = 0
        for folder in folders {
            let statusJSON = SyncBridgeService.getFolderStatusJSON(folderID: folder.id)
            if let d = statusJSON.data(using: .utf8),
               let s = try? JSONDecoder().decode(StatusStub.self, from: d) {
                total += s.completionPct
            }
            // Decode failure contributes 0%, not 100%
        }
        return total / Double(folders.count)
    }

    private static func notifyConflictsIfAny() async {
        let json = SyncBridgeService.getFoldersJSON()
        guard let data = json.data(using: .utf8),
              let folders = try? JSONDecoder().decode([FolderStub].self, from: data) else {
            return
        }

        var count = 0
        for folder in folders {
            let cJSON = SyncBridgeService.getConflictFilesJSON(folderID: folder.id)
            if let cData = cJSON.data(using: .utf8),
               let conflicts = try? JSONDecoder().decode([ConflictStub].self, from: cData) {
                count += conflicts.count
            }
        }

        guard count > 0 else { return }

        let content = UNMutableNotificationContent()
        content.title = "Sync Conflicts"
        content.body = count == 1
            ? "1 file has a sync conflict. Open VaultSync to resolve it."
            : "\(count) files have sync conflicts. Open VaultSync to resolve them."
        content.sound = .default

        let request = UNNotificationRequest(
            identifier: "sync-conflict-\(UUID().uuidString)",
            content: content,
            trigger: nil
        )

        do {
            try await UNUserNotificationCenter.current().add(request)
            logger.info("Conflict notification sent (\(count) conflicts)")
        } catch {
            logger.error("Failed to send notification: \(error)")
        }
    }

    private static func restoreBookmarkAccess() -> [URL] {
        let id = "obsidian-root"
        guard let (url, isStale) = BookmarkService.resolveBookmark(identifier: id) else {
            trace("Bookmark lookup returned no stored Obsidian root.")
            return []
        }
        guard BookmarkService.startAccessing(url: url) else {
            trace("Bookmark access start failed for \(url.lastPathComponent).")
            return []
        }
        if isStale {
            do {
                try BookmarkService.saveBookmark(for: url, identifier: id)
                logger.info("Refreshed stale Obsidian bookmark in background")
                trace("Refreshed stale bookmark during background sync.")
            } catch {
                logger.warning("Could not refresh stale Obsidian bookmark")
                trace("Failed to refresh stale bookmark during background sync.")
            }
        }
        return [url]
    }

    private static func releaseAccess(_ urls: [URL]) {
        urls.forEach { BookmarkService.stopAccessing(url: $0) }
    }

    private static func syncthingConfigDir() -> String {
        let docs = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask).first!
        return docs.appendingPathComponent("syncthing", isDirectory: true).path
    }

    private static func requestFolderRescans() -> Int? {
        let json = SyncBridgeService.getFoldersJSON()
        guard let data = json.data(using: .utf8),
              let folders = try? JSONDecoder().decode([FolderStub].self, from: data) else {
            return nil
        }
        for folder in folders {
            _ = SyncBridgeService.rescanFolder(folderID: folder.id)
        }
        return folders.count
    }

    private static func waitForSilentPushWakeEvidence(maxWait: TimeInterval) async -> Bool {
        let deadline = Date(timeIntervalSinceNow: maxWait)
        while Date() < deadline {
            let hasPeer = hasAnyConnectedPeer()
            let idle = allFoldersIdle()
            if hasPeer || !idle {
                trace("Wake evidence observed: connectedPeer=\(hasPeer), allFoldersIdle=\(idle).")
                return true
            }
            if !SyncBridgeService.isRunning() {
                trace("Wake evidence check aborted because bridge stopped running.")
                return false
            }
            try? await Task.sleep(for: .milliseconds(400))
        }
        let hasPeer = hasAnyConnectedPeer()
        let idle = allFoldersIdle()
        trace("Wake evidence window ended: connectedPeer=\(hasPeer), allFoldersIdle=\(idle).")
        return hasPeer || !idle
    }

    private static func hasAnyConnectedPeer() -> Bool {
        let json = SyncBridgeService.getDevicesJSON()
        guard let data = json.data(using: .utf8),
              let devices = try? JSONDecoder().decode([DeviceStub].self, from: data) else {
            return false
        }
        return devices.contains { $0.connected && !$0.paused }
    }

    private static func forceRestartForSilentPush(
        managedURLs existingManagedURLs: [URL]
    ) async -> (success: Bool, ownsLifecycle: Bool, managedURLs: [URL], errorDetail: String?) {
        var managedURLs = existingManagedURLs
        if managedURLs.isEmpty {
            managedURLs = restoreBookmarkAccess()
            if managedURLs.isEmpty {
                trace("Forced restart could not restore bookmark access.")
                return (false, true, [], "No security-scoped bookmark access was available for forced restart.")
            }
        }

        if SyncBridgeService.isRunning() {
            trace("Forced restart stopping running bridge first.")
            SyncBridgeService.stopSyncthing()
            try? await Task.sleep(for: .milliseconds(350))
        }

        let configDir = syncthingConfigDir()
        let err = SyncBridgeService.startSyncthing(configDir: configDir)
        if let err, !err.isEmpty, !SyncBridgeService.isRunning() {
            trace("Forced restart start failed: \(err)")
            return (false, true, managedURLs, err)
        }

        trace("Forced restart started bridge successfully.")
        return (true, true, managedURLs, nil)
    }

    private static func traceManagedRoots(_ urls: [URL], label: String) {
        let roots = urls.map(\.path)
        trace("Managed roots [\(label)]: \(roots.isEmpty ? "none" : roots.joined(separator: ", ")).")
    }

    private static func traceVaultSnapshot(label: String) {
        let identifier = "obsidian-root"
        guard let (url, isStale) = BookmarkService.resolveBookmark(identifier: identifier) else {
            trace("Vault snapshot [\(label)]: no bookmark available.")
            return
        }

        let startedAccess = BookmarkService.startAccessing(url: url)
        defer {
            if startedAccess {
                BookmarkService.stopAccessing(url: url)
            }
        }

        guard startedAccess else {
            trace("Vault snapshot [\(label)]: bookmark access failed for \(url.path).")
            return
        }

        let fileManager = FileManager.default
        let resourceKeys: [URLResourceKey] = [.isRegularFileKey, .contentModificationDateKey, .fileSizeKey]
        guard let enumerator = fileManager.enumerator(
            at: url,
            includingPropertiesForKeys: resourceKeys,
            options: [.skipsHiddenFiles, .skipsPackageDescendants]
        ) else {
            trace("Vault snapshot [\(label)]: could not enumerate \(url.path).")
            return
        }

        var regularFileCount = 0
        var markdownFileCount = 0
        var recentFiles: [(path: String, modified: Date, size: Int?)] = []

        for case let fileURL as URL in enumerator {
            guard let values = try? fileURL.resourceValues(forKeys: Set(resourceKeys)),
                  values.isRegularFile == true else {
                continue
            }

            regularFileCount += 1
            if fileURL.pathExtension.lowercased() == "md" {
                markdownFileCount += 1
            }

            let modified = values.contentModificationDate ?? .distantPast
            recentFiles.append((
                path: fileURL.path.replacingOccurrences(of: url.path + "/", with: ""),
                modified: modified,
                size: values.fileSize
            ))
        }

        let recentSummary = recentFiles
            .sorted { $0.modified > $1.modified }
            .prefix(5)
            .map {
                let sizePart = $0.size.map { "\($0)b" } ?? "?"
                return "\($0.path) [\(sizePart), mtime=\(iso8601String(from: $0.modified))]"
            }
            .joined(separator: " | ")

        trace(
            "Vault snapshot [\(label)]: root=\(url.path), stale=\(isStale), files=\(regularFileCount), markdown=\(markdownFileCount), recent=\(recentSummary.isEmpty ? "none" : recentSummary)."
        )
    }

    private static func traceFolderStatuses(label: String) {
        let json = SyncBridgeService.getFoldersJSON()
        guard let data = json.data(using: .utf8),
              let folders = try? JSONDecoder().decode([FolderStub].self, from: data),
              !folders.isEmpty else {
            trace("Folder status [\(label)]: no decodable folders.")
            return
        }

        let summaries = folders.compactMap { folder -> String? in
            guard let status = SyncBridgeService.getFolderStatus(folderID: folder.id) else {
                return "\(folder.id)=decode-failed"
            }
            return "\(folder.id):state=\(status.state), needFiles=\(status.needFiles), needBytes=\(status.needBytes), inProgressBytes=\(status.inProgressBytes), localFiles=\(status.localFiles), globalFiles=\(status.globalFiles), completion=\(Int(status.completionPct))%"
        }

        trace("Folder status [\(label)]: \(summaries.joined(separator: " | ")).")
    }

    private static func traceRelevantBridgeEvents(since lastEventID: inout Int, label: String) {
        let events = decodeBridgeEvents(from: SyncBridgeService.getEventsSince(lastID: lastEventID))
        if let lastSeen = events.last?.id, lastSeen > lastEventID {
            lastEventID = lastSeen
        }

        let relevant = events.filter(\.isDiagnosticRelevant)
        guard !relevant.isEmpty else {
            trace("Bridge events [\(label)]: none since cursor.")
            return
        }

        let summary = relevant
            .suffix(8)
            .map(\.diagnosticSummary)
            .joined(separator: " | ")
        trace("Bridge events [\(label)]: \(summary).")
    }

    private static func trace(_ message: String) {
        logger.debug("\(message, privacy: .public)")
    }

    private static func latestBridgeEventID() -> Int {
        let snapshot = decodeBridgeEvents(from: SyncBridgeService.getEventsSince(lastID: 0))
        return snapshot.last?.id ?? 0
    }

    private static func decodeBridgeEvents(from json: String) -> [BridgeEventStub] {
        guard let data = json.data(using: .utf8),
              let events = try? JSONDecoder().decode([BridgeEventStub].self, from: data) else {
            return []
        }
        return events
    }

    private static func progressTrackerTraceIfNeeded(_ snapshot: SilentPushProgressTracker.ProgressSnapshot) {
        guard let summary = snapshot.summaryForTrace else { return }
        trace(summary)
    }

    private static func iso8601String(from date: Date) -> String {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime]
        return formatter.string(from: date)
    }

    // MARK: - Stub types for JSON decoding

    private struct FolderStub: Decodable {
        let id: String
    }

    private struct DeviceStub: Decodable {
        let connected: Bool
        let paused: Bool

        private enum CodingKeys: String, CodingKey {
            case connected
            case paused
        }

        init(from decoder: any Decoder) throws {
            let container = try decoder.container(keyedBy: CodingKeys.self)
            connected = try container.decode(Bool.self, forKey: .connected)
            paused = try container.decodeIfPresent(Bool.self, forKey: .paused) ?? false
        }
    }

    private struct StatusStub: Decodable {
        let state: String
        let completionPct: Double
        let needFiles: Int
        let needBytes: Int64
        let inProgressBytes: Int64
    }

    private struct ConflictStub: Decodable {
        let conflictPath: String
    }

    struct SilentPushProgressTracker: Sendable {
        struct ProgressSnapshot: Sendable {
            let lastEventID: Int
            let requiresMeaningfulProgress: Bool
            let sawMeaningfulProgress: Bool
            let summaryForTrace: String?
        }

        var lastEventID: Int
        var requiresMeaningfulProgress = false
        private(set) var sawMeaningfulProgress = false

        mutating func poll() -> ProgressSnapshot {
            let events = BackgroundSyncService.decodeBridgeEvents(
                from: SyncBridgeService.getEventsSince(lastID: lastEventID)
            )
            return observe(events)
        }

        mutating func observe(_ events: [BridgeEventStub]) -> ProgressSnapshot {
            var summary: String?
            for event in events {
                if event.id > lastEventID {
                    lastEventID = event.id
                }
                if !sawMeaningfulProgress && event.indicatesMeaningfulProgress {
                    sawMeaningfulProgress = true
                    requiresMeaningfulProgress = false
                    summary = "Observed sync progress via \(event.type) (event id \(event.id))."
                }
            }

            return ProgressSnapshot(
                lastEventID: lastEventID,
                requiresMeaningfulProgress: requiresMeaningfulProgress,
                sawMeaningfulProgress: sawMeaningfulProgress,
                summaryForTrace: summary
            )
        }
    }

    struct BridgeEventStub: Decodable, Sendable {
        let id: Int
        let type: String
        let data: [String: String]?

        var isDiagnosticRelevant: Bool {
            switch type {
            case "DeviceConnected", "DeviceDisconnected", "LocalIndexUpdated", "RemoteIndexUpdated",
                 "ItemStarted", "ItemFinished", "StateChanged", "FolderCompletion", "FolderErrors":
                return true
            default:
                return false
            }
        }

        var diagnosticSummary: String {
            var details: [String] = ["#\(id)", type]
            if let item = data?["item"] {
                details.append("item=\(item)")
            }
            if let folder = data?["folder"] {
                details.append("folder=\(folder)")
            }
            if let device = data?["device"] {
                details.append("device=\(device)")
            }
            if let from = data?["from"] {
                details.append("from=\(from)")
            }
            if let to = data?["to"] {
                details.append("to=\(to)")
            }
            if let error = data?["error"], !error.isEmpty {
                details.append("error=\(error)")
            }
            return details.joined(separator: " ")
        }

        var indicatesMeaningfulProgress: Bool {
            switch type {
            case "RemoteIndexUpdated", "LocalIndexUpdated", "ItemFinished":
                return true
            case "StateChanged":
                let toState = data?["to"]?.lowercased()
                let fromState = data?["from"]?.lowercased()
                return toState == "syncing"
                    || fromState == "syncing"
                    || toState == "scanning"
                    || fromState == "scanning"
            default:
                return false
            }
        }
    }
}
