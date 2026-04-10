package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetConflictFilesJSON(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	// Add a folder.
	folderPath := filepath.Join(configDir, "conflicttest")
	if errMsg := AddFolder("conflicttest", "Conflict Test", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	// No conflicts yet.
	got := GetConflictFilesJSON("conflicttest")
	var conflicts []ConflictFile
	if err := json.Unmarshal([]byte(got), &conflicts); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("expected 0 conflicts, got %d", len(conflicts))
	}

	// Create a conflict file.
	original := filepath.Join(folderPath, "notes.md")
	os.WriteFile(original, []byte("original content"), 0o644)

	conflictFile := filepath.Join(folderPath, "notes.sync-conflict-20260406-143022-ABC1234.md")
	os.WriteFile(conflictFile, []byte("conflict content"), 0o644)

	// Create a conflict in a subdirectory.
	subDir := filepath.Join(folderPath, "subfolder")
	os.MkdirAll(subDir, 0o755)
	subConflict := filepath.Join(subDir, "readme.sync-conflict-20260405-120000-XYZ9876.md")
	os.WriteFile(subConflict, []byte("sub conflict"), 0o644)

	// Should find 2 conflicts.
	got = GetConflictFilesJSON("conflicttest")
	if err := json.Unmarshal([]byte(got), &conflicts); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(conflicts) != 2 {
		t.Fatalf("expected 2 conflicts, got %d", len(conflicts))
	}

	// Verify first conflict (root level).
	found := false
	for _, c := range conflicts {
		if c.OriginalPath == "notes.md" {
			found = true
			if c.ConflictDate != "20260406-143022" {
				t.Errorf("conflictDate = %q, want %q", c.ConflictDate, "20260406-143022")
			}
			if c.DeviceShortID != "ABC1234" {
				t.Errorf("deviceShortID = %q, want %q", c.DeviceShortID, "ABC1234")
			}
			break
		}
	}
	if !found {
		t.Error("root-level conflict not found")
	}

	// Verify subdirectory conflict.
	found = false
	for _, c := range conflicts {
		if c.OriginalPath == filepath.Join("subfolder", "readme.md") {
			found = true
			if c.DeviceShortID != "XYZ9876" {
				t.Errorf("sub deviceShortID = %q, want %q", c.DeviceShortID, "XYZ9876")
			}
			break
		}
	}
	if !found {
		t.Error("subdirectory conflict not found")
	}

	// Nonexistent folder returns empty array.
	if got := GetConflictFilesJSON("nonexistent"); got != "[]" {
		t.Errorf("nonexistent folder = %q, want '[]'", got)
	}
}

