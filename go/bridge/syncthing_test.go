package bridge

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// testConfigDir creates a temporary config directory that won't fail
// on cleanup if Syncthing's database files are still being flushed.
func testConfigDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "vaultsync-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		StopSyncthing()
		// Allow Syncthing's async config flush to complete before
		// removing the temp directory — avoids "rename config.xml"
		// log noise during test teardown.
		time.Sleep(100 * time.Millisecond)
		os.RemoveAll(dir)
	})
	return dir
}

func TestStartStopSyncthing(t *testing.T) {
	configDir := testConfigDir(t)

	// Should not be running initially.
	if IsRunning() {
		t.Fatal("IsRunning() = true before start")
	}

	// DeviceID should be empty when not running.
	if id := DeviceID(); id != "" {
		t.Fatalf("DeviceID() = %q before start, want empty", id)
	}

	// Start Syncthing.
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}

	if !IsRunning() {
		t.Fatal("IsRunning() = false after start")
	}

	// Starting again should return error.
	if errMsg := StartSyncthing(configDir); errMsg != "already running" {
		t.Fatalf("second StartSyncthing() = %q, want %q", errMsg, "already running")
	}

	// Stop Syncthing.
	StopSyncthing()

	if IsRunning() {
		t.Fatal("IsRunning() = true after stop")
	}
}

func TestDeviceIDFormat(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	id := DeviceID()
	if id == "" {
		t.Fatal("DeviceID() returned empty string")
	}

	// Canonical format: 7 groups of 7 chars separated by dashes (52 chars + 7 dashes = 63 total with separators).
	// Actually format is XXXXXXX-XXXXXXX-XXXXXXX-XXXXXXX-XXXXXXX-XXXXXXX-XXXXXXX-XXXXXXX (8 groups of 7).
	parts := strings.Split(id, "-")
	if len(parts) != 8 {
		t.Fatalf("DeviceID() = %q, want 8 groups separated by dashes, got %d groups", id, len(parts))
	}
	for i, part := range parts {
		if len(part) != 7 {
			t.Errorf("DeviceID group %d = %q, want 7 chars", i, part)
		}
	}
	t.Logf("DeviceID() = %s", id)
}

func TestDeviceIDPersistence(t *testing.T) {
	configDir := testConfigDir(t)

	// Start, get ID, stop.
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	id1 := DeviceID()
	StopSyncthing()

	// Start again with same configDir — ID should be the same.
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("second StartSyncthing() failed: %s", errMsg)
	}
	id2 := DeviceID()
	StopSyncthing()

	if id1 != id2 {
		t.Errorf("DeviceID changed across restarts: %q vs %q", id1, id2)
	}
}

func TestCertificatePersisted(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	StopSyncthing()

	// cert.pem and key.pem should exist in configDir.
	for _, name := range []string{"cert.pem", "key.pem"} {
		path := configDir + "/" + name
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected %s to exist in configDir", name)
		}
	}
}

