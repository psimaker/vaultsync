package bridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const issue150ConflictRecoveryUnavailable = "vaultsync-conflict-recovery-unavailable"

type issue150ConflictInspectionV2Payload struct {
	Version   int            `json:"version"`
	Conflicts []ConflictFile `json:"conflicts"`
	Complete  bool           `json:"complete"`
	Error     string         `json:"error"`
}

type issue150ReadFileContentV2Payload struct {
	Version int     `json:"version"`
	Content *string `json:"content"`
	Error   string  `json:"error"`
}

func issue150DecodeConflictInspectionV2(t *testing.T, raw string) issue150ConflictInspectionV2Payload {
	t.Helper()
	var result issue150ConflictInspectionV2Payload
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode V2 conflict inspection: %v (raw: %q)", err, raw)
	}
	return result
}

func issue150DecodeReadFileContentV2(t *testing.T, raw string) issue150ReadFileContentV2Payload {
	t.Helper()
	var result issue150ReadFileContentV2Payload
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode V2 file-content inspection: %v (raw: %q)", err, raw)
	}
	return result
}

type issue150RecoveryEntry struct {
	Mode    os.FileMode
	ModTime time.Time
	Content []byte
}

func issue150RecoverySnapshot(t *testing.T, root string) map[string]issue150RecoveryEntry {
	t.Helper()

	snapshot := make(map[string]issue150RecoveryEntry)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		item := issue150RecoveryEntry{
			Mode:    info.Mode(),
			ModTime: info.ModTime(),
		}
		if info.Mode().IsRegular() {
			item.Content, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		snapshot[rel] = item
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot recovery fixture: %v", err)
	}
	return snapshot
}

func TestIssue150ConflictRecoveryABIStubsAreStablePathFreeAndReadOnly(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	t.Cleanup(StopSyncthing)

	const folderID = "issue150-recovery-read-only"
	folderPath := filepath.Join(configDir, folderID)
	if errMsg := addFolderForTesting(folderID, "Issue 150 synthetic recovery", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}
	removeError := func(raw string) string {
		var result struct {
			Removed int    `json:"removed"`
			Error   string `json:"error"`
		}
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Errorf("decode recovery stub response: %v (raw: %q)", err, raw)
			return raw
		}
		if result.Removed != 0 {
			t.Errorf("removed = %d, want zero", result.Removed)
		}
		return result.Error
	}

	type recoveryCall struct {
		name                string
		conflictInspectable bool
		call                func(conflictName, originalName string) string
	}
	calls := []recoveryCall{
		{
			name: "ResolveConflict Keep This (#150)",
			call: func(conflictName, _ string) string {
				return ResolveConflict(folderID, conflictName, false)
			},
		},
		{
			name: "ResolveConflict Keep Other (#150)",
			call: func(conflictName, _ string) string {
				return ResolveConflict(folderID, conflictName, true)
			},
		},
		{
			name:                "KeepBothConflict (#150)",
			conflictInspectable: true,
			call: func(conflictName, _ string) string {
				return KeepBothConflict(folderID, conflictName)
			},
		},
		{
			name: "RemoveConflictFilesForOriginal Always Skip (#150)",
			call: func(_, originalName string) string {
				return removeError(RemoveConflictFilesForOriginal(folderID, originalName))
			},
		},
	}

	for index, testCase := range calls {
		t.Run(testCase.name, func(t *testing.T) {
			dirName := fmt.Sprintf("case-%d", index)
			dir := filepath.Join(folderPath, dirName)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("create recovery fixture: %v", err)
			}
			originalName := filepath.Join(dirName, "note.md")
			conflictName := filepath.Join(dirName, "note.sync-conflict-20260829-120000-ABC1234.md")
			fixtures := map[string][]byte{
				filepath.Join(folderPath, originalName):                     []byte("local version\n"),
				filepath.Join(folderPath, conflictName):                     []byte("other version\n"),
				filepath.Join(dir, ".stignore"):                             []byte("existing-rule\n"),
				filepath.Join(dir, ".syncthing.vaultsync-resolve-existing"): []byte("existing temporary bytes\n"),
			}
			for path, content := range fixtures {
				if err := os.WriteFile(path, content, 0o640); err != nil {
					t.Fatalf("write recovery fixture: %v", err)
				}
			}

			before := issue150RecoverySnapshot(t, dir)
			got := testCase.call(conflictName, originalName)
			if got != issue150ConflictRecoveryUnavailable {
				t.Errorf("error = %q, want fixed recovery-unavailable code", got)
			}
			for _, forbidden := range []string{folderID, folderPath, originalName, conflictName, "ABC1234"} {
				if strings.Contains(got, forbidden) {
					t.Errorf("fixed recovery error leaked input %q: %q", forbidden, got)
				}
			}
			if testCase.conflictInspectable {
				var conflicts []ConflictFile
				raw := GetConflictFilesJSON(folderID)
				if err := json.Unmarshal([]byte(raw), &conflicts); err != nil {
					t.Fatalf("decode conflict inspection response: %v (raw: %q)", err, raw)
				}
				found := false
				for _, conflict := range conflicts {
					if conflict.ConflictPath == conflictName {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("read-only Keep Both hid conflict %q from inspection", conflictName)
				}
			}
			after := issue150RecoverySnapshot(t, dir)
			if !reflect.DeepEqual(after, before) {
				t.Errorf("recovery ABI mutated filesystem or temporary entries:\nbefore=%#v\nafter=%#v", before, after)
			}
		})
	}

	invalidCalls := []struct {
		name string
		call func() string
	}{
		{
			name: "ResolveConflict Keep This traversal (#150)",
			call: func() string {
				return ResolveConflict(folderID, filepath.Join("..", "outside.sync-conflict-20260829-120000-ABC1234.md"), false)
			},
		},
		{
			name: "ResolveConflict Keep This invalid filename (#150)",
			call: func() string {
				return ResolveConflict(folderID, "not-a-conflict.md", false)
			},
		},
		{
			name: "ResolveConflict Keep Other traversal (#150)",
			call: func() string {
				return ResolveConflict(folderID, filepath.Join("..", "outside.sync-conflict-20260829-120000-ABC1234.md"), true)
			},
		},
		{
			name: "ResolveConflict Keep Other invalid filename (#150)",
			call: func() string {
				return ResolveConflict(folderID, "not-a-conflict.md", true)
			},
		},
		{
			name: "KeepBothConflict traversal (#150)",
			call: func() string {
				return KeepBothConflict(folderID, filepath.Join("..", "outside.sync-conflict-20260829-120000-ABC1234.md"))
			},
		},
		{
			name: "KeepBothConflict invalid filename (#150)",
			call: func() string {
				return KeepBothConflict(folderID, "not-a-conflict.md")
			},
		},
		{
			name: "RemoveConflictFilesForOriginal traversal (#150)",
			call: func() string {
				return removeError(RemoveConflictFilesForOriginal(folderID, filepath.Join("..", "outside.md")))
			},
		},
		{
			name: "RemoveConflictFilesForOriginal invalid root path (#150)",
			call: func() string {
				return removeError(RemoveConflictFilesForOriginal(folderID, "."))
			},
		},
	}
	for _, testCase := range invalidCalls {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.call(); got != issue150ConflictRecoveryUnavailable {
				t.Errorf("invalid-input error = %q, want context-independent fixed code", got)
			}
		})
	}

	StopSyncthing()
	for _, got := range []string{
		ResolveConflict("redaction-probe-folder", "redaction-probe/path.sync-conflict-20260829-120000-ABC1234.md", false),
		ResolveConflict("redaction-probe-folder", "redaction-probe/path.sync-conflict-20260829-120000-ABC1234.md", true),
		KeepBothConflict("redaction-probe-folder", "redaction-probe/path.sync-conflict-20260829-120000-ABC1234.md"),
	} {
		if got != issue150ConflictRecoveryUnavailable {
			t.Errorf("stopped-engine error = %q, want context-independent fixed code", got)
		}
	}
	if got := removeError(RemoveConflictFilesForOriginal("redaction-probe-folder", "redaction-probe/path.md")); got != issue150ConflictRecoveryUnavailable {
		t.Errorf("stopped-engine remove error = %q, want context-independent fixed code", got)
	}
}

