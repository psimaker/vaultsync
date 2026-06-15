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
        baseIsVault: Bool = false,
        nameMatchesBase: Bool = false,
        name: String,
        occupied: Set<String> = []
    ) -> String {
        VaultManager.resolveSharePath(
            rawRoot: root,
            baseIsVault: baseIsVault,
            nameMatchesBase: nameMatchesBase,
            folderName: name,
            occupiedCanonLower: occupied,
            canonicalize: identity
        )
    }

    @Test("Fresh install: a single vault collapses into the root")
    func freshSingleVaultCollapsesIntoRoot() {
        #expect(resolve(baseIsVault: true, name: "MyVault") == "/Obsidian")
    }

    @Test("Multi-vault root: a distinct vault gets its own subdirectory")
    func distinctVaultGetsSubfolder() {
        #expect(resolve(name: "Personal") == "/Obsidian/Personal")
    }

    @Test("nameMatchesBase collapses into the root only while it is free")
    func nameMatchesBaseCollapsesWhenFree() {
        #expect(resolve(nameMatchesBase: true, name: "Obsidian") == "/Obsidian")
    }

    @Test("#45: a second vault never reuses an occupied root")
    func secondVaultDoesNotReuseOccupiedRoot() {
        // The root already holds the first vault — `baseIsVault` is true because
        // the root now contains a `.obsidian/`. The second share must NOT
        // collapse on top of it; it gets its own subdirectory.
        let result = resolve(
            baseIsVault: true,
            name: "Second",
            occupied: occupied("/Obsidian")
        )
        #expect(result == "/Obsidian/Second")
    }

    @Test("nameMatchesBase falls back to a subdirectory when the root is taken")
    func nameMatchesBaseFallsBackWhenRootTaken() {
        let result = resolve(
            nameMatchesBase: true,
            name: "Obsidian",
            occupied: occupied("/Obsidian")
        )
        #expect(result == "/Obsidian/Obsidian")
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
        let b = resolve(name: "Personal", occupied: occupied(a))
        #expect(b == "/Obsidian/Personal")
    }

    @Test("Sequential accept: first collapses into root, second goes to a subfolder")
    func sequentialAcceptIsCollisionFree() {
        // First vault, root empty, baseIsVault true → collapses into the root.
        let first = resolve(baseIsVault: true, name: "Alpha")
        #expect(first == "/Obsidian")
        // Second vault, root now occupied → its own subdirectory, no merge.
        let second = resolve(baseIsVault: true, name: "Beta", occupied: occupied(first))
        #expect(second == "/Obsidian/Beta")
    }
}
