package bridge

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
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

func TestSetFolderPath(t *testing.T) {
	configDir := testConfigDir(t)

	// Not running.
	if errMsg := SetFolderPath("x", "/tmp"); errMsg != "syncthing not running" {
		t.Fatalf("SetFolderPath when stopped = %q, want 'syncthing not running'", errMsg)
	}

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	pathA := filepath.Join(configDir, "vaultA")
	if errMsg := AddFolder("pathtest", "Path Test", pathA); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	// Share with a device so we can assert the share survives the path change.
	testDeviceID := "MFZWI3D-BONSGYC-YLTMRWG-C43ENR5-QXGZDMM-FZWI3DP-BONSGYY-LTMRWAD"
	if errMsg := AddDevice(testDeviceID, "Peer"); errMsg != "" {
		t.Fatalf("AddDevice failed: %s", errMsg)
	}
	if errMsg := ShareFolderWithDevice("pathtest", testDeviceID); errMsg != "" {
		t.Fatalf("ShareFolderWithDevice failed: %s", errMsg)
	}

	// Unknown folder.
	if errMsg := SetFolderPath("nope", pathA); errMsg != "folder not found" {
		t.Fatalf("SetFolderPath unknown = %q, want 'folder not found'", errMsg)
	}

	// No-op when the path is unchanged.
	if errMsg := SetFolderPath("pathtest", pathA); errMsg != "" {
		t.Fatalf("SetFolderPath no-op = %q, want ''", errMsg)
	}

	// Non-existent target is refused (would otherwise risk marker loss / deletions).
	missing := filepath.Join(configDir, "does-not-exist")
	if errMsg := SetFolderPath("pathtest", missing); errMsg != "target path does not exist" {
		t.Fatalf("SetFolderPath missing target = %q, want 'target path does not exist'", errMsg)
	}

	// A file (not a directory) target is refused.
	filePath := filepath.Join(configDir, "afile")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if errMsg := SetFolderPath("pathtest", filePath); errMsg != "target path is not a directory" {
		t.Fatalf("SetFolderPath file target = %q, want 'target path is not a directory'", errMsg)
	}

	// An existing but empty directory is refused: it lacks this folder's marker,
	// and pointing a send-receive folder there would propagate deletions.
	emptyDir := filepath.Join(configDir, "emptyVault")
	if err := os.MkdirAll(emptyDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if errMsg := SetFolderPath("pathtest", emptyDir); errMsg != "target does not contain this folder's data (marker missing)" {
		t.Fatalf("SetFolderPath empty target = %q, want marker-missing refusal", errMsg)
	}

	// A directory whose marker belongs to a DIFFERENT folder is also refused.
	foreignDir := filepath.Join(configDir, "foreignVault")
	writeFolderMarker(t, foreignDir, "some-other-folder")
	if errMsg := SetFolderPath("pathtest", foreignDir); errMsg != "target does not contain this folder's data (marker missing)" {
		t.Fatalf("SetFolderPath foreign target = %q, want marker-missing refusal", errMsg)
	}

	// Valid in-place rebase to a directory that holds THIS folder's data
	// (marker with this folder's fingerprint present).
	pathB := filepath.Join(configDir, "vaultB")
	writeFolderMarker(t, pathB, "pathtest")
	if errMsg := SetFolderPath("pathtest", pathB); errMsg != "" {
		t.Fatalf("SetFolderPath valid rebase = %q, want ''", errMsg)
	}

	// Verify the path changed and the device share survived.
	var folders []FolderInfo
	if err := json.Unmarshal([]byte(GetFoldersJSON()), &folders); err != nil {
		t.Fatalf("GetFoldersJSON unmarshal: %v", err)
	}
	if len(folders) != 1 {
		t.Fatalf("got %d folders, want 1", len(folders))
	}
	if folders[0].Path != pathB {
		t.Errorf("path = %q, want %q", folders[0].Path, pathB)
	}
	if len(folders[0].DeviceIDs) != 1 || folders[0].DeviceIDs[0] != testDeviceID {
		t.Errorf("deviceIDs = %v, want [%s] (share must survive path change)", folders[0].DeviceIDs, testDeviceID)
	}
}

func TestEnsureDefaultIgnores(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	folderPath := filepath.Join(configDir, "ensuretest")
	if errMsg := AddFolder("ensuretest", "Ensure Test", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	defaults := []string{".Trash", ".obsidian/workspace.json"}
	defaultsJSON, _ := json.Marshal(defaults)

	// (a) No .stignore yet → defaults created.
	if errMsg := EnsureDefaultIgnores("ensuretest", string(defaultsJSON)); errMsg != "" {
		t.Fatalf("EnsureDefaultIgnores create = %q, want ''", errMsg)
	}
	if got := readIgnoreLines(t, "ensuretest"); len(got) != 2 {
		t.Fatalf("after create = %v, want 2 lines", got)
	}

	// (b) Existing customs preserved, defaults appended, order stable.
	customs := []string{"*.tmp", "Drafts/"}
	customsJSON, _ := json.Marshal(customs)
	if errMsg := SetFolderIgnores("ensuretest", string(customsJSON)); errMsg != "" {
		t.Fatalf("SetFolderIgnores failed: %s", errMsg)
	}
	if errMsg := EnsureDefaultIgnores("ensuretest", string(defaultsJSON)); errMsg != "" {
		t.Fatalf("EnsureDefaultIgnores merge = %q, want ''", errMsg)
	}
	got := readIgnoreLines(t, "ensuretest")
	want := []string{"*.tmp", "Drafts/", ".Trash", ".obsidian/workspace.json"}
	if len(got) != len(want) {
		t.Fatalf("after merge = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q (customs first, order stable)", i, got[i], want[i])
		}
	}

	// (c) All defaults present → idempotent no-op.
	if errMsg := EnsureDefaultIgnores("ensuretest", string(defaultsJSON)); errMsg != "" {
		t.Fatalf("EnsureDefaultIgnores idempotent = %q, want ''", errMsg)
	}
	if got2 := readIgnoreLines(t, "ensuretest"); len(got2) != len(want) {
		t.Errorf("idempotent run changed line count: %v", got2)
	}

	// (d) Unknown folder.
	if errMsg := EnsureDefaultIgnores("nope", string(defaultsJSON)); errMsg != "folder not found" {
		t.Fatalf("EnsureDefaultIgnores unknown = %q, want 'folder not found'", errMsg)
	}

	// (e) Invalid JSON.
	if errMsg := EnsureDefaultIgnores("ensuretest", "not json"); errMsg == "" {
		t.Fatal("EnsureDefaultIgnores invalid JSON should error")
	}

	// (f) Read error → abort without overwriting (skip when root can bypass perms).
	if os.Geteuid() != 0 {
		stignore := filepath.Join(folderPath, ".stignore")
		if err := os.Chmod(stignore, 0o000); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		errMsg := EnsureDefaultIgnores("ensuretest", string(defaultsJSON))
		_ = os.Chmod(stignore, 0o600)
		if errMsg == "" {
			t.Error("EnsureDefaultIgnores with unreadable .stignore should error, not silently overwrite")
		}
		if got3 := readIgnoreLines(t, "ensuretest"); len(got3) != len(want) {
			t.Errorf("content changed despite read error: %v", got3)
		}
	}
}

// writeFolderMarker creates the default `.stfolder` marker for the given folder
// ID inside root, matching what Syncthing writes when it creates a folder.
func writeFolderMarker(t *testing.T, root, folderID string) {
	t.Helper()
	markerDir := filepath.Join(root, ".stfolder")
	if err := os.MkdirAll(markerDir, 0o700); err != nil {
		t.Fatalf("create marker dir: %v", err)
	}
	h := sha256.Sum256([]byte(folderID))
	markerFile := filepath.Join(markerDir, fmt.Sprintf("syncthing-folder-%x.txt", h[:3]))
	if err := os.WriteFile(markerFile, []byte("test marker"), 0o600); err != nil {
		t.Fatalf("write marker file: %v", err)
	}
}

func readIgnoreLines(t *testing.T, folderID string) []string {
	t.Helper()
	var lines []string
	if err := json.Unmarshal([]byte(GetFolderIgnores(folderID)), &lines); err != nil {
		t.Fatalf("GetFolderIgnores unmarshal: %v", err)
	}
	return lines
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
