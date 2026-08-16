import Foundation
import Testing
@testable import VaultSync

/// Pins the retired automatic resolver's safety policy and keeps the conflict
/// count semantics independent of the number of copies for one file (#145).
@Suite("Automatic conflict mutation disabled (#145)")
struct ConflictAutoResolveTests {

    private func conflict(
        _ originalPath: String,
        copySuffix: String = "20260601-120000-AAA1111"
    ) -> SyncthingManager.ConflictInfo {
        SyncthingManager.ConflictInfo(
            originalPath: originalPath,
            conflictPath: originalPath + ".sync-conflict-" + copySuffix,
            conflictDate: "20260601-120000",
            deviceShortID: "AAA1111"
        )
    }

    // MARK: - Retired preference policy

    @Test("Missing legacy preference cannot enable automatic mutation (#145)")
    func issue145MissingLegacyPreferenceStaysDisabledAndAbsent() {
        let defaults = TestSupport.makeIsolatedDefaults(label: "issue-145-missing")
        let key = SyncthingManager.autoResolveStateConflictsKey

        #expect(defaults.object(forKey: key) == nil)
        #expect(!SyncthingManager.isAutoResolveStateConflictsEnabled(defaults: defaults))
        #expect(defaults.object(forKey: key) == nil)
    }

    @Test("Legacy false preference stays disabled and intact (#145)")
    func issue145LegacyFalseStaysDisabledAndIntact() {
        let defaults = TestSupport.makeIsolatedDefaults(label: "issue-145-false")
        let key = SyncthingManager.autoResolveStateConflictsKey
        defaults.set(false, forKey: key)

        #expect(!SyncthingManager.isAutoResolveStateConflictsEnabled(defaults: defaults))
        #expect((defaults.object(forKey: key) as? Bool) == false)
    }

    @Test("Legacy true preference cannot re-enable mutation and stays intact (#145)")
    func issue145LegacyTrueStaysDisabledAndIntact() {
        let defaults = TestSupport.makeIsolatedDefaults(label: "issue-145-true")
        let key = SyncthingManager.autoResolveStateConflictsKey
        defaults.set(true, forKey: key)

        #expect(!SyncthingManager.isAutoResolveStateConflictsEnabled(defaults: defaults))
        #expect((defaults.object(forKey: key) as? Bool) == true)
    }

    // MARK: - Automatic caller removal

    @Test("Foreground poll has no automatic resolver call (#145)")
    func issue145ForegroundSourceHasNoAutomaticResolverCall() throws {
        let source = try productSource("VaultSync/Services/SyncthingManager.swift")
        expectNoAutomaticResolverCall(in: source)
    }

    @Test("Background paths have no automatic resolver call (#145)")
    func issue145BackgroundSourceHasNoAutomaticResolverCall() throws {
        let source = try productSource("VaultSync/Services/BackgroundSyncService.swift")
        expectNoAutomaticResolverCall(in: source)
    }

    @Test("No product source can route around the retired callers (#145)")
    func issue145ProductSourcesHaveNoAutomaticResolverReference() throws {
        let productDirectory = productURL("VaultSync")
        guard let enumerator = FileManager.default.enumerator(
            at: productDirectory,
            includingPropertiesForKeys: [.isRegularFileKey]
        ) else {
            Issue.record("Could not enumerate VaultSync product sources")
            return
        }

        var auditedSourceCount = 0
        for case let sourceURL as URL in enumerator {
            guard sourceURL.pathExtension == "swift",
                  sourceURL.lastPathComponent != "SyncBridgeService.swift" else {
                continue
            }
            auditedSourceCount += 1
            let source = try String(contentsOf: sourceURL, encoding: .utf8)
            expectNoAutomaticResolverCall(in: source)
        }
        #expect(auditedSourceCount > 0)
    }

    // MARK: - Distinct-file conflict count

    @Test("Banner count is distinct files, not conflict copies")
    @MainActor
    func countsDistinctFiles() {
        let manager = SyncthingManager()
        manager._testSetConflictFiles([
            "folder1": [
                // One churn-prone file with three copies counts once.
                conflict("V/.obsidian/workspace.json", copySuffix: "20260601-120000-AAA1111"),
                conflict("V/.obsidian/workspace.json", copySuffix: "20260601-130000-BBB2222"),
                conflict("V/.obsidian/workspace.json", copySuffix: "20260601-140000-CCC3333"),
                conflict("V/diary.md"),
            ],
            "folder2": [
                conflict("notes.md"),
            ],
        ])
        #expect(manager.unresolvedConflictCount == 3)
    }

    @Test("Empty conflict map counts zero")
    @MainActor
    func emptyCountsZero() {
        let manager = SyncthingManager()
        manager._testSetConflictFiles([:])
        #expect(manager.unresolvedConflictCount == 0)
    }

    private func productSource(_ relativePath: String, filePath: StaticString = #filePath) throws -> String {
        try String(contentsOf: productURL(relativePath, filePath: filePath), encoding: .utf8)
    }

    private func productURL(_ relativePath: String, filePath: StaticString = #filePath) -> URL {
        let iosDirectory = URL(fileURLWithPath: "\(filePath)")
            .deletingLastPathComponent()
            .deletingLastPathComponent()
        return iosDirectory.appendingPathComponent(relativePath)
    }

    private func expectNoAutomaticResolverCall(in source: String) {
        let compact = source.filter { !$0.isWhitespace }
        #expect(!compact.contains("SyncBridgeService.autoResolveStateConflicts"))
        #expect(!compact.contains("BridgeAutoResolveStateConflicts"))
    }
}
