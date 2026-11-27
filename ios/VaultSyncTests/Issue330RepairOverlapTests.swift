import Foundation
import Testing
@testable import VaultSync

/// The path repair never re-points a folder where it would overlap another
/// folder's (#330, decision 082). The bridge refuses; the reconciler passes
/// the refusal on, and the vault's card and page say what was found instead
/// of "storage no longer exists". No test here reads or writes the stored
/// folder mappings: every mapping is handed in.
@Suite("The path repair never makes two folders overlap (#330)")
struct Issue330RepairOverlapTests {

    /// One folder whose own directory is gone, with a directory of its name
    /// under the newly selected folder.
    private func reconcile(
        bridgeAnswers answer: FolderPathReconciler.SetPathResult
    ) -> (outcome: FolderPathReconciler.Outcome, asked: [String], rel: [String: String]) {
        let target = FolderPathReconciler.canonical("/new/Obsidian/Vault")
        var asked: [String] = []
        var rel = ["f1": "Vault"]
        let env = FolderPathReconciler.Environment(
            obsidianRoot: "/new/Obsidian",
            loadRel: { rel },
            recordRel: { id, value in rel[id] = value },
            dirExists: { FolderPathReconciler.canonical($0) == target },
            setPath: { _, path in
                asked.append(FolderPathReconciler.canonical(path))
                return answer
            }
        )
        let outcome = FolderPathReconciler.reconcile(folders: [(id: "f1", path: "/old/Obsidian/Vault")], env: env)
        return (outcome, asked, rel)
    }

    @Test("A directory the bridge refuses for an overlap is reported, asked once, and the mapping stays")
    func overlapIsReported() {
        let result = reconcile(bridgeAnswers: .overlapsAnotherFolder)
        #expect(result.outcome == FolderPathReconciler.Outcome(overlapsAnotherFolder: ["f1"]))
        #expect(result.asked == [FolderPathReconciler.canonical("/new/Obsidian/Vault")])
        #expect(result.rel == ["f1": "Vault"])
    }

    @Test("Neither a copy nor another refusal is taken for an overlap")
    func otherRefusalsAreNotAnOverlap() {
        #expect(reconcile(bridgeAnswers: .notItsStorage).outcome == FolderPathReconciler.Outcome(notTheirStorage: ["f1"]))
        #expect(reconcile(bridgeAnswers: .failed).outcome == FolderPathReconciler.Outcome())
        #expect(reconcile(bridgeAnswers: .moved).outcome == FolderPathReconciler.Outcome())
    }

    @Test("What a reconcile found is cut to the folders that are still configured, overlaps too")
    func outcomeIsCutToConfiguredFolders() {
        let outcome = FolderPathReconciler.Outcome(
            notTheirStorage: ["copy-removed", "copy-kept"],
            overlapsAnotherFolder: ["overlap-removed", "overlap-kept"]
        )
        let cut = outcome.cut(to: ["copy-kept", "overlap-kept"])
        #expect(cut == FolderPathReconciler.Outcome(notTheirStorage: ["copy-kept"], overlapsAnotherFolder: ["overlap-kept"]))
    }

    private func card(overlap: Bool, copy: Bool = false) -> SyncthingManager.UnreachableFolder {
        SyncthingManager.UnreachableFolder(
            id: "f1",
            label: "Vault",
            path: "/old/Obsidian/Vault",
            reason: "folder_path_missing",
            hasObsidianMapping: true,
            foundDirectoryIsNotItsStorage: copy,
            syncsSelectedFolderItself: false,
            foundDirectoryOverlapsAnotherFolder: overlap
        )
    }

