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
	if errMsg := acceptAsDirectChild("test", "Test", "/tmp/test", false); errMsg != "syncthing not running" {
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
	if errMsg := acceptAsDirectChild("", "Test", "/tmp/test", false); errMsg != "folder ID is required" {
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
	if errMsg := acceptAsDirectChild("existing", "Dup", acceptPath, false); errMsg != "folder already exists" {
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
	if errMsg := acceptAsDirectChild("vault-b", "Vault B", vaultPath, false); errMsg != wantCollision {
		t.Fatalf("AcceptPendingFolder same path = %q, want %q", errMsg, wantCollision)
	}

	// Trailing-slash and case variants resolve to the same directory and must
	// be rejected too (cleaned + case-insensitive comparison).
	if errMsg := acceptAsDirectChild("vault-c", "Vault C", vaultPath+"/", false); errMsg != wantCollision {
		t.Fatalf("AcceptPendingFolder trailing-slash variant = %q, want %q", errMsg, wantCollision)
	}
	caseVariant := filepath.Join(configDir, "vaulta")
	if errMsg := acceptAsDirectChild("vault-d", "Vault D", caseVariant, false); errMsg != wantCollision {
		t.Fatalf("AcceptPendingFolder case variant = %q, want %q", errMsg, wantCollision)
	}

	// A genuinely distinct path is still accepted.
	otherPath := filepath.Join(configDir, "VaultB")
	if errMsg := acceptAsDirectChild("vault-e", "Vault E", otherPath, false); errMsg != "" {
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
	if errMsg := acceptAsDirectChild("vault-life", "Life", nested, false); errMsg != wantInside {
		t.Fatalf("AcceptPendingFolder nested path = %q, want %q", errMsg, wantInside)
	}

	// Deeper nesting and case variants resolve into the same subtree and must
	// be rejected too (case-folding APFS).
	deep := filepath.Join(vaultPath, "notes", "sub")
	if errMsg := acceptAsDirectChild("vault-deep", "Deep", deep, false); errMsg != wantInside {
		t.Fatalf("AcceptPendingFolder deeply nested path = %q, want %q", errMsg, wantInside)
	}
	caseVariant := filepath.Join(configDir, "workshops", "Nested")
	if errMsg := acceptAsDirectChild("vault-case", "Case", caseVariant, false); errMsg != wantInside {
		t.Fatalf("AcceptPendingFolder case-variant nested path = %q, want %q", errMsg, wantInside)
	}

	// A share that would CONTAIN an existing folder is the same overlap from
	// the other side and must be rejected as well.
	if errMsg := acceptAsDirectChild("vault-parent", "Parent", configDir, false); errMsg != wantContains {
		t.Fatalf("AcceptPendingFolder containing path = %q, want %q", errMsg, wantContains)
	}

	// A sibling whose name merely starts with the existing folder's name is
	// NOT nested (boundary-aware comparison) and is accepted.
	sibling := filepath.Join(configDir, "WorkshopsArchive")
	if errMsg := acceptAsDirectChild("vault-sibling", "Sibling", sibling, false); errMsg != "" {
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
	if errMsg := acceptAsDirectChild("vault-nonempty", "Existing Notes", nonEmpty, false); errMsg != wantRefused {
		t.Fatalf("AcceptPendingFolder non-empty unconfirmed = %q, want %q", errMsg, wantRefused)
	}

	// The user's explicit confirmation travels through allowNonEmpty and
	// lets the same accept proceed (remove + re-accept recovery, 006).
	if errMsg := acceptAsDirectChild("vault-nonempty", "Existing Notes", nonEmpty, true); errMsg != "" {
		t.Fatalf("AcceptPendingFolder non-empty confirmed = %q, want success", errMsg)
	}

	// An existing empty vault (nothing beyond .obsidian) stays acceptable
	// without confirmation — the #52 picker relies on that.
	emptyVault := filepath.Join(configDir, "Fresh Vault")
	if err := os.MkdirAll(filepath.Join(emptyVault, ".obsidian"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if errMsg := acceptAsDirectChild("vault-emptyvault", "Fresh Vault", emptyVault, false); errMsg != "" {
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
		errMsg := acceptAsDirectChild("vault-unreadable", "Unreadable", unreadable, false)
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
	if errMsg := acceptAsDirectChild("vault-1", "My Vault", folderPath, false); errMsg != "" {
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

// acceptAsDirectChild accepts into path with path's parent as the selected
// folder — the shape of every accept the app makes (#270). The tests above
// are about the other guards and reach them through it.
func acceptAsDirectChild(folderID, label, path string, allowNonEmpty bool) string {
	return AcceptPendingFolder(folderID, label, path, filepath.Dir(filepath.Clean(path)), allowNonEmpty)
}

// The pure rule behind the #270 floor: only a direct subfolder of the
// selected folder passes.
func TestIssue270_NotDirectChildError(t *testing.T) {
	const root = "/var/mobile/Containers/Data/Application/0A1B2C3D/Documents"
	const refused = "the folder must be a direct subfolder of the selected folder"

	tests := []struct {
		name string
		root string
		path string
		want string
	}{
		{"a direct subfolder", root, root + "/Notes", ""},
		{"a direct subfolder named like the selected folder", root, root + "/Documents", ""},
		{"a direct subfolder with a trailing separator", root, root + "/Notes/", ""},
		{"a selected folder spelled in another case", root, "/var/mobile/Containers/Data/Application/0A1B2C3D/documents/Notes", ""},
		{"the selected folder itself", root, root, refused},
		{"the selected folder with a trailing separator", root, root + "/", refused},
		{"the selected folder in another case", root, "/var/mobile/Containers/Data/Application/0A1B2C3D/documents", refused},
		{"a name of .", root, root + "/.", refused},
		{"a name of ..", root, root + "/..", refused},
		{"two levels down", root, root + "/Notes/Inner", refused},
		{"a subfolder reached through ..", root, root + "/Notes/../../Elsewhere", refused},
		{"a sibling of the selected folder", root, "/var/mobile/Containers/Data/Application/0A1B2C3D/Library", refused},
		{"a folder that only shares the name prefix", root, root + "x/Notes", refused},
		{"the file system root as the selected folder itself", "/", "/", refused},
		{"no selected folder", "", root + "/Notes", "the selected folder is required"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := notDirectChildError(tc.root, tc.path); got != tc.want {
				t.Fatalf("notDirectChildError(%q, %q) = %q, want %q", tc.root, tc.path, got, tc.want)
			}
		})
	}
}

// The accept itself holds the floor (#270): the selected folder and anything
// deeper than its direct subfolders is refused before a directory is created
// or the configuration changes; a direct subfolder is accepted.
func TestIssue270_AcceptOnlyIntoADirectSubfolderOfTheSelectedFolder(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	root := filepath.Join(configDir, "Documents")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	const refused = "the folder must be a direct subfolder of the selected folder"
	deeper := filepath.Join(root, "Notes", "Inner")
	refusals := []struct {
		name string
		path string
	}{
		{"the selected folder itself", root},
		{"a name of .", root + string(filepath.Separator) + "."},
		{"a name of ..", root + string(filepath.Separator) + ".."},
		{"two levels down", deeper},
	}
	for i, tc := range refusals {
		folderID := "vault-270-refused-" + string(rune('a'+i))
		if errMsg := AcceptPendingFolder(folderID, "Notes", tc.path, root, false); errMsg != refused {
			t.Fatalf("AcceptPendingFolder into %s = %q, want %q", tc.name, errMsg, refused)
		}
	}
	if errMsg := AcceptPendingFolder("vault-270-no-root", "Notes", filepath.Join(root, "Notes"), "", false); errMsg != "the selected folder is required" {
		t.Fatalf("AcceptPendingFolder without a selected folder = %q, want a refusal", errMsg)
	}

	// A refusal leaves nothing behind: no directory, no configured folder.
	if _, err := os.Stat(filepath.Join(root, "Notes")); !os.IsNotExist(err) {
		t.Fatalf("a refused accept created a directory (stat err = %v)", err)
	}
	var folders []FolderInfo
	if err := json.Unmarshal([]byte(GetFoldersJSON()), &folders); err != nil {
		t.Fatalf("GetFoldersJSON() unmarshal: %v", err)
	}
	for _, f := range folders {
		if strings.HasPrefix(f.ID, "vault-270-") {
			t.Fatalf("refused accept still added folder %q to the config", f.ID)
		}
	}

	// A direct subfolder is where every share goes — also one named like the
	// selected folder, which used to collapse into it.
	for _, name := range []string{"Notes", "Documents"} {
		path := filepath.Join(root, name)
		if errMsg := AcceptPendingFolder("vault-270-"+name, name, path, root, false); errMsg != "" {
			t.Fatalf("AcceptPendingFolder into the direct subfolder %q = %q, want success", name, errMsg)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("accepted folder %q was not created: %v", name, err)
		}
	}
}
