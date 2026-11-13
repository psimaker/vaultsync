package bridge

import (
	"encoding/json"
	"errors"
	"io/fs"
	"iter"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/protocol"
)

// A copy of a vault made by hand — in the Files app, for one — carries the
// hidden entries along: `.stfolder` with the marker file of the folder it was
// copied from (#315, seen on a device). The marker therefore says where a
// folder's storage once was copied from, not that a directory is that storage.
//
// These tests run the bridge against a real loopback peer, because what is at
// stake only shows on the other device: a folder continued in a directory
// that is behind its index sends the difference out as deletions and as old
// versions.

const (
	issue315FolderID = "issue315-vault"
	issue315NoteV1   = "# Edited\n\nfirst version\n"
	issue315NoteV2   = "# Edited\n\nsecond version, written after the copy was made\n"
)

// issue315Rig is one vault a real peer shares and the bridge has accepted
// under a first selected folder, settled.
type issue315Rig struct {
	peer      *peerInstance
	peerVault string
	configDir string
	// own is the folder's own directory: <first selected folder>/Vault.
	own string
}

func newIssue315Rig(t *testing.T) *issue315Rig {
	t.Helper()
	peer := startPeer(t, peerBinary(t), "issue315-peer")
	rig := &issue315Rig{peer: peer, peerVault: filepath.Join(peer.home, "vault")}
	if err := os.MkdirAll(rig.peerVault, 0o700); err != nil {
		t.Fatal(err)
	}
	rig.writePeer(t, "kept.md", "# Kept\n\nnever touched again\n")
	rig.writePeer(t, "edited.md", issue315NoteV1)
	peer.mustDo(t, http.MethodPost, "/rest/config/folders", map[string]any{
		"id":               issue315FolderID,
		"label":            "Vault",
		"path":             rig.peerVault,
		"type":             "sendreceive",
		"rescanIntervalS":  3600,
		"fsWatcherEnabled": false,
		"devices":          []map[string]any{},
	}, nil)
	peer.scanAndSettle(t, issue315FolderID)

	rig.configDir = testConfigDir(t)
	if errMsg := StartSyncthing(rig.configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	captureBridgeLog(t, filepath.Join(rig.configDir, "bridge-engine.log"))
	bridgeListen := "tcp://127.0.0.1:" + strconv.Itoa(freePort(t))
	mu.Lock()
	err := commitConfigLocked(func(cfg *config.Configuration) {
		cfg.Options.RawListenAddresses = []string{bridgeListen}
		cfg.Options.GlobalAnnEnabled = false
		cfg.Options.LocalAnnEnabled = false
		cfg.Options.RelaysEnabled = false
		cfg.Options.NATEnabled = false
	})
	mu.Unlock()
	if err != nil {
		t.Fatalf("pin bridge to loopback: %v", err)
	}
	bridgeID := DeviceID()
	if errMsg := AddDevice(peer.id, "Peer"); errMsg != "" {
		t.Fatalf("AddDevice(peer) = %q", errMsg)
	}
	mu.Lock()
	err = commitConfigLocked(func(cfg *config.Configuration) {
		for i := range cfg.Devices {
			if cfg.Devices[i].DeviceID.String() == peer.id {
				cfg.Devices[i].Addresses = []string{peer.listen}
			}
		}
	})
	mu.Unlock()
	if err != nil {
		t.Fatalf("set peer address: %v", err)
	}
	peer.mustDo(t, http.MethodPost, "/rest/config/devices", map[string]any{
		"deviceID":  bridgeID,
		"name":      "Bridge",
		"addresses": []string{bridgeListen},
	}, nil)
	var folderCfg map[string]any
	peer.mustDo(t, http.MethodGet, "/rest/config/folders/"+issue315FolderID, nil, &folderCfg)
	folderCfg["devices"] = []map[string]any{{"deviceID": peer.id}, {"deviceID": bridgeID}}
	peer.mustDo(t, http.MethodPut, "/rest/config/folders/"+issue315FolderID, folderCfg, nil)

	waitFor(t, "bridge connected to peer", 60*time.Second, func() bool {
		return bridgeConnected(peer.id)
	})
	waitFor(t, "pending folder offer", 60*time.Second, func() bool {
		var pending []PendingFolderInfo
		json.Unmarshal([]byte(GetPendingFoldersJSON()), &pending)
		for _, p := range pending {
			if p.ID == issue315FolderID {
				return true
			}
		}
		return false
	})
	rig.own = filepath.Join(rig.configDir, "selected-1", "Vault")
	if errMsg := acceptAsDirectChild(issue315FolderID, "Vault", rig.own, false); errMsg != "" {
		t.Fatalf("acceptAsDirectChild() = %q", errMsg)
	}
	rig.waitBridgeHolds(t, rig.own, map[string]string{
		"kept.md":   "# Kept\n\nnever touched again\n",
		"edited.md": issue315NoteV1,
	})
	return rig
}

func (r *issue315Rig) writePeer(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.peerVault, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// waitBridgeHolds waits until dir holds every named file with its content and
// the folder is idle with nothing left to fetch.
func (r *issue315Rig) waitBridgeHolds(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	waitFor(t, "the bridge's folder to hold what the peer has", 120*time.Second, func() bool {
		for name, want := range files {
			if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(got) != want {
				return false
			}
		}
		var st FolderStatus
		if err := json.Unmarshal([]byte(GetFolderStatusJSON(issue315FolderID)), &st); err != nil {
			return false
		}
		return st.State == "idle" && st.NeedFiles == 0 && st.LocalFiles == len(files)
	})
}

// peerFile returns a file's content on the peer, and whether it exists.
func (r *issue315Rig) peerFile(t *testing.T, name string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(r.peerVault, name))
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data), true
}

