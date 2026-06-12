import Foundation
import Testing
@testable import VaultSync

/// Pins the Swift-side classification and counting that powers conflict
/// auto-resolution: which conflicts count as auto-resolvable `.obsidian`
/// state, and how the home-screen banner counts distinct files instead of
/// conflict copies. The on-disk resolution itself lives in the Go bridge
/// (AutoResolveStateConflicts) and is tested there.
@Suite("Conflict auto-resolve classification")
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

    // MARK: - isStateConflict

    @Test("Files inside .obsidian are state conflicts at any depth")
    func obsidianFilesAreState() {
        #expect(conflict(".obsidian/workspace.json").isStateConflict)
        #expect(conflict(".obsidian/plugins/dataview/data.json").isStateConflict)
        #expect(conflict("MyVault/.obsidian/app.json").isStateConflict)
        #expect(conflict("MyVault/.obsidian/plugins/calendar/data.json").isStateConflict)
    }

    @Test("Notes and lookalike paths are not state conflicts")
    func notesAreNotState() {
        #expect(!conflict("notes.md").isStateConflict)
        #expect(!conflict("Personal/diary.md").isStateConflict)
        // A file literally named ".obsidian.md" is a note, not state.
        #expect(!conflict(".obsidian.md").isStateConflict)
        // Only the exact directory name counts, not a prefix of it.
        #expect(!conflict("docs/.obsidian-guide/readme.md").isStateConflict)
    }

    // MARK: - containsStateConflict (poll-loop gate)

    @Test("JSON gate detects a state conflict among notes")
    func gateDetectsStateConflict() throws {
        let mixed = [conflict("a.md"), conflict("V/.obsidian/app.json")]
        let json = String(data: try JSONEncoder().encode(mixed), encoding: .utf8)!
        #expect(SyncthingManager.containsStateConflict(conflictsJSON: json))
    }

    @Test("JSON gate stays quiet for notes-only and invalid payloads")
    func gateQuietOtherwise() throws {
        let notes = [conflict("a.md"), conflict("V/b.md")]
        let json = String(data: try JSONEncoder().encode(notes), encoding: .utf8)!
        #expect(!SyncthingManager.containsStateConflict(conflictsJSON: json))
        #expect(!SyncthingManager.containsStateConflict(conflictsJSON: "[]"))
        #expect(!SyncthingManager.containsStateConflict(conflictsJSON: "not json"))
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
}
