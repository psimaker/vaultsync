package bridge

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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

func TestIssue143ResolveConflictPreservesExistingTemporaryFile(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	const folderID = "issue143temp"
	folderPath := filepath.Join(configDir, folderID)
	if errMsg := AddFolder(folderID, "Issue 143 Temp Collision", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	const conflictName = "doc.sync-conflict-20260406-100000-DEF5678.md"
	originalPath := filepath.Join(folderPath, "doc.md")
	conflictPath := filepath.Join(folderPath, conflictName)
	tempPath := originalPath + ".vaultsync-tmp"
	unrelatedPath := filepath.Join(folderPath, "unrelated-sentinel.md")

	originalSentinel := []byte("issue-143-original-sentinel")
	conflictSentinel := []byte("issue-143-conflict-sentinel")
	tempSentinel := []byte("issue-143-existing-temp-sentinel")
	unrelatedSentinel := []byte("issue-143-unrelated-sentinel")
	fixtures := []struct {
		name string
		path string
		data []byte
	}{
		{name: "original", path: originalPath, data: originalSentinel},
		{name: "conflict", path: conflictPath, data: conflictSentinel},
		{name: "pre-existing temp", path: tempPath, data: tempSentinel},
		{name: "unrelated", path: unrelatedPath, data: unrelatedSentinel},
	}
	for _, fixture := range fixtures {
		if err := os.WriteFile(fixture.path, fixture.data, 0o644); err != nil {
			t.Fatalf("write %s fixture: %v", fixture.name, err)
		}
		got, err := os.ReadFile(fixture.path)
		if err != nil {
			t.Fatalf("read back %s fixture: %v", fixture.name, err)
		}
		if !bytes.Equal(got, fixture.data) {
			t.Fatalf("%s fixture bytes = %q, want %q", fixture.name, got, fixture.data)
		}
	}

	if errMsg := ResolveConflict(folderID, conflictName, true); errMsg != "" {
		t.Fatalf("ResolveConflict(keepConflict=true) failed: %s", errMsg)
	}

	tempAfter, err := os.ReadFile(tempPath)
	if err != nil {
		t.Fatalf("pre-existing temp file was not preserved: %v", err)
	}
	if !bytes.Equal(tempAfter, tempSentinel) {
		t.Errorf("pre-existing temp bytes = %q, want %q", tempAfter, tempSentinel)
	}

	originalAfter, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatalf("read resolved original: %v", err)
	}
	if !bytes.Equal(originalAfter, conflictSentinel) {
		t.Errorf("resolved original bytes = %q, want conflict bytes %q", originalAfter, conflictSentinel)
	}
	if _, err := os.Stat(conflictPath); !os.IsNotExist(err) {
		t.Errorf("resolved conflict path still exists or stat failed: %v", err)
	}
	unrelatedAfter, err := os.ReadFile(unrelatedPath)
	if err != nil {
		t.Fatalf("read unrelated file: %v", err)
	}
	if !bytes.Equal(unrelatedAfter, unrelatedSentinel) {
		t.Errorf("unrelated bytes = %q, want %q", unrelatedAfter, unrelatedSentinel)
	}
	ownedTemps, err := filepath.Glob(filepath.Join(folderPath, ".syncthing.vaultsync-resolve-*"))
	if err != nil {
		t.Fatalf("glob VaultSync temporary files: %v", err)
	}
	if len(ownedTemps) != 0 {
		t.Errorf("successful resolution left VaultSync temporary files: %v", ownedTemps)
	}
}