func TestAddRemoveDevice(t *testing.T) {
	configDir := testConfigDir(t)

	// Should fail when not running.
	if errMsg := AddDevice("invalid", "test"); errMsg != "syncthing not running" {
		t.Fatalf("AddDevice when stopped = %q, want 'syncthing not running'", errMsg)
	}

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	// Invalid device ID.
	if errMsg := AddDevice("not-a-device-id", "test"); errMsg == "" {
		t.Fatal("AddDevice with invalid ID should return error")
	}

	// Cannot add own device ID.
	if errMsg := AddDevice(DeviceID(), "self"); errMsg != "cannot add own device ID" {
		t.Fatalf("AddDevice own ID = %q, want 'cannot add own device ID'", errMsg)
	}

	// GetDevicesJSON should return empty array (only self in config).
	devicesJSON := GetDevicesJSON()
	var devices []DeviceInfo
	if err := json.Unmarshal([]byte(devicesJSON), &devices); err != nil {
		t.Fatalf("GetDevicesJSON() unmarshal: %v", err)
	}
	if len(devices) != 0 {
		t.Fatalf("GetDevicesJSON() = %d devices, want 0", len(devices))
	}

	// Add a valid foreign device.
	// This is a syntactically valid device ID (passes Luhn check).
	testDeviceID := "MFZWI3D-BONSGYC-YLTMRWG-C43ENR5-QXGZDMM-FZWI3DP-BONSGYY-LTMRWAD"
	if errMsg := AddDevice(testDeviceID, "TestPeer"); errMsg != "" {
		t.Fatalf("AddDevice valid ID failed: %s", errMsg)
	}

	// Should now have 1 device.
	devicesJSON = GetDevicesJSON()
	if err := json.Unmarshal([]byte(devicesJSON), &devices); err != nil {
		t.Fatalf("GetDevicesJSON() unmarshal after add: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("GetDevicesJSON() = %d devices after add, want 1", len(devices))
	}
	if devices[0].Name != "TestPeer" {
		t.Errorf("device name = %q, want %q", devices[0].Name, "TestPeer")
	}

	// Duplicate add should fail.
	if errMsg := AddDevice(testDeviceID, "Dup"); errMsg != "device already exists" {
		t.Fatalf("duplicate AddDevice = %q, want 'device already exists'", errMsg)
	}

	// Remove the device.
	if errMsg := RemoveDevice(testDeviceID); errMsg != "" {
		t.Fatalf("RemoveDevice failed: %s", errMsg)
	}

	// Should be back to 0.
	devicesJSON = GetDevicesJSON()
	if err := json.Unmarshal([]byte(devicesJSON), &devices); err != nil {
		t.Fatalf("GetDevicesJSON() unmarshal after remove: %v", err)
	}
	if len(devices) != 0 {
		t.Fatalf("GetDevicesJSON() = %d devices after remove, want 0", len(devices))
	}
}

func TestGetConnectionsJSON(t *testing.T) {
	configDir := testConfigDir(t)

	// Empty when not running.
	if got := GetConnectionsJSON(); got != "[]" {
		t.Fatalf("GetConnectionsJSON() when stopped = %q, want '[]'", got)
	}

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	// Should return valid JSON array.
	connsJSON := GetConnectionsJSON()
	var conns []DeviceInfo
	if err := json.Unmarshal([]byte(connsJSON), &conns); err != nil {
		t.Fatalf("GetConnectionsJSON() unmarshal: %v", err)
	}
}

func TestGetConfigJSON(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	cfgJSON := GetConfigJSON()
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(cfgJSON), &cfg); err != nil {
		t.Fatalf("GetConfigJSON() unmarshal: %v", err)
	}

	// Verify GUI is disabled.
	gui, ok := cfg["gui"].(map[string]interface{})
	if !ok {
		t.Fatal("config missing 'gui' section")
	}
	if enabled, ok := gui["enabled"].(bool); !ok || enabled {
		t.Error("GUI should be disabled in embedded config")
	}
}

func TestDiscoverySettings(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	// Toggle discovery off.
	if errMsg := SetDiscoveryEnabled(false, false); errMsg != "" {
		t.Fatalf("SetDiscoveryEnabled(false, false) = %q", errMsg)
	}

	// Verify via config.
	cfgJSON := GetConfigJSON()
	var cfg map[string]interface{}
	json.Unmarshal([]byte(cfgJSON), &cfg)
	opts, _ := cfg["options"].(map[string]interface{})
	if local, _ := opts["localAnnounceEnabled"].(bool); local {
		t.Error("local discovery should be disabled")
	}
	if global, _ := opts["globalAnnounceEnabled"].(bool); global {
		t.Error("global discovery should be disabled")
	}

	// Toggle back on.
	if errMsg := SetDiscoveryEnabled(true, true); errMsg != "" {
		t.Fatalf("SetDiscoveryEnabled(true, true) = %q", errMsg)
	}
}
