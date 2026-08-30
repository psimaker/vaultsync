package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/syncthing/syncthing/lib/config"
)

func TestScanFolderForKnownPatternsDetectsGitDirectory(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	vault := t.TempDir()
	gitDir := filepath.Join(vault, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatalf("write HEAD: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte("[core]\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	folderID := "scan-git"
	if errMsg := addFolderForTesting(folderID, "Scan Git", vault); errMsg != "" {
		t.Fatalf("AddFolder: %s", errMsg)
	}

	raw := ScanFolderForKnownPatterns(folderID)
	var result ScanResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("unmarshal failed: %v (raw=%s)", err, raw)
	}
	if len(result.Detected) != 1 {
		t.Fatalf("expected 1 detected pattern, got %d (raw=%s)", len(result.Detected), raw)
	}
	got := result.Detected[0]
	if got.Pattern != ".git" {
		t.Errorf("pattern = %q, want .git", got.Pattern)
	}
	if got.Label != "Git repository" {
		t.Errorf("label = %q, want Git repository", got.Label)
	}
	if got.FileCount != 2 {
		t.Errorf("fileCount = %d, want 2", got.FileCount)
	}
	if got.SizeBytes <= 0 {
		t.Errorf("sizeBytes = %d, want > 0", got.SizeBytes)
	}
}

func TestScanFolderForKnownPatternsEmptyVault(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	vault := t.TempDir()
	folderID := "scan-empty"
	if errMsg := addFolderForTesting(folderID, "Empty", vault); errMsg != "" {
		t.Fatalf("AddFolder: %s", errMsg)
	}

	raw := ScanFolderForKnownPatterns(folderID)
	if raw != `{"detected":[],"complete":true}` {
		t.Errorf("got %q, want empty detected list", raw)
	}
}

func TestScanFolderForKnownPatternsUnknownFolderID(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	raw := ScanFolderForKnownPatterns("does-not-exist")
	var result ScanResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("unmarshal failed: %v (raw=%s)", err, raw)
	}
	if result.Complete || result.Error != knownPatternScanUnavailableError || len(result.Detected) != 0 {
		t.Errorf("unknown folder result = %+v, want explicit unavailable", result)
	}
}

func TestScanFolderForKnownPatternsMultipleCandidates(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	vault := t.TempDir()
	for _, dir := range []string{".git", ".copilot-index", "node_modules"} {
		full := filepath.Join(vault, dir)
		if err := os.MkdirAll(full, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(full, "marker"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write marker in %s: %v", dir, err)
		}
	}

	folderID := "scan-multi"
	if errMsg := addFolderForTesting(folderID, "Multi", vault); errMsg != "" {
		t.Fatalf("AddFolder: %s", errMsg)
	}

	raw := ScanFolderForKnownPatterns(folderID)
	var result ScanResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(result.Detected) != 3 {
		t.Fatalf("expected 3 detected, got %d (raw=%s)", len(result.Detected), raw)
	}
	patterns := map[string]bool{}
	for _, d := range result.Detected {
		patterns[d.Pattern] = true
	}
	for _, want := range []string{".git", ".copilot-index", "node_modules"} {
		if !patterns[want] {
			t.Errorf("missing detected pattern %q", want)
		}
	}
}

func TestScanFolderForKnownPatternsAggregatesNestedVaults(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	// Mimic the typical Obsidian setup: a sync folder that is the Obsidian
	// root, with each vault as an immediate subdirectory.
	root := t.TempDir()
	for _, vault := range []string{"Personal", "Work"} {
		gitDir := filepath.Join(root, vault, ".git")
		if err := os.MkdirAll(gitDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", gitDir, err)
		}
		// Two files per .git so total FileCount = 4.
		if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
			t.Fatalf("write HEAD in %s: %v", vault, err)
		}
		if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte("[core]\n"), 0o644); err != nil {
			t.Fatalf("write config in %s: %v", vault, err)
		}
	}
	// One vault also has a Copilot index — should appear as its own entry.
	copilotDir := filepath.Join(root, "Personal", ".copilot-index")
	if err := os.MkdirAll(copilotDir, 0o755); err != nil {
		t.Fatalf("mkdir copilot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(copilotDir, "shard"), []byte("xx"), 0o644); err != nil {
		t.Fatalf("write copilot: %v", err)
	}

	folderID := "scan-nested"
	if errMsg := addFolderForTesting(folderID, "Nested", root); errMsg != "" {
		t.Fatalf("AddFolder: %s", errMsg)
	}

	raw := ScanFolderForKnownPatterns(folderID)
	var result ScanResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("unmarshal failed: %v (raw=%s)", err, raw)
	}

	byPattern := map[string]DetectedPattern{}
	for _, d := range result.Detected {
		byPattern[d.Pattern] = d
	}

	git, ok := byPattern[".git"]
	if !ok {
		t.Fatalf("expected .git entry in detected (raw=%s)", raw)
	}
	if git.FileCount != 4 {
		t.Errorf(".git fileCount = %d, want 4 (aggregated across 2 vaults)", git.FileCount)
	}
	if git.SizeBytes <= 0 {
		t.Errorf(".git sizeBytes = %d, want > 0", git.SizeBytes)
	}

	copilot, ok := byPattern[".copilot-index"]
	if !ok {
		t.Fatalf("expected .copilot-index entry in detected (raw=%s)", raw)
	}
	if copilot.FileCount != 1 {
		t.Errorf(".copilot-index fileCount = %d, want 1", copilot.FileCount)
	}
}

