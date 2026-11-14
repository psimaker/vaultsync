import Foundation
import Testing
@testable import VaultSync

/// A copy of a vault made by hand carries the folder's marker (#315). The
/// bridge decides whether a directory is the one a folder synced in
/// (decision 063); the reconciler passes its answer on, and the vault's card
/// and page say what was found instead of "storage no longer exists".
@Suite("A copy with the folder's marker is not the folder's directory (#315)")
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

    @Test("A directory the bridge refuses as a copy is reported, asked once, and the mapping stays")
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

    @Test("Another refusal is not taken for a copy")
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

    @Test("What a reconcile found is cut to the folders that are still configured")
    func outcomeIsCutToConfiguredFolders() {
        let outcome = FolderPathReconciler.Outcome(notTheirStorage: ["removed-meanwhile", "still-there"])
        #expect(outcome.cut(to: ["still-there", "another"]).notTheirStorage == ["still-there"])
        #expect(outcome.cut(to: []).notTheirStorage.isEmpty)
    }

    @Test("The vault's card names the copy and the way on, and keeps its text for storage that is gone")
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
        #expect(copy.contains("it is a copy, not the folder this vault synced in"))
        #expect(copy.contains("remove this vault and accept it again under Pending Shares"))
        // The way on never combines the share with the copy (#331), and the
        // step hangs on what the share says, not on a question that a share
        // with a remembered folder never asks.
        #expect(copy.contains("If the share then says that its folder already holds files, do not combine them"))
        #expect(!copy.contains("asks before it combines"))
    }

    private func pathMissing() -> SyncthingManager.FolderStatusInfo {
        SyncthingManager.FolderStatusInfo(payload: .init(
            state: "error",
            stateChanged: "2026-11-14T09:00:00Z",
            completionPct: 100,
            globalBytes: 0,
            globalFiles: 0,
            localBytes: 0,
            localFiles: 0,
            needBytes: 0,
            needFiles: 0,
            inProgressBytes: 0,
            errorReason: "folder_path_missing",
            errorMessage: nil,
            errorPath: nil,
            errorChanged: nil
        ))
    }

    private func folder(_ id: String) -> SyncthingManager.FolderInfo {
        SyncthingManager.FolderInfo(
            id: id, label: id, path: "/old/Obsidian/\(id)", type: "sendreceive", paused: false, deviceIDs: []
        )
    }

    private func found(_ ids: Set<String>) -> FolderPathReconciler.Outcome {
        FolderPathReconciler.Outcome(notTheirStorage: ids)
    }

    @MainActor
    @Test("A reconcile that was overtaken does not decide what the card says, whenever it finishes")
    func overtakenReconcileDoesNotPublish() {
        let manager = SyncthingManager()
        manager._testSetFolders([folder("f1")])

        // The overtaken pass finishes last: it must not undo the later one.
        var overtaken = manager._testBeginReconcile()
        var last = manager._testBeginReconcile()
        manager._testFinishReconcile(last, finding: found([]))
        manager._testFinishReconcile(overtaken, finding: found(["f1"]))
        #expect(manager.foldersWithoutTheirStorage.isEmpty)

        // The overtaken pass finishes first: it says nothing until the later
        // one has.
        overtaken = manager._testBeginReconcile()
        last = manager._testBeginReconcile()
        manager._testFinishReconcile(overtaken, finding: found([]))
        manager._testSetFoldersWithoutTheirStorage(["marker-of-the-state-before"])
        #expect(manager.foldersWithoutTheirStorage == ["marker-of-the-state-before"])
        manager._testFinishReconcile(last, finding: found(["f1"]))
        #expect(manager.foldersWithoutTheirStorage == ["f1"])
    }

    @MainActor
    @Test("A finding for a vault that was removed while the reconcile ran is dropped")
    func findingForARemovedVaultIsDropped() {
        let manager = SyncthingManager()
        manager._testSetFolders([folder("still-there")])
        let token = manager._testBeginReconcile()
        manager._testFinishReconcile(token, finding: found(["removed-meanwhile", "still-there"]))
        #expect(manager.foldersWithoutTheirStorage == ["still-there"])
    }

    @MainActor
    @Test("The vault's own page says what its card says, not \"recreate or reselect the folder\"")
    func vaultPageSaysWhatTheCardSays() throws {
        let manager = SyncthingManager()
        manager._testSetFolderStatuses(["f1": pathMissing(), "f2": pathMissing()])
        manager._testSetFoldersWithoutTheirStorage(["f1"])

        let copy = try #require(manager.folderUserError(folderID: "f1"))
        #expect(copy.category == .folderIsACopy)
        #expect(copy.message == SyncthingManager.UnreachableFolder.copyExplanation)
        // Recreating the folder is what puts a copy in the vault's place.
        #expect(copy.remediation.isEmpty)
        #expect(SyncUserError.troubleshootingURL(for: copy)?.fragment == "the-folder-found-is-a-copy")

        // A vault whose folder is gone and nothing was found keeps its text.
        let gone = try #require(manager.folderUserError(folderID: "f2"))
        #expect(gone.category == .config)
        #expect(gone.remediation == "Recreate or reselect the folder, then trigger a rescan.")
    }
}