func TestGetConflictFilesJSON(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	// Add a folder.
	folderPath := filepath.Join(configDir, "conflicttest")
	if errMsg := addFolderForTesting("conflicttest", "Conflict Test", folderPath); errMsg != "" {
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

	// The historical endpoint always returns an array, including unavailable.
	if got := GetConflictFilesJSON("nonexistent"); got != "[]" {
		t.Errorf("nonexistent legacy folder = %q, want []", got)
	}
}

func TestIssue150LegacyConflictInspectionWireShapeRemainsJSONArray(t *testing.T) {
	StopSyncthing()
	for _, raw := range []string{
		GetConflictFilesJSON("issue150-stopped-legacy-inspection"),
	} {
		var conflicts []ConflictFile
		if err := json.Unmarshal([]byte(raw), &conflicts); err != nil {
			t.Fatalf("legacy stopped-engine response is not a JSON array: %v (raw: %q)", err, raw)
		}
	}

	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	if raw := GetConflictFilesJSON("issue150-unknown-legacy-inspection"); raw != "[]" {
		t.Fatalf("legacy unknown-folder response = %q, want []", raw)
	}
}

func TestIssue150LegacyReadFileContentWireShapeRemainsRawOrErrorPrefixed(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}

	folderPath := filepath.Join(configDir, "issue150-legacy-read")
	if errMsg := addFolderForTesting("issue150-legacy-read", "Legacy Read", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}
	fixtures := map[string]string{
		"empty.md":        "",
		"error-prefix.md": "error:legitimate note content",
		"note.md":         "plain note content\n",
	}
	for name, content := range fixtures {
		if err := os.WriteFile(filepath.Join(folderPath, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		if got := ReadFileContent("issue150-legacy-read", name); got != content {
			t.Errorf("legacy content for %s = %q, want exact raw bytes %q", name, got, content)
		}
	}

	for _, raw := range []string{
		ReadFileContent("issue150-legacy-read", "missing-redaction-probe.md"),
		ReadFileContent("issue150-legacy-read", "../outside-redaction-probe.md"),
		ReadFileContent("issue150-unknown-legacy-read", "note.md"),
	} {
		if !strings.HasPrefix(raw, "error:") {
			t.Errorf("legacy read failure = %q, want error: prefix", raw)
		}
		for _, privateDetail := range []string{"missing-redaction-probe", "outside-redaction-probe", folderPath} {
			if strings.Contains(raw, privateDetail) {
				t.Errorf("legacy read failure leaked private detail %q: %q", privateDetail, raw)
			}
		}
	}
}

func TestIssue150ConflictInspectionV2DistinguishesCompleteEmptyListAndUnavailable(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}

	folderID := "issue150-v2-inspection"
	folderPath := filepath.Join(configDir, folderID)
	if errMsg := addFolderForTesting(folderID, "V2 Inspection", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	empty := issue150DecodeConflictInspectionV2(t, GetConflictFilesInspectionJSONV2(folderID))
	if empty.Version != 2 || !empty.Complete || empty.Error != "" || empty.Conflicts == nil || len(empty.Conflicts) != 0 {
		t.Fatalf("complete empty inspection = %+v, want versioned verified empty", empty)
	}

	conflictName := "note.sync-conflict-20260830-120000-ABC1234.md"
	if err := os.WriteFile(filepath.Join(folderPath, conflictName), []byte("other bytes"), 0o600); err != nil {
		t.Fatalf("write conflict fixture: %v", err)
	}
	complete := issue150DecodeConflictInspectionV2(t, GetConflictFilesInspectionJSONV2(folderID))
	if complete.Version != 2 || !complete.Complete || complete.Error != "" || len(complete.Conflicts) != 1 {
		t.Fatalf("complete conflict inspection = %+v, want exact complete list", complete)
	}
	if complete.Conflicts[0].ConflictPath != conflictName {
		t.Fatalf("conflict path = %q, want %q", complete.Conflicts[0].ConflictPath, conflictName)
	}

	if err := os.RemoveAll(folderPath); err != nil {
		t.Fatalf("remove inspection fixture: %v", err)
	}
	missing := issue150DecodeConflictInspectionV2(t, GetConflictFilesInspectionJSONV2(folderID))
	if missing.Version != 2 || missing.Complete || missing.Error != conflictInspectionUnavailableError || missing.Conflicts == nil || len(missing.Conflicts) != 0 {
		t.Fatalf("missing-path inspection = %+v, want explicit unavailable", missing)
	}

	for _, raw := range []string{
		GetConflictFilesInspectionJSONV2("issue150-v2-unknown"),
	} {
		unavailable := issue150DecodeConflictInspectionV2(t, raw)
		if unavailable.Version != 2 || unavailable.Complete || unavailable.Error != conflictInspectionUnavailableError || unavailable.Conflicts == nil || len(unavailable.Conflicts) != 0 {
			t.Fatalf("unknown inspection = %+v, want explicit unavailable", unavailable)
		}
		if strings.Contains(raw, "issue150-") || strings.Contains(raw, folderPath) {
			t.Fatalf("unavailable V2 inspection leaked private detail: %q", raw)
		}
	}
}

func TestIssue150ConflictInspectionV2KeepsPreLimitAndRejectsPostLimitEntries(t *testing.T) {
	root := t.TempDir()
	beforeName := "00-before.sync-conflict-20260830-120000-ABC1234.md"
	afterName := "02-after.sync-conflict-20260830-120001-XYZ9876.md"
	for _, name := range []string{beforeName, "01-ordinary.md", afterName} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatalf("write bounded inspection fixture %q: %v", name, err)
		}
	}

	result := inspectConflictFiles(root, conflictInspectionLimits{
		maxVisitedEntries:     2,
		maxCollectedConflicts: 10,
	}, filepath.WalkDir)
	if result.Version != 2 || result.Complete || result.Error != "" {
		t.Fatalf("visit-limited inspection = %+v, want versioned partial result", result)
	}
	if len(result.Conflicts) != 1 || result.Conflicts[0].ConflictPath != beforeName {
		t.Fatalf("partial conflicts = %+v, want only pre-limit %q", result.Conflicts, beforeName)
	}
	for _, conflict := range result.Conflicts {
		if conflict.ConflictPath == afterName {
			t.Fatalf("post-limit conflict was included: %+v", conflict)
		}
	}
}