func TestScanFolderForKnownPatternsSkipsHiddenSubdirs(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	// A hidden top-level dir (e.g. .obsidian) should not be descended into;
	// its candidates would otherwise be double-counted.
	root := t.TempDir()
	cacheDir := filepath.Join(root, ".obsidian", "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "blob"), []byte("xx"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	folderID := "scan-hidden"
	if errMsg := addFolderForTesting(folderID, "Hidden", root); errMsg != "" {
		t.Fatalf("AddFolder: %s", errMsg)
	}

	raw := ScanFolderForKnownPatterns(folderID)
	var result ScanResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(result.Detected) != 1 {
		t.Fatalf("expected 1 detected pattern, got %d (raw=%s)", len(result.Detected), raw)
	}
	if result.Detected[0].Pattern != ".obsidian/cache" {
		t.Errorf("pattern = %q, want .obsidian/cache", result.Detected[0].Pattern)
	}
	if result.Detected[0].FileCount != 1 {
		t.Errorf("fileCount = %d, want 1 (no double-counting)", result.Detected[0].FileCount)
	}
}

func TestScanFolderForKnownPatternsIgnoresEmptyDirectories(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	vault := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vault, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	folderID := "scan-empty-git"
	if errMsg := addFolderForTesting(folderID, "Empty Git", vault); errMsg != "" {
		t.Fatalf("AddFolder: %s", errMsg)
	}

	raw := ScanFolderForKnownPatterns(folderID)
	if raw != `{"detected":[],"complete":true}` {
		t.Errorf("expected empty list for dir with no files, got %q", raw)
	}
}

func TestIssue150KnownPatternScanRejectsReceiveFoldersBeforeInspection(t *testing.T) {
	for _, folderType := range []config.FolderType{
		config.FolderTypeSendReceive,
		config.FolderTypeReceiveOnly,
		config.FolderTypeReceiveEncrypted,
	} {
		t.Run(folderType.String()+" (#150)", func(t *testing.T) {
			configDir := testConfigDir(t)
			folderPath := filepath.Join(configDir, "receive-vault")
			gitPath := filepath.Join(folderPath, ".git")
			if err := os.MkdirAll(gitPath, 0o700); err != nil {
				t.Fatalf("create protected scan fixture: %v", err)
			}
			if err := os.WriteFile(filepath.Join(gitPath, "private-note-index"), []byte("private"), 0o600); err != nil {
				t.Fatalf("write protected scan fixture: %v", err)
			}
			issue150SeedBridgeFolderBeforeStart(t, configDir, "issue150-filter-protected", folderPath, folderType)
			if errMsg := StartSyncthing(configDir); errMsg != "" {
				t.Fatalf("StartSyncthing() failed: %s", errMsg)
			}

			var result struct {
				Detected []DetectedPattern `json:"detected"`
				Complete bool              `json:"complete"`
				Error    string            `json:"error"`
			}
			raw := ScanFolderForKnownPatterns("issue150-filter-protected")
			if err := json.Unmarshal([]byte(raw), &result); err != nil {
				t.Fatalf("decode protected scan result: %v (raw: %q)", err, raw)
			}
			if result.Complete || result.Error == "" || len(result.Detected) != 0 {
				t.Fatalf("protected scan returned success-shaped evidence: %+v (raw: %q)", result, raw)
			}
			if strings.Contains(raw, folderPath) || strings.Contains(raw, "private-note-index") {
				t.Fatalf("protected scan failure leaked private detail: %q", raw)
			}
		})
	}
}

func TestIssue150KnownPatternScanUnavailableStatesAreExplicitAndPathFree(t *testing.T) {
	StopSyncthing()
	responses := []string{
		ScanFolderForKnownPatterns("issue150-stopped-filter-scan"),
	}

	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	responses = append(responses, ScanFolderForKnownPatterns("issue150-unknown-filter-scan"))

	for _, raw := range responses {
		var result struct {
			Detected []DetectedPattern `json:"detected"`
			Complete bool              `json:"complete"`
			Error    string            `json:"error"`
		}
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatalf("decode unavailable scan: %v (raw: %q)", err, raw)
		}
		if result.Complete || result.Error == "" || len(result.Detected) != 0 {
			t.Fatalf("unavailable scan returned success-shaped evidence: %+v (raw: %q)", result, raw)
		}
		if strings.Contains(raw, "issue150-") {
			t.Fatalf("unavailable scan leaked caller input: %q", raw)
		}
	}
}

