import Foundation
import Testing
@testable import VaultSync

/// Bounded conflict reads (#184, decision 041): the bridge protocol decoder,
/// pinned without an engine.
@Suite("Conflict read protocol (#184)")
struct ConflictReadProtocolTests {
    @Test("The bridge read protocol decodes content, the too-large refusal and plain failures")
    func parsesReadProtocol() {
        #expect(SyncBridgeService.parseConflictFileRead("# note") == .content("# note"))
        #expect(SyncBridgeService.parseConflictFileRead("") == .content(""))
        #expect(SyncBridgeService.parseConflictFileRead("error:too large:1048577") == .tooLarge(bytes: 1_048_577))
        #expect(SyncBridgeService.parseConflictFileRead("error:folder not found") == .failed("folder not found"))
        #expect(SyncBridgeService.parseConflictFileRead("error:too large:x") == .failed("too large:x"))
    }

    /// A failed read must win over an oversized one: the failure state hides
    /// the resolution actions, so a version nobody could read is never
    /// discarded or overwritten by a choice made without seeing it.
    @Test("A failed read is reported before an oversized one; two oversized reads report the larger size")
    func failureWinsOverTooLarge() {
        let tooLarge = SyncBridgeService.ConflictFileRead.tooLarge(bytes: 2_000_000)
        let failed = SyncBridgeService.ConflictFileRead.failed("permission denied")
        let content = SyncBridgeService.ConflictFileRead.content("# note")

        let failedOriginal = ConflictDiffView.loadedContent(original: failed, conflict: tooLarge)
        #expect(failedOriginal.error != nil)
        #expect(failedOriginal.tooLargeBytes == nil)

        let failedConflict = ConflictDiffView.loadedContent(original: tooLarge, conflict: failed)
        #expect(failedConflict.error != nil)
        #expect(failedConflict.tooLargeBytes == nil)

        let oneOversized = ConflictDiffView.loadedContent(original: content, conflict: tooLarge)
        #expect(oneOversized.error == nil)
        #expect(oneOversized.tooLargeBytes == 2_000_000)

        let bothOversized = ConflictDiffView.loadedContent(original: .tooLarge(bytes: 3_000_000), conflict: tooLarge)
        #expect(bothOversized.tooLargeBytes == 3_000_000)

        let both = ConflictDiffView.loadedContent(original: content, conflict: content)
        #expect(both == ConflictDiffView.LoadedContent(original: "# note", conflict: "# note"))
    }
}

extension EngineBridgeSuites {
    /// Bounded reads and the off-main skip flow against a real engine (#184).
    @Suite("Conflict reads are bounded and the skip flow runs off the main actor (#184)")
    struct ConflictReadBoundTests {

        private static func makeFolderURL(_ id: String) -> URL {
            let url = FileManager.default.temporaryDirectory
                .appendingPathComponent("vaultsync-tests", isDirectory: true)
                .appendingPathComponent(id, isDirectory: true)
            try? FileManager.default.removeItem(at: url)
            return url
        }

        /// A note at the bound reads in full; one byte over reads as too large,
        /// with the size the bridge saw — the app never receives its bytes.
        @MainActor
        @Test("A note one byte over the bound reads as too large; one at the bound reads in full")
        func overCapReadsAsTooLarge() throws {
            TestSupport.resetSyncthingState()
            #expect(SyncBridgeService.startSyncthing(configDir: TestSupport.syncthingConfigPath()) == nil)
            defer { TestSupport.resetSyncthingState() }

            let folderID = "read-bound-\(UUID().uuidString.prefix(8))"
            let folderURL = Self.makeFolderURL(folderID)
            #expect(SyncBridgeService.addFolder(id: folderID, label: "Read Bound", path: folderURL.path) == nil)

            let limit = SyncBridgeService.maxReadFileBytes()
            #expect(limit > 0)
            let atCap = Data(repeating: 0x61, count: Int(limit))
            try atCap.write(to: folderURL.appendingPathComponent("at-cap.md"))
            var overCap = atCap
            overCap.append(0x62)
            try overCap.write(to: folderURL.appendingPathComponent("over-cap.md"))

            let atCapRead = SyncBridgeService.readFileContent(folderID: folderID, relPath: "at-cap.md")
            guard case .content(let text) = atCapRead else {
                Issue.record("at-cap read = \(atCapRead), want content")
                return
            }
            #expect(text.utf8.count == Int(limit))

            let overCapRead = SyncBridgeService.readFileContent(folderID: folderID, relPath: "over-cap.md")
            #expect(overCapRead == .tooLarge(bytes: limit + 1), "one byte over the bound must be refused, not loaded")
        }

