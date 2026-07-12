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
    static let processingIdentifier = "eu.vaultsync.app.sync-processing"

    /// Shared lock coordinating Syncthing bridge start/stop across foreground and background.
    static let lifecycleLock = OSAllocatedUnfairLock(initialState: SyncLifecycleState())

    /// Single-flight guard: only one background sync may drive the shared
    /// Syncthing instance at a time. Distinct from `lifecycleLock`, which only
    /// tracks whether the foreground owns start/stop — this prevents two
    /// concurrent background wake-ups from tearing down each other's sync.
    private static let syncInFlightLock = OSAllocatedUnfairLock(initialState: false)

    /// Whether the scene is currently foreground-active. Drives conflict-banner
    /// behavior: while active, the in-app UI surfaces conflicts so no banner is
    /// posted and the foreground poll keeps the suppression baseline in sync;
    /// while inactive (including the ~30s post-background grace window, when the
    /// poll is still running) the baseline is frozen and silent-push handlers
    /// may post banners. This is NOT `lifecycleLock.foregroundActive`, which
    /// tracks bridge ownership and is released early on `.background`.
    private static let sceneActiveLock = OSAllocatedUnfairLock(initialState: false)

    static func setSceneActive(_ active: Bool) {
        sceneActiveLock.withLock { $0 = active }
    }

    static func isSceneActive() -> Bool {
        sceneActiveLock.withLock { $0 }
    }

    /// Guards the UIApplication background-task assertion used for the
    /// scene-phase grace period after the app leaves the foreground.
    private static let backgroundAssertionLock = OSAllocatedUnfairLock(
        initialState: UIBackgroundTaskIdentifier.invalid
    )

    /// Whether each background task type was successfully registered.
    /// Written once during app launch, read-only afterwards — safe without synchronization.
    nonisolated(unsafe) private(set) static var appRefreshRegistered = false
    nonisolated(unsafe) private(set) static var continuedProcessingRegistered = false
    nonisolated(unsafe) private(set) static var processingRegistered = false
    private static let lastSyncOutcomeStorageKey = "background-sync-last-outcome-v2"
    private static let legacyLastSyncOutcomeStorageKey = "background-sync-last-outcome-v1"
    static let lastSyncOutcomeDidChangeNotification = Notification.Name("BackgroundSyncLastOutcomeDidChange")

    /// Stable identifier for the conflict banner. Re-posting with the same id
    /// replaces the existing notification in place instead of stacking a fresh
    /// one each silent push (issue #10).
    private static let conflictNotificationIdentifier = "sync-conflict"
    /// Persisted distinct/visible conflict count last surfaced to the user.
    /// Used to suppress re-posting an unchanged count. Lives in
    /// `UserDefaults.standard`, which the in-process silent-push / BGTask
    /// handlers share with the foreground UI.
    private static let lastNotifiedConflictCountKey = "conflict-notification-last-count-v1"
    /// In-app toggle gating the conflict banner (Settings → Notifications).
    /// Defaults ON; an absent key must read as ON so existing installs are not
    /// silently muted after upgrade. Gates only the banner — never relay
    /// silent-push wake-ups, which do not depend on alert authorization.
    static let conflictNotificationsEnabledKey = "conflict-notifications-enabled-v1"

    struct SyncOutcome: Codable, Equatable, Sendable {
        let timestamp: Date
        let triggerReason: String
        let result: SyncResult
        let detail: String?
        /// Optional for backward-compatible decoding of outcomes persisted by
        /// older app versions. Only a fresh successful local file application
        /// sets this to true; idle/scan/index activity never does.
        let localDataProgressObserved: Bool?
    }

    // MARK: - Registration

    /// Register background task handlers. Must be called before app finishes launching.
    /// Registration may fail in the Simulator (ESRCH) — failures are non-fatal.
    static func registerTasks() {
        let refreshHandler: @Sendable (BGTask) -> Void = { task in
            guard let refreshTask = task as? BGAppRefreshTask else { return }
            let wrapped = UnsafeSendable(value: refreshTask)
            Task { await handleAppRefresh(task: wrapped.value) }
        }
        appRefreshRegistered = BGTaskScheduler.shared.register(
            forTaskWithIdentifier: appRefreshIdentifier,
            using: nil,
            launchHandler: refreshHandler
        )

        if !appRefreshRegistered {
            logger.warning("Failed to register app refresh task — background refresh unavailable")
        }

        let processingHandler: @Sendable (BGTask) -> Void = { task in
            guard let processingTask = task as? BGProcessingTask else { return }
            let wrapped = UnsafeSendable(value: processingTask)
            Task { await handleProcessing(task: wrapped.value) }
        }
        processingRegistered = BGTaskScheduler.shared.register(
            forTaskWithIdentifier: processingIdentifier,
            using: nil,
            launchHandler: processingHandler
        )

        if !processingRegistered {
            logger.warning("Failed to register processing task — overnight catch-up sync unavailable")
        }

        if #available(iOS 26.0, *) {
            let continuedHandler: @Sendable (BGTask) -> Void = { task in
                guard let processingTask = task as? BGContinuedProcessingTask else { return }
                let wrapped = UnsafeSendable(value: processingTask)
                Task { await handleContinuedProcessing(task: wrapped.value) }
            }
            continuedProcessingRegistered = BGTaskScheduler.shared.register(
                forTaskWithIdentifier: continuedProcessingIdentifier,
                using: nil,
                launchHandler: continuedHandler
            )
        } else {
            continuedProcessingRegistered = false
        }

        if !continuedProcessingRegistered {
            logger.warning("Failed to register continued processing task — continued processing unavailable (expected in Simulator)")
        }

        logger.info("Background task registration complete (refresh=\(appRefreshRegistered), processing=\(processingRegistered), continued=\(continuedProcessingRegistered))")
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

    /// Pure predicate: should the app trigger a foreground rescan when the
    /// scene returns to `.active`? True when the previous-background duration
    /// reaches `threshold`. Debounces brief Control-Center swipes that would
    /// otherwise spam rescans on every minor scene flip.
    static func shouldRescanOnForeground(
        now: Date,
        lastBackgroundedAt: Date?,
        threshold: TimeInterval
    ) -> Bool {
        guard let lastBackgroundedAt else { return false }
        return now.timeIntervalSince(lastBackgroundedAt) >= threshold
    }

    /// How the scene-activation handler should attach to the sync engine,
    /// given who is currently running. Pure so the branch matrix is
    /// unit-testable without the bridge (#60).
    enum SceneActivationAction: Equatable, Sendable {
        /// Engine not running: reset stale manager state if needed, then start.
        case coldStart
        /// Engine running but the manager never started it — a background
        /// handler brought it up in a background-launched process. Adopt it:
        /// restore manager state, start polling, then reconcile paths so
        /// accept decisions gate correctly (#60, decision 008).
        case adoptRunningEngine
        /// Engine running and manager attached: normal foreground return, at
        /// most a debounced rescan (`shouldRescanOnForeground`).
        case alreadyAttached
    }

    /// Pure decision for the `.active` scene-phase branch. Guarding on the
    /// bridge alone (the pre-#60 behavior) conflated `adoptRunningEngine`
    /// with `alreadyAttached`, so a bridge-running / manager-cold engine got
    /// neither polling nor a reconcile — and was later stopped under the
    /// active scene by the background handler's cleanup.
    static func sceneActivationAction(
        bridgeRunning: Bool,
        managerRunning: Bool
    ) -> SceneActivationAction {
        if !bridgeRunning { return .coldStart }
        return managerRunning ? .alreadyAttached : .adoptRunningEngine
    }

    /// Begin a UIApplication background-task assertion so iOS grants up to
    /// ~30 seconds of continued execution after the app is backgrounded.
    /// Without this the system can suspend the process within ~5s, severing
    /// Syncthing's peer connections before any scheduled BG task runs.
    /// Safe to call more than once — ends any previous assertion first.
    @MainActor
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
    @MainActor
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
    @MainActor
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
            logger.error("Could not schedule app refresh")
        }
    }

    /// Schedule the overnight catch-up sync. iOS runs it when conditions are
    /// right — typically while charging at night — with a multi-minute budget,
    /// unlike the ~30s BGAppRefreshTask. This is the safety net for catch-ups
    /// too large for the refresh/silent-push budget.
    @MainActor
    static func scheduleProcessing() {
        guard processingRegistered else { return }

        let hasAccess = BookmarkService.resolveBookmark(identifier: "obsidian-root") != nil
        guard hasAccess else {
            logger.info("No Obsidian directory configured, skipping processing schedule")
            return
        }

        let request = BGProcessingTaskRequest(identifier: processingIdentifier)
        request.requiresNetworkConnectivity = true
        // Charger-time only: the long catch-up must never cost battery.
        request.requiresExternalPower = true
        request.earliestBeginDate = Date(timeIntervalSinceNow: 60 * 60)

        do {
            try BGTaskScheduler.shared.submit(request)
            logger.info("Processing task scheduled")
        } catch {
            logger.error("Could not schedule processing task")
        }
    }

    /// Submit continued processing for active sync. Call from foreground only.
    @MainActor
    static func submitContinuedProcessing() {
        guard continuedProcessingRegistered else { return }

        if #available(iOS 26.0, *) {
            let request = BGContinuedProcessingTaskRequest(
                identifier: continuedProcessingIdentifier,
                title: L10n.tr("Syncing Vault"),
                subtitle: L10n.tr("Synchronizing your Obsidian vault…")
            )

            do {
                try BGTaskScheduler.shared.submit(request)
                logger.info("Continued processing submitted")
            } catch {
                logger.error("Could not submit continued processing")
            }
        } else {
            return
        }
    }

    /// Cancel pending continued processing task.
    @MainActor
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
            logger.error("Notification permission request failed")
            return false
        }
    }

    /// Whether iOS will actually present a conflict alert banner. Affects ONLY
    /// the banner — silent (content-available) pushes used by Cloud Relay are
    /// delivered regardless, so this is surfaced as informational and never as a
    /// relay/APNs failure.
    enum AlertBannerStatus: Sendable {
        case allowed   // authorized AND banners enabled
        case denied    // denied, or banners explicitly turned off
        case unknown   // not determined / provisional / not supported
    }

    /// Resolve the real banner capability from UNNotificationSettings. Authorized
    /// is not enough on its own — the user can keep authorization but switch
    /// "Banners" off in iOS Settings (`alertSetting == .disabled`).
    static func alertBannerStatus() async -> AlertBannerStatus {
        let settings = await UNUserNotificationCenter.current().notificationSettings()
        switch settings.authorizationStatus {
        case .denied:
            return .denied
        case .authorized:
            switch settings.alertSetting {
            case .enabled:
                return .allowed
            case .disabled:
                return .denied
            default:
                return .unknown
            }
        case .notDetermined, .provisional, .ephemeral:
            // Not requested by VaultSync (full alert auth is asked via the
            // post-first-sync explainer card, #69); provisional/ephemeral
            // deliver quietly, not as banners.
            return .unknown
        @unknown default:
            return .unknown
        }
    }

    /// What to do with the conflict banner given the current conflict count and
    /// the count last surfaced to the user. Pure and deterministic so the
    /// suppression logic can be unit-tested without the bridge.
    enum ConflictNotificationAction: Equatable, Sendable {
        /// Count rose since last notification — post audibly (genuinely new).
        case alert
        /// Count fell but conflicts remain — refresh the banner quietly.
        case updateQuiet
        /// Count is unchanged — leave the existing banner untouched (no spam).
        case suppress
        /// No conflicts remain — remove the banner.
        case clear
    }

    static func conflictNotificationAction(
        currentCount current: Int,
        lastNotifiedCount last: Int
    ) -> ConflictNotificationAction {
        if current <= 0 { return .clear }
        if current > last { return .alert }
        if current < last { return .updateQuiet }
        return .suppress
    }

    /// Re-baseline conflict-notification suppression to what the user can
    /// currently see in the app, and clear the banner when no conflicts remain.
    /// Called from the foreground poll so (a) a banner the user has already seen
    /// is not re-alerted in the background, and (b) a brand-new conflict that
    /// appears after a full resolve still alerts instead of being treated as a
    /// "decrease" against a stale high-water mark.
    @MainActor
    static func reconcileConflictNotificationBaseline(currentCount: Int) {
        // Only re-baseline while the scene is genuinely active. During the ~30s
        // post-background grace window the foreground poll keeps running; if it
        // silently bumped the baseline there, a conflict that arrived in that
        // window would be read as "unchanged" by the next silent push and never
        // alert. Freezing the baseline keeps such a conflict a genuine rise.
        guard isSceneActive() else { return }

        let normalized = max(0, currentCount)
        // Skip the write/IPC entirely when nothing changed — the 2s poll would
        // otherwise hit usernotificationsd every tick in the zero-conflict case.
        guard normalized != UserDefaults.standard.integer(forKey: lastNotifiedConflictCountKey) else { return }

        UserDefaults.standard.set(normalized, forKey: lastNotifiedConflictCountKey)
        if normalized == 0 {
            UNUserNotificationCenter.current().removeDeliveredNotifications(
                withIdentifiers: [conflictNotificationIdentifier]
            )
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
        /// The run stopped early because a folder is in a terminal error state
        /// (nothing more could sync). NOT a timeout — the folder error surfaces
        /// via the in-app Sync Issues panel, so this must not raise a misleading
        /// "Timed Out" issue, but it is also not a clean success: the widget
        /// should still reflect the error rather than a green idle state.
        case settledWithFolderError

        var isSuccessful: Bool {
            switch self {
            case .synced, .alreadyIdle:
                return true
            case .noBookmarkAccess, .noFoldersConfigured, .bridgeStartFailed, .notIdleBeforeDeadline, .failed, .settledWithFolderError:
                return false
            }
        }

        var shouldSurfaceIssue: Bool {
            switch self {
            case .synced, .alreadyIdle, .settledWithFolderError:
                return false
            case .noBookmarkAccess, .noFoldersConfigured, .bridgeStartFailed, .notIdleBeforeDeadline, .failed:
                return true
            }
        }

        var issueTitle: String {
            switch self {
            case .noBookmarkAccess:
                return L10n.tr("Background Sync Could Not Access Obsidian")
            case .noFoldersConfigured:
                return L10n.tr("Background Sync Found No Vaults")
            case .bridgeStartFailed:
                return L10n.tr("Background Sync Could Not Start")
            case .notIdleBeforeDeadline:
                return L10n.tr("Background Sync Timed Out")
            case .failed:
                return L10n.tr("Background Sync Failed")
            case .synced, .alreadyIdle, .settledWithFolderError:
                return L10n.tr("Background Sync Completed")
            }
        }

        var issueMessage: String {
            switch self {
            case .noBookmarkAccess:
                return L10n.tr("VaultSync could not restore bookmark access for the Obsidian folder during a background run.")
            case .noFoldersConfigured:
                return L10n.tr("No Syncthing folders were available to sync in the background.")
            case .bridgeStartFailed:
                return L10n.tr("The embedded Syncthing bridge did not start for a background sync.")
            case .notIdleBeforeDeadline:
                return L10n.tr("Background sync did not reach an idle folder state before the iOS deadline.")
            case .failed:
                return L10n.tr("Background sync ended with an unexpected failure.")
            case .synced:
                return L10n.tr("Background sync completed and reached idle.")
            case .alreadyIdle:
                return L10n.tr("Background sync ran, but folders were already idle.")
            case .settledWithFolderError:
                return L10n.tr("Background sync settled with at least one folder in an error state.")
            }
        }

        var remediation: String {
            switch self {
            case .noBookmarkAccess:
                return L10n.tr("Reconnect your Obsidian folder access in VaultSync, then run a foreground rescan.")
            case .noFoldersConfigured:
                return L10n.tr("Accept or create a shared vault before relying on background sync.")
            case .bridgeStartFailed:
                return L10n.tr("Open VaultSync once to restart Syncthing, then retry.")
            case .notIdleBeforeDeadline:
                return L10n.tr("Open VaultSync to allow a longer foreground sync session.")
            case .failed:
                return L10n.tr("Retry from the app and review relay/background diagnostics in Settings.")
            case .synced, .alreadyIdle, .settledWithFolderError:
                return L10n.tr("No action needed.")
            }
        }
    }

    static func lastSyncOutcome(defaults: UserDefaults = .standard) -> SyncOutcome? {
        if let data = defaults.data(forKey: lastSyncOutcomeStorageKey),
           let decoded = try? JSONDecoder().decode(SyncOutcome.self, from: data) {
            defaults.removeObject(forKey: legacyLastSyncOutcomeStorageKey)
            return decoded
        }
        guard let data = defaults.data(forKey: legacyLastSyncOutcomeStorageKey) else {
            return nil
        }
        guard let legacy = try? JSONDecoder().decode(SyncOutcome.self, from: data) else {
            defaults.removeObject(forKey: legacyLastSyncOutcomeStorageKey)
            return nil
        }
        // v1 could contain a raw bridge error with local paths. Preserve only
        // the safe operational shape for existing users; never surface or copy
        // its free-form detail into the v2 record.
        let safeReason: String
        switch legacy.triggerReason {
        case "silent-push", "app-refresh", "processing":
            safeReason = legacy.triggerReason
        default:
            safeReason = "background"
        }
        let sanitized = SyncOutcome(
            timestamp: legacy.timestamp,
            triggerReason: safeReason,
            result: legacy.result,
            detail: nil,
            localDataProgressObserved: nil
        )
        if let encoded = try? JSONEncoder().encode(sanitized) {
            defaults.set(encoded, forKey: lastSyncOutcomeStorageKey)
        }
        defaults.removeObject(forKey: legacyLastSyncOutcomeStorageKey)
        return sanitized
    }

    /// Perform a background sync cycle. Manages Syncthing lifecycle if not already running.
    /// Used by BGAppRefreshTask handler and Silent Push handler.
    static func performBackgroundSync(
        reason: String,
        maxDuration: TimeInterval = 25
    ) async -> SyncResult {
        logger.info("Background sync starting (reason=\(reason))")
        trace("Starting background sync (reason=\(reason), maxDuration=\(Int(maxDuration))s).")

        // Single-flight: a second concurrent background wake-up must not drive
        // the shared Syncthing instance while another sync is mid-flight — one
        // task's expiration/cleanup could stop the bridge under the other and
        // silently abort a transfer. The loser just nudges a rescan so its
        // changes are still picked up, then returns without competing.
        let didAcquireSyncSlot = syncInFlightLock.withLock { inFlight -> Bool in
            if inFlight { return false }
            inFlight = true
            return true
        }
        guard didAcquireSyncSlot else {
            logger.info("Background sync already in flight — coalescing (reason=\(reason))")
            trace("Concurrent background sync suppressed (reason=\(reason)); nudging rescan.")
            if SyncBridgeService.isRunning() {
                _ = requestFolderRescans()
            }
            return .alreadyIdle
        }
        defer { syncInFlightLock.withLock { $0 = false } }

        let syncStartedAt = Date()
        var telemetryEventCursor = latestBridgeEventID()
        let syncStartEventCursor = telemetryEventCursor

        // Lifecycle decisions run through the injectable guard core — the
        // decision logic and its read-timing are pinned by unit tests (#61).
        let guards = BackgroundSyncGuards(environment: .live)

        let alreadyRunning = guards.bridgeSnapshot()
        trace("Bridge state before sync: running=\(alreadyRunning).")
        if BackgroundSyncGuards.shouldFastPathRescan(reason: reason, bridgeAlreadyRunning: alreadyRunning) {
            logger.info("Silent push: triggering folder rescans to wake Syncthing peer dialer")
            if let rescanCount = requestFolderRescans() {
                trace("Silent push fast path: rescanning \(rescanCount) folder(s).")
            } else {
                trace("Silent push fast path: folder decode failed before rescan.")
            }
            traceRelevantBridgeEvents(since: &telemetryEventCursor, label: "after-fast-path-rescan")
        }

        // Check lifecycle lock: skip start/stop if foreground owns the instance.
        let ownership = guards.lifecycleOwnership(bridgeAlreadyRunning: alreadyRunning)
        var ownsLifecycle = ownership.backgroundOwns
        trace("Lifecycle ownership: foregroundOwns=\(ownership.foregroundOwns), backgroundOwns=\(ownsLifecycle).")

        var managedURLs: [URL] = []
        if ownsLifecycle {
            managedURLs = restoreBookmarkAccess()
            guard !managedURLs.isEmpty else {
                logger.info("No bookmarks restored")
                trace("Bookmark restore failed: no security-scoped access available.")
                return completeSync(
                    reason: reason,
                    result: .noBookmarkAccess,
                    detail: L10n.tr("No security-scoped bookmark access was available."),
                    startedAt: syncStartedAt,
                    initialEventCursor: syncStartEventCursor
                )
            }
            trace("Managed access restored (count=\(managedURLs.count)).")

            let configDir = syncthingConfigDir()
            let err = SyncBridgeService.startSyncthing(configDir: configDir)
            if let err, !err.isEmpty, !SyncBridgeService.isRunning() {
                logger.error("Background bridge start failed")
                trace("Bridge start failed.")
                releaseAccess(managedURLs)
                return completeSync(
                    reason: reason,
                    result: .bridgeStartFailed,
                    detail: L10n.tr("The embedded sync engine could not start."),
                    startedAt: syncStartedAt,
                    initialEventCursor: syncStartEventCursor
                )
            }
            trace("Bridge start requested for background-owned sync.")
        }

        let hasFolders = await waitForAnyFolders(maxWait: 3)
        trace("Folder availability check completed: hasFolders=\(hasFolders).")
        traceFolderStatuses(label: "post-folder-availability")
        guard hasFolders else {
            let completion = completeSync(
                reason: reason,
                result: .noFoldersConfigured,
                detail: L10n.tr("No folders were available for background sync."),
                startedAt: syncStartedAt,
                initialEventCursor: syncStartEventCursor
            )
            if ownsLifecycle {
                cleanupBackgroundManaged(managedURLs)
            }
            return completion
        }

        if ownsLifecycle {
            // Re-point any folder whose stored absolute path went stale after an
            // iOS container change, before syncing against it (issue #25).
            FolderPathReconciler.reconcileLive(obsidianRoot: managedURLs.first?.path)
        }

        var progressTracker = reason == "silent-push"
            ? SilentPushProgressTracker(lastEventID: syncStartEventCursor, startedAt: syncStartedAt)
            : nil
        if progressTracker != nil {
            trace("Silent push local-progress tracking started.")
        }
        var forcedRestartPerformed = false

        if reason == "silent-push" {
            let sawWakeEvidence = await waitForSilentPushWakeEvidence(maxWait: 4)
            trace("Silent push wake evidence: \(sawWakeEvidence).")
            traceRelevantBridgeEvents(since: &telemetryEventCursor, label: "post-wake-evidence-window")
            traceFolderStatuses(label: "post-wake-evidence-window")
            // The guard re-reads the lifecycle lock at decision time — the
            // cycle-start snapshot goes stale exactly when the user opens
            // the app mid-push and the foreground adopts the running engine
            // (#60); see BackgroundSyncGuards.
            if guards.shouldForceRestartForSilentPush(sawWakeEvidence: sawWakeEvidence) {
                logger.warning("Silent push showed no peer/sync activity after rescan — forcing Syncthing restart")
                trace("No wake evidence after rescan. Forcing Syncthing restart.")
                let restart = await forceRestartForSilentPush(managedURLs: managedURLs)
                guard restart.success else {
                    trace("Forced restart failed.")
                    let completion = completeSync(
                        reason: reason,
                        result: .bridgeStartFailed,
                        detail: restart.errorDetail ?? L10n.tr("Forced silent-push restart failed."),
                        startedAt: syncStartedAt,
                        initialEventCursor: syncStartEventCursor
                    )
                    if restart.ownsLifecycle {
                        cleanupBackgroundManaged(restart.managedURLs)
                    }
                    return completion
                }

                managedURLs = restart.managedURLs
                ownsLifecycle = restart.ownsLifecycle
                forcedRestartPerformed = true
                progressTracker?.requiresLocalDataProgress = true
                progressTracker?.lastEventID = latestBridgeEventID()
                trace("Forced restart succeeded. Background now owns lifecycle=\(ownsLifecycle).")
                trace("Silent push local-progress tracking reset after forced restart.")

                let recoveredFolders = await waitForAnyFolders(maxWait: 3)
                trace("Post-restart folder availability: \(recoveredFolders).")
                trace("Managed access restored after restart (count=\(managedURLs.count)).")
                traceFolderStatuses(label: "post-forced-restart")
                guard recoveredFolders else {
                    let completion = completeSync(
                        reason: reason,
                        result: .noFoldersConfigured,
                        detail: L10n.tr("No folders were available after forced silent-push restart."),
                        startedAt: syncStartedAt,
                        initialEventCursor: syncStartEventCursor
                    )
                    if ownsLifecycle {
                        cleanupBackgroundManaged(managedURLs)
                    }
                    return completion
                }

                if ownsLifecycle {
                    FolderPathReconciler.reconcileLive(obsidianRoot: managedURLs.first?.path)
                }

                if let rescanCount = requestFolderRescans() {
                    trace("Post-restart local rescan requested for \(rescanCount) folder(s).")
                } else {
                    trace("Post-restart local rescan skipped because folder decode failed.")
                }
                traceRelevantBridgeEvents(since: &telemetryEventCursor, label: "after-post-restart-rescan")
            }
        }

        let setupProgressSnapshot = progressTracker?.poll()

        if allFoldersIdle() && !(progressTracker?.requiresLocalDataProgress == true) {
            trace("Folders already idle after setup checks.")
            // Surface conflicts on the fast idle path too — a small change that
            // conflicts and settles before the deadline loop would otherwise
            // skip notification. Must run BEFORE cleanup stops the bridge, since
            // the conflict scan reads through it. notifyConflictsIfAny is
            // internally gated (foreground/toggle/suppression), so it's cheap.
            await notifyConflictsIfAny()
            let completion = completeSync(
                reason: reason,
                result: .alreadyIdle,
                detail: nil,
                startedAt: syncStartedAt,
                initialEventCursor: syncStartEventCursor,
                localDataProgressObserved: setupProgressSnapshot?.sawLocalDataProgress == true
            )
            if ownsLifecycle {
                cleanupBackgroundManaged(managedURLs)
            }
            return completion
        }

        // Budget the wait from sync START, not from "now": the silent-push setup
        // above (folder availability, wake-evidence window, optional forced
        // restart) already consumed part of iOS's ~30s content-available budget.
        // An absolute deadline keeps total wall-clock under budget so overruns
        // don't get future wake-ups throttled.
        let deadline = syncStartedAt.addingTimeInterval(maxDuration)
        trace("Waiting for idle state until deadline.")
        while SyncBridgeService.isRunning() && Date() < deadline {
            if Task.isCancelled { break }
            try? await Task.sleep(for: .milliseconds(500))
            // Stop as soon as no folder can make further progress — either all
            // idle, or a folder is stuck in a terminal error state. Without the
            // error case a single stuck folder spins out the whole deadline.
            let settled = allFoldersSettledOrErrored()
            if var tracker = progressTracker {
                let snapshot = tracker.poll()
                progressTrackerTraceIfNeeded(snapshot)
                if settled && !snapshot.requiresLocalDataProgress {
                    progressTracker = tracker
                    break
                }
                progressTracker = tracker
            } else if settled {
                break
            }
        }

        let idle = allFoldersIdle()
        let settledWithError = !idle && allFoldersSettledOrErrored()
        let progressSnapshot = progressTracker?.poll()
        if let progressSnapshot {
            progressTrackerTraceIfNeeded(progressSnapshot)
        }
        trace("Deadline reached or idle observed. idle=\(idle).")
        traceRelevantBridgeEvents(since: &telemetryEventCursor, label: "pre-completion")
        traceFolderStatuses(label: "pre-completion")
        if idle {
            await notifyConflictsIfAny()
        }

        let result: SyncResult
        let detail: String?
        if let progressSnapshot, forcedRestartPerformed, progressSnapshot.requiresLocalDataProgress {
            result = .failed
            detail = L10n.tr("Silent push restarted Syncthing, but no real sync progress was observed before the app returned to idle.")
        } else if idle {
            result = .synced
            detail = nil
        } else if settledWithError {
            // Stopped because a folder is in a terminal error state, not because
            // we ran out of time. The folder error surfaces via the in-app Sync
            // Issues panel; don't raise a misleading "Background Sync Timed Out",
            // but don't report a clean success either (the widget should still
            // reflect the error, not a green idle state).
            result = .settledWithFolderError
            detail = L10n.tr("Background sync settled with at least one folder in an error state.")
        } else {
            result = .notIdleBeforeDeadline
            detail = L10n.fmt("Sync did not reach idle before %ds deadline.", Int(maxDuration))
        }
        let completion = completeSync(
            reason: reason,
            result: result,
            detail: detail,
            startedAt: syncStartedAt,
            initialEventCursor: syncStartEventCursor,
            localDataProgressObserved: progressSnapshot?.sawLocalDataProgress == true
        )
        // Only stop if we started it and foreground hasn't taken over.
        if ownsLifecycle {
            cleanupBackgroundManaged(managedURLs)
        }
        return completion
    }

    // MARK: - BGAppRefreshTask Handler

    private static func handleAppRefresh(task: BGAppRefreshTask) async {
        logger.info("Background refresh starting")
        await MainActor.run {
            scheduleAppRefresh()
        }

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

    // MARK: - BGProcessingTask Handler

    /// Overnight catch-up: the same sync cycle as app refresh, but with the
    /// multi-minute budget BGProcessingTask grants (charging + network
    /// required). If iOS expires the task before our own deadline, the
    /// expiration handler stops the bridge and the wait loop exits on
    /// isRunning() == false.
    private static func handleProcessing(task: BGProcessingTask) async {
        logger.info("Background processing starting")
        await MainActor.run {
            scheduleProcessing()
        }

        task.expirationHandler = {
            let shouldStop = lifecycleLock.withLock { !$0.foregroundActive }
            if shouldStop {
                SyncBridgeService.stopSyncthing()
                logger.info("Background processing expired — Syncthing stopped")
            } else {
                logger.info("Background processing expired — skipped stop (foreground active)")
            }
        }

        let result = await performBackgroundSync(reason: "processing", maxDuration: 180)
        task.setTaskCompleted(success: result.isSuccessful)
    }

    // MARK: - BGContinuedProcessingTask Handler

    @available(iOS 26.0, *)
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
            task.updateTitle(L10n.tr("Syncing Vault"), subtitle: L10n.fmt("%d%% complete", Int(pct)))
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

    /// Widget tier for a background completion (#76). Pure so the matrix is
    /// unit-testable. Routes through the SAME header cascade as the
    /// foreground write (decision 012): the run's own result maps to the
    /// severities `backgroundSyncIssueItem` will surface in-app, combined
    /// with the foreground-persisted floor of issues a background run cannot
    /// resolve — so a successful background sync can never overwrite an
    /// honest attention snapshot with a green idle. Engine/vault tiers are
    /// pinned: a stopped bridge is the normal end state of a background-owned
    /// run, not the "Starting" anomaly the foreground tier expresses.
    static func backgroundCompletionWidgetStatus(
        result: SyncResult,
        issueFloor: WidgetSnapshotStore.IssueFloor
    ) -> SyncStatus {
        var severities: [SyncthingManager.SyncIssueSeverity] = []
        switch issueFloor {
        case .none: break
        case .warning: severities.append(.warning)
        case .critical: severities.append(.critical)
        }
        switch result {
        case .synced, .alreadyIdle:
            break
        case .noFoldersConfigured, .notIdleBeforeDeadline:
            severities.append(.warning)
        case .noBookmarkAccess, .bridgeStartFailed, .failed, .settledWithFolderError:
            // settledWithFolderError surfaces in-app via the folderErrors
            // issue (critical), not via backgroundSyncIssueItem — same tier.
            severities.append(.critical)
        }
        return SyncHeaderModel.deriveWidgetStatus(
            hasEngineError: false,
            engineRunning: true,
            issueSeverities: severities,
            hasUnreachableFolders: false,
            isSyncing: false,
            hasSyncFolders: true
        )
    }

    /// Snapshot assembly for a background completion (#76 follow-up). Pure
    /// so the fallback matrix is unit-testable. Two rules keep the widget's
    /// numbers honest when the run cannot supply fresh values:
    /// - The last-sync triple (time, duration, files) is stamped only by a
    ///   successful run; a failure carries the previous snapshot's triple
    ///   forward so "last synced" keeps naming the last real sync, never the
    ///   failure moment.
    /// - The folder count comes from the bridge only while it is readable;
    ///   once a background-owned run stopped it, the bridge reports an empty
    ///   folder list — writing that through rendered "Vaults: 0" on the
    ///   widget, so the previous snapshot's count stands in instead.
    static func backgroundCompletionSnapshot(
        result: SyncResult,
        issueFloor: WidgetSnapshotStore.IssueFloor,
        completedAt: Date,
        startedAt: Date,
        bridgeRunning: Bool,
        liveFolderCount: Int,
        liveSyncedFiles: Int,
        previous: WidgetSnapshotStore.Snapshot?
    ) -> WidgetSnapshotStore.Snapshot {
        let status = backgroundCompletionWidgetStatus(result: result, issueFloor: issueFloor)
        let folderCount = bridgeRunning ? liveFolderCount : (previous?.folderCount ?? 0)
        if result.isSuccessful {
            return WidgetSnapshotStore.Snapshot(
                lastSyncTime: WidgetSnapshotStore.iso8601String(from: completedAt),
                lastSyncDuration: max(0, completedAt.timeIntervalSince(startedAt)),
                status: status.wireValue,
                filesSynced: liveSyncedFiles,
                folderCount: folderCount
            )
        }
        return WidgetSnapshotStore.Snapshot(
            lastSyncTime: previous?.lastSyncTime ?? "",
            lastSyncDuration: previous?.lastSyncDuration ?? 0,
            status: status.wireValue,
            filesSynced: previous?.filesSynced ?? 0,
            folderCount: folderCount
        )
    }

    /// Persist the outcome and the widget snapshot. Call BEFORE
    /// `cleanupBackgroundManaged` — the snapshot reads the folder count and
    /// the synced-file events through the bridge, and cleanup may stop it
    /// (#76 follow-up).
    @discardableResult
    private static func completeSync(
        reason: String,
        result: SyncResult,
        detail: String?,
        startedAt: Date,
        initialEventCursor: Int,
        localDataProgressObserved: Bool = false
    ) -> SyncResult {
        let outcome = SyncOutcome(
            timestamp: Date(),
            triggerReason: reason,
            result: result,
            detail: detail,
            localDataProgressObserved: localDataProgressObserved
        )
        persistSyncOutcome(outcome)
        if reason == "silent-push", localDataProgressObserved {
            RelaySyncProofStore.markLocalDataProgressObserved(at: outcome.timestamp)
        }
        WidgetSnapshotStore.write(
            snapshot: backgroundCompletionSnapshot(
                result: result,
                issueFloor: WidgetSnapshotStore.readIssueFloor(),
                completedAt: outcome.timestamp,
                startedAt: startedAt,
                bridgeRunning: SyncBridgeService.isRunning(),
                liveFolderCount: currentFolderCount(),
                liveSyncedFiles: syncedFileCount(since: initialEventCursor),
                previous: WidgetSnapshotStore.read()
            )
        )
        trace("Completed with result=\(result.rawValue).")
        logger.info("Background sync completed (reason=\(reason), result=\(result.rawValue))")
        return result
    }

    private static func persistSyncOutcome(_ outcome: SyncOutcome) {
        guard let data = try? JSONEncoder().encode(outcome) else { return }
        UserDefaults.standard.set(data, forKey: lastSyncOutcomeStorageKey)
        UserDefaults.standard.removeObject(forKey: legacyLastSyncOutcomeStorageKey)
        NotificationCenter.default.post(name: lastSyncOutcomeDidChangeNotification, object: nil)
    }

    /// Settlement classification for one folder, derived purely from its status
    /// fields so it can be unit-tested without the bridge.
    enum FolderSettlement: Equatable, Sendable {
        case idle        // nothing left to do
        case errored     // terminal error — cannot progress without intervention
        case active      // scanning, syncing, or outstanding work to pull
    }

    static func folderSettlement(
        state: String,
        needFiles: Int,
        needBytes: Int64,
        inProgressBytes: Int64
    ) -> FolderSettlement {
        if state == "error" { return .errored }
        // A folder is truly idle only when Syncthing is neither scanning nor
        // syncing AND has no outstanding work. The state field alone is not
        // enough: Syncthing briefly reports `idle` between scan and sync phases
        // while needBytes/needFiles still await a pull. Treating that window as
        // "done" caused background-sync handlers to shut Syncthing down before
        // any file was actually pulled.
        let hasPendingWork = needFiles > 0 || needBytes > 0 || inProgressBytes > 0
        if state == "idle" && !hasPendingWork { return .idle }
        return .active
    }

    /// Per-folder settlement snapshot, or nil when the folder list is empty or a
    /// status fails to decode (can't confirm state — caller treats as not-idle).
    private static func folderSettlements() -> [FolderSettlement]? {
        let json = SyncBridgeService.getFoldersJSON()
        guard let data = json.data(using: .utf8),
              let folders = try? JSONDecoder().decode([FolderStub].self, from: data),
              !folders.isEmpty else {
            return nil
        }

        var settlements: [FolderSettlement] = []
        settlements.reserveCapacity(folders.count)
        for folder in folders {
            let statusJSON = SyncBridgeService.getFolderStatusJSON(folderID: folder.id)
            guard let statusData = statusJSON.data(using: .utf8),
                  let status = try? JSONDecoder().decode(StatusStub.self, from: statusData) else {
                return nil
            }
            settlements.append(folderSettlement(
                state: status.state,
                needFiles: status.needFiles,
                needBytes: status.needBytes,
                inProgressBytes: status.inProgressBytes
            ))
        }
        return settlements
    }

    private static func allFoldersIdle() -> Bool {
        guard let settlements = folderSettlements() else { return false }
        return settlements.allSatisfy { $0 == .idle }
    }

    /// True when no folder can make further progress on its own — every folder
    /// is either idle or in a terminal error state. Used to stop waiting out the
    /// full deadline (and raising a misleading "timed out") when a folder is
    /// stuck in error and nothing more can happen.
    private static func allFoldersSettledOrErrored() -> Bool {
        guard let settlements = folderSettlements() else { return false }
        return settlements.allSatisfy { $0 != .active }
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

    private static func currentFolderCount() -> Int {
        let json = SyncBridgeService.getFoldersJSON()
        guard let data = json.data(using: .utf8),
              let folders = try? JSONDecoder().decode([FolderStub].self, from: data) else {
            return 0
        }
        return folders.count
    }

    private static func syncedFileCount(since lastEventID: Int) -> Int {
        let events = decodeBridgeEvents(from: SyncBridgeService.getEventsSince(lastID: lastEventID))
        return events.reduce(into: 0) { count, event in
            guard event.type == "ItemFinished" else { return }
            let error = event.data?["error"] ?? ""
            if error.isEmpty {
                count += 1
            }
        }
    }

    private static func notifyConflictsIfAny() async {
        // If the app is foreground-active, the in-app conflict UI already
        // surfaces these — don't also post a banner, and don't race the
        // foreground poll's baseline reconcile over the shared count key. (A
        // silent push can arrive while the app is open.)
        guard !isSceneActive() else { return }

        // In-app toggle (default ON). Read the raw object so an absent key —
        // every install from before this feature shipped — reads as ON, not
        // false (UserDefaults.bool returns false for a missing key). Gating
        // here, before the per-folder conflict scan, also skips that disk I/O
        // when the user has turned banners off.
        let bannersEnabled = (UserDefaults.standard.object(forKey: conflictNotificationsEnabledKey) as? Bool) ?? true
        guard bannersEnabled else { return }

        // Resolve `.obsidian` state-file conflicts (last-writer-wins) before
        // counting, so a background sync never wakes the user for conflicts
        // the app can settle on its own. Same opt-out as the foreground poll.
        autoResolveStateConflictsIfEnabled()

        guard let count = currentConflictCount() else {
            // Unreadable conflict snapshot (transient bridge/decode failure) —
            // leave the banner and persisted baseline untouched rather than
            // mistaking it for "no conflicts".
            return
        }
        let lastCount = UserDefaults.standard.integer(forKey: lastNotifiedConflictCountKey)
        let action = conflictNotificationAction(currentCount: count, lastNotifiedCount: lastCount)

        switch action {
        case .suppress:
            // Same count as last time — not new information. Leave the existing
            // banner alone so the screen does not light up every silent push.
            return

        case .clear:
            UNUserNotificationCenter.current().removeDeliveredNotifications(
                withIdentifiers: [conflictNotificationIdentifier]
            )
            UserDefaults.standard.set(0, forKey: lastNotifiedConflictCountKey)
            return

        case .alert, .updateQuiet:
            let content = UNMutableNotificationContent()
            content.title = L10n.tr("Sync Conflicts")
            content.body = count == 1
                ? L10n.tr("1 file has a sync conflict. Open VaultSync to resolve it.")
                : L10n.fmt("%d files have sync conflicts. Open VaultSync to resolve them.", count)
            if action == .alert {
                content.sound = .default
                content.interruptionLevel = .active
            } else {
                // Count dropped (some resolved) — refresh the number without a
                // sound or screen-wake.
                content.interruptionLevel = .passive
            }

            let request = UNNotificationRequest(
                identifier: conflictNotificationIdentifier,
                content: content,
                trigger: nil
            )

            do {
                try await UNUserNotificationCenter.current().add(request)
                UserDefaults.standard.set(count, forKey: lastNotifiedConflictCountKey)
                logger.info("Conflict notification \(action == .alert ? "posted" : "updated quietly") (\(count) conflicts)")
            } catch {
                logger.error("Failed to send notification")
            }
        }
    }

    /// Best-effort last-writer-wins cleanup of `.obsidian` state-file
    /// conflicts across all folders. Runs in the background path before the
    /// conflict count, mirroring what the foreground 2s poll does, so the
    /// notification only ever reflects conflicts that need the user.
    private static func autoResolveStateConflictsIfEnabled() {
        guard SyncthingManager.isAutoResolveStateConflictsEnabled else { return }
        let json = SyncBridgeService.getFoldersJSON()
        guard let data = json.data(using: .utf8),
              let folders = try? JSONDecoder().decode([FolderStub].self, from: data) else {
            return
        }
        for folder in folders {
            let result = SyncBridgeService.autoResolveStateConflicts(folderID: folder.id)
            if result.resolved > 0 {
                _ = SyncBridgeService.rescanFolder(folderID: folder.id)
            }
        }
    }

    /// Distinct conflicted files across all folders, as reported by the
    /// bridge's on-disk scan. Counts files (not copies) — same semantics as
    /// `SyncthingManager.unresolvedConflictCount` and the in-app banner, and
    /// the notification copy says "N files have sync conflicts".
    ///
    /// Returns nil when the snapshot is UNREADABLE — the folder list or any
    /// folder's conflict list failed to decode. A transient bridge/decode
    /// failure must NOT be mistaken for "no conflicts": that would clear the
    /// banner and reset the baseline, and the next successful read would then
    /// re-alert the still-present conflicts as if they were new.
    private static func currentConflictCount() -> Int? {
        let json = SyncBridgeService.getFoldersJSON()
        guard let data = json.data(using: .utf8),
              let folders = try? JSONDecoder().decode([FolderStub].self, from: data) else {
            return nil
        }

        var count = 0
        for folder in folders {
            let cJSON = SyncBridgeService.getConflictFilesJSON(folderID: folder.id)
            guard let cData = cJSON.data(using: .utf8),
                  let conflicts = try? JSONDecoder().decode([ConflictStub].self, from: cData) else {
                // Suppress rather than undercount this folder's conflicts to 0.
                return nil
            }
            count += Set(conflicts.map(\.originalPath)).count
        }
        return count
    }

    private static func restoreBookmarkAccess() -> [URL] {
        let id = "obsidian-root"
        guard let (url, isStale) = BookmarkService.resolveBookmark(identifier: id) else {
            trace("Bookmark lookup returned no stored Obsidian root.")
            return []
        }
        guard BookmarkService.startAccessing(url: url) else {
            trace("Bookmark access start failed.")
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
            trace("Forced restart start failed.")
            return (false, true, managedURLs, L10n.tr("The embedded sync engine could not restart."))
        }

        trace("Forced restart started bridge successfully.")
        return (true, true, managedURLs, nil)
    }

    private static func traceFolderStatuses(label: String) {
        let json = SyncBridgeService.getFoldersJSON()
        guard let data = json.data(using: .utf8),
              let folders = try? JSONDecoder().decode([FolderStub].self, from: data),
              !folders.isEmpty else {
            trace("Folder status [\(label)]: no decodable folders.")
            return
        }

        var idleCount = 0
        var activeCount = 0
        var errorCount = 0
        var decodeFailureCount = 0
        var foldersWithPendingWork = 0
        for folder in folders {
            guard let status = SyncBridgeService.getFolderStatus(folderID: folder.id) else {
                decodeFailureCount += 1
                continue
            }
            switch folderSettlement(
                state: status.state,
                needFiles: status.needFiles,
                needBytes: status.needBytes,
                inProgressBytes: status.inProgressBytes
            ) {
            case .idle: idleCount += 1
            case .active: activeCount += 1
            case .errored: errorCount += 1
            }
            if status.needFiles > 0 || status.needBytes > 0 || status.inProgressBytes > 0 {
                foldersWithPendingWork += 1
            }
        }
        trace(
            "Folder status [\(label)]: count=\(folders.count), idle=\(idleCount), active=\(activeCount), errors=\(errorCount), pending=\(foldersWithPendingWork), unreadable=\(decodeFailureCount)."
        )
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

        let typeCounts = relevant.reduce(into: [String: Int]()) { counts, event in
            let category: String
            switch event.type {
            case "DeviceConnected", "DeviceDisconnected": category = "connection"
            case "LocalIndexUpdated", "RemoteIndexUpdated": category = "index"
            case "ItemStarted", "ItemFinished": category = "item"
            case "StateChanged": category = "state"
            case "FolderCompletion": category = "completion"
            case "FolderErrors": category = "error"
            default: category = "other"
            }
            counts[category, default: 0] += 1
        }
        let summary = typeCounts.keys.sorted().map { "\($0)=\(typeCounts[$0, default: 0])" }.joined(separator: ", ")
        trace("Bridge events [\(label)]: count=\(relevant.count), \(summary).")
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
        let originalPath: String
        let conflictPath: String
    }

    struct SilentPushProgressTracker: Sendable {
        struct ProgressSnapshot: Sendable {
            let lastEventID: Int
            let requiresLocalDataProgress: Bool
            let sawLocalDataProgress: Bool
            let summaryForTrace: String?
        }

        var lastEventID: Int
        let startedAt: Date
        var requiresLocalDataProgress = false
        private(set) var sawLocalDataProgress = false

        mutating func poll() -> ProgressSnapshot {
            let events = BackgroundSyncService.decodeBridgeEvents(
                from: SyncBridgeService.getEventsSince(lastID: lastEventID)
            )
            return observe(events)
        }

        mutating func observe(_ events: [BridgeEventStub]) -> ProgressSnapshot {
            var summary: String?
            for event in events {
                guard event.id > lastEventID else { continue }
                lastEventID = event.id
                if !sawLocalDataProgress && event.indicatesLocalDataProgress(since: startedAt) {
                    sawLocalDataProgress = true
                    requiresLocalDataProgress = false
                    summary = "Observed local data progress."
                }
            }

            return ProgressSnapshot(
                lastEventID: lastEventID,
                requiresLocalDataProgress: requiresLocalDataProgress,
                sawLocalDataProgress: sawLocalDataProgress,
                summaryForTrace: summary
            )
        }
    }

    struct BridgeEventStub: Decodable, Sendable {
        let id: Int
        let type: String
        let time: String
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

        func indicatesLocalDataProgress(since startedAt: Date) -> Bool {
            guard type == "ItemFinished",
                  data?["type"] == "file",
                  data?["action"] == "update" || data?["action"] == "delete",
                  (data?["error"] ?? "").isEmpty,
                  data?["folder"]?.isEmpty == false,
                  let eventDate = SyncBridgeService.parseBridgeTimestamp(time),
                  eventDate >= startedAt else {
                return false
            }
            return true
        }
    }
}
