import Foundation
import Testing
@testable import VaultSync

@Suite("App Store rating prompt (#327)")
struct ReviewPromptGateTests {
    /// A fixed zone, so a day boundary in a test does not move with the host.
    private static let berlin: Calendar = {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: "Europe/Berlin")!
        return calendar
    }()

    private static func date(_ iso: String) -> Date {
        ISO8601DateFormatter().date(from: iso)!
    }

    private static func record(days: Int, asked: String? = nil) -> ReviewPromptGate.Record {
        ReviewPromptGate.Record(
            syncDays: (0..<days).map { String(format: "2026-11-%02d", $0 + 1) },
            askedVersion: asked
        )
    }

    /// Everything calm, three sync days, never asked: the one state that asks.
    private static func calm(_ record: ReviewPromptGate.Record = record(days: 3)) -> ReviewPromptGate.Inputs {
        ReviewPromptGate.Inputs(
            record: record,
            appVersion: "3.0.0",
            engineRunning: true,
            hasEngineError: false,
            vaultAccessible: true,
            hasSyncFolders: true,
            hasUnresolvedIssues: false,
            hasUnreachableFolders: false,
            hasPendingShares: false,
            isSyncing: false,
            allVaultsUpToDate: true,
            somethingInFront: false,
            otherAskVisible: false
        )
    }

    // MARK: - Recording sync days

    @Test("A completion counts for its own calendar day, once")
    func recordsDistinctDays() {
        var record = ReviewPromptGate.Record()
        record = ReviewPromptGate.recording(syncCompletedAt: Self.date("2026-11-10T08:00:00Z"), into: record, calendar: Self.berlin)
        record = ReviewPromptGate.recording(syncCompletedAt: Self.date("2026-11-10T20:00:00Z"), into: record, calendar: Self.berlin)
        #expect(record.syncDays == ["2026-11-10"])
        record = ReviewPromptGate.recording(syncCompletedAt: Self.date("2026-11-12T08:00:00Z"), into: record, calendar: Self.berlin)
        #expect(record.syncDays == ["2026-11-10", "2026-11-12"])
    }

    @Test("The day is taken in the calendar's time zone")
    func dayFollowsTimeZone() {
        // 23:30 UTC on the 10th is already the 11th in Berlin.
        let late = Self.date("2026-11-10T23:30:00Z")
        #expect(ReviewPromptGate.dayKey(late, calendar: Self.berlin) == "2026-11-11")
        var utc = Calendar(identifier: .gregorian)
        utc.timeZone = TimeZone(identifier: "UTC")!
        #expect(ReviewPromptGate.dayKey(late, calendar: utc) == "2026-11-10")
    }

    @Test("A date loaded from history counts for the day it happened, not today")
    func oldDateCountsForItsDay() {
        let record = ReviewPromptGate.recording(
            syncCompletedAt: Self.date("2026-10-01T12:00:00Z"),
            into: ReviewPromptGate.Record(),
            calendar: Self.berlin
        )
        #expect(record.syncDays == ["2026-10-01"])
    }

    @Test("The record keeps only the most recent days, in order")
    func recordIsBounded() {
        var record = ReviewPromptGate.Record()
        for day in [5, 1, 4, 2, 3] {
            record = ReviewPromptGate.recording(
                syncCompletedAt: Self.date(String(format: "2026-11-%02dT12:00:00Z", day)),
                into: record,
                calendar: Self.berlin
            )
        }
        #expect(record.syncDays.count == ReviewPromptGate.requiredSyncDays)
        #expect(record.syncDays == ["2026-11-03", "2026-11-04", "2026-11-05"])
    }

    // MARK: - When to ask

    @Test("Asks only once sync completed on three different days")
    func needsThreeDays() {
        #expect(ReviewPromptGate.requiredSyncDays == 3)
        #expect(!ReviewPromptGate.shouldRequest(Self.calm(Self.record(days: 0))))
        #expect(!ReviewPromptGate.shouldRequest(Self.calm(Self.record(days: 2))))
        #expect(ReviewPromptGate.shouldRequest(Self.calm(Self.record(days: 3))))
    }

    @Test("Never asks twice in one version, however many days follow")
    func oncePerVersion() {
        let asked = ReviewPromptGate.Record(
            syncDays: ["2026-11-01", "2026-11-02", "2026-11-03"],
            askedVersion: "3.0.0"
        )
        #expect(!ReviewPromptGate.shouldRequest(Self.calm(asked)))
        var next = Self.calm(asked)
        next.appVersion = "3.0.1"
        #expect(ReviewPromptGate.shouldRequest(next))
    }

    @Test("Without a version the app never asks")
    func needsVersion() {
        var inputs = Self.calm()
        inputs.appVersion = nil
        #expect(!ReviewPromptGate.shouldRequest(inputs))
        inputs.appVersion = ""
        #expect(!ReviewPromptGate.shouldRequest(inputs))
    }

    @Test("An ask marks the version and the days start over")
    func afterRequestResets() {
        let after = ReviewPromptGate.afterRequest(version: "3.0.0", record: Self.record(days: 3))
        #expect(after == ReviewPromptGate.Record(syncDays: [], askedVersion: "3.0.0"))
        // The next version needs three new days, not the old ones.
        var next = Self.calm(after)
        next.appVersion = "3.0.1"
        #expect(!ReviewPromptGate.shouldRequest(next))
    }

    @Test(
        "Never while anything needs attention, during setup or in front of something",
        arguments: [
            "engine stopped", "engine error", "setup not finished", "no vaults",
            "open issue", "unreachable folder", "pending share", "syncing",
            "a vault not up to date", "something in front", "another ask visible",
        ]
    )
    func everyBlockerBlocks(_ blocker: String) {
        var inputs = Self.calm()
        switch blocker {
        case "engine stopped": inputs.engineRunning = false
        case "engine error": inputs.hasEngineError = true
        case "setup not finished": inputs.vaultAccessible = false
        case "no vaults": inputs.hasSyncFolders = false
        case "open issue": inputs.hasUnresolvedIssues = true
        case "unreachable folder": inputs.hasUnreachableFolders = true
        case "pending share": inputs.hasPendingShares = true
        case "syncing": inputs.isSyncing = true
        case "a vault not up to date": inputs.allVaultsUpToDate = false
        case "something in front": inputs.somethingInFront = true
        case "another ask visible": inputs.otherAskVisible = true
        default: Issue.record("unknown blocker \(blocker)")
        }
        #expect(ReviewPromptGate.shouldRequest(Self.calm()))
        #expect(!ReviewPromptGate.shouldRequest(inputs))
    }

    // MARK: - Store

    @Test("The record survives a round trip; an unreadable value reads as empty")
    func storeRoundTrip() throws {
        let suite = "ReviewPromptGateTests-\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        let store = ReviewPromptStore(defaults: defaults)
        #expect(store.load() == ReviewPromptGate.Record())
        let record = ReviewPromptGate.Record(syncDays: ["2026-11-01"], askedVersion: "2.1.0")
        store.save(record)
        #expect(store.load() == record)
        defaults.set(Data("not json".utf8), forKey: "reviewPrompt.v1")
        #expect(store.load() == ReviewPromptGate.Record())
    }
}