    @Test("The vault's card names the overlap and the way on, and links to its own section")
    func cardExplainsTheOverlap() {
        let text = card(overlap: true).explanation
        #expect(text == SyncthingManager.UnreachableFolder.overlapExplanation)
        #expect(text.contains("another vault already syncs that folder, a folder inside it, or a folder around it"))
        #expect(text.contains("Two vaults never sync in one place"))
        // The share of a removed vault is offered again only after a new
        // start of VaultSync (#316).
        #expect(text.contains("Remove this vault, wait ten seconds, close VaultSync in the app switcher and open it again, and accept the vault again under Pending Shares"))
        // Never combined with what a folder holds (#331).
        #expect(text.contains("If the share then says that its folder already holds files, do not combine them"))
        #expect(card(overlap: true).troubleshootingURL?.fragment == "the-folder-found-overlaps-another-vault")

        // Without the finding the card keeps its text, and has no link.
        #expect(card(overlap: false).explanation == "This vault points to storage that no longer exists on this iPhone, so it can no longer sync.")
        #expect(card(overlap: false).troubleshootingURL == nil)
        // The copy keeps its own text and section.
        #expect(card(overlap: false, copy: true).troubleshootingURL?.fragment == "the-folder-found-is-a-copy")
    }

    @Test("The card's text and its title are in every language")
    func textsAreTranslated() throws {
        for language in ["en", "de", "es", "zh-Hans"] {
            let table = try TestSupport.localizedStrings(language)
            for key in [
                "The Folder Found Overlaps Another Vault",
                SyncthingManager.UnreachableFolder.overlapExplanation,
            ] {
                let text = try #require(table[key], "\(language) lacks \(key)")
                #expect(!text.isEmpty)
                if language != "en" { #expect(text != key, "\(language) leaves \(key) untranslated") }
            }
        }
    }

    private func inError(_ reason: String) -> SyncthingManager.FolderStatusInfo {
        SyncthingManager.FolderStatusInfo(payload: .init(
            state: "error",
            stateChanged: "2026-11-27T09:00:00Z",
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

    @MainActor
    @Test("The vault's own page says what its card says, while its folder cannot be reached")
    func vaultPageSaysWhatTheCardSays() throws {
        let manager = SyncthingManager()
        manager._testSetFolders([folder("f1"), folder("f2")])
        manager._testSetFolderStatuses(["f1": inError("folder_path_missing"), "f2": inError("unknown_error")])
        manager._testSetFoldersOverlappingAnotherFolder(["f1", "f2"])

        let page = try #require(manager.folderUserError(folderID: "f1"))
        #expect(page.category == .folderOverlapsAnotherFolder)
        #expect(page.title == "The Folder Found Overlaps Another Vault")
        #expect(page.message == SyncthingManager.UnreachableFolder.overlapExplanation)
        #expect(page.remediation.isEmpty)
        #expect(SyncUserError.troubleshootingURL(for: page)?.fragment == "the-folder-found-overlaps-another-vault")
        // A flagged vault in another error is shown that error.
        #expect(manager.folderUserError(folderID: "f2")?.category != .folderOverlapsAnotherFolder)

        // The card under Sync Issues carries the finding.
        let cards = manager.unreachableFolders
        #expect(cards.map(\.id) == ["f1"])
        #expect(cards.first?.foundDirectoryOverlapsAnotherFolder == true)
        #expect(cards.first?.foundDirectoryIsNotItsStorage == false)
    }

    @MainActor
    @Test("A reconcile that was overtaken does not decide what the card says, and a removed vault's finding is dropped")
    func onlyTheLastReconcileDecides() {
        let manager = SyncthingManager()
        manager._testSetFolders([folder("f1")])
        let found = FolderPathReconciler.Outcome(overlapsAnotherFolder: ["f1", "removed-meanwhile"])

        let overtaken = manager._testBeginReconcile()
        let last = manager._testBeginReconcile()
        manager._testFinishReconcile(last, finding: found)
        #expect(manager.foldersOverlappingAnotherFolder == ["f1"])
        manager._testFinishReconcile(overtaken, finding: FolderPathReconciler.Outcome())
        #expect(manager.foldersOverlappingAnotherFolder == ["f1"])

        let next = manager._testBeginReconcile()
        manager._testFinishReconcile(next, finding: FolderPathReconciler.Outcome())
        #expect(manager.foldersOverlappingAnotherFolder.isEmpty)
    }

    @MainActor
    @Test("Coming back to the app looks again while an overlap is reported")
    func returnLooksAgain() {
        let manager = SyncthingManager()
        #expect(manager.recheckCopiesFound(obsidianRoot: "/new/Obsidian") == nil)
        manager._testSetFoldersOverlappingAnotherFolder(["f1"])
        // The engine is not running here, so the reconcile it starts ends at
        // once; that one is started is what counts.
        #expect(manager.recheckCopiesFound(obsidianRoot: "/new/Obsidian") != nil)
    }

    @MainActor
    @Test("A vault's own folder refused for an overlap is not listed as a vault waiting to be shared")
    func refusedFolderIsNotAnUnsyncedVault() {
        let manager = SyncthingManager()
        manager._testSetFoldersOverlappingAnotherFolder(["f1"])
        manager._testSetFoldersWithoutTheirStorage(["f2"])
        let rels = manager.refusedCopyRels(rel: ["f1": "Moved", "f2": "Copy", "f3": "Other"])
        #expect(Set(rels) == ["Moved", "Copy"])
        #expect(UnsyncedVaultsModel.derive(
            detectedVaults: ["Moved", "Other"],
            folderPathsCanonLower: ["/new/obsidian/moved/inner"],
            rootCanonLower: "/new/obsidian",
            refusedCopyRels: rels
        ) == ["Other"])
    }
}

extension EngineBridgeSuites {
    /// #330 against the real engine, in the framework the app links: the
    /// refusal and its text are the bridge's, so only a run against it shows
    /// that the app tells the refusal from the others. The mapping is handed
    /// to the reconcile, not stored.
    @Suite("The path repair against the real engine refuses a vault's folder inside another vault's (#330)")
    struct Issue330RepairOverlapThroughTheBridgeTests {

        private struct BridgeFolder: Decodable {
            let id: String
            let path: String
        }

        private func configuredPath(_ folderID: String) -> String? {
            let data = Data(SyncBridgeService.getFoldersJSON().utf8)
            let folders = (try? JSONDecoder().decode([BridgeFolder].self, from: data)) ?? []
            return folders.first { $0.id == folderID }.map { FolderPathReconciler.canonical($0.path) }
        }

        @Test("A vault's own folder moved into another vault's folder is refused, and the reconcile says why")
        func overlapIsRefused() throws {
            TestSupport.resetSyncthingState()
            let sandbox = FileManager.default.temporaryDirectory
                .appendingPathComponent("issue330-\(UUID().uuidString)", isDirectory: true)
            defer {
                TestSupport.resetSyncthingState()
                try? FileManager.default.removeItem(at: sandbox)
            }
            #expect(SyncBridgeService.startSyncthing(configDir: TestSupport.syncthingConfigPath()) == nil)

            let root = sandbox.appendingPathComponent("selected", isDirectory: true)
            let outer = root.appendingPathComponent("Outer", isDirectory: true)
            let own = root.appendingPathComponent("Inner", isDirectory: true)
            let outerID = "issue330-outer-\(UUID().uuidString)"
            let innerID = "issue330-inner-\(UUID().uuidString)"
            try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
            #expect(SyncBridgeService.addFolder(id: outerID, label: "Outer", path: outer.path) == nil)
            #expect(SyncBridgeService.addFolder(id: innerID, label: "Inner", path: own.path) == nil)

            // A move keeps the marker's file number: the directory is the
            // folder's own, and only the overlap is left to refuse it.
            let moved = outer.appendingPathComponent("Inner", isDirectory: true)
            try FileManager.default.moveItem(at: own, to: moved)

            var rel = [outerID: "Outer", innerID: "Outer/Inner"]
            let env = FolderPathReconciler.Environment(
                obsidianRoot: root.path,
                loadRel: { rel },
                recordRel: { id, value in rel[id] = value },
                dirExists: { path in
                    var isDirectory: ObjCBool = false
                    return FileManager.default.fileExists(atPath: path, isDirectory: &isDirectory) && isDirectory.boolValue
                },
                setPath: FolderPathReconciler.bridgeSetPath
            )
            let outcome = FolderPathReconciler.reconcile(
                folders: [(id: outerID, path: outer.path), (id: innerID, path: own.path)],
                env: env
            )

            #expect(outcome == FolderPathReconciler.Outcome(overlapsAnotherFolder: [innerID]))
            #expect(configuredPath(innerID) == FolderPathReconciler.canonical(own.path))
            #expect(configuredPath(outerID) == FolderPathReconciler.canonical(outer.path))
        }
    }
}
