import Foundation
import SyncBridge

/// Swift wrapper around the gomobile-generated Go bridge.
/// Provides a clean Swift API for all bridge functions.
struct SyncBridgeService {
    struct FolderStatusPayload: Codable {
        let state: String
        let stateChanged: String
        let completionPct: Double
        let globalBytes: Int64
        let globalFiles: Int
        let localBytes: Int64
        let localFiles: Int
        let needBytes: Int64
        let needFiles: Int
        let inProgressBytes: Int64
        let errorReason: String?
        let errorMessage: String?
        let errorPath: String?
        let errorChanged: String?
    }

    // MARK: - Phase 1: Basic bridge info

    /// Verify the bridge is loaded and callable.
    static func ping() -> String {
        BridgePing()
    }

    /// Go runtime version used to build the bridge.
    static func goVersion() -> String {
        BridgeVersion()
    }

    /// Architecture the bridge was compiled for.
    static func arch() -> String {
        BridgeArch()
    }

    /// Version of the embedded Syncthing library.
    static func syncthingVersion() -> String {
        BridgeSyncthingVersion()
    }

    // MARK: - Phase 3: Syncthing lifecycle

    /// Start the embedded Syncthing instance.
    /// - Parameter configDir: Base directory for config, certs, and database.
    /// - Returns: nil on success, error message on failure.
    static func startSyncthing(configDir: String) -> String? {
        let result = BridgeStartSyncthing(configDir)
        return result.isEmpty ? nil : result
    }

    /// Stop the running Syncthing instance.
    static func stopSyncthing() {
        BridgeStopSyncthing()
    }

    /// Whether Syncthing is currently running.
    static func isRunning() -> Bool {
        BridgeIsRunning()
    }

    /// This device's ID in canonical format (e.g. XXXXXXX-XXXXXXX-...).
    static func deviceID() -> String {
        BridgeDeviceID()
    }

    // MARK: - Phase 3: Device management

    /// Add a peer device.
    /// - Returns: nil on success, error message on failure.
    static func addDevice(deviceID: String, name: String) -> String? {
        let result = BridgeAddDevice(deviceID, name)
        return result.isEmpty ? nil : result
    }

    /// Remove a peer device.
    /// - Returns: nil on success, error message on failure.
    static func removeDevice(deviceID: String) -> String? {
        let result = BridgeRemoveDevice(deviceID)
        return result.isEmpty ? nil : result
    }

    /// Get all configured devices as JSON.
    static func getDevicesJSON() -> String {
        BridgeGetDevicesJSON()
    }

    // MARK: - Phase 3: Status & events

    /// Get connection status of all devices as JSON.
    static func getConnectionsJSON() -> String {
        BridgeGetConnectionsJSON()
    }

    /// Get events since the given event ID as JSON.
    static func getEventsSince(lastID: Int) -> String {
        BridgeGetEventsSince(lastID)
    }

    /// Get current Syncthing configuration as JSON.
    static func getConfigJSON() -> String {
        BridgeGetConfigJSON()
    }

    /// Toggle local and global discovery.
    /// - Returns: nil on success, error message on failure.
    static func setDiscoveryEnabled(local: Bool, global: Bool) -> String? {
        let result = BridgeSetDiscoveryEnabled(local, global)
        return result.isEmpty ? nil : result
    }

    // MARK: - Phase 4: Folder management

    /// Add a new folder with SendReceive type.
    /// - Returns: nil on success, error message on failure.
    static func addFolder(id: String, label: String, path: String) -> String? {
        let result = BridgeAddFolder(id, label, path)
        return result.isEmpty ? nil : result
    }

    /// Remove a folder by ID.
    /// - Returns: nil on success, error message on failure.
    static func removeFolder(id: String) -> String? {
        let result = BridgeRemoveFolder(id)
        return result.isEmpty ? nil : result
    }

    /// Get all configured folders as JSON.
    static func getFoldersJSON() -> String {
        BridgeGetFoldersJSON()
    }

