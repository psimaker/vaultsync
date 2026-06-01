import Foundation
import Testing
@testable import VaultSync

@Suite("FolderPathReconciler — launch-time path rebasing")
struct FolderPathReconcilerTests {

    /// In-memory backing for an injected `Environment`, recording every
    /// `setPath` call and exposing the final relative-path map.
    final class Spy {
        var rel: [String: String]
        var existingDirs: Set<String>
        var setPathCalls: [(id: String, path: String)] = []

        init(rel: [String: String], existingDirs: Set<String> = []) {
            self.rel = rel
            self.existingDirs = existingDirs
        }
    }

    private func makeEnv(spy: Spy, root: String?) -> FolderPathReconciler.Environment {
        FolderPathReconciler.Environment(
            obsidianRoot: root,
            loadRel: { spy.rel },
            recordRel: { id, rel in spy.rel[id] = rel },
            dirExists: { spy.existingDirs.contains(FolderPathReconciler.canonical($0)) },
            setPath: { id, path in
                spy.setPathCalls.append((id, path))
                return nil
            }
        )
    }

    // MARK: - Rebasing

    @Test("Whole-directory folder rebases to the current root when its path is stale")
    func wholeDirRebase() {
        let root = "/new/Obsidian"
        let spy = Spy(rel: ["f1": ""], existingDirs: [FolderPathReconciler.canonical(root)])
        FolderPathReconciler.reconcile(
            folders: [(id: "f1", path: "/old/Obsidian")],
            env: makeEnv(spy: spy, root: root)
        )
        #expect(spy.setPathCalls.count == 1)
        #expect(spy.setPathCalls.first?.id == "f1")
        #expect(FolderPathReconciler.canonical(spy.setPathCalls.first?.path ?? "")
            == FolderPathReconciler.canonical(root))
    }

    @Test("Subdirectory folder rebases to root + relative subpath")
    func subdirRebase() {
        let root = "/new/Obsidian"
        let desired = "/new/Obsidian/Personal"
        let spy = Spy(rel: ["f1": "Personal"], existingDirs: [FolderPathReconciler.canonical(desired)])
        FolderPathReconciler.reconcile(
            folders: [(id: "f1", path: "/old/Obsidian/Personal")],
            env: makeEnv(spy: spy, root: root)
        )
        #expect(spy.setPathCalls.count == 1)
        #expect(FolderPathReconciler.canonical(spy.setPathCalls.first?.path ?? "")
            == FolderPathReconciler.canonical(desired))
    }

    @Test("No rebase when the path already matches (steady state is restart-free)")
    func noOpWhenMatching() {
        let root = "/Obsidian"
        let spy = Spy(rel: ["f1": ""], existingDirs: [FolderPathReconciler.canonical(root)])
        FolderPathReconciler.reconcile(
            folders: [(id: "f1", path: "/Obsidian")],
            env: makeEnv(spy: spy, root: root)
        )
        #expect(spy.setPathCalls.isEmpty)
    }

    @Test("No rebase when the would-be target does not exist (never point at an empty dir)")
    func skipWhenTargetMissing() {
        let root = "/new/Obsidian"
        let spy = Spy(rel: ["f1": "Personal"], existingDirs: []) // desired dir absent
        FolderPathReconciler.reconcile(
            folders: [(id: "f1", path: "/old/Obsidian/Personal")],
            env: makeEnv(spy: spy, root: root)
        )
        #expect(spy.setPathCalls.isEmpty)
    }

    @Test("Known mapping is left untouched when no Obsidian root is available this launch")
    func noRootNoChange() {
        let spy = Spy(rel: ["f1": ""])
        FolderPathReconciler.reconcile(
            folders: [(id: "f1", path: "/old/Obsidian")],
            env: makeEnv(spy: spy, root: nil)
        )
        #expect(spy.setPathCalls.isEmpty)
        #expect(spy.rel["f1"] == "")
    }

    // MARK: - Migration (backfill) & unrecoverable folders

    @Test("Unmapped folder under the root is backfilled, not rebased")
    func backfillUnderRoot() {
        let root = "/Obsidian"
        let spy = Spy(rel: [:])
        FolderPathReconciler.reconcile(
            folders: [(id: "f1", path: "/Obsidian/Work")],
            env: makeEnv(spy: spy, root: root)
        )
        #expect(spy.setPathCalls.isEmpty)
        #expect(spy.rel["f1"] == "Work")
    }

    @Test("Unmapped folder equal to the root is backfilled as the whole directory")
    func backfillWholeDir() {
        let root = "/Obsidian"
        let spy = Spy(rel: [:])
        FolderPathReconciler.reconcile(
            folders: [(id: "f1", path: "/Obsidian")],
            env: makeEnv(spy: spy, root: root)
        )
        #expect(spy.setPathCalls.isEmpty)
        #expect(spy.rel["f1"] == "")
    }

    @Test("Legacy app-container folder (issue #25) is left untouched for guided removal")
    func unmappableLeftAlone() {
        let root = "/Obsidian"
        let spy = Spy(rel: [:])
        FolderPathReconciler.reconcile(
            folders: [(
                id: "6xueb-3iqkn",
                path: "/var/mobile/Containers/Data/Application/OLD-UUID/Documents/6xueb-3iqkn"
            )],
            env: makeEnv(spy: spy, root: root)
        )
        #expect(spy.setPathCalls.isEmpty)
        #expect(spy.rel["6xueb-3iqkn"] == nil)
    }

    // MARK: - Pure helpers

    @Test("relativeIfUnder: empty for equal, suffix for nested, nil for outside")
    func relativeHelper() {
        #expect(FolderPathReconciler.relativeIfUnder("/Obsidian", root: "/Obsidian") == "")
        #expect(FolderPathReconciler.relativeIfUnder("/Obsidian/Personal", root: "/Obsidian") == "Personal")
        #expect(FolderPathReconciler.relativeIfUnder("/Other/Personal", root: "/Obsidian") == nil)
        // A sibling whose name merely starts with the root string is not "under" it.
        #expect(FolderPathReconciler.relativeIfUnder("/ObsidianBackup", root: "/Obsidian") == nil)
    }

    @Test("desiredPath joins root and relative path")
    func desiredPathHelper() {
        #expect(FolderPathReconciler.desiredPath(root: "/Obsidian", rel: "") == "/Obsidian")
        #expect(FolderPathReconciler.desiredPath(root: "/Obsidian", rel: "Personal") == "/Obsidian/Personal")
    }

    // MARK: - Sidecar store

    @Test("Sidecar set / load / remove round-trips through UserDefaults")
    func sidecarRoundTrip() {
        UserDefaults.standard.removeObject(forKey: "vaultsync.folderRelPath")
        FolderPathReconciler.setRel("Personal", forFolder: "f1")
        #expect(FolderPathReconciler.loadRel()["f1"] == "Personal")
        FolderPathReconciler.removeRel(forFolder: "f1")
        #expect(FolderPathReconciler.loadRel()["f1"] == nil)
        UserDefaults.standard.removeObject(forKey: "vaultsync.folderRelPath")
    }
}