func TestIssue150KnownPatternScanCoreStopsBeforeFilesystemForUnauthorizedFolders(t *testing.T) {
	tests := []struct {
		name    string
		folders func() map[string]config.FolderConfiguration
	}{
		{name: "engine stopped (#150)", folders: func() map[string]config.FolderConfiguration { return nil }},
		{name: "unknown folder (#150)", folders: func() map[string]config.FolderConfiguration { return map[string]config.FolderConfiguration{} }},
		{name: "send receive (#150)", folders: issue150ScanFolderConfigs(config.FolderTypeSendReceive)},
		{name: "receive only (#150)", folders: issue150ScanFolderConfigs(config.FolderTypeReceiveOnly)},
		{name: "receive encrypted (#150)", folders: issue150ScanFolderConfigs(config.FolderTypeReceiveEncrypted)},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			filesystemCalls := 0
			result := scanFolderForKnownPatterns("issue150-core-filter", knownPatternScanEnvironment{
				folderConfigs: testCase.folders,
				inspectFolder: func(string) ([]DetectedPattern, error) {
					filesystemCalls++
					return []DetectedPattern{{Pattern: ".git"}}, nil
				},
			})
			if filesystemCalls != 0 {
				t.Fatalf("unauthorized scan made %d filesystem calls, want zero", filesystemCalls)
			}
			if result.Complete || result.Error == "" || len(result.Detected) != 0 {
				t.Fatalf("unauthorized core result = %+v, want explicit unavailable", result)
			}
		})
	}
}

func TestIssue150KnownPatternScanCorePreservesSendOnlyPositiveControl(t *testing.T) {
	filesystemCalls := 0
	result := scanFolderForKnownPatterns("issue150-core-filter", knownPatternScanEnvironment{
		folderConfigs: issue150ScanFolderConfigs(config.FolderTypeSendOnly),
		inspectFolder: func(path string) ([]DetectedPattern, error) {
			filesystemCalls++
			if path != "/synthetic/issue150-sendonly" {
				t.Fatalf("inspected path = %q, want configured SendOnly path", path)
			}
			return []DetectedPattern{{Pattern: ".git", Label: "Git repository", SizeBytes: 7, FileCount: 1}}, nil
		},
	})
	if filesystemCalls != 1 {
		t.Fatalf("SendOnly scan made %d filesystem calls, want exactly one", filesystemCalls)
	}
	if !result.Complete || result.Error != "" || len(result.Detected) != 1 || result.Detected[0].Pattern != ".git" {
		t.Fatalf("SendOnly core result = %+v, want complete detected pattern", result)
	}
}

func issue150ScanFolderConfigs(folderType config.FolderType) func() map[string]config.FolderConfiguration {
	return func() map[string]config.FolderConfiguration {
		return map[string]config.FolderConfiguration{
			"issue150-core-filter": {
				ID:   "issue150-core-filter",
				Path: "/synthetic/issue150-sendonly",
				Type: folderType,
			},
		}
	}
}