func TestIssue143ResolveConflictRejectsPathTraversal(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	const folderID = "issue143traversal"
	folderPath := filepath.Join(configDir, folderID)
	if errMsg := AddFolder(folderID, "Issue 143 Traversal", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	const conflictName = "outside.sync-conflict-20260406-100000-DEF5678.md"
	originalPath := filepath.Join(configDir, "outside.md")
	conflictPath := filepath.Join(configDir, conflictName)
	legacyPath := originalPath + ".vaultsync-tmp"
	unrelatedPath := filepath.Join(configDir, "outside-unrelated-sentinel.md")
	originalBytes := []byte("issue-143-traversal-original")
	conflictBytes := []byte("issue-143-traversal-conflict")
	legacyBytes := []byte("issue-143-traversal-legacy-temp")
	unrelatedBytes := []byte("issue-143-traversal-unrelated")

	fixtures := []struct {
		name string
		path string
		data []byte
	}{
		{name: "outside original", path: originalPath, data: originalBytes},
		{name: "outside conflict", path: conflictPath, data: conflictBytes},
		{name: "outside legacy temp", path: legacyPath, data: legacyBytes},
		{name: "outside unrelated", path: unrelatedPath, data: unrelatedBytes},
	}
	for _, fixture := range fixtures {
		if err := os.WriteFile(fixture.path, fixture.data, 0o600); err != nil {
			t.Fatalf("write %s: %v", fixture.name, err)
		}
		issue143AssertFileBytes(t, fixture.path, fixture.data)
	}

	errMsg := ResolveConflict(folderID, filepath.Join("..", conflictName), true)
	if errMsg != "invalid path: outside folder root" {
		t.Fatalf("ResolveConflict traversal error = %q, want invalid path error", errMsg)
	}

	for _, fixture := range fixtures {
		issue143AssertFileBytes(t, fixture.path, fixture.data)
	}
	issue143AssertNoOperationTemps(t, configDir)
}

func TestIssue143ResolveConflictPreservesModeAndSupportsMissingOriginal(t *testing.T) {
	tests := []struct {
		name           string
		createOriginal bool
		wantMode       os.FileMode
	}{
		{name: "existing original", createOriginal: true, wantMode: 0o640},
		{name: "missing original", createOriginal: false, wantMode: 0o644},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			originalPath := filepath.Join(dir, "doc.md")
			conflictPath := filepath.Join(dir, "doc.sync-conflict-20260406-100000-DEF5678.md")
			conflictBytes := []byte("issue-143-selected-conflict")

			if tt.createOriginal {
				if err := os.WriteFile(originalPath, []byte("issue-143-original"), 0o600); err != nil {
					t.Fatalf("write original: %v", err)
				}
				if err := os.Chmod(originalPath, tt.wantMode); err != nil {
					t.Fatalf("set original permissions: %v", err)
				}
			}
			if err := os.WriteFile(conflictPath, conflictBytes, 0o600); err != nil {
				t.Fatalf("write conflict: %v", err)
			}

			if err := replaceConflictAndRemoveSource(conflictPath, originalPath, systemConflictFileOperations()); err != nil {
				t.Fatalf("replaceConflictAndRemoveSource() failed: %v", err)
			}

			issue143AssertFileBytes(t, originalPath, conflictBytes)
			if _, err := os.Stat(conflictPath); !os.IsNotExist(err) {
				t.Fatalf("conflict path still exists or stat failed: %v", err)
			}
			info, err := os.Stat(originalPath)
			if err != nil {
				t.Fatalf("stat resolved original: %v", err)
			}
			if got := info.Mode().Perm(); got != tt.wantMode {
				t.Errorf("resolved original permissions = %04o, want %04o", got, tt.wantMode)
			}
			issue143AssertNoOperationTemps(t, dir)
		})
	}
}

