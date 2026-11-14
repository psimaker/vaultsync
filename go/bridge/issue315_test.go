package bridge

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
// The tests that claim something about the other device run the bridge
// against a real loopback peer: a folder continued in a directory that is
// behind its index sends the difference out as deletions and as old versions,
// and that only shows there.
//
// Every copy in these tests is made while its original exists. A file system
// may hand a deleted file's number to the next file created (Linux does), and
// a copy made after the original is gone could then carry the original's
// number by chance.

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
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		info, err := d.Info()
		if err == nil {
			var data []byte
			if data, err = os.ReadFile(path); err == nil {
				if err = os.WriteFile(target, data, 0o600); err == nil {
					err = os.Chtimes(target, info.ModTime(), info.ModTime())
				}
			}
		}
		// A file that goes away while the tree is read is not part of the
		// copy: a stopped engine may still be finishing a temporary file.
		if os.IsNotExist(err) {
			return nil
		}
		return err
	})
	if err != nil {
		t.Fatalf("copy %s: %v", src, err)
	}
}

// issue315MarkerNumber returns the file number of the folder's marker in dir.
func issue315MarkerNumber(t *testing.T, folderID, dir string) uint64 {
	t.Helper()
	number, err := fileNumber(folderMarkerPath(config.FolderConfiguration{ID: folderID}, dir))
	if err != nil {
		t.Fatalf("the marker of %s in %s: %v", folderID, dir, err)
	}
	return number
}

// issue315Store returns the engine's database as the records use it.
func issue315Store(t *testing.T) storageRecords {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	store, ok := stDB.(storageRecords)
	if !ok {
		t.Fatal("the engine's database does not offer what the records need — adjust storageRecords for this Syncthing version")
	}
	return store
}

// issue315Record reads a folder's record from the engine's database.
func issue315Record(t *testing.T, folderID string) (folderStorageRecord, bool) {
	t.Helper()
	record, found, err := readFolderStorage(issue315Store(t), folderID)
	if err != nil {
		t.Fatalf("read the record of %s: %v", folderID, err)
	}
	return record, found
}

// issue315ConfigOnDisk waits until the configuration file holds text. The
// engine writes it a moment after a change, and an engine stopped before that
// starts again without the change.
func issue315ConfigOnDisk(t *testing.T, configDir, text string) {
	t.Helper()
	waitFor(t, "the change in the configuration on disk", 30*time.Second, func() bool {
		data, err := os.ReadFile(filepath.Join(configDir, "config.xml"))
		return err == nil && strings.Contains(string(data), text)
	})
}

