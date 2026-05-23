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

func TestRemoveConflictFilesForOriginal(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	folderPath := filepath.Join(configDir, "skipfamily")
	if errMsg := AddFolder("skipfamily", "Skip Family", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	// Root-level original + two conflict copies (different timestamps/devices).
	if err := os.WriteFile(filepath.Join(folderPath, "notes.md"), []byte("original"), 0o644); err != nil {
		t.Fatalf("write notes.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(folderPath, "notes.sync-conflict-20260520-120000-AAA1111.md"), []byte("c1"), 0o644); err != nil {
		t.Fatalf("write notes conflict c1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(folderPath, "notes.sync-conflict-20260521-130000-BBB2222.md"), []byte("c2"), 0o644); err != nil {
		t.Fatalf("write notes conflict c2: %v", err)
	}

	// Unrelated file that must not be touched.
	if err := os.WriteFile(filepath.Join(folderPath, "other.md"), []byte("other"), 0o644); err != nil {
		t.Fatalf("write other.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(folderPath, "other.sync-conflict-20260520-120000-CCC3333.md"), []byte("o1"), 0o644); err != nil {
		t.Fatalf("write other conflict: %v", err)
	}

	// Nested original + nested conflict.
	subDir := filepath.Join(folderPath, "Personal")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir subDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "diary.md"), []byte("d"), 0o644); err != nil {
		t.Fatalf("write diary.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "diary.sync-conflict-20260520-120000-DDD4444.md"), []byte("d1"), 0o644); err != nil {
		t.Fatalf("write diary conflict: %v", err)
	}

	// Remove conflict copies for "notes.md" only.
	got := RemoveConflictFilesForOriginal("skipfamily", "notes.md")
	var result struct {
		Removed int    `json:"removed"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal([]byte(got), &result); err != nil {
		t.Fatalf("unmarshal: %v (raw: %s)", err, got)
	}
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.Removed != 2 {
		t.Errorf("removed = %d, want 2", result.Removed)
	}

	// Original "notes.md" must survive.
	if _, err := os.Stat(filepath.Join(folderPath, "notes.md")); err != nil {
		t.Errorf("notes.md should still exist: %v", err)
	}

	// Both notes conflict copies must be gone.
	for _, name := range []string{
		"notes.sync-conflict-20260520-120000-AAA1111.md",
		"notes.sync-conflict-20260521-130000-BBB2222.md",
	} {
		if _, err := os.Stat(filepath.Join(folderPath, name)); !os.IsNotExist(err) {
			t.Errorf("%s should have been deleted", name)
		}
	}

	// Unrelated "other.*" files must survive.
	if _, err := os.Stat(filepath.Join(folderPath, "other.md")); err != nil {
		t.Errorf("other.md should still exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(folderPath, "other.sync-conflict-20260520-120000-CCC3333.md")); err != nil {
		t.Errorf("other.sync-conflict-* should still exist: %v", err)
	}

	// Nested originals and their conflicts in another directory must survive
	// when we ask for the root file only.
	if _, err := os.Stat(filepath.Join(subDir, "diary.sync-conflict-20260520-120000-DDD4444.md")); err != nil {
		t.Errorf("nested conflict should still exist: %v", err)
	}

	// Now ask for nested "Personal/diary.md" and verify only the nested copy goes.
	got = RemoveConflictFilesForOriginal("skipfamily", filepath.Join("Personal", "diary.md"))
	if err := json.Unmarshal([]byte(got), &result); err != nil {
		t.Fatalf("unmarshal nested: %v (raw: %s)", err, got)
	}
	if result.Removed != 1 || result.Error != "" {
		t.Errorf("nested call result = %+v, want removed=1 error=\"\"", result)
	}

	// Dotted-stem regression: archive.tar.gz must match its own conflict copy
	// but not a sibling that happens to share the inner stem.
	if err := os.WriteFile(filepath.Join(folderPath, "archive.tar.gz"), []byte("a"), 0o644); err != nil {
		t.Fatalf("write archive.tar.gz: %v", err)
	}
	if err := os.WriteFile(filepath.Join(folderPath, "archive.tar.sync-conflict-20260520-120000-EEE5555.gz"), []byte("ac"), 0o644); err != nil {
		t.Fatalf("write archive conflict copy: %v", err)
	}
	// Same inner stem but different extension — must NOT match.
	if err := os.WriteFile(filepath.Join(folderPath, "archive.tar.sync-conflict-20260520-120000-FFF6666.md"), []byte("decoy"), 0o644); err != nil {
		t.Fatalf("write decoy: %v", err)
	}

	got = RemoveConflictFilesForOriginal("skipfamily", "archive.tar.gz")
	if err := json.Unmarshal([]byte(got), &result); err != nil {
		t.Fatalf("unmarshal dotted: %v (raw: %s)", err, got)
	}
	if result.Removed != 1 || result.Error != "" {
		t.Errorf("dotted-stem call result = %+v, want removed=1 error=\"\"", result)
	}
	if _, err := os.Stat(filepath.Join(folderPath, "archive.tar.sync-conflict-20260520-120000-EEE5555.gz")); !os.IsNotExist(err) {
		t.Error("archive.tar.gz conflict copy should have been deleted")
	}
	if _, err := os.Stat(filepath.Join(folderPath, "archive.tar.sync-conflict-20260520-120000-FFF6666.md")); err != nil {
		t.Errorf("decoy with different extension should still exist: %v", err)
	}

	// Idempotency: running again returns removed=0, no error.
	got = RemoveConflictFilesForOriginal("skipfamily", "notes.md")
	if err := json.Unmarshal([]byte(got), &result); err != nil {
		t.Fatalf("unmarshal idempotent: %v (raw: %s)", err, got)
	}
	if result.Removed != 0 || result.Error != "" {
		t.Errorf("idempotent call = %+v, want removed=0 error=\"\"", result)
	}
}

func TestRemoveConflictFilesForOriginalErrors(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	folderPath := filepath.Join(configDir, "skipfamilyerr")
	if errMsg := AddFolder("skipfamilyerr", "Skip Family Err", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	// Unknown folder.
	got := RemoveConflictFilesForOriginal("nonexistent", "x.md")
	if !strings.Contains(got, `"error":"folder not found"`) {
		t.Errorf("unknown folder result = %q, want error 'folder not found'", got)
	}

	// Path traversal.
	got = RemoveConflictFilesForOriginal("skipfamilyerr", "../../etc/passwd")
	if !strings.Contains(got, `"error":"invalid path: outside folder root"`) {
		t.Errorf("traversal result = %q, want invalid-path error", got)
	}

	// Empty / root-equivalent paths must be rejected (would otherwise scan outside folder root).
	for _, rp := range []string{"", ".", "/"} {
		got := RemoveConflictFilesForOriginal("skipfamilyerr", rp)
		if !strings.Contains(got, `"error":"invalid path: outside folder root"`) {
			t.Errorf("root path %q result = %q, want invalid-path error", rp, got)
		}
	}
}
