import Foundation
import Testing
@testable import VaultSync

/// A copy of a vault made by hand carries the folder's marker (#315). The
/// bridge decides whether a directory is a folder's storage (decision 063);
/// the reconciler passes its answer on, and the vault's card says what was
/// found instead of "storage no longer exists".
@Suite("A copy with the folder's marker is not the folder's storage (#315)")
struct Issue315CopiedMarkerTests {

    /// One folder whose own directory is gone, with a directory of its name
    /// under the newly selected folder.
    private func reconcile(
        bridgeAnswers answer: FolderPathReconciler.SetPathResult,
        targetExists: Bool = true,
        ownDirectoryExists: Bool = false
    ) -> (outcome: FolderPathReconciler.Outcome, asked: [String], rel: [String: String]) {
        let own = "/old/Obsidian/Vault"
        let root = "/new/Obsidian"
        let target = "/new/Obsidian/Vault"
        var existing: Set<String> = []
        if targetExists { existing.insert(FolderPathReconciler.canonical(target)) }
        if ownDirectoryExists { existing.insert(FolderPathReconciler.canonical(own)) }
        var asked: [String] = []
        var rel = ["f1": "Vault"]
        let env = FolderPathReconciler.Environment(
            obsidianRoot: root,
            loadRel: { rel },
            recordRel: { id, value in rel[id] = value },
            dirExists: { existing.contains(FolderPathReconciler.canonical($0)) },
            setPath: { _, path in
                asked.append(FolderPathReconciler.canonical(path))
                return answer
            }
        )
        let outcome = FolderPathReconciler.reconcile(folders: [(id: "f1", path: own)], env: env)
        return (outcome, asked, rel)
    }

    @Test("A directory the bridge refuses as not the folder's storage is reported, asked once, and the mapping stays")
    func refusedDirectoryIsReported() {
        let result = reconcile(bridgeAnswers: .notItsStorage)
        #expect(result.outcome.notTheirStorage == ["f1"])
        #expect(result.asked == [FolderPathReconciler.canonical("/new/Obsidian/Vault")])
        #expect(result.rel == ["f1": "Vault"])
    }

    @Test("A folder the bridge moves is not reported")
    func movedFolderIsNotReported() {
        let result = reconcile(bridgeAnswers: .moved)
        #expect(result.outcome.notTheirStorage.isEmpty)
        #expect(result.asked.count == 1)
    }

    @Test("Another refusal is not taken for an earlier copy")
    func otherFailureIsNotReported() {
        let result = reconcile(bridgeAnswers: .failed)
        #expect(result.outcome.notTheirStorage.isEmpty)
        #expect(result.asked.count == 1)
    }

    @Test("Without a directory of the folder's name the bridge is not asked and nothing is reported")
    func missingTargetIsNotReported() {
        let result = reconcile(bridgeAnswers: .notItsStorage, targetExists: false)
        #expect(result.outcome.notTheirStorage.isEmpty)
        #expect(result.asked.isEmpty)
    }

    @Test("A folder whose own directory exists is never offered another one")
    func liveFolderIsNotOffered() {
        let result = reconcile(bridgeAnswers: .notItsStorage, ownDirectoryExists: true)
        #expect(result.outcome.notTheirStorage.isEmpty)
        #expect(result.asked.isEmpty)
    }

    @Test("The vault's card names the earlier copy and the way on, and keeps its text for storage that is gone")
    func cardExplainsWhatWasFound() {
        func folder(refused: Bool) -> SyncthingManager.UnreachableFolder {
            SyncthingManager.UnreachableFolder(
                id: "f1",
                label: "Vault",
                path: "/old/Obsidian/Vault",
                reason: "folder_path_missing",
                hasObsidianMapping: true,
                foundDirectoryIsNotItsStorage: refused
            )
        }
        let gone = folder(refused: false).explanation
        let copy = folder(refused: true).explanation
        #expect(gone == "This vault points to storage that no longer exists on this iPhone, so it can no longer sync.")
        #expect(copy != gone)
        #expect(copy.contains("earlier copy"))
        #expect(copy.contains("remove this vault and accept it again under Pending Shares"))
    }
}

extension EngineBridgeSuites {
    /// #315 through every layer: the app's own reconcile against the real
    /// engine. What the bridge refuses and the text it refuses with are the
    /// bridge's, so only a run against it shows that the app recognises the
    /// answer.
    @Suite("The path repair against the real engine: a copy is refused, a moved vault goes on (#315)")
    struct Issue315RepairThroughTheBridgeTests {