func TestReadFileContent(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	folderPath := filepath.Join(configDir, "readtest")
	if errMsg := AddFolder("readtest", "Read Test", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	content := "# Hello World\n\nThis is a test."
	os.WriteFile(filepath.Join(folderPath, "test.md"), []byte(content), 0o644)

	got := ReadFileContent("readtest", "test.md")
	if got != content {
		t.Errorf("ReadFileContent = %q, want %q", got, content)
	}

	// Nonexistent file returns error prefix.
	if got := ReadFileContent("readtest", "nope.md"); !strings.HasPrefix(got, "error:") {
		t.Errorf("nonexistent file = %q, want error: prefix", got)
	}

	// Path traversal returns error prefix.
	if got := ReadFileContent("readtest", "../../etc/passwd"); !strings.HasPrefix(got, "error:") {
		t.Errorf("path traversal = %q, want error: prefix", got)
	}

	// Nonexistent folder returns error prefix.
	if got := ReadFileContent("nonexistent", "test.md"); !strings.HasPrefix(got, "error:") {
		t.Errorf("nonexistent folder = %q, want error: prefix", got)
	}
}

func TestResolveConflictKeepOriginal(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	folderPath := filepath.Join(configDir, "resolvetest")
	if errMsg := AddFolder("resolvetest", "Resolve Test", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	// Create original and conflict.
	original := filepath.Join(folderPath, "doc.md")
	os.WriteFile(original, []byte("original"), 0o644)

	conflictName := "doc.sync-conflict-20260406-100000-DEF5678.md"
	os.WriteFile(filepath.Join(folderPath, conflictName), []byte("conflict version"), 0o644)

	// Resolve: keep original (delete conflict).
	if errMsg := ResolveConflict("resolvetest", conflictName, false); errMsg != "" {
		t.Fatalf("ResolveConflict(keepConflict=false) failed: %s", errMsg)
	}

	// Original should be unchanged.
	data, _ := os.ReadFile(original)
	if string(data) != "original" {
		t.Errorf("original content = %q, want %q", string(data), "original")
	}

	// Conflict file should be gone.
	if _, err := os.Stat(filepath.Join(folderPath, conflictName)); !os.IsNotExist(err) {
		t.Error("conflict file should have been deleted")
	}
}

func TestResolveConflictKeepConflict(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	folderPath := filepath.Join(configDir, "resolvetest2")
	if errMsg := AddFolder("resolvetest2", "Resolve Test 2", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	// Create original and conflict.
	original := filepath.Join(folderPath, "doc.md")
	os.WriteFile(original, []byte("original"), 0o644)

	conflictName := "doc.sync-conflict-20260406-100000-DEF5678.md"
	os.WriteFile(filepath.Join(folderPath, conflictName), []byte("conflict version"), 0o644)

	// Resolve: keep conflict (replace original).
	if errMsg := ResolveConflict("resolvetest2", conflictName, true); errMsg != "" {
		t.Fatalf("ResolveConflict(keepConflict=true) failed: %s", errMsg)
	}

	// Original should now have conflict content.
	data, _ := os.ReadFile(original)
	if string(data) != "conflict version" {
		t.Errorf("original content = %q, want %q", string(data), "conflict version")
	}

	// Conflict file should be gone.
	if _, err := os.Stat(filepath.Join(folderPath, conflictName)); !os.IsNotExist(err) {
		t.Error("conflict file should have been deleted")
	}
}

func TestResolveConflictErrors(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	folderPath := filepath.Join(configDir, "resolveerr")
	if errMsg := AddFolder("resolveerr", "Resolve Err", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	// Nonexistent folder.
	if errMsg := ResolveConflict("nonexistent", "file.md", false); errMsg != "folder not found" {
		t.Errorf("nonexistent folder = %q, want 'folder not found'", errMsg)
	}

	// Nonexistent conflict file.
	if errMsg := ResolveConflict("resolveerr", "nope.sync-conflict-20260406-100000-ABC1234.md", false); errMsg != "conflict file not found" {
		t.Errorf("nonexistent file = %q, want 'conflict file not found'", errMsg)
	}

	// Invalid conflict filename with keepConflict=true.
	normalFile := filepath.Join(folderPath, "normal.md")
	os.WriteFile(normalFile, []byte("normal"), 0o644)
	if errMsg := ResolveConflict("resolveerr", "normal.md", true); errMsg != "invalid conflict filename" {
		t.Errorf("invalid filename = %q, want 'invalid conflict filename'", errMsg)
	}
}

func TestKeepBothConflict(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	folderPath := filepath.Join(configDir, "keepbothtest")
	if errMsg := AddFolder("keepbothtest", "Keep Both Test", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	// Create original and conflict.
	original := filepath.Join(folderPath, "doc.md")
	os.WriteFile(original, []byte("original"), 0o644)

	conflictName := "doc.sync-conflict-20260406-100000-DEF5678.md"
	os.WriteFile(filepath.Join(folderPath, conflictName), []byte("conflict version"), 0o644)

	// Keep both: rename conflict to non-conflict name.
	if errMsg := KeepBothConflict("keepbothtest", conflictName); errMsg != "" {
		t.Fatalf("KeepBothConflict failed: %s", errMsg)
	}

	// Original should still exist unchanged.
	data, _ := os.ReadFile(original)
	if string(data) != "original" {
		t.Errorf("original content = %q, want %q", string(data), "original")
	}

	// Conflict file should be gone.
	if _, err := os.Stat(filepath.Join(folderPath, conflictName)); !os.IsNotExist(err) {
		t.Error("conflict file should have been renamed")
	}

	// Renamed file should exist with new name.
	renamedPath := filepath.Join(folderPath, "doc.conflict-DEF5678.md")
	data, err := os.ReadFile(renamedPath)
	if err != nil {
		t.Fatalf("renamed file not found: %v", err)
	}
	if string(data) != "conflict version" {
		t.Errorf("renamed content = %q, want %q", string(data), "conflict version")
	}

	// Should no longer appear in conflict scan.
	got := GetConflictFilesJSON("keepbothtest")
	var conflicts []ConflictFile
	if err := json.Unmarshal([]byte(got), &conflicts); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(conflicts) != 0 {
		t.Errorf("expected 0 conflicts after keep-both, got %d", len(conflicts))
	}
}

func TestRenameDevice(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	// Add a device.
	testDeviceID := "MFZWI3D-BONSGYC-YLTMRWG-C43ENR5-QXGZDMM-FZWI3DP-BONSGYY-LTMRWAD"
	if errMsg := AddDevice(testDeviceID, "OldName"); errMsg != "" {
		t.Fatalf("AddDevice failed: %s", errMsg)
	}

	// Rename it.
	if errMsg := RenameDevice(testDeviceID, "NewName"); errMsg != "" {
		t.Fatalf("RenameDevice failed: %s", errMsg)
	}

	// Verify the name changed.
	devicesJSON := GetDevicesJSON()
	var devices []DeviceInfo
	if err := json.Unmarshal([]byte(devicesJSON), &devices); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("expected 1 device, got %d", len(devices))
	}
	if devices[0].Name != "NewName" {
		t.Errorf("device name = %q, want %q", devices[0].Name, "NewName")
	}

	// Rename nonexistent device (remove first, then try to rename).
	RemoveDevice(testDeviceID)
	if errMsg := RenameDevice(testDeviceID, "X"); errMsg != "device not found" {
		t.Errorf("rename nonexistent = %q, want 'device not found'", errMsg)
	}

	// Cannot rename own device.
	if errMsg := RenameDevice(DeviceID(), "Me"); errMsg != "cannot rename own device" {
		t.Errorf("rename self = %q, want 'cannot rename own device'", errMsg)
	}

	// Not running.
	StopSyncthing()
	if errMsg := RenameDevice(testDeviceID, "X"); errMsg != "syncthing not running" {
		t.Errorf("rename when stopped = %q, want 'syncthing not running'", errMsg)
	}
}
