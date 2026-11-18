package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A vault whose folder was moved or deleted while VaultSync was closed got an
// empty folder of its name back at its old place on the next launch: the
// launch pass wrote the default ignore patterns, and the engine's write of
// `.stignore` makes the folder's directory when it is missing (#371). The
// engine then reported the marker missing instead of the path missing, the
// app lost the way out that a vault with a missing path has, and the path
// reconcile took the new directory for the vault's own.

var issue371Defaults = []string{".Trash", ".obsidian/workspace.json", ".obsidian/workspace-mobile.json"}

// issue371Vault starts the engine with one vault under a selected folder and
// waits until a note is in the vault's index. A folder with nothing in its
// index is a new one to the engine, which makes its directory and marker
// again at the next start if they are gone (#338) — a test without the note
// would see the engine, not the bridge, put the directory back.
func issue371Vault(t *testing.T, folderID string) (base, own string) {
	t.Helper()
	base = testConfigDir(t)
	if errMsg := StartSyncthing(base); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	own = filepath.Join(base, "selected", "Vault")
	if errMsg := AddFolder(folderID, "Vault", own); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}
	if err := os.WriteFile(filepath.Join(own, "note.md"), []byte("# Note\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the note in the folder's index", 60*time.Second, func() bool {
		RescanFolder(folderID)
		var st FolderStatus
		if err := json.Unmarshal([]byte(GetFolderStatusJSON(folderID)), &st); err != nil {
			return false
		}
		return st.LocalFiles == 1
	})
	return base, own
}

func issue371DefaultsJSON(t *testing.T, patterns []string) string {
	t.Helper()
	data, err := json.Marshal(patterns)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// issue371ExpectRefusal checks that a write was refused with a text the app
// shows as a vault whose folder was moved or deleted — SyncUserError maps a
// message that contains "marker missing" to that card (#65) — and that the
// text carries no path.
func issue371ExpectRefusal(t *testing.T, what, errMsg string) {
	t.Helper()
	if errMsg == "" {
		t.Errorf("%s wrote into a folder whose directory is not the folder's (want a refusal)", what)
		return
	}
	if !strings.Contains(errMsg, "marker missing") {
		t.Errorf("%s = %q, want a refusal that contains \"marker missing\"", what, errMsg)
	}
	if strings.Contains(errMsg, string(filepath.Separator)) {
		t.Errorf("%s = %q carries a path", what, errMsg)
	}
}

// issue371EngineRefusal calls the engine's write directly, past the bridge's
// check, and returns the text the bridge makes of its error.
func issue371EngineRefusal(t *testing.T, folderID string) string {
	t.Helper()
	internals := getInternals()
	if internals == nil {
		t.Fatal("the engine is not running")
	}
	if err := internals.SetIgnores(folderID, []string{"*.tmp"}); err != nil {
		return ignoresWriteError(err)
	}
	return ""
}

func issue371ExpectNoEntry(t *testing.T, path, what string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("%s exists at %s (lstat err = %v), want nothing there", what, path, err)
	}
}

func issue371ErrorReason(t *testing.T, folderID string) string {
	t.Helper()
	var st FolderStatus
	if err := json.Unmarshal([]byte(GetFolderStatusJSON(folderID)), &st); err != nil {
		t.Fatalf("GetFolderStatusJSON unmarshal: %v", err)
	}
	return st.ErrorReason
}

// The reported scenario: VaultSync is closed, the vault's folder is moved
// into another folder, VaultSync starts again and its launch pass writes the
// default patterns.
func TestIssue371_ALaunchAfterTheVaultWasMovedCreatesNoFolder(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the engine twice")
	}
	const folderID = "vault-371-moved"
	base, own := issue371Vault(t, folderID)

	issue315StopWithConfigOnDisk(t, base, `id="`+folderID+`"`)
	moved := filepath.Join(base, "moved-into", "Vault")
	if err := os.MkdirAll(filepath.Dir(moved), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(own, moved); err != nil {
		t.Fatal(err)
	}
	if errMsg := StartSyncthing(base); errMsg != "" {
		t.Fatalf("StartSyncthing() after the move failed: %s", errMsg)
	}
	waitFor(t, "the engine reporting the vault's path missing", 60*time.Second, func() bool {
		return issue371ErrorReason(t, folderID) == "folder_path_missing"
	})

	// The launch pass.
	issue371ExpectRefusal(t, "EnsureDefaultIgnores", EnsureDefaultIgnores(folderID, issue371DefaultsJSON(t, issue371Defaults)))
	issue371ExpectNoEntry(t, own, "a directory at the vault's old place after the launch pass")

	// A filter changed on the vault's page takes the same engine call.
	issue371ExpectRefusal(t, "SetFolderIgnores", SetFolderIgnores(folderID, issue371DefaultsJSON(t, []string{"*.tmp"})))
	issue371ExpectNoEntry(t, own, "a directory at the vault's old place after a filter change")

	// The engine still says what happened, after a scan has looked again.
	RescanFolder(folderID)
	if reason := issue371ErrorReason(t, folderID); reason != "folder_path_missing" {
		t.Fatalf("error reason = %q after the launch pass, want folder_path_missing", reason)
	}

	// And the move can be followed: the vault is re-pointed at the folder it
	// was moved into, where the defaults are then written.
	if errMsg := SetFolderPath(folderID, moved); errMsg != "" {
		t.Fatalf("SetFolderPath to the folder the vault was moved into = %q, want success", errMsg)
	}
	if errMsg := EnsureDefaultIgnores(folderID, issue371DefaultsJSON(t, issue371Defaults)); errMsg != "" {
		t.Fatalf("EnsureDefaultIgnores in the moved vault = %q, want success", errMsg)
	}
	if got := readIgnoreLines(t, folderID); strings.Join(got, "\n") != strings.Join(issue371Defaults, "\n") {
		t.Fatalf("the moved vault's patterns = %v, want %v", got, issue371Defaults)
	}
	issue371ExpectNoEntry(t, own, "a directory at the vault's old place after the re-point")
}

