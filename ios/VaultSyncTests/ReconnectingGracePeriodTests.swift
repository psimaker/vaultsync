import XCTest
@testable import VaultSync

@MainActor
final class ReconnectingGracePeriodTests: XCTestCase {

    // MARK: - Helpers

    /// Wraps a SyncthingManager + a manually-advanceable clock.
    @MainActor
    private final class Harness {
        let manager: SyncthingManager
        var clock: Date

        init(now: Date = Date(timeIntervalSince1970: 1_000_000)) {
            self.clock = now
            self.manager = SyncthingManager()
            self.manager.now = { [weak self] in self?.clock ?? Date() }
        }

        func advance(_ seconds: TimeInterval) {
            clock = clock.addingTimeInterval(seconds)
        }
    }

    private func makeFolder(id: String, deviceIDs: [String]) -> SyncthingManager.FolderInfo {
        // FolderInfo uses default Codable, so every stored field must appear.
        // Build via JSON to avoid depending on an internal initializer.
        let idsJSON = "[" + deviceIDs.map { "\"\($0)\"" }.joined(separator: ",") + "]"
        let json = """
        {
          "id": "\(id)",
          "label": "Test",
          "path": "/tmp/\(id)",
          "type": "sendreceive",
          "paused": false,
          "deviceIDs": \(idsJSON)
        }
        """
        let data = Data(json.utf8)
        return try! JSONDecoder().decode(SyncthingManager.FolderInfo.self, from: data)
    }

    private func makeDevice(id: String, connected: Bool) -> SyncthingManager.DeviceInfo {
        // DeviceInfo.paused uses decodeIfPresent so we can omit it; provide
        // the three required fields. Custom init does the actual decoding.
        let json = """
        {
          "deviceID": "\(id)",
          "name": "Device-\(id.prefix(4))",
          "connected": \(connected)
        }
        """
        let data = Data(json.utf8)
        return try! JSONDecoder().decode(SyncthingManager.DeviceInfo.self, from: data)
    }

    // MARK: - Tests

    func test_freshDisconnect_appearsInReconnectingOnly() {
        let h = Harness()
        h.manager._testSetFolders([makeFolder(id: "f1", deviceIDs: ["DEVICE-A"])])
        h.manager._testApplyDeviceList([makeDevice(id: "DEVICE-A", connected: false)])

        XCTAssertEqual(h.manager.reconnectingRequiredDeviceIDs, ["DEVICE-A"])
        XCTAssertTrue(h.manager.disconnectedRequiredDeviceIDs.isEmpty)
    }

    func test_disconnectAged30s_movesToDisconnected() {
        let h = Harness()
        h.manager._testSetFolders([makeFolder(id: "f1", deviceIDs: ["DEVICE-A"])])
        h.manager._testApplyDeviceList([makeDevice(id: "DEVICE-A", connected: false)])

        h.advance(31)
        // No new device-list update — only the clock moved.

        XCTAssertTrue(h.manager.reconnectingRequiredDeviceIDs.isEmpty)
        XCTAssertEqual(h.manager.disconnectedRequiredDeviceIDs, ["DEVICE-A"])
    }

    func test_reconnectWithinWindow_clearsBothLists() {
        let h = Harness()
        h.manager._testSetFolders([makeFolder(id: "f1", deviceIDs: ["DEVICE-A"])])
        h.manager._testApplyDeviceList([makeDevice(id: "DEVICE-A", connected: false)])
        h.advance(5)
        h.manager._testApplyDeviceList([makeDevice(id: "DEVICE-A", connected: true)])

        XCTAssertTrue(h.manager.reconnectingRequiredDeviceIDs.isEmpty)
        XCTAssertTrue(h.manager.disconnectedRequiredDeviceIDs.isEmpty)
    }

    func test_deviceVanishes_isDroppedFromTracking() {
        let h = Harness()
        h.manager._testSetFolders([makeFolder(id: "f1", deviceIDs: ["DEVICE-A", "DEVICE-B"])])
        h.manager._testApplyDeviceList([
            makeDevice(id: "DEVICE-A", connected: false),
            makeDevice(id: "DEVICE-B", connected: false),
        ])

        // Drop DEVICE-A from the device list entirely (e.g. removed from config).
        // Folder still requires both — but A is now "unresolved unknown".
        h.manager._testApplyDeviceList([
            makeDevice(id: "DEVICE-B", connected: false),
        ])

        // DEVICE-A should appear in disconnected (unresolved unknown), not in reconnecting.
        XCTAssertEqual(h.manager.reconnectingRequiredDeviceIDs, ["DEVICE-B"])
        XCTAssertEqual(h.manager.disconnectedRequiredDeviceIDs, ["DEVICE-A"])
    }