func TestIssue143ResolveConflictDoesNotTouchLegacyTempNodes(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string) func(*testing.T)
	}{
		{
			name: "regular file",
			setup: func(t *testing.T, legacyPath string) func(*testing.T) {
				t.Helper()
				want := []byte("issue-143-legacy-file-sentinel")
				if err := os.WriteFile(legacyPath, want, 0o600); err != nil {
					t.Fatalf("write legacy regular file: %v", err)
				}
				return func(t *testing.T) {
					t.Helper()
					issue143AssertFileBytes(t, legacyPath, want)
				}
			},
		},
		{
			name: "symlink",
			setup: func(t *testing.T, legacyPath string) func(*testing.T) {
				t.Helper()
				targetPath := filepath.Join(filepath.Dir(legacyPath), "legacy-symlink-target")
				want := []byte("issue-143-symlink-target-sentinel")
				if err := os.WriteFile(targetPath, want, 0o600); err != nil {
					t.Fatalf("write symlink target: %v", err)
				}
				if err := os.Symlink(targetPath, legacyPath); err != nil {
					t.Fatalf("create legacy symlink: %v", err)
				}
				return func(t *testing.T) {
					t.Helper()
					info, err := os.Lstat(legacyPath)
					if err != nil {
						t.Fatalf("lstat legacy symlink: %v", err)
					}
					if info.Mode()&os.ModeSymlink == 0 {
						t.Fatalf("legacy path mode = %v, want symlink", info.Mode())
					}
					gotTarget, err := os.Readlink(legacyPath)
					if err != nil {
						t.Fatalf("read legacy symlink: %v", err)
					}
					if gotTarget != targetPath {
						t.Errorf("legacy symlink target = %q, want %q", gotTarget, targetPath)
					}
					issue143AssertFileBytes(t, targetPath, want)
				}
			},
		},
		{
			name: "directory",
			setup: func(t *testing.T, legacyPath string) func(*testing.T) {
				t.Helper()
				if err := os.Mkdir(legacyPath, 0o700); err != nil {
					t.Fatalf("create legacy directory: %v", err)
				}
				childPath := filepath.Join(legacyPath, "sentinel")
				want := []byte("issue-143-legacy-directory-sentinel")
				if err := os.WriteFile(childPath, want, 0o600); err != nil {
					t.Fatalf("write legacy directory sentinel: %v", err)
				}
				return func(t *testing.T) {
					t.Helper()
					info, err := os.Lstat(legacyPath)
					if err != nil {
						t.Fatalf("lstat legacy directory: %v", err)
					}
					if !info.IsDir() {
						t.Fatalf("legacy path mode = %v, want directory", info.Mode())
					}
					issue143AssertFileBytes(t, childPath, want)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			originalPath := filepath.Join(dir, "doc.md")
			conflictPath := filepath.Join(dir, "doc.sync-conflict-20260406-100000-DEF5678.md")
			legacyPath := originalPath + ".vaultsync-tmp"
			conflictBytes := []byte("issue-143-selected-conflict")

			if err := os.WriteFile(originalPath, []byte("issue-143-original"), 0o600); err != nil {
				t.Fatalf("write original: %v", err)
			}
			if err := os.WriteFile(conflictPath, conflictBytes, 0o600); err != nil {
				t.Fatalf("write conflict: %v", err)
			}
			verifyLegacy := tt.setup(t, legacyPath)

			if err := replaceConflictAndRemoveSource(conflictPath, originalPath, systemConflictFileOperations()); err != nil {
				t.Fatalf("replaceConflictAndRemoveSource() failed: %v", err)
			}

			issue143AssertFileBytes(t, originalPath, conflictBytes)
			if _, err := os.Stat(conflictPath); !os.IsNotExist(err) {
				t.Fatalf("conflict path still exists or stat failed: %v", err)
			}
			verifyLegacy(t)
			issue143AssertNoOperationTemps(t, dir)
		})
	}
}

