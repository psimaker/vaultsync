import Foundation
import Testing
@testable import VaultSync

@MainActor
@Suite("Bridge reads that gate decisions fail closed (#182)", .serialized)
struct FailClosedReadsTests {
    /// An unreadable `.stignore` used to reach the manager as an empty list, so
    /// the Sync Filters screen showed "no filters" while filters existed and a
    /// toggle would have rewritten the file from that picture. The read must
    /// surface as unavailable, and no write may happen on top of it.
    @Test("Unreadable .stignore reads as unavailable, never as an empty list, and no write happens")
    func unreadableIgnoresAreUnavailable() async throws {
        TestSupport.resetSyncthingState()
        let manager = SyncthingManager()
        await manager.start()
        defer {
            manager.stop()
            TestSupport.resetSyncthingState()
        }
        #expect(manager.isRunning)

        let folderID = "fail-closed-\(UUID().uuidString.prefix(8))"
        let folderURL = FileManager.default.temporaryDirectory
            .appendingPathComponent("vaultsync-tests", isDirectory: true)
            .appendingPathComponent(folderID, isDirectory: true)
        try? FileManager.default.removeItem(at: folderURL)
        #expect(manager.addFolder(id: folderID, label: "Fail Closed", path: folderURL.path) == nil)

        // A readable state (no file yet, or defaults) is a list, never nil.
        #expect(manager.ignorePatterns(folderID: folderID) != nil)

        // A directory in place of the file makes the read fail deterministically.
        let ignoreURL = folderURL.appendingPathComponent(".stignore")
        try? FileManager.default.removeItem(at: ignoreURL)
        try FileManager.default.createDirectory(at: ignoreURL, withIntermediateDirectories: false)

        #expect(manager.ignorePatterns(folderID: folderID) == nil, "a failed read must not look like an empty filter list")

        let toggleError = manager.togglePreset(IgnorePreset.recommended[0], folderID: folderID, enabled: true)
        #expect(toggleError != nil, "a toggle over an unreadable filter file must refuse, not write")
        var isDirectory: ObjCBool = false
        let stillDirectory = FileManager.default.fileExists(atPath: ignoreURL.path, isDirectory: &isDirectory) && isDirectory.boolValue
        #expect(stillDirectory, "nothing may replace the unreadable filter file")
    }
}