func TestIssue150ConflictInspectionV2UsesIndependentCollectionBound(t *testing.T) {
	root := t.TempDir()
	firstName := "00-first.sync-conflict-20260830-120000-ABC1234.md"
	secondName := "01-second.sync-conflict-20260830-120001-XYZ9876.md"
	for _, name := range []string{firstName, secondName} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatalf("write collection-bound fixture %q: %v", name, err)
		}
	}

	result := inspectConflictFiles(root, conflictInspectionLimits{
		maxVisitedEntries:     10,
		maxCollectedConflicts: 1,
	}, filepath.WalkDir)
	if result.Version != 2 || result.Complete || result.Error != "" {
		t.Fatalf("collection-limited inspection = %+v, want versioned partial result", result)
	}
	if len(result.Conflicts) != 1 || result.Conflicts[0].ConflictPath != firstName {
		t.Fatalf("collection-limited conflicts = %+v, want only %q", result.Conflicts, firstName)
	}
}

func TestIssue150ConflictInspectionV2ClassifiesPreEntryWalkFailureAsUnavailable(t *testing.T) {
	root := t.TempDir()
	walk := func(root string, visit fs.WalkDirFunc) error {
		info, err := os.Stat(root)
		if err != nil {
			t.Fatalf("stat synthetic inspection root: %v", err)
		}
		if err := visit(root, fs.FileInfoToDirEntry(info), nil); err != nil {
			return err
		}
		return errors.New("synthetic pre-entry walk failure")
	}

	result := inspectConflictFiles(root, conflictInspectionLimits{
		maxVisitedEntries:     10,
		maxCollectedConflicts: 10,
	}, walk)
	if result.Version != 2 || result.Complete || result.Error != conflictInspectionUnavailableError || result.Conflicts == nil || len(result.Conflicts) != 0 {
		t.Fatalf("pre-entry walk failure = %+v, want explicit unavailable", result)
	}
}

func TestIssue150ReadFileContentV2DistinguishesEmptyErrorPrefixedAndUnavailable(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}

	folderID := "issue150-v2-read"
	folderPath := filepath.Join(configDir, folderID)
	if errMsg := addFolderForTesting(folderID, "V2 Read", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}
	fixtures := map[string]string{
		"empty.md":        "",
		"error-prefix.md": "error:legitimate note content",
	}
	for name, content := range fixtures {
		if err := os.WriteFile(filepath.Join(folderPath, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write V2 read fixture %q: %v", name, err)
		}
		got := issue150DecodeReadFileContentV2(t, ReadFileContentJSONV2(folderID, name))
		if got.Version != 2 || got.Content == nil || *got.Content != content || got.Error != "" {
			t.Fatalf("V2 read for %q = %+v, want exact content", name, got)
		}
	}

	for _, raw := range []string{
		ReadFileContentJSONV2(folderID, "missing-redaction-probe.md"),
		ReadFileContentJSONV2(folderID, "../outside-redaction-probe.md"),
		ReadFileContentJSONV2("issue150-v2-unknown", "missing-redaction-probe.md"),
	} {
		got := issue150DecodeReadFileContentV2(t, raw)
		if got.Version != 2 || got.Content != nil || got.Error != conflictInspectionUnavailableError {
			t.Fatalf("V2 unavailable read = %+v, want fixed unavailable", got)
		}
		for _, privateDetail := range []string{"missing-redaction-probe", "outside-redaction-probe", folderPath} {
			if strings.Contains(raw, privateDetail) {
				t.Fatalf("V2 unavailable read leaked private detail %q: %q", privateDetail, raw)
			}
		}
	}
}

func TestReadFileContent(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	folderPath := filepath.Join(configDir, "readtest")
	if errMsg := addFolderForTesting("readtest", "Read Test", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	content := "# Hello World\n\nThis is a test."
	os.WriteFile(filepath.Join(folderPath, "test.md"), []byte(content), 0o644)

	type result struct {
		Content *string `json:"content"`
		Error   string  `json:"error"`
	}
	decode := func(raw string) result {
		t.Helper()
		var got result
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("decode inspection result %q: %v", raw, err)
		}
		return got
	}

	got := decode(ReadFileContentJSONV2("readtest", "test.md"))
	if got.Content == nil || *got.Content != content || got.Error != "" {
		t.Errorf("ReadFileContent = %+v, want exact content", got)
	}

	// Inspection failures return only the stable path-free code.
	if got := decode(ReadFileContentJSONV2("readtest", "nope.md")); got.Content != nil || got.Error != conflictInspectionUnavailableError {
		t.Errorf("nonexistent file = %+v, want unavailable", got)
	}

	if got := decode(ReadFileContentJSONV2("readtest", "../../etc/passwd")); got.Content != nil || got.Error != conflictInspectionUnavailableError {
		t.Errorf("path traversal = %+v, want unavailable", got)
	}

	if got := decode(ReadFileContentJSONV2("nonexistent", "test.md")); got.Content != nil || got.Error != conflictInspectionUnavailableError {
		t.Errorf("nonexistent folder = %+v, want unavailable", got)
	}
}

