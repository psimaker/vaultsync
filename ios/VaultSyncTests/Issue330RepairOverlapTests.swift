import Foundation
import Testing
@testable import VaultSync

/// The path repair never re-points a folder where it would overlap another
/// folder's (#330, decision 082). The reconcile holds such a repair back
/// before it asks the bridge — both folders of a pair whose new paths
/// overlap, in whatever order they come — and the bridge's floor refuses
/// whatever it is asked anyway. The vault's card and page say what was found
/// instead of "storage no longer exists". No test here reads or writes the
/// stored folder mappings: every mapping is handed in.
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

    /// One reconcile pass under `/new/Obsidian`. `existing` lists the
    /// directories that exist; a folder in `withoutMarker` has no marker file
    /// at its target, and the bridge refuses it for that if asked — the way
    /// the engine leaves the second folder of one directory.
    private func pass(
        _ folders: [(id: String, path: String)],
        rel: [String: String],
        existing: Set<String>,
        withoutMarker: Set<String> = []
    ) -> (outcome: FolderPathReconciler.Outcome, asked: [String]) {
        let existingCanon = Set(existing.map(FolderPathReconciler.canonical))
        var asked: [String] = []
        var rel = rel
        let env = FolderPathReconciler.Environment(
            obsidianRoot: "/new/Obsidian",
            loadRel: { rel },
            recordRel: { id, value in rel[id] = value },
            dirExists: { existingCanon.contains(FolderPathReconciler.canonical($0)) },
            setPath: { id, _ in
                asked.append(id)
                return withoutMarker.contains(id) ? .failed : .moved
            }
        )
        return (FolderPathReconciler.reconcile(folders: folders, env: env), asked)
    }

    @Test("Both folders of a nested pair stay where they are, whichever comes first")
    func nestedPairIsHeldInEitherOrder() {
        let outer = (id: "outer", path: "/old/Obsidian/Vault")
        let inner = (id: "inner", path: "/old/Obsidian/Vault/Inner")
        let rel = ["outer": "Vault", "inner": "Vault/Inner"]
        let existing: Set<String> = ["/new/Obsidian/Vault", "/new/Obsidian/Vault/Inner"]
        for folders in [[outer, inner], [inner, outer]] {
            let result = pass(folders, rel: rel, existing: existing)
            #expect(result.asked.isEmpty)
            #expect(result.outcome == FolderPathReconciler.Outcome(overlapsAnotherFolder: ["outer", "inner"]))
        }
    }

    @Test("Both folders on one directory stay where they are, whichever comes first")
    func sameDirectoryPairIsHeldInEitherOrder() {
        let a = (id: "a", path: "/old/Obsidian/Vault")
        let b = (id: "b", path: "/old/Obsidian/Vault")
        let rel = ["a": "Vault", "b": "Vault"]
        for folders in [[a, b], [b, a]] {
            let result = pass(folders, rel: rel, existing: ["/new/Obsidian/Vault"])
            #expect(result.asked.isEmpty)
            #expect(result.outcome == FolderPathReconciler.Outcome(overlapsAnotherFolder: ["a", "b"]))
        }
    }

    @Test("Two folders on one directory as the engine leaves them, one marker file: neither moves, whichever comes first")
    func sameDirectoryPairWithOneMarkerIsHeld() {
        // The engine writes a folder's marker file only where `.stfolder` is
        // missing, so the second folder of one directory has none.
        let a = (id: "a", path: "/old/Obsidian/Vault")
        let b = (id: "b", path: "/old/Obsidian/Vault")
        let rel = ["a": "Vault", "b": "Vault"]
        for folders in [[a, b], [b, a]] {
            let result = pass(folders, rel: rel, existing: ["/new/Obsidian/Vault"], withoutMarker: ["b"])
            #expect(result.asked.isEmpty)
            #expect(result.outcome == FolderPathReconciler.Outcome(overlapsAnotherFolder: ["a", "b"]))
        }
    }

    @Test("A nested pair whose inner marker file is missing: neither moves, whichever comes first")
    func nestedPairWithoutTheInnerMarkerIsHeld() {
        let outer = (id: "outer", path: "/old/Obsidian/Vault")
        let inner = (id: "inner", path: "/old/Obsidian/Vault/Inner")
        let rel = ["outer": "Vault", "inner": "Vault/Inner"]
        let existing: Set<String> = ["/new/Obsidian/Vault", "/new/Obsidian/Vault/Inner"]
        for folders in [[outer, inner], [inner, outer]] {
            let result = pass(folders, rel: rel, existing: existing, withoutMarker: ["inner"])
            #expect(result.asked.isEmpty)
            #expect(result.outcome == FolderPathReconciler.Outcome(overlapsAnotherFolder: ["outer", "inner"]))
        }
    }

    @Test("Of a pair, a folder whose new place does not exist holds nothing back")
    func aMissingTargetHoldsNothing() {
        let outer = (id: "outer", path: "/old/Obsidian/Vault")
        let inner = (id: "inner", path: "/old/Obsidian/Vault/Inner")
        let missing = pass(
            [inner, outer],
            rel: ["outer": "Vault", "inner": "Vault/Inner"],
            existing: ["/new/Obsidian/Vault"]
        )
        #expect(missing.asked == ["outer"])
        #expect(missing.outcome == FolderPathReconciler.Outcome())
    }

    @Test("Of three folders, the two that overlap stay where they are and the third is repaired")
    func thirdFolderIsRepaired() {
        let result = pass(
            [
                (id: "outer", path: "/old/Obsidian/Vault"),
                (id: "other", path: "/old/Obsidian/Other"),
                (id: "inner", path: "/old/Obsidian/Vault/Inner"),
            ],
            rel: ["outer": "Vault", "inner": "Vault/Inner", "other": "Other"],
            existing: ["/new/Obsidian/Vault", "/new/Obsidian/Vault/Inner", "/new/Obsidian/Other"]
        )
        #expect(result.asked == ["other"])
        #expect(result.outcome == FolderPathReconciler.Outcome(overlapsAnotherFolder: ["outer", "inner"]))
    }

    @Test("Once one of a pair is removed, the next pass repairs the other")
    func removingOneLetsTheOtherGoOn() {
        let outer = (id: "outer", path: "/old/Obsidian/Vault")
        let inner = (id: "inner", path: "/old/Obsidian/Vault/Inner")
        let rel = ["outer": "Vault", "inner": "Vault/Inner"]
        let existing: Set<String> = ["/new/Obsidian/Vault", "/new/Obsidian/Vault/Inner"]
        #expect(pass([outer, inner], rel: rel, existing: existing).asked.isEmpty)

        let afterRemovingInner = pass([outer], rel: ["outer": "Vault"], existing: existing)
        #expect(afterRemovingInner.asked == ["outer"])
        #expect(afterRemovingInner.outcome == FolderPathReconciler.Outcome())

        let afterRemovingOuter = pass([inner], rel: ["inner": "Vault/Inner"], existing: existing)
        #expect(afterRemovingOuter.asked == ["inner"])
    }

    @Test("A repair onto a folder that stays where it is is held, and that folder is left alone")
    func repairOntoALiveFolderIsHeld() {
        // The share accepted into this vault's folder while it could not be
        // reached: that vault syncs there now.
        let result = pass(
            [(id: "here", path: "/new/Obsidian/Notes"), (id: "gone", path: "/old/Obsidian/Notes")],
            rel: ["here": "Notes", "gone": "Notes"],
            existing: ["/new/Obsidian/Notes"]
        )
        #expect(result.asked.isEmpty)
        #expect(result.outcome == FolderPathReconciler.Outcome(overlapsAnotherFolder: ["gone"]))
    }

    @Test("A repair around another folder's old path waits one pass, without the card, while that folder moves away")
    func repairWaitsForAFolderMovingAway() {
        // `leaving` is configured inside the folder `waiting` is about to be
        // repaired to, but its own repair takes it elsewhere.
        let waiting = (id: "waiting", path: "/old/Obsidian/Vault")
        let rel = ["waiting": "Vault", "leaving": "Elsewhere"]
        let existing: Set<String> = ["/new/Obsidian/Vault", "/new/Obsidian/Elsewhere"]

        // Not asked, and no finding: the card would tell the person to remove
        // a vault that the next pass repairs by itself.
        let first = pass([waiting, (id: "leaving", path: "/new/Obsidian/Vault/Sub")], rel: rel, existing: existing)
        #expect(first.asked == ["leaving"])
        #expect(first.outcome == FolderPathReconciler.Outcome())

        let next = pass([waiting, (id: "leaving", path: "/new/Obsidian/Elsewhere")], rel: rel, existing: existing)
        #expect(next.asked == ["waiting"])
        #expect(next.outcome == FolderPathReconciler.Outcome())
    }

    @MainActor
    @Test("A repair that waits one pass shows its vault as before: no overlap card")
    func aWaitingRepairShowsNoCard() {
        let first = pass(
            [(id: "waiting", path: "/old/Obsidian/Vault"), (id: "leaving", path: "/new/Obsidian/Vault/Sub")],
            rel: ["waiting": "Vault", "leaving": "Elsewhere"],
            existing: ["/new/Obsidian/Vault", "/new/Obsidian/Elsewhere"]
        )
        let manager = SyncthingManager()
        manager._testSetFolders([folder("waiting")])
        manager._testSetFolderStatuses(["waiting": inError("folder_path_missing")])
        manager._testFinishReconcile(manager._testBeginReconcile(), finding: first.outcome)
        #expect(manager.foldersOverlappingAnotherFolder.isEmpty)
        #expect(manager.unreachableFolders.first?.foundDirectoryOverlapsAnotherFolder == false)
    }

    @Test("A folder's old path in the /private form still counts against a target in the resolved form")
    func privateFormCountsAgainstTheResolvedForm() throws {
        // `canonical` drops /private only from a path that exists: an existing
        // target under a link such as /tmp → /private/tmp (or /var on a
        // device) comes out without it, a path that is gone keeps it.
        let sandbox = URL(fileURLWithPath: "/tmp", isDirectory: true)
            .appendingPathComponent("issue330-private-\(UUID().uuidString)", isDirectory: true)
        let target = sandbox.appendingPathComponent("Vault", isDirectory: true)
        try FileManager.default.createDirectory(at: target, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: sandbox) }
        let resolved = FolderPathReconciler.canonical(target.path)
        try #require(!resolved.hasPrefix("/private/"))
        let goneInside = "/private" + resolved + "/Gone"
        try #require(FolderPathReconciler.canonical(goneInside).hasPrefix("/private/"))
        let holds = FolderPathReconciler.heldRepairs(
            [(id: "repair", target: target.path)],
            folders: [(id: "repair", path: "/old/Vault"), (id: "other", path: goneInside)]
        )
        #expect(holds.held == ["repair"])
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
        // Also true where both vaults of a pair are held where they were:
        // neither syncs there now, but each one's folder is the other's.
        #expect(text.contains("it is another vault's folder too, or lies inside or around another vault's folder"))
        #expect(!text.contains("already syncs"))
        #expect(text.contains("Two vaults never sync in one place"))
        // Two such cards each name one way on: removing either one is enough.
        #expect(text.contains("when one of the two is removed, the other goes on there"))
        // The share of a removed vault is offered again only after a new
        // start of VaultSync (#316).
        #expect(text.contains("remove it, wait ten seconds, close VaultSync in the app switcher and open it again, and accept the vault again under Pending Shares"))
        // Never combined with what a folder holds (#331).
        #expect(text.contains("If the share then says that its folder already holds files, do not combine them"))
        #expect(card(overlap: true).troubleshootingURL?.fragment == "the-folder-found-overlaps-another-vault")

        // Without the finding the card keeps its text, and has no link.
        #expect(card(overlap: false).explanation == "This vault points to storage that no longer exists on this iPhone, so it can no longer sync.")
        #expect(card(overlap: false).troubleshootingURL == nil)
        // The copy keeps its own text and section.
        #expect(card(overlap: false, copy: true).troubleshootingURL?.fragment == "the-folder-found-is-a-copy")
    }

    @Test("The overlap card does not offer to reconnect; the other cards still do")
    func overlapCardOffersNoReconnect() {
        // The vault's own folder was found: no other selection holds it,
        // and a reconnect would only repeat the refusal.
        #expect(!card(overlap: true).offersReconnect)
        #expect(card(overlap: false).offersReconnect)
        #expect(card(overlap: false, copy: true).offersReconnect)
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

    @Test("The card's text carries the whole removal step, the wait and the reopen, in every language")
    func cardTextCarriesTheRemovalStep() throws {
        // The #316 check finds the first word for "remove"; in this text that
        // is the clause that says what removing one of the two does. This
        // holds the step itself.
        let steps = [
            "en": "remove it, wait ten seconds, close VaultSync in the app switcher and open it again, and accept the vault again under Pending Shares",
            "de": "entferne ihn, warte zehn Sekunden, schließe VaultSync im App-Umschalter, öffne es erneut und nimm den Vault unter „Ausstehende Freigaben“ erneut an",
            "es": "elimina este Vault, espera diez segundos, cierra VaultSync en el selector de aplicaciones, vuelve a abrirla y acepta el Vault de nuevo en \"Comparticiones pendientes\"",
            "zh-Hans": "请移除它，等待十秒，在应用切换器中关闭 VaultSync 并重新打开，待它出现在“待处理共享”中后重新接受它",
        ]
        for (language, step) in steps {
            let table = try TestSupport.localizedStrings(language)
            let text = try #require(table[SyncthingManager.UnreachableFolder.overlapExplanation])
            #expect(text.contains(step), "\(language) lacks the removal step in: \(text)")
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
        // The engine is not running here, so `reconcileFolderPaths` returns
        // at once without reconciling anything. What this shows is the
        // recheck's own guard: an overlap finding alone lets the call through
        // to the reconcile, as a copy finding does.
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
    /// floor's refusal and its text are the bridge's, so only a run against
    /// it shows that the app reads them right. The mapping is handed to the
    /// reconcile, not stored.
    @Suite("The path repair against the real engine keeps a vault's folder out of another vault's (#330)")
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

        @MainActor
        @Test("A vault's own folder moved into another vault's folder is held by the reconcile and refused by the bridge")
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
            var asked: [String] = []
            let env = FolderPathReconciler.Environment(
                obsidianRoot: root.path,
                loadRel: { rel },
                recordRel: { id, value in rel[id] = value },
                dirExists: { path in
                    var isDirectory: ObjCBool = false
                    return FileManager.default.fileExists(atPath: path, isDirectory: &isDirectory) && isDirectory.boolValue
                },
                setPath: { folderID, newPath in
                    asked.append(folderID)
                    return FolderPathReconciler.bridgeSetPath(folderID: folderID, newPath: newPath)
                }
            )
            let outcome = FolderPathReconciler.reconcile(
                folders: [(id: outerID, path: outer.path), (id: innerID, path: own.path)],
                env: env
            )

            // The reconcile holds the repair before it asks the bridge.
            #expect(asked.isEmpty)
            #expect(outcome == FolderPathReconciler.Outcome(overlapsAnotherFolder: [innerID]))
            // Asked anyway, the bridge's floor refuses it with the text the
            // app tells from the others.
            #expect(FolderPathReconciler.bridgeSetPath(folderID: innerID, newPath: moved.path) == .overlapsAnotherFolder)
            #expect(configuredPath(innerID) == FolderPathReconciler.canonical(own.path))
            #expect(configuredPath(outerID) == FolderPathReconciler.canonical(outer.path))
        }
    }
}