// A directory that is there but no longer holds the folder's marker is not
// the folder's either — it may be a folder made by hand in the vault's place,
// or what #371 itself left behind in an install that ran an earlier version.
// Nothing is written into it, and its marker is not made.
func TestIssue371_ADirectoryWithoutTheMarkerGetsNothingWritten(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the engine")
	}
	const folderID = "vault-371-marker"
	_, own := issue371Vault(t, folderID)

	if errMsg := EnsureDefaultIgnores(folderID, issue371DefaultsJSON(t, issue371Defaults)); errMsg != "" {
		t.Fatalf("EnsureDefaultIgnores in the vault's own directory = %q, want success", errMsg)
	}
	stignore := filepath.Join(own, ".stignore")
	before, err := os.ReadFile(stignore)
	if err != nil {
		t.Fatal(err)
	}

	marker := filepath.Join(own, ".stfolder")
	if err := os.RemoveAll(marker); err != nil {
		t.Fatal(err)
	}

	withExtra := append(append([]string{}, issue371Defaults...), "extra/")
	issue371ExpectRefusal(t, "EnsureDefaultIgnores", EnsureDefaultIgnores(folderID, issue371DefaultsJSON(t, withExtra)))
	issue371ExpectRefusal(t, "SetFolderIgnores", SetFolderIgnores(folderID, issue371DefaultsJSON(t, []string{"*.tmp"})))

	after, err := os.ReadFile(stignore)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf(".stignore changed from %q to %q in a directory without the marker", before, after)
	}
	issue371ExpectNoEntry(t, marker, "a folder marker the bridge made")
}

// Past the bridge's check: the engine's own write refuses too (patch 006). A
// writer that reaches the engine without asking the bridge, or a folder
// taken away between the bridge's check and the engine's write, makes
// nothing.
func TestIssue371_TheEnginesOwnWriteMakesNoFolder(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the engine")
	}
	const folderID = "vault-371-engine"
	base, own := issue371Vault(t, folderID)

	moved := filepath.Join(base, "moved-into", "Vault")
	if err := os.MkdirAll(filepath.Dir(moved), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(own, moved); err != nil {
		t.Fatal(err)
	}
	issue371ExpectRefusal(t, "the engine's write where the directory is gone", issue371EngineRefusal(t, folderID))
	issue371ExpectNoEntry(t, own, "a directory the engine made at the vault's old place")

	// Back in its place, but without its marker.
	if err := os.Rename(moved, own); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(own, ".stfolder")
	if err := os.RemoveAll(marker); err != nil {
		t.Fatal(err)
	}
	issue371ExpectRefusal(t, "the engine's write without the marker", issue371EngineRefusal(t, folderID))
	issue371ExpectNoEntry(t, filepath.Join(own, ".stignore"), "a .stignore the engine wrote without the marker")
	issue371ExpectNoEntry(t, marker, "a folder marker the engine made")
}

// A file where one of the folder's directories was (ENOTDIR) is a gone
// directory too: refused with the same text by the bridge and the engine,
// and the file stays as it is.
func TestIssue371_AFileInPlaceOfTheParentIsAGoneDirectory(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the engine")
	}
	const folderID = "vault-371-enotdir"
	base, _ := issue371Vault(t, folderID)

	selected := filepath.Join(base, "selected")
	if err := os.Rename(selected, filepath.Join(base, "moved-away")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(selected, []byte("a file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	issue371ExpectRefusal(t, "EnsureDefaultIgnores", EnsureDefaultIgnores(folderID, issue371DefaultsJSON(t, issue371Defaults)))
	issue371ExpectRefusal(t, "SetFolderIgnores", SetFolderIgnores(folderID, issue371DefaultsJSON(t, []string{"*.tmp"})))
	issue371ExpectRefusal(t, "the engine's write", issue371EngineRefusal(t, folderID))

	info, err := os.Lstat(selected)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("the file in place of the selected folder is gone or changed (lstat = %v, %v)", info, err)
	}
}

// A vault accepted a moment ago already holds its marker: the engine makes it
// before the accept returns. The defaults the app writes two seconds after an
// accept therefore still land in a new vault.
func TestIssue371_ANewVaultGetsItsDefaultsRightAfterTheAccept(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	folderPath := filepath.Join(configDir, "new-vault-371")
	if errMsg := acceptAsDirectChild("vault-371-new", "Vault", folderPath, false); errMsg != "" {
		t.Fatalf("acceptAsDirectChild() = %q, want success", errMsg)
	}
	if errMsg := EnsureDefaultIgnores("vault-371-new", issue371DefaultsJSON(t, issue371Defaults)); errMsg != "" {
		t.Fatalf("EnsureDefaultIgnores right after the accept = %q, want success", errMsg)
	}
	if got := readIgnoreLines(t, "vault-371-new"); strings.Join(got, "\n") != strings.Join(issue371Defaults, "\n") {
		t.Fatalf("the new vault's patterns = %v, want %v", got, issue371Defaults)
	}
}