func TestIssue150ConflictInspectionFailureIsNotAnEmptySuccess(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	folderPath := filepath.Join(configDir, "issue150-inspection-unavailable")
	if errMsg := addFolderForTesting("issue150-inspection-unavailable", "Inspection", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}
	if err := os.RemoveAll(folderPath); err != nil {
		t.Fatalf("remove isolated fixture folder: %v", err)
	}

	assertUnavailable := func(label, raw string) {
		t.Helper()
		got := issue150DecodeConflictInspectionV2(t, raw)
		if got.Complete || got.Error != conflictInspectionUnavailableError || len(got.Conflicts) != 0 {
			t.Fatalf("%s inspection = %+v, want fixed unavailable", label, got)
		}
	}
	assertUnavailable("missing-folder", GetConflictFilesInspectionJSONV2("issue150-inspection-unavailable"))
	assertUnavailable("unknown-folder", GetConflictFilesInspectionJSONV2("issue150-unknown-folder"))
	StopSyncthing()
	assertUnavailable("stopped-engine", GetConflictFilesInspectionJSONV2("issue150-inspection-unavailable"))
}

func TestIssue150ConflictInspectionDistinguishesEmptyContentAndUnavailableWithoutDetails(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	folderPath := filepath.Join(configDir, "issue150-content-inspection")
	if errMsg := addFolderForTesting("issue150-content-inspection", "Inspection", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	type result struct {
		Content *string `json:"content"`
		Error   string  `json:"error"`
	}
	decode := func(raw string) result {
		t.Helper()
		var got result
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("decode inspection result %q: %v", raw, err)
		}
		return got
	}

	fixtures := map[string]string{
		"empty.md":        "",
		"error-prefix.md": "error:this is legitimate note content",
	}
	for name, content := range fixtures {
		if err := os.WriteFile(filepath.Join(folderPath, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		got := decode(ReadFileContentJSONV2("issue150-content-inspection", name))
		if got.Content == nil || *got.Content != content || got.Error != "" {
			t.Fatalf("successful inspection for %q = %+v, want exact content", name, got)
		}
	}

	for _, raw := range []string{
		ReadFileContentJSONV2("issue150-content-inspection", "missing-redaction-probe.md"),
		ReadFileContentJSONV2("issue150-content-inspection", "../outside-redaction-probe.md"),
		ReadFileContentJSONV2("issue150-unknown-folder", "missing-redaction-probe.md"),
	} {
		got := decode(raw)
		if got.Content != nil || got.Error != conflictInspectionUnavailableError {
			t.Fatalf("failed inspection = %+v, want fixed unavailable result", got)
		}
		for _, sensitiveDetail := range []string{"missing-redaction-probe", "outside-redaction-probe", folderPath} {
			if strings.Contains(raw, sensitiveDetail) {
				t.Fatalf("failed inspection leaked private detail in %q", raw)
			}
		}
	}
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

func TestIssue144KeepBothSuccessPreservesAllContents(t *testing.T) {
	dir := t.TempDir()
	conflictName := "doc.sync-conflict-20260406-100000-DEF5678.md"
	originalPath := filepath.Join(dir, "doc.md")
	conflictPath := filepath.Join(dir, conflictName)
	targetPath := filepath.Join(dir, "doc.conflict-DEF5678.md")
	unrelatedPath := filepath.Join(dir, "unrelated.md")
	originalBytes := []byte("issue-144-success-original")
	conflictBytes := []byte("issue-144-success-conflict")
	unrelatedBytes := []byte("issue-144-success-unrelated")

	issue144WriteAndReadBack(t, originalPath, originalBytes)
	issue144WriteAndReadBack(t, conflictPath, conflictBytes)
	issue144WriteAndReadBack(t, unrelatedPath, unrelatedBytes)

	if errMsg := keepBothConflictFile(conflictPath, conflictName, systemKeepBothFileOperations()); errMsg != "" {
		t.Fatalf("keepBothConflictFile failed: %s", errMsg)
	}

	issue144AssertFileBytes(t, originalPath, originalBytes)
	issue144AssertPathMissing(t, conflictPath)
	issue144AssertFileBytes(t, targetPath, conflictBytes)
	issue144AssertFileBytes(t, unrelatedPath, unrelatedBytes)
}

func TestIssue144KeepBothParallelCollisionPreservesAllContents(t *testing.T) {
	folderPath := t.TempDir()
	originalPath := filepath.Join(folderPath, "doc.md")
	unrelatedPath := filepath.Join(folderPath, "unrelated.md")
	targetPath := filepath.Join(folderPath, "doc.conflict-DEF5678.md")
	originalBytes := []byte("issue-144-parallel-original")
	unrelatedBytes := []byte("issue-144-parallel-unrelated")

	type parallelCandidate struct {
		name string
		path string
		data []byte
	}
	candidates := []parallelCandidate{
		{
			name: "doc.sync-conflict-20260406-100000-DEF5678.md",
			data: []byte("issue-144-parallel-first"),
		},
		{
			name: "doc.sync-conflict-20260406-100001-DEF5678.md",
			data: []byte("issue-144-parallel-second"),
		},
	}
	for i := range candidates {
		candidates[i].path = filepath.Join(folderPath, candidates[i].name)
		issue144WriteAndReadBack(t, candidates[i].path, candidates[i].data)
	}
	issue144WriteAndReadBack(t, originalPath, originalBytes)
	issue144WriteAndReadBack(t, unrelatedPath, unrelatedBytes)

	type result struct {
		candidate parallelCandidate
		errMsg    string
	}
	start := make(chan struct{})
	results := make(chan result, len(candidates))
	var workers sync.WaitGroup
	for _, candidate := range candidates {
		workers.Add(1)
		go func(candidate parallelCandidate) {
			defer workers.Done()
			<-start
			results <- result{
				candidate: candidate,
				errMsg: keepBothConflictFile(
					candidate.path,
					candidate.name,
					systemKeepBothFileOperations(),
				),
			}
		}(candidate)
	}
	close(start)
	workers.Wait()
	close(results)

	var winner, loser parallelCandidate
	successes := 0
	collisions := 0
	for result := range results {
		switch result.errMsg {
		case "":
			successes++
			winner = result.candidate
		case keepBothTargetExistsError:
			collisions++
			loser = result.candidate
		default:
			t.Errorf("parallel KeepBothConflict error = %q", result.errMsg)
		}
	}
	if successes != 1 || collisions != 1 {
		t.Fatalf("parallel results: successes=%d collisions=%d, want 1 each", successes, collisions)
	}

	issue144AssertFileBytes(t, originalPath, originalBytes)
	issue144AssertFileBytes(t, unrelatedPath, unrelatedBytes)
	issue144AssertFileBytes(t, targetPath, winner.data)
	issue144AssertPathMissing(t, winner.path)
	issue144AssertFileBytes(t, loser.path, loser.data)
}

func TestIssue144KeepBothRacingTargetCreationPreservesAllContents(t *testing.T) {
	dir := t.TempDir()
	conflictName := "doc.sync-conflict-20260406-100000-DEF5678.md"
	conflictPath := filepath.Join(dir, conflictName)
	targetPath := filepath.Join(dir, "doc.conflict-DEF5678.md")
	conflictBytes := []byte("issue-144-racing-source")
	foreignBytes := []byte("issue-144-racing-foreign-target")
	issue144WriteAndReadBack(t, conflictPath, conflictBytes)

	ops := systemKeepBothFileOperations()
	nativeRenameNoReplace := ops.renameNoReplace
	ops.renameNoReplace = func(oldPath, newPath string) error {
		if oldPath != conflictPath || newPath != targetPath {
			t.Errorf("rename paths = (%q, %q), want (%q, %q)", oldPath, newPath, conflictPath, targetPath)
		}
		file, err := os.OpenFile(newPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.Fatalf("create racing target: %v", err)
		}
		if _, err := file.Write(foreignBytes); err != nil {
			_ = file.Close()
			t.Fatalf("write racing target: %v", err)
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			t.Fatalf("sync racing target: %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("close racing target: %v", err)
		}
		return nativeRenameNoReplace(oldPath, newPath)
	}

	if errMsg := keepBothConflictFile(conflictPath, conflictName, ops); errMsg != keepBothTargetExistsError {
		t.Fatalf("racing Keep Both error = %q, want %q", errMsg, keepBothTargetExistsError)
	}
	issue144AssertFileBytes(t, conflictPath, conflictBytes)
	issue144AssertFileBytes(t, targetPath, foreignBytes)
}

func TestIssue144KeepBothOperationFailuresPreserveAllContents(t *testing.T) {
	t.Run("inspection I/O error", func(t *testing.T) {
		dir := t.TempDir()
		conflictName := "doc.sync-conflict-20260406-100000-DEF5678.md"
		conflictPath := filepath.Join(dir, conflictName)
		targetPath := filepath.Join(dir, "doc.conflict-DEF5678.md")
		conflictBytes := []byte("issue-144-inspection-io-source")
		issue144WriteAndReadBack(t, conflictPath, conflictBytes)

		renameCalled := false
		ops := keepBothFileOperations{
			stat: func(string) (os.FileInfo, error) {
				return nil, &os.PathError{Op: "stat", Path: conflictPath, Err: syscall.EIO}
			},
			renameNoReplace: func(string, string) error {
				renameCalled = true
				return nil
			},
		}
		errMsg := keepBothConflictFile(conflictPath, conflictName, ops)
		if !strings.HasPrefix(errMsg, "inspect conflict file: ") || !strings.Contains(errMsg, syscall.EIO.Error()) {
			t.Errorf("inspection error = %q, want path-free EIO", errMsg)
		}
		if strings.Contains(errMsg, conflictPath) {
			t.Errorf("inspection error leaked conflict path: %q", errMsg)
		}
		if renameCalled {
			t.Error("rename was called after inspection failure")
		}
		issue144AssertFileBytes(t, conflictPath, conflictBytes)
		issue144AssertPathMissing(t, targetPath)
	})

	operationErrors := []struct {
		name          string
		injectedError error
		wantExact     string
	}{
		{name: "I/O error", injectedError: syscall.EIO},
		{name: "ENOSPC", injectedError: syscall.ENOSPC},
		{
			name:          "unsupported filesystem",
			injectedError: errNoReplaceUnsupported,
			wantExact:     errNoReplaceUnsupported.Error(),
		},
	}
	for _, testCase := range operationErrors {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			conflictName := "doc.sync-conflict-20260406-100000-DEF5678.md"
			originalPath := filepath.Join(dir, "doc.md")
			conflictPath := filepath.Join(dir, conflictName)
			targetPath := filepath.Join(dir, "doc.conflict-DEF5678.md")
			unrelatedPath := filepath.Join(dir, "unrelated.md")
			originalBytes := []byte("issue-144-error-original")
			conflictBytes := []byte("issue-144-error-conflict")
			unrelatedBytes := []byte("issue-144-error-unrelated")
			issue144WriteAndReadBack(t, originalPath, originalBytes)
			issue144WriteAndReadBack(t, conflictPath, conflictBytes)
			issue144WriteAndReadBack(t, unrelatedPath, unrelatedBytes)

			renameCalls := 0
			ops := systemKeepBothFileOperations()
			ops.renameNoReplace = func(oldPath, newPath string) error {
				renameCalls++
				if oldPath != conflictPath || newPath != targetPath {
					t.Errorf("rename paths = (%q, %q), want (%q, %q)", oldPath, newPath, conflictPath, targetPath)
				}
				return testCase.injectedError
			}
			errMsg := keepBothConflictFile(conflictPath, conflictName, ops)
			if testCase.wantExact != "" {
				if errMsg != testCase.wantExact {
					t.Errorf("mutation error = %q, want %q", errMsg, testCase.wantExact)
				}
			} else if !strings.HasPrefix(errMsg, "rename conflict file without replacing target: ") || !strings.Contains(errMsg, testCase.injectedError.Error()) {
				t.Errorf("mutation error = %q, want stable prefix and %q", errMsg, testCase.injectedError.Error())
			}
			if renameCalls != 1 {
				t.Errorf("rename calls = %d, want 1", renameCalls)
			}
			issue144AssertFileBytes(t, originalPath, originalBytes)
			issue144AssertFileBytes(t, conflictPath, conflictBytes)
			issue144AssertFileBytes(t, unrelatedPath, unrelatedBytes)
			issue144AssertPathMissing(t, targetPath)
		})
	}
}

func TestIssue144KeepBothPreservesUnexpectedTargetNodes(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		dir := t.TempDir()
		conflictName := "directory.sync-conflict-20260406-100000-DEF5678.md"
		conflictPath := filepath.Join(dir, conflictName)
		targetPath := filepath.Join(dir, "directory.conflict-DEF5678.md")
		childPath := filepath.Join(targetPath, "sentinel.md")
		conflictBytes := []byte("issue-144-target-directory-conflict")
		childBytes := []byte("issue-144-target-directory-child")
		issue144WriteAndReadBack(t, conflictPath, conflictBytes)
		if err := os.Mkdir(targetPath, 0o700); err != nil {
			t.Fatalf("create target directory: %v", err)
		}
		issue144WriteAndReadBack(t, childPath, childBytes)

		if errMsg := keepBothConflictFile(conflictPath, conflictName, systemKeepBothFileOperations()); errMsg != keepBothTargetExistsError {
			t.Fatalf("directory target error = %q, want %q", errMsg, keepBothTargetExistsError)
		}
		issue144AssertFileBytes(t, conflictPath, conflictBytes)
		issue144AssertFileBytes(t, childPath, childBytes)
	})

	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		externalDir := t.TempDir()
		conflictName := "symlink.sync-conflict-20260406-100000-DEF5678.md"
		conflictPath := filepath.Join(dir, conflictName)
		targetPath := filepath.Join(dir, "symlink.conflict-DEF5678.md")
		externalPath := filepath.Join(externalDir, "external.md")
		conflictBytes := []byte("issue-144-target-symlink-conflict")
		externalBytes := []byte("issue-144-target-symlink-external")
		issue144WriteAndReadBack(t, conflictPath, conflictBytes)
		issue144WriteAndReadBack(t, externalPath, externalBytes)
		if err := os.Symlink(externalPath, targetPath); err != nil {
			t.Fatalf("create target symlink: %v", err)
		}

		if errMsg := keepBothConflictFile(conflictPath, conflictName, systemKeepBothFileOperations()); errMsg != keepBothTargetExistsError {
			t.Fatalf("symlink target error = %q, want %q", errMsg, keepBothTargetExistsError)
		}
		issue144AssertFileBytes(t, conflictPath, conflictBytes)
		linkTarget, err := os.Readlink(targetPath)
		if err != nil {
			t.Fatalf("read target symlink: %v", err)
		}
		if linkTarget != externalPath {
			t.Errorf("target symlink destination = %q, want %q", linkTarget, externalPath)
		}
		issue144AssertFileBytes(t, externalPath, externalBytes)
	})
}

