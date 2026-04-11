import Foundation
import Observation
import os

private let logger = Logger(subsystem: "eu.vaultsync.app", category: "syncthing")

/// Manages the embedded Syncthing instance lifecycle and state.
@Observable @MainActor
final class SyncthingManager {
    private(set) var isRunning = false
    private(set) var deviceID = ""
    private(set) var devices: [DeviceInfo] = []
    private(set) var folders: [FolderInfo] = []
    private(set) var folderStatuses: [String: FolderStatusInfo] = [:]
    private(set) var conflictFiles: [String: [ConflictInfo]] = [:]
    private(set) var pendingFolders: [PendingFolderInfo] = []
    private(set) var ignoredPendingFolderIDs: Set<String> = []
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
    private var previousFolderStates: [String: String] = [:]
    private var lastBridgeEventID = 0
    private var activityDeduplicationCache: [String: Date] = [:]
    private var nextSyntheticEventID = -1
    private var lastBackgroundOutcomeEventDate: Date?
    private var backgroundSyncObserver: NSObjectProtocol?
    private let syncHistoryStore: SyncHistoryStore
    private static let ignoredPendingFoldersDefaultsKey = "syncthing.ignoredPendingFolderIDs"
    private static let hasSeenPendingOfferDefaultsKey = "syncthing.hasSeenPendingFolderOffer"
    private static let staleSyncThreshold: TimeInterval = 12 * 60 * 60
    private static let maxSyncActivityItems = 120
    private static let maxFileEventsPerFolderPerPoll = 6

    /// Default .stignore patterns for Obsidian vaults to prevent sync conflicts
    /// on device-specific files.
    private static let defaultIgnorePatterns = [
        ".Trash",
        ".obsidian/workspace.json",
        ".obsidian/workspace-mobile.json",
    ]

    private var hasAppliedStartupIgnores = false

