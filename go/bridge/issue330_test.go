package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/syncthing/syncthing/lib/config"
)

// The path repair re-points a folder whose directory is gone at the directory
// the folder synced in, wherever that is now. It must hold the same floor as
// adding and accepting a folder: two folders never overlap on disk — not the
// same directory, not one inside the other (decision 001, #330). The marker
// and the record of the folder's directory (decision 063) say that the target
// is this folder's; they say nothing about the other folders.
//
// Every move in these tests is a rename on one volume, so the marker keeps
// its file number and the record of the folder's directory applies: the
// overlap is the only thing left that could refuse the repair.

// issue330FolderPath reads one folder's configured path from the bridge.
func issue330FolderPath(t *testing.T, folderID string) string {
	t.Helper()
	var folders []FolderInfo
	if err := json.Unmarshal([]byte(GetFoldersJSON()), &folders); err != nil {
		t.Fatalf("GetFoldersJSON unmarshal: %v", err)
	}
	for _, f := range folders {
		if f.ID == folderID {
			return f.Path
		}
	}
	t.Fatalf("folder %q is not configured", folderID)
	return ""
}

// issue330Start starts the engine in a fresh configuration directory and
// returns the directory the test's vaults live in.
func issue330Start(t *testing.T) string {
	t.Helper()
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	root := filepath.Join(configDir, "Obsidian")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	return root
}

func issue330Rename(t *testing.T, from, to string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Rename(from, to); err != nil {
		t.Fatalf("Rename: %v", err)
	}
}

// issue330WantRefused asserts that the repair of folderID onto target was
// refused as an overlap and left the folder where it was.
func issue330WantRefused(t *testing.T, folderID, target, configured string) {
	t.Helper()
	if errMsg := SetFolderPath(folderID, target); errMsg != FolderPathOverlapError() {
		t.Errorf("SetFolderPath(%q) onto a directory that overlaps another folder's = %q, want %q", folderID, errMsg, FolderPathOverlapError())
	}
	if got := issue330FolderPath(t, folderID); got != configured {
		t.Errorf("after the refused repair the folder is at %q, want it left at %q", got, configured)
	}
}

// issue330WantRepaired asserts that the repair of folderID onto target went
// through.
func issue330WantRepaired(t *testing.T, folderID, target string) {
	t.Helper()
	if errMsg := SetFolderPath(folderID, target); errMsg != "" {
		t.Errorf("SetFolderPath(%q) onto its own directory = %q, want \"\"", folderID, errMsg)
	}
	if got := issue330FolderPath(t, folderID); got != target {
		t.Errorf("after the repair the folder is at %q, want %q", got, target)
	}
}

// issue330AddPastTheFloor configures a folder the way an earlier version
// could — without the overlap check AddFolder holds now — and records its
// directory, as the bridge does for every folder it can reach. The marker
// file is written here: the engine makes none for a folder whose directory
// already has `.stfolder`, and without it the repair is refused for the
// missing marker before the overlap is looked at.
func issue330AddPastTheFloor(t *testing.T, folderID, path string) {
	t.Helper()
	writeFolderMarker(t, path, folderID)
	mu.Lock()
	folder := config.FolderConfiguration{
		ID:               folderID,
		Label:            folderID,
		Path:             path,
		Type:             config.FolderTypeSendReceive,
		RescanIntervalS:  defaultRescanIntervalS,
		FSWatcherEnabled: true,
		FSWatcherDelayS:  10,
		IgnorePerms:      true,
		Devices:          []config.FolderDeviceConfiguration{{DeviceID: stMyID}},
	}
	err := commitConfigLocked(func(cfg *config.Configuration) {
		cfg.Folders = append(cfg.Folders, folder)
	})
	if err == nil {
		noteFolderStorageLocked(folder)
	}
	mu.Unlock()
	if err != nil {
		t.Fatalf("commit a folder past the floor: %v", err)
	}
	if _, err := os.Stat(folderMarkerPath(folder, path)); err != nil {
		t.Fatalf("the folder added past the floor has no marker: %v", err)
	}
}