func issue144StartFolder(t *testing.T, folderID string) string {
	t.Helper()
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	t.Cleanup(func() { StopSyncthing() })

	folderPath := filepath.Join(configDir, folderID)
	if errMsg := addFolderForTesting(folderID, "Issue 144 Keep Both", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}
	return folderPath
}

func issue144WriteAndReadBack(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %q: %v", filepath.Base(path), err)
	}
	issue144AssertFileBytes(t, path, data)
}

func issue144AssertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", filepath.Base(path), err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%q bytes = %q, want %q", filepath.Base(path), got, want)
	}
}

func issue144AssertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("%q exists or lstat failed: %v", filepath.Base(path), err)
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

func TestIssue145AutoResolveStateConflictsPreservesLegacyScenarios(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	const folderID = "issue145-legacy"
	folderPath := filepath.Join(configDir, folderID)
	if errMsg := addFolderForTesting(folderID, "Issue 145 Legacy Scenarios", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	type scenario struct {
		name            string
		originalRelPath string
		conflictRelPath string
		conflictDate    string
		deviceShortID   string
		createOriginal  bool
		originalBytes   []byte
		conflictBytes   []byte
		originalMtime   time.Time
		conflictMtime   time.Time
	}

	baseMtime := time.Date(2026, time.August, 16, 10, 0, 0, 0, time.UTC)
	scenarios := []scenario{
		{
			name:            "older conflict",
			originalRelPath: ".obsidian/older.json",
			conflictRelPath: ".obsidian/older.sync-conflict-20260816-100000-OLD0001.json",
			conflictDate:    "20260816-100000",
			deviceShortID:   "OLD0001",
			createOriginal:  true,
			originalBytes:   []byte("issue-145-older-original\x00"),
			conflictBytes:   []byte("issue-145-older-conflict\x00"),
			originalMtime:   baseMtime.Add(2 * time.Hour),
			conflictMtime:   baseMtime,
		},
		{
			name:            "newer conflict",
			originalRelPath: "MyVault/.obsidian/newer.json",
			conflictRelPath: "MyVault/.obsidian/newer.sync-conflict-20260816-110000-NEW0001.json",
			conflictDate:    "20260816-110000",
			deviceShortID:   "NEW0001",
			createOriginal:  true,
			originalBytes:   []byte("issue-145-newer-original\x00"),
			conflictBytes:   []byte("issue-145-newer-conflict\x00"),
			originalMtime:   baseMtime,
			conflictMtime:   baseMtime.Add(2 * time.Hour),
		},
		{
			name:            "equal mtimes",
			originalRelPath: ".obsidian/equal.json",
			conflictRelPath: ".obsidian/equal.sync-conflict-20260816-120000-EQL0001.json",
			conflictDate:    "20260816-120000",
			deviceShortID:   "EQL0001",
			createOriginal:  true,
			originalBytes:   []byte("issue-145-equal-original\x00"),
			conflictBytes:   []byte("issue-145-equal-conflict\x00"),
			originalMtime:   baseMtime.Add(4 * time.Hour),
			conflictMtime:   baseMtime.Add(4 * time.Hour),
		},
		{
			name:            "future clock skew",
			originalRelPath: "MyVault/.obsidian/clock-skew.json",
			conflictRelPath: "MyVault/.obsidian/clock-skew.sync-conflict-20260816-130000-CLK0001.json",
			conflictDate:    "20260816-130000",
			deviceShortID:   "CLK0001",
			createOriginal:  true,
			originalBytes:   []byte("issue-145-clock-skew-original\x00"),
			conflictBytes:   []byte("issue-145-clock-skew-conflict\x00"),
			originalMtime:   baseMtime,
			conflictMtime:   time.Date(2046, time.August, 16, 10, 0, 0, 0, time.UTC),
		},
		{
			name:            "missing original",
			originalRelPath: ".obsidian/missing.json",
			conflictRelPath: ".obsidian/missing.sync-conflict-20260816-140000-MIS0001.json",
			conflictDate:    "20260816-140000",
			deviceShortID:   "MIS0001",
			createOriginal:  false,
			conflictBytes:   []byte("issue-145-missing-conflict\x00"),
			conflictMtime:   baseMtime.Add(6 * time.Hour),
		},
	}

	originalsBefore := make(map[string][]byte)
	conflictsBefore := make(map[string][]byte, len(scenarios))
	expectedConflicts := make(map[string]ConflictFile, len(scenarios))

	writeAndReadFixture := func(label, path string, data []byte, mtime time.Time) []byte {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create directory for %s: %v", label, err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write %s: %v", label, err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatalf("set %s mtime: %v", label, err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read back %s: %v", label, err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("%s bytes = %q, want %q", label, got, data)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", label, err)
		}
		if !info.ModTime().Equal(mtime) {
			t.Fatalf("%s mtime = %s, want %s", label, info.ModTime(), mtime)
		}
		return append([]byte(nil), got...)
	}

	for _, scenario := range scenarios {
		originalRelPath := filepath.FromSlash(scenario.originalRelPath)
		conflictRelPath := filepath.FromSlash(scenario.conflictRelPath)
		originalPath := filepath.Join(folderPath, originalRelPath)
		conflictPath := filepath.Join(folderPath, conflictRelPath)
		if scenario.createOriginal {
			if bytes.Equal(scenario.originalBytes, scenario.conflictBytes) {
				t.Fatalf("%s fixture bytes must be distinct", scenario.name)
			}
			originalsBefore[originalPath] = writeAndReadFixture(
				scenario.name+" original",
				originalPath,
				scenario.originalBytes,
				scenario.originalMtime,
			)
		} else if _, err := os.Lstat(originalPath); !os.IsNotExist(err) {
			t.Fatalf("%s original must be absent before resolver: %v", scenario.name, err)
		}
		conflictsBefore[conflictPath] = writeAndReadFixture(
			scenario.name+" conflict",
			conflictPath,
			scenario.conflictBytes,
			scenario.conflictMtime,
		)
		expectedConflicts[conflictRelPath] = ConflictFile{
			OriginalPath:  originalRelPath,
			ConflictPath:  conflictRelPath,
			ConflictDate:  scenario.conflictDate,
			DeviceShortID: scenario.deviceShortID,
		}
	}

	decodeConflicts := func(stage string) ([]ConflictFile, string) {
		t.Helper()
		raw := GetConflictFilesJSON(folderID)
		var conflicts []ConflictFile
		if err := json.Unmarshal([]byte(raw), &conflicts); err != nil {
			t.Fatalf("decode conflicts %s resolver: %v (raw: %s)", stage, err, raw)
		}
		return conflicts, raw
	}
	conflictSetMatches := func(conflicts []ConflictFile) bool {
		if len(conflicts) != len(expectedConflicts) {
			return false
		}
		seen := make(map[string]struct{}, len(conflicts))
		for _, conflict := range conflicts {
			expected, exists := expectedConflicts[conflict.ConflictPath]
			if !exists || conflict != expected {
				return false
			}
			seen[conflict.ConflictPath] = struct{}{}
		}
		return len(seen) == len(expectedConflicts)
	}

	visibleBefore, visibleBeforeJSON := decodeConflicts("before")
	if !conflictSetMatches(visibleBefore) {
		t.Fatalf("conflicts before resolver = %s, want exact fixture set %+v", visibleBeforeJSON, expectedConflicts)
	}

	type autoResolveResult struct {
		Resolved int    `json:"resolved"`
		Error    string `json:"error"`
	}
	decodeResult := func(label, raw string) autoResolveResult {
		t.Helper()
		var result autoResolveResult
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatalf("decode %s result: %v (raw: %s)", label, err, raw)
		}
		return result
	}

	resolverJSON := AutoResolveStateConflicts(folderID)
	resolverResult := decodeResult("valid folder", resolverJSON)
	if resolverResult != (autoResolveResult{}) {
		t.Errorf("valid-folder result = %+v, want resolved 0 and no error (raw: %s)", resolverResult, resolverJSON)
	}

	assertFilePreserved := func(label, path string, want []byte) {
		t.Helper()
		got, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s after resolver: %v", label, err)
			return
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s bytes after resolver = %q, want unchanged %q", label, got, want)
		}
	}
	for _, scenario := range scenarios {
		originalPath := filepath.Join(folderPath, filepath.FromSlash(scenario.originalRelPath))
		conflictPath := filepath.Join(folderPath, filepath.FromSlash(scenario.conflictRelPath))
		if scenario.createOriginal {
			assertFilePreserved(scenario.name+" original", originalPath, originalsBefore[originalPath])
		} else if _, err := os.Lstat(originalPath); !os.IsNotExist(err) {
			t.Errorf("%s original was created or cannot be inspected after resolver: %v", scenario.name, err)
		}
		assertFilePreserved(scenario.name+" conflict", conflictPath, conflictsBefore[conflictPath])
	}

	visibleAfter, visibleAfterJSON := decodeConflicts("after")
	if !conflictSetMatches(visibleAfter) {
		t.Errorf("conflicts after resolver = %s, want unchanged fixture set %+v", visibleAfterJSON, expectedConflicts)
	}

	unknownJSON := AutoResolveStateConflicts("nonexistent")
	unknownResult := decodeResult("unknown folder", unknownJSON)
	if unknownResult != (autoResolveResult{Error: "folder not found"}) {
		t.Errorf("unknown-folder result = %+v, want folder-not-found envelope (raw: %s)", unknownResult, unknownJSON)
	}

	StopSyncthing()
	stoppedJSON := AutoResolveStateConflicts(folderID)
	stoppedResult := decodeResult("stopped engine", stoppedJSON)
	if stoppedResult != (autoResolveResult{Error: "syncthing not running"}) {
		t.Errorf("stopped-engine result = %+v, want not-running envelope (raw: %s)", stoppedResult, stoppedJSON)
	}
}

