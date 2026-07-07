package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	if errMsg := AcceptPendingFolder("test", "Test", "/tmp/test", false); errMsg != "syncthing not running" {
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
	if errMsg := AcceptPendingFolder("", "Test", "/tmp/test", false); errMsg != "folder ID is required" {
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
	if errMsg := AcceptPendingFolder("existing", "Dup", acceptPath, false); errMsg != "folder already exists" {
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
	if errMsg := AcceptPendingFolder("vault-b", "Vault B", vaultPath, false); errMsg != wantCollision {
		t.Fatalf("AcceptPendingFolder same path = %q, want %q", errMsg, wantCollision)
	}

	// Trailing-slash and case variants resolve to the same directory and must
	// be rejected too (cleaned + case-insensitive comparison).
	if errMsg := AcceptPendingFolder("vault-c", "Vault C", vaultPath+"/", false); errMsg != wantCollision {
		t.Fatalf("AcceptPendingFolder trailing-slash variant = %q, want %q", errMsg, wantCollision)
	}
	caseVariant := filepath.Join(configDir, "vaulta")
	if errMsg := AcceptPendingFolder("vault-d", "Vault D", caseVariant, false); errMsg != wantCollision {
		t.Fatalf("AcceptPendingFolder case variant = %q, want %q", errMsg, wantCollision)
	}

	// A genuinely distinct path is still accepted.
	otherPath := filepath.Join(configDir, "VaultB")
	if errMsg := AcceptPendingFolder("vault-e", "Vault E", otherPath, false); errMsg != "" {
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
	if errMsg := AcceptPendingFolder("vault-life", "Life", nested, false); errMsg != wantInside {
		t.Fatalf("AcceptPendingFolder nested path = %q, want %q", errMsg, wantInside)
	}

	// Deeper nesting and case variants resolve into the same subtree and must
	// be rejected too (case-folding APFS).
	deep := filepath.Join(vaultPath, "notes", "sub")
	if errMsg := AcceptPendingFolder("vault-deep", "Deep", deep, false); errMsg != wantInside {
		t.Fatalf("AcceptPendingFolder deeply nested path = %q, want %q", errMsg, wantInside)
	}
	caseVariant := filepath.Join(configDir, "workshops", "Nested")
	if errMsg := AcceptPendingFolder("vault-case", "Case", caseVariant, false); errMsg != wantInside {
		t.Fatalf("AcceptPendingFolder case-variant nested path = %q, want %q", errMsg, wantInside)
	}

	// A share that would CONTAIN an existing folder is the same overlap from
	// the other side and must be rejected as well.
	if errMsg := AcceptPendingFolder("vault-parent", "Parent", configDir, false); errMsg != wantContains {
		t.Fatalf("AcceptPendingFolder containing path = %q, want %q", errMsg, wantContains)
	}

	// A sibling whose name merely starts with the existing folder's name is
	// NOT nested (boundary-aware comparison) and is accepted.
	sibling := filepath.Join(configDir, "WorkshopsArchive")
	if errMsg := AcceptPendingFolder("vault-sibling", "Sibling", sibling, false); errMsg != "" {
		t.Fatalf("AcceptPendingFolder name-prefix sibling = %q, want success", errMsg)
	}
}

// Mirror of the Swift emptiness rule (`VaultManager.isEmptyVaultListing`,
// exercised by ImplicitMergeGuardTests, #54): both layers must decide the
// same cases identically — a divergent edge case would be a silent gap
// between the Swift guard and this hard floor. Keep this table and the Swift
// test table in sync.
func TestNonEmptyTargetErrorMirrorsSwiftEmptyVaultRule(t *testing.T) {
	const refused = "the target folder already contains files"
	cases := []struct {
		name    string
		exists  bool
		entries []string
		want    string
	}{
		{name: "missing directory", exists: false, want: ""},
		{name: "empty directory", exists: true, entries: nil, want: ""},
		{name: "obsidian config only", exists: true, entries: []string{".obsidian"}, want: ""},
		{name: "obsidian case variant", exists: true, entries: []string{".Obsidian"}, want: ""},
		{name: "stfolder marker disqualifies", exists: true, entries: []string{".stfolder"}, want: refused},
		{name: "note disqualifies", exists: true, entries: []string{"note.md"}, want: refused},
		{name: "obsidian plus note disqualifies", exists: true, entries: []string{".obsidian", "note.md"}, want: refused},
		{name: "hidden leftover disqualifies", exists: true, entries: []string{".DS_Store"}, want: refused},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "Target")
			if tc.exists {
				if err := os.MkdirAll(target, 0o700); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				for _, entry := range tc.entries {
					entryPath := filepath.Join(target, entry)
					if entry == ".obsidian" || entry == ".Obsidian" || entry == ".stfolder" {
						if err := os.MkdirAll(entryPath, 0o700); err != nil {
							t.Fatalf("mkdir entry: %v", err)
						}
					} else if err := os.WriteFile(entryPath, []byte("x"), 0o600); err != nil {
						t.Fatalf("write entry: %v", err)
					}
				}
			}
			if got := nonEmptyTargetError(target); got != tc.want {
				t.Fatalf("nonEmptyTargetError(%s) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func TestAcceptPendingFolderNonEmptyTarget(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	const wantRefused = "the target folder already contains files"

	// An unconfigured directory that already holds content must be refused
	// without explicit confirmation: accepting would merge two content sets
	// and push the mix to every offering peer (#54).
	nonEmpty := filepath.Join(configDir, "Existing Notes")
	if err := os.MkdirAll(nonEmpty, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nonEmpty, "note.md"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if errMsg := AcceptPendingFolder("vault-nonempty", "Existing Notes", nonEmpty, false); errMsg != wantRefused {
		t.Fatalf("AcceptPendingFolder non-empty unconfirmed = %q, want %q", errMsg, wantRefused)
	}

	// The user's explicit confirmation travels through allowNonEmpty and
	// lets the same accept proceed (remove + re-accept recovery, 006).
	if errMsg := AcceptPendingFolder("vault-nonempty", "Existing Notes", nonEmpty, true); errMsg != "" {
		t.Fatalf("AcceptPendingFolder non-empty confirmed = %q, want success", errMsg)
	}

	// An existing empty vault (nothing beyond .obsidian) stays acceptable
	// without confirmation — the #52 picker relies on that.
	emptyVault := filepath.Join(configDir, "Fresh Vault")
	if err := os.MkdirAll(filepath.Join(emptyVault, ".obsidian"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if errMsg := AcceptPendingFolder("vault-emptyvault", "Fresh Vault", emptyVault, false); errMsg != "" {
		t.Fatalf("AcceptPendingFolder empty vault unconfirmed = %q, want success", errMsg)
	}

	// A target whose emptiness cannot be verified is refused — never assumed.
	if os.Getuid() != 0 {
		unreadable := filepath.Join(configDir, "Unreadable")
		if err := os.MkdirAll(unreadable, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(unreadable, "note.md"), []byte("x"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := os.Chmod(unreadable, 0o000); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		defer os.Chmod(unreadable, 0o700)
		errMsg := AcceptPendingFolder("vault-unreadable", "Unreadable", unreadable, false)
		if !strings.HasPrefix(errMsg, "read folder path:") {
			t.Fatalf("AcceptPendingFolder unreadable = %q, want 'read folder path:' prefix", errMsg)
		}
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
	if errMsg := AcceptPendingFolder("vault-1", "My Vault", folderPath, false); errMsg != "" {
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