    /// Share a folder with a peer device.
    /// - Returns: nil on success, error message on failure.
    static func shareFolderWithDevice(folderID: String, deviceID: String) -> String? {
        let result = BridgeShareFolderWithDevice(folderID, deviceID)
        return result.isEmpty ? nil : result
    }

    /// Remove a device from a folder's share list.
    /// - Returns: nil on success, error message on failure.
    static func unshareFolderFromDevice(folderID: String, deviceID: String) -> String? {
        let result = BridgeUnshareFolderFromDevice(folderID, deviceID)
        return result.isEmpty ? nil : result
    }

    // MARK: - Phase 4: Folder status & ignores

    /// Get the sync status of a folder as JSON.
    static func getFolderStatusJSON(folderID: String) -> String {
        BridgeGetFolderStatusJSON(folderID)
    }

    /// Get the sync status of a folder decoded into a typed payload.
    static func getFolderStatus(folderID: String) -> FolderStatusPayload? {
        let json = BridgeGetFolderStatusJSON(folderID)
        guard let data = json.data(using: .utf8) else { return nil }
        return try? JSONDecoder().decode(FolderStatusPayload.self, from: data)
    }

    /// Get .stignore lines for a folder as JSON array.
    static func getFolderIgnores(folderID: String) -> String {
        BridgeGetFolderIgnores(folderID)
    }

    /// Set .stignore lines for a folder. ignoresJSON is a JSON array of strings.
    /// - Returns: nil on success, error message on failure.
    static func setFolderIgnores(folderID: String, ignoresJSON: String) -> String? {
        let result = BridgeSetFolderIgnores(folderID, ignoresJSON)
        return result.isEmpty ? nil : result
    }

    /// Trigger a rescan of all files in a folder.
    /// - Returns: nil on success, error message on failure.
    static func rescanFolder(folderID: String) -> String? {
        let result = BridgeRescanFolder(folderID)
        return result.isEmpty ? nil : result
    }

    // MARK: - Phase 6: Conflict management

    /// Get all conflict files in a folder as JSON.
    static func getConflictFilesJSON(folderID: String) -> String {
        BridgeGetConflictFilesJSON(folderID)
    }

    /// Read a text file's content within a folder. relPath is relative to the folder root.
    /// Returns `(content, nil)` on success, or `(nil, errorMessage)` on failure.
    static func readFileContent(folderID: String, relPath: String) -> (content: String?, error: String?) {
        let result = BridgeReadFileContent(folderID, relPath)
        if result.hasPrefix("error:") {
            return (nil, String(result.dropFirst(6)))
        }
        return (result, nil)
    }

    /// Resolve a sync conflict. If keepConflict is true, the conflict version replaces the original.
    /// - Returns: nil on success, error message on failure.
    static func resolveConflict(folderID: String, conflictFileName: String, keepConflict: Bool) -> String? {
        let result = BridgeResolveConflict(folderID, conflictFileName, keepConflict)
        return result.isEmpty ? nil : result
    }

    /// Keep both versions by renaming the conflict file to a non-conflict name.
    /// - Returns: nil on success, error message on failure.
    static func keepBothConflict(folderID: String, conflictFileName: String) -> String? {
        let result = BridgeKeepBothConflict(folderID, conflictFileName)
        return result.isEmpty ? nil : result
    }

    // MARK: - Pending folder shares

    /// Get all pending folder offers from remote devices as JSON.
    static func getPendingFoldersJSON() -> String {
        BridgeGetPendingFoldersJSON()
    }

    /// Accept a pending folder offer by creating it locally and sharing with offering devices.
    /// - Returns: nil on success, error message on failure.
    static func acceptPendingFolder(folderID: String, label: String, path: String) -> String? {
        let result = BridgeAcceptPendingFolder(folderID, label, path)
        return result.isEmpty ? nil : result
    }

    // MARK: - Phase 6: Device rename

    /// Rename a peer device.
    /// - Returns: nil on success, error message on failure.
    static func renameDevice(deviceID: String, newName: String) -> String? {
        let result = BridgeRenameDevice(deviceID, newName)
        return result.isEmpty ? nil : result
    }
}