    private static let bridgeDateParser: ISO8601DateFormatter = {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime]
        return formatter
    }()

    init(syncHistoryStore: SyncHistoryStore = SyncHistoryStore()) {
        self.syncHistoryStore = syncHistoryStore

        ignoredPendingFolderIDs = Self.loadIgnoredPendingFolderIDs()
        hasSeenPendingFolderOffer = UserDefaults.standard.bool(forKey: Self.hasSeenPendingOfferDefaultsKey)

        let history = syncHistoryStore.load()
        lastSyncTime = history.globalLastSync
        lastSyncTimeByFolder = history.lastSyncByFolder

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

    var ignoredPendingFolders: [PendingFolderInfo] {
        pendingFolders.filter { ignoredPendingFolderIDs.contains($0.id) }
    }

    var staleSyncWarning: String? {
        guard !folders.isEmpty else { return nil }
        guard !isAnySyncing else { return nil }
        guard let lastSyncTime else {
            return "No successful sync has been recorded for your vaults yet."
        }

        let age = Date().timeIntervalSince(lastSyncTime)
        guard age > Self.staleSyncThreshold else { return nil }
        let staleHours = Int(age / 3600)
        if staleHours >= 24 {
            let staleDays = max(1, staleHours / 24)
            return "Last successful sync was more than \(staleDays) day\(staleDays == 1 ? "" : "s") ago."
        }
        return "Last successful sync was about \(max(1, staleHours)) hour\(staleHours == 1 ? "" : "s") ago."
    }

    var folderIDsWithErrors: [String] {
        folderStatuses
            .filter { $0.value.state == "error" }
            .map(\.key)
            .sorted()
    }

    var disconnectedRequiredDeviceIDs: [String] {
        let required = Set(folders.flatMap(\.deviceIDs))
        guard !required.isEmpty else { return [] }
        let disconnectedKnown = Set(
            devices
                .filter { required.contains($0.deviceID) && !$0.connected }
                .map(\.deviceID)
        )
        let unresolvedUnknown = required.subtracting(Set(devices.map(\.deviceID)))
        return Array(disconnectedKnown.union(unresolvedUnknown)).sorted()
    }

    var unresolvedConflictCount: Int {
        conflictFiles.values.reduce(0) { $0 + $1.count }
    }

    var unresolvedIssues: [SyncIssueItem] {
        var issues: [SyncIssueItem] = []

        if !folderIDsWithErrors.isEmpty {
            let count = folderIDsWithErrors.count
            issues.append(
                SyncIssueItem(
                    kind: .folderErrors,
                    title: count == 1 ? "1 Vault Has Sync Errors" : "\(count) Vaults Have Sync Errors",
                    message: "At least one folder is currently in an error state.",
                    remediation: "Rescan failed vaults, then verify folder access and permissions.",
                    severity: .critical,
                    count: count,
                    folderID: folderIDsWithErrors.first,
                    deviceID: nil
                )
            )
        }

        if !disconnectedRequiredDeviceIDs.isEmpty {
            let count = disconnectedRequiredDeviceIDs.count
            issues.append(
                SyncIssueItem(
                    kind: .disconnectedPeers,
                    title: count == 1 ? "1 Required Device Is Disconnected" : "\(count) Required Devices Are Disconnected",
                    message: "Some shared peers are offline or unreachable right now.",
                    remediation: "Reconnect devices or add missing peers to restore continuous sync.",
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
                    title: pendingCount == 1 ? "1 Pending Share Needs Attention" : "\(pendingCount) Pending Shares Need Attention",
                    message: "Pending shares are waiting to be accepted before sync can start.",
                    remediation: "Accept a share to activate syncing for that vault.",
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
                    title: unresolvedConflictCount == 1 ? "1 Conflict Needs Resolution" : "\(unresolvedConflictCount) Conflicts Need Resolution",
                    message: "Conflicts mean multiple versions exist and need a manual decision.",
                    remediation: "Open conflicts and choose which version to keep.",
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
                    title: "Sync Activity Looks Stale",
                    message: staleSyncWarning,
                    remediation: "Trigger a vault rescan to refresh sync state.",
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
        case .synced, .alreadyIdle:
            return nil
        }

        let trigger = outcome.triggerReason.replacingOccurrences(of: "-", with: " ").capitalized
        let detail = outcome.detail ?? outcome.result.issueMessage

        return SyncIssueItem(
            kind: .backgroundSync,
            title: outcome.result.issueTitle,
            message: "\(detail) (Trigger: \(trigger))",
            remediation: outcome.result.remediation,
            severity: severity,
            count: 1,
            folderID: nil,
            deviceID: nil
        )
    }

    /// Start Syncthing using the app's Documents directory.
    func start() {
        guard !isRunning else { return }

        let configDir = Self.configDirectory()
        logger.info("Starting Syncthing with configDir: \(configDir)")

        BackgroundSyncService.lifecycleLock.withLock { $0.foregroundActive = true }

        if let err = SyncBridgeService.startSyncthing(configDir: configDir) {
            BackgroundSyncService.lifecycleLock.withLock { $0.foregroundActive = false }
            logger.error("Failed to start Syncthing: \(err)")
            error = err
            userError = SyncUserError.from(rawMessage: err, fallbackTitle: "Could Not Start Sync")
            return
        }

        isRunning = true
        deviceID = SyncBridgeService.deviceID()
        error = nil
        userError = nil
        logger.info("Syncthing started. Device ID: \(self.deviceID)")

        startPolling()
    }

    /// Stop the running Syncthing instance.
    func stop() {
        stopPolling()
        SyncBridgeService.stopSyncthing()
        BackgroundSyncService.lifecycleLock.withLock { $0.foregroundActive = false }
        isRunning = false
        deviceID = ""
        devices = []
        folders = []
        folderStatuses = [:]
        conflictFiles = [:]
        pendingFolders = []
        lastBridgeEventID = 0
        activityDeduplicationCache = [:]
        nextSyntheticEventID = -1
        error = nil
        userError = nil
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
            Task {
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
    func acceptPendingFolder(folderID: String, label: String, path: String) -> String? {
        let result = SyncBridgeService.acceptPendingFolder(folderID: folderID, label: label, path: path)
        if result == nil {
            refreshFolders()
            refreshPendingFolders()
            // Trigger a rescan after a short delay to kick-start initial sync.
            // The delay gives Syncthing time to initialize the folder model
            // before we request the scan.
            let id = folderID
            rescanTask?.cancel()
            rescanTask = Task {
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
        folders = []
        folderStatuses = [:]
        conflictFiles = [:]
        pendingFolders = []
        lastBridgeEventID = 0
        activityDeduplicationCache = [:]
        nextSyntheticEventID = -1
        error = nil
        userError = nil
    }

    // MARK: - Default ignore patterns

    /// Apply default .stignore patterns for an Obsidian vault folder.
    /// Reads existing patterns and merges in any missing defaults.
    private static func applyDefaultIgnoresIfNeeded(folderID: String) {
        let currentJSON = SyncBridgeService.getFolderIgnores(folderID: folderID)

        var currentPatterns: [String] = []
        if let data = currentJSON.data(using: .utf8),
           let decoded = try? JSONDecoder().decode([String].self, from: data) {
            currentPatterns = decoded
        }

        let missing = defaultIgnorePatterns.filter { pattern in
            !currentPatterns.contains(pattern)
        }
        guard !missing.isEmpty else { return }

        let updated = currentPatterns + missing
        guard let updatedData = try? JSONEncoder().encode(updated),
              let updatedJSON = String(data: updatedData, encoding: .utf8) else { return }

        let result = SyncBridgeService.setFolderIgnores(folderID: folderID, ignoresJSON: updatedJSON)
        if let error = result {
            logger.warning("Failed to set default ignores for \(folderID): \(error)")
        } else {
            logger.info("Applied default ignore patterns for folder \(folderID)")
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
        let snapshot = await Task.detached {
            let devicesJSON = SyncBridgeService.getDevicesJSON()
            let foldersJSON = SyncBridgeService.getFoldersJSON()
            let pendingJSON = SyncBridgeService.getPendingFoldersJSON()
            let eventsJSON = SyncBridgeService.getEventsSince(lastID: currentEventCursor)
            return (devicesJSON, foldersJSON, pendingJSON, eventsJSON)
        }.value

        // Decode on main — lightweight after the bridge calls are done.
        if let data = snapshot.0.data(using: .utf8),
           let decoded = try? JSONDecoder().decode([DeviceInfo].self, from: data) {
            devices = decoded
        }
        if let data = snapshot.1.data(using: .utf8),
           let decoded = try? JSONDecoder().decode([FolderInfo].self, from: data) {
            folders = decoded
        }

        // One-time check: apply default .stignore patterns for existing folders.
        if !hasAppliedStartupIgnores && !folders.isEmpty {
            hasAppliedStartupIgnores = true
            let folderIDs = folders.map(\.id)
            Task {
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
            for folder in currentFolders {
                if let status = SyncBridgeService.getFolderStatus(folderID: folder.id) {
                    statuses[folder.id] = FolderStatusInfo(payload: status)
                }
                let cJSON = SyncBridgeService.getConflictFilesJSON(folderID: folder.id)
                if let d = cJSON.data(using: .utf8) {
                    conflicts[folder.id] = d
                }
            }
            return (statuses, conflicts)
        }.value

        let newStatuses = statusSnapshot.0
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

        let trigger = outcome.triggerReason.replacingOccurrences(of: "-", with: " ").capitalized
        let title = "\(outcome.result.issueTitle) (\(trigger))"
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

    private func refreshDevices() {
        let json = SyncBridgeService.getDevicesJSON()
        guard let data = json.data(using: .utf8),
              let decoded = try? JSONDecoder().decode([DeviceInfo].self, from: data) else {
            return
        }
        devices = decoded
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
                    continue
                }
                emittedFileEventsByFolder[bucket, default: 0] += 1
            }

            if isDuplicateActivity(item) {
                continue
            }

            nextItems.append(item)
        }

        if !suppressedFileEventsByFolder.isEmpty {
            for (folderID, count) in suppressedFileEventsByFolder.sorted(by: { $0.key < $1.key }) where count > 0 {
                let folderName = displayFolderName(folderID, folderNamesByID: folderNamesByID)
                let summaryTitle = count == 1
                    ? "1 additional file synced in \(folderName)"
                    : "\(count) additional files synced in \(folderName)"
                let summary = SyncEventItem(
                    id: nextSyntheticID(),
                    kind: .summary,
                    date: now,
                    title: summaryTitle,
                    detail: "Timeline updates were rate-limited to keep activity readable.",
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
                    title: "Scanning started in \(folderName)",
                    detail: "Syncthing is scanning local changes.",
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
                    title: "Scanning completed in \(folderName)",
                    detail: "The folder scan finished successfully.",
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
                    title: "Sync started in \(folderName)",
                    detail: "Files are being synchronized with peers.",
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
                    title: "Sync completed in \(folderName)",
                    detail: "Folder reached idle state after syncing.",
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
                    title: "Sync error in \(folderName)",
                    detail: data["error"] ?? "Folder entered an error state.",
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
                    title: "Failed to sync file in \(folderName)",
                    detail: "\(itemPath): \(errorMessage)",
                    folderID: folderID,
                    deviceID: nil,
                    filePath: itemPath
                )
            }
            return SyncEventItem(
                id: event.id,
                kind: .fileSynced,
                date: timestamp,
                title: "File synced in \(folderName)",
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
                title: "\(deviceName) connected",
                detail: data["addr"] ?? "Peer connection is active.",
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
                title: "\(deviceName) disconnected",
                detail: data["error"] ?? "Connection to peer was closed.",
                folderID: nil,
                deviceID: deviceID,
                filePath: nil
            )

        case "FolderErrors":
            let folderID = data["folder"]
            let folderName = displayFolderName(folderID, folderNamesByID: folderNamesByID)
            let message = data["message"] ?? "Folder reported an error."
            let detail: String
            if let path = data["path"], !path.isEmpty {
                detail = "\(path): \(message)"
            } else {
                detail = message
            }

            return SyncEventItem(
                id: event.id,
                kind: .folderError,
                date: timestamp,
                title: "Folder error in \(folderName)",
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
        guard let folderID, !folderID.isEmpty else { return "Unknown Folder" }
        return folderNamesByID[folderID] ?? folderID
    }

    private func displayDeviceName(
        _ deviceID: String?,
        deviceNamesByID: [String: String]
    ) -> String {
        guard let deviceID, !deviceID.isEmpty else { return "Unknown Device" }
        return deviceNamesByID[deviceID] ?? shortDeviceID(deviceID)
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

    func folderUserError(folderID: String) -> SyncUserError? {
        guard let status = folderStatuses[folderID], status.state == "error" else { return nil }
        return SyncUserError.fromFolderStatus(
            reason: status.errorReason,
            message: status.errorMessage,
            path: status.errorPath
        )
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

    private static func loadIgnoredPendingFolderIDs() -> Set<String> {
        guard let values = UserDefaults.standard.array(forKey: ignoredPendingFoldersDefaultsKey) as? [String] else {
            return []
        }
        return Set(values)
    }
}