func TestIssue143ResolveConflictPreCommitFailuresPreserveUserFiles(t *testing.T) {
	tests := []struct {
		name          string
		wantErrPrefix string
		wantTempCount int
		inject        func(*conflictFileOperations)
	}{
		{
			name:          "read",
			wantErrPrefix: "read conflict file:",
			wantTempCount: 0,
			inject: func(ops *conflictFileOperations) {
				ops.readFile = func(string) ([]byte, error) { return nil, syscall.EIO }
			},
		},
		{
			name:          "stat",
			wantErrPrefix: "stat original file:",
			wantTempCount: 0,
			inject: func(ops *conflictFileOperations) {
				ops.stat = func(string) (os.FileInfo, error) { return nil, syscall.EACCES }
			},
		},
		{
			name:          "create disk full",
			wantErrPrefix: "create temp file:",
			wantTempCount: 0,
			inject: func(ops *conflictFileOperations) {
				ops.createTemp = func(string, string) (conflictTempFile, error) {
					return nil, syscall.ENOSPC
				}
			},
		},
		{
			name:          "partial write disk full",
			wantErrPrefix: "write temp file:",
			wantTempCount: 1,
			inject: func(ops *conflictFileOperations) {
				issue143InjectTempFault(ops, func(file *issue143FaultingTempFile) {
					file.write = func(data []byte) (int, error) {
						n, err := file.conflictTempFile.Write(data[:len(data)/2])
						if err != nil {
							return n, err
						}
						return n, syscall.ENOSPC
					}
				})
			},
		},
		{
			name:          "short write",
			wantErrPrefix: "write temp file:",
			wantTempCount: 1,
			inject: func(ops *conflictFileOperations) {
				issue143InjectTempFault(ops, func(file *issue143FaultingTempFile) {
					file.write = func(data []byte) (int, error) {
						return file.conflictTempFile.Write(data[:len(data)/2])
					}
				})
			},
		},
		{
			name:          "chmod",
			wantErrPrefix: "set temp permissions:",
			wantTempCount: 1,
			inject: func(ops *conflictFileOperations) {
				issue143InjectTempFault(ops, func(file *issue143FaultingTempFile) {
					file.chmod = func(os.FileMode) error { return syscall.EPERM }
				})
			},
		},
		{
			name:          "sync",
			wantErrPrefix: "sync temp file:",
			wantTempCount: 1,
			inject: func(ops *conflictFileOperations) {
				issue143InjectTempFault(ops, func(file *issue143FaultingTempFile) {
					file.sync = func() error { return syscall.EIO }
				})
			},
		},
		{
			name:          "close",
			wantErrPrefix: "close temp file:",
			wantTempCount: 1,
			inject: func(ops *conflictFileOperations) {
				issue143InjectTempFault(ops, func(file *issue143FaultingTempFile) {
					file.close = func() error {
						if err := file.conflictTempFile.Close(); err != nil {
							return err
						}
						return syscall.EIO
					}
				})
			},
		},
		{
			name:          "rename",
			wantErrPrefix: "replace original file:",
			wantTempCount: 1,
			inject: func(ops *conflictFileOperations) {
				ops.rename = func(string, string) error { return syscall.EIO }
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := issue143NewFileFixture(t)
			ops := systemConflictFileOperations()
			tt.inject(&ops)

			err := replaceConflictAndRemoveSource(fixture.conflictPath, fixture.originalPath, ops)
			if err == nil {
				t.Fatalf("replaceConflictAndRemoveSource() succeeded, want %q error", tt.wantErrPrefix)
			}
			if !strings.HasPrefix(err.Error(), tt.wantErrPrefix) {
				t.Errorf("error = %q, want prefix %q", err, tt.wantErrPrefix)
			}

			issue143AssertPreCommitFixture(t, fixture)
			temps := issue143OperationTemps(t, fixture.dir)
			if len(temps) != tt.wantTempCount {
				t.Errorf("temporary file count = %d, want %d: %v", len(temps), tt.wantTempCount, temps)
			}
			for _, tempPath := range temps {
				info, statErr := os.Lstat(tempPath)
				if statErr != nil {
					t.Fatalf("lstat operation temp: %v", statErr)
				}
				if !info.Mode().IsRegular() {
					t.Errorf("operation temp mode = %v, want regular file", info.Mode())
				}
			}
		})
	}
}

