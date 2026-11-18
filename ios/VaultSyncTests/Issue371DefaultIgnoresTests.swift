import Foundation
import Testing
@testable import VaultSync

/// Once per launch the app writes its default ignore patterns into every
/// vault. The engine's write of `.stignore` made a missing directory, so a
/// vault whose folder was moved or deleted while VaultSync was closed got an
/// empty folder of its name back at its old place, and lost the way out that
/// a vault with a missing path has (#371). The bridge and the engine now
/// refuse to write anywhere but into a directory that holds the folder's
/// marker (decision 069); these tests hold the app's side of it.
@Suite("A refused filter write reads as a vault whose folder is gone (#371)")
struct Issue371DefaultIgnoresTests {

    /// The bridge's refusal reaches the Sync Filters screen as a message:
    /// it has to read as a vault whose folder was moved or deleted, not as
    /// a configuration problem or an unexpected error.
    @Test("The bridge's refusal reads as a vault whose folder was moved or deleted")
    func refusalMapsToTheMovedOrDeletedCard() {
        for refusal in [
            "folder directory or marker missing: the folder's directory is gone",
            "folder directory or marker missing: the directory holds no folder marker",
            "folder directory or marker missing: the folder's path is not a directory",
        ] {
            #expect(SyncUserError.from(rawMessage: refusal).category == .folderMarkerMissing)
        }
    }

    /// A directory the app cannot reach — access to the selected folder
    /// expired — is not taken for a gone one: it keeps the permission
    /// guidance, which leads to reconnecting.
    @Test("A directory that cannot be read keeps the permission guidance")
    func unreadableDirectoryMapsToPermission() {
        for refusal in [
            "check the folder's directory: operation not permitted",
            "check the folder's directory: permission denied",
        ] {
            #expect(SyncUserError.from(rawMessage: refusal).category == .permission)
        }
    }
}

extension EngineBridgeSuites {
    /// #371 against the real engine, in the framework the app links: a
    /// framework built before the fix fails here.
    @Suite("The default patterns against the real engine make no folder (#371)")
    struct Issue371DefaultIgnoresThroughTheBridgeTests {

        /// A running engine with one vault that holds an indexed note — a
        /// folder with nothing in its index is a new one to the engine,
        /// which would make its directory again on its own (#338) — and
        /// then the vault's folder is moved away.
        @MainActor
        private final class Rig {
            let manager: SyncthingManager
            let folderID = "issue371-\(UUID().uuidString)"
            let sandbox: URL
            var own: URL { sandbox.appendingPathComponent("selected/Vault", isDirectory: true) }
            var moved: URL { sandbox.appendingPathComponent("moved-into/Vault", isDirectory: true) }

            init() async throws {
                TestSupport.resetSyncthingState()
                BackgroundSyncService.lifecycleLock.withLock { $0.releaseForeground() }
                #expect(SyncBridgeService.startSyncthing(configDir: TestSupport.syncthingConfigPath()) == nil)
                manager = SyncthingManager()
                #expect(manager.adoptRunningEngine())
                sandbox = FileManager.default.temporaryDirectory
                    .appendingPathComponent("issue371-\(UUID().uuidString)", isDirectory: true)
                #expect(SyncBridgeService.addFolder(id: folderID, label: "Vault", path: own.path) == nil)
                try Data("# Note\n".utf8).write(to: own.appendingPathComponent("note.md"))
                try await scanUntilIndexed()
                try FileManager.default.createDirectory(
                    at: moved.deletingLastPathComponent(),
                    withIntermediateDirectories: true
                )
                try FileManager.default.moveItem(at: own, to: moved)
            }

            func tearDown() {
                manager.stop()
                TestSupport.resetSyncthingState()
                try? FileManager.default.removeItem(at: sandbox)
            }

            private func scanUntilIndexed() async throws {
                for _ in 0..<100 {
                    _ = SyncBridgeService.rescanFolder(folderID: folderID)
                    if let status = SyncBridgeService.getFolderStatus(folderID: folderID), status.localFiles == 1 {
                        return
                    }
                    try await Task.sleep(for: .milliseconds(100))
                }
                Issue.record("the folder never indexed its note")
            }

            var ownExists: Bool { FileManager.default.fileExists(atPath: own.path) }
        }

        @MainActor
        @Test("The launch pass over a vault that was moved away makes no folder at its old place")
        func launchPassMakesNoFolder() async throws {
            let rig = try await Rig()
            defer { rig.tearDown() }

            SyncthingManager.applyDefaultIgnoresIfNeeded(folderID: rig.folderID)
            #expect(!rig.ownExists)
        }

        @MainActor
        @Test("Both writes are refused, and a filter change says the folder is gone")
        func bridgeRefusesTheWrites() async throws {
            let rig = try await Rig()
            defer { rig.tearDown() }

            let ensure = SyncBridgeService.ensureDefaultIgnores(folderID: rig.folderID, defaultsJSON: #"[".Trash"]"#)
            #expect(ensure?.contains("marker missing") == true)
            #expect(!rig.ownExists)

            let filterError = rig.manager.setIgnorePatterns(folderID: rig.folderID, patterns: ["*.tmp"])
            #expect(filterError?.category == .folderMarkerMissing)
            #expect(!rig.ownExists)

            // The moved vault keeps everything it had.
            #expect(FileManager.default.fileExists(atPath: rig.moved.appendingPathComponent(".stfolder").path))
            #expect(FileManager.default.fileExists(atPath: rig.moved.appendingPathComponent("note.md").path))
        }
    }
}
