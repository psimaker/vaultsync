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
        func folder(refused: Bool, wholeFolder: Bool = false) -> SyncthingManager.UnreachableFolder {
            SyncthingManager.UnreachableFolder(
                id: "f1",
                label: "Vault",
                path: "/old/Obsidian/Vault",
                reason: "folder_path_missing",
                hasObsidianMapping: true,
                foundDirectoryIsNotItsStorage: refused,
                syncsSelectedFolderItself: wholeFolder
            )
        }
        let gone = folder(refused: false).explanation
        let copy = folder(refused: true).explanation
        // Only the copy's card has a section of its own to link to.
        #expect(folder(refused: false).troubleshootingURL == nil)
        #expect(folder(refused: true).troubleshootingURL?.fragment == "the-folder-found-is-a-copy")
        // A vault that syncs the selected folder itself: no folder of its
        // name is involved, and "the folder that contains it" has no marker.
        let wholeFolderCopy = folder(refused: true, wholeFolder: true).explanation
        #expect(wholeFolderCopy.contains("The folder you selected is a copy"))
        #expect(wholeFolderCopy.contains("select that folder itself"))
        #expect(!wholeFolderCopy.contains("with this vault's name"))
        #expect(folder(refused: false, wholeFolder: true).explanation == gone)
        #expect(gone == "This vault points to storage that no longer exists on this iPhone, so it can no longer sync.")
        #expect(copy != gone)
        #expect(copy.contains("it is a copy, not the folder this vault synced in"))
        // The share of a removed vault is offered again only after a new
        // start of VaultSync (#316), so the step says to reopen it first.
        #expect(copy.contains("remove this vault, wait ten seconds, close VaultSync in the app switcher and open it again, and accept the vault again under Pending Shares"))
        #expect(wholeFolderCopy.contains("then wait ten seconds, close VaultSync in the app switcher and open it again, and accept the vault again under Pending Shares"))
        // The way on never combines the share with the copy (#331), and the
        // step hangs on what the share says, not on a question that a share
        // with a remembered folder never asks.
        #expect(copy.contains("If the share then says that its folder already holds files, do not combine them"))
        #expect(!copy.contains("asks before it combines"))
    }

    private func inError(_ reason: String = "folder_path_missing") -> SyncthingManager.FolderStatusInfo {
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
            errorReason: reason,
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

        // The overtaken pass finishes first: what it found changes nothing,
        // and the later one decides when it is done.
        manager._testSetFoldersWithoutTheirStorage(["the-state-before"])
        overtaken = manager._testBeginReconcile()
        last = manager._testBeginReconcile()
        manager._testFinishReconcile(overtaken, finding: found(["f1"]))
        #expect(manager.foldersWithoutTheirStorage == ["the-state-before"])
        manager._testFinishReconcile(last, finding: found([]))
        #expect(manager.foldersWithoutTheirStorage.isEmpty)
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
        manager._testSetFolderStatuses(["f1": inError(), "f2": inError(), "f3": inError("unknown_error")])
        manager._testSetFoldersWithoutTheirStorage(["f1", "f3"])

        let copy = try #require(manager.folderUserError(folderID: "f1"))
        #expect(copy.category == .folderIsACopy)
        #expect(copy.message == SyncthingManager.UnreachableFolder.copyExplanation(syncsSelectedFolderItself: false))
        // Recreating the folder is what puts a copy in the vault's place.
        #expect(copy.remediation.isEmpty)
        #expect(SyncUserError.troubleshootingURL(for: copy)?.fragment == "the-folder-found-is-a-copy")

        // A vault whose folder is gone and nothing was found keeps its own
        // text — which no longer says to recreate the folder (#308).
        let gone = try #require(manager.folderUserError(folderID: "f2"))
        #expect(gone.category == .config)
        #expect(gone.title == "Folder Path Missing")
        #expect(gone.remediation.hasPrefix("If you moved or renamed the folder, move it back to its original place."))

        // The copy text belongs to a directory that cannot be reached. A
        // flagged vault in another error is shown that error.
        let other = try #require(manager.folderUserError(folderID: "f3"))
        #expect(other.category != .folderIsACopy)
    }

    @MainActor
    @Test("A vault that syncs the selected folder itself gets the text for a copied selected folder on its page")
    func vaultPageForAWholeFolderVault() throws {
        let id = "issue315-whole-\(UUID().uuidString)"
        FolderPathReconciler.setRel("", forFolder: id)
        defer { FolderPathReconciler.removeRel(forFolder: id) }
        let manager = SyncthingManager()
        manager._testSetFolderStatuses([id: inError()])
        manager._testSetFoldersWithoutTheirStorage([id])

        let copy = try #require(manager.folderUserError(folderID: id))
        #expect(copy.message == SyncthingManager.UnreachableFolder.copyExplanation(syncsSelectedFolderItself: true))
        // And every vault in that folder is part of the copy.
        #expect(manager.refusedCopyRels == [""])
    }

    @Test("A copy that was refused in a vault's place is not listed as a vault waiting to be shared")
    func refusedCopyIsNotAnUnsyncedVault() {
        let root = "/new/obsidian"
        func unsynced(_ rels: [String]) -> [String] {
            UnsyncedVaultsModel.derive(
                detectedVaults: ["Vault", "Other"],
                folderPathsCanonLower: ["/old/obsidian/vault"],
                rootCanonLower: root,
                refusedCopyRels: rels
            )
        }
        // Without a finding both are vaults nothing syncs.
        #expect(unsynced([]) == ["Vault", "Other"])
        // The copy under the vault's name is spoken for by its card.
        #expect(unsynced(["Vault"]) == ["Other"])
        // A copied selected folder: everything in it is part of the copy.
        #expect(unsynced([""]).isEmpty)
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
                // Right after an engine start the folder can get its marker a
                // moment after the add, and the add then wrote no record of
                // the directory the folder syncs in: a copy would be taken
                // for it (#373). Every reconcile writes that record first, as
                // here, once the marker is there.
                try #require(await TestSupport.waitForFolderMarker(folderID: folderID, in: own))
                SyncBridgeService.noteFolderStorage()
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

            /// The folder's path as the engine has it. Not the manager's
            /// `folders`: the manager's poll refreshes that copy, and a poll
            /// that read the folders before a reconcile moved this one can
            /// write what it read after the reconcile's own refresh (#373).
            func configuredPath() -> String? {
                guard let data = SyncBridgeService.getFoldersJSON().data(using: .utf8),
                      let folders = try? JSONDecoder().decode([SyncthingManager.FolderInfo].self, from: data) else {
                    return nil
                }
                return folders.first { $0.id == folderID }.map { FolderPathReconciler.canonical($0.path) }
            }

            /// Makes the folder's marker file anew in its place: the same
            /// content under another file number. The new one is written
            /// before the old one goes, so it cannot be handed the old one's
            /// number.
            func makeTheMarkerAnew() throws {
                let marker = TestSupport.folderMarkerFile(folderID: folderID, in: own)
                let replacement = marker.appendingPathExtension("new")
                try Data(contentsOf: marker).write(to: replacement)
                try FileManager.default.removeItem(at: marker)
                try FileManager.default.moveItem(at: replacement, to: marker)
            }

            /// Rescans until the engine's error reason for the folder is
            /// `reason`, or until it is idle without an error for nil.
            func engineSays(reason: String?) async -> Bool {
                for _ in 0..<100 {
                    _ = SyncBridgeService.rescanFolder(folderID: folderID)
                    if let status = SyncBridgeService.getFolderStatus(folderID: folderID),
                       status.errorReason == reason,
                       reason != nil || status.state == "idle" {
                        return true
                    }
                    try? await Task.sleep(for: .milliseconds(100))
                }
                return false
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
        @Test("The path these tests read is the engine's, also when a poll writes what it read before the reconcile after it (#373)")
        func configuredPathIsTheEngines() async throws {
            let rig = try await Rig(wholeFolder: true)
            defer { rig.tearDown() }

            // A poll that read the folders before the folder moved...
            let readBefore = SyncBridgeService.getFoldersJSON()
            let readStartedAt = SyncthingManager.monotonicNow()
            try FileManager.default.moveItem(at: rig.own, to: rig.other)
            await rig.manager.reconcileFolderPaths(obsidianRoot: rig.secondRoot.path).value
            // ...and writes it after the reconcile's own refresh, as one did
            // on the CI runner: the manager's copy names the old place.
            rig.manager._testApplyPolledFolderRead(json: readBefore, readStartedAt: readStartedAt, engineRunningAcrossRead: true)
            try #require(
                rig.manager.folders.first { $0.id == rig.folderID }.map { FolderPathReconciler.canonical($0.path) }
                    == FolderPathReconciler.canonical(rig.firstRoot.path)
            )

            #expect(rig.configuredPath() == FolderPathReconciler.canonical(rig.secondRoot.path))
        }

        @MainActor
        @Test("Removing the vault takes its finding along")
        func removalForgetsTheFinding() async throws {
            let rig = try await Rig()
            defer { rig.tearDown() }
            try await rig.leaveOnlyACopy()
            #expect(rig.manager.foldersWithoutTheirStorage == [rig.folderID])

            // A folder name remembered for the vault's share, chosen under
            // the folder that is selected now: until here it stood for
            // consent to sync into the directory of that name.
            let root = ManualShareTargetStore.rootKey(rig.secondRoot.path)
            ManualShareTargetStore.setTarget("Vault", forFolder: rig.folderID, underRoot: root)

            #expect(rig.manager.removeFolder(id: rig.folderID) == nil)
            #expect(rig.manager.foldersWithoutTheirStorage.isEmpty)
            // The name stays, the consent does not: that directory is the
            // copy, and the share must not go into it without the question.
            #expect(ManualShareTargetStore.target(forFolder: rig.folderID) == "Vault")
            #expect(ManualShareTargetStore.consentRoot(forFolder: rig.folderID) == nil)
        }

        @MainActor
        @Test("Removing a vault without such a finding, on settled paths, leaves a remembered folder name as it was (#52)")
        func removalWithoutAFindingKeepsTheConsent() async throws {
            let rig = try await Rig()
            defer { rig.tearDown() }
            let root = ManualShareTargetStore.rootKey(rig.firstRoot.path)
            ManualShareTargetStore.setTarget("Vault", forFolder: rig.folderID, underRoot: root)
            await rig.manager.reconcileFolderPaths(obsidianRoot: rig.firstRoot.path).value
            #expect(rig.manager.pathSettlement.settled)

            #expect(rig.manager.removeFolder(id: rig.folderID) == nil)
            #expect(ManualShareTargetStore.consentRoot(forFolder: rig.folderID) == root)
        }

        @MainActor
        @Test("Removing a vault while a reconcile is still under way drops the consent: what it will find is not known yet")
        func removalDuringAReconcileForgetsTheConsent() async throws {
            let rig = try await Rig()
            defer { rig.tearDown() }
            let root = ManualShareTargetStore.rootKey(rig.firstRoot.path)
            ManualShareTargetStore.setTarget("Vault", forFolder: rig.folderID, underRoot: root)
            await rig.manager.reconcileFolderPaths(obsidianRoot: rig.firstRoot.path).value

            // A pass that has begun and not reported: it may be about to
            // find a copy in this vault's place.
            let underWay = rig.manager._testBeginReconcile()
            #expect(!rig.manager.pathSettlement.settled)
            #expect(rig.manager.removeFolder(id: rig.folderID) == nil)
            #expect(ManualShareTargetStore.target(forFolder: rig.folderID) == "Vault")
            #expect(ManualShareTargetStore.consentRoot(forFolder: rig.folderID) == nil)

            // And what that pass then reports for the removed vault is dropped.
            rig.manager._testFinishReconcile(
                underWay,
                finding: FolderPathReconciler.Outcome(notTheirStorage: [rig.folderID])
            )
            #expect(rig.manager.foldersWithoutTheirStorage.isEmpty)
        }

        @MainActor
        @Test("A copied selected folder is refused for a vault that syncs the selected folder itself")
        func wholeFolderCopyIsRefused() async throws {
            let rig = try await Rig(wholeFolder: true)
            defer { rig.tearDown() }

            try await rig.leaveOnlyACopy()

            #expect(rig.manager.foldersWithoutTheirStorage == [rig.folderID])
            #expect(rig.manager.refusedCopyRels == [""])
        }

        @MainActor
        @Test("Coming back to the app looks again while a copy is reported: once the copy is gone, so is the finding")
        func returningToTheAppRechecksACopy() async throws {
            let rig = try await Rig()
            defer { rig.tearDown() }
            let vaultManager = VaultManager()
            vaultManager._testSetAccess(accessible: true, detectedVaults: [], obsidianDirectoryURL: rig.secondRoot)

            // Nothing reported: coming back reconciles nothing.
            #expect(rig.manager.recheckCopiesFound(obsidianRoot: rig.secondRoot.path) == nil)

            try await rig.leaveOnlyACopy()
            #expect(rig.manager.foldersWithoutTheirStorage == [rig.folderID])

            // The copy is deleted in the Files app, and the app comes back
            // to the foreground over its running engine.
            try FileManager.default.removeItem(at: rig.other)
            #expect(EngineAttach.onForeground(syncthingManager: rig.manager, vaultManager: vaultManager) == .alreadyAttached)
            for _ in 0..<100 where !rig.manager.foldersWithoutTheirStorage.isEmpty {
                try await Task.sleep(for: .milliseconds(50))
            }
            #expect(rig.manager.foldersWithoutTheirStorage.isEmpty)
        }

        @MainActor
        @Test("A marker made anew in a vault's place moves no record: the engine stops syncing the folder there, and moved afterwards it is a copy (#329)")
        func aMarkerMadeAnewMovesNoRecord() async throws {
            let rig = try await Rig()
            defer { rig.tearDown() }

            // The marker is made anew in its place, so the folder is known
            // by another file than the one recorded when it was added — what
            // a copy put in the vault's place looks like (#329). The
            // reconcile leaves the record as it is, and the engine refuses
            // the directory before its next scan.
            try rig.makeTheMarkerAnew()
            await rig.manager.reconcileFolderPaths(obsidianRoot: rig.firstRoot.path).value
            #expect(await rig.engineSays(reason: "folder_not_its_directory"))

            // Moved afterwards, the folder is not the directory it synced
            // in, and the repair does not re-point it.
            try FileManager.default.moveItem(at: rig.own, to: rig.other)
            await rig.manager.reconcileFolderPaths(obsidianRoot: rig.secondRoot.path).value

            #expect(rig.manager.foldersWithoutTheirStorage == [rig.folderID])
            #expect(rig.configuredPath() == FolderPathReconciler.canonical(rig.own.path))
        }

        @MainActor
        @Test("Where no record applies, a reconcile records the folder by the marker it has now (#329)")
        func aReconcileRecordsTheFolderWhereNoRecordApplies() async throws {
            let rig = try await Rig()
            defer { rig.tearDown() }

            // What a restore does to this install's anchor: no record written
            // before it applies once the engine starts again (decision 063).
            rig.manager.stop()
            let anchor = URL(fileURLWithPath: TestSupport.syncthingConfigPath())
                .appendingPathComponent("vaultsync-storage-anchor")
            // Kept under another name, so its file number is not handed out
            // to the new one.
            try FileManager.default.moveItem(at: anchor, to: anchor.appendingPathExtension("kept"))
            #expect(SyncBridgeService.startSyncthing(configDir: TestSupport.syncthingConfigPath()) == nil)
            #expect(rig.manager.adoptRunningEngine())

            // The marker made anew: with no record that applies, the
            // reconcile records the folder by it, and the engine syncs on.
            try rig.makeTheMarkerAnew()
            await rig.manager.reconcileFolderPaths(obsidianRoot: rig.firstRoot.path).value
            #expect(await rig.engineSays(reason: nil))

            // Moved afterwards, the folder is taken where it is.
            try FileManager.default.moveItem(at: rig.own, to: rig.other)
            await rig.manager.reconcileFolderPaths(obsidianRoot: rig.secondRoot.path).value

            #expect(rig.manager.foldersWithoutTheirStorage.isEmpty)
            #expect(rig.configuredPath() == FolderPathReconciler.canonical(rig.other.path))
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