// issue315FolderPath returns the path a folder is configured at.
func issue315FolderPath(t *testing.T, folderID string) string {
	t.Helper()
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
	earlier := filepath.Join(rig.configDir, "selected-2", "Vault")
	issue315CopyTree(t, rig.own, earlier)
	if errMsg := verifyFolderMarker(config.FolderConfiguration{ID: issue315FolderID}, earlier); errMsg != "" {
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

	// A second copy, made now: it holds exactly what the folder holds.
	identical := filepath.Join(rig.configDir, "selected-3", "Vault")
	issue315CopyTree(t, rig.own, identical)

	// The folder's own directory goes away with the selected folder it was in.
	if err := os.RemoveAll(filepath.Dir(rig.own)); err != nil {
		t.Fatal(err)
	}

	// What the path repair does once the other folder is selected: the
	// folder's directory is gone, a directory of its name exists there.
	errMsg := SetFolderPath(issue315FolderID, earlier)
	if errMsg == "" {
		t.Errorf("SetFolderPath took a copy that is behind the folder's index for the directory the folder synced in")
		// Show what that costs: the scan of the copy reaches the peer.
		lost := waitForCondition(90*time.Second, func() bool {
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

	// Refused with the text the app knows this case by, and the folder stays
	// where it was.
	if errMsg != FolderStorageMismatchError() {
		t.Fatalf("SetFolderPath refused the copy with %q, want %q", errMsg, FolderStorageMismatchError())
	}
	// A copy is a copy whatever it holds: nothing in it is read to find out.
	if errMsg := SetFolderPath(issue315FolderID, identical); errMsg != FolderStorageMismatchError() {
		t.Fatalf("SetFolderPath onto a copy that holds what the folder holds = %q, want %q", errMsg, FolderStorageMismatchError())
	}
	if got := issue315FolderPath(t, issue315FolderID); got != rig.own {
		t.Fatalf("after the refusals the folder is at %q, want it left at %q", got, rig.own)
	}
}

// A vault moved as a whole — in the Files app, under another selected folder —
// is the directory the folder synced in, whatever happened in it since the
// engine last looked: Obsidian edits, renames and deletes notes while
// VaultSync is not running. The repair goes on there, and each of those
// changes goes out as the change it is.
func TestIssue315_AMovedVaultGoesOnWithWhatChangedInIt(t *testing.T) {
	if testing.Short() {
		t.Skip("real peer process; skipped in -short")
	}
	rig := newIssue315Rig(t)
	rig.ownDirectoryGoesOn(t, func() string {
		moved := filepath.Join(rig.configDir, "selected-2", "Vault")
		if err := os.MkdirAll(filepath.Dir(moved), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(rig.own, moved); err != nil {
			t.Fatal(err)
		}
		return moved
	})
}

// The repair this path exists for (#25): the container changes its path and
// everything in it is where it was, under another name.
func TestIssue315_AMovedContainerGoesOnWithWhatChangedInIt(t *testing.T) {
	if testing.Short() {
		t.Skip("real peer process; skipped in -short")
	}
	rig := newIssue315Rig(t)
	rig.ownDirectoryGoesOn(t, func() string {
		container := filepath.Join(rig.configDir, "selected-1-after-the-move")
		if err := os.Rename(filepath.Dir(rig.own), container); err != nil {
			t.Fatal(err)
		}
		return filepath.Join(container, "Vault")
	})
}

// ownDirectoryGoesOn moves the folder's own directory with move, changes
// notes in it that the engine has not seen, and expects the repair to go on
// there: the peer gets every change, and what the peer writes next arrives.
func (r *issue315Rig) ownDirectoryGoesOn(t *testing.T, move func() string) {
	t.Helper()
	const (
		kept     = "# Kept\n\nnever touched again\n"
		renamed  = "# Renamed\n\nkeeps its text, gets another name\n"
		returned = "# Returned\n\nwritten again under a name that was deleted\n"
	)
	r.writePeer(t, "old-name.md", renamed)
	r.writePeer(t, "deleted.md", "# Deleted\n\ngoes away while VaultSync is not running\n")
	r.writePeer(t, "returns.md", "# Returns\n\nfirst life\n")
	r.peer.scanAndSettle(t, issue315FolderID)
	r.waitBridgeHolds(t, r.own, map[string]string{
		"kept.md": kept, "edited.md": issue315NoteV1, "old-name.md": renamed,
		"deleted.md": "# Deleted\n\ngoes away while VaultSync is not running\n",
		"returns.md": "# Returns\n\nfirst life\n",
	})
	// One name the index holds as deleted.
	if err := os.Remove(filepath.Join(r.peerVault, "returns.md")); err != nil {
		t.Fatal(err)
	}
	r.peer.scanAndSettle(t, issue315FolderID)
	waitFor(t, "the deletion to reach the bridge's folder", 120*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(r.own, "returns.md"))
		return os.IsNotExist(err)
	})
	r.waitBridgeHolds(t, r.own, map[string]string{
		"kept.md": kept, "edited.md": issue315NoteV1, "old-name.md": renamed,
		"deleted.md": "# Deleted\n\ngoes away while VaultSync is not running\n",
	})

	// The directory is known by its marker's file number, and the move keeps it.
	record, found := issue315Record(t, issue315FolderID)
	if !found || record.Marker != issue315MarkerNumber(t, issue315FolderID, r.own) {
		t.Fatalf("before the move the folder's record is %+v (found %v), want one that names the marker in its directory", record, found)
	}
	moved := move()

	// What the engine has not seen: the folder's path is dead, nothing scans.
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(moved, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("edited.md", issue315NoteV2)
	if err := os.Rename(filepath.Join(moved, "old-name.md"), filepath.Join(moved, "new-name.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(moved, "deleted.md")); err != nil {
		t.Fatal(err)
	}
	write("returns.md", returned)

	if errMsg := SetFolderPath(issue315FolderID, moved); errMsg != "" {
		t.Fatalf("SetFolderPath onto the folder's own directory = %q, want it to go on there", errMsg)
	}
	if got := issue315FolderPath(t, issue315FolderID); got != moved {
		t.Fatalf("the folder is at %q, want it at %q", got, moved)
	}
	if after, _ := issue315Record(t, issue315FolderID); after != record {
		t.Fatalf("the record changed from %+v to %+v although the folder is in the directory it was in", record, after)
	}

	want := map[string]string{
		"kept.md": kept, "edited.md": issue315NoteV2, "new-name.md": renamed, "returns.md": returned,
	}
	waitFor(t, "the peer to hold what the folder holds", 120*time.Second, func() bool {
		for name, content := range want {
			if got, ok := r.peerFile(t, name); !ok || got != content {
				return false
			}
		}
		for _, name := range []string{"old-name.md", "deleted.md"} {
			if _, ok := r.peerFile(t, name); ok {
				return false
			}
		}
		return true
	})
	r.writePeer(t, "after.md", "# After\n\nwritten on the peer after the repair\n")
	r.peer.scanAndSettle(t, issue315FolderID)
	want["after.md"] = "# After\n\nwritten on the peer after the repair\n"
	r.waitBridgeHolds(t, moved, want)
}

// issue315Engine is a running engine with one vault that no peer shares,
// under a first selected folder. What these tests claim is about the bridge
// alone.
type issue315Engine struct {
	t        *testing.T
	folderID string
	base     string
	copies   int
}

func newIssue315Engine(t *testing.T, folderID string) (*issue315Engine, string) {
	t.Helper()
	e := &issue315Engine{t: t, folderID: folderID, base: testConfigDir(t)}
	if errMsg := StartSyncthing(e.base); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	own := filepath.Join(e.base, "selected-1", "Vault")
	if errMsg := AddFolder(folderID, "Vault", own); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}
	if err := os.WriteFile(filepath.Join(own, "note.md"), []byte("# Note\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return e, own
}

// copyAndRemove makes a copy of dir under another selected folder and then
// removes dir: the folder's directory is gone, and a copy of it is offered.
func (e *issue315Engine) copyAndRemove(dir string) string {
	e.t.Helper()
	e.copies++
	copied := filepath.Join(e.base, "copy-"+strconv.Itoa(e.copies), "Vault")
	issue315CopyTree(e.t, dir, copied)
	if err := os.RemoveAll(dir); err != nil {
		e.t.Fatal(err)
	}
	return copied
}

// expectRecord checks that the folder's record names the marker in dir, for
// the index and the anchor the engine has now.
func (e *issue315Engine) expectRecord(dir string) folderStorageRecord {
	e.t.Helper()
	record, found := issue315Record(e.t, e.folderID)
	index, err := issue315Store(e.t).GetIndexID(e.folderID, protocol.LocalDeviceID)
	if err != nil {
		e.t.Fatalf("GetIndexID: %v", err)
	}
	mu.Lock()
	anchor := stStorageAnchor
	mu.Unlock()
	want := folderStorageRecord{Marker: issue315MarkerNumber(e.t, e.folderID, dir), Index: uint64(index), Anchor: anchor}
	if !found || record != want || anchor == 0 {
		e.t.Fatalf("the folder's record is %+v (found %v), want %+v with an anchor", record, found, want)
	}
	return record
}

func (e *issue315Engine) putRecord(value []byte) {
	e.t.Helper()
	if err := issue315Store(e.t).PutKV(folderStorageKeyPrefix+e.folderID, value); err != nil {
		e.t.Fatalf("PutKV: %v", err)
	}
}

// issue315FailingStore is the engine's database with reads that fail.
type issue315FailingStore struct {
	storageRecords
	io.Closer
	kvErr, indexErr error
}

func (s issue315FailingStore) GetKV(key string) ([]byte, error) {
	if s.kvErr != nil {
		return nil, s.kvErr
	}
	return s.storageRecords.GetKV(key)
}

func (s issue315FailingStore) GetIndexID(folder string, device protocol.DeviceID) (protocol.IndexID, error) {
	if s.indexErr != nil {
		return 0, s.indexErr
	}
	return s.storageRecords.GetIndexID(folder, device)
}

// What the rule rests on, on the file system the tests run on: a file keeps
// its number through a rename, of itself or of a directory above it, and a
// copy is another file. A link is pinned too: a hard link carries the number
// (the Files app makes none), a symbolic link has its own.
func TestIssue315_FileNumber(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, "a", "marker.txt")
	if err := os.MkdirAll(filepath.Dir(original), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, []byte("marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	number := func(path string) uint64 {
		t.Helper()
		n, err := fileNumber(path)
		if err != nil {
			t.Fatalf("fileNumber(%s): %v", path, err)
		}
		return n
	}
	want := number(original)

	issue315CopyTree(t, filepath.Join(dir, "a"), filepath.Join(dir, "copy"))
	if got := number(filepath.Join(dir, "copy", "marker.txt")); got == want {
		t.Fatalf("a copy has the number of its original (%d)", got)
	}
	hard, soft := filepath.Join(dir, "hard.txt"), filepath.Join(dir, "soft.txt")
	if err := os.Link(original, hard); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(original, soft); err != nil {
		t.Fatal(err)
	}
	if got := number(hard); got != want {
		t.Fatalf("a hard link has number %d, its file %d", got, want)
	}
	if got := number(soft); got == want {
		t.Fatalf("a symbolic link has the number of the file it leads to (%d)", got)
	}
	if err := os.Rename(original, filepath.Join(dir, "a", "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "a"), filepath.Join(dir, "b")); err != nil {
		t.Fatal(err)
	}
	if got := number(filepath.Join(dir, "b", "renamed.txt")); got != want {
		t.Fatalf("after two renames the file has number %d, was %d", got, want)
	}
	if _, err := fileNumber(original); err == nil {
		t.Fatal("fileNumber answered for a path that is gone")
	}
}

// When a record speaks and when it does not. A copy of the folder's directory
// is what tells the two apart: a record that applies refuses it, and where
// none applies the marker lets it through, as it did before there were
// records.
func TestIssue315_WhenARecordApplies(t *testing.T) {
	const folderID = "issue315-record"
	e, own := newIssue315Engine(t, folderID)
	defer StopSyncthing()
	mismatch := FolderStorageMismatchError()

	// Written when the folder is added: its directory and marker are there.
	e.expectRecord(own)

	// The record applies: a copy is refused, the directory itself is taken.
	copied := filepath.Join(e.base, "copy-0", "Vault")
	issue315CopyTree(t, own, copied)
	moved := filepath.Join(e.base, "selected-2", "Vault")
	if err := os.MkdirAll(filepath.Dir(moved), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(own, moved); err != nil {
		t.Fatal(err)
	}
	if errMsg := SetFolderPath(folderID, copied); errMsg != mismatch {
		t.Fatalf("SetFolderPath onto a copy = %q, want %q", errMsg, mismatch)
	}
	if errMsg := SetFolderPath(folderID, moved); errMsg != "" {
		t.Fatalf("SetFolderPath onto the moved directory = %q, want ''", errMsg)
	}
	record := e.expectRecord(moved)

	// A record written for another index says nothing about this one.
	current := e.copyAndRemove(moved)
	if errMsg := SetFolderPath(folderID, current); errMsg != mismatch {
		t.Fatalf("SetFolderPath onto a copy = %q, want %q", errMsg, mismatch)
	}
	other := record
	other.Index++
	value, err := json.Marshal(other)
	if err != nil {
		t.Fatal(err)
	}
	e.putRecord(value)
	if errMsg := SetFolderPath(folderID, current); errMsg != "" {
		t.Fatalf("SetFolderPath with a record for another index ID = %q, want the marker to decide", errMsg)
	}
	// The re-point leaves a record of the directory the folder is in now.
	e.expectRecord(current)

	// Nor does something in the record's place that is not a record.
	current = e.copyAndRemove(current)
	e.putRecord([]byte("not a record"))
	if errMsg := SetFolderPath(folderID, current); errMsg != "" {
		t.Fatalf("SetFolderPath with an unreadable record = %q, want the marker to decide", errMsg)
	}
	e.expectRecord(current)

	// What cannot be read is not taken for "no record" (decision 039): the
	// folder's own directory waits until the read works again.
	renamed := filepath.Join(e.base, "selected-3", "Vault")
	if err := os.MkdirAll(filepath.Dir(renamed), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(current, renamed); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	database := stDB
	mu.Unlock()
	for _, failing := range []struct {
		store issue315FailingStore
		want  string
	}{
		{issue315FailingStore{kvErr: errors.New("disk I/O error")}, "check folder storage: read the record: disk I/O error"},
		{issue315FailingStore{indexErr: errors.New("disk I/O error")}, "check folder storage: read the index ID: disk I/O error"},
	} {
		failing.store.storageRecords, failing.store.Closer = database.(storageRecords), database
		mu.Lock()
		stDB = failing.store
		mu.Unlock()
		errMsg := SetFolderPath(folderID, renamed)
		mu.Lock()
		stDB = database
		mu.Unlock()
		if errMsg != failing.want {
			t.Fatalf("SetFolderPath while the database cannot be read = %q, want %q", errMsg, failing.want)
		}
	}
	if got := issue315FolderPath(t, folderID); got != current {
		t.Fatalf("after the refusals the folder is at %q, want it left at %q", got, current)
	}
	if errMsg := SetFolderPath(folderID, renamed); errMsg != "" {
		t.Fatalf("SetFolderPath once the database reads again = %q, want ''", errMsg)
	}

	// An install that comes from a version without records gets them at its
	// next start, for every folder whose directory is there.
	e.putRecord([]byte("not a record"))
	issue315ConfigOnDisk(t, e.base, renamed)
	if errMsg := StopSyncthing(); errMsg != "" {
		t.Fatalf("StopSyncthing() = %q", errMsg)
	}
	if errMsg := StartSyncthing(e.base); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	e.expectRecord(renamed)

	// An install whose anchor is gone no longer knows what its records were
	// written on. A new anchor is made, the old records stop applying, and
	// the next re-point writes one that does.
	if errMsg := StopSyncthing(); errMsg != "" {
		t.Fatalf("StopSyncthing() = %q", errMsg)
	}
	anchorPath := filepath.Join(e.base, storageAnchorName)
	// Kept under another name, so its file number is not handed out again.
	if err := os.Rename(anchorPath, anchorPath+".kept"); err != nil {
		t.Fatal(err)
	}
	current = e.copyAndRemove(renamed)
	if errMsg := StartSyncthing(e.base); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	if stale, _ := issue315Record(t, folderID); stale.Anchor != record.Anchor {
		t.Fatalf("the record of a folder whose directory is gone was rewritten at start: %+v", stale)
	}
	if errMsg := SetFolderPath(folderID, current); errMsg != "" {
		t.Fatalf("SetFolderPath with a record from before the anchor was lost = %q, want the marker to decide", errMsg)
	}
	if fresh := e.expectRecord(current); fresh.Anchor == record.Anchor {
		t.Fatalf("the new record carries the lost anchor's number: %+v", fresh)
	}
	// And from here on a copy is refused again.
	if errMsg := SetFolderPath(folderID, e.copyAndRemove(current)); errMsg != mismatch {
		t.Fatalf("SetFolderPath onto a copy, with a fresh record = %q, want %q", errMsg, mismatch)
	}
}

// The way on from the card: remove the vault, accept it again in a folder of
// its own. The vault's earlier directory stays behind, and selecting its
// folder again later must not bring the folder back into it.
func TestIssue315_AVaultAddedAgainDoesNotGoBackToWhatItLeftBehind(t *testing.T) {
	const folderID = "issue315-again"
	const markerMissing = "target does not contain this folder's data (marker missing)"
	e, first := newIssue315Engine(t, folderID)
	defer StopSyncthing()
	firstIndex := e.expectRecord(first).Index

	// Removed while its directory cannot be reached — the state the card is
	// shown in. The engine cannot take the marker along, so what is left
	// behind still carries it.
	away := first + "-out-of-reach"
	if err := os.Rename(first, away); err != nil {
		t.Fatal(err)
	}
	if errMsg := RemoveFolder(folderID); errMsg != "" {
		t.Fatalf("RemoveFolder failed: %s", errMsg)
	}
	if err := os.Rename(away, first); err != nil {
		t.Fatal(err)
	}
	if errMsg := verifyFolderMarker(config.FolderConfiguration{ID: folderID}, first); errMsg != "" {
		t.Fatalf("what the removed folder left behind does not carry its marker (%s) — the test no longer builds the state it is about", errMsg)
	}

	// Accepted again elsewhere: a new index, and a record for it.
	second := filepath.Join(e.base, "selected-2", "Vault")
	if errMsg := AddFolder(folderID, "Vault", second); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}
	if again := e.expectRecord(second); again.Index == firstIndex {
		t.Fatalf("a folder added again has the index ID of the folder removed before (%d): the removed folder's record would apply to it", again.Index)
	}

	// The new directory goes away, the folder with the old one is selected.
	secondAway := second + "-out-of-reach"
	if err := os.Rename(second, secondAway); err != nil {
		t.Fatal(err)
	}
	if errMsg := SetFolderPath(folderID, first); errMsg != FolderStorageMismatchError() {
		t.Fatalf("SetFolderPath onto what the folder left behind = %q, want %q", errMsg, FolderStorageMismatchError())
	}

	// Removed while its directory is there, the engine takes the marker with
	// it: what stays behind is refused for the missing marker, record or not.
	if err := os.Rename(secondAway, second); err != nil {
		t.Fatal(err)
	}
	if errMsg := RemoveFolder(folderID); errMsg != "" {
		t.Fatalf("RemoveFolder failed: %s", errMsg)
	}
	if errMsg := verifyFolderMarker(config.FolderConfiguration{ID: folderID}, second); errMsg != markerMissing {
		t.Fatalf("after removing a folder whose directory is there, its marker check says %q, want %q", errMsg, markerMissing)
	}
	third := filepath.Join(e.base, "selected-3", "Vault")
	if errMsg := AddFolder(folderID, "Vault", third); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}
	if err := os.Rename(third, third+"-out-of-reach"); err != nil {
		t.Fatal(err)
	}
	if errMsg := SetFolderPath(folderID, second); errMsg != markerMissing {
		t.Fatalf("SetFolderPath onto a directory without the marker = %q, want %q", errMsg, markerMissing)
	}
}

// A restore or a migration to another iPhone creates every file anew: the
// vault's, the marker's, and the app's own. No record written before it says
// anything about the directories found after it, so the marker decides, as it
// did before there were records — and the first re-point writes the record
// that knows the restored directory from then on.
func TestIssue315_AfterARestoreTheMarkerDecides(t *testing.T) {
	const folderID = "issue315-restore"
	base := testConfigDir(t)
	device := filepath.Join(base, "device")
	own := filepath.Join(device, "selected", "Vault")
	if errMsg := StartSyncthing(filepath.Join(device, "config")); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	if errMsg := AddFolder(folderID, "Vault", own); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}
	indexed := func(files int, bytes int64) {
		t.Helper()
		waitFor(t, "the folder's index", 60*time.Second, func() bool {
			RescanFolder(folderID)
			var st FolderStatus
			if err := json.Unmarshal([]byte(GetFolderStatusJSON(folderID)), &st); err != nil {
				return false
			}
			return st.LocalFiles == files && st.LocalBytes == bytes
		})
	}
	if err := os.WriteFile(filepath.Join(own, "note.md"), []byte(issue315NoteV1), 0o600); err != nil {
		t.Fatal(err)
	}
	indexed(1, int64(len(issue315NoteV1)))
	// An earlier copy of the vault, in a folder of its own.
	issue315CopyTree(t, own, filepath.Join(device, "earlier", "Vault"))
	if err := os.WriteFile(filepath.Join(own, "note.md"), []byte(issue315NoteV2), 0o600); err != nil {
		t.Fatal(err)
	}
	indexed(1, int64(len(issue315NoteV2)))
	before, found := issue315Record(t, folderID)
	if !found {
		t.Fatal("the folder has no record before the restore")
	}
	issue315ConfigOnDisk(t, filepath.Join(device, "config"), folderID)
	if errMsg := StopSyncthing(); errMsg != "" {
		t.Fatalf("StopSyncthing() = %q", errMsg)
	}

	// Two restores of the same device, each a copy of everything it held.
	restoredA, restoredB := filepath.Join(base, "restored-a"), filepath.Join(base, "restored-b")
	issue315CopyTree(t, device, restoredA)
	issue315CopyTree(t, device, restoredB)
	if err := os.RemoveAll(device); err != nil {
		t.Fatal(err)
	}

	// The restored vault: taken, and recorded for the restored install.
	if errMsg := StartSyncthing(filepath.Join(restoredA, "config")); errMsg != "" {
		t.Fatalf("StartSyncthing() after the restore failed: %s", errMsg)
	}
	if got := issue315FolderPath(t, folderID); got != own {
		t.Fatalf("after the restore the folder is configured at %q, want the path it had (%q)", got, own)
	}
	if stale, _ := issue315Record(t, folderID); stale != before {
		t.Fatalf("the record changed across the restore without a re-point: %+v, was %+v", stale, before)
	}
	restoredVault := filepath.Join(restoredA, "selected", "Vault")
	if errMsg := SetFolderPath(folderID, restoredVault); errMsg != "" {
		t.Fatalf("SetFolderPath onto the restored vault = %q, want the marker to decide", errMsg)
	}
	e := &issue315Engine{t: t, folderID: folderID, base: restoredA}
	if after := e.expectRecord(restoredVault); after.Anchor == before.Anchor || after.Marker == before.Marker {
		t.Fatalf("the record after the restore (%+v) still carries a file number from before it (%+v)", after, before)
	}
	if errMsg := StopSyncthing(); errMsg != "" {
		t.Fatalf("StopSyncthing() = %q", errMsg)
	}

	// NOT COVERED, and pinned here so that it is known: if the folder
	// selected first after a restore holds an earlier copy of the vault, the
	// copy is taken for the vault, as it was before there were records.
	// Nothing on the restored iPhone tells the two apart (decision 063).
	if errMsg := StartSyncthing(filepath.Join(restoredB, "config")); errMsg != "" {
		t.Fatalf("StartSyncthing() after the second restore failed: %s", errMsg)
	}
	defer StopSyncthing()
	if errMsg := SetFolderPath(folderID, filepath.Join(restoredB, "earlier", "Vault")); errMsg != "" {
		t.Fatalf("SetFolderPath onto an earlier copy after a restore = %q; if records now cover this case, decision 063 and this test say so", errMsg)
	}
}
