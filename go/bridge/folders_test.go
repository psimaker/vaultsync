package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAddRemoveFolder(t *testing.T) {
	configDir := testConfigDir(t)

	// Should fail when not running.
	if errMsg := AddFolder("test", "Test", "/tmp"); errMsg != "syncthing not running" {
		t.Fatalf("AddFolder when stopped = %q, want 'syncthing not running'", errMsg)
	}

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	// Empty ID should fail.
	if errMsg := AddFolder("", "Test", "/tmp"); errMsg != "folder ID is required" {
		t.Fatalf("AddFolder empty ID = %q, want 'folder ID is required'", errMsg)
	}

	// Add a folder.
	folderPath := filepath.Join(configDir, "testfolder")
	if errMsg := AddFolder("test-folder", "Test Folder", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	// Folder path should have been created.
	if _, err := os.Stat(folderPath); os.IsNotExist(err) {
		t.Error("folder path was not created")
	}

	// Duplicate add should fail.
	if errMsg := AddFolder("test-folder", "Dup", folderPath); errMsg != "folder already exists" {
		t.Fatalf("duplicate AddFolder = %q, want 'folder already exists'", errMsg)
	}

	// GetFoldersJSON should return 1 folder.
	foldersJSON := GetFoldersJSON()
	var folders []FolderInfo
	if err := json.Unmarshal([]byte(foldersJSON), &folders); err != nil {
		t.Fatalf("GetFoldersJSON() unmarshal: %v", err)
	}
	if len(folders) != 1 {
		t.Fatalf("GetFoldersJSON() = %d folders, want 1", len(folders))
	}
	if folders[0].ID != "test-folder" {
		t.Errorf("folder ID = %q, want %q", folders[0].ID, "test-folder")
	}
	if folders[0].Label != "Test Folder" {
		t.Errorf("folder label = %q, want %q", folders[0].Label, "Test Folder")
	}
	if folders[0].Path != folderPath {
		t.Errorf("folder path = %q, want %q", folders[0].Path, folderPath)
	}

	// Remove the folder.
	if errMsg := RemoveFolder("test-folder"); errMsg != "" {
		t.Fatalf("RemoveFolder failed: %s", errMsg)
	}

	// Should be back to 0.
	foldersJSON = GetFoldersJSON()
	if err := json.Unmarshal([]byte(foldersJSON), &folders); err != nil {
		t.Fatalf("GetFoldersJSON() unmarshal after remove: %v", err)
	}
	if len(folders) != 0 {
		t.Fatalf("GetFoldersJSON() = %d folders after remove, want 0", len(folders))
	}
}

func TestShareFolderWithDevice(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	// Add a folder and a device.
	folderPath := filepath.Join(configDir, "shared")
	if errMsg := AddFolder("shared", "Shared", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	testDeviceID := "MFZWI3D-BONSGYC-YLTMRWG-C43ENR5-QXGZDMM-FZWI3DP-BONSGYY-LTMRWAD"
	if errMsg := AddDevice(testDeviceID, "Peer"); errMsg != "" {
		t.Fatalf("AddDevice failed: %s", errMsg)
	}

	// Share folder not found.
	if errMsg := ShareFolderWithDevice("nonexistent", testDeviceID); errMsg != "folder not found" {
		t.Fatalf("ShareFolder nonexistent = %q, want 'folder not found'", errMsg)
	}

	// Share folder with device.
	if errMsg := ShareFolderWithDevice("shared", testDeviceID); errMsg != "" {
		t.Fatalf("ShareFolderWithDevice failed: %s", errMsg)
	}

	// Verify device is in folder's device list.
	foldersJSON := GetFoldersJSON()
	var folders []FolderInfo
	if err := json.Unmarshal([]byte(foldersJSON), &folders); err != nil {
		t.Fatalf("GetFoldersJSON() unmarshal after share: %v", err)
	}
	if len(folders) != 1 {
		t.Fatalf("expected 1 folder, got %d", len(folders))
	}
	if len(folders[0].DeviceIDs) != 1 || folders[0].DeviceIDs[0] != testDeviceID {
		t.Errorf("folder deviceIDs = %v, want [%s]", folders[0].DeviceIDs, testDeviceID)
	}

	// Duplicate share should fail.
	if errMsg := ShareFolderWithDevice("shared", testDeviceID); errMsg != "folder already shared with device" {
		t.Fatalf("duplicate share = %q, want 'folder already shared with device'", errMsg)
	}

	// Unshare.
	if errMsg := UnshareFolderFromDevice("shared", testDeviceID); errMsg != "" {
		t.Fatalf("UnshareFolderFromDevice failed: %s", errMsg)
	}

	// Verify device is removed.
	foldersJSON = GetFoldersJSON()
	if err := json.Unmarshal([]byte(foldersJSON), &folders); err != nil {
		t.Fatalf("GetFoldersJSON() unmarshal after unshare: %v", err)
	}
	if len(folders[0].DeviceIDs) != 0 {
		t.Errorf("folder deviceIDs after unshare = %v, want empty", folders[0].DeviceIDs)
	}

	// Cannot unshare own device.
	if errMsg := UnshareFolderFromDevice("shared", DeviceID()); errMsg != "cannot unshare from own device" {
		t.Fatalf("unshare self = %q, want 'cannot unshare from own device'", errMsg)
	}
}

func TestGetFolderStatusJSON(t *testing.T) {
	configDir := testConfigDir(t)

	// Should return empty object when not running.
	if got := GetFolderStatusJSON("test"); got != "{}" {
		t.Fatalf("GetFolderStatusJSON when stopped = %q, want '{}'", got)
	}

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	// Add a folder so we can query its status.
	folderPath := filepath.Join(configDir, "statustest")
	if errMsg := AddFolder("statustest", "Status Test", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	// Get status — should return valid JSON with state field.
	statusJSON := GetFolderStatusJSON("statustest")
	var status FolderStatus
	if err := json.Unmarshal([]byte(statusJSON), &status); err != nil {
		t.Fatalf("GetFolderStatusJSON unmarshal: %v (raw: %s)", err, statusJSON)
	}

	// State should be non-empty (typically "idle" or "scanning" for a new folder).
	if status.State == "" {
		t.Error("folder state is empty")
	}
	t.Logf("Folder state: %s", status.State)
}

func TestFolderIgnores(t *testing.T) {
	configDir := testConfigDir(t)

	// Should return empty array when not running.
	if got := GetFolderIgnores("test"); got != "[]" {
		t.Fatalf("GetFolderIgnores when stopped = %q, want '[]'", got)
	}

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	// Add a folder.
	folderPath := filepath.Join(configDir, "ignoretest")
	if errMsg := AddFolder("ignoretest", "Ignore Test", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	// Set ignores.
	ignores := []string{"*.tmp", ".DS_Store", "*.sync-conflict-*"}
	ignoresJSON, _ := json.Marshal(ignores)
	if errMsg := SetFolderIgnores("ignoretest", string(ignoresJSON)); errMsg != "" {
		t.Fatalf("SetFolderIgnores failed: %s", errMsg)
	}

	// Get ignores back.
	gotJSON := GetFolderIgnores("ignoretest")
	var gotIgnores []string
	if err := json.Unmarshal([]byte(gotJSON), &gotIgnores); err != nil {
		t.Fatalf("GetFolderIgnores unmarshal: %v", err)
	}
	if len(gotIgnores) != len(ignores) {
		t.Fatalf("GetFolderIgnores = %d lines, want %d", len(gotIgnores), len(ignores))
	}
	for i, line := range ignores {
		if gotIgnores[i] != line {
			t.Errorf("ignore line %d = %q, want %q", i, gotIgnores[i], line)
		}
	}

	// Invalid JSON should fail.
	if errMsg := SetFolderIgnores("ignoretest", "not json"); errMsg == "" {
		t.Fatal("SetFolderIgnores with invalid JSON should return error")
	}
}

func TestRescanFolder(t *testing.T) {
	configDir := testConfigDir(t)

	// Should fail when not running.
	if errMsg := RescanFolder("test"); errMsg != "syncthing not running" {
		t.Fatalf("RescanFolder when stopped = %q, want 'syncthing not running'", errMsg)
	}

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	// Add a folder.
	folderPath := filepath.Join(configDir, "rescantest")
	if errMsg := AddFolder("rescantest", "Rescan Test", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	// Rescan should succeed.
	if errMsg := RescanFolder("rescantest"); errMsg != "" {
		t.Fatalf("RescanFolder failed: %s", errMsg)
	}
}

func TestGetFoldersJSONNotRunning(t *testing.T) {
	if got := GetFoldersJSON(); got != "[]" {
		t.Fatalf("GetFoldersJSON when stopped = %q, want '[]'", got)
	}
}