// The folder's own directory, moved into another folder's directory: the
// other folder would scan this vault's notes as its own and send them to its
// devices, and a deletion of that stray copy on any of them would reach this
// vault everywhere (#45 follow-up).
func TestIssue330_TheRepairRefusesAPathInsideAnotherFolder(t *testing.T) {
	root := issue330Start(t)
	outer := filepath.Join(root, "Outer")
	inner := filepath.Join(root, "Inner")
	if errMsg := AddFolder("issue330-outer", "Outer", outer); errMsg != "" {
		t.Fatalf("AddFolder outer: %s", errMsg)
	}
	if errMsg := AddFolder("issue330-inner", "Inner", inner); errMsg != "" {
		t.Fatalf("AddFolder inner: %s", errMsg)
	}

	moved := filepath.Join(outer, "Inner")
	issue330Rename(t, inner, moved)

	issue330WantRefused(t, "issue330-inner", moved, inner)
}

// The folder's own directory, with another folder's directory inside it —
// the same overlap from the other side.
func TestIssue330_TheRepairRefusesAPathAroundAnotherFolder(t *testing.T) {
	root := issue330Start(t)
	outer := filepath.Join(root, "Outer")
	if errMsg := AddFolder("issue330-outer", "Outer", outer); errMsg != "" {
		t.Fatalf("AddFolder outer: %s", errMsg)
	}
	moved := filepath.Join(root, "Moved")
	issue330Rename(t, outer, moved)

	// Nothing overlapped when this folder was added: the outer folder was
	// configured at a path that no longer exists.
	if errMsg := AddFolder("issue330-inner", "Inner", filepath.Join(moved, "Inner")); errMsg != "" {
		t.Fatalf("AddFolder inner: %s", errMsg)
	}

	issue330WantRefused(t, "issue330-outer", moved, outer)
}

// The folder's own directory, synced by another folder now — a share accepted
// there with the user's confirmation that it may be combined with what the
// directory holds, while this vault could not be reached (#330).
func TestIssue330_TheRepairRefusesAnotherFoldersDirectory(t *testing.T) {
	root := issue330Start(t)
	own := filepath.Join(root, "Vault")
	if errMsg := AddFolder("issue330-a", "Vault", own); errMsg != "" {
		t.Fatalf("AddFolder a: %s", errMsg)
	}
	moved := filepath.Join(root, "Notes")
	issue330Rename(t, own, moved)

	if errMsg := AddFolder("issue330-b", "Notes", moved); errMsg != "" {
		t.Fatalf("AddFolder b: %s", errMsg)
	}

	issue330WantRefused(t, "issue330-a", moved, own)
}

// The other folder's directory is gone too: its configured path still counts,
// as it does when a folder is added or accepted. Should that directory come
// back — restored in the Files app, or made again by the engine — the two
// would overlap.
func TestIssue330_TheRepairRefusesAPathAroundAnotherFoldersMissingDirectory(t *testing.T) {
	root := issue330Start(t)
	outer := filepath.Join(root, "Outer")
	if errMsg := AddFolder("issue330-outer", "Outer", outer); errMsg != "" {
		t.Fatalf("AddFolder outer: %s", errMsg)
	}
	moved := filepath.Join(root, "Moved")
	issue330Rename(t, outer, moved)

	innerPath := filepath.Join(moved, "Inner")
	if errMsg := AddFolder("issue330-inner", "Inner", innerPath); errMsg != "" {
		t.Fatalf("AddFolder inner: %s", errMsg)
	}
	issue330Rename(t, innerPath, filepath.Join(root, "Elsewhere"))

	issue330WantRefused(t, "issue330-outer", moved, outer)
}

