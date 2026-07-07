import Foundation
import Observation
import WidgetKit
import os

enum WidgetSnapshotStore {
    static let appGroupSuiteName = "group.eu.vaultsync.shared"
    static let snapshotDefaultsKey = "vaultsync.widget.snapshot"
    static let widgetKind = "VaultSyncWidget"

    struct Snapshot: Codable, Equatable, Sendable {
        let lastSyncTime: String
        let lastSyncDuration: Double
        let status: String
        let filesSynced: Int
        let folderCount: Int
    }

    static func write(snapshot: Snapshot) {
        guard let defaults = UserDefaults(suiteName: appGroupSuiteName),
              let data = try? JSONEncoder().encode(snapshot),
              let json = String(data: data, encoding: .utf8) else {
            return
        }

        defaults.set(json, forKey: snapshotDefaultsKey)
        WidgetCenter.shared.reloadTimelines(ofKind: widgetKind)
    }

    static func iso8601String(from date: Date?) -> String {
        guard let date else { return "" }
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime]
        return formatter.string(from: date)
    }
}

private let logger = Logger(subsystem: "eu.vaultsync.app", category: "syncthing")

/// Manages the embedded Syncthing instance lifecycle and state.
@Observable @MainActor
final class SyncthingManager {
    private(set) var isRunning = false
    /// True while `start()` is awaiting the (off-main) bridge start. The UI
    /// keeps showing the calm "Starting…" state during this window.
    private(set) var isStarting = false
    private(set) var deviceID = ""
    private(set) var devices: [DeviceInfo] = []

    /// First-observed-disconnect timestamp per device ID, for every configured
    /// device (the required-device filter is applied by the computed
    /// properties). Mutated by `applyDeviceList(_:)`.
    private var disconnectedSince: [String: Date] = [:]

    /// When the engine last (re)started. Disconnects observed shortly after
    /// this get the longer startup grace period. Cleared on stop.
    private(set) var engineStartedAt: Date?

    /// Length of the reconnecting grace period for a disconnect observed
    /// mid-session. After this many seconds a disconnected required device
    /// migrates from `reconnectingRequiredDeviceIDs` to
    /// `disconnectedRequiredDeviceIDs` and surfaces as a real warning.
    static let reconnectGracePeriod: TimeInterval = 30

    /// Grace period for disconnects observed right after an engine start.
    /// Cold-start reconnects legitimately take longer (empty discovery cache,
    /// possible relay fallback), so the calm "Connecting…" treatment holds
    /// longer before anything looks like a warning.
    static let startupGracePeriod: TimeInterval = 60

    /// How long after engine start a first-observed disconnect still counts
    /// as a cold-start reconnect (and gets `startupGracePeriod`).
    static let startupWindow: TimeInterval = 30

    /// Clock source for the grace-period calculation. Production code uses
    /// the default `{ Date() }`; tests inject a controllable clock via
    /// direct assignment. Intentionally exposed in release builds — this is
    /// a standard clock-injection seam and the production default is a
    /// no-op wrapper over `Date.init()`.
    var now: () -> Date = { Date() }

    private(set) var folders: [FolderInfo] = []
    private(set) var folderStatuses: [String: FolderStatusInfo] = [:]
    private(set) var conflictFiles: [String: [ConflictInfo]] = [:]
    private(set) var pendingFolders: [PendingFolderInfo] = []
    private(set) var ignoredPendingFolderIDs: Set<String> = []
    /// Folder IDs the user removed on this iPhone. While a peer still shares
    /// such a folder, its offer reappears as pending within moments — and
    /// auto-accepting it would silently undo the removal. Re-adding is an
    /// explicit user decision (doctrine 002 / #52). Deliberately never pruned:
    /// the set stays tiny and an entry is only lifted by an explicit accept.
    private(set) var userRemovedFolderIDs: Set<String> = []
    /// Settledness of the folder paths that accept decisions judge against
    /// (#56, decision 008). Observable so the UI can hold accept passes while
    /// a path reconcile is in flight and re-fire them once it completes.
    private(set) var pathSettlement = PathSettlement()
    private(set) var hasSeenPendingFolderOffer = false
    private(set) var lastSyncTime: Date?
    private(set) var lastSyncTimeByFolder: [String: Date] = [:]
    private(set) var syncActivity: [SyncEventItem] = []
    private(set) var lastBackgroundSyncOutcome: BackgroundSyncService.SyncOutcome?
    private(set) var error: String?
    private(set) var userError: SyncUserError?

    /// Whether any folder is currently syncing or scanning.
    var isAnySyncing: Bool {
        folderStatuses.values.contains { $0.state == "syncing" || $0.state == "scanning" }
    }

    private var pollTask: Task<Void, Never>?
    private var rescanTask: Task<Void, Never>?
    /// True once this externally initiated engine generation has used its one
    /// automatic restart after a detected engine death (#61). Reset by every
    /// external lifecycle transition (`stop`, `resetForRestart`,
    /// `adoptRunningEngine`) — deliberately NOT by the auto-restart's own
    /// `start()`, or a crash-looping engine would restart forever.
    private var engineDeathAutoRestartConsumed = false
    /// The Obsidian root the last `reconcileFolderPaths` call used. The
    /// death-detection auto-restart reconciles against it — the root cannot
    /// change mid-session (only a scene-level reconnect updates it, which
    /// triggers its own reconcile).
    private var lastReconcileObsidianRoot: String?
    private var previousFolderStates: [String: String] = [:]
    private var lastBridgeEventID = 0
    private var activityDeduplicationCache: [String: Date] = [:]
    private var nextSyntheticEventID = -1
    private var lastBackgroundOutcomeEventDate: Date?
    private var backgroundSyncObserver: NSObjectProtocol?
    private let syncHistoryStore: SyncHistoryStore
    private static let ignoredPendingFoldersDefaultsKey = "syncthing.ignoredPendingFolderIDs"
    private static let userRemovedFoldersDefaultsKey = "syncthing.userRemovedFolderIDs"
    private static let hasSeenPendingOfferDefaultsKey = "syncthing.hasSeenPendingFolderOffer"
    private static let staleSyncThreshold: TimeInterval = 12 * 60 * 60
    private static let maxSyncActivityItems = 120
    private static let maxFileEventsPerFolderPerPoll = 6

    /// Migration-safe silent auto-apply patterns. Hard-coded to the historical
    /// set so future changes to `IgnorePreset.recommended` (which can grow or
    /// shrink over time) do not silently mutate `.stignore` on existing vaults
    /// during startup auto-merge. The first-run recommendation sheet uses
    /// `IgnorePreset.recommended` separately for UI defaults — see
    /// `SyncFilterRecommendationSheet`.
    private nonisolated static let defaultIgnorePatterns: [String] = [
        ".Trash",
        ".obsidian/workspace.json",
        ".obsidian/workspace-mobile.json",
    ]

    /// Opt-out switch for automatic last-writer-wins resolution of conflicts
    /// on `.obsidian` state files. A missing key reads as ON so existing
    /// installs get the calmer behaviour without migration. Shared with
    /// `BackgroundSyncService` (background sync resolves before notifying)
    /// and `SettingsView` (the toggle).
    nonisolated static let autoResolveStateConflictsKey = "auto-resolve-state-conflicts-v1"

    nonisolated static var isAutoResolveStateConflictsEnabled: Bool {
        (UserDefaults.standard.object(forKey: autoResolveStateConflictsKey) as? Bool) ?? true
    }

    private var hasAppliedStartupIgnores = false
    private var activeWidgetSyncStart: Date?
    private var activeWidgetSyncFilesSynced = 0
    private var lastWidgetSyncCompletionTime: Date?
    private var lastWidgetSyncDuration: TimeInterval = 0
    private var lastWidgetSyncFilesSynced = 0
    private var lastWrittenWidgetSnapshot: WidgetSnapshotStore.Snapshot?

