import Testing
@testable import VaultSync

/// When one Syncthing folder syncs the whole Obsidian directory, the home screen
/// expands it into one row per vault and must attribute each conflict to the
/// right vault by its folder-relative path. These tests pin that boundary logic.
@Suite("Vault conflict attribution")
struct VaultConflictAttributionTests {

    private func conflict(_ originalPath: String) -> SyncthingManager.ConflictInfo {
        SyncthingManager.ConflictInfo(
            originalPath: originalPath,
            conflictPath: originalPath + ".sync-conflict",
            conflictDate: "20260531-120000",
            deviceShortID: "ABCDEFG"
        )
    }

    @Test("A nested file is attributed to its own vault")
    func nestedFileBelongsToItsVault() {
        let c = conflict("brain/notes/today.md")
        #expect(c.belongs(toVault: "brain"))
        #expect(!c.belongs(toVault: "openclaw"))
    }

    @Test("A vault-prefix is not a substring match")
    func prefixIsNotSubstring() {
        // "brain" must not swallow conflicts that live in "brainstorm".
        let c = conflict("brainstorm/index.md")
        #expect(!c.belongs(toVault: "brain"))
        #expect(c.belongs(toVault: "brainstorm"))
    }

    @Test("An exact vault-root path matches its vault")
    func exactRootMatches() {
        #expect(conflict("brain").belongs(toVault: "brain"))
    }

    @Test("A stray leading slash is tolerated")
    func leadingSlashTolerated() {
        #expect(conflict("/brain/notes/a.md").belongs(toVault: "brain"))
    }
}
