package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGetPendingFoldersJSONNotRunning(t *testing.T) {
	// Should return empty array when not running.
	if got := GetPendingFoldersJSON(); got != "[]" {
		t.Fatalf("GetPendingFoldersJSON() when stopped = %q, want '[]'", got)
	}
}

func TestGetPendingFoldersJSONEmpty(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	// With no remote devices, pending folders should be empty.
	got := GetPendingFoldersJSON()
	var pending []PendingFolderInfo
	if err := json.Unmarshal([]byte(got), &pending); err != nil {
		t.Fatalf("GetPendingFoldersJSON() unmarshal: %v (raw: %s)", err, got)
	}
	if len(pending) != 0 {
		t.Fatalf("GetPendingFoldersJSON() = %d entries, want 0", len(pending))
	}
}

func TestAcceptPendingFolderNotRunning(t *testing.T) {
	// Should fail when not running.
	if errMsg := AcceptPendingFolder("test", "Test", "/tmp/test"); errMsg != "syncthing not running" {
		t.Fatalf("AcceptPendingFolder when stopped = %q, want 'syncthing not running'", errMsg)
	}
}

func TestAcceptPendingFolderEmptyID(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	// Empty folder ID should fail.
	if errMsg := AcceptPendingFolder("", "Test", "/tmp/test"); errMsg != "folder ID is required" {
		t.Fatalf("AcceptPendingFolder empty ID = %q, want 'folder ID is required'", errMsg)
	}
}

func TestAcceptPendingFolderDuplicate(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	// Add a folder first.
	folderPath := filepath.Join(configDir, "existing")
	if errMsg := AddFolder("existing", "Existing", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	// Accepting a folder with the same ID should fail.
	acceptPath := filepath.Join(configDir, "accept")
	if errMsg := AcceptPendingFolder("existing", "Dup", acceptPath); errMsg != "folder already exists" {
		t.Fatalf("AcceptPendingFolder duplicate = %q, want 'folder already exists'", errMsg)
	}
}

func TestAcceptPendingFolderPathCollision(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	// Configure a first folder at a local path.
	vaultPath := filepath.Join(configDir, "VaultA")
	if errMsg := AddFolder("vault-a", "Vault A", vaultPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	const wantCollision = "another folder already syncs to this path"

	// A second, distinct folder ID targeting the SAME path must be rejected —
	// otherwise the two vaults merge into one directory and propagate the mix
	// back to both peers (issue #45).
	if errMsg := AcceptPendingFolder("vault-b", "Vault B", vaultPath); errMsg != wantCollision {
		t.Fatalf("AcceptPendingFolder same path = %q, want %q", errMsg, wantCollision)
	}

	// Trailing-slash and case variants resolve to the same directory and must
	// be rejected too (cleaned + case-insensitive comparison).
	if errMsg := AcceptPendingFolder("vault-c", "Vault C", vaultPath+"/"); errMsg != wantCollision {
		t.Fatalf("AcceptPendingFolder trailing-slash variant = %q, want %q", errMsg, wantCollision)
	}
	caseVariant := filepath.Join(configDir, "vaulta")
	if errMsg := AcceptPendingFolder("vault-d", "Vault D", caseVariant); errMsg != wantCollision {
		t.Fatalf("AcceptPendingFolder case variant = %q, want %q", errMsg, wantCollision)
	}

	// A genuinely distinct path is still accepted.
	otherPath := filepath.Join(configDir, "VaultB")
	if errMsg := AcceptPendingFolder("vault-e", "Vault E", otherPath); errMsg != "" {
		t.Fatalf("AcceptPendingFolder distinct path = %q, want success", errMsg)
	}
}

func TestAcceptPendingFolderNestedPathCollision(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	// Configure a first folder — a vault that owns its directory.
	vaultPath := filepath.Join(configDir, "Workshops")
	if errMsg := AddFolder("vault-workshops", "Workshops", vaultPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	const wantInside = "this path is inside a directory another folder already syncs"
	const wantContains = "another folder already syncs a directory inside this path"

	// A share nested INSIDE an existing folder must be rejected: the outer
	// folder scans the inner vault's files as its own content and pushes them
	// to its peers — the #45 merge one level down (the vault-as-root setup
	// from the #45 follow-up report).
	nested := filepath.Join(vaultPath, "Obsidian-Vault-Life")
	if errMsg := AcceptPendingFolder("vault-life", "Life", nested); errMsg != wantInside {
		t.Fatalf("AcceptPendingFolder nested path = %q, want %q", errMsg, wantInside)
	}

	// Deeper nesting and case variants resolve into the same subtree and must
	// be rejected too (case-folding APFS).
	deep := filepath.Join(vaultPath, "notes", "sub")
	if errMsg := AcceptPendingFolder("vault-deep", "Deep", deep); errMsg != wantInside {
		t.Fatalf("AcceptPendingFolder deeply nested path = %q, want %q", errMsg, wantInside)
	}
	caseVariant := filepath.Join(configDir, "workshops", "Nested")
	if errMsg := AcceptPendingFolder("vault-case", "Case", caseVariant); errMsg != wantInside {
		t.Fatalf("AcceptPendingFolder case-variant nested path = %q, want %q", errMsg, wantInside)
	}

	// A share that would CONTAIN an existing folder is the same overlap from
	// the other side and must be rejected as well.
	if errMsg := AcceptPendingFolder("vault-parent", "Parent", configDir); errMsg != wantContains {
		t.Fatalf("AcceptPendingFolder containing path = %q, want %q", errMsg, wantContains)
	}

	// A sibling whose name merely starts with the existing folder's name is
	// NOT nested (boundary-aware comparison) and is accepted.
	sibling := filepath.Join(configDir, "WorkshopsArchive")
	if errMsg := AcceptPendingFolder("vault-sibling", "Sibling", sibling); errMsg != "" {
		t.Fatalf("AcceptPendingFolder name-prefix sibling = %q, want success", errMsg)
	}
}

func TestAcceptPendingFolderCreatesPath(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	// Accept a folder that isn't actually pending — this exercises the
	// path creation and config mutation without requiring a real remote
	// device offer.
	folderPath := filepath.Join(configDir, "accepted-vault")
	if errMsg := AcceptPendingFolder("vault-1", "My Vault", folderPath); errMsg != "" {
		t.Fatalf("AcceptPendingFolder failed: %s", errMsg)
	}

	// Folder path should have been created.
	if _, err := os.Stat(folderPath); os.IsNotExist(err) {
		t.Error("AcceptPendingFolder did not create folder path")
	}

	// Folder should now appear in GetFoldersJSON.
	foldersJSON := GetFoldersJSON()
	var folders []FolderInfo
	if err := json.Unmarshal([]byte(foldersJSON), &folders); err != nil {
		t.Fatalf("GetFoldersJSON() unmarshal: %v", err)
	}

	found := false
	for _, f := range folders {
		if f.ID == "vault-1" {
			found = true
			if f.Label != "My Vault" {
				t.Errorf("folder label = %q, want %q", f.Label, "My Vault")
			}
			if f.Path != folderPath {
				t.Errorf("folder path = %q, want %q", f.Path, folderPath)
			}
		}
	}
	if !found {
		t.Error("accepted folder not found in GetFoldersJSON()")
	}
}