        /// The skip flow moved off the main actor (#184); its observable
        /// contract is unchanged: ignore rules written, copies removed and
        /// counted, the original untouched.
        @MainActor
        @Test("Skipping a file writes the ignore rules, removes its conflict copies and reports the count")
        func skipFlowKeepsItsContract() async throws {
            TestSupport.resetSyncthingState()
            let manager = SyncthingManager()
            await manager.start()
            defer {
                manager.stop()
                TestSupport.resetSyncthingState()
            }
            #expect(manager.isRunning)

            let folderID = "skip-flow-\(UUID().uuidString.prefix(8))"
            let folderURL = Self.makeFolderURL(folderID)
            #expect(manager.addFolder(id: folderID, label: "Skip Flow", path: folderURL.path) == nil)

            let original = "Note.md"
            let copy = "Note.sync-conflict-20260915-120000-ABCDEFG.md"
            try "mine".write(to: folderURL.appendingPathComponent(original), atomically: true, encoding: .utf8)
            try "theirs".write(to: folderURL.appendingPathComponent(copy), atomically: true, encoding: .utf8)

            let outcome = await manager.skipFileAndCleanupConflicts(folderID: folderID, originalPath: original)
            #expect(outcome.error == nil, "skip failed: \(outcome.error?.message ?? "")")
            #expect(outcome.removedConflicts == 1)
            #expect(!FileManager.default.fileExists(atPath: folderURL.appendingPathComponent(copy).path))
            #expect(FileManager.default.fileExists(atPath: folderURL.appendingPathComponent(original).path))

            let patterns = manager.ignorePatterns(folderID: folderID)
            #expect(patterns.contains(original))
            #expect(patterns.contains(SyncthingManager.conflictGlob(forOriginalPath: original)))
        }

        /// The skip flow edits `.stignore` off the main actor while the preset
        /// and pattern edits run on it. Every read-modify-write goes through
        /// one lock, so no interleaving can drop the other side's rule.
        @MainActor
        @Test("Concurrent ignore edits from the skip flow and the main actor never lose a rule")
        func concurrentIgnoreEditsKeepEveryRule() async throws {
            TestSupport.resetSyncthingState()
            let manager = SyncthingManager()
            await manager.start()
            defer {
                manager.stop()
                TestSupport.resetSyncthingState()
            }
            #expect(manager.isRunning)

            let folderID = "ignore-race-\(UUID().uuidString.prefix(8))"
            let folderURL = Self.makeFolderURL(folderID)
            #expect(manager.addFolder(id: folderID, label: "Ignore Race", path: folderURL.path) == nil)

            for round in 0..<20 {
                let skipped = "skipped-\(round).md"
                let custom = "custom-\(round)"
                async let skip = manager.skipFileAndCleanupConflicts(folderID: folderID, originalPath: skipped)
                let addError = manager.addIgnorePatterns([custom], folderID: folderID)
                let skipOutcome = await skip
                #expect(addError == nil)
                #expect(skipOutcome.error == nil)
                let patterns = manager.ignorePatterns(folderID: folderID)
                #expect(patterns.contains(skipped), "round \(round): the skip rule was lost")
                #expect(patterns.contains(custom), "round \(round): the custom rule was lost")
            }
        }
    }
}