// issue315CopyTree copies src to dst the way a copy made by hand does: every
// entry, the hidden ones included, with its modification time.
func issue315CopyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			return err
		}
		return os.Chtimes(target, info.ModTime(), info.ModTime())
	})
	if err != nil {
		t.Fatalf("copy %s: %v", src, err)
	}
}

// issue315Eventually polls cond until it holds or the time is up.
func issue315Eventually(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
	return true
}

// The reported scenario: a vault syncs under one selected folder, its owner
// copies it by hand under another one, sync goes on, and later the first
// folder is deleted. The copy has the folder's name and its marker, and
// holds what the vault held when the copy was made.
func TestIssue315_ACopyWithTheFoldersMarkerIsNotItsStorage(t *testing.T) {
	if testing.Short() {
		t.Skip("real peer process; skipped in -short")
	}
	rig := newIssue315Rig(t)

	// The copy, made by hand while the vault syncs.
	copyDir := filepath.Join(rig.configDir, "selected-2", "Vault")
	issue315CopyTree(t, rig.own, copyDir)
	if errMsg := verifyFolderMarker(config.FolderConfiguration{ID: issue315FolderID}, copyDir); errMsg != "" {
		t.Fatalf("the copy does not carry the folder's marker (%s) — the test no longer builds the reported state", errMsg)
	}

	// Sync goes on in the folder's own directory: one note changes, one is new.
	rig.writePeer(t, "edited.md", issue315NoteV2)
	rig.writePeer(t, "later.md", "# Later\n\nwritten after the copy was made\n")
	rig.peer.scanAndSettle(t, issue315FolderID)
	rig.waitBridgeHolds(t, rig.own, map[string]string{
		"kept.md":   "# Kept\n\nnever touched again\n",
		"edited.md": issue315NoteV2,
		"later.md":  "# Later\n\nwritten after the copy was made\n",
	})

	// The folder's own directory goes away with the selected folder it was in.
	if err := os.RemoveAll(filepath.Dir(rig.own)); err != nil {
		t.Fatal(err)
	}

	// What the path repair does once the other folder is selected: the
	// folder's directory is gone, a directory of its name exists there.
	errMsg := SetFolderPath(issue315FolderID, copyDir)
	if errMsg == "" {
		t.Errorf("SetFolderPath took a copy that is behind the folder's index for the folder's storage")
		// Show what that costs: the scan of the copy reaches the peer.
		lost := issue315Eventually(90*time.Second, func() bool {
			edited, _ := rig.peerFile(t, "edited.md")
			_, hasLater := rig.peerFile(t, "later.md")
			return edited != issue315NoteV2 || !hasLater
		})
		if lost {
			edited, _ := rig.peerFile(t, "edited.md")
			_, hasLater := rig.peerFile(t, "later.md")
			t.Fatalf("the peer lost what had synced after the copy was made: edited.md holds the second version = %v, later.md exists = %v",
				edited == issue315NoteV2, hasLater)
		}
		t.FailNow()
	}

	// Refused with the text the app knows this case by: the folder stays
	// where it was, and nothing reaches the peer.
	if errMsg != FolderStorageMismatchError() {
		t.Fatalf("SetFolderPath refused the copy with %q, want %q", errMsg, FolderStorageMismatchError())
	}
	var folders []FolderInfo
	if err := json.Unmarshal([]byte(GetFoldersJSON()), &folders); err != nil {
		t.Fatalf("GetFoldersJSON unmarshal: %v", err)
	}
	if len(folders) != 1 || folders[0].Path != rig.own {
		t.Fatalf("after the refusal the folder is at %v, want it left at %s", folders, rig.own)
	}
	RescanFolder(issue315FolderID)
	time.Sleep(3 * time.Second)
	if edited, _ := rig.peerFile(t, "edited.md"); edited != issue315NoteV2 {
		t.Fatalf("the peer's edited.md changed after a refused rebase: %q", edited)
	}
	if _, hasLater := rig.peerFile(t, "later.md"); !hasLater {
		t.Fatal("the peer's later.md is gone after a refused rebase")
	}
}