        /// A running engine with one vault that holds an indexed note, under
        /// a first selected folder.
        @MainActor
        private final class Rig {
            let manager: SyncthingManager
            let folderID = "issue315-\(UUID().uuidString)"
            let sandbox: URL
            var own: URL { sandbox.appendingPathComponent("selected-1/Vault", isDirectory: true) }
            var other: URL { sandbox.appendingPathComponent("selected-2/Vault", isDirectory: true) }

            init() async throws {
                TestSupport.resetSyncthingState()
                BackgroundSyncService.lifecycleLock.withLock { $0.releaseForeground() }
                #expect(SyncBridgeService.startSyncthing(configDir: TestSupport.syncthingConfigPath()) == nil)
                manager = SyncthingManager()
                #expect(manager.adoptRunningEngine())
                sandbox = FileManager.default.temporaryDirectory
                    .appendingPathComponent("issue315-\(UUID().uuidString)", isDirectory: true)
                try FileManager.default.createDirectory(
                    at: other.deletingLastPathComponent(),
                    withIntermediateDirectories: true
                )
                #expect(manager.addFolder(id: folderID, label: "Vault", path: own.path) == nil)
                FolderPathReconciler.setRel("Vault", forFolder: folderID)
                try write("first version\n", to: "note.md")
                try await scanUntilIndexed(files: 1)
            }

            func tearDown() {
                manager.stop()
                TestSupport.resetSyncthingState()
                FolderPathReconciler.removeRel(forFolder: folderID)
                try? FileManager.default.removeItem(at: sandbox)
            }

            func write(_ content: String, to name: String) throws {
                try Data(content.utf8).write(to: own.appendingPathComponent(name))
            }

            /// Scans until the index holds `files` files and `bytes` bytes,
            /// when given: a changed note is in the index once its size is.
            func scanUntilIndexed(files: Int, bytes: Int64? = nil) async throws {
                for _ in 0..<100 {
                    _ = SyncBridgeService.rescanFolder(folderID: folderID)
                    if let status = SyncBridgeService.getFolderStatus(folderID: folderID),
                       status.localFiles == files, bytes == nil || status.localBytes == bytes {
                        return
                    }
                    try await Task.sleep(for: .milliseconds(100))
                }
                Issue.record("the folder never indexed \(files) file(s)")
            }

            func configuredPath() -> String? {
                manager.folders.first { $0.id == folderID }.map { FolderPathReconciler.canonical($0.path) }
            }
        }

        @MainActor
        @Test("A copy made while the vault kept syncing is refused: the folder stays, and the manager knows why")
        func copyIsRefused() async throws {
            let rig = try await Rig()
            defer { rig.tearDown() }

            // The copy by hand, hidden entries and modification times included.
            try FileManager.default.copyItem(at: rig.own, to: rig.other)
            #expect(FileManager.default.fileExists(atPath: rig.other.appendingPathComponent(".stfolder").path))

            // The vault goes on: one note changes, one is new.
            try rig.write("second version, written after the copy was made\n", to: "note.md")
            try rig.write("written after the copy was made\n", to: "later.md")
            let bytes = Int64("second version, written after the copy was made\n".utf8.count
                + "written after the copy was made\n".utf8.count)
            try await rig.scanUntilIndexed(files: 2, bytes: bytes)

            let ownPath = FolderPathReconciler.canonical(rig.own.path)
            try FileManager.default.removeItem(at: rig.own.deletingLastPathComponent())

            await rig.manager.reconcileFolderPaths(obsidianRoot: rig.other.deletingLastPathComponent().path).value

            #expect(rig.manager.foldersWithoutTheirStorage == [rig.folderID])
            #expect(rig.configuredPath() == ownPath)
            #expect(FolderPathReconciler.loadRel()[rig.folderID] == "Vault")
        }

        @MainActor
        @Test("A vault moved as a whole goes on where it is, a note written after the move included")
        func movedVaultGoesOn() async throws {
            let rig = try await Rig()
            defer { rig.tearDown() }

            try FileManager.default.moveItem(at: rig.own, to: rig.other)
            try Data("written after the move\n".utf8).write(to: rig.other.appendingPathComponent("new.md"))

            await rig.manager.reconcileFolderPaths(obsidianRoot: rig.other.deletingLastPathComponent().path).value

            #expect(rig.manager.foldersWithoutTheirStorage.isEmpty)
            #expect(rig.configuredPath() == FolderPathReconciler.canonical(rig.other.path))
        }
    }
}