    private static let bridgeDateParser: ISO8601DateFormatter = {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime]
        return formatter
    }()

    init(syncHistoryStore: SyncHistoryStore = SyncHistoryStore()) {
        self.syncHistoryStore = syncHistoryStore

        ignoredPendingFolderIDs = Self.loadIgnoredPendingFolderIDs()
        userRemovedFolderIDs = Self.loadUserRemovedFolderIDs()
        hasSeenPendingFolderOffer = UserDefaults.standard.bool(forKey: Self.hasSeenPendingOfferDefaultsKey)

        let history = syncHistoryStore.load()
        lastSyncTime = history.globalLastSync
        lastSyncTimeByFolder = history.lastSyncByFolder
        lastWidgetSyncCompletionTime = history.globalLastSync

        lastBackgroundSyncOutcome = BackgroundSyncService.lastSyncOutcome()
        lastBackgroundOutcomeEventDate = lastBackgroundSyncOutcome?.timestamp
        if let outcome = lastBackgroundSyncOutcome {
            appendBackgroundSyncActivityIfNeeded(outcome, force: true)
        }

        backgroundSyncObserver = NotificationCenter.default.addObserver(
            forName: BackgroundSyncService.lastSyncOutcomeDidChangeNotification,
            object: nil,
            queue: .main
        ) { [weak self] _ in
            Task { @MainActor in
                self?.refreshBackgroundSyncOutcome()
            }
        }
    }

    struct DeviceInfo: Codable, Identifiable, Sendable {
        let deviceID: String
        let name: String
        let connected: Bool
        let paused: Bool

        var id: String { deviceID }

        init(from decoder: any Decoder) throws {
            let container = try decoder.container(keyedBy: CodingKeys.self)
            deviceID = try container.decode(String.self, forKey: .deviceID)
            name = try container.decode(String.self, forKey: .name)
            connected = try container.decode(Bool.self, forKey: .connected)
            paused = try container.decodeIfPresent(Bool.self, forKey: .paused) ?? false
        }
    }

    struct FolderInfo: Codable, Identifiable, Sendable {
        let id: String
        let label: String
        let path: String
        let type: String
        let paused: Bool
        let deviceIDs: [String]
    }

    struct FolderStatusInfo: Codable, Sendable {
        let state: String
        let stateChanged: String
        let completionPct: Double
        let globalBytes: Int64
        let globalFiles: Int
        let localBytes: Int64
        let localFiles: Int
        let needBytes: Int64
        let needFiles: Int
        let inProgressBytes: Int64
        let errorReason: String?
        let errorMessage: String?
        let errorPath: String?
        let errorChanged: String?

        init(payload: SyncBridgeService.FolderStatusPayload) {
            state = payload.state
            stateChanged = payload.stateChanged
            completionPct = payload.completionPct
            globalBytes = payload.globalBytes
            globalFiles = payload.globalFiles
            localBytes = payload.localBytes
            localFiles = payload.localFiles
            needBytes = payload.needBytes
            needFiles = payload.needFiles
            inProgressBytes = payload.inProgressBytes
            errorReason = payload.errorReason
            errorMessage = payload.errorMessage
            errorPath = payload.errorPath
            errorChanged = payload.errorChanged
        }
    }

    struct ConflictInfo: Codable, Identifiable, Sendable {
        let originalPath: String
        let conflictPath: String
        let conflictDate: String
        let deviceShortID: String

        var id: String { conflictPath }

        /// True when the conflicted file is Obsidian app state (lives inside a
        /// `.obsidian` directory at any depth) rather than a user note. State
        /// conflicts are eligible for automatic last-writer-wins resolution.
        /// Mirrors the Go bridge's `isStateFilePath`.
        var isStateConflict: Bool {
            originalPath.split(separator: "/").dropLast().contains(".obsidian")
        }
    }

    struct PendingFolderInfo: Codable, Identifiable, Hashable, Sendable {
        let id: String
        let label: String
        let offeredBy: [PendingDeviceInfo]
    }

    struct PendingDeviceInfo: Codable, Hashable, Sendable {
        let deviceID: String
        let name: String
        let time: String
    }

    enum SyncIssueSeverity: Sendable {
        case warning
        case critical
    }

    struct SyncIssueItem: Identifiable, Hashable, Sendable {
        enum Kind: String, Sendable {
            case pathCollision
            case nestedFolders
            case folderErrors
            case disconnectedPeers
            case pendingShares
            case conflicts
            case staleSync
            case backgroundSync
        }

        let kind: Kind
        let title: String
        let message: String
        let remediation: String
        let severity: SyncIssueSeverity
        let count: Int
        let folderID: String?
        let deviceID: String?

        var id: String {
            "\(kind.rawValue)|\(count)|\(folderID ?? "")|\(deviceID ?? "")"
        }
    }

    private struct BridgeEventInfo: Decodable {
        let id: Int
        let type: String
        let time: String
        let relevant: Bool?
        let data: [String: String]?
    }

    var actionablePendingFolders: [PendingFolderInfo] {
        pendingFolders.filter { !ignoredPendingFolderIDs.contains($0.id) }
    }

    /// Pending shares the automatic accept loop may act on: actionable (not
    /// ignored) and not previously removed by the user. A share whose folder
    /// the user removed stays visible as a pending row but is only ever
    /// accepted by an explicit tap (doctrine 002 / #52) — auto-accepting it
    /// would undo the removal moments later while a peer still shares it.
    var autoAcceptEligiblePendingFolders: [PendingFolderInfo] {
        Self.autoAcceptEligible(actionable: actionablePendingFolders, userRemoved: userRemovedFolderIDs)
    }

    /// Pure core of the auto-accept eligibility rule (unit-testable).
    nonisolated static func autoAcceptEligible(
        actionable: [PendingFolderInfo],
        userRemoved: Set<String>
    ) -> [PendingFolderInfo] {
        actionable.filter { !userRemoved.contains($0.id) }
    }

    var ignoredPendingFolders: [PendingFolderInfo] {
        pendingFolders.filter { ignoredPendingFolderIDs.contains($0.id) }
    }

    var staleSyncWarning: String? {
        guard !folders.isEmpty else { return nil }
        guard !isAnySyncing else { return nil }
        guard let lastSyncTime else {
            return L10n.tr("No successful sync has been recorded for your vaults yet.")
        }

        let age = Date().timeIntervalSince(lastSyncTime)
        guard age > Self.staleSyncThreshold else { return nil }
        let staleHours = Int(age / 3600)
        if staleHours >= 24 {
            let staleDays = max(1, staleHours / 24)
            return L10n.fmt(
                "Last successful sync was more than %d %@ ago.",
                staleDays,
                staleDays == 1 ? L10n.tr("day") : L10n.tr("days")
            )
        }
        let hours = max(1, staleHours)
        return L10n.fmt(
            "Last successful sync was about %d %@ ago.",
            hours,
            hours == 1 ? L10n.tr("hour") : L10n.tr("hours")
        )
    }

    var folderIDsWithErrors: [String] {
        folderStatuses
            .filter { $0.value.state == "error" }
            .map(\.key)
            .sorted()
    }

    /// When a device's reconnect grace window ends. Disconnects first observed
    /// within `startupWindow` of an engine start get the longer
    /// `startupGracePeriod` (measured from engine start); everything else gets
    /// `reconnectGracePeriod` from the disconnect itself.
    private func graceDeadline(firstDisconnected: Date) -> Date {
        if let engineStartedAt,
           firstDisconnected.timeIntervalSince(engineStartedAt) >= 0,
           firstDisconnected.timeIntervalSince(engineStartedAt) < Self.startupWindow {
            return engineStartedAt.addingTimeInterval(Self.startupGracePeriod)
        }
        return firstDisconnected.addingTimeInterval(Self.reconnectGracePeriod)
    }

    /// True while a disconnected device is still inside its reconnect grace
    /// window — the UI shows a calm "Connecting…" instead of a warning state.
    func isWithinReconnectGrace(deviceID: String) -> Bool {
        guard let since = disconnectedSince[deviceID] else { return false }
        return graceDeadline(firstDisconnected: since) > now()
    }

    /// Required devices whose disconnect is still within its grace period.
    /// Surfaced as a calm "Connecting…" dashboard state, not a warning.
    /// Paused devices are excluded — pausing is intentional, not a reconnect.
    var reconnectingRequiredDeviceIDs: [String] {
        let nowDate = now()
        let required = Set(folders.flatMap(\.deviceIDs))
        let paused = Set(devices.filter(\.paused).map(\.deviceID))
        return disconnectedSince
            .filter {
                graceDeadline(firstDisconnected: $0.value) > nowDate
                    && required.contains($0.key)
                    && !paused.contains($0.key)
            }
            .map(\.key)
            .sorted()
    }

    /// Required devices that have been disconnected for longer than their grace
    /// period, plus any required device that has never appeared in the device
    /// list at all (e.g. peer removed from config but still listed on a folder).
    /// Paused devices are excluded — an intentionally paused peer must not
    /// raise the "required device disconnected" issue.
    var disconnectedRequiredDeviceIDs: [String] {
        let nowDate = now()
        let required = Set(folders.flatMap(\.deviceIDs))
        let paused = Set(devices.filter(\.paused).map(\.deviceID))
        let stale = disconnectedSince
            .filter {
                graceDeadline(firstDisconnected: $0.value) <= nowDate
                    && required.contains($0.key)
                    && !paused.contains($0.key)
            }
            .map(\.key)

        let unresolvedUnknown = required.subtracting(Set(devices.map(\.deviceID)))

        return Array(Set(stale).union(unresolvedUnknown)).sorted()
    }

    /// Number of distinct files that currently have at least one conflict
    /// copy. Counts files, not copies: with `MaxConflicts: 10` a single
    /// churn-prone file can accumulate many copies, and counting each copy
    /// made the home-screen banner shout "10 conflicts" for what is one
    /// decision.
    var unresolvedConflictCount: Int {
        conflictFiles.values.reduce(0) { $0 + Set($1.map(\.originalPath)).count }
    }

    /// Decode a conflict-scan payload and report whether any entry is an
    /// auto-resolvable state conflict. Gates the auto-resolve walk in the
    /// poll loop on folders that actually need it.
    nonisolated static func containsStateConflict(conflictsJSON: String) -> Bool {
        guard let data = conflictsJSON.data(using: .utf8),
              let decoded = try? JSONDecoder().decode([ConflictInfo].self, from: data) else {
            return false
        }
        return decoded.contains { $0.isStateConflict }
    }

    var unresolvedIssues: [SyncIssueItem] {
        var issues: [SyncIssueItem] = []

        // Two or more folders sharing one local path is active data corruption
        // (issue #45): Syncthing merges their contents and pushes the mix to
        // every peer. The launch-time guard pauses them to stop the bleeding;
        // this is the most severe issue, so it leads the list and stays up until
        // the user separates the vaults. Same canonical-path rule as the
        // accept-time guard so detection can never disagree with it.
        let collisionGroups = PathCollisionGuard.collidingFolderGroups(
            folders.map { (id: $0.id, path: $0.path) },
            canonicalize: FolderPathReconciler.canonical
        )
        if !collisionGroups.isEmpty {
            let affectedCount = collisionGroups.reduce(0) { $0 + $1.count }
            issues.append(
                SyncIssueItem(
                    kind: .pathCollision,
                    title: L10n.tr("Two Vaults Are Sharing One Folder"),
                    message: L10n.tr("Two or more vaults sync into the same local folder, so their contents are being mixed together. The affected vaults have been paused to stop further damage."),
                    remediation: L10n.tr("Remove an affected vault on this iPhone, then accept it again under Pending Shares — it moves into its own folder. If the files are already mixed, restore the clean copy on your computer first."),
                    severity: .critical,
                    count: affectedCount,
                    folderID: collisionGroups.flatMap { $0 }.min(),
                    deviceID: nil
                )
            )
        }

        // A folder nested inside another folder's directory is the same
        // corruption one level down: the outer vault syncs the inner vault's
        // files as its own content, and a peer deleting that stray copy would
        // wipe the inner vault everywhere (#45 follow-up). Paused by the same
        // launch-time guard; surfaced as its own issue because the recovery
        // differs — re-select the container folder, then remove the inner vault.
        let nestedIDs = PathCollisionGuard.nestedFolderIDs(
            folders.map { (id: $0.id, path: $0.path) },
            canonicalize: FolderPathReconciler.canonical
        )
        if !nestedIDs.isEmpty {
            issues.append(
                SyncIssueItem(
                    kind: .nestedFolders,
                    title: L10n.tr("One Vault Is Nested Inside Another"),
                    message: L10n.tr("A vault's folder is inside another vault's folder, so the outer vault syncs the inner vault's notes to its own devices. The affected vaults have been paused to stop further mixing."),
                    remediation: L10n.tr("Select the folder that contains your vaults (\"On My iPhone\" → \"Obsidian\") as VaultSync's Obsidian directory, then remove the inner vault on this iPhone and accept it again under Pending Shares — it gets its own folder. Only afterwards, delete the leftover copy inside the outer vault on your other devices."),
                    severity: .critical,
                    count: nestedIDs.count,
                    folderID: nestedIDs.min(),
                    deviceID: nil
                )
            )
        }

        // Folders stuck on a stale/inaccessible path are surfaced by their own
        // guided "remove / reconnect" card, so exclude them here to avoid
        // double-listing them with the generic (and, for them, useless)
        // "rescan failed vaults" remediation.
        let unreachableIDs = Set(unreachableFolders.map(\.id))
        let erroredFolderIDs = folderIDsWithErrors.filter { !unreachableIDs.contains($0) }
        if !erroredFolderIDs.isEmpty {
            let count = erroredFolderIDs.count
            issues.append(
                SyncIssueItem(
                    kind: .folderErrors,
                    title: count == 1 ? L10n.tr("1 Vault Has Sync Errors") : L10n.fmt("%d Vaults Have Sync Errors", count),
                    message: L10n.tr("At least one folder is currently in an error state."),
                    remediation: L10n.tr("Rescan failed vaults, then verify folder access and permissions."),
                    severity: .critical,
                    count: count,
                    folderID: erroredFolderIDs.first,
                    deviceID: nil
                )
            )
        }

        if !disconnectedRequiredDeviceIDs.isEmpty {
            let count = disconnectedRequiredDeviceIDs.count
            issues.append(
                SyncIssueItem(
                    kind: .disconnectedPeers,
                    title: count == 1 ? L10n.tr("1 Required Device Is Disconnected") : L10n.fmt("%d Required Devices Are Disconnected", count),
                    message: L10n.tr("Some shared peers are offline or unreachable right now."),
                    remediation: L10n.tr("Reconnect devices or add missing peers to restore continuous sync."),
                    severity: .warning,
                    count: count,
                    folderID: nil,
                    deviceID: disconnectedRequiredDeviceIDs.first
                )
            )
        }

        let pendingCount = actionablePendingFolders.count
        if pendingCount > 0 {
            issues.append(
                SyncIssueItem(
                    kind: .pendingShares,
                    title: pendingCount == 1 ? L10n.tr("1 Pending Share Needs Attention") : L10n.fmt("%d Pending Shares Need Attention", pendingCount),
                    message: L10n.tr("Pending shares are waiting to be accepted before sync can start."),
                    remediation: L10n.tr("Accept a share to activate syncing for that vault."),
                    severity: .warning,
                    count: pendingCount,
                    folderID: actionablePendingFolders.first?.id,
                    deviceID: actionablePendingFolders.first?.offeredBy.first?.deviceID
                )
            )
        }

        if unresolvedConflictCount > 0 {
            let firstFolderID = conflictFiles
                .filter { !$0.value.isEmpty }
                .sorted(by: { $0.key < $1.key })
                .first?
                .key
            issues.append(
                SyncIssueItem(
                    kind: .conflicts,
                    title: unresolvedConflictCount == 1 ? L10n.tr("1 Conflict Needs Resolution") : L10n.fmt("%d Conflicts Need Resolution", unresolvedConflictCount),
                    message: L10n.tr("Conflicts mean multiple versions exist and need a manual decision."),
                    remediation: L10n.tr("Open conflicts and choose which version to keep."),
                    severity: .warning,
                    count: unresolvedConflictCount,
                    folderID: firstFolderID,
                    deviceID: nil
                )
            )
        }

        if let staleSyncWarning {
            issues.append(
                SyncIssueItem(
                    kind: .staleSync,
                    title: L10n.tr("Sync Activity Looks Stale"),
                    message: staleSyncWarning,
                    remediation: L10n.tr("Trigger a vault rescan to refresh sync state."),
                    severity: .warning,
                    count: 1,
                    folderID: nil,
                    deviceID: nil
                )
            )
        }

        if let backgroundIssue = backgroundSyncIssueItem() {
            issues.append(backgroundIssue)
        }

        return issues
    }

    private func backgroundSyncIssueItem() -> SyncIssueItem? {
        guard let outcome = lastBackgroundSyncOutcome else { return nil }
        guard outcome.result.shouldSurfaceIssue else { return nil }

        let severity: SyncIssueSeverity
        switch outcome.result {
        case .bridgeStartFailed, .noBookmarkAccess, .failed:
            severity = .critical
        case .noFoldersConfigured, .notIdleBeforeDeadline:
            severity = .warning
        case .synced, .alreadyIdle, .settledWithFolderError:
            return nil
        }

        let trigger = localizedTriggerReason(outcome.triggerReason)
        let detail = outcome.detail ?? outcome.result.issueMessage

        return SyncIssueItem(
            kind: .backgroundSync,
            title: outcome.result.issueTitle,
            message: L10n.fmt("%@ (Trigger: %@)", detail, trigger),
            remediation: outcome.result.remediation,
            severity: severity,
            count: 1,
            folderID: nil,
            deviceID: nil
        )
    }

    /// Start Syncthing using the app's Documents directory.
    ///
    /// The bridge call loads certificates, parses the config, and opens the
    /// SQLite index database — blocking work that used to stall the main
    /// thread (and the launch frame) on big vaults, so it runs detached and
    /// the method is `async`. Re-entrant calls during an in-flight start are
    /// no-ops, mirroring the `isRunning` guard.
    func start() async {
        guard !isRunning, !isStarting else { return }
        isStarting = true
        defer { isStarting = false }

        let configDir = Self.configDirectory()
        logger.info("Starting Syncthing with configDir: \(configDir)")

        BackgroundSyncService.lifecycleLock.withLock { $0.foregroundActive = true }

        let startError = await Task.detached(priority: .userInitiated) {
            SyncBridgeService.startSyncthing(configDir: configDir)
        }.value

        if let err = startError {
            BackgroundSyncService.lifecycleLock.withLock { $0.foregroundActive = false }
            logger.error("Failed to start Syncthing: \(err)")
            error = err
            userError = SyncUserError.from(rawMessage: err, fallbackTitle: L10n.tr("Could Not Start Sync"))
            return
        }

        engineStartedAt = now()
        isRunning = true
        deviceID = SyncBridgeService.deviceID()
        error = nil
        userError = nil
        logger.info("Syncthing started. Device ID: \(self.deviceID)")

        startPolling()
    }

    /// Attach this manager to an engine that is already running but was
    /// started outside the manager — by a background handler
    /// (`BackgroundSyncService.performBackgroundSync`) in a process iOS
    /// launched in the background, before the foreground scene ever ran
    /// `start()` (#60). Restores manager state and starts polling; the caller
    /// must fire `reconcileFolderPaths` afterwards — until that reconcile
    /// completes, the adopted engine's paths count as unsettled and accept
    /// decisions stay held (decision 008; the fresh `pathSettlement`
    /// generation enforces this without a special case).
    ///
    /// Returns false when there is no running engine to adopt after all (it
    /// stopped between the caller's check and the claim) — the caller should
    /// fall back to a cold `start()`.
    func adoptRunningEngine() -> Bool {
        guard !isRunning, !isStarting else { return isRunning }

        // Claim the lifecycle lock BEFORE verifying the engine still runs —
        // never the other way around. The background handlers re-read this
        // lock immediately before their stop (`cleanupBackgroundManaged`, the
        // BGTask expiration handlers), so claiming first closes the window in
        // which a finishing background sync would stop the engine under the
        // freshly adopted foreground. Verify-then-claim re-opens that window:
        // the engine could pass the check and be stopped before the claim
        // lands, leaving the manager attached to nothing.
        BackgroundSyncService.lifecycleLock.withLock { $0.foregroundActive = true }

        guard SyncBridgeService.isRunning() else {
            // Nothing to adopt — release the claim so background handlers
            // regain lifecycle ownership, and let the caller cold-start.
            BackgroundSyncService.lifecycleLock.withLock { $0.foregroundActive = false }
            return false
        }

        engineStartedAt = now()
        isRunning = true
        deviceID = SyncBridgeService.deviceID()
        error = nil
        userError = nil
        // Adoption is an external lifecycle transition: the adopted
        // generation gets a fresh death-auto-restart budget (#61).
        engineDeathAutoRestartConsumed = false
        logger.info("Adopted running Syncthing engine started by a background handler. Device ID: \(self.deviceID)")

        startPolling()
        return true
    }

    /// Re-derive and correct every folder's absolute path from the current
    /// Obsidian root before a stale path can strand a folder in a permanent
    /// access error (issue #25). Safe to call on every engine start — unchanged
    /// paths are a no-op. The blocking bridge work runs off the main actor.
    ///
    /// Returns the reconcile task so a caller can sequence work after paths
    /// have settled: an accept pass that runs concurrently would compute its
    /// occupied-path set from the pre-reconcile folder list — stale exactly
    /// when the user is repairing a container move (#53).
    @discardableResult
    func reconcileFolderPaths(obsidianRoot: String?) -> Task<Void, Never> {
        lastReconcileObsidianRoot = obsidianRoot
        guard isRunning else { return Task {} }
        // Mark paths unsettled BEFORE the detached work exists: the poll loop
        // can deliver pendingFolders at any suspension point, and an accept
        // pass must find the hold already in place (#56).
        let token = pathSettlement.reconcileBegan()
        // Run the blocking bridge work off the main actor; only the final
        // folder-list refresh hops back to the main actor.
        return Task.detached(priority: .utility) { [weak self] in
            // Wait briefly for the engine to load its folder list after start.
            for _ in 0..<12 {
                guard SyncBridgeService.isRunning() else {
                    // Engine died mid-wait: nothing was reconciled. Abandon —
                    // never settle — so accept decisions stay held until a
                    // fresh start's reconcile completes (#56).
                    await self?.markReconcile(token: token, completed: false)
                    return
                }
                let json = SyncBridgeService.getFoldersJSON()
                if json != "[]", !json.isEmpty { break }
                try? await Task.sleep(for: .milliseconds(250))
            }
            FolderPathReconciler.reconcileLive(obsidianRoot: obsidianRoot)
            // With paths settled, pause any folders an older version already
            // merged onto one local path (#45 migration shield) — once each.
            // Runs before the refresh below so the new paused state and the
            // critical banner surface on this same launch.
            PathCollisionGuard.pauseCollisionsLive()
            await self?.refreshFolders()
            await self?.markReconcile(token: token, completed: true)
        }
    }

    /// Record a reconcile outcome on the main actor. Completion settles paths
    /// and releases held accept passes; abandonment only releases the
    /// in-flight count and keeps accepts held (see `PathSettlement`).
    private func markReconcile(token: PathSettlement.Token, completed: Bool) {
        if completed {
            pathSettlement.reconcileFinished(token: token)
        } else {
            pathSettlement.reconcileAbandoned(token: token)
        }
    }

    /// Stop the running Syncthing instance.
    func stop() {
        stopPolling()
        SyncBridgeService.stopSyncthing()
        BackgroundSyncService.lifecycleLock.withLock { $0.foregroundActive = false }
        isRunning = false
        deviceID = ""
        devices = []
        disconnectedSince.removeAll()
        engineStartedAt = nil
        folders = []
        folderStatuses = [:]
        conflictFiles = [:]
        pendingFolders = []
        // New generation: accepts hold until the next start's reconcile
        // completes, and a still-running reconcile's late outcome is ignored
        // (#56).
        pathSettlement.reset()
        lastBridgeEventID = 0
        activityDeduplicationCache = [:]
        nextSyntheticEventID = -1
        error = nil
        userError = nil
        engineDeathAutoRestartConsumed = false
        logger.info("Syncthing stopped")
    }

    /// Add a peer device by Device ID.
    func addDevice(id: String, name: String) -> String? {
        let result = SyncBridgeService.addDevice(deviceID: id, name: name)
        if result == nil {
            refreshDevices()
        }
        return result
    }

    /// Remove a peer device by Device ID.
    func removeDevice(id: String) -> String? {
        let result = SyncBridgeService.removeDevice(deviceID: id)
        if result == nil {
            refreshDevices()
        }
        return result
    }

    // MARK: - Folder management

    /// Add a new folder.
    func addFolder(id: String, label: String, path: String) -> String? {
        let result = SyncBridgeService.addFolder(id: id, label: label, path: path)
        if result == nil {
            refreshFolders()
            let folderID = id
            Task.detached {
                try? await Task.sleep(for: .seconds(2))
                guard !Task.isCancelled else { return }
                Self.applyDefaultIgnoresIfNeeded(folderID: folderID)
            }
        }
        return result
    }

    /// Remove a folder by ID.
    func removeFolder(id: String) -> String? {
        let result = SyncBridgeService.removeFolder(id: id)
        if result == nil {
            refreshFolders()
            folderStatuses.removeValue(forKey: id)
            // Drop the path mapping so a future folder reusing this ID does not
            // inherit a stale relative path.
            FolderPathReconciler.removeRel(forFolder: id)
            // Forget any #45 auto-pause record so a future folder reusing this
            // ID can be paused again if it collides (removing a colliding vault
            // is the sanctioned recovery; accepting the returning share re-adds
            // it unpaused into its own folder).
            PathCollisionGuard.clearAutoPaused(id)
            // Never auto-re-accept a share the user just removed: while a peer
            // still shares the folder, the offer reappears within moments, and
            // silently pulling it back in would undo the removal (doctrine
            // 002 / #52). The share stays visible under Pending Shares until
            // the user explicitly accepts it — which then honours a manually
            // chosen target.
            userRemovedFolderIDs.insert(id)
            persistUserRemovedFolderIDs()
        }
        return result
    }

    /// Share a folder with a device.
    func shareFolderWithDevice(folderID: String, deviceID: String) -> String? {
        let result = SyncBridgeService.shareFolderWithDevice(folderID: folderID, deviceID: deviceID)
        if result == nil {
            refreshFolders()
        }
        return result
    }

    /// Unshare a folder from a device.
    func unshareFolderFromDevice(folderID: String, deviceID: String) -> String? {
        let result = SyncBridgeService.unshareFolderFromDevice(folderID: folderID, deviceID: deviceID)
        if result == nil {
            refreshFolders()
        }
        return result
    }

    /// Trigger a rescan of a folder.
    func rescanFolder(id: String) -> String? {
        SyncBridgeService.rescanFolder(folderID: id)
    }

    /// Trigger a foreground sync using the same rescan path as the main UI.
    /// If a sync is already active, ignore the request to keep it idempotent.
    func triggerForegroundSync(folderID: String? = nil) {
        guard !isAnySyncing else {
            logger.info("Ignoring sync request because a sync is already in progress")
            return
        }

        Task {
            await performForegroundSyncRequest(folderID: folderID)
        }
    }

    /// Async variant of `triggerForegroundSync` for callers that want to await
    /// completion — e.g. SwiftUI `.refreshable`, where the spinner should stay
    /// visible until the trigger has actually landed in the bridge.
    func performForegroundSync(folderID: String? = nil) async {
        guard !isAnySyncing else {
            logger.info("Ignoring sync request because a sync is already in progress")
            return
        }
        await performForegroundSyncRequest(folderID: folderID)
    }

    // MARK: - Conflict management

    /// Resolve a conflict file. Returns nil on success.
    func resolveConflict(folderID: String, conflictFileName: String, keepConflict: Bool) -> String? {
        let result = SyncBridgeService.resolveConflict(
            folderID: folderID,
            conflictFileName: conflictFileName,
            keepConflict: keepConflict
        )
        if result == nil {
            refreshConflicts()
        }
        return result
    }

    /// Keep both versions by renaming the conflict file. Returns (nil, newPath) on success, or (error, nil) on failure.
    func keepBothConflict(folderID: String, conflict: ConflictInfo) -> (error: String?, newPath: String?) {
        let result = SyncBridgeService.keepBothConflict(folderID: folderID, conflictFileName: conflict.conflictPath)
        if result == nil {
            refreshConflicts()
            let url = URL(fileURLWithPath: conflict.originalPath)
            let ext = url.pathExtension
            let base = url.deletingPathExtension().path
            let newPath = ext.isEmpty ? "\(base).conflict-\(conflict.deviceShortID)" : "\(base).conflict-\(conflict.deviceShortID).\(ext)"
            return (nil, newPath)
        }
        return (result, nil)
    }

    // MARK: - Pending folder shares

    /// Accept a pending folder offer. Creates the folder locally and shares with offering devices.
    /// `allowNonEmpty` carries the user's explicit merge confirmation (or a
    /// recorded manual target, #52) through to the Go hard floor (#54).
    func acceptPendingFolder(folderID: String, label: String, path: String, allowNonEmpty: Bool) -> String? {
        let result = SyncBridgeService.acceptPendingFolder(folderID: folderID, label: label, path: path, allowNonEmpty: allowNonEmpty)
        if result == nil {
            // An explicit accept supersedes an earlier removal — lift the
            // auto-accept suppression for this folder ID (#52).
            if userRemovedFolderIDs.contains(folderID) {
                userRemovedFolderIDs.remove(folderID)
                persistUserRemovedFolderIDs()
            }
            refreshFolders()
            refreshPendingFolders()
            // Trigger a rescan after a short delay to kick-start initial sync.
            // The delay gives Syncthing time to initialize the folder model
            // before we request the scan.
            let id = folderID
            rescanTask?.cancel()
            rescanTask = Task.detached {
                try? await Task.sleep(for: .seconds(2))
                guard !Task.isCancelled else { return }
                Self.applyDefaultIgnoresIfNeeded(folderID: id)
                _ = SyncBridgeService.rescanFolder(folderID: id)
            }
        }
        return result
    }

    // MARK: - Device rename

    /// Rename a peer device.
    func renameDevice(id: String, newName: String) -> String? {
        let result = SyncBridgeService.renameDevice(deviceID: id, newName: newName)
        if result == nil {
            refreshDevices()
        }
        return result
    }

    /// Reset Swift-side state after Syncthing was stopped externally
    /// (e.g., by a BGTask expiration handler). Does NOT call the bridge.
    func resetForRestart() {
        stopPolling()
        BackgroundSyncService.lifecycleLock.withLock { $0.foregroundActive = false }
        isRunning = false
        deviceID = ""
        devices = []
        disconnectedSince.removeAll()
        engineStartedAt = nil
        folders = []
        folderStatuses = [:]
        conflictFiles = [:]
        pendingFolders = []
        // New generation, same as stop(): the restarted engine's paths count
        // as unsettled until its own reconcile completes (#56).
        pathSettlement.reset()
        lastBridgeEventID = 0
        activityDeduplicationCache = [:]
        nextSyntheticEventID = -1
        error = nil
        userError = nil
        engineDeathAutoRestartConsumed = false
    }

    /// The poll loop found the bridge dead under an attached manager — the
    /// residual #60 adoption race or an engine crash. Without this the
    /// manager keeps polling empty JSON and the UI shows "Ready" while
    /// nothing syncs (#61). Transition to a clean stopped state and restart
    /// once per external generation; a second death in the same generation
    /// stays stopped and tells the user, because blind restarts would just
    /// flap a crash-looping engine.
    private func handleEngineDeath() {
        guard isRunning else { return }
        let restartAllowed = !engineDeathAutoRestartConsumed
        logger.warning("Sync engine died under an attached manager (autoRestartAllowed=\(restartAllowed))")

        // Same reset as the scene-activation cold-start path. It also clears
        // engineDeathAutoRestartConsumed, so consume AFTER the reset — the
        // flag must survive into the restarted generation.
        resetForRestart()
        engineDeathAutoRestartConsumed = true

        guard restartAllowed else {
            userError = SyncUserError(
                category: .syncthingNotRunning,
                title: L10n.tr("Sync Engine Stopped"),
                message: L10n.tr("The sync engine stopped unexpectedly."),
                remediation: L10n.tr("Close and reopen VaultSync to restart syncing."),
                technicalDetails: nil
            )
            return
        }

        Task {
            await start()
            // Reconcile against the last known root so accept decisions do
            // not stay held until the next scene cycle (decision 008): a
            // restarted engine with no completed reconcile would park every
            // share accept indefinitely, because a scene return over a
            // running engine (`alreadyAttached`) never fires one.
            reconcileFolderPaths(obsidianRoot: lastReconcileObsidianRoot)
        }
    }

    // MARK: - Default ignore patterns

    /// Apply default .stignore patterns for an Obsidian vault folder.
    ///
    /// Delegates the read-merge-write to the Go bridge's `EnsureDefaultIgnores`,
    /// which distinguishes "no .stignore yet" (safe to create) from "could not
    /// read .stignore" (transient error) and aborts on the latter. A naive
    /// Swift-side read could see a momentary empty/unreadable result and
    /// overwrite a populated `.stignore` with just the defaults — this avoids
    /// that data-loss path entirely.
    /// `nonisolated` so callers can run the bridge read-merge-write off the main
    /// actor — it touches no main-actor state, only the bridge and the logger.
    private nonisolated static func applyDefaultIgnoresIfNeeded(folderID: String) {
        guard let data = try? JSONEncoder().encode(defaultIgnorePatterns),
              let json = String(data: data, encoding: .utf8) else { return }

        if let error = SyncBridgeService.ensureDefaultIgnores(folderID: folderID, defaultsJSON: json) {
            logger.warning("Failed to ensure default ignores for \(folderID, privacy: .private): \(error, privacy: .private)")
        }
    }

    // MARK: - Private

    private func startPolling() {
        pollTask = Task {
            // Immediate initial refresh so state is available without waiting
            // for the first poll interval (prevents empty device list after restart).
            await pollBridgeState()

            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(2))
                guard !Task.isCancelled else { break }
                await pollBridgeState()
            }
        }
    }

    /// Run bridge calls off the main thread, then update @MainActor properties.
    private func pollBridgeState() async {
        let currentEventCursor = lastBridgeEventID
        let snapshot: (String, String, String, String)? = await Task.detached {
            // A dead bridge under an attached manager — the residual adoption
            // race from #60 (a background stop that passed its lock re-read
            // just before the foreground claimed) or an engine crash — must
            // never poll empty JSON into a healthy-looking "Ready" display
            // (#61). Detect it here, before decoding.
            guard SyncBridgeService.isRunning() else { return nil }
            let devicesJSON = SyncBridgeService.getDevicesJSON()
            let foldersJSON = SyncBridgeService.getFoldersJSON()
            let pendingJSON = SyncBridgeService.getPendingFoldersJSON()
            let eventsJSON = SyncBridgeService.getEventsSince(lastID: currentEventCursor)
            return (devicesJSON, foldersJSON, pendingJSON, eventsJSON)
        }.value

        guard let snapshot else {
            handleEngineDeath()
            return
        }

        // Decode on main — lightweight after the bridge calls are done.
        // Folders must be applied before devices so applyDeviceList sees the
        // current required-device set when reconciling disconnectedSince.
        if let data = snapshot.1.data(using: .utf8),
           let decoded = try? JSONDecoder().decode([FolderInfo].self, from: data) {
            folders = decoded
        }
        if let data = snapshot.0.data(using: .utf8),
           let decoded = try? JSONDecoder().decode([DeviceInfo].self, from: data) {
            applyDeviceList(decoded)
        }

        // One-time check: apply default .stignore patterns for existing folders.
        if !hasAppliedStartupIgnores && !folders.isEmpty {
            hasAppliedStartupIgnores = true
            let folderIDs = folders.map(\.id)
            Task.detached {
                try? await Task.sleep(for: .seconds(3))
                for id in folderIDs {
                    Self.applyDefaultIgnoresIfNeeded(folderID: id)
                }
            }
        }

        if let data = snapshot.2.data(using: .utf8),
           let decoded = try? JSONDecoder().decode([PendingFolderInfo].self, from: data) {
            pendingFolders = decoded
            if !decoded.isEmpty {
                markPendingShareSeen()
            }
            pruneIgnoredPendingFolders(availableFolderIDs: Set(decoded.map(\.id)))
        }

        let bridgeEvents = decodeBridgeEvents(snapshot.3)
        if let latestEventID = bridgeEvents.map(\.id).max() {
            lastBridgeEventID = max(lastBridgeEventID, latestEventID)
        }

        let folderNameByID = Dictionary(
            uniqueKeysWithValues: folders.map { folder in
                let displayName = folder.label.isEmpty ? folder.id : folder.label
                return (folder.id, displayName)
            }
        )
        let deviceNameByID = Dictionary(
            uniqueKeysWithValues: devices.map { device in
                let displayName = device.name.isEmpty ? shortDeviceID(device.deviceID) : device.name
                return (device.deviceID, displayName)
            }
        )
        appendActivityEvents(
            bridgeEvents,
            folderNamesByID: folderNameByID,
            deviceNamesByID: deviceNameByID
        )

        // Folder statuses + conflicts need the current folder list.
        let currentFolders = folders
        let statusSnapshot = await Task.detached {
            var statuses: [String: FolderStatusInfo] = [:]
            var conflicts: [String: Data] = [:]
            let autoResolve = Self.isAutoResolveStateConflictsEnabled
            for folder in currentFolders {
                if let status = SyncBridgeService.getFolderStatus(folderID: folder.id) {
                    statuses[folder.id] = FolderStatusInfo(payload: status)
                }
                var cJSON = SyncBridgeService.getConflictFilesJSON(folderID: folder.id)
                // Gate the (extra) auto-resolve walk on the scan we already
                // have, so the 2s poll only pays for it when a state conflict
                // actually exists.
                if autoResolve, Self.containsStateConflict(conflictsJSON: cJSON) {
                    let result = SyncBridgeService.autoResolveStateConflicts(folderID: folder.id)
                    if result.resolved > 0 {
                        // Keep Syncthing's index in line with the on-disk
                        // changes, then re-read so this poll already reports
                        // the calmer state.
                        _ = SyncBridgeService.rescanFolder(folderID: folder.id)
                        cJSON = SyncBridgeService.getConflictFilesJSON(folderID: folder.id)
                    }
                }
                if let d = cJSON.data(using: .utf8) {
                    conflicts[folder.id] = d
                }
            }
            return (statuses, conflicts)
        }.value

        let previousStatuses = folderStatuses
        let newStatuses = statusSnapshot.0
        updateWidgetSyncMetrics(previousStatuses: previousStatuses, newStatuses: newStatuses)
        updateSyncHistory(
            newStatuses: newStatuses,
            activeFolderIDs: Set(currentFolders.map(\.id))
        )
        folderStatuses = newStatuses

        var newConflicts: [String: [ConflictInfo]] = [:]
        for (id, data) in statusSnapshot.1 {
            if let decoded = try? JSONDecoder().decode([ConflictInfo].self, from: data), !decoded.isEmpty {
                newConflicts[id] = decoded
            }
        }
        conflictFiles = newConflicts
        BackgroundSyncService.reconcileConflictNotificationBaseline(currentCount: unresolvedConflictCount)
        writeWidgetSnapshotIfNeeded()
    }

    private func stopPolling() {
        pollTask?.cancel()
        pollTask = nil
        rescanTask?.cancel()
        rescanTask = nil
    }

    private func refreshBackgroundSyncOutcome() {
        guard let outcome = BackgroundSyncService.lastSyncOutcome() else { return }
        guard outcome != lastBackgroundSyncOutcome else { return }
        lastBackgroundSyncOutcome = outcome
        appendBackgroundSyncActivityIfNeeded(outcome, force: false)
    }

    private func appendBackgroundSyncActivityIfNeeded(
        _ outcome: BackgroundSyncService.SyncOutcome,
        force: Bool
    ) {
        if !force, let lastDate = lastBackgroundOutcomeEventDate, outcome.timestamp <= lastDate {
            return
        }
        lastBackgroundOutcomeEventDate = outcome.timestamp

        guard outcome.result.shouldSurfaceIssue else { return }

        let trigger = localizedTriggerReason(outcome.triggerReason)
        let title = L10n.fmt("%@ (%@)", outcome.result.issueTitle, trigger)
        let detail = outcome.detail ?? outcome.result.issueMessage
        let item = SyncEventItem(
            id: nextSyntheticID(),
            kind: .summary,
            date: outcome.timestamp,
            title: title,
            detail: detail,
            folderID: nil,
            deviceID: nil,
            filePath: nil
        )

        guard !isDuplicateActivity(item) else { return }
        syncActivity = Array(
            (syncActivity + [item])
                .sorted { lhs, rhs in
                    if lhs.date == rhs.date {
                        return lhs.id > rhs.id
                    }
                    return lhs.date > rhs.date
                }
                .prefix(Self.maxSyncActivityItems)
        )
    }

    /// Apply a new device list AND update the disconnected-since tracking
    /// dictionary. Single entry point for both `pollBridgeState` and
    /// `refreshDevices` so the timestamp accounting can't drift.
    private func applyDeviceList(_ newDevices: [DeviceInfo]) {
        devices = newDevices

        let nowDate = now()

        // Insert timestamps for newly-disconnected devices; clear them for
        // connected ones. All devices are tracked (the Devices tab needs the
        // per-device grace state); the required-device filter is applied by
        // the computed warning properties.
        for d in newDevices {
            if !d.connected {
                if disconnectedSince[d.deviceID] == nil {
                    disconnectedSince[d.deviceID] = nowDate
                }
            } else {
                disconnectedSince.removeValue(forKey: d.deviceID)
            }
        }

        // Drop entries for device IDs no longer present in the device list
        // (peer removed from config). Otherwise the dictionary leaks.
        let presentIDs = Set(newDevices.map(\.deviceID))
        disconnectedSince = disconnectedSince.filter { presentIDs.contains($0.key) }
    }

    private func refreshDevices() {
        let json = SyncBridgeService.getDevicesJSON()
        guard let data = json.data(using: .utf8),
              let decoded = try? JSONDecoder().decode([DeviceInfo].self, from: data) else {
            return
        }
        applyDeviceList(decoded)
    }

    private func refreshFolders() {
        let json = SyncBridgeService.getFoldersJSON()
        guard let data = json.data(using: .utf8),
              let decoded = try? JSONDecoder().decode([FolderInfo].self, from: data) else {
            return
        }
        folders = decoded
    }

    private func refreshConflicts() {
        var allConflicts: [String: [ConflictInfo]] = [:]
        for folder in folders {
            let json = SyncBridgeService.getConflictFilesJSON(folderID: folder.id)
            guard let data = json.data(using: .utf8),
                  let decoded = try? JSONDecoder().decode([ConflictInfo].self, from: data) else {
                continue
            }
            if !decoded.isEmpty {
                allConflicts[folder.id] = decoded
            }
        }
        conflictFiles = allConflicts
        BackgroundSyncService.reconcileConflictNotificationBaseline(currentCount: unresolvedConflictCount)
    }

    private func refreshPendingFolders() {
        let json = SyncBridgeService.getPendingFoldersJSON()
        guard let data = json.data(using: .utf8),
              let decoded = try? JSONDecoder().decode([PendingFolderInfo].self, from: data) else {
            return
        }
        pendingFolders = decoded
        if !decoded.isEmpty {
            markPendingShareSeen()
        }
        pruneIgnoredPendingFolders(availableFolderIDs: Set(decoded.map(\.id)))
    }

    private func decodeBridgeEvents(_ json: String) -> [BridgeEventInfo] {
        guard let data = json.data(using: .utf8),
              let decoded = try? JSONDecoder().decode([BridgeEventInfo].self, from: data) else {
            return []
        }
        return decoded
    }

    private func appendActivityEvents(
        _ bridgeEvents: [BridgeEventInfo],
        folderNamesByID: [String: String],
        deviceNamesByID: [String: String]
    ) {
        guard !bridgeEvents.isEmpty else { return }

        var nextItems = syncActivity
        var emittedFileEventsByFolder: [String: Int] = [:]
        var suppressedFileEventsByFolder: [String: Int] = [:]
        let now = Date()

        for event in bridgeEvents {
            guard event.relevant ?? true else { continue }
            guard let item = makeSyncEventItem(
                from: event,
                folderNamesByID: folderNamesByID,
                deviceNamesByID: deviceNamesByID
            ) else {
                continue
            }

            if item.kind == .fileSynced {
                let bucket = item.folderID ?? "unknown"
                if emittedFileEventsByFolder[bucket, default: 0] >= Self.maxFileEventsPerFolderPerPoll {
                    suppressedFileEventsByFolder[bucket, default: 0] += 1
                    beginWidgetSyncSessionIfNeeded(startDate: item.date)
                    activeWidgetSyncFilesSynced += 1
                    continue
                }
                emittedFileEventsByFolder[bucket, default: 0] += 1
            }

            if isDuplicateActivity(item) {
                continue
            }

            if item.kind == .fileSynced {
                beginWidgetSyncSessionIfNeeded(startDate: item.date)
                activeWidgetSyncFilesSynced += 1
            }

            nextItems.append(item)
        }

        if !suppressedFileEventsByFolder.isEmpty {
            for (folderID, count) in suppressedFileEventsByFolder.sorted(by: { $0.key < $1.key }) where count > 0 {
                let folderName = displayFolderName(folderID, folderNamesByID: folderNamesByID)
                let summaryTitle = count == 1
                    ? L10n.fmt("1 additional file synced in %@", folderName)
                    : L10n.fmt("%d additional files synced in %@", count, folderName)
                let summary = SyncEventItem(
                    id: nextSyntheticID(),
                    kind: .summary,
                    date: now,
                    title: summaryTitle,
                    detail: L10n.tr("Timeline updates were rate-limited to keep activity readable."),
                    folderID: folderID,
                    deviceID: nil,
                    filePath: nil
                )
                if !isDuplicateActivity(summary) {
                    nextItems.append(summary)
                }
            }
        }

        syncActivity = Array(
            nextItems
                .sorted { lhs, rhs in
                    if lhs.date == rhs.date {
                        return lhs.id > rhs.id
                    }
                    return lhs.date > rhs.date
                }
                .prefix(Self.maxSyncActivityItems)
        )
        pruneActivityDeduplicationCache(referenceDate: now)
    }

    private func makeSyncEventItem(
        from event: BridgeEventInfo,
        folderNamesByID: [String: String],
        deviceNamesByID: [String: String]
    ) -> SyncEventItem? {
        let data = event.data ?? [:]
        let timestamp = parseBridgeDate(event.time) ?? Date()

        switch event.type {
        case "StateChanged":
            let folderID = data["folder"]
            let folderName = displayFolderName(folderID, folderNamesByID: folderNamesByID)
            let fromState = data["from"]?.lowercased() ?? ""
            let toState = data["to"]?.lowercased() ?? ""

            if toState == "scanning", fromState != "scanning" {
                return SyncEventItem(
                    id: event.id,
                    kind: .scanStarted,
                    date: timestamp,
                    title: L10n.fmt("Scanning started in %@", folderName),
                    detail: L10n.tr("Syncthing is scanning local changes."),
                    folderID: folderID,
                    deviceID: nil,
                    filePath: nil
                )
            }
            if fromState == "scanning", toState == "idle" {
                return SyncEventItem(
                    id: event.id,
                    kind: .scanCompleted,
                    date: timestamp,
                    title: L10n.fmt("Scanning completed in %@", folderName),
                    detail: L10n.tr("The folder scan finished successfully."),
                    folderID: folderID,
                    deviceID: nil,
                    filePath: nil
                )
            }
            if toState == "syncing", fromState != "syncing" {
                return SyncEventItem(
                    id: event.id,
                    kind: .syncStarted,
                    date: timestamp,
                    title: L10n.fmt("Sync started in %@", folderName),
                    detail: L10n.tr("Files are being synchronized with peers."),
                    folderID: folderID,
                    deviceID: nil,
                    filePath: nil
                )
            }
            if fromState == "syncing", toState == "idle" {
                return SyncEventItem(
                    id: event.id,
                    kind: .syncCompleted,
                    date: timestamp,
                    title: L10n.fmt("Sync completed in %@", folderName),
                    detail: L10n.tr("Folder reached idle state after syncing."),
                    folderID: folderID,
                    deviceID: nil,
                    filePath: nil
                )
            }
            if toState == "error" || (data["error"]?.isEmpty == false) {
                return SyncEventItem(
                    id: event.id,
                    kind: .folderError,
                    date: timestamp,
                    title: L10n.fmt("Sync error in %@", folderName),
                    detail: data["error"] ?? L10n.tr("Folder entered an error state."),
                    folderID: folderID,
                    deviceID: nil,
                    filePath: nil
                )
            }
            return nil

        case "ItemFinished":
            guard let folderID = data["folder"],
                  let itemPath = data["item"],
                  !itemPath.isEmpty else {
                return nil
            }
            let folderName = displayFolderName(folderID, folderNamesByID: folderNamesByID)
            if let errorMessage = data["error"], !errorMessage.isEmpty {
                return SyncEventItem(
                    id: event.id,
                    kind: .fileError,
                    date: timestamp,
                    title: L10n.fmt("Failed to sync file in %@", folderName),
                    detail: L10n.fmt("%@: %@", itemPath, errorMessage),
                    folderID: folderID,
                    deviceID: nil,
                    filePath: itemPath
                )
            }
            return SyncEventItem(
                id: event.id,
                kind: .fileSynced,
                date: timestamp,
                title: L10n.fmt("File synced in %@", folderName),
                detail: itemPath,
                folderID: folderID,
                deviceID: nil,
                filePath: itemPath
            )

        case "DeviceConnected":
            let deviceID = data["id"]
            let deviceName = displayDeviceName(deviceID, deviceNamesByID: deviceNamesByID)
            return SyncEventItem(
                id: event.id,
                kind: .deviceConnected,
                date: timestamp,
                title: L10n.fmt("%@ connected", deviceName),
                detail: data["addr"] ?? L10n.tr("Peer connection is active."),
                folderID: nil,
                deviceID: deviceID,
                filePath: nil
            )

        case "DeviceDisconnected":
            let deviceID = data["id"]
            let deviceName = displayDeviceName(deviceID, deviceNamesByID: deviceNamesByID)
            return SyncEventItem(
                id: event.id,
                kind: .deviceDisconnected,
                date: timestamp,
                title: L10n.fmt("%@ disconnected", deviceName),
                detail: data["error"] ?? L10n.tr("Connection to peer was closed."),
                folderID: nil,
                deviceID: deviceID,
                filePath: nil
            )

        case "FolderErrors":
            let folderID = data["folder"]
            let folderName = displayFolderName(folderID, folderNamesByID: folderNamesByID)
            let message = data["message"] ?? L10n.tr("Folder reported an error.")
            let detail: String
            if let path = data["path"], !path.isEmpty {
                detail = L10n.fmt("%@: %@", path, message)
            } else {
                detail = message
            }

            return SyncEventItem(
                id: event.id,
                kind: .folderError,
                date: timestamp,
                title: L10n.fmt("Folder error in %@", folderName),
                detail: detail,
                folderID: folderID,
                deviceID: nil,
                filePath: data["path"]
            )

        default:
            return nil
        }
    }

    private func isDuplicateActivity(_ item: SyncEventItem) -> Bool {
        let fingerprint = [
            item.kind.rawValue,
            item.folderID ?? "",
            item.deviceID ?? "",
            item.filePath ?? "",
            item.title,
            item.detail
        ].joined(separator: "|")

        if let previous = activityDeduplicationCache[fingerprint],
           item.date.timeIntervalSince(previous) < 2 {
            return true
        }

        activityDeduplicationCache[fingerprint] = item.date
        return false
    }

    private func pruneActivityDeduplicationCache(referenceDate: Date) {
        let cutoff = referenceDate.addingTimeInterval(-180)
        activityDeduplicationCache = activityDeduplicationCache.filter { $0.value >= cutoff }
    }

    private func nextSyntheticID() -> Int {
        defer { nextSyntheticEventID -= 1 }
        return nextSyntheticEventID
    }

    private func parseBridgeDate(_ value: String) -> Date? {
        Self.bridgeDateParser.date(from: value)
    }

    private func displayFolderName(
        _ folderID: String?,
        folderNamesByID: [String: String]
    ) -> String {
        guard let folderID, !folderID.isEmpty else { return L10n.tr("Unknown Folder") }
        return folderNamesByID[folderID] ?? folderID
    }

    private func displayDeviceName(
        _ deviceID: String?,
        deviceNamesByID: [String: String]
    ) -> String {
        guard let deviceID, !deviceID.isEmpty else { return L10n.tr("Unknown Device") }
        return deviceNamesByID[deviceID] ?? shortDeviceID(deviceID)
    }

    private func localizedTriggerReason(_ reason: String) -> String {
        switch reason {
        case "silent-push":
            return L10n.tr("Silent Push")
        case "app-refresh":
            return L10n.tr("App Refresh")
        default:
            return reason.replacingOccurrences(of: "-", with: " ").capitalized
        }
    }

    private func shortDeviceID(_ deviceID: String) -> String {
        let firstGroup = deviceID.split(separator: "-").first.map(String.init)
        if let firstGroup, !firstGroup.isEmpty {
            return firstGroup
        }
        return String(deviceID.prefix(7))
    }

    private func updateSyncHistory(
        newStatuses: [String: FolderStatusInfo],
        activeFolderIDs: Set<String>
    ) {
        var didChange = false

        for (folderID, status) in newStatuses {
            let previousState = previousFolderStates[folderID]

            if didTransitionToSuccessfulIdle(previousState: previousState, status: status) {
                if upsertLastSyncDate(folderID: folderID, date: Date()) {
                    didChange = true
                }
            }

            if shouldTreatIdleStateAsSuccess(status: status, existingDate: lastSyncTimeByFolder[folderID]),
               let changedAt = parseBridgeDate(status.stateChanged),
               upsertLastSyncDate(folderID: folderID, date: changedAt) {
                didChange = true
            }

            previousFolderStates[folderID] = status.state
        }

        let filtered = lastSyncTimeByFolder.filter { activeFolderIDs.contains($0.key) }
        if filtered.count != lastSyncTimeByFolder.count {
            lastSyncTimeByFolder = filtered
            didChange = true
        }

        if let latestFolderSync = lastSyncTimeByFolder.values.max(),
           latestFolderSync > (lastSyncTime ?? .distantPast) {
            lastSyncTime = latestFolderSync
            didChange = true
        }

        if didChange {
            persistSyncHistory()
        }
    }

    private func didTransitionToSuccessfulIdle(
        previousState: String?,
        status: FolderStatusInfo
    ) -> Bool {
        guard let previousState else { return false }
        let wasActive = previousState == "syncing" || previousState == "scanning"
        guard wasActive, status.state == "idle" else { return false }
        return status.needFiles == 0 && status.errorMessage == nil
    }

    private func shouldTreatIdleStateAsSuccess(
        status: FolderStatusInfo,
        existingDate: Date?
    ) -> Bool {
        guard status.state == "idle" else { return false }
        guard status.needFiles == 0 else { return false }
        guard status.errorMessage == nil else { return false }
        guard let changedAt = parseBridgeDate(status.stateChanged) else { return false }
        if let existingDate, changedAt <= existingDate {
            return false
        }
        return true
    }

    @discardableResult
    private func upsertLastSyncDate(folderID: String, date: Date) -> Bool {
        if let existing = lastSyncTimeByFolder[folderID], existing >= date {
            return false
        }
        lastSyncTimeByFolder[folderID] = date
        if let lastSyncTime {
            if date > lastSyncTime {
                self.lastSyncTime = date
            }
        } else {
            lastSyncTime = date
        }
        return true
    }

    private func persistSyncHistory() {
        syncHistoryStore.save(
            globalLastSync: lastSyncTime,
            lastSyncByFolder: lastSyncTimeByFolder
        )
    }

    private func performForegroundSyncRequest(folderID: String?) async {
        if !isRunning {
            await start()
        }

        let normalizedFolderID = folderID?.trimmingCharacters(in: .whitespacesAndNewlines)
        let availableFolders = await waitForFoldersForSyncRequest(maxWait: 3)

        let targetFolderIDs: [String]
        if let normalizedFolderID, !normalizedFolderID.isEmpty {
            guard availableFolders.contains(where: { $0.id == normalizedFolderID }) else {
                logger.warning("Ignoring sync request for unknown folder ID: \(normalizedFolderID, privacy: .public)")
                return
            }
            targetFolderIDs = [normalizedFolderID]
        } else {
            guard !availableFolders.isEmpty else {
                logger.info("Ignoring sync request because no folders are configured")
                return
            }
            targetFolderIDs = availableFolders.map(\.id)
        }

        guard !isAnySyncing else {
            logger.info("Ignoring sync request because a sync started while preparing the request")
            return
        }

        beginWidgetSyncSessionIfNeeded(startDate: Date())

        var didTriggerSync = false
        var lastTriggerError: String?

        for id in Array(Set(targetFolderIDs)).sorted() {
            if let err = rescanFolder(id: id) {
                lastTriggerError = err
                logger.error("Foreground sync trigger failed for \(id, privacy: .public): \(err, privacy: .public)")
            } else {
                didTriggerSync = true
            }
        }

        if didTriggerSync {
            error = nil
            userError = nil
            writeWidgetSnapshotIfNeeded(statusOverride: .syncing)
            return
        }

        if let lastTriggerError {
            error = lastTriggerError
            userError = SyncUserError.from(
                rawMessage: lastTriggerError,
                fallbackTitle: L10n.tr("Could Not Start Sync")
            )
        }
        completeWidgetSyncSession(status: .error, completedAt: Date())
    }

    private func waitForFoldersForSyncRequest(maxWait: TimeInterval) async -> [FolderInfo] {
        let deadline = Date(timeIntervalSinceNow: maxWait)

        while Date() < deadline {
            refreshFolders()
            if !folders.isEmpty || !SyncBridgeService.isRunning() {
                return folders
            }
            try? await Task.sleep(for: .milliseconds(250))
        }

        refreshFolders()
        return folders
    }

    private enum WidgetSnapshotStatus: String {
        case idle
        case syncing
        case error
    }

    private func updateWidgetSyncMetrics(
        previousStatuses: [String: FolderStatusInfo],
        newStatuses: [String: FolderStatusInfo]
    ) {
        let wasSyncing = previousStatuses.values.contains { $0.state == "syncing" || $0.state == "scanning" }
        let isSyncingNow = newStatuses.values.contains { $0.state == "syncing" || $0.state == "scanning" }

        if isSyncingNow {
            beginWidgetSyncSessionIfNeeded(startDate: Date())
        } else if wasSyncing || activeWidgetSyncStart != nil {
            completeWidgetSyncSession(
                status: currentWidgetSnapshotStatus(using: newStatuses),
                completedAt: Date()
            )
        }
    }

    private func beginWidgetSyncSessionIfNeeded(startDate: Date) {
        guard activeWidgetSyncStart == nil else { return }
        activeWidgetSyncStart = startDate
        activeWidgetSyncFilesSynced = 0
    }

    private func completeWidgetSyncSession(
        status: WidgetSnapshotStatus,
        completedAt: Date
    ) {
        let startedAt = activeWidgetSyncStart ?? completedAt
        lastWidgetSyncCompletionTime = completedAt
        lastWidgetSyncDuration = max(0, completedAt.timeIntervalSince(startedAt))
        lastWidgetSyncFilesSynced = activeWidgetSyncFilesSynced
        activeWidgetSyncStart = nil
        activeWidgetSyncFilesSynced = 0
        writeWidgetSnapshotIfNeeded(statusOverride: status)
    }

    private func currentWidgetSnapshotStatus(
        using statuses: [String: FolderStatusInfo]? = nil
    ) -> WidgetSnapshotStatus {
        let statuses = statuses ?? folderStatuses

        if error != nil || userError != nil || statuses.values.contains(where: { $0.state == "error" }) {
            return .error
        }

        if activeWidgetSyncStart != nil ||
            statuses.values.contains(where: { $0.state == "syncing" || $0.state == "scanning" }) {
            return .syncing
        }

        return .idle
    }

    private func writeWidgetSnapshotIfNeeded(statusOverride: WidgetSnapshotStatus? = nil) {
        let snapshot = WidgetSnapshotStore.Snapshot(
            lastSyncTime: WidgetSnapshotStore.iso8601String(from: lastWidgetSyncCompletionTime ?? lastSyncTime),
            lastSyncDuration: activeWidgetSyncStart == nil ? lastWidgetSyncDuration : 0,
            status: (statusOverride ?? currentWidgetSnapshotStatus()).rawValue,
            filesSynced: activeWidgetSyncStart == nil ? lastWidgetSyncFilesSynced : activeWidgetSyncFilesSynced,
            folderCount: folders.count
        )

        guard snapshot != lastWrittenWidgetSnapshot else { return }
        lastWrittenWidgetSnapshot = snapshot
        WidgetSnapshotStore.write(snapshot: snapshot)
    }

    func folderUserError(folderID: String) -> SyncUserError? {
        guard let status = folderStatuses[folderID], status.state == "error" else { return nil }
        return SyncUserError.fromFolderStatus(
            reason: status.errorReason,
            message: status.errorMessage,
            path: status.errorPath
        )
    }

    /// A folder stuck in a path-related error that the launch-time path
    /// reconcile could not auto-heal — typically a legacy folder pointing at a
    /// since-removed app-container location (issue #25). Surfaced to the user as
    /// a guided "remove this vault" (or "reconnect") prompt instead of an inert
    /// permanent error.
    struct UnreachableFolder: Identifiable, Sendable {
        let id: String
        let label: String
        let path: String
        let reason: String
        /// True if the folder has a recorded Obsidian-relative mapping, meaning
        /// re-picking the Obsidian directory can rebase it (vs. a legacy folder
        /// that only ever lived in app storage and should just be removed).
        let hasObsidianMapping: Bool
    }

    var unreachableFolders: [UnreachableFolder] {
        let pathErrorReasons: Set<String> = [
            "folder_path_missing",
            "permission_denied",
            "folder_path_invalid",
            "folder_path_unreadable",
        ]
        let rel = FolderPathReconciler.loadRel()
        return folders.compactMap { folder in
            guard let status = folderStatuses[folder.id], status.state == "error",
                  let reason = status.errorReason, pathErrorReasons.contains(reason)
            else { return nil }
            return UnreachableFolder(
                id: folder.id,
                label: folder.label.isEmpty ? folder.id : folder.label,
                path: status.errorPath ?? folder.path,
                reason: reason,
                hasObsidianMapping: rel[folder.id] != nil
            )
        }
    }

    func ignorePendingFolder(id: String) {
        ignoredPendingFolderIDs.insert(id)
        persistIgnoredPendingFolderIDs()
    }

    func unignorePendingFolder(id: String) {
        ignoredPendingFolderIDs.remove(id)
        persistIgnoredPendingFolderIDs()
    }

    private static func configDirectory() -> String {
        let documentsURL = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask).first!
        let syncDir = documentsURL.appendingPathComponent("syncthing", isDirectory: true)
        return syncDir.path
    }

    private func markPendingShareSeen() {
        guard !hasSeenPendingFolderOffer else { return }
        hasSeenPendingFolderOffer = true
        UserDefaults.standard.set(true, forKey: Self.hasSeenPendingOfferDefaultsKey)
    }

    private func pruneIgnoredPendingFolders(availableFolderIDs: Set<String>) {
        let filtered = ignoredPendingFolderIDs.intersection(availableFolderIDs)
        guard filtered != ignoredPendingFolderIDs else { return }
        ignoredPendingFolderIDs = filtered
        persistIgnoredPendingFolderIDs()
    }

    private func persistIgnoredPendingFolderIDs() {
        UserDefaults.standard.set(Array(ignoredPendingFolderIDs).sorted(), forKey: Self.ignoredPendingFoldersDefaultsKey)
    }

    private func persistUserRemovedFolderIDs() {
        UserDefaults.standard.set(Array(userRemovedFolderIDs).sorted(), forKey: Self.userRemovedFoldersDefaultsKey)
    }

    private static func loadUserRemovedFolderIDs() -> Set<String> {
        guard let values = UserDefaults.standard.array(forKey: userRemovedFoldersDefaultsKey) as? [String] else {
            return []
        }
        return Set(values)
    }

    private static func loadIgnoredPendingFolderIDs() -> Set<String> {
        guard let values = UserDefaults.standard.array(forKey: ignoredPendingFoldersDefaultsKey) as? [String] else {
            return []
        }
        return Set(values)
    }

    // MARK: - Sync Filters (Ignore Patterns)

    private static let recommendationSheetShownKey = "syncthing.recommendationSheetShownFolders"

    /// Read current `.stignore` lines, distinguishing "no patterns yet" from
    /// "could not parse bridge output". Returns nil only on decode failure.
    /// Used internally by every read-modify-write flow so a malformed bridge
    /// response can never silently cause `.stignore` to be overwritten with
    /// an empty list (CodeRabbit data-loss guard).
    private func readIgnorePatternsOrNil(folderID: String) -> [String]? {
        let raw = SyncBridgeService.getFolderIgnores(folderID: folderID)
        guard let data = raw.data(using: .utf8),
              let decoded = try? JSONDecoder().decode([String].self, from: data) else {
            return nil
        }
        return decoded
    }

    private func unreadableFiltersError() -> SyncUserError {
        SyncUserError.from(rawMessage: L10n.tr("Could not read current sync filters. Please try again."))
    }

    /// Read current `.stignore` lines for a folder. Display-friendly: returns
    /// an empty list if the bridge response cannot be parsed. Read-modify-write
    /// flows must use `readIgnorePatternsOrNil` instead.
    func ignorePatterns(folderID: String) -> [String] {
        readIgnorePatternsOrNil(folderID: folderID) ?? []
    }

    /// Replace all `.stignore` lines for a folder.
    @discardableResult
    func setIgnorePatterns(folderID: String, patterns: [String]) -> SyncUserError? {
        guard let data = try? JSONEncoder().encode(patterns),
              let json = String(data: data, encoding: .utf8) else {
            return SyncUserError.from(rawMessage: "encoding ignore patterns failed")
        }
        if let err = SyncBridgeService.setFolderIgnores(folderID: folderID, ignoresJSON: json) {
            return SyncUserError.from(rawMessage: err)
        }
        return nil
    }

    /// Atomically add or remove a preset's patterns from `.stignore`. Aborts
    /// without writing if the current `.stignore` cannot be parsed, so an
    /// unreadable bridge response can never wipe existing rules.
    @discardableResult
    func togglePreset(_ preset: IgnorePreset, folderID: String, enabled: Bool) -> SyncUserError? {
        guard var current = readIgnorePatternsOrNil(folderID: folderID) else {
            return unreadableFiltersError()
        }
        let presetSet = Set(preset.patterns)
        if enabled {
            for pattern in preset.patterns where !current.contains(pattern) {
                current.append(pattern)
            }
        } else {
            current.removeAll { presetSet.contains($0) }
        }
        return setIgnorePatterns(folderID: folderID, patterns: current)
    }

    /// Add a single pattern (e.g. exact relPath from a conflict). No-op if
    /// already present. Aborts without writing if the current `.stignore`
    /// cannot be parsed.
    @discardableResult
    func addIgnorePattern(_ pattern: String, folderID: String) -> SyncUserError? {
        addIgnorePatterns([pattern], folderID: folderID)
    }

    /// Add multiple patterns at once, preserving the order of existing lines and
    /// appending only those not already present (intra-batch duplicates are also
    /// skipped). No-op if every pattern is already present. Aborts without
    /// writing if the current `.stignore` cannot be parsed, so an unreadable
    /// bridge response can never wipe or reorder existing rules.
    @discardableResult
    func addIgnorePatterns(_ patterns: [String], folderID: String) -> SyncUserError? {
        guard var current = readIgnorePatternsOrNil(folderID: folderID) else {
            return unreadableFiltersError()
        }
        var seen = Set(current)
        var appended = false
        for pattern in patterns where !seen.contains(pattern) {
            current.append(pattern)
            seen.insert(pattern)
            appended = true
        }
        guard appended else { return nil }
        return setIgnorePatterns(folderID: folderID, patterns: current)
    }

    /// Remove the given patterns from `.stignore`, preserving the order of every
    /// remaining line. No-op if none are present. Aborts without writing if the
    /// current `.stignore` cannot be parsed — so a delete never silently
    /// reorders the file (Syncthing matches first-pattern-wins, so order is
    /// semantically significant, e.g. for `!` un-ignore rules).
    @discardableResult
    func removeIgnorePatterns(_ patterns: [String], folderID: String) -> SyncUserError? {
        guard var current = readIgnorePatternsOrNil(folderID: folderID) else {
            return unreadableFiltersError()
        }
        let removeSet = Set(patterns)
        let before = current.count
        current.removeAll { removeSet.contains($0) }
        guard current.count != before else { return nil }
        return setIgnorePatterns(folderID: folderID, patterns: current)
    }

    /// Apply a target set of preset toggles and detected-pattern toggles to
    /// `.stignore`. Sheet-managed entries (preset patterns + the given
    /// detected items) are removed first, then re-added only if currently
    /// enabled, so deselecting actually takes effect. Custom patterns the
    /// user added previously are preserved. Aborts without writing if the
    /// current `.stignore` cannot be parsed.
    @discardableResult
    func applyRecommendedFilters(
        folderID: String,
        enabledPresetIDs: Set<String>,
        detectedPatterns: [String],
        enabledDetectedPatterns: Set<String>
    ) -> SyncUserError? {
        guard let existing = readIgnorePatternsOrNil(folderID: folderID) else {
            return unreadableFiltersError()
        }
        let managed = Set(IgnorePreset.all.flatMap(\.patterns))
            .union(detectedPatterns)
        var patterns = existing.filter { !managed.contains($0) }

        for preset in IgnorePreset.all where enabledPresetIDs.contains(preset.id) {
            for pattern in preset.patterns where !patterns.contains(pattern) {
                patterns.append(pattern)
            }
        }
        for pattern in enabledDetectedPatterns where !patterns.contains(pattern) {
            patterns.append(pattern)
        }
        return setIgnorePatterns(folderID: folderID, patterns: patterns)
    }

    /// Run the Go-side scanner for known heavy directories.
    /// `nonisolated static` so views can dispatch it on a detached Task without
    /// blocking the main actor.
    nonisolated static func scanFolderForKnownPatterns(folderID: String) -> [DetectedPattern] {
        let raw = SyncBridgeService.scanFolderForKnownPatterns(folderID: folderID)
        guard let data = raw.data(using: .utf8),
              let decoded = try? JSONDecoder().decode(DetectedScan.self, from: data) else {
            return []
        }
        return decoded.detected
    }

    func hasShownRecommendationSheet(folderID: String) -> Bool {
        let shown = UserDefaults.standard.array(forKey: Self.recommendationSheetShownKey) as? [String] ?? []
        return shown.contains(folderID)
    }

    func markRecommendationSheetShown(folderID: String) {
        var shown = UserDefaults.standard.array(forKey: Self.recommendationSheetShownKey) as? [String] ?? []
        guard !shown.contains(folderID) else { return }
        shown.append(folderID)
        UserDefaults.standard.set(shown, forKey: Self.recommendationSheetShownKey)
    }

    // MARK: - Skip Family

    /// Returns the `.stignore` glob that matches every Syncthing conflict copy
    /// of the given original file (relative path inside the folder).
    /// Example: "Personal/diary.md" -> "Personal/diary.sync-conflict-*".
    /// Files with no extension still work: "Makefile" -> "Makefile.sync-conflict-*".
    nonisolated static func conflictGlob(forOriginalPath originalPath: String) -> String {
        // Defensive: empty / root-equivalent inputs cannot have meaningful conflict copies.
        // Return the input unchanged so callers (e.g. group()) treat it as a singleton.
        let trimmed = originalPath.trimmingCharacters(in: .whitespaces)
        if trimmed.isEmpty || trimmed == "." || trimmed == "/" {
            return originalPath
        }
        let url = URL(fileURLWithPath: originalPath)
        let ext = url.pathExtension
        let stem = ext.isEmpty ? url.lastPathComponent : url.deletingPathExtension().lastPathComponent
        let parent = url.deletingLastPathComponent().relativePath
        let glob = "\(stem).sync-conflict-*"
        if parent.isEmpty || parent == "." {
            return glob
        }
        return "\(parent)/\(glob)"
    }

    /// Perform the full "Always skip on this iPhone" action atomically:
    ///   1. Add both the original-path pattern and its conflict-copies glob to `.stignore`.
    ///   2. Remove every existing sync-conflict copy of the original file from disk.
    ///   3. Trigger a folder rescan so Syncthing's in-memory index reflects the changes.
    ///   4. Refresh the iOS-side conflict cache so the resolved conflict disappears.
    /// Returns:
    ///   - `error`: a user-facing error if ANY step failed (`.stignore` write,
    ///     conflict-copy cleanup, or rescan). The `.stignore` write may have
    ///     succeeded even when a later step reported an error — check
    ///     `removedConflicts` to see how many copies were actually deleted
    ///     before the failure.
    ///   - `removedConflicts`: the number of on-disk conflict-copy files that were deleted.
    @discardableResult
    func skipFileAndCleanupConflicts(folderID: String, originalPath: String) -> (error: SyncUserError?, removedConflicts: Int) {
        let glob = Self.conflictGlob(forOriginalPath: originalPath)

        guard var current = readIgnorePatternsOrNil(folderID: folderID) else {
            return (unreadableFiltersError(), 0)
        }
        if !current.contains(originalPath) {
            current.append(originalPath)
        }
        if !current.contains(glob) {
            current.append(glob)
        }
        if let err = setIgnorePatterns(folderID: folderID, patterns: current) {
            return (err, 0)
        }

        let cleanup = SyncBridgeService.removeConflictFilesForOriginal(
            folderID: folderID,
            originalPath: originalPath
        )
        if let cleanupError = cleanup.error {
            // .stignore write succeeded but on-disk cleanup didn't.
            // Surface the failure so the user knows the leftover copies
            // haven't been removed and the home-screen Sync Issues entry
            // may still flag the file.
            refreshConflicts()
            return (SyncUserError.from(rawMessage: cleanupError), cleanup.removed)
        }

        if let rescanError = SyncBridgeService.rescanFolder(folderID: folderID) {
            refreshConflicts()
            return (SyncUserError.from(rawMessage: rescanError), cleanup.removed)
        }
        refreshConflicts()

        return (nil, cleanup.removed)
    }

    // MARK: - Test hooks

    #if DEBUG
    func _testApplyDeviceList(_ newDevices: [DeviceInfo]) {
        applyDeviceList(newDevices)
    }

    func _testSetFolders(_ newFolders: [FolderInfo]) {
        folders = newFolders
    }

    func _testSetEngineStartedAt(_ date: Date?) {
        engineStartedAt = date
    }

    func _testEngineDeathAutoRestartConsumed() -> Bool {
        engineDeathAutoRestartConsumed
    }

    func _testMarkEngineDeathAutoRestartConsumed() {
        engineDeathAutoRestartConsumed = true
    }

    func _testSetConflictFiles(_ newConflicts: [String: [ConflictInfo]]) {
        conflictFiles = newConflicts
    }
    #endif
}