    func test_requiredButUnknownDevice_skipsGracePeriod() {
        let h = Harness()
        // Folder requires DEVICE-X but the device list never includes it.
        h.manager._testSetFolders([makeFolder(id: "f1", deviceIDs: ["DEVICE-X"])])
        h.manager._testApplyDeviceList([])

        XCTAssertTrue(h.manager.reconnectingRequiredDeviceIDs.isEmpty)
        XCTAssertEqual(h.manager.disconnectedRequiredDeviceIDs, ["DEVICE-X"])
    }

    func test_nonRequiredDisconnect_isIgnored() {
        let h = Harness()
        h.manager._testSetFolders([])  // no folders → no required devices
        h.manager._testApplyDeviceList([makeDevice(id: "DEVICE-A", connected: false)])

        XCTAssertTrue(h.manager.reconnectingRequiredDeviceIDs.isEmpty)
        XCTAssertTrue(h.manager.disconnectedRequiredDeviceIDs.isEmpty)
    }

    func test_flipFlop_resetsTimestamp() {
        let h = Harness()
        h.manager._testSetFolders([makeFolder(id: "f1", deviceIDs: ["DEVICE-A"])])
        h.manager._testApplyDeviceList([makeDevice(id: "DEVICE-A", connected: false)])
        h.advance(20)
        h.manager._testApplyDeviceList([makeDevice(id: "DEVICE-A", connected: true)])
        h.advance(5)
        h.manager._testApplyDeviceList([makeDevice(id: "DEVICE-A", connected: false)])

        // The second disconnect gets a fresh 30s window.
        XCTAssertEqual(h.manager.reconnectingRequiredDeviceIDs, ["DEVICE-A"])
        XCTAssertTrue(h.manager.disconnectedRequiredDeviceIDs.isEmpty)
    }

    // MARK: - Startup grace period

    func test_coldStartDisconnect_getsStartupGrace() {
        let h = Harness()
        h.manager._testSetEngineStartedAt(h.clock)
        h.manager._testSetFolders([makeFolder(id: "f1", deviceIDs: ["DEVICE-A"])])
        // First poll observes the disconnect 2s after engine start.
        h.advance(2)
        h.manager._testApplyDeviceList([makeDevice(id: "DEVICE-A", connected: false)])

        // 45s after engine start: past the 30s mid-session window, but still
        // inside the 60s startup grace — stays calm.
        h.advance(43)
        XCTAssertEqual(h.manager.reconnectingRequiredDeviceIDs, ["DEVICE-A"])
        XCTAssertTrue(h.manager.disconnectedRequiredDeviceIDs.isEmpty)

        // 61s after engine start: startup grace expired.
        h.advance(16)
        XCTAssertTrue(h.manager.reconnectingRequiredDeviceIDs.isEmpty)
        XCTAssertEqual(h.manager.disconnectedRequiredDeviceIDs, ["DEVICE-A"])
    }

    func test_midSessionDisconnect_keepsShortGrace() {
        let h = Harness()
        h.manager._testSetEngineStartedAt(h.clock)
        h.manager._testSetFolders([makeFolder(id: "f1", deviceIDs: ["DEVICE-A"])])

        // Disconnect observed well after the startup window → 30s rule.
        h.advance(120)
        h.manager._testApplyDeviceList([makeDevice(id: "DEVICE-A", connected: false)])

        h.advance(31)
        XCTAssertTrue(h.manager.reconnectingRequiredDeviceIDs.isEmpty)
        XCTAssertEqual(h.manager.disconnectedRequiredDeviceIDs, ["DEVICE-A"])
    }

    func test_isWithinReconnectGrace_coversNonRequiredDevices() {
        let h = Harness()
        // No folders → DEVICE-A is not required, but the Devices tab still
        // shows the calm connecting state through the per-device API.
        h.manager._testSetFolders([])
        h.manager._testApplyDeviceList([makeDevice(id: "DEVICE-A", connected: false)])

        XCTAssertTrue(h.manager.isWithinReconnectGrace(deviceID: "DEVICE-A"))

        h.advance(31)
        XCTAssertFalse(h.manager.isWithinReconnectGrace(deviceID: "DEVICE-A"))

        // Unknown devices are never "connecting".
        XCTAssertFalse(h.manager.isWithinReconnectGrace(deviceID: "DEVICE-B"))
    }

    func test_connectedDevice_isNotWithinGrace() {
        let h = Harness()
        h.manager._testSetFolders([makeFolder(id: "f1", deviceIDs: ["DEVICE-A"])])
        h.manager._testApplyDeviceList([makeDevice(id: "DEVICE-A", connected: true)])

        XCTAssertFalse(h.manager.isWithinReconnectGrace(deviceID: "DEVICE-A"))
    }
}