extension EngineBridgeSuites {
    /// #315 through every layer: the app's own reconcile against the real
    /// engine. What the bridge refuses and the text it refuses with are the
    /// bridge's, so only a run against it shows that the app recognises the
    /// answer.
    @Suite("The path repair against the real engine: a copy is refused, the vault's own folder goes on (#315)")
    struct Issue315RepairThroughTheBridgeTests {

        /// A running engine with one vault that holds an indexed note, under
        /// a first selected folder — in a folder of its own, or syncing the
        /// selected folder itself the way installs from before 3.0 can.
        @MainActor
        private final class Rig {
            let manager: SyncthingManager
            let folderID = "issue315-\(UUID().uuidString)"
            let sandbox: URL
            let wholeFolder: Bool
            var firstRoot: URL { sandbox.appendingPathComponent("selected-1", isDirectory: true) }
            var secondRoot: URL { sandbox.appendingPathComponent("selected-2", isDirectory: true) }
            var own: URL { wholeFolder ? firstRoot : firstRoot.appendingPathComponent("Vault", isDirectory: true) }
            var other: URL { wholeFolder ? secondRoot : secondRoot.appendingPathComponent("Vault", isDirectory: true) }

            init(wholeFolder: Bool = false) async throws {
                self.wholeFolder = wholeFolder
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
                FolderPathReconciler.setRel(wholeFolder ? "" : "Vault", forFolder: folderID)
                try write("first version\n", to: "note.md")
                try write("goes away while VaultSync does not look\n", to: "deleted.md")
                try await scanUntilIndexed(files: 2)
            }

            func tearDown() {
                manager.stop()
                TestSupport.resetSyncthingState()
                FolderPathReconciler.removeRel(forFolder: folderID)
                try? FileManager.default.removeItem(at: sandbox)
            }

            func write(_ content: String, to name: String, in directory: URL? = nil) throws {
                try Data(content.utf8).write(to: (directory ?? own).appendingPathComponent(name))
            }

            /// Scans until the index holds `files` files.
            func scanUntilIndexed(files: Int) async throws {
                for _ in 0..<100 {
                    _ = SyncBridgeService.rescanFolder(folderID: folderID)
                    if let status = SyncBridgeService.getFolderStatus(folderID: folderID), status.localFiles == files {
                        return
                    }
                    try await Task.sleep(for: .milliseconds(100))
                }
                Issue.record("the folder never indexed \(files) file(s)")
            }

            func configuredPath() -> String? {
                manager.folders.first { $0.id == folderID }.map { FolderPathReconciler.canonical($0.path) }
            }

            /// A copy made by hand, hidden entries included, and then the
            /// vault's own folder is gone: what the repair finds is the copy.
            func leaveOnlyACopy() async throws {
                try FileManager.default.copyItem(at: own, to: other)
                #expect(FileManager.default.fileExists(atPath: other.appendingPathComponent(".stfolder").path))
                try FileManager.default.removeItem(at: firstRoot)
                await manager.reconcileFolderPaths(obsidianRoot: secondRoot.path).value
            }
        }

        @MainActor
        @Test("A copy is refused: the folder stays, and the manager knows why")
        func copyIsRefused() async throws {
            let rig = try await Rig()
            defer { rig.tearDown() }
            let ownPath = FolderPathReconciler.canonical(rig.own.path)

            try await rig.leaveOnlyACopy()

            #expect(rig.manager.foldersWithoutTheirStorage == [rig.folderID])
            #expect(rig.configuredPath() == ownPath)
            #expect(FolderPathReconciler.loadRel()[rig.folderID] == "Vault")
        }

