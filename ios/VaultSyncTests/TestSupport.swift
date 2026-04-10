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
            "relay-diagnostics-last-error",
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

    private static func syncthingConfigDirectory() -> URL {
        let documentsURL = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask).first!
        return documentsURL.appendingPathComponent("syncthing", isDirectory: true)
    }
}
