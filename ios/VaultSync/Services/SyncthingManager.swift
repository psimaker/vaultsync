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

    /// Read back the last persisted snapshot (app side). The background
    /// completion write carries values forward from it that a finished run
    /// cannot re-read honestly (#76 follow-up): the bridge reports an empty
    /// folder list once stopped, and a failed run has no sync completion of
    /// its own to stamp.
    static func read() -> Snapshot? {
        guard let defaults = UserDefaults(suiteName: appGroupSuiteName),
              let json = defaults.string(forKey: snapshotDefaultsKey),
              let data = json.data(using: .utf8) else {
            return nil
        }
        return try? JSONDecoder().decode(Snapshot.self, from: data)
    }

    /// Issue-severity floor persisted for the background completion write
    /// (#76). The foreground manager derives it from the same issue list the
    /// header and widget render (decision 012); `BackgroundSyncService
    /// .completeSync` — a static context with no manager and usually a
    /// stopped bridge — reads it so a successful background run cannot
    /// overwrite an honest attention snapshot with a green idle. Only issue
    /// kinds a background sync cannot resolve on its own are recorded (see
    /// `SyncthingManager.durableIssueFloor`).
    enum IssueFloor: String, Sendable {
        case none
        case warning
        case critical

        /// Decode the persisted floor. Absent reads as `.none` (fresh install
        /// or first run after update — matches the pre-#76 behavior until the
        /// foreground writes one); an unknown value maps to `.warning`, never
        /// silently to `.none` — same doctrine as `SyncStatus.fromWire`.
        static func decode(_ raw: String?) -> IssueFloor {
            guard let raw else { return .none }
            return IssueFloor(rawValue: raw) ?? .warning
        }
    }

    static let issueFloorDefaultsKey = "vaultsync.widget.issue-floor"

    static func writeIssueFloor(_ floor: IssueFloor) {
        UserDefaults(suiteName: appGroupSuiteName)?.set(floor.rawValue, forKey: issueFloorDefaultsKey)
    }

    static func readIssueFloor() -> IssueFloor {
        IssueFloor.decode(
            UserDefaults(suiteName: appGroupSuiteName)?.string(forKey: issueFloorDefaultsKey)
        )
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
    private(set) var conflictInspectionUnavailableFolderIDs: Set<String> = []
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

    /// Retired preference key from the former automatic last-writer-wins
    /// resolver. Keep the key stable and leave existing values untouched so an
    /// upgrade never rewrites user preferences as part of this safety change.
    nonisolated static let autoResolveStateConflictsKey = "auto-resolve-state-conflicts-v1"

    /// Safety tombstone for the retired automatic resolver (#145). The injected
    /// defaults are deliberately ignored: a missing value, `false`, and a
    /// persisted legacy `true` all leave automatic mutation disabled without
    /// deleting or resetting the stored value.
    nonisolated static func isAutoResolveStateConflictsEnabled(
        defaults _: UserDefaults = .standard
    ) -> Bool {
        false
    }

    private var activeWidgetSyncStart: Date?
    private var activeWidgetSyncFilesSynced = 0
    private var lastWidgetSyncCompletionTime: Date?
    private var lastWidgetSyncDuration: TimeInterval = 0
    private var lastWidgetSyncFilesSynced = 0
    private var lastWrittenWidgetSnapshot: WidgetSnapshotStore.Snapshot?
    private var lastWrittenIssueFloor: WidgetSnapshotStore.IssueFloor?

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
    }

    struct ConflictInspectionSnapshot: Sendable {
        let conflicts: [String: [ConflictInfo]]
        let unavailableFolderIDs: Set<String>
    }

    /// A verified empty JSON array may remove cached conflicts. Any missing,
    /// malformed, or explicitly unavailable response preserves the last
    /// reviewable copies for that active folder and records incomplete
    /// evidence instead of claiming that no conflicts exist (#150).
    nonisolated static func mergeConflictInspection(
        previous: [String: [ConflictInfo]],
        activeFolderIDs: [String],
        rawByFolder: [String: String]
    ) -> ConflictInspectionSnapshot {
        let activeIDs = Set(activeFolderIDs)
        var conflicts: [String: [ConflictInfo]] = [:]
        var unavailable: Set<String> = []

        for folderID in activeIDs.sorted() {
            guard let raw = rawByFolder[folderID],
                  let data = raw.data(using: .utf8),
                  let decoded = try? JSONDecoder().decode([ConflictInfo].self, from: data) else {
                if let retained = previous[folderID], !retained.isEmpty {
                    conflicts[folderID] = retained
                }
                unavailable.insert(folderID)
                continue
            }
            if !decoded.isEmpty {
                conflicts[folderID] = decoded
            }
        }
        return ConflictInspectionSnapshot(
            conflicts: conflicts,
            unavailableFolderIDs: unavailable
        )
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
            case conflictRetentionSafety
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

    nonisolated static func conflictSafetyState(
        for status: FolderStatusInfo?
    ) -> ConflictSafetyPolicy.State {
        guard let status else {
            return .unknown
        }
        return ConflictSafetyPolicy.classify(
            statusReadable: true,
            state: status.state,
            errorReason: status.errorReason,
            hasRawErrorDetail: status.errorMessage?.isEmpty == false || status.errorPath?.isEmpty == false
        )
    }

    /// Combines the immutable 2.0.2 folder-mode policy with live engine
    /// evidence for receive-capable and unknown modes. Known SendOnly folders
    /// retain their normal diagnostics; only fixed #150 reasons override that
    /// mode globally.
    nonisolated static func effectiveConflictSafetyState(
        folderType: String?,
        status: FolderStatusInfo?
    ) -> ConflictSafetyPolicy.State {
        let runtimeState = ConflictSafetyPolicy.runtimeState(forFolderType: folderType)
        if runtimeState == .clear {
            return ConflictSafetyPolicy.state(forEventReason: status?.errorReason) ?? .clear
        }
        return ConflictSafetyPolicy.aggregate([
            runtimeState,
            conflictSafetyState(for: status),
        ])
    }

    nonisolated static func conflictMutationBlockCode(
        folderType: String? = nil,
        cachedStatus: FolderStatusInfo?,
        liveStatusJSON: String
    ) -> String? {
        let cachedState = effectiveConflictSafetyState(
            folderType: folderType,
            status: cachedStatus
        )
        if let cachedCode = ConflictSafetyPolicy.actionErrorCode(for: cachedState) {
            return cachedCode
        }

        guard let data = liveStatusJSON.data(using: .utf8),
              let liveStatus = try? JSONDecoder().decode(SyncBridgeService.FolderStatusPayload.self, from: data) else {
            return ConflictSafetyPolicy.actionErrorCode(
                for: ConflictSafetyPolicy.runtimeState(forFolderType: folderType)
            )
        }
        let liveState = effectiveConflictSafetyState(
            folderType: folderType,
            status: FolderStatusInfo(payload: liveStatus)
        )
        return ConflictSafetyPolicy.actionErrorCode(for: liveState)
    }

    func conflictSafetyState(folderID: String) -> ConflictSafetyPolicy.State {
        let folderType = folders.first(where: { $0.id == folderID })?.type
        return Self.effectiveConflictSafetyState(
            folderType: folderType,
            status: folderStatuses[folderID]
        )
    }

    /// Folder mode and live engine evidence jointly authorize receive-side
    /// work. A missing status after a restart remains unknown for an unknown
    /// mode, and every receive-capable mode remains stopped even when the
    /// bridge reports a clear-shaped status. SendOnly keeps its normal status
    /// diagnostics unless the bridge supplies a fixed #150 reason.
    var conflictRetentionSafetyFolderIDs: [String] {
        folders.compactMap { folder in
            conflictSafetyState(folderID: folder.id) == .stopped ? folder.id : nil
        }.sorted()
    }

    var conflictSafetyUnknownFolderIDs: [String] {
        folders.compactMap { folder in
            conflictSafetyState(folderID: folder.id) == .unknown ? folder.id : nil
        }.sorted()
    }

    var conflictSafetyBlockedFolderIDs: [String] {
        Array(Set(conflictRetentionSafetyFolderIDs).union(conflictSafetyUnknownFolderIDs)).sorted()
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
                    remediation: L10n.tr("Keep the affected vaults paused and preserve every remaining copy. New share acceptance is unavailable in this version."),
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
                    remediation: L10n.tr("Keep the affected vaults paused and preserve every remaining copy. New share acceptance is unavailable in this version."),
                    severity: .critical,
                    count: nestedIDs.count,
                    folderID: nestedIDs.min(),
                    deviceID: nil
                )
            )
        }

        let conflictSafetyBlockedIDs = conflictSafetyBlockedFolderIDs
        let specificIntegrityErrorIDs = Set(conflictSafetyBlockedIDs.filter {
            recognizableProtectedIntegrityError(folderID: $0) != nil
        })
        for folderID in conflictSafetyBlockedIDs where !specificIntegrityErrorIDs.contains(folderID) {
            let state = conflictSafetyState(folderID: folderID)
            let safetyError = SyncUserError.conflictSafetyError(for: state)
            issues.append(
                SyncIssueItem(
                    kind: .conflictRetentionSafety,
                    title: safetyError.title,
                    message: safetyError.message,
                    remediation: safetyError.remediation,
                    severity: .critical,
                    count: 1,
                    folderID: folderID,
                    deviceID: nil
                )
            )
        }

        // Folders stuck on a stale/inaccessible path are surfaced by their own
        // guided "remove / reconnect" card, so exclude them here to avoid
        // double-listing them with the generic (and, for them, useless)
        // "rescan failed vaults" remediation.
        let unreachableIDs = Set(unreachableFolders.map(\.id))
        let erroredFolderIDs = folderIDsWithErrors.filter {
            !unreachableIDs.contains($0)
                && (!conflictSafetyBlockedIDs.contains($0) || specificIntegrityErrorIDs.contains($0))
        }
        if !erroredFolderIDs.isEmpty {
            let count = erroredFolderIDs.count
            issues.append(
                SyncIssueItem(
                    kind: .folderErrors,
                    title: count == 1 ? L10n.tr("1 Vault Has Sync Errors") : L10n.fmt("%d Vaults Have Sync Errors", count),
                    message: L10n.tr("At least one folder is currently in an error state."),
                    // A rescan cannot recreate a missing folder marker — when
                    // marker loss is the only error, pointing at it would
                    // misdirect the recovery (#65).
                    remediation: hasRescanableFolderErrors
                        ? L10n.tr("Rescan failed vaults, then verify folder access and permissions.")
                        : L10n.tr("Follow the recovery steps shown with the affected vault — rescanning cannot fix a vault folder that was moved or deleted."),
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

        // Conflict review remains read-only and available even when the folder
        // is safety-stopped or its status evidence is incomplete (#150).
        let reviewableConflictFiles = conflictFiles.filter { !$0.value.isEmpty }
        let reviewableConflictCount = reviewableConflictFiles.values.reduce(0) {
            $0 + Set($1.map(\.originalPath)).count
        }
        if reviewableConflictCount > 0 {
            let firstFolderID = reviewableConflictFiles
                .sorted(by: { $0.key < $1.key })
                .first?
                .key
            issues.append(
                SyncIssueItem(
                    kind: .conflicts,
                    title: reviewableConflictCount == 1 ? L10n.tr("1 Conflict Available for Review") : L10n.fmt("%d Conflicts Available for Review", reviewableConflictCount),
                    message: L10n.tr("A separate conflict copy was detected for a file."),
                    remediation: L10n.tr("Open conflicts to see which copies are still available. Recovery actions are unavailable."),
                    severity: .warning,
                    count: reviewableConflictCount,
                    folderID: firstFolderID,
                    deviceID: nil
                )
            )
        }

        if !conflictInspectionUnavailableFolderIDs.isEmpty {
            let count = conflictInspectionUnavailableFolderIDs.count
            issues.append(
                SyncIssueItem(
                    kind: .conflicts,
                    title: L10n.tr("Conflict Inspection Unavailable"),
                    message: L10n.tr("VaultSync cannot verify whether the conflict list is complete."),
                    remediation: L10n.tr("Open conflicts to review any previously visible copies. No recovery action is available."),
                    severity: .warning,
                    count: count,
                    folderID: conflictInspectionUnavailableFolderIDs.sorted().first,
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
        case .bridgeStartFailed, .noBookmarkAccess, .failed, .settledWithFolderError:
            severity = .critical
        case .noFoldersConfigured, .notIdleBeforeDeadline:
            severity = .warning
        case .synced, .alreadyIdle:
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
        logger.info("Starting Syncthing")

        BackgroundSyncService.lifecycleLock.withLock { $0.foregroundActive = true }

        let startError = await Task.detached(priority: .userInitiated) {
            SyncBridgeService.startSyncthing(configDir: configDir)
        }.value

        if let err = startError {
            BackgroundSyncService.lifecycleLock.withLock { $0.foregroundActive = false }
            logger.error("Failed to start Syncthing")
            error = err
            userError = SyncUserError.from(rawMessage: err, fallbackTitle: L10n.tr("Could Not Start Sync"))
            return
        }

        engineStartedAt = now()
        isRunning = true
        deviceID = SyncBridgeService.deviceID()
        error = nil
        userError = nil
        logger.info("Syncthing started")

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
        logger.info("Adopted running Syncthing engine started by a background handler")

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
        conflictInspectionUnavailableFolderIDs = []
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

    /// Retained facade for the existing add-folder API. The bridge creates a
    /// receive-capable folder, which is read-only in 2.0.2, so return before
    /// bridge, refresh, marker, scan, or persistence work (#150).
    func addFolder(id: String, label: String, path: String) -> String? {
        "vaultsync-conflict-retention-safety-stop"
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
        if let errorCode = conflictMutationBlockCode(folderID: id) {
            return errorCode
        }
        return SyncBridgeService.rescanFolder(folderID: id)
    }

    enum ForegroundRescanResult: Equatable, Sendable {
        case triggered
        case blocked(String)
        case failed(String)
    }

    nonisolated static func defaultForegroundRescanTargetFolderIDs(
        _ configuredFolders: [FolderInfo]
    ) -> [String] {
        configuredFolders.compactMap { folder in
            ConflictSafetyPolicy.runtimeState(forFolderType: folder.type) == .clear
                ? folder.id
                : nil
        }.sorted()
    }

    var foregroundRescanEligibleFolderIDs: [String] {
        Self.defaultForegroundRescanTargetFolderIDs(folders)
    }

    /// Runs a complete read-only safety pass before the first foreground
    /// rescan, then rechecks the exact folder at its mutation gate (#150).
    nonisolated static func performForegroundRescans(
        configuredFolders: [FolderInfo],
        targetFolderIDs: [String],
        statusJSON: (_ folderID: String) -> String,
        rescan: (_ folderID: String) -> String?
    ) -> ForegroundRescanResult {
        let configuredIDs = configuredFolders.map(\.id)
        guard !targetFolderIDs.isEmpty,
              Set(configuredIDs).count == configuredIDs.count,
              Set(targetFolderIDs).count == targetFolderIDs.count,
              configuredIDs.allSatisfy({ !$0.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }),
              targetFolderIDs.allSatisfy({ !$0.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }) else {
            return .blocked(ConflictSafetyPolicy.engineStopMarker)
        }
        let foldersByID = Dictionary(uniqueKeysWithValues: configuredFolders.map { ($0.id, $0) })
        guard targetFolderIDs.allSatisfy({ foldersByID[$0] != nil }) else {
            return .blocked(ConflictSafetyPolicy.engineStopMarker)
        }
        let orderedFolderIDs = targetFolderIDs.sorted()

        // Folder type is checked before the first status read or bridge call.
        // Receive-capable targets stop the entire requested operation, while a
        // receive sibling cannot suppress an explicitly send-only request.
        for folderID in orderedFolderIDs {
            let runtimeState = ConflictSafetyPolicy.runtimeState(
                forFolderType: foldersByID[folderID]?.type
            )
            if let code = ConflictSafetyPolicy.actionErrorCode(for: runtimeState) {
                return .blocked(code)
            }
        }

        func blockCode(folderID: String) -> String? {
            let encodedStatus = statusJSON(folderID)
            guard let data = encodedStatus.data(using: .utf8),
                  let status = try? JSONDecoder().decode(SyncBridgeService.FolderStatusPayload.self, from: data) else {
                // The configured type is the SendOnly authorization boundary.
                // Ordinary or temporarily unreadable status must not disable
                // that mode's established repair rescan; the bridge repeats
                // the hard-floor check at the mutation ABI.
                return nil
            }
            let state = ConflictSafetyPolicy.state(forEventReason: status.errorReason) ?? .clear
            return ConflictSafetyPolicy.actionErrorCode(for: state)
        }

        for folderID in orderedFolderIDs {
            if let code = blockCode(folderID: folderID) {
                return .blocked(code)
            }
        }

        for folderID in orderedFolderIDs {
            if let code = blockCode(folderID: folderID) {
                return .blocked(code)
            }
            if let error = rescan(folderID) {
                return .failed(error)
            }
        }
        return .triggered
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

    func triggerForegroundSync(folderIDs: [String]) {
        guard !isAnySyncing else {
            logger.info("Ignoring sync request because a sync is already in progress")
            return
        }

        Task {
            await performForegroundSyncRequest(folderIDs: folderIDs)
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

    private func conflictMutationBlockCode(folderID: String) -> String? {
        let folderType = folders.first(where: { $0.id == folderID })?.type
        return Self.conflictMutationBlockCode(
            folderType: folderType,
            cachedStatus: folderStatuses[folderID],
            liveStatusJSON: SyncBridgeService.getFolderStatusJSON(folderID: folderID)
        )
    }

    /// Retained Swift facade for the stable gomobile conflict ABI.
    ///
    /// Conflict recovery is intentionally unavailable in 2.0.2. Return the
    /// fixed path-free error before reading status or reaching an older
    /// framework that may still contain the retired mutating implementation.
    func resolveConflict(folderID: String, conflictFileName: String, keepConflict: Bool) -> String? {
        "vaultsync-conflict-recovery-unavailable"
    }

    /// Retained Swift facade for the stable gomobile conflict ABI. No name is
    /// derived and no bridge call is made while recovery is unavailable.
    func keepBothConflict(folderID: String, conflict: ConflictInfo) -> (error: String?, newPath: String?) {
        ("vaultsync-conflict-recovery-unavailable", nil)
    }

    // MARK: - Pending folder shares

    /// Retained facade for pending-share ABI compatibility. Every accepted
    /// offer is receive-capable, so 2.0.2 returns before bridge, folder-list,
    /// removed-state, sidecar, scan, or persistence work (#150).
    func acceptPendingFolder(folderID: String, label: String, path: String, allowNonEmpty: Bool) -> String? {
        "vaultsync-conflict-retention-safety-stop"
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
        conflictInspectionUnavailableFolderIDs = []
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
        // Folder statuses + conflicts need the current folder list.
        let currentFolders = folders
        let statusSnapshot = await Task.detached {
            var statuses: [String: FolderStatusInfo] = [:]
            var conflicts: [String: String] = [:]
            for folder in currentFolders {
                if let status = SyncBridgeService.getFolderStatus(folderID: folder.id) {
                    statuses[folder.id] = FolderStatusInfo(payload: status)
                }
                conflicts[folder.id] = SyncBridgeService.getConflictFilesJSON(folderID: folder.id)
            }
            return (statuses, conflicts)
        }.value

        let previousStatuses = folderStatuses
        let newStatuses = statusSnapshot.0
        let safetyStates = Dictionary(
            uniqueKeysWithValues: currentFolders.map {
                ($0.id, Self.effectiveConflictSafetyState(
                    folderType: $0.type,
                    status: newStatuses[$0.id]
                ))
            }
        )
        appendActivityEvents(
            bridgeEvents,
            folderNamesByID: folderNameByID,
            deviceNamesByID: deviceNameByID,
            folderSafetyStates: safetyStates
        )
        updateWidgetSyncMetrics(previousStatuses: previousStatuses, newStatuses: newStatuses)
        updateSyncHistory(
            newStatuses: newStatuses,
            activeFolderIDs: Set(currentFolders.map(\.id)),
            foldersWithConnectedPeer: Self.foldersWithConnectedPeer(
                folders: currentFolders,
                connectedDeviceIDs: Set(devices.filter(\.connected).map(\.deviceID))
            )
        )
        folderStatuses = newStatuses

        let conflictSnapshot = Self.mergeConflictInspection(
            previous: conflictFiles,
            activeFolderIDs: currentFolders.map(\.id),
            rawByFolder: statusSnapshot.1
        )
        conflictFiles = conflictSnapshot.conflicts
        conflictInspectionUnavailableFolderIDs = conflictSnapshot.unavailableFolderIDs
        if conflictSnapshot.unavailableFolderIDs.isEmpty {
            BackgroundSyncService.reconcileConflictNotificationBaseline(currentCount: unresolvedConflictCount)
        }
        writeWidgetSnapshotIfNeeded()
    }

    private func stopPolling() {
        pollTask?.cancel()
        pollTask = nil
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
        let rawByFolder = Dictionary(
            uniqueKeysWithValues: folders.map {
                ($0.id, SyncBridgeService.getConflictFilesJSON(folderID: $0.id))
            }
        )
        let snapshot = Self.mergeConflictInspection(
            previous: conflictFiles,
            activeFolderIDs: folders.map(\.id),
            rawByFolder: rawByFolder
        )
        conflictFiles = snapshot.conflicts
        conflictInspectionUnavailableFolderIDs = snapshot.unavailableFolderIDs
        if snapshot.unavailableFolderIDs.isEmpty {
            BackgroundSyncService.reconcileConflictNotificationBaseline(currentCount: unresolvedConflictCount)
        }
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
        deviceNamesByID: [String: String],
        folderSafetyStates: [String: ConflictSafetyPolicy.State]
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
                deviceNamesByID: deviceNamesByID,
                folderSafetyStates: folderSafetyStates
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
        deviceNamesByID: [String: String],
        folderSafetyStates: [String: ConflictSafetyPolicy.State] = [:]
    ) -> SyncEventItem? {
        let data = event.data ?? [:]
        let timestamp = parseBridgeDate(event.time) ?? Date()

        // The fixed reason wins before event type, raw state, folder/name/path,
        // or success handling. In particular, pathless ItemFinished safety
        // events must never disappear or become a successful file event.
        if let safetyState = ConflictSafetyPolicy.state(forEventReason: data["reason"]) {
            return conflictSafetyEvent(id: event.id, date: timestamp, state: safetyState)
        }
        if let folderID = data["folder"],
           let safetyState = folderSafetyStates[folderID],
           safetyState != .clear,
           event.type == "StateChanged" || event.type == "ItemFinished" || event.type == "FolderErrors" {
            return conflictSafetyEvent(id: event.id, date: timestamp, state: safetyState)
        }

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

    private func conflictSafetyEvent(
        id: Int,
        date: Date,
        state: ConflictSafetyPolicy.State
    ) -> SyncEventItem {
        let safetyError = SyncUserError.conflictSafetyError(for: state)
        return SyncEventItem(
            id: id,
            kind: .folderError,
            date: date,
            title: safetyError.title,
            detail: safetyError.message,
            folderID: nil,
            deviceID: nil,
            filePath: nil
        )
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
        SyncBridgeService.parseBridgeTimestamp(value)
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
        activeFolderIDs: Set<String>,
        foldersWithConnectedPeer: Set<String>
    ) {
        var didChange = false
        let folderTypesByID = Dictionary(uniqueKeysWithValues: folders.map { ($0.id, $0.type) })

        for (folderID, status) in newStatuses {
            let previousState = previousFolderStates[folderID]
            let hasConnectedPeer = foldersWithConnectedPeer.contains(folderID)
            let safetyState = Self.effectiveConflictSafetyState(
                folderType: folderTypesByID[folderID],
                status: status
            )

            if Self.didTransitionToSuccessfulIdle(
                previousState: previousState,
                status: status,
                hasConnectedPeer: hasConnectedPeer,
                safetyState: safetyState
            ) {
                if upsertLastSyncDate(folderID: folderID, date: Date()) {
                    didChange = true
                }
            }

            if Self.shouldTreatIdleStateAsSuccess(
                status: status,
                stateChangedAt: parseBridgeDate(status.stateChanged),
                existingDate: lastSyncTimeByFolder[folderID],
                hasConnectedPeer: hasConnectedPeer,
                safetyState: safetyState
            ),
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

    /// Pure core of the "a sync just completed" rule (#94): an active-to-idle
    /// transition with nothing left to fetch only counts as a sync while a
    /// remote peer sharing the folder is connected. Without that evidence a
    /// freshly accepted, still-empty folder satisfies needFiles == 0 after its
    /// first local scan even though no remote index ever arrived — and the app
    /// would claim success in exactly the moment a stall needs surfacing.
    nonisolated static func didTransitionToSuccessfulIdle(
        previousState: String?,
        status: FolderStatusInfo,
        hasConnectedPeer: Bool,
        safetyState: ConflictSafetyPolicy.State? = nil
    ) -> Bool {
        guard let previousState else { return false }
        let wasActive = previousState == "syncing" || previousState == "scanning"
        guard wasActive, status.state == "idle" else { return false }
        guard (safetyState ?? conflictSafetyState(for: status)) == .clear else { return false }
        guard hasConnectedPeer else { return false }
        return status.needFiles == 0 && status.errorMessage == nil
    }

    /// Pure core of the idle-backfill rule (#94): same peer-evidence bar as the
    /// transition detector. Skipping the backfill while no peer is connected
    /// only under-records — persisted history keeps the last real sync date,
    /// so the header degrades to the honest stale warning instead of lying.
    nonisolated static func shouldTreatIdleStateAsSuccess(
        status: FolderStatusInfo,
        stateChangedAt: Date?,
        existingDate: Date?,
        hasConnectedPeer: Bool,
        safetyState: ConflictSafetyPolicy.State? = nil
    ) -> Bool {
        guard status.state == "idle" else { return false }
        guard (safetyState ?? conflictSafetyState(for: status)) == .clear else { return false }
        guard status.needFiles == 0 else { return false }
        guard status.errorMessage == nil else { return false }
        guard hasConnectedPeer else { return false }
        guard let changedAt = stateChangedAt else { return false }
        if let existingDate, changedAt <= existingDate {
            return false
        }
        return true
    }

    /// Folders with at least one connected remote peer — the exchange evidence
    /// the sync-history detectors require (#94). Takes device IDs, not
    /// DeviceInfo (whose custom Decodable init suppresses the memberwise init
    /// tests would need). `FolderInfo.deviceIDs` already excludes this device
    /// (bridge folders.go strips stMyID).
    nonisolated static func foldersWithConnectedPeer(
        folders: [FolderInfo],
        connectedDeviceIDs: Set<String>
    ) -> Set<String> {
        Set(
            folders
                .filter { folder in folder.deviceIDs.contains { connectedDeviceIDs.contains($0) } }
                .map(\.id)
        )
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
        await performForegroundSyncRequest(folderIDs: folderID.map { [$0] })
    }

    private func performForegroundSyncRequest(folderIDs requestedFolderIDs: [String]?) async {
        if !isRunning {
            await start()
        }

        let availableFolders = await waitForFoldersForSyncRequest(maxWait: 3)

        let targetFolderIDs: [String]
        if let requestedFolderIDs {
            let normalized = requestedFolderIDs.map {
                $0.trimmingCharacters(in: .whitespacesAndNewlines)
            }
            let availableIDs = Set(availableFolders.map(\.id))
            guard !normalized.isEmpty,
                  normalized.allSatisfy({ !$0.isEmpty }),
                  Set(normalized).count == normalized.count,
                  normalized.allSatisfy(availableIDs.contains) else {
                logger.warning("Ignoring sync request with invalid folder selection")
                return
            }
            targetFolderIDs = normalized.sorted()
        } else {
            guard !availableFolders.isEmpty else {
                logger.info("Ignoring sync request because no folders are configured")
                return
            }
            targetFolderIDs = Self.defaultForegroundRescanTargetFolderIDs(availableFolders)
            guard !targetFolderIDs.isEmpty else {
                logger.info("Ignoring sync request because no SendOnly folders are mutable")
                return
            }
        }

        guard !isAnySyncing else {
            logger.info("Ignoring sync request because a sync started while preparing the request")
            return
        }

        let triggerResult = Self.performForegroundRescans(
            configuredFolders: availableFolders,
            targetFolderIDs: targetFolderIDs,
            statusJSON: { SyncBridgeService.getFolderStatusJSON(folderID: $0) },
            rescan: { SyncBridgeService.rescanFolder(folderID: $0) }
        )

        switch triggerResult {
        case .triggered:
            beginWidgetSyncSessionIfNeeded(startDate: Date())
            error = nil
            userError = nil
            writeWidgetSnapshotIfNeeded(statusOverride: .syncing)
        case let .blocked(code):
            error = code
            userError = SyncUserError.from(
                rawMessage: code,
                fallbackTitle: L10n.tr("Could Not Start Sync")
            )
            completeWidgetSyncSession(status: .error, completedAt: Date())
        case let .failed(triggerError):
            logger.error("Foreground sync trigger failed")
            error = triggerError
            userError = SyncUserError.from(
                rawMessage: triggerError,
                fallbackTitle: L10n.tr("Could Not Start Sync")
            )
            completeWidgetSyncSession(status: .error, completedAt: Date())
        }
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

    private func updateWidgetSyncMetrics(
        previousStatuses: [String: FolderStatusInfo],
        newStatuses: [String: FolderStatusInfo]
    ) {
        let wasSyncing = previousStatuses.values.contains { $0.state == "syncing" || $0.state == "scanning" }
        let isSyncingNow = newStatuses.values.contains { $0.state == "syncing" || $0.state == "scanning" }

        if isSyncingNow {
            beginWidgetSyncSessionIfNeeded(startDate: Date())
        } else if wasSyncing || activeWidgetSyncStart != nil {
            let safetyClear = folders.allSatisfy {
                Self.effectiveConflictSafetyState(
                    folderType: $0.type,
                    status: newStatuses[$0.id]
                ) == .clear
            }
            guard safetyClear else {
                abandonWidgetSyncSession()
                writeWidgetSnapshotIfNeeded(
                    statusOverride: currentWidgetSnapshotStatus(using: newStatuses)
                )
                return
            }
            // Close the session BEFORE deriving the tier: with the session
            // still open, the cascade reports .syncing and the completion
            // write persists a stale snapshot the poll-end write immediately
            // replaces — two writes and widget reloads per completion (#77).
            finalizeWidgetSyncSession(completedAt: Date())
            writeWidgetSnapshotIfNeeded(
                statusOverride: currentWidgetSnapshotStatus(using: newStatuses)
            )
        }
    }

    private func beginWidgetSyncSessionIfNeeded(startDate: Date) {
        guard activeWidgetSyncStart == nil else { return }
        activeWidgetSyncStart = startDate
        activeWidgetSyncFilesSynced = 0
    }

    /// Roll the just-ended session into the last-sync metrics and clear it.
    /// Must run before the completion tier is derived (#77).
    private func finalizeWidgetSyncSession(completedAt: Date) {
        let startedAt = activeWidgetSyncStart ?? completedAt
        lastWidgetSyncCompletionTime = completedAt
        lastWidgetSyncDuration = max(0, completedAt.timeIntervalSince(startedAt))
        lastWidgetSyncFilesSynced = activeWidgetSyncFilesSynced
        activeWidgetSyncStart = nil
        activeWidgetSyncFilesSynced = 0
    }

    private func abandonWidgetSyncSession() {
        activeWidgetSyncStart = nil
        activeWidgetSyncFilesSynced = 0
    }

    private func completeWidgetSyncSession(
        status: SyncStatus,
        completedAt _: Date
    ) {
        abandonWidgetSyncSession()
        writeWidgetSnapshotIfNeeded(statusOverride: status)
    }

    /// Status tier persisted for the widget. Derives from the SAME issue list
    /// the dashboard header and the "Sync Issues" section render (#73,
    /// decision 012) — before this, the widget only knew idle/syncing/error
    /// and kept a green check while a share was parked or a required peer was
    /// offline. Freshly polled statuses arrive via `using:` because this runs
    /// before `folderStatuses` is published, so a folder error in them is not
    /// yet visible to `unresolvedIssues`.
    private func currentWidgetSnapshotStatus(
        using statuses: [String: FolderStatusInfo]? = nil
    ) -> SyncStatus {
        let usesFreshStatuses = statuses != nil
        let statuses = statuses ?? folderStatuses

        // During a poll, `unresolvedIssues` still reflects the previously
        // published statuses. Replace only its safety tier with the supplied
        // fresh snapshot so an old unknown cannot mask a newly verified clear
        // status (and a new stop can never wait until the next write).
        var severities = unresolvedIssues.compactMap {
            usesFreshStatuses && $0.kind == .conflictRetentionSafety ? nil : $0.severity
        }
        if statuses.values.contains(where: { $0.state == "error" }) {
            severities.append(.critical)
        }
        if folders.contains(where: {
            Self.effectiveConflictSafetyState(
                folderType: $0.type,
                status: statuses[$0.id]
            ) != .clear
        }) {
            severities.append(.critical)
        }

        return SyncHeaderModel.deriveWidgetStatus(
            hasEngineError: error != nil || userError != nil,
            engineRunning: isRunning,
            issueSeverities: severities,
            hasUnreachableFolders: !unreachableFolders.isEmpty,
            isSyncing: activeWidgetSyncStart != nil ||
                statuses.values.contains(where: { $0.state == "syncing" || $0.state == "scanning" }),
            hasSyncFolders: !folders.isEmpty
        )
    }

    /// Max severity among issues that persist across a background sync — the
    /// floor `BackgroundSyncService.completeSync` folds into its widget write
    /// (#76). Excluded kinds are the ones a background run itself
    /// invalidates: `.staleSync` (a successful sync resolves staleness by
    /// definition) and `.backgroundSync` (the completion write knows the
    /// fresh outcome, which supersedes the persisted one) — including either
    /// would stick a false amber on the widget that only a foreground open
    /// could clear. Unreachable folders count as critical, mirroring the
    /// header cascade.
    nonisolated static func durableIssueFloor(
        issues: [(kind: SyncIssueItem.Kind, severity: SyncIssueSeverity)],
        hasUnreachableFolders: Bool
    ) -> WidgetSnapshotStore.IssueFloor {
        if hasUnreachableFolders { return .critical }
        var floor = WidgetSnapshotStore.IssueFloor.none
        for issue in issues where issue.kind != .staleSync && issue.kind != .backgroundSync {
            switch issue.severity {
            case .critical: return .critical
            case .warning: floor = .warning
            }
        }
        return floor
    }

    private func writeWidgetSnapshotIfNeeded(statusOverride: SyncStatus? = nil) {
        // Keep the persisted issue floor current on every write attempt, even
        // a deduped one — the floor can change (e.g. stale-sync warning joins
        // an existing attention state) without the snapshot changing.
        let issueFloor = Self.durableIssueFloor(
            issues: unresolvedIssues.map { ($0.kind, $0.severity) },
            hasUnreachableFolders: !unreachableFolders.isEmpty
        )
        if issueFloor != lastWrittenIssueFloor {
            lastWrittenIssueFloor = issueFloor
            WidgetSnapshotStore.writeIssueFloor(issueFloor)
        }

        let snapshot = WidgetSnapshotStore.Snapshot(
            lastSyncTime: WidgetSnapshotStore.iso8601String(from: lastWidgetSyncCompletionTime ?? lastSyncTime),
            lastSyncDuration: activeWidgetSyncStart == nil ? lastWidgetSyncDuration : 0,
            status: (statusOverride ?? currentWidgetSnapshotStatus()).wireValue,
            filesSynced: activeWidgetSyncStart == nil ? lastWidgetSyncFilesSynced : activeWidgetSyncFilesSynced,
            folderCount: folders.count
        )

        guard snapshot != lastWrittenWidgetSnapshot else { return }
        lastWrittenWidgetSnapshot = snapshot
        WidgetSnapshotStore.write(snapshot: snapshot)
    }

    func folderUserError(folderID: String) -> SyncUserError? {
        if let integrityError = recognizableProtectedIntegrityError(folderID: folderID) {
            return integrityError
        }
        let safetyState = conflictSafetyState(folderID: folderID)
        if safetyState != .clear {
            return SyncUserError.conflictSafetyError(for: safetyState)
        }
        guard let status = folderStatuses[folderID], status.state == "error" else { return nil }
        return SyncUserError.fromFolderStatus(
            reason: status.errorReason,
            message: status.errorMessage,
            path: status.errorPath
        )
    }

    private func recognizableProtectedIntegrityError(folderID: String) -> SyncUserError? {
        let folderType = folders.first(where: { $0.id == folderID })?.type
        guard ConflictSafetyPolicy.runtimeState(forFolderType: folderType) != .clear,
              let status = folderStatuses[folderID],
              status.state == "error" else {
            return nil
        }

        let mapped = SyncUserError.fromFolderStatus(
            reason: status.errorReason,
            message: status.errorMessage,
            path: nil
        )
        if mapped.category == .folderMarkerMissing {
            return mapped
        }

        let pathReasons: Set<String> = [
            "permission_denied",
            "folder_path_missing",
            "folder_path_invalid",
            "folder_path_unreadable",
        ]
        guard let reason = status.errorReason?.lowercased(), pathReasons.contains(reason) else {
            return nil
        }
        return SyncUserError(
            category: .fileAccess,
            title: L10n.tr("Vault Folder Needs Manual Recovery"),
            message: L10n.tr("VaultSync can no longer verify the configured vault folder."),
            remediation: L10n.tr("Keep this vault stopped and preserve every remaining copy. Restore the original folder at its original location; VaultSync will not move or re-point it automatically."),
            technicalDetails: nil
        )
    }

    /// True when at least one errored folder could plausibly be helped by a
    /// rescan. A folder whose sync marker is gone cannot — Syncthing refuses
    /// to scan without the marker, by design — so when marker loss is the
    /// only error, the rescan CTA is hidden and the doctrine-002 prose is the
    /// guidance (#65).
    var hasRescanableFolderErrors: Bool {
        folderIDsWithErrors.contains { id in
            guard let type = folders.first(where: { $0.id == id })?.type,
                  ConflictSafetyPolicy.runtimeState(forFolderType: type) == .clear else {
                return false
            }
            guard let category = folderUserError(folderID: id)?.category else { return false }
            return category != .folderMarkerMissing && category != .conflictRetentionSafetyStop
        }
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
            guard ConflictSafetyPolicy.runtimeState(forFolderType: folder.type) == .clear else {
                return nil
            }
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

    private func conflictMutationUserError(folderID: String) -> SyncUserError? {
        guard let code = conflictMutationBlockCode(folderID: folderID) else { return nil }
        return SyncUserError.from(rawMessage: code)
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
        if let safetyError = conflictMutationUserError(folderID: folderID) {
            return safetyError
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
        if let safetyError = conflictMutationUserError(folderID: folderID) {
            return safetyError
        }
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
        if let safetyError = conflictMutationUserError(folderID: folderID) {
            return safetyError
        }
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
        if let safetyError = conflictMutationUserError(folderID: folderID) {
            return safetyError
        }
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
        if let safetyError = conflictMutationUserError(folderID: folderID) {
            return safetyError
        }
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

    /// Retained facade for the former conflict-originated Always Skip flow.
    /// Recovery is read-only in 2.0.2, so this returns before reading or writing
    /// `.stignore`, removing conflict copies, requesting a scan, or refreshing
    /// state. Normal explicit Sync Filters remain available separately.
    @discardableResult
    func skipFileAndCleanupConflicts(folderID: String, originalPath: String) -> (error: SyncUserError?, removedConflicts: Int) {
        (
            SyncUserError.from(rawMessage: "vaultsync-conflict-recovery-unavailable"),
            0
        )
    }

    // MARK: - Test hooks

    #if DEBUG
    func _testApplyDeviceList(_ newDevices: [DeviceInfo]) {
        applyDeviceList(newDevices)
    }

    func _testSetFolders(_ newFolders: [FolderInfo]) {
        folders = newFolders
    }

    func _testSetPendingFolders(_ newPending: [PendingFolderInfo]) {
        pendingFolders = newPending
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
        conflictInspectionUnavailableFolderIDs = []
    }

    func _testSetConflictInspectionUnavailableFolderIDs(_ folderIDs: Set<String>) {
        conflictInspectionUnavailableFolderIDs = folderIDs
    }

    func _testSetFolderStatuses(_ newStatuses: [String: FolderStatusInfo]) {
        folderStatuses = newStatuses
    }

    func _testSetRunning(_ running: Bool) {
        isRunning = running
    }

    func _testMakeSyncEventItem(
        id: Int = 1,
        type: String,
        time: String = "2026-08-28T12:00:00Z",
        data: [String: String],
        folderNamesByID: [String: String] = [:],
        deviceNamesByID: [String: String] = [:],
        folderSafetyStates: [String: ConflictSafetyPolicy.State] = [:]
    ) -> SyncEventItem? {
        makeSyncEventItem(
            from: BridgeEventInfo(id: id, type: type, time: time, relevant: true, data: data),
            folderNamesByID: folderNamesByID,
            deviceNamesByID: deviceNamesByID,
            folderSafetyStates: folderSafetyStates
        )
    }

    func _testUpdateWidgetSyncMetrics(
        previousStatuses: [String: FolderStatusInfo],
        newStatuses: [String: FolderStatusInfo]
    ) {
        updateWidgetSyncMetrics(previousStatuses: previousStatuses, newStatuses: newStatuses)
    }

    func _testWriteWidgetSnapshot() {
        writeWidgetSnapshotIfNeeded()
    }

    func _testLastWrittenWidgetSnapshot() -> WidgetSnapshotStore.Snapshot? {
        lastWrittenWidgetSnapshot
    }

    func _testLastWrittenIssueFloor() -> WidgetSnapshotStore.IssueFloor? {
        lastWrittenIssueFloor
    }

    /// Init restores the last background outcome from `UserDefaults.standard`,
    /// which parallel suites share — tests that assert on the issue cascade
    /// clear it to stay hermetic.
    func _testSetLastBackgroundSyncOutcome(_ outcome: BackgroundSyncService.SyncOutcome?) {
        lastBackgroundSyncOutcome = outcome
    }
    #endif
}