        @MainActor
        @Test("A vault moved as a whole goes on where it is, with what changed in it while VaultSync did not look")
        func movedVaultGoesOn() async throws {
            let rig = try await Rig()
            defer { rig.tearDown() }

            try FileManager.default.moveItem(at: rig.own, to: rig.other)
            // What Obsidian does while VaultSync is not running: none of it
            // is in the index, and none of it makes the folder another one.
            try rig.write("second version, never scanned\n", to: "note.md", in: rig.other)
            try rig.write("written after the move\n", to: "new.md", in: rig.other)
            try FileManager.default.removeItem(at: rig.other.appendingPathComponent("deleted.md"))

            await rig.manager.reconcileFolderPaths(obsidianRoot: rig.secondRoot.path).value

            #expect(rig.manager.foldersWithoutTheirStorage.isEmpty)
            #expect(rig.configuredPath() == FolderPathReconciler.canonical(rig.other.path))
        }

        @MainActor
        @Test("A vault that syncs the selected folder itself goes on after its folder moved, an unseen change included")
        func wholeFolderVaultGoesOn() async throws {
            let rig = try await Rig(wholeFolder: true)
            defer { rig.tearDown() }

            try FileManager.default.moveItem(at: rig.own, to: rig.other)
            try rig.write("second version, never scanned\n", to: "note.md", in: rig.other)

            await rig.manager.reconcileFolderPaths(obsidianRoot: rig.secondRoot.path).value

            #expect(rig.manager.foldersWithoutTheirStorage.isEmpty)
            #expect(rig.configuredPath() == FolderPathReconciler.canonical(rig.secondRoot.path))
        }

        @MainActor
        @Test("Removing the vault takes its finding along")
        func removalForgetsTheFinding() async throws {
            let rig = try await Rig()
            defer { rig.tearDown() }
            try await rig.leaveOnlyACopy()
            #expect(rig.manager.foldersWithoutTheirStorage == [rig.folderID])

            #expect(rig.manager.removeFolder(id: rig.folderID) == nil)
            #expect(rig.manager.foldersWithoutTheirStorage.isEmpty)
        }

        @MainActor
        @Test("Stopping the engine, or finding it stopped, empties what the last reconcile found")
        func stopAndResetForgetTheFindings() async throws {
            let rig = try await Rig()
            defer { rig.tearDown() }
            try await rig.leaveOnlyACopy()
            #expect(rig.manager.foldersWithoutTheirStorage == [rig.folderID])

            rig.manager.stop()
            #expect(rig.manager.foldersWithoutTheirStorage.isEmpty)

            rig.manager._testSetFoldersWithoutTheirStorage([rig.folderID])
            rig.manager.resetForRestart()
            #expect(rig.manager.foldersWithoutTheirStorage.isEmpty)
        }

        /// Here and not beside the other tests without an engine: resetting
        /// the manager releases the process-wide foreground claim, which a
        /// suite running at the same time may hold.
        @MainActor
        @Test("A reconcile from before the engine stopped does not decide what the card says")
        func reconcileFromAnEarlierEngineRunDoesNotPublish() {
            let manager = SyncthingManager()
            let vault = SyncthingManager.FolderInfo(
                id: "f1", label: "Vault", path: "/old/Obsidian/Vault", type: "sendreceive", paused: false, deviceIDs: []
            )
            manager._testSetFolders([vault])
            let stale = manager._testBeginReconcile()
            manager.resetForRestart()
            manager._testSetFolders([vault])
            manager._testFinishReconcile(stale, finding: FolderPathReconciler.Outcome(notTheirStorage: ["f1"]))
            #expect(manager.foldersWithoutTheirStorage.isEmpty)
        }

        @MainActor
        @Test("Of two overlapping reconciles the one that began last decides what the card says")
        func theReconcileThatBeganLastDecides() async throws {
            let rig = try await Rig()
            defer { rig.tearDown() }
            // A copy under the second folder, nothing of the vault's name
            // under a third one, and the vault's own folder gone.
            try FileManager.default.copyItem(at: rig.own, to: rig.other)
            let thirdRoot = rig.sandbox.appendingPathComponent("selected-3", isDirectory: true)
            try FileManager.default.createDirectory(at: thirdRoot, withIntermediateDirectories: true)
            try FileManager.default.removeItem(at: rig.firstRoot)

            // The pass that finds the copy begins first and is overtaken.
            let overtaken = rig.manager.reconcileFolderPaths(obsidianRoot: rig.secondRoot.path)
            let last = rig.manager.reconcileFolderPaths(obsidianRoot: thirdRoot.path)
            await overtaken.value
            await last.value
            #expect(rig.manager.foldersWithoutTheirStorage.isEmpty)

            // And the other way round: the pass that finds it begins last.
            let first = rig.manager.reconcileFolderPaths(obsidianRoot: thirdRoot.path)
            let finding = rig.manager.reconcileFolderPaths(obsidianRoot: rig.secondRoot.path)
            await first.value
            await finding.value
            #expect(rig.manager.foldersWithoutTheirStorage == [rig.folderID])
        }
    }
}
