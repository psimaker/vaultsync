import Foundation
import Testing
@testable import VaultSync

@Suite("VaultManager.resolveSharePath — collision-safe vault mapping (#45)")
struct VaultManagerSharePathTests {

    /// Production passes `FolderPathReconciler.canonical`; tests inject identity
    /// so the collision logic is exercised without touching the filesystem. The
    /// function lowercases internally, so occupied entries are supplied lowercased.
    private let identity: (String) -> String = { $0 }

    private func occupied(_ paths: String...) -> Set<String> {
        Set(paths.map { $0.lowercased() })
    }

    private func resolve(
        root: String = "/Obsidian",
        name: String,
        occupied: Set<String> = []
    ) -> String? {
        VaultManager.resolveSharePath(
            rawRoot: root,
            folderName: name,
            occupiedCanonLower: occupied,
            canonicalize: identity
        )
    }

    @Test("#270: the first vault on a fresh install gets its own subdirectory, never the root")
    func freshSingleVaultGetsSubfolder() {
        #expect(resolve(name: "MyVault") == "/Obsidian/MyVault")
    }

    @Test("Multi-vault root: a distinct vault gets its own subdirectory")
    func distinctVaultGetsSubfolder() {
        #expect(resolve(name: "Personal") == "/Obsidian/Personal")
    }

    @Test("#270: a vault named like the root gets its own subdirectory, never the root")
    func vaultNamedLikeTheRootGetsSubfolder() {
        #expect(resolve(name: "Obsidian") == "/Obsidian/Obsidian")
        // Obsidian's folder is `Documents` on disk — the name a label has to
        // match to have collapsed into it.
        let obsidiansFolder = "/var/mobile/Containers/Data/Application/0A1B2C3D-0000-4000-8000-000000000000/Documents"
        #expect(resolve(root: obsidiansFolder, name: "Documents") == obsidiansFolder + "/Documents")
        #expect(resolve(root: obsidiansFolder, name: "documents") == obsidiansFolder + "/documents")
    }

    @Test("#45 follow-up: a vault-as-root setup refuses a second share instead of nesting it")
    func secondVaultIsRefusedWhenRootIsOccupiedVault() {
        // The root already holds the first vault, synced into the root itself
        // by an earlier version. The second share must NOT get a subdirectory:
        // that subdirectory would sit inside the first vault's synced tree, so
        // the outer share syncs the inner vault's files to its own peers.
        // There is no safe location — refuse.
        let result = resolve(
            name: "Second",
            occupied: occupied("/Obsidian")
        )
        #expect(result == nil)
    }

    @Test("A vault named like the root is refused when the root is another folder's directory")
    func vaultNamedLikeTheRootRefusedWhenRootTaken() {
        // The root is occupied by a whole-directory share, so any subdirectory
        // (including `Obsidian/Obsidian`) would nest inside its synced tree.
        let result = resolve(
            name: "Obsidian",
            occupied: occupied("/Obsidian")
        )
        #expect(result == nil)
    }

    @Test("A root that lies inside an occupied directory is refused")
    func rootInsideOccupiedDirectoryIsRefused() {
        let result = resolve(
            root: "/Obsidian/Workshops",
            name: "Life",
            occupied: occupied("/Obsidian")
        )
        #expect(result == nil)
    }

    @Test("Same-name vaults are disambiguated, never merged")
    func sameNameVaultsAreDisambiguated() {
        #expect(resolve(name: "Notes", occupied: occupied("/Obsidian/Notes")) == "/Obsidian/Notes (2)")
        #expect(
            resolve(name: "Notes", occupied: occupied("/Obsidian/Notes", "/Obsidian/Notes (2)"))
                == "/Obsidian/Notes (3)"
        )
    }

    @Test("Collision detection is case-insensitive (case-folding APFS)")
    func collisionIsCaseInsensitive() {
        // An existing folder lives at /Obsidian/VaultA; a share whose label
        // differs only in case must still be treated as a clash.
        let result = resolve(name: "VaultA", occupied: occupied("/obsidian/vaulta"))
        #expect(result == "/Obsidian/VaultA (2)")
    }

    @Test("Two distinct vaults under one root map to two distinct subdirectories")
    func twoDistinctVaultsNoCollision() {
        let a = resolve(name: "Work")
        #expect(a == "/Obsidian/Work")
        // Once A is configured, B sees A as occupied and still gets its own dir.
        let b = resolve(name: "Personal", occupied: occupied(a ?? ""))
        #expect(b == "/Obsidian/Personal")
    }

    @Test("#270: whatever the label and whatever already syncs, the target is a direct child of the root")
    func targetIsAlwaysADirectChildOfTheRoot() {
        let root = "/Obsidian"
        let occupiedSets: [Set<String>] = [
            [],
            occupied("/Obsidian/Notes"),
            occupied("/Obsidian/Obsidian", "/Obsidian/Obsidian (2)"),
            occupied("/Elsewhere/Vault")
        ]
        for name in ["Obsidian", "obsidian", "OBSIDIAN", "Notes", "Documents", "A", "Notes (2)"] {
            for taken in occupiedSets {
                guard let target = resolve(root: root, name: name, occupied: taken) else {
                    Issue.record("\(name) found no place under a root nothing overlaps")
                    continue
                }
                #expect(target != root)
                #expect((target as NSString).deletingLastPathComponent == root)
            }
        }
    }

    @Test("Sequential accept on a container root maps every vault to its own sibling")
    func sequentialAcceptOnContainerIsCollisionFree() {
        // The intended setup: the root is the Obsidian container, not a vault.
        let first = resolve(name: "Workshops")
        #expect(first == "/Obsidian/Workshops")
        let second = resolve(name: "Life", occupied: occupied(first ?? ""))
        #expect(second == "/Obsidian/Life")
    }

    @Test("A candidate that would contain an occupied directory is skipped, not merged")
    func candidateContainingOccupiedDirIsSkipped() {
        // A folder already syncs a directory deeper down (legacy state). The
        // share may not claim its ancestor — that would sync the existing
        // folder's files as its own content — so it is disambiguated instead.
        let result = resolve(
            name: "Notes",
            occupied: occupied("/Obsidian/Notes/Attachments")
        )
        #expect(result == "/Obsidian/Notes (2)")
    }

    @Test("Overlap checks are boundary-aware: a name-prefix sibling is not a clash")
    func namePrefixSiblingIsNotAClash() {
        let result = resolve(
            name: "Workshops",
            occupied: occupied("/Obsidian/Work")
        )
        #expect(result == "/Obsidian/Workshops")
    }
}
