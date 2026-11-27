import Foundation
import Testing
@testable import VaultSync

/// The wait the engine tests take after adding a folder (#373, #398, #225).
@Suite("Engine tests wait for a new folder's marker (#373, #398, #225)")
struct Issue373FolderMarkerWaitTests {
    private static func makeDirectory() throws -> URL {
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("issue373-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        return url
    }

    @Test("The wait ends once the marker file is there, and not while only its directory is")
    func waitsForTheMarkerFile() async throws {
        let folderID = "wait-\(UUID().uuidString)"
        let directory = try Self.makeDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }
        // The engine makes `.stfolder` first and writes the file into it
        // after: the file is what the bridge reads the directory by.
        try FileManager.default.createDirectory(
            at: directory.appendingPathComponent(".stfolder", isDirectory: true),
            withIntermediateDirectories: false
        )
        let marker = TestSupport.folderMarkerFile(folderID: folderID, in: directory)
        let writer = Task.detached {
            try await Task.sleep(for: .milliseconds(200))
            try Data("marker\n".utf8).write(to: marker)
        }

        let clock = ContinuousClock()
        let began = clock.now
        #expect(await TestSupport.waitForFolderMarker(folderID: folderID, in: directory))
        #expect(began.duration(to: clock.now) >= .milliseconds(200))
        #expect(FileManager.default.fileExists(atPath: marker.path))
        try await writer.value
    }

    @Test("Without a marker the wait gives up and says so")
    func givesUpWithoutAMarker() async throws {
        let directory = try Self.makeDirectory()
        defer { try? FileManager.default.removeItem(at: directory) }

        #expect(!(await TestSupport.waitForFolderMarker(
            folderID: "never-\(UUID().uuidString)",
            in: directory,
            timeout: .milliseconds(50)
        )))
    }
}

extension EngineBridgeSuites {
    /// A folder that gets its marker only after the add returned, against
    /// the real engine. On the CI runner the engine sometimes starts a
    /// folder added right after its own start a moment late, from its first
    /// look at the configuration; a folder added into a directory the engine
    /// cannot write into, which then gets its marker, stands in for that here.
    @Suite("A folder whose marker came after the add, against the real engine (#373, #398)")
    struct Issue373LateMarkerThroughTheBridgeTests {

        @MainActor
        private final class Rig {
            let folderID = "issue373-\(UUID().uuidString)"
            let sandbox: URL
            var own: URL { sandbox.appendingPathComponent("selected-1/Vault", isDirectory: true) }
            var copy: URL { sandbox.appendingPathComponent("selected-2/Vault", isDirectory: true) }

            /// The folder is added into a directory without write permission,
            /// so the add makes no marker: the state the add leaves when the
            /// engine starts the folder late.
            init() throws {
                TestSupport.resetSyncthingState()
                #expect(SyncBridgeService.startSyncthing(configDir: TestSupport.syncthingConfigPath()) == nil)
                sandbox = FileManager.default.temporaryDirectory
                    .appendingPathComponent("issue373-\(UUID().uuidString)", isDirectory: true)
                try FileManager.default.createDirectory(at: own, withIntermediateDirectories: true)
                try FileManager.default.createDirectory(at: copy.deletingLastPathComponent(), withIntermediateDirectories: true)
                try FileManager.default.setAttributes([.posixPermissions: 0o555], ofItemAtPath: own.path)
                #expect(SyncBridgeService.addFolder(id: folderID, label: "Vault", path: own.path) == nil)
                try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: own.path)
            }

            func tearDown() {
                try? FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: own.path)
                TestSupport.resetSyncthingState()
                try? FileManager.default.removeItem(at: sandbox)
            }

            var markerFile: URL { TestSupport.folderMarkerFile(folderID: folderID, in: own) }

            /// What the engine writes when it starts the folder.
            func makeTheMarker() throws {
                try FileManager.default.createDirectory(
                    at: own.appendingPathComponent(".stfolder", isDirectory: true),
                    withIntermediateDirectories: true
                )
                try Data("# This directory is a Syncthing folder marker.\n".utf8).write(to: markerFile)
            }

            func writeFilters(_ patterns: [String]) -> String? {
                let json = (try? JSONEncoder().encode(patterns)).flatMap { String(data: $0, encoding: .utf8) } ?? "[]"
                return SyncBridgeService.setFolderIgnores(folderID: folderID, ignoresJSON: json)
            }

            func scanUntilIndexed(files: Int) async throws {
                for _ in 0..<100 {
                    _ = SyncBridgeService.rescanFolder(folderID: folderID)
                    if SyncBridgeService.getFolderStatus(folderID: folderID)?.localFiles == files {
                        return
                    }
                    try await Task.sleep(for: .milliseconds(50))
                }
                Issue.record("the folder never indexed \(files) file(s)")
            }
        }

        @MainActor
        @Test("The marker file the wait looks for is the one the engine writes")
        func theWaitLooksForTheEnginesMarker() async throws {
            TestSupport.resetSyncthingState()
            #expect(SyncBridgeService.startSyncthing(configDir: TestSupport.syncthingConfigPath()) == nil)
            let folderID = "issue373-\(UUID().uuidString)"
            let directory = FileManager.default.temporaryDirectory
                .appendingPathComponent("issue373-\(UUID().uuidString)", isDirectory: true)
            defer {
                TestSupport.resetSyncthingState()
                try? FileManager.default.removeItem(at: directory)
            }

            #expect(SyncBridgeService.addFolder(id: folderID, label: "Vault", path: directory.path) == nil)
            #expect(await TestSupport.waitForFolderMarker(folderID: folderID, in: directory))
            let entries = try FileManager.default.contentsOfDirectory(
                atPath: directory.appendingPathComponent(".stfolder", isDirectory: true).path
            )
            #expect(entries == [TestSupport.folderMarkerFile(folderID: folderID, in: directory).lastPathComponent])
        }

        @MainActor
        @Test("A filter write right after the add is refused as marker missing, and goes through once the marker is there")
        func aFilterWriteWaitsForTheMarker() async throws {
            let rig = try Rig()
            defer { rig.tearDown() }
            try #require(!FileManager.default.fileExists(atPath: rig.markerFile.path))

            // What #398's round 0 met.
            #expect(rig.writeFilters(["custom-0"])?.contains("marker missing") == true)

            try rig.makeTheMarker()
            #expect(await TestSupport.waitForFolderMarker(folderID: rig.folderID, in: rig.own))
            #expect(rig.writeFilters(["custom-0"]) == nil)
        }

        @MainActor
        @Test("Once the marker is there, the note records the folder's directory, and a copy of it is refused")
        func theNoteRecordsALateMarker() async throws {
            let rig = try Rig()
            defer { rig.tearDown() }
            try #require(!FileManager.default.fileExists(atPath: rig.markerFile.path))

            try rig.makeTheMarker()
            #expect(await TestSupport.waitForFolderMarker(folderID: rig.folderID, in: rig.own))
            // The add found no marker and recorded nothing; this is the
            // record every reconcile writes first.
            SyncBridgeService.noteFolderStorage()
            try Data("note\n".utf8).write(to: rig.own.appendingPathComponent("note.md"))
            try await rig.scanUntilIndexed(files: 1)

            try FileManager.default.copyItem(at: rig.own, to: rig.copy)
            try FileManager.default.removeItem(at: rig.own.deletingLastPathComponent())
            #expect(SyncBridgeService.setFolderPath(folderID: rig.folderID, path: rig.copy.path)
                == SyncBridgeService.folderStorageMismatchError)
        }
    }
}