func TestIssue143ResolveConflictPostCommitCleanupFailurePreservesDuplicate(t *testing.T) {
	fixture := issue143NewFileFixture(t)
	ops := systemConflictFileOperations()
	realRemove := ops.remove
	ops.remove = func(path string) error {
		if path == fixture.conflictPath {
			return syscall.EIO
		}
		return realRemove(path)
	}

	err := replaceConflictAndRemoveSource(fixture.conflictPath, fixture.originalPath, ops)
	if err == nil {
		t.Fatal("replaceConflictAndRemoveSource() succeeded, want cleanup error")
	}
	if !strings.HasPrefix(err.Error(), "delete conflict file:") {
		t.Errorf("error = %q, want delete conflict file prefix", err)
	}

	issue143AssertFileBytes(t, fixture.originalPath, fixture.conflictBytes)
	issue143AssertFileBytes(t, fixture.conflictPath, fixture.conflictBytes)
	issue143AssertFileBytes(t, fixture.legacyPath, fixture.legacyBytes)
	issue143AssertFileBytes(t, fixture.unrelatedPath, fixture.unrelatedBytes)
	issue143AssertNoOperationTemps(t, fixture.dir)
}

func TestIssue143ResolveConflictDoesNotRemoveReusedTempPathAfterCommit(t *testing.T) {
	fixture := issue143NewFileFixture(t)
	ops := systemConflictFileOperations()
	createTemp := ops.createTemp
	realRename := ops.rename
	var tempPath string
	ops.createTemp = func(dir, pattern string) (conflictTempFile, error) {
		created, err := createTemp(dir, pattern)
		if err == nil {
			tempPath = created.Name()
		}
		return created, err
	}
	foreignBytes := []byte("issue-143-post-rename-foreign-sentinel")
	ops.rename = func(oldPath, newPath string) error {
		if err := realRename(oldPath, newPath); err != nil {
			return err
		}
		file, err := os.OpenFile(oldPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err := file.Write(foreignBytes); err != nil {
			file.Close()
			return err
		}
		return file.Close()
	}

	if err := replaceConflictAndRemoveSource(fixture.conflictPath, fixture.originalPath, ops); err != nil {
		t.Fatalf("replaceConflictAndRemoveSource() failed: %v", err)
	}
	if tempPath == "" {
		t.Fatal("operation did not report its temporary path")
	}

	issue143AssertFileBytes(t, fixture.originalPath, fixture.conflictBytes)
	if _, err := os.Stat(fixture.conflictPath); !os.IsNotExist(err) {
		t.Fatalf("conflict path still exists or stat failed: %v", err)
	}
	issue143AssertFileBytes(t, tempPath, foreignBytes)
	issue143AssertFileBytes(t, fixture.legacyPath, fixture.legacyBytes)
	issue143AssertFileBytes(t, fixture.unrelatedPath, fixture.unrelatedBytes)
}

type issue143FaultingTempFile struct {
	conflictTempFile
	write func([]byte) (int, error)
	chmod func(os.FileMode) error
	sync  func() error
	close func() error
}

func (file *issue143FaultingTempFile) Write(data []byte) (int, error) {
	if file.write != nil {
		return file.write(data)
	}
	return file.conflictTempFile.Write(data)
}

func (file *issue143FaultingTempFile) Chmod(mode os.FileMode) error {
	if file.chmod != nil {
		return file.chmod(mode)
	}
	return file.conflictTempFile.Chmod(mode)
}

func (file *issue143FaultingTempFile) Sync() error {
	if file.sync != nil {
		return file.sync()
	}
	return file.conflictTempFile.Sync()
}

func (file *issue143FaultingTempFile) Close() error {
	if file.close != nil {
		return file.close()
	}
	return file.conflictTempFile.Close()
}

func issue143InjectTempFault(ops *conflictFileOperations, configure func(*issue143FaultingTempFile)) {
	createTemp := ops.createTemp
	ops.createTemp = func(dir, pattern string) (conflictTempFile, error) {
		created, err := createTemp(dir, pattern)
		if err != nil {
			return nil, err
		}
		faulting := &issue143FaultingTempFile{conflictTempFile: created}
		configure(faulting)
		return faulting, nil
	}
}

type issue143FileFixture struct {
	dir            string
	originalPath   string
	conflictPath   string
	legacyPath     string
	unrelatedPath  string
	originalBytes  []byte
	conflictBytes  []byte
	legacyBytes    []byte
	unrelatedBytes []byte
}

func issue143NewFileFixture(t *testing.T) issue143FileFixture {
	t.Helper()
	dir := t.TempDir()
	fixture := issue143FileFixture{
		dir:            dir,
		originalPath:   filepath.Join(dir, "doc.md"),
		conflictPath:   filepath.Join(dir, "doc.sync-conflict-20260406-100000-DEF5678.md"),
		legacyPath:     filepath.Join(dir, "doc.md.vaultsync-tmp"),
		unrelatedPath:  filepath.Join(dir, "unrelated-sentinel.md"),
		originalBytes:  []byte("issue-143-original-sentinel"),
		conflictBytes:  []byte("issue-143-conflict-sentinel"),
		legacyBytes:    []byte("issue-143-legacy-temp-sentinel"),
		unrelatedBytes: []byte("issue-143-unrelated-sentinel"),
	}

	files := []struct {
		name string
		path string
		data []byte
	}{
		{name: "original", path: fixture.originalPath, data: fixture.originalBytes},
		{name: "conflict", path: fixture.conflictPath, data: fixture.conflictBytes},
		{name: "legacy temp", path: fixture.legacyPath, data: fixture.legacyBytes},
		{name: "unrelated", path: fixture.unrelatedPath, data: fixture.unrelatedBytes},
	}
	for _, file := range files {
		if err := os.WriteFile(file.path, file.data, 0o600); err != nil {
			t.Fatalf("write %s: %v", file.name, err)
		}
		issue143AssertFileBytes(t, file.path, file.data)
	}

	return fixture
}

func issue143AssertPreCommitFixture(t *testing.T, fixture issue143FileFixture) {
	t.Helper()
	issue143AssertFileBytes(t, fixture.originalPath, fixture.originalBytes)
	issue143AssertFileBytes(t, fixture.conflictPath, fixture.conflictBytes)
	issue143AssertFileBytes(t, fixture.legacyPath, fixture.legacyBytes)
	issue143AssertFileBytes(t, fixture.unrelatedPath, fixture.unrelatedBytes)
}

func issue143AssertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", filepath.Base(path), err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%q bytes = %q, want %q", filepath.Base(path), got, want)
	}
}

