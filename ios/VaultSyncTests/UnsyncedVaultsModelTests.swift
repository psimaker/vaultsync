import Foundation
import Testing
@testable import VaultSync

@Suite("Unsynced vault rows — detected vaults without a sync folder (#79)")
struct UnsyncedVaultsModelTests {

    private func derive(
        detected: [String],
        folders: Set<String> = [],
        root: String? = "/obsidian"
    ) -> [String] {
        UnsyncedVaultsModel.derive(
            detectedVaults: detected,
            folderPathsCanonLower: folders,
            rootCanonLower: root
        )
    }

    // The #79 device state: two vaults on disk, fresh engine, zero folders —
    // the list must name both instead of rendering empty.
    @Test("No folders at all: every detected vault is unsynced")
    func noFoldersListsEverything() {
        #expect(derive(detected: ["Test", "brain"]) == ["Test", "brain"])
    }

    @Test("A whole-directory folder at the root covers every vault")
    func wholeDirectoryFolderCoversAll() {
        #expect(derive(detected: ["Test", "brain"], folders: ["/obsidian"]).isEmpty)
    }

    @Test("A per-vault folder covers exactly its vault")
    func perVaultFolderCoversItsVault() {
        #expect(derive(detected: ["Test", "brain"], folders: ["/obsidian/brain"]) == ["Test"])
    }

    @Test("Coverage is case-insensitive (case-folding APFS)")
    func coverageIsCaseInsensitive() {
        // Convention: folder paths arrive canonicalized + lowercased; vault
        // names keep their display case and are lowered for the comparison.
        #expect(derive(detected: ["Brain"], folders: ["/obsidian/brain"]).isEmpty)
    }

    @Test("No connected root: nothing is listed")
    func noRootListsNothing() {
        #expect(derive(detected: ["brain"], root: nil).isEmpty)
        #expect(derive(detected: ["brain"], root: "").isEmpty)
    }
}