// A vault moved as a whole — in the Files app, under another selected folder —
// arrives with what it held: the repair goes on in it, and a note written
// there before the repair goes out as the new note it is.
func TestIssue315_AMovedVaultGoesOnWhereItIs(t *testing.T) {
	if testing.Short() {
		t.Skip("real peer process; skipped in -short")
	}
	rig := newIssue315Rig(t)

	moved := filepath.Join(rig.configDir, "selected-2", "Vault")
	if err := os.MkdirAll(filepath.Dir(moved), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(rig.own, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moved, "new.md"), []byte("# New\n\nwritten after the move\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if errMsg := SetFolderPath(issue315FolderID, moved); errMsg != "" {
		t.Fatalf("SetFolderPath onto the moved vault = %q, want it to go on there", errMsg)
	}
	rig.expectSyncGoesOn(t, moved, map[string]string{
		"kept.md":   "# Kept\n\nnever touched again\n",
		"edited.md": issue315NoteV1,
		"new.md":    "# New\n\nwritten after the move\n",
	})
}

// The repair this path exists for (#25): the container changes its path and
// everything in it is where it was, under another name.
func TestIssue315_AMovedContainerGoesOnWhereItIs(t *testing.T) {
	if testing.Short() {
		t.Skip("real peer process; skipped in -short")
	}
	rig := newIssue315Rig(t)

	container := filepath.Join(rig.configDir, "selected-1-after-the-move")
	if err := os.Rename(filepath.Dir(rig.own), container); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(container, "Vault")

	if errMsg := SetFolderPath(issue315FolderID, moved); errMsg != "" {
		t.Fatalf("SetFolderPath after the container moved = %q, want it to go on there", errMsg)
	}
	rig.expectSyncGoesOn(t, moved, map[string]string{
		"kept.md":   "# Kept\n\nnever touched again\n",
		"edited.md": issue315NoteV1,
	})
}

// expectSyncGoesOn checks a folder that was re-pointed at dir: the peer gets
// what dir holds and loses nothing, and what the peer writes next arrives in
// dir.
func (r *issue315Rig) expectSyncGoesOn(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	var folders []FolderInfo
	if err := json.Unmarshal([]byte(GetFoldersJSON()), &folders); err != nil {
		t.Fatalf("GetFoldersJSON unmarshal: %v", err)
	}
	if len(folders) != 1 || folders[0].Path != dir {
		t.Fatalf("the folder is at %v, want it at %s", folders, dir)
	}
	waitFor(t, "the peer to hold what the folder holds", 120*time.Second, func() bool {
		for name, want := range files {
			if got, ok := r.peerFile(t, name); !ok || got != want {
				return false
			}
		}
		return true
	})
	r.writePeer(t, "after.md", "# After\n\nwritten on the peer after the repair\n")
	r.peer.scanAndSettle(t, issue315FolderID)
	want := map[string]string{"after.md": "# After\n\nwritten on the peer after the repair\n"}
	for name, content := range files {
		want[name] = content
	}
	r.waitBridgeHolds(t, dir, want)
	for name, content := range files {
		if got, ok := r.peerFile(t, name); !ok || got != content {
			t.Fatalf("the peer's %s changed after the repair: exists=%v content=%q", name, ok, got)
		}
	}
}

// issue315FakeIndex answers the proof from a list instead of a database.
type issue315FakeIndex struct {
	entries []protocol.FileInfo
	err     error
}

func (i issue315FakeIndex) AllLocalFiles(string, protocol.DeviceID) (iter.Seq[protocol.FileInfo], func() error) {
	return func(yield func(protocol.FileInfo) bool) {
		for _, entry := range i.entries {
			if !yield(entry) {
				return
			}
		}
	}, func() error { return i.err }
}

func (issue315FakeIndex) GetMtime(string, string) (time.Time, time.Time) {
	return time.Time{}, time.Time{}
}
func (issue315FakeIndex) PutMtime(string, string, time.Time, time.Time) error {
	return errors.New("the proof must not write")
}
func (issue315FakeIndex) DeleteMtime(string, string) error {
	return errors.New("the proof must not write")
}

// What agrees with a folder's index and what does not, against the index a
// real engine wrote: every difference a scan would send out as a deletion, as
// a replaced file or as a file brought back is a refusal.
func TestIssue315_StorageProof(t *testing.T) {
	const folderID = "issue315-proof"
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	own := filepath.Join(configDir, "selected", "Vault")
	if errMsg := AddFolder(folderID, "Vault", own); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}
	write := func(dir, name, content string) {
		t.Helper()
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(own, "kept.md", "# Kept\n")
	write(own, "notes/a.md", "# A\n\nfirst\n")
	write(own, "notes/deep/b.md", "# B\n")
	write(own, "removed.md", "# Removed\n")
	write(own, "hidden.md", "# Hidden\n")

	mu.Lock()
	folderCfg := stCfg.Folders()[folderID]
	index, ok := stDB.(localIndex)
	mu.Unlock()
	if !ok {
		t.Fatal("the engine's database does not offer what the proof reads — adjust localIndex for this Syncthing version")
	}
	recorded := func(name string) (protocol.FileInfo, bool) {
		entries, errFn := index.AllLocalFiles(folderID, protocol.LocalDeviceID)
		var found protocol.FileInfo
		var has bool
		for entry := range entries {
			if entry.Name == name {
				found, has = entry, true
			}
		}
		if err := errFn(); err != nil {
			t.Fatalf("read the index: %v", err)
		}
		return found, has
	}
	scanUntil := func(what string, cond func() bool) {
		t.Helper()
		waitFor(t, what, 60*time.Second, func() bool {
			RescanFolder(folderID)
			return cond()
		})
	}
	scanUntil("the first scan", func() bool {
		for _, name := range []string{"kept.md", "notes", "notes/a.md", "notes/deep", "notes/deep/b.md", "removed.md", "hidden.md"} {
			if entry, has := recorded(name); !has || entry.IsDeleted() {
				return false
			}
		}
		return true
	})
	// One note is deleted and one file becomes ignored, both recorded.
	if err := os.Remove(filepath.Join(own, "removed.md")); err != nil {
		t.Fatal(err)
	}
	if errMsg := SetFolderIgnores(folderID, `["hidden.md"]`); errMsg != "" {
		t.Fatalf("SetFolderIgnores failed: %s", errMsg)
	}
	scanUntil("the deletion and the ignore pattern in the index", func() bool {
		removed, hasRemoved := recorded("removed.md")
		hidden, hasHidden := recorded("hidden.md")
		return hasRemoved && removed.IsDeleted() && hasHidden && hidden.IsIgnored()
	})

	noteA, _ := recorded("notes/a.md")
	const refused = folderStorageMismatch
	tests := []struct {
		name   string
		change func(t *testing.T, dir string)
		index  func() localIndex
		want   string
	}{
		{name: "the directory as it was last scanned", want: ""},
		{name: "a note the index has never seen", want: "", change: func(t *testing.T, dir string) {
			write(dir, "new.md", "# New\n")
			write(dir, "notes/new-too.md", "# New too\n")
		}},
		{name: "an ignored file with other content", want: "", change: func(t *testing.T, dir string) {
			write(dir, "hidden.md", "# Hidden\n\nand changed\n")
		}},
		{name: "an ignored file that is missing", want: "", change: func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "hidden.md")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a note is missing", want: refused, change: func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "kept.md")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a note holds other content", want: refused, change: func(t *testing.T, dir string) {
			write(dir, "notes/a.md", "# A\n\nan earlier draft\n")
		}},
		{name: "every note carries another time and the recorded bytes", want: "", change: func(t *testing.T, dir string) {
			// What a restore from a backup can leave: times rounded, content as it was.
			for _, name := range []string{"kept.md", "notes/a.md", "notes/deep/b.md"} {
				path := filepath.Join(dir, filepath.FromSlash(name))
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				rounded := info.ModTime().Truncate(time.Second).Add(-time.Second)
				if err := os.Chtimes(path, rounded, rounded); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{name: "a note of the same size carries another time and other bytes", want: refused, change: func(t *testing.T, dir string) {
			// "first" and "FIRST" are of one length.
			write(dir, "notes/a.md", "# A\n\nFIRST\n")
			earlier := time.Now().Add(-48 * time.Hour)
			if err := os.Chtimes(filepath.Join(dir, "notes", "a.md"), earlier, earlier); err != nil {
				t.Fatal(err)
			}
		}},
		{
			name: "a note with another time whose bytes the index does not hold", want: refused,
			change: func(t *testing.T, dir string) {
				earlier := time.Now().Add(-48 * time.Hour)
				if err := os.Chtimes(filepath.Join(dir, "notes", "a.md"), earlier, earlier); err != nil {
					t.Fatal(err)
				}
			},
			index: func() localIndex {
				bare := noteA
				bare.Blocks, bare.BlocksHash = nil, nil
				return issue315FakeIndex{entries: []protocol.FileInfo{bare}}
			},
		},
		{name: "a folder with its notes is missing", want: refused, change: func(t *testing.T, dir string) {
			if err := os.RemoveAll(filepath.Join(dir, "notes", "deep")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a note where a folder was", want: refused, change: func(t *testing.T, dir string) {
			if err := os.RemoveAll(filepath.Join(dir, "notes", "deep")); err != nil {
				t.Fatal(err)
			}
			write(dir, "notes/deep", "not a folder\n")
		}},
		{name: "a deleted note is there again", want: refused, change: func(t *testing.T, dir string) {
			write(dir, "removed.md", "# Removed\n")
		}},
		{
			// Only the note is in this index, its folder is not: the note is
			// found with its size and time behind the link, and still it is
			// not where the scanner looks.
			name: "a note behind a link to a folder elsewhere", want: refused,
			change: func(t *testing.T, dir string) {
				elsewhere := filepath.Join(filepath.Dir(dir), "elsewhere")
				if err := os.Rename(filepath.Join(dir, "notes"), elsewhere); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(elsewhere, filepath.Join(dir, "notes")); err != nil {
					t.Fatal(err)
				}
			},
			index: func() localIndex { return issue315FakeIndex{entries: []protocol.FileInfo{noteA}} },
		},
		{
			name: "an entry the index itself marks as not known", want: refused,
			index: func() localIndex {
				unsure := noteA
				unsure.LocalFlags |= protocol.FlagLocalMustRescan
				return issue315FakeIndex{entries: []protocol.FileInfo{unsure}}
			},
		},
		{
			name: "an index that cannot be read to its end", want: "check folder storage: read the index: disk I/O error",
			index: func() localIndex {
				return issue315FakeIndex{entries: []protocol.FileInfo{noteA}, err: errors.New("disk I/O error")}
			},
		},
		{
			name: "no index", want: "check folder storage: the index is not available",
			index: func() localIndex { return nil },
		},
		{
			name: "an index without entries", want: "",
			change: func(t *testing.T, dir string) {
				if err := os.Remove(filepath.Join(dir, "kept.md")); err != nil {
					t.Fatal(err)
				}
			},
			index: func() localIndex { return issue315FakeIndex{} },
		},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(configDir, "case-"+strconv.Itoa(i), "Vault")
			issue315CopyTree(t, own, dir)
			if tt.change != nil {
				tt.change(t, dir)
			}
			against := index
			if tt.index != nil {
				against = tt.index()
			}
			if got := verifyFolderStorage(folderCfg, dir, against); got != tt.want {
				t.Fatalf("verifyFolderStorage = %q, want %q", got, tt.want)
			}
		})
	}
}

// The checks run without the bridge lock. What they found holds for the
// folder as it was then: one that was removed or moved since is not written.
func TestIssue315_AFolderThatChangedWhileItWasCheckedIsNotMoved(t *testing.T) {
	const folderID = "issue315-changed"
	const refused = "the folder changed while the target was checked"
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	own := filepath.Join(configDir, "selected-1", "Vault")
	other := filepath.Join(configDir, "selected-2", "Vault")
	if errMsg := AddFolder(folderID, "Vault", own); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}
	configuredPath := func() string {
		var folders []FolderInfo
		if err := json.Unmarshal([]byte(GetFoldersJSON()), &folders); err != nil {
			t.Fatalf("GetFoldersJSON unmarshal: %v", err)
		}
		for _, folder := range folders {
			if folder.ID == folderID {
				return folder.Path
			}
		}
		return ""
	}

	// Checked at one path, found at another: another call moved it.
	if errMsg := writeFolderPath(folderID, filepath.Join(configDir, "selected-0", "Vault"), other); errMsg != refused {
		t.Fatalf("writeFolderPath for a folder that moved = %q, want %q", errMsg, refused)
	}
	if got := configuredPath(); got != own {
		t.Fatalf("the folder is at %q after the refusal, want it left at %q", got, own)
	}

	// Still the folder that was checked: written.
	if errMsg := writeFolderPath(folderID, own, other); errMsg != "" {
		t.Fatalf("writeFolderPath for the folder as it was checked = %q, want ''", errMsg)
	}
	if got := configuredPath(); got != other {
		t.Fatalf("the folder is at %q, want %q", got, other)
	}

	// Removed since: nothing to write, and nothing is added back.
	if errMsg := RemoveFolder(folderID); errMsg != "" {
		t.Fatalf("RemoveFolder failed: %s", errMsg)
	}
	if errMsg := writeFolderPath(folderID, other, own); errMsg != refused {
		t.Fatalf("writeFolderPath for a removed folder = %q, want %q", errMsg, refused)
	}
	if got := configuredPath(); got != "" {
		t.Fatalf("a removed folder is configured again at %q", got)
	}
}
