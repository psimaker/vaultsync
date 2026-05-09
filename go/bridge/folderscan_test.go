package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
	if errMsg := AddFolder(folderID, "Scan Git", vault); errMsg != "" {
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
	if errMsg := AddFolder(folderID, "Empty", vault); errMsg != "" {
		t.Fatalf("AddFolder: %s", errMsg)
	}

	raw := ScanFolderForKnownPatterns(folderID)
	if raw != `{"detected":[]}` {
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
	if raw != `{"detected":[]}` {
		t.Errorf("got %q, want empty detected list", raw)
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
	if errMsg := AddFolder(folderID, "Multi", vault); errMsg != "" {
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
	if errMsg := AddFolder(folderID, "Empty Git", vault); errMsg != "" {
		t.Fatalf("AddFolder: %s", errMsg)
	}

	raw := ScanFolderForKnownPatterns(folderID)
	if raw != `{"detected":[]}` {
		t.Errorf("expected empty list for dir with no files, got %q", raw)
	}
}
