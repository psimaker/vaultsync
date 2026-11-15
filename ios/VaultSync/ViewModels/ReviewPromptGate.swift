import Foundation

/// Pure gate for the system's App Store rating prompt (#327, decision 067).
///
/// The app asks through `requestReview` and nothing else — no dialog of its
/// own in front of it, no second ask, no link out of the app. It asks only
/// once sync has completed on `requiredSyncDays` different calendar days,
/// only while nothing on the dashboard needs the user, and at most once per
/// app version. iOS adds its own limit on top (three showings a year) and
/// may show nothing at all; the app never learns whether it did, so "asked"
/// means "called", and the call is recorded before it is made.
enum ReviewPromptGate {
    /// "Several different days". After an ask the days start over, so the
    /// next version needs as many new ones.
    static let requiredSyncDays = 3

    /// What is persisted between launches (`ReviewPromptStore`).
    struct Record: Codable, Equatable, Sendable {
        /// Distinct calendar days ("yyyy-MM-dd" in the calendar's time zone)
        /// on which a sync completed, oldest first, at most
        /// `requiredSyncDays` of them.
        var syncDays: [String] = []
        /// The app version (`CFBundleShortVersionString`) the prompt was last
        /// requested for, if any.
        var askedVersion: String?
    }

    /// Everything that has to be calm at the moment of the ask. Each field is
    /// a blocker of its own, so a change to one surface cannot silently open
    /// or shut the gate through another.
    struct Inputs: Equatable, Sendable {
        var record: Record
        /// The running version; nil when the bundle does not name one.
        var appVersion: String?
        var engineRunning: Bool
        var hasEngineError: Bool
        /// The Obsidian folder is connected (setup is finished).
        var vaultAccessible: Bool
        var hasSyncFolders: Bool
        /// `SyncthingManager.unresolvedIssues` is non-empty (issues, pending
        /// shares that count, conflicts, disconnected peers, stale or failed
        /// background sync).
        var hasUnresolvedIssues: Bool
        /// Folders on a dead path or whose folder turned out to be a copy.
        var hasUnreachableFolders: Bool
        /// Any pending share, including one the user set aside: it still
        /// waits for a decision.
        var hasPendingShares: Bool
        var isSyncing: Bool
        /// Every vault row reads "Up to date" — none paused, in error,
        /// waiting for its first sync, holding conflicts or missing.
        var allVaultsUpToDate: Bool
        /// A sheet, alert, pushed screen or another tab is in front of the
        /// Sync tab, or the app is not active.
        var somethingInFront: Bool
        /// Another one-time ask (the Relay offer, the notification primer)
        /// is on the dashboard. One ask at a time.
        var otherAskVisible: Bool
    }

    /// The record with the calendar day of `syncCompletedAt` added. The day
    /// is that of the completion date itself — a date loaded from the sync
    /// history may be days old, and it counts for the day it happened on.
    static func recording(
        syncCompletedAt date: Date,
        into record: Record,
        calendar: Calendar
    ) -> Record {
        let day = dayKey(date, calendar: calendar)
        guard !record.syncDays.contains(day) else { return record }
        var updated = record
        updated.syncDays.append(day)
        updated.syncDays.sort()
        // Bounded: only the most recent days are ever needed.
        if updated.syncDays.count > requiredSyncDays {
            updated.syncDays.removeFirst(updated.syncDays.count - requiredSyncDays)
        }
        return updated
    }

    static func shouldRequest(_ inputs: Inputs) -> Bool {
        guard let version = inputs.appVersion, !version.isEmpty else { return false }
        guard inputs.record.askedVersion != version else { return false }
        guard inputs.record.syncDays.count >= requiredSyncDays else { return false }
        return inputs.engineRunning
            && !inputs.hasEngineError
            && inputs.vaultAccessible
            && inputs.hasSyncFolders
            && !inputs.hasUnresolvedIssues
            && !inputs.hasUnreachableFolders
            && !inputs.hasPendingShares
            && !inputs.isSyncing
            && inputs.allVaultsUpToDate
            && !inputs.somethingInFront
            && !inputs.otherAskVisible
    }

    /// The record to persist before the prompt is requested for `version`:
    /// the version is marked asked and the days start over.
    static func afterRequest(version: String, record: Record) -> Record {
        var updated = record
        updated.askedVersion = version
        updated.syncDays = []
        return updated
    }

    static func dayKey(_ date: Date, calendar: Calendar) -> String {
        let parts = calendar.dateComponents([.year, .month, .day], from: date)
        return String(format: "%04d-%02d-%02d", parts.year ?? 0, parts.month ?? 0, parts.day ?? 0)
    }
}

/// The persisted `ReviewPromptGate.Record`, one JSON value in UserDefaults.
/// An unreadable value reads as an empty record: the days are counted again
/// from none, so even then an ask needs `requiredSyncDays` new sync days.
struct ReviewPromptStore {
    private let defaults: UserDefaults
    private let storageKey: String

    init(defaults: UserDefaults = .standard, storageKey: String = "reviewPrompt.v1") {
        self.defaults = defaults
        self.storageKey = storageKey
    }

    func load() -> ReviewPromptGate.Record {
        guard let data = defaults.data(forKey: storageKey),
              let record = try? JSONDecoder().decode(ReviewPromptGate.Record.self, from: data) else {
            return ReviewPromptGate.Record()
        }
        return record
    }

    func save(_ record: ReviewPromptGate.Record) {
        guard let data = try? JSONEncoder().encode(record) else { return }
        defaults.set(data, forKey: storageKey)
    }
}
