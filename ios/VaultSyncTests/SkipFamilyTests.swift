import XCTest
@testable import VaultSync

final class SkipFamilyTests: XCTestCase {

    // MARK: - conflictGlob

    func test_conflictGlob_rootFileWithExtension() {
        let glob = SyncthingManager.conflictGlob(forOriginalPath: "notes.md")
        XCTAssertEqual(glob, "notes.sync-conflict-*")
    }

    func test_conflictGlob_nestedFile() {
        let glob = SyncthingManager.conflictGlob(forOriginalPath: "Personal/diary.md")
        XCTAssertEqual(glob, "Personal/diary.sync-conflict-*")
    }

    func test_conflictGlob_noExtension() {
        let glob = SyncthingManager.conflictGlob(forOriginalPath: "Makefile")
        XCTAssertEqual(glob, "Makefile.sync-conflict-*")
    }

    func test_conflictGlob_filenameWithDots() {
        let glob = SyncthingManager.conflictGlob(forOriginalPath: "archive.tar.gz")
        XCTAssertEqual(glob, "archive.tar.sync-conflict-*")
    }

    func test_conflictGlob_emptyAndRootInputs_returnUnchanged() {
        XCTAssertEqual(SyncthingManager.conflictGlob(forOriginalPath: ""), "")
        XCTAssertEqual(SyncthingManager.conflictGlob(forOriginalPath: "."), ".")
        XCTAssertEqual(SyncthingManager.conflictGlob(forOriginalPath: "/"), "/")
    }

    // MARK: - Pair detection

    func test_pairing_recognisesPair() {
        let lines = ["notes.md", "notes.sync-conflict-*"]
        let result = SkipFamilyGrouping.group(customLines: lines)
        XCTAssertEqual(result.count, 1)
        XCTAssertEqual(result[0].original, "notes.md")
        XCTAssertTrue(result[0].hasConflictGlob)
    }

    func test_pairing_singletonRemainsSingleton() {
        let lines = ["*.tmp"]
        let result = SkipFamilyGrouping.group(customLines: lines)
        XCTAssertEqual(result.count, 1)
        XCTAssertEqual(result[0].original, "*.tmp")
        XCTAssertFalse(result[0].hasConflictGlob)
    }

    func test_pairing_orphanConflictGlobRendersAsSingle() {
        // A conflict glob with no matching original is rendered as its own row.
        let lines = ["foo.sync-conflict-*"]
        let result = SkipFamilyGrouping.group(customLines: lines)
        XCTAssertEqual(result.count, 1)
        XCTAssertEqual(result[0].original, "foo.sync-conflict-*")
        XCTAssertFalse(result[0].hasConflictGlob)
    }

    func test_pairing_reverseOrder_doesNotDoubleEmit() {
        // Glob appears before its original — must still produce ONE paired entry,
        // not a singleton glob followed by a paired entry that re-uses the same line.
        let lines = ["notes.sync-conflict-*", "notes.md"]
        let result = SkipFamilyGrouping.group(customLines: lines)
        XCTAssertEqual(result.count, 1, "reverse-order input should still produce a single paired entry")
        XCTAssertEqual(result[0].original, "notes.md")
        XCTAssertTrue(result[0].hasConflictGlob)
        XCTAssertEqual(result[0].underlyingLines.sorted(), ["notes.md", "notes.sync-conflict-*"])
    }

    func test_pairing_mixedLines() {
        let lines = ["*.tmp", "notes.md", "notes.sync-conflict-*", "drafts/"]
        let result = SkipFamilyGrouping.group(customLines: lines).sorted { $0.original < $1.original }
        XCTAssertEqual(result.count, 3)

        XCTAssertEqual(result[0].original, "*.tmp")
        XCTAssertFalse(result[0].hasConflictGlob)

        XCTAssertEqual(result[1].original, "drafts/")
        XCTAssertFalse(result[1].hasConflictGlob)

        XCTAssertEqual(result[2].original, "notes.md")
        XCTAssertTrue(result[2].hasConflictGlob)
    }

    func test_pairing_nestedPair() {
        let lines = ["Personal/diary.md", "Personal/diary.sync-conflict-*"]
        let result = SkipFamilyGrouping.group(customLines: lines)
        XCTAssertEqual(result.count, 1)
        XCTAssertEqual(result[0].original, "Personal/diary.md")
        XCTAssertTrue(result[0].hasConflictGlob)
    }

    func test_underlyingLines_pair() {
        let entry = SkipFamilyEntry(original: "notes.md", hasConflictGlob: true)
        XCTAssertEqual(entry.underlyingLines, ["notes.md", "notes.sync-conflict-*"])
    }

    func test_underlyingLines_singleton() {
        let entry = SkipFamilyEntry(original: "*.tmp", hasConflictGlob: false)
        XCTAssertEqual(entry.underlyingLines, ["*.tmp"])
    }
}
