import Foundation
import Testing
@testable import VaultSync

@Suite("Root classification — a stray .obsidian never turns the container into a vault (#79)")
struct VaultRootClassificationTests {

    // The reporting device's exact layout: the legacy whole-root sync left a
    // stray `.obsidian/` (plus `.stfolder`/`.stignore`) at the container's top
    // level, next to real vault subfolders. The root must classify as a
    // container: the advisory must not fire, and — data-critical — a share
    // accept must map to `root/<name>` instead of collapsing into the root
    // (which would nest every existing vault inside that share's synced tree).
    @Test("Stray .obsidian next to real vaults classifies as container")
    func strayConfigNextToVaultsIsContainer() {
        #expect(!VaultManager.rootIsItselfVault(hasOwnConfig: true, hasVaultSubfolders: true))
    }

    @Test("A root whose only vault config is its own stays a vault-as-root")
    func bareVaultRootStaysVault() {
        #expect(VaultManager.rootIsItselfVault(hasOwnConfig: true, hasVaultSubfolders: false))
    }

    @Test("A root without its own config is never a vault")
    func plainContainerIsNotVault() {
        #expect(!VaultManager.rootIsItselfVault(hasOwnConfig: false, hasVaultSubfolders: true))
        #expect(!VaultManager.rootIsItselfVault(hasOwnConfig: false, hasVaultSubfolders: false))
    }

    // FS seam behind the classification, against the reported on-disk layout.
    @Test("vaultSubfolderNames lists vault subfolders, not stray root-level entries")
    func vaultSubfolderNamesMatchesDeviceLayout() throws {
        let fm = FileManager.default
        let root = fm.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? fm.removeItem(at: root) }
        try fm.createDirectory(at: root.appendingPathComponent(".obsidian"), withIntermediateDirectories: true)
        try fm.createDirectory(at: root.appendingPathComponent(".stfolder"), withIntermediateDirectories: true)
        try fm.createDirectory(at: root.appendingPathComponent("brain/.obsidian"), withIntermediateDirectories: true)
        try fm.createDirectory(at: root.appendingPathComponent("Test"), withIntermediateDirectories: true)
        #expect(fm.createFile(atPath: root.appendingPathComponent(".stignore").path, contents: Data()))

        #expect(VaultManager.vaultSubfolderNames(in: root) == ["brain"])
    }

    @Test("vaultSubfolderNames: unreadable root is nil, readable-but-empty is []")
    func unreadableVsEmptyRoot() throws {
        let fm = FileManager.default
        let root = fm.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        #expect(VaultManager.vaultSubfolderNames(in: root) == nil)

        try fm.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? fm.removeItem(at: root) }
        #expect(VaultManager.vaultSubfolderNames(in: root) == [])
    }

    // A `.obsidian` that is a FILE (e.g. a sync artifact), not a directory,
    // does not make the subfolder a vault — mirrors the existing per-site
    // `isDirectory` checks the classification replaced.
    @Test("A .obsidian file (not directory) does not mark a vault")
    func obsidianFileIsNotAVaultMarker() throws {
        let fm = FileManager.default
        let root = fm.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? fm.removeItem(at: root) }
        try fm.createDirectory(at: root.appendingPathComponent("NotAVault"), withIntermediateDirectories: true)
        #expect(fm.createFile(atPath: root.appendingPathComponent("NotAVault/.obsidian").path, contents: Data()))

        #expect(VaultManager.vaultSubfolderNames(in: root) == [])
    }
}