func issue143AssertNoOperationTemps(t *testing.T, dir string) {
	t.Helper()
	temps := issue143OperationTemps(t, dir)
	if len(temps) != 0 {
		t.Errorf("successful resolution left VaultSync temporary files: %v", temps)
	}
}

func issue143OperationTemps(t *testing.T, dir string) []string {
	t.Helper()
	temps, err := filepath.Glob(filepath.Join(dir, conflictResolveTempPattern))
	if err != nil {
		t.Fatalf("glob VaultSync temporary files: %v", err)
	}
	return temps
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

func TestIsStateFilePath(t *testing.T) {
	cases := []struct {
		relPath string
		want    bool
	}{
		{".obsidian/workspace.json", true},
		{".obsidian/plugins/dataview/data.json", true},
		{"MyVault/.obsidian/app.json", true},
		{"MyVault/.obsidian/plugins/calendar/data.json", true},
		{"notes.md", false},
		{"Personal/diary.md", false},
		{".obsidian.md", false},
		{"docs/.obsidian-guide/readme.md", false},
	}
	for _, c := range cases {
		if got := isStateFilePath(c.relPath); got != c.want {
			t.Errorf("isStateFilePath(%q) = %v, want %v", c.relPath, got, c.want)
		}
	}
}

func TestAutoResolveStateConflicts(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	folderPath := filepath.Join(configDir, "autoresolve")
	if errMsg := AddFolder("autoresolve", "Auto Resolve", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	mustWrite := func(rel, content string) string {
		full := filepath.Join(folderPath, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
		return full
	}
	setMtime := func(path string, offsetSeconds int) {
		ts := time.Now().Add(time.Duration(offsetSeconds) * time.Second)
		if err := os.Chtimes(path, ts, ts); err != nil {
			t.Fatalf("chtimes %s: %v", path, err)
		}
	}

	// Case 1: original newer than copy -> copy discarded, original content kept.
	origNewer := mustWrite(".obsidian/workspace.json", "original-newer")
	copyOlder := mustWrite(".obsidian/workspace.sync-conflict-20260601-100000-AAA1111.json", "copy-older")
	setMtime(origNewer, 0)
	setMtime(copyOlder, -3600)

	// Case 2: copy newer than original -> copy promoted over original.
	origOlder := mustWrite("MyVault/.obsidian/app.json", "original-older")
	copyNewer := mustWrite("MyVault/.obsidian/app.sync-conflict-20260601-110000-BBB2222.json", "copy-newer")
	setMtime(origOlder, -3600)
	setMtime(copyNewer, 0)

	// Case 3: original missing -> copy promoted, no data loss.
	orphanCopy := mustWrite("MyVault/.obsidian/plugins/calendar/data.sync-conflict-20260601-120000-CCC3333.json", "orphan")
	_ = orphanCopy

	// Case 4: note conflict outside .obsidian -> untouched.
	noteOrig := mustWrite("MyVault/diary.md", "note-original")
	noteCopy := mustWrite("MyVault/diary.sync-conflict-20260601-130000-DDD4444.md", "note-copy")
	setMtime(noteOrig, -3600)
	setMtime(noteCopy, 0)

	got := AutoResolveStateConflicts("autoresolve")
	var res struct {
		Resolved int    `json:"resolved"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal([]byte(got), &res); err != nil {
		t.Fatalf("unmarshal %q: %v", got, err)
	}
	if res.Error != "" {
		t.Fatalf("error = %q, want empty", res.Error)
	}
	if res.Resolved != 3 {
		t.Errorf("resolved = %d, want 3", res.Resolved)
	}

	readFile := func(rel string) string {
		data, err := os.ReadFile(filepath.Join(folderPath, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		return string(data)
	}
	mustBeGone := func(rel string) {
		if _, err := os.Stat(filepath.Join(folderPath, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("%s still exists, want removed", rel)
		}
	}

	// Case 1: original kept, copy gone.
	if c := readFile(".obsidian/workspace.json"); c != "original-newer" {
		t.Errorf("workspace.json = %q, want 'original-newer'", c)
	}
	mustBeGone(".obsidian/workspace.sync-conflict-20260601-100000-AAA1111.json")

	// Case 2: copy promoted, copy file gone.
	if c := readFile("MyVault/.obsidian/app.json"); c != "copy-newer" {
		t.Errorf("app.json = %q, want 'copy-newer'", c)
	}
	mustBeGone("MyVault/.obsidian/app.sync-conflict-20260601-110000-BBB2222.json")

	// Case 3: orphan promoted to original.
	if c := readFile("MyVault/.obsidian/plugins/calendar/data.json"); c != "orphan" {
		t.Errorf("data.json = %q, want 'orphan'", c)
	}
	mustBeGone("MyVault/.obsidian/plugins/calendar/data.sync-conflict-20260601-120000-CCC3333.json")

	// Case 4: note conflict untouched.
	if c := readFile("MyVault/diary.md"); c != "note-original" {
		t.Errorf("diary.md = %q, want 'note-original'", c)
	}
	if c := readFile("MyVault/diary.sync-conflict-20260601-130000-DDD4444.md"); c != "note-copy" {
		t.Errorf("diary conflict copy = %q, want 'note-copy'", c)
	}

	// Idempotent: a second run finds nothing to resolve.
	got = AutoResolveStateConflicts("autoresolve")
	if err := json.Unmarshal([]byte(got), &res); err != nil {
		t.Fatalf("unmarshal second run %q: %v", got, err)
	}
	if res.Resolved != 0 || res.Error != "" {
		t.Errorf("second run = %+v, want resolved 0 and no error", res)
	}

	// Unknown folder -> error envelope.
	got = AutoResolveStateConflicts("nonexistent")
	if !strings.Contains(got, `"error":"folder not found"`) {
		t.Errorf("unknown folder result = %q, want 'folder not found'", got)
	}
}
