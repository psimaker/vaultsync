import Foundation
import Testing
@testable import VaultSync

/// A vault's folder error showed the engine's raw path in its text — the
/// app container's path with its ID, which nobody finds in Files and which
/// lands in every screenshot of the card (#378). The card names no path:
/// the vault page's Location row names the place readably (#307) and keeps
/// the full path below it.
@Suite("A folder error card shows no path (#378)")
@MainActor
struct Issue378FolderErrorShowsNoPathTests {
    static let containerID = "5B2E0C4A-9D17-4E6B-A3F1-7C8D2E4F6A1B"
    static let vaultPath = "/private/var/mobile/Containers/Data/Application/\(containerID)/Documents/Push Notifications"

    static let markerMissingText =
        "folder marker missing (this indicates potential data loss, search docs/forum to get information about how to proceed)"

    private func status(reason: String, message: String) -> SyncthingManager.FolderStatusInfo {
        SyncthingManager.FolderStatusInfo(payload: .init(
            state: "error",
            stateChanged: "2026-11-18T20:00:00Z",
            completionPct: 0,
            globalBytes: 0,
            globalFiles: 0,
            localBytes: 0,
            localFiles: 0,
            needBytes: 0,
            needFiles: 0,
            inProgressBytes: 0,
            errorReason: reason,
            errorMessage: message,
            errorPath: Self.vaultPath,
            errorChanged: nil
        ))
    }

    private func error(reason: String, message: String) throws -> SyncUserError {
        let manager = SyncthingManager()
        manager._testSetFolderStatuses(["vault-a": status(reason: reason, message: message)])
        return try #require(manager.folderUserError(folderID: "vault-a"))
    }

    private func expectNoPath(_ error: SyncUserError) {
        for text in [error.title, error.message, error.remediation, error.userVisibleDescription] {
            #expect(!text.contains(Self.vaultPath))
            #expect(!text.contains(Self.containerID))
            #expect(!text.contains("/private/var"))
            #expect(!text.contains("Push Notifications"))
        }
    }

    @Test("Folder Path Missing says that the folder is gone, without its path")
    func folderPathMissing() throws {
        let error = try error(reason: "folder_path_missing", message: "folder path missing")
        #expect(error.title == L10n.tr("Folder Path Missing"))
        #expect(error.message == L10n.tr("The folder path no longer exists."))
        #expect(error.remediation == SyncUserError.vaultFolderGoneRemediation)
        // Where the original place is shown, also on the Sync tab, which has
        // no Location row.
        #expect(error.remediation.contains("That place is shown under Location on the vault's page."))
        #expect(error.technicalDetails == "folder path missing")
        expectNoPath(error)
    }

    @Test("Folder Permission Error says that the folder cannot be read, without its path")
    func permissionDenied() throws {
        let error = try error(reason: "permission_denied", message: "permission denied")
        #expect(error.title == L10n.tr("Folder Permission Error"))
        #expect(error.message == L10n.tr("VaultSync cannot access this folder."))
        #expect(error.technicalDetails == "permission denied")
        expectNoPath(error)
    }

    @Test("Vault Folder Was Moved or Deleted explains itself without the path")
    func markerMissing() throws {
        let error = try error(reason: "unknown_error", message: Self.markerMissingText)
        #expect(error.category == .folderMarkerMissing)
        #expect(error.message == L10n.tr("VaultSync can no longer verify that this folder still holds this vault's data — the folder was likely moved, renamed, replaced, or deleted outside VaultSync. Syncing has stopped to protect your notes."))
        #expect(error.technicalDetails == Self.markerMissingText)
        expectNoPath(error)
    }

    /// The troubleshooting link is chosen by words in the error's message,
    /// its way on and its technical details. With the path in the message,
    /// the card's own text named the vault, so its name could choose the
    /// section. This pins that the card's own text no longer does.
    @Test("The card's own text no longer steers the troubleshooting link")
    func cardTextDoesNotSteerTheLink() throws {
        let error = try error(reason: "permission_denied", message: "permission denied")
        let url = try #require(SyncUserError.troubleshootingURL(for: error))
        #expect(url.absoluteString.hasSuffix("#bookmark-access-expired"))
    }

    /// The engine's own text for a folder it may not read names the full
    /// path, and the link reads it in the technical details: a vault named
    /// "Push Notifications" still gets the push registration help. This
    /// pins today's wrong section and turns red when #474 fixes it.
    @Test("The engine's own text still steers the link: today's wrong section, until #474")
    func engineTextStillSteersTheLink() throws {
        let error = try error(
            reason: "permission_denied",
            message: "stat \(Self.vaultPath): operation not permitted"
        )
        expectNoPath(error)
        let url = try #require(SyncUserError.troubleshootingURL(for: error))
        #expect(url.absoluteString.hasSuffix("#apns-not-registered"), "#474: \(url.absoluteString)")
    }
}
