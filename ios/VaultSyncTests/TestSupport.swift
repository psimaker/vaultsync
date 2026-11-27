import CryptoKit
import Foundation
@testable import VaultSync

enum TestSupport {
    static let samplePeerDeviceID = "MFZWI3D-BONSGYC-YLTMRWG-C43ENR5-QXGZDMM-FZWI3DP-BONSGYY-LTMRWAD"

    @MainActor
    static func resetSyncthingState() {
        if SyncBridgeService.isRunning() {
            SyncBridgeService.stopSyncthing()
        }

        let configDirectory = syncthingConfigDirectory()
        try? FileManager.default.removeItem(at: configDirectory)

        let defaults = UserDefaults.standard
        defaults.removeObject(forKey: "syncthing.ignoredPendingFolderIDs")
        defaults.removeObject(forKey: "syncthing.hasSeenPendingFolderOffer")
        defaults.removeObject(forKey: "background-sync-last-outcome-v1")
        defaults.removeObject(forKey: "background-sync-last-outcome-v2")
    }

    static func resetRelayState() {
        let defaults = UserDefaults.standard
        for key in [
            "apns-registration-status",
            "apns-registration-failure-reason",
            "apns-registration-updated-at",
            "apns-registration-last-success-at",
            "apns-registration-last-failure-at",
            "relay-last-trigger-received-at",
            "relay-trigger-received-history-v1",
            "relay-background-sync-started-at",
            "relay-sync-progress-observed-at",
            "relay-local-data-progress-observed-at-v2",
            "relay-diagnostics-last-error",
            "relay-diagnostics-last-error-v2",
            "relay-last-provision-date",
            "relay-provisioned-device-ids",
            "relay-provision-statuses-v2",
        ] {
            defaults.removeObject(forKey: key)
        }

        _ = KeychainService.clearAPNsDeviceToken()
        _ = KeychainService.delete(key: "relay-device-ids")
    }

    static func makeIsolatedDefaults(label: String) -> UserDefaults {
        let suiteName = "VaultSyncTests.\(label).\(UUID().uuidString)"
        guard let defaults = UserDefaults(suiteName: suiteName) else {
            fatalError("Could not create isolated UserDefaults suite \(suiteName)")
        }
        defaults.removePersistentDomain(forName: suiteName)
        return defaults
    }

    /// The file in a folder's `.stfolder` the engine writes to mark the
    /// directory as the folder's, named after the folder's ID: what the
    /// bridge reads the folder's directory by (#315).
    static func folderMarkerFile(folderID: String, in directory: URL) -> URL {
        let hash = SHA256.hash(data: Data(folderID.utf8)).prefix(3).map { String(format: "%02x", $0) }.joined()
        return directory
            .appendingPathComponent(".stfolder", isDirectory: true)
            .appendingPathComponent("syncthing-folder-\(hash).txt")
    }

    /// Waits until the engine has started a folder a test has just added:
    /// its directory holds the folder's marker. Returns false if that takes
    /// longer than `timeout`.
    ///
    /// The add returns once every part of the engine that follows the
    /// configuration has taken the change, and the part that starts folders
    /// makes the marker as it does. Right after an engine start that part may
    /// not follow the configuration yet; it then starts the folder a moment
    /// later, from its first look at the configuration. Until then a write of
    /// the folder's filters is refused as "marker missing" (#398, #225), and
    /// the add has written no record of the directory the folder syncs in
    /// (#373). A test that acts on a folder right after adding it waits here
    /// first.
    static func waitForFolderMarker(
        folderID: String,
        in directory: URL,
        timeout: Duration = .seconds(10)
    ) async -> Bool {
        let marker = folderMarkerFile(folderID: folderID, in: directory).path
        let clock = ContinuousClock()
        let deadline = clock.now.advanced(by: timeout)
        while !FileManager.default.fileExists(atPath: marker) {
            guard clock.now < deadline else { return false }
            try? await Task.sleep(for: .milliseconds(5))
        }
        return true
    }

    /// The config directory the live app uses (`SyncthingManager.configDirectory()`
    /// and `BackgroundSyncService.syncthingConfigDir()` derive the same path).
    /// Exposed so bridge-state tests can start a real engine exactly the way a
    /// background handler does (#61).
    static func syncthingConfigPath() -> String {
        syncthingConfigDirectory().path
    }

    private static func syncthingConfigDirectory() -> URL {
        let documentsURL = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask).first!
        return documentsURL.appendingPathComponent("syncthing", isDirectory: true)
    }

    struct LocalizedStringsUnreadable: Error {
        let language: String
    }

    /// The app's `Localizable.strings` for one language (`en`, `de`, `es`,
    /// `zh-Hans`), read from the source tree beside this file — key to text.
    /// For tests that hold a text to what it must say in every language.
    static func localizedStrings(_ language: String) throws -> [String: String] {
        let url = URL(fileURLWithPath: "\(#filePath)")
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .appendingPathComponent("VaultSync", isDirectory: true)
            .appendingPathComponent("\(language).lproj", isDirectory: true)
            .appendingPathComponent("Localizable.strings")
        let data = try Data(contentsOf: url)
        let list = try PropertyListSerialization.propertyList(from: data, format: nil)
        guard let table = list as? [String: String] else {
            throw LocalizedStringsUnreadable(language: language)
        }
        return table
    }
}