// A pair an earlier version set up — two folders on one directory (1.6.0 to
// 1.7.0), or one inside the other (1.7.1) — whose paths change together with
// the container. This is the floor alone: asked for both, in either order, it
// re-points the first and refuses the second, because it sees only where the
// other folder is now. The app never asks it this way: its reconcile knows
// both new paths before it asks for either, and holds both folders of such a
// pair where they are (decision 082, `FolderPathReconciler`). The marker
// query it decides with says yes for both new paths. Nothing is paused,
// moved or deleted for the folder the floor refuses.
func TestIssue330_TheFloorAloneRefusesTheSecondOfALegacyPair(t *testing.T) {
	type pair struct {
		name          string
		first, second string            // folder IDs, in the order the floor is asked
		rel           map[string]string // each folder's directory under the old parent
		viaAddFolder  string            // the folder AddFolder adds; the other goes past the floor
	}
	nested := map[string]string{"outer": "Vault", "inner": "Vault/Inner"}
	pairs := []pair{
		{name: "the floor alone, nested, the inner one first", first: "inner", second: "outer", rel: nested, viaAddFolder: "outer"},
		{name: "the floor alone, nested, the outer one first", first: "outer", second: "inner", rel: nested, viaAddFolder: "outer"},
		{name: "the floor alone, one directory", first: "a", second: "b", rel: map[string]string{"a": "Vault", "b": "Vault"}, viaAddFolder: "a"},
	}
	for _, p := range pairs {
		t.Run(p.name, func(t *testing.T) {
			root := issue330Start(t)
			old := filepath.Join(root, "old")
			id := func(short string) string { return "issue330-" + short }
			past := p.first
			if past == p.viaAddFolder {
				past = p.second
			}

			if errMsg := AddFolder(id(p.viaAddFolder), p.viaAddFolder, filepath.Join(old, p.rel[p.viaAddFolder])); errMsg != "" {
				t.Fatalf("AddFolder %s: %s", p.viaAddFolder, errMsg)
			}
			issue330AddPastTheFloor(t, id(past), filepath.Join(old, p.rel[past]))

			// While the pair does not move, nothing is written: a path that
			// is already the configured one stays a no-op.
			if errMsg := SetFolderPath(id(p.second), filepath.Join(old, p.rel[p.second])); errMsg != "" {
				t.Fatalf("SetFolderPath onto the path already configured = %q, want the no-op", errMsg)
			}

			moved := filepath.Join(root, "new")
			issue330Rename(t, old, moved)

			// The reconcile counts both as repairs the bridge would take.
			for _, short := range []string{p.first, p.second} {
				if !FolderMarkerIsAt(id(short), filepath.Join(moved, p.rel[short])) {
					t.Fatalf("FolderMarkerIsAt(%s) at its new path = false, want true", short)
				}
			}

			issue330WantRepaired(t, id(p.first), filepath.Join(moved, p.rel[p.first]))
			issue330WantRefused(t, id(p.second), filepath.Join(moved, p.rel[p.second]), filepath.Join(old, p.rel[p.second]))
		})
	}
}

// The floor is the overlap, not a name the two share the start of: a vault
// repaired next to another one goes on.
func TestIssue330_ARepairBesideAnotherFolderGoesThrough(t *testing.T) {
	root := issue330Start(t)
	if errMsg := AddFolder("issue330-other", "Outer", filepath.Join(root, "Outer")); errMsg != "" {
		t.Fatalf("AddFolder other: %s", errMsg)
	}
	own := filepath.Join(root, "Vault")
	if errMsg := AddFolder("issue330-vault", "Vault", own); errMsg != "" {
		t.Fatalf("AddFolder vault: %s", errMsg)
	}
	moved := filepath.Join(root, "OuterNotes")
	issue330Rename(t, own, moved)

	issue330WantRepaired(t, "issue330-vault", moved)
}

// The query the reconcile asks before it holds anything: is this folder's
// marker at that path? Only a directory with the folder's own marker file
// says yes; it writes nothing.
func TestIssue330_FolderMarkerIsAtNamesOnlyTheFoldersOwnMarker(t *testing.T) {
	configDir := testConfigDir(t)
	if FolderMarkerIsAt("issue330-vault", configDir) {
		t.Fatalf("FolderMarkerIsAt with the engine stopped = true, want false")
	}
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	root := filepath.Join(configDir, "Obsidian")
	own := filepath.Join(root, "Vault")
	if errMsg := AddFolder("issue330-vault", "Vault", own); errMsg != "" {
		t.Fatalf("AddFolder: %s", errMsg)
	}
	moved := filepath.Join(root, "Moved")
	issue330Rename(t, own, moved)
	foreign := filepath.Join(root, "Foreign")
	writeFolderMarker(t, foreign, "some-other-folder")
	empty := filepath.Join(root, "Empty")
	if err := os.MkdirAll(empty, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	cases := []struct {
		folderID, path string
		want           bool
	}{
		{"issue330-vault", moved, true},
		{"issue330-vault", own, false},
		{"issue330-vault", empty, false},
		{"issue330-vault", foreign, false},
		{"not-configured", moved, false},
	}
	for _, c := range cases {
		if got := FolderMarkerIsAt(c.folderID, c.path); got != c.want {
			t.Errorf("FolderMarkerIsAt(%q, %q) = %v, want %v", c.folderID, c.path, got, c.want)
		}
	}
	if got := issue330FolderPath(t, "issue330-vault"); got != own {
		t.Errorf("the query moved the folder to %q, want it left at %q", got, own)
	}
}