func TestIssue145AutoResolveStateConflictsPreservesAllFiles(t *testing.T) {
	const (
		folderID           = "issue145-preserve"
		stateDirectoryName = ".obsidian"
		originalName       = "workspace.json"
		conflictName       = "workspace.sync-conflict-20260816-120000-ABC1234.json"
	)

	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	folderPath := filepath.Join(configDir, folderID)
	if errMsg := addFolderForTesting(folderID, "Issue 145 Preserve", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	if matches := conflictPattern.FindStringSubmatch(conflictName); matches == nil {
		t.Fatalf("conflict fixture %q does not match Syncthing conflict pattern", conflictName)
	}

	stateDir := filepath.Join(folderPath, stateDirectoryName)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("create .obsidian fixture directory: %v", err)
	}
	originalPath := filepath.Join(stateDir, originalName)
	conflictPath := filepath.Join(stateDir, conflictName)
	originalBytes := []byte("{\"source\":\"original-newer\",\"payload\":\"original-bytes\\u0000\"}\n")
	conflictBytes := []byte("{\"source\":\"conflict-older\",\"payload\":\"conflict-bytes\\u0000\"}\n")
	if bytes.Equal(originalBytes, conflictBytes) {
		t.Fatal("fixture contents must be distinct")
	}
	if err := os.WriteFile(originalPath, originalBytes, 0o644); err != nil {
		t.Fatalf("write original fixture: %v", err)
	}
	if err := os.WriteFile(conflictPath, conflictBytes, 0o644); err != nil {
		t.Fatalf("write conflict fixture: %v", err)
	}

	conflictMtime := time.Date(2026, time.August, 16, 10, 0, 0, 0, time.UTC)
	originalMtime := conflictMtime.Add(2 * time.Hour)
	if err := os.Chtimes(originalPath, originalMtime, originalMtime); err != nil {
		t.Fatalf("set original mtime: %v", err)
	}
	if err := os.Chtimes(conflictPath, conflictMtime, conflictMtime); err != nil {
		t.Fatalf("set conflict mtime: %v", err)
	}

	readFixture := func(label, path string, want []byte) []byte {
		t.Helper()
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s fixture before resolver: %v", label, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s fixture before resolver = %q, want exact bytes %q", label, got, want)
		}
		return got
	}
	originalBefore := readFixture("original", originalPath, originalBytes)
	conflictBefore := readFixture("conflict", conflictPath, conflictBytes)

	originalInfo, err := os.Stat(originalPath)
	if err != nil {
		t.Fatalf("stat original fixture before resolver: %v", err)
	}
	conflictInfo, err := os.Stat(conflictPath)
	if err != nil {
		t.Fatalf("stat conflict fixture before resolver: %v", err)
	}
	if !originalInfo.ModTime().Equal(originalMtime) {
		t.Fatalf("original mtime = %s, want exact fixture time %s", originalInfo.ModTime(), originalMtime)
	}
	if !conflictInfo.ModTime().Equal(conflictMtime) {
		t.Fatalf("conflict mtime = %s, want exact fixture time %s", conflictInfo.ModTime(), conflictMtime)
	}
	if !originalInfo.ModTime().After(conflictInfo.ModTime()) {
		t.Fatalf("original mtime %s must be newer than conflict mtime %s", originalInfo.ModTime(), conflictInfo.ModTime())
	}

	expectedConflict := ConflictFile{
		OriginalPath:  filepath.Join(stateDirectoryName, originalName),
		ConflictPath:  filepath.Join(stateDirectoryName, conflictName),
		ConflictDate:  "20260816-120000",
		DeviceShortID: "ABC1234",
	}
	decodeConflicts := func(stage string) ([]ConflictFile, string) {
		t.Helper()
		raw := GetConflictFilesJSON(folderID)
		var conflicts []ConflictFile
		if err := json.Unmarshal([]byte(raw), &conflicts); err != nil {
			t.Fatalf("decode conflicts %s resolver: %v (raw: %s)", stage, err, raw)
		}
		return conflicts, raw
	}
	conflictsBefore, rawConflictsBefore := decodeConflicts("before")
	if len(conflictsBefore) != 1 || conflictsBefore[0] != expectedConflict {
		t.Fatalf("conflict scan before resolver = %s, want exactly %+v", rawConflictsBefore, expectedConflict)
	}

	resolverJSON := AutoResolveStateConflicts(folderID)
	var resolverResult struct {
		Resolved int    `json:"resolved"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal([]byte(resolverJSON), &resolverResult); err != nil {
		t.Fatalf("decode AutoResolveStateConflicts result: %v (raw: %s)", err, resolverJSON)
	}
	if resolverResult.Error != "" {
		t.Fatalf("AutoResolveStateConflicts returned error %q (raw: %s)", resolverResult.Error, resolverJSON)
	}

	assertPreserved := func(label, path string, want []byte) {
		t.Helper()
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s path must still exist after AutoResolveStateConflicts: %v (resolver: %s)", label, err, resolverJSON)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s after AutoResolveStateConflicts: %v (resolver: %s)", label, err, resolverJSON)
			return
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s bytes after AutoResolveStateConflicts = %q, want unchanged %q (resolver: %s)", label, got, want, resolverJSON)
		}
	}
	assertPreserved("original", originalPath, originalBefore)
	assertPreserved("conflict", conflictPath, conflictBefore)

	conflictsAfter, rawConflictsAfter := decodeConflicts("after")
	foundConflict := false
	for _, conflict := range conflictsAfter {
		if conflict == expectedConflict {
			foundConflict = true
			break
		}
	}
	if !foundConflict {
		t.Errorf("conflict scan after resolver = %s, want preserved manual conflict %+v (resolver: %s)", rawConflictsAfter, expectedConflict, resolverJSON)
	}
}
