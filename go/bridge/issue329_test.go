package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/protocol"
)

// A copy put in a vault's own place, under the vault's own path (#329). In
// the Files app the vault's folder is deleted and an earlier duplicate is
// renamed to its name. The folder's path is alive and holds a `.stfolder`, so
// no path repair runs, and the engine scanned the copy as the folder, changed
// by hand: what the copy lacks went out as deletions, its older notes as the
// current versions. Now the engine's CheckPath asks the folder's record
// (fork patch 007, decision 084), and a folder whose path holds another
// directory than the one it synced in is not synced at all.

const (
	issue329Kept  = "# Kept\n\nnever touched again\n"
	issue329Later = "# Later\n\nwritten after the copy was made\n"
	// The reason the bridge gives the app for a copy in a folder's place,
	// and for a check that could not read what it compares.
	issue329NotItsDirectory = "folder_not_its_directory"
	issue329Unchecked       = "folder_directory_unchecked"
)

// issue329Record301 mirrors folderStorageRecord as released in VaultSync
// 3.0.1 (tag v3.0.1, go/bridge/folderstorage.go): the record before it held
// the file number of the marker's directory. It stands for every version
// that does not know that field.
type issue329Record301 struct {
	Marker uint64 `json:"marker"`
	Index  uint64 `json:"index"`
	Anchor uint64 `json:"anchor"`
}

// issue329PutRecord301 stores record as 3.0.1 wrote it, without the field.
func issue329PutRecord301(t *testing.T, folderID string, record folderStorageRecord) {
	t.Helper()
	value, err := json.Marshal(issue329Record301{Marker: record.Marker, Index: record.Index, Anchor: record.Anchor})
	if err != nil {
		t.Fatal(err)
	}
	if err := issue315Store(t).PutKV(folderStorageKeyPrefix+folderID, value); err != nil {
		t.Fatalf("PutKV: %v", err)
	}
}

// issue329RawRecord returns a folder's record as the engine's database holds
// it, byte for byte.
func issue329RawRecord(t *testing.T, folderID string) []byte {
	t.Helper()
	value, err := issue315Store(t).GetKV(folderStorageKeyPrefix + folderID)
	if err != nil {
		t.Fatalf("GetKV: %v", err)
	}
	return value
}

// issue329MakeMarkerAnew writes the folder's marker file anew inside its own
// `.stfolder`: the same content under another file number. The new one is
// written before the old one goes, so it cannot be handed the old number.
func issue329MakeMarkerAnew(t *testing.T, folderID, dir string) {
	t.Helper()
	marker := folderMarkerPath(config.FolderConfiguration{ID: folderID}, dir)
	content, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	before := issue315MarkerNumber(t, folderID, dir)
	if err := os.WriteFile(marker+".new", content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(marker+".new", marker); err != nil {
		t.Fatal(err)
	}
	if after := issue315MarkerNumber(t, folderID, dir); after == before {
		t.Fatalf("the marker made anew has its old file number (%d)", after)
	}
}

// issue329CheckPath asks the engine's CheckPath for a folder as the engine
// has it configured now.
func issue329CheckPath(t *testing.T, folderID string) error {
	t.Helper()
	folder, running, exists := getFolderConfig(folderID)
	if !running || !exists {
		t.Fatalf("no running engine with folder %s", folderID)
	}
	return folder.CheckPath()
}

// issue329Rig is #315's rig with an earlier copy of the vault made by hand
// under another folder, and sync gone on after it: one note changed, one is
// new. The copy holds what the vault held when it was made, its hidden
// entries included.
func issue329Rig(t *testing.T) (*issue315Rig, string) {
	t.Helper()
	rig := newIssue315Rig(t)
	earlier := filepath.Join(rig.configDir, "elsewhere", "Vault")
	issue315CopyTree(t, rig.own, earlier)
	if errMsg := verifyFolderMarker(config.FolderConfiguration{ID: issue315FolderID}, earlier); errMsg != "" {
		t.Fatalf("the copy does not carry the folder's marker (%s) — the test no longer builds the reported state", errMsg)
	}
	rig.writePeer(t, "edited.md", issue315NoteV2)
	rig.writePeer(t, "later.md", issue329Later)
	rig.peer.scanAndSettle(t, issue315FolderID)
	rig.waitBridgeHolds(t, rig.own, issue329Synced())
	return rig, earlier
}

// issue329Synced is what the vault holds once sync has gone on after the copy.
func issue329Synced() map[string]string {
	return map[string]string{"kept.md": issue329Kept, "edited.md": issue315NoteV2, "later.md": issue329Later}
}

// issue329PutInPlace takes the vault's own folder out of its place — kept,
// the way a deleted folder stays in Recently Deleted for a while — and
// renames the copy to the vault's name there. It returns where the vault's
// own folder is now.
func issue329PutInPlace(t *testing.T, folderID, own, copied string) string {
	t.Helper()
	before := issue315MarkerNumber(t, folderID, own)
	aside := filepath.Join(filepath.Dir(filepath.Dir(own)), "recently-deleted", filepath.Base(own))
	for i := 2; ; i++ {
		if _, err := os.Lstat(aside); os.IsNotExist(err) {
			break
		}
		aside = filepath.Join(filepath.Dir(filepath.Dir(own)), "recently-deleted", fmt.Sprintf("%s %d", filepath.Base(own), i))
	}
	if err := os.MkdirAll(filepath.Dir(aside), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(own, aside); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(copied, own); err != nil {
		t.Fatal(err)
	}
	if after := issue315MarkerNumber(t, folderID, own); after == before {
		t.Fatalf("the copy's marker has the original's file number (%d) — the test no longer builds the reported state", after)
	}
	return aside
}

// issue329PutBack moves the copy out of the vault's place and puts the
// vault's own folder back.
func issue329PutBack(t *testing.T, own, aside string) {
	t.Helper()
	if err := os.Rename(own, filepath.Join(filepath.Dir(filepath.Dir(own)), "copy-moved-out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(aside, own); err != nil {
		t.Fatal(err)
	}
}

func issue329Status(t *testing.T, folderID string) FolderStatus {
	t.Helper()
	var st FolderStatus
	if err := json.Unmarshal([]byte(GetFolderStatusJSON(folderID)), &st); err != nil {
		t.Fatalf("GetFolderStatusJSON unmarshal: %v", err)
	}
	return st
}

// peerLost says whether the peer lost what synced after the copy was made.
func (r *issue315Rig) peerLost(t *testing.T) (bool, string) {
	t.Helper()
	edited, _ := r.peerFile(t, "edited.md")
	_, hasLater := r.peerFile(t, "later.md")
	return edited != issue315NoteV2 || !hasLater, fmt.Sprintf(
		"edited.md holds the second version = %v (the first = %v), later.md exists = %v",
		edited == issue315NoteV2, edited == issue315NoteV1, hasLater)
}

// expectPeerWhole fails if the peer loses what synced after the copy was
// made within the given time.
func (r *issue315Rig) expectPeerWhole(t *testing.T, within time.Duration) {
	t.Helper()
	if waitForCondition(within, func() bool { lost, _ := r.peerLost(t); return lost }) {
		_, what := r.peerLost(t)
		t.Fatalf("the peer lost what had synced after the copy was made: %s", what)
	}
}

// expectCopyRefused waits until the engine has refused the folder's
// directory, and fails at once if the peer loses anything meanwhile.
func (r *issue315Rig) expectCopyRefused(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		if lost, what := r.peerLost(t); lost {
			t.Fatalf("the copy in the vault's place reached the peer: %s", what)
		}
		st := issue329Status(t, issue315FolderID)
		if st.ErrorReason == issue329NotItsDirectory {
			issue329ExpectCleanText(t, st.ErrorMessage, r.configDir)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the engine did not refuse the copy in the vault's place: state %q, reason %q", st.State, st.ErrorReason)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// issue329ExpectCleanText checks that a refusal names no path under base —
// where the test keeps the vault — and does not read like a folder that was
// moved or deleted.
func issue329ExpectCleanText(t *testing.T, text, base string) {
	t.Helper()
	if strings.Contains(text, base) {
		t.Errorf("the refusal %q carries a path", text)
	}
	if strings.Contains(text, "marker missing") {
		t.Errorf("the refusal %q reads like a folder that was moved or deleted", text)
	}
}

// issue329Answers is what the engine's check answered, through
// observeDirectoryCheck.
type issue329Answers struct {
	mu      sync.Mutex
	took    map[string][]time.Duration
	refused []string
}

func observeIssue329(t *testing.T) *issue329Answers {
	t.Helper()
	a := &issue329Answers{took: map[string][]time.Duration{}}
	observeDirectoryCheck = func(folderID string, err error, took time.Duration) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.took[folderID] = append(a.took[folderID], took)
		if err != nil {
			a.refused = append(a.refused, folderID+": "+err.Error())
		}
	}
	t.Cleanup(func() { observeDirectoryCheck = nil })
	return a
}

func (a *issue329Answers) asked(folderID string) []time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.took[folderID])
}

func (a *issue329Answers) expectNoRefusal(t *testing.T, step string) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.refused) > 0 {
		t.Fatalf("%s: the check refused the vault's own folder: %q", step, a.refused)
	}
}

// The reported case: the copy is put in place while VaultSync is closed, and
// the next launch starts the engine over it. Then the way back: the vault's
// own folder is put back in place of the copy, and syncing goes on by itself.
func TestIssue329_ACopyInTheVaultsOwnPlaceKeepsThePeerWhole(t *testing.T) {
	if testing.Short() {
		t.Skip("real peer process; skipped in -short")
	}
	rig, earlier := issue329Rig(t)
	record, found := issue315Record(t, issue315FolderID)
	if !found {
		t.Fatal("the vault has no record before the copy is put in its place")
	}
	issue315StopWithConfigOnDisk(t, rig.configDir, rig.own)
	aside := issue329PutInPlace(t, issue315FolderID, rig.own, earlier)

	if errMsg := StartSyncthing(rig.configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()
	// What the app does first after a start: the path reconcile notes every
	// folder whose directory it can reach. The folder's path is alive, so
	// nothing is re-pointed.
	NoteFolderStorage()
	waitFor(t, "bridge connected to peer after the restart", 60*time.Second, func() bool {
		return bridgeConnected(rig.peer.id)
	})
	rig.expectCopyRefused(t)
	if after, _ := issue315Record(t, issue315FolderID); after != record {
		t.Fatalf("the record followed the copy: %+v, was %+v", after, record)
	}
	// Every scan asks again, and a rescan asked for changes nothing.
	if errMsg := RescanFolder(issue315FolderID); !strings.Contains(errMsg, folderNotItsDirectory) {
		t.Fatalf("RescanFolder over the copy = %q, want the refusal", errMsg)
	}
	rig.expectPeerWhole(t, 10*time.Second)
	// Nor are the vault's filters written into the copy.
	if errMsg := EnsureDefaultIgnores(issue315FolderID, issue371DefaultsJSON(t, issue371Defaults)); errMsg != folderNotItsDirectory {
		t.Fatalf("EnsureDefaultIgnores into the copy = %q, want %q", errMsg, folderNotItsDirectory)
	}

	// The vault's own folder back in its place: no repair, no re-point.
	issue329PutBack(t, rig.own, aside)
	if errMsg := RescanFolder(issue315FolderID); errMsg != "" {
		t.Fatalf("RescanFolder once the vault's own folder is back = %q", errMsg)
	}
	want := issue329Synced()
	rig.waitBridgeHolds(t, rig.own, want)
	rig.writePeer(t, "after.md", "# After\n\nwritten on the peer once the vault was back\n")
	rig.peer.scanAndSettle(t, issue315FolderID)
	want["after.md"] = "# After\n\nwritten on the peer once the vault was back\n"
	rig.waitBridgeHolds(t, rig.own, want)
	if lost, what := rig.peerLost(t); lost {
		t.Fatalf("the peer lost notes after the vault came back: %s", what)
	}
	if after, _ := issue315Record(t, issue315FolderID); after != record {
		t.Fatalf("the record changed without an act: %+v, was %+v", after, record)
	}
}

// The same while the engine runs: the watcher's scan reaches the copy ten
// seconds after it is put in place, before any reconcile.
func TestIssue329_ACopyPutInPlaceWhileTheEngineRunsKeepsThePeerWhole(t *testing.T) {
	if testing.Short() {
		t.Skip("real peer process; skipped in -short")
	}
	rig, earlier := issue329Rig(t)
	defer StopSyncthing()
	record, _ := issue315Record(t, issue315FolderID)
	issue329PutInPlace(t, issue315FolderID, rig.own, earlier)
	rig.expectCopyRefused(t)
	// A return to the app reconciles and rescans.
	NoteFolderStorage()
	if after, _ := issue315Record(t, issue315FolderID); after != record {
		t.Fatalf("the record followed the copy: %+v, was %+v", after, record)
	}
	RescanFolder(issue315FolderID)
	rig.expectPeerWhole(t, 10*time.Second)
}

// NOT COVERED, and pinned here so that it is known: a copy that carries the
// original's marker file itself — `.stfolder` moved over from the vault's own
// folder — has the file number the record names. Nothing tells it from the
// vault's own folder with its notes changed by hand, and no content is read
// to find out (decision 084). The Files app does not show that entry.
func TestIssue329_ACopyWithTheOriginalsMarkerIsTakenForTheVault(t *testing.T) {
	const folderID = "issue329-moved-marker"
	e, own := newIssue315Engine(t, folderID)
	defer StopSyncthing()
	copied := filepath.Join(e.base, "elsewhere", "Vault")
	issue315CopyTree(t, own, copied)
	if err := os.RemoveAll(filepath.Join(copied, config.DefaultMarkerName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(own, config.DefaultMarkerName), filepath.Join(copied, config.DefaultMarkerName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(own, own+"-aside"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(copied, own); err != nil {
		t.Fatal(err)
	}
	folder, _, _ := getFolderConfig(folderID)
	if err := folder.CheckPath(); err != nil {
		t.Fatalf("CheckPath over a copy with the original's marker = %v; if the check now catches this, decision 084 and this test say so", err)
	}
}

// The engine itself asks: fork patch 007. Without it CheckPath answers for
// the marker entry alone, and every test above loses the peer's notes.
func TestIssue329_TheEnginesCheckPathAsksTheRecords(t *testing.T) {
	const folderID = "issue329-checkpath"
	e, own := newIssue315Engine(t, folderID)
	copied := filepath.Join(e.base, "elsewhere", "Vault")
	issue315CopyTree(t, own, copied)
	aside := issue329PutInPlace(t, folderID, own, copied)

	folder, _, _ := getFolderConfig(folderID)
	if err := folder.CheckPath(); !errors.Is(err, errFolderNotItsDirectory) {
		t.Fatalf("the engine's CheckPath over a copy in the folder's place = %v, want %q (is fork patch 007 applied?)", err, folderNotItsDirectory)
	}
	// What the engine's own write of the filters makes of it, past the
	// bridge's check (patch 006 asks CheckPath too), and what the bridge's
	// writes say.
	if got := issue371EngineRefusal(t, folderID); got != folderNotItsDirectory {
		t.Fatalf("the engine's SetIgnores into the copy = %q, want %q", got, folderNotItsDirectory)
	}
	if got := SetFolderIgnores(folderID, `["*.tmp"]`); got != folderNotItsDirectory {
		t.Fatalf("SetFolderIgnores into the copy = %q, want %q", got, folderNotItsDirectory)
	}
	issue371ExpectNoEntry(t, filepath.Join(own, ".stignore"), "a .stignore in the copy")

	// A directory with a `.stfolder` entry but without the folder's marker
	// file in it passes the engine's own look and is not the folder's either.
	if err := os.Remove(folderMarkerPath(folder, own)); err != nil {
		t.Fatal(err)
	}
	if err := folder.CheckPath(); !errors.Is(err, errFolderNotItsDirectory) {
		t.Fatalf("CheckPath over a directory whose .stfolder holds no marker file = %v, want %q", err, folderNotItsDirectory)
	}

	issue329PutBack(t, own, aside)
	if err := folder.CheckPath(); err != nil {
		t.Fatalf("CheckPath once the folder's own directory is back = %v", err)
	}
	if errMsg := StopSyncthing(); errMsg != "" {
		t.Fatalf("StopSyncthing() = %q", errMsg)
	}
	if config.DirectoryCheck.Load() != nil {
		t.Fatal("the check is still installed after the engine stopped")
	}
}

// The record follows a folder only with an act on it. The path reconcile
// writes one where none applies, and never over one that does: that one
// names the directory the folder synced in.
func TestIssue329_TheRecordChangesOnlyWithAnAct(t *testing.T) {
	const folderID = "issue329-record"
	e, own := newIssue315Engine(t, folderID)
	defer StopSyncthing()
	first := e.expectRecord(own)

	copied := filepath.Join(e.base, "elsewhere", "Vault")
	issue315CopyTree(t, own, copied)
	aside := issue329PutInPlace(t, folderID, own, copied)
	NoteFolderStorage()
	if got, _ := issue315Record(t, folderID); got != first {
		t.Fatalf("the reconcile's note wrote the copy's number over the record: %+v, was %+v", got, first)
	}
	// Nor when the record cannot be read.
	database := issue315Store(t)
	failing := issue315FailingStore{kvErr: errors.New("disk I/O error")}
	failing.storageRecords, failing.Closer = database, database
	mu.Lock()
	stDB = failing
	mu.Unlock()
	NoteFolderStorage()
	mu.Lock()
	stDB = database
	mu.Unlock()
	if got, _ := issue315Record(t, folderID); got != first {
		t.Fatalf("the note wrote over a record it could not read: %+v, was %+v", got, first)
	}

	// Where no record applies, the note writes one for the directory there:
	// none, one for another index or another anchor, something that is no
	// record. The marker decides then, as it did before there were records.
	issue329PutBack(t, own, aside)
	for _, variant := range []struct {
		name string
		put  func()
	}{
		{"no record", e.dropRecord},
		{"another index", func() {
			other := first
			other.Index++
			value, _ := json.Marshal(other)
			e.putRecord(value)
		}},
		{"another anchor", func() {
			other := first
			other.Anchor++
			value, _ := json.Marshal(other)
			e.putRecord(value)
		}},
		{"no record at all in its place", func() { e.putRecord([]byte("not a record")) }},
	} {
		variant.put()
		NoteFolderStorage()
		if got := e.expectRecord(own); got != first {
			t.Fatalf("%s: the note wrote %+v, want %+v", variant.name, got, first)
		}
	}
}

// The vault's own directory with its marker file made anew, or gone, is not a
// copy: the directory that holds the marker is the one the record names
// (decision 084). The reconcile writes the marker's new number, so the
// repair, which holds a folder to its marker, still re-points the folder when
// it moves with its container.
func TestIssue329_AMarkerMadeAnewInTheFoldersOwnDirectoryIsNoCopy(t *testing.T) {
	const folderID = "issue329-marker-anew"
	e, own := newIssue315Engine(t, folderID)
	defer StopSyncthing()
	first := e.expectRecord(own)

	issue329MakeMarkerAnew(t, folderID, own)
	if err := issue329CheckPath(t, folderID); err != nil {
		t.Fatalf("CheckPath with the marker made anew in the folder's own .stfolder = %v, want nil", err)
	}
	if errMsg := RescanFolder(folderID); errMsg != "" {
		t.Fatalf("RescanFolder = %q", errMsg)
	}
	NoteFolderStorage()
	noted := e.expectRecord(own)
	if noted.Stfolder != first.Stfolder || noted.Marker == first.Marker {
		t.Fatalf("after the reconcile the record is %+v, want the new marker under the same .stfolder as %+v", noted, first)
	}

	// Moved with its container, the folder is re-pointed by its new marker.
	container := filepath.Join(e.base, "selected-1-after-the-move")
	if err := os.Rename(filepath.Dir(own), container); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(container, "Vault")
	if errMsg := SetFolderPath(folderID, moved); errMsg != "" {
		t.Fatalf("SetFolderPath after the container move = %q, want ''", errMsg)
	}

	// The marker file gone from its own .stfolder: still the folder's own,
	// and nothing is written.
	if err := os.Remove(folderMarkerPath(config.FolderConfiguration{ID: folderID}, moved)); err != nil {
		t.Fatal(err)
	}
	if err := issue329CheckPath(t, folderID); err != nil {
		t.Fatalf("CheckPath with the marker file gone from the folder's own .stfolder = %v, want nil", err)
	}
	if errMsg := RescanFolder(folderID); errMsg != "" {
		t.Fatalf("RescanFolder = %q", errMsg)
	}
	NoteFolderStorage()
	if got, _ := issue315Record(t, folderID); got != noted {
		t.Fatalf("the record changed although no marker was there to read: %+v, was %+v", got, noted)
	}
}

// A check reads; it never writes. The engine asks it before every scan, and
// a marker made anew changes nothing stored until the reconcile writes it.
func TestIssue329_TheEnginesCheckNeverWritesTheRecord(t *testing.T) {
	const folderID = "issue329-reads-only"
	e, own := newIssue315Engine(t, folderID)
	defer StopSyncthing()
	e.expectRecord(own)
	before := issue329RawRecord(t, folderID)

	issue329MakeMarkerAnew(t, folderID, own)
	for i := 0; i < 3; i++ {
		if err := issue329CheckPath(t, folderID); err != nil {
			t.Fatalf("CheckPath = %v", err)
		}
		if errMsg := RescanFolder(folderID); errMsg != "" {
			t.Fatalf("RescanFolder = %q", errMsg)
		}
	}
	if after := issue329RawRecord(t, folderID); string(after) != string(before) {
		t.Fatalf("the check changed the stored record: %s, was %s", after, before)
	}
}

// Another .stfolder refuses whatever marker it holds. A marker file whose
// number was carried into a copy's .stfolder — a hard link to the original's
// — matches the record's marker, and the copy is still not the directory the
// folder synced in.
func TestIssue329_AMarkerCarriedIntoAnotherStfolderIsRefused(t *testing.T) {
	const folderID = "issue329-carried-marker"
	e, own := newIssue315Engine(t, folderID)
	defer StopSyncthing()
	first := e.expectRecord(own)

	copied := filepath.Join(e.base, "elsewhere", "Vault")
	issue315CopyTree(t, own, copied)
	folder := config.FolderConfiguration{ID: folderID}
	if err := os.Remove(folderMarkerPath(folder, copied)); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(folderMarkerPath(folder, own), folderMarkerPath(folder, copied)); err != nil {
		t.Fatal(err)
	}
	if n := issue315MarkerNumber(t, folderID, copied); n != first.Marker {
		t.Fatalf("the linked marker has number %d, the record %d — the test no longer builds the state it is about", n, first.Marker)
	}
	if err := os.Rename(own, own+"-aside"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(copied, own); err != nil {
		t.Fatal(err)
	}
	if err := issue329CheckPath(t, folderID); !errors.Is(err, errFolderNotItsDirectory) {
		t.Fatalf("CheckPath over a copy whose .stfolder holds the original's marker = %v, want %q", err, folderNotItsDirectory)
	}
	NoteFolderStorage()
	if got, _ := issue315Record(t, folderID); got != first {
		t.Fatalf("the record followed the copy: %+v, was %+v", got, first)
	}
}

// A symbolic link in .stfolder's place is never followed. The engine makes
// .stfolder as a directory and never syncs it, so no peer can put a link
// there; a person can. Whatever the link leads to — here the folder's own
// .stfolder, moved beside the vault — the check refuses, and the reconcile
// writes nothing.
func TestIssue329_AStfolderThatIsALinkIsNotTheFoldersDirectory(t *testing.T) {
	const folderID = "issue329-linked-stfolder"
	e, own := newIssue315Engine(t, folderID)
	defer StopSyncthing()
	first := e.expectRecord(own)

	elsewhere := filepath.Join(e.base, "stfolder-elsewhere")
	if err := os.Rename(filepath.Join(own, config.DefaultMarkerName), elsewhere); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(own, config.DefaultMarkerName)); err != nil {
		t.Fatal(err)
	}
	if err := issue329CheckPath(t, folderID); !errors.Is(err, errFolderNotItsDirectory) {
		t.Fatalf("CheckPath with a link in .stfolder's place = %v, want %q", err, folderNotItsDirectory)
	}
	NoteFolderStorage()
	if got, _ := issue315Record(t, folderID); got != first {
		t.Fatalf("the record changed for a link in .stfolder's place: %+v, was %+v", got, first)
	}
}

// The repair holds a record from before the field to its marker, as 3.0.1
// did: a folder whose directory is gone is not re-pointed at a copy.
func TestIssue329_ARecordFromBeforeTheFieldIsHeldToItsMarkerByTheRepair(t *testing.T) {
	const folderID = "issue329-old-record-repair"
	e, own := newIssue315Engine(t, folderID)
	defer StopSyncthing()
	issue329PutRecord301(t, folderID, e.expectRecord(own))
	if record, _ := issue315Record(t, folderID); record.Stfolder != 0 {
		t.Fatalf("the record still carries the field: %+v", record)
	}

	copied := e.copyAndRemove(own)
	if errMsg := SetFolderPath(folderID, copied); errMsg != FolderStorageMismatchError() {
		t.Fatalf("SetFolderPath onto a copy, with a record from before the field = %q, want %q", errMsg, FolderStorageMismatchError())
	}
}

// What the update costs, once. A record from before the field cannot tell a
// copy put in the folder's place before the update from the folder's own
// directory with a marker made anew: the check lets the folder go on, and
// the reconcile records what is there, as 3.0.x recorded it at every
// reconcile. From then on the field is there, and the next copy is refused.
func TestIssue329_ARecordFromBeforeTheFieldOnlyWaitsForTheNextReconcile(t *testing.T) {
	const folderID = "issue329-old-record-live"
	e, own := newIssue315Engine(t, folderID)
	defer StopSyncthing()
	first := e.expectRecord(own)

	copied := filepath.Join(e.base, "elsewhere", "Vault")
	issue315CopyTree(t, own, copied)
	issue329PutInPlace(t, folderID, own, copied)
	issue329PutRecord301(t, folderID, first)
	if err := issue329CheckPath(t, folderID); err != nil {
		t.Fatalf("CheckPath with a record from before the field = %v, want nil: the update must not refuse what it cannot tell", err)
	}
	NoteFolderStorage()
	taken := e.expectRecord(own)
	if taken.Stfolder == 0 || taken.Marker == first.Marker {
		t.Fatalf("the reconcile recorded %+v, want the directory there with its .stfolder", taken)
	}

	another := filepath.Join(e.base, "elsewhere-2", "Vault")
	issue315CopyTree(t, own, another)
	issue329PutInPlace(t, folderID, own, another)
	if err := issue329CheckPath(t, folderID); !errors.Is(err, errFolderNotItsDirectory) {
		t.Fatalf("CheckPath over a copy once the field is written = %v, want %q", err, folderNotItsDirectory)
	}
}

// A version that does not know the field reads the record as it always did
// and drops the field on its next write; the update after it writes the
// field again, never refusing on the way, and two reconciles in a row leave
// the same record.
func TestIssue329_AVersionThatDoesNotKnowTheFieldReadsAndDropsIt(t *testing.T) {
	const folderID = "issue329-field"
	e, own := newIssue315Engine(t, folderID)
	defer StopSyncthing()
	full := e.expectRecord(own)
	if full.Stfolder == 0 {
		t.Fatalf("the record written at the add has no .stfolder number: %+v", full)
	}

	var old issue329Record301
	if err := json.Unmarshal(issue329RawRecord(t, folderID), &old); err != nil {
		t.Fatalf("3.0.1's record type cannot read the record: %v", err)
	}
	if old != (issue329Record301{Marker: full.Marker, Index: full.Index, Anchor: full.Anchor}) {
		t.Fatalf("3.0.1's record type reads %+v from %+v", old, full)
	}

	issue329PutRecord301(t, folderID, full)
	if err := issue329CheckPath(t, folderID); err != nil {
		t.Fatalf("CheckPath after a version without the field wrote the record = %v, want nil", err)
	}
	NoteFolderStorage()
	if got := e.expectRecord(own); got != full {
		t.Fatalf("the reconcile wrote %+v, want %+v", got, full)
	}
	once := issue329RawRecord(t, folderID)
	NoteFolderStorage()
	if twice := issue329RawRecord(t, folderID); string(twice) != string(once) {
		t.Fatalf("a second reconcile changed the record: %s, was %s", twice, once)
	}
}

// The first reconcile after the update writes every record from 3.0.x in
// this version's shape, whatever the folder's state, and a second reconcile
// changes nothing. The folder there — with the recorded marker, with another
// one, or without one — is never refused on the way.
func TestIssue329_ARecordFromBeforeTheFieldTakesThisShapeAtTheFirstReconcile(t *testing.T) {
	const folderID = "issue329-convert-there"
	e, own := newIssue315Engine(t, folderID)
	defer StopSyncthing()
	full := e.expectRecord(own)
	twice := func(step string) folderStorageRecord {
		t.Helper()
		if err := issue329CheckPath(t, folderID); err != nil {
			t.Fatalf("%s: CheckPath before the reconcile = %v, want nil", step, err)
		}
		NoteFolderStorage()
		once := issue329RawRecord(t, folderID)
		NoteFolderStorage()
		if again := issue329RawRecord(t, folderID); string(again) != string(once) {
			t.Fatalf("%s: a second reconcile changed the record: %s, was %s", step, again, once)
		}
		if err := issue329CheckPath(t, folderID); err != nil {
			t.Fatalf("%s: CheckPath after the reconcile = %v, want nil", step, err)
		}
		record, _ := issue315Record(t, folderID)
		return record
	}

	// Its directory with the recorded marker: .stfolder's number is added.
	issue329PutRecord301(t, folderID, full)
	if got := twice("the recorded marker"); got != full {
		t.Fatalf("the reconcile wrote %+v, want %+v", got, full)
	}

	// Its directory with another marker: what is there is recorded, once.
	issue329PutRecord301(t, folderID, full)
	issue329MakeMarkerAnew(t, folderID, own)
	anew := twice("another marker")
	if anew != e.expectRecord(own) || anew.Stfolder != full.Stfolder || anew.Marker == full.Marker {
		t.Fatalf("the reconcile wrote %+v, want the marker there under %+v's .stfolder", anew, full)
	}

	// Its directory without a marker file: .stfolder's number is recorded
	// with the marker the record had.
	issue329PutRecord301(t, folderID, anew)
	if err := os.Remove(folderMarkerPath(config.FolderConfiguration{ID: folderID}, own)); err != nil {
		t.Fatal(err)
	}
	if got := twice("no marker file"); got != anew {
		t.Fatalf("the reconcile wrote %+v, want %+v", got, anew)
	}
}

// A record from 3.0.x whose folder is not there at the first reconcile after
// the update — moved in Files and not reconnected yet — takes this version's
// shape without `.stfolder`'s number, and the folder is held to its marker
// from then on: a copy that lands at its path later is refused, and only its
// own directory, with the recorded marker, passes and completes the record.
// A version that does not know the fields reads the record as one it knows,
// and after it the same record is written again.
func TestIssue329_ARecordOfAFolderThatIsNotThereIsHeldToItsMarker(t *testing.T) {
	const folderID = "issue329-convert-gone"
	e, own := newIssue315Engine(t, folderID)
	defer StopSyncthing()
	full := e.expectRecord(own)
	issue329PutRecord301(t, folderID, full)
	away := filepath.Join(e.base, "moved-away", "Vault")
	if err := os.MkdirAll(filepath.Dir(away), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(own, away); err != nil {
		t.Fatal(err)
	}

	NoteFolderStorage()
	held := issue329RawRecord(t, folderID)
	NoteFolderStorage()
	if again := issue329RawRecord(t, folderID); string(again) != string(held) {
		t.Fatalf("a second reconcile changed the record: %s, was %s", again, held)
	}
	want := folderStorageRecord{Marker: full.Marker, Index: full.Index, Anchor: full.Anchor, Strict: true}
	if got, _ := issue315Record(t, folderID); got != want {
		t.Fatalf("the reconcile wrote %+v, want %+v", got, want)
	}

	// VaultSync 3.0.1 decodes with json.Unmarshal into a type without these
	// fields (v3.0.1, readFolderStorage): unknown fields are skipped, and an
	// empty `.stfolder` number is not written at all.
	var old issue329Record301
	if err := json.Unmarshal(held, &old); err != nil {
		t.Fatalf("3.0.1's record type cannot read the record: %v", err)
	}
	if old != (issue329Record301{Marker: full.Marker, Index: full.Index, Anchor: full.Anchor}) {
		t.Fatalf("3.0.1's record type reads %+v from %s", old, held)
	}
	if strings.Contains(string(held), "stfolder") {
		t.Fatalf("the record names an empty .stfolder number: %s", held)
	}
	issue329PutRecord301(t, folderID, full)
	NoteFolderStorage()
	if again := issue329RawRecord(t, folderID); string(again) != string(held) {
		t.Fatalf("after a version without the fields wrote it, the reconcile wrote %s, want %s", again, held)
	}

	// A copy lands at the folder's path: refused, and nothing is recorded.
	copied := filepath.Join(e.base, "elsewhere", "Vault")
	issue315CopyTree(t, away, copied)
	if err := os.Rename(copied, own); err != nil {
		t.Fatal(err)
	}
	if err := issue329CheckPath(t, folderID); !errors.Is(err, errFolderNotItsDirectory) {
		t.Fatalf("CheckPath over a copy at the path of a folder held to its marker = %v, want %q", err, folderNotItsDirectory)
	}
	NoteFolderStorage()
	if again := issue329RawRecord(t, folderID); string(again) != string(held) {
		t.Fatalf("the reconcile wrote the copy down: %s, was %s", again, held)
	}

	// The folder's own directory back in its place: it passes, and the
	// reconcile completes the record.
	if err := os.Rename(own, filepath.Join(e.base, "copy-moved-out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(away, own); err != nil {
		t.Fatal(err)
	}
	if err := issue329CheckPath(t, folderID); err != nil {
		t.Fatalf("CheckPath once the folder's own directory is back = %v, want nil", err)
	}
	NoteFolderStorage()
	if got := e.expectRecord(own); got != full {
		t.Fatalf("the reconcile completed the record as %+v, want %+v", got, full)
	}
}

// A record from 3.0.x could only let the folder through, so no read the
// check would need for another record stops it: not the anchor's, not the
// index ID's. "The update never refuses a vault" holds for a read that fails.
func TestIssue329_AFailedReadNeverStopsAVaultWhoseRecordComesFromBeforeTheField(t *testing.T) {
	const folderID = "issue329-old-record-unread"
	e, own := newIssue315Engine(t, folderID)
	defer StopSyncthing()
	issue329PutRecord301(t, folderID, e.expectRecord(own))
	folder, _, _ := getFolderConfig(folderID)
	if err := issue329Check(issue315Store(t), 0, errors.New("disk I/O error"), nil).answer(folder); err != nil {
		t.Fatalf("the check with a failed anchor read and an unknown index ID = %v, want nil", err)
	}

	// Through the engine: a start whose anchor cannot be read.
	issue315StopWithConfigOnDisk(t, e.base, own)
	realPeek := peekStorageAnchor
	failing := func(string) (uint64, error) { return 0, errors.New("disk I/O error") }
	readStorageAnchor, peekStorageAnchor = failing, failing
	defer func() { readStorageAnchor, peekStorageAnchor = ensureStorageAnchor, realPeek }()
	if errMsg := StartSyncthing(e.base); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()
	waitFor(t, "the folder to sync on while the anchor cannot be read", 30*time.Second, func() bool {
		RescanFolder(folderID)
		st := issue329Status(t, folderID)
		return st.State == "idle" && st.ErrorReason == ""
	})
}

// issue329WatchingStore is the engine's database with every call that can
// write counted: PutKV, and GetIndexID, which makes an index ID where there
// is none.
type issue329WatchingStore struct {
	inner      storageRecords
	writes     int
	indexReads int
}

func (s *issue329WatchingStore) GetKV(key string) ([]byte, error) { return s.inner.GetKV(key) }

func (s *issue329WatchingStore) PutKV(key string, val []byte) error {
	s.writes++
	return s.inner.PutKV(key, val)
}

func (s *issue329WatchingStore) GetIndexID(folder string, device protocol.DeviceID) (protocol.IndexID, error) {
	s.indexReads++
	return s.inner.GetIndexID(folder, device)
}

// issue329Disk lists what a check could change on disk: every entry of dir
// with its file number, and the anchor's file number or its absence.
func issue329Disk(t *testing.T, dir, configDir string) string {
	t.Helper()
	var listing strings.Builder
	err := filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		number, err := fileNumber(path)
		fmt.Fprintf(&listing, "%s %d %v\n", path, number, err)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	number, err := fileNumber(filepath.Join(configDir, storageAnchorName))
	fmt.Fprintf(&listing, "anchor %d %v\n", number, err)
	return listing.String()
}

// A check reads, and writes nothing at all: not the record, not an index ID,
// not the anchor, nothing in the folder. Where it cannot decide without a
// write, it decides without: an index ID it does not know means no record
// applies, and an anchor that is gone means no record applies, as at the
// next start, which makes a new one.
func TestIssue329_TheEnginesCheckWritesNothing(t *testing.T) {
	const folderID = "issue329-writes-nothing"
	e, own := newIssue315Engine(t, folderID)
	defer StopSyncthing()
	full := e.expectRecord(own)
	folder, _, _ := getFolderConfig(folderID)
	watching := &issue329WatchingStore{inner: issue315Store(t)}
	known := map[string]uint64{folderID: full.Index}
	anchorFile := filepath.Join(e.base, storageAnchorName)

	ask := func(state string, check folderDirectoryCheck) {
		t.Helper()
		disk, record := issue329Disk(t, own, e.base), issue329RawRecord(t, folderID)
		if err := check.answer(folder); err != nil {
			t.Fatalf("%s: the check = %v, want nil", state, err)
		}
		if after := issue329Disk(t, own, e.base); after != disk {
			t.Fatalf("%s: the check changed the disk:\n%s\nwas\n%s", state, after, disk)
		}
		if after := issue329RawRecord(t, folderID); string(after) != string(record) {
			t.Fatalf("%s: the check changed the record: %s, was %s", state, after, record)
		}
		if watching.writes != 0 || watching.indexReads != 0 {
			t.Fatalf("%s: the check wrote %d times and asked the engine for an index ID %d times", state, watching.writes, watching.indexReads)
		}
	}

	issue329MakeMarkerAnew(t, folderID, own)
	ask("a marker made anew", issue329Check(watching, full.Anchor, nil, known))
	ask("an index ID the check does not know", issue329Check(watching, full.Anchor, nil, nil))

	// The start's read of the anchor failed, and the file is gone now.
	if err := os.Rename(anchorFile, anchorFile+".kept"); err != nil {
		t.Fatal(err)
	}
	ask("an anchor that is gone", folderDirectoryCheck{
		store:   watching,
		anchor:  &anchorReading{read: func() (uint64, error) { return peekStorageAnchor(e.base) }, err: errors.New("disk I/O error")},
		indexes: &indexSnapshot{ids: known},
	})
	if err := os.Rename(anchorFile+".kept", anchorFile); err != nil {
		t.Fatal(err)
	}

	issue329PutRecord301(t, folderID, full)
	ask("a record from before the field", issue329Check(watching, full.Anchor, nil, known))
}

// A read that fails refuses (decision 039), and is no finding about the
// directory: its reason is its own, nobody is told the folder is a copy, and
// the next check asks again.
func TestIssue329_AReadThatFailsIsNoCopy(t *testing.T) {
	const folderID = "issue329-unread"
	e, own := newIssue315Engine(t, folderID)
	defer StopSyncthing()
	e.expectRecord(own)
	folder, _, _ := getFolderConfig(folderID)
	database := issue315Store(t)
	mu.Lock()
	anchor := stStorageAnchor
	mu.Unlock()
	indexes := map[string]uint64{folderID: issue329Index(t, folderID)}
	failingKV := issue315FailingStore{kvErr: errors.New("disk I/O error")}
	failingKV.storageRecords, failingKV.Closer = database, database

	expectUnchecked := func(what string, err error) {
		t.Helper()
		if err == nil || errors.Is(err, errFolderNotItsDirectory) || !strings.HasPrefix(err.Error(), folderDirectoryUnchecked) {
			t.Fatalf("%s: the check answered %v, want a refusal of its own that begins %q", what, err, folderDirectoryUnchecked)
		}
		if got := classifyFolderErrorReason(err.Error()); got != issue329Unchecked {
			t.Fatalf("%s: the app is given the reason %q, want %q", what, got, issue329Unchecked)
		}
		issue329ExpectCleanText(t, err.Error(), e.base)
	}
	expectUnchecked("the record", issue329Check(failingKV, anchor, nil, indexes).answer(folder))
	expectUnchecked("the anchor", issue329Check(database, 0, errors.New("disk I/O error"), indexes).answer(folder))
	stfolder := filepath.Join(own, config.DefaultMarkerName)
	if err := os.Chmod(stfolder, 0); err != nil {
		t.Fatal(err)
	}
	err := issue329Check(database, anchor, nil, indexes).answer(folder)
	if chmodErr := os.Chmod(stfolder, 0o755); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	expectUnchecked("the marker's directory", err)
	if err := issue329Check(database, anchor, nil, indexes).answer(folder); err != nil {
		t.Fatalf("the check once everything reads = %v, want nil", err)
	}
	// An index ID the check does not know is one it cannot read without
	// making it: the record does not apply, as far as the check can tell.
	if err := issue329Check(database, anchor, nil, nil).answer(folder); err != nil {
		t.Fatalf("the check without the folder's index ID = %v, want nil", err)
	}

	// Through the engine: the folder shows the read's reason, and once the
	// read works, the next check lets the folder go on.
	swap := func(store storageRecords) {
		mu.Lock()
		defer mu.Unlock()
		installed, indexes := installDirectoryCheckLocked(store, e.base, anchor, nil)
		removeDirectoryCheck(stDirectoryCheck)
		stDirectoryCheck, stIndexes = installed, indexes
	}
	swap(failingKV)
	RescanFolder(folderID)
	waitFor(t, "the folder to show the failed read", 30*time.Second, func() bool {
		return issue329Status(t, folderID).ErrorReason == issue329Unchecked
	})
	swap(database)
	waitFor(t, "the folder to go on once the read works", 30*time.Second, func() bool {
		RescanFolder(folderID)
		st := issue329Status(t, folderID)
		return st.State == "idle" && st.ErrorReason == ""
	})
}

// issue329Check is the engine's check over store, with an anchor that reads
// as given every time and the local index IDs given.
func issue329Check(store recordReader, anchor uint64, anchorErr error, indexes map[string]uint64) folderDirectoryCheck {
	snapshot := &indexSnapshot{ids: make(map[string]uint64)}
	for id, index := range indexes {
		snapshot.ids[id] = index
	}
	return folderDirectoryCheck{
		store: store,
		anchor: &anchorReading{
			read:   func() (uint64, error) { return anchor, anchorErr },
			number: anchor,
			err:    anchorErr,
		},
		indexes: snapshot,
	}
}

// issue329Index returns a folder's local index ID from the engine, which
// makes one where there is none; the tests ask it for folders that have one.
func issue329Index(t *testing.T, folderID string) uint64 {
	t.Helper()
	index, err := issue315Store(t).GetIndexID(folderID, protocol.LocalDeviceID)
	if err != nil {
		t.Fatalf("GetIndexID: %v", err)
	}
	return uint64(index)
}

// An anchor the start could not read stops no folder for the rest of the
// engine run: the check reads it again while a read has failed, and the
// folder goes on once it reads (decision 084).
func TestIssue329_AnAnchorThatCouldNotBeReadIsReadAgain(t *testing.T) {
	const folderID = "issue329-anchor"
	e, own := newIssue315Engine(t, folderID)
	e.expectRecord(own)
	issue315StopWithConfigOnDisk(t, e.base, own)

	var failing atomic.Bool
	failing.Store(true)
	fail := func(dir string) error {
		return &os.PathError{Op: "lstat", Path: filepath.Join(dir, storageAnchorName), Err: syscall.EACCES}
	}
	realPeek := peekStorageAnchor
	readStorageAnchor = func(dir string) (uint64, error) {
		if failing.Load() {
			return 0, fail(dir)
		}
		return ensureStorageAnchor(dir)
	}
	peekStorageAnchor = func(dir string) (uint64, error) {
		if failing.Load() {
			return 0, fail(dir)
		}
		return realPeek(dir)
	}
	defer func() { readStorageAnchor, peekStorageAnchor = ensureStorageAnchor, realPeek }()
	if errMsg := StartSyncthing(e.base); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	waitFor(t, "the folder to show that its check could not read the anchor", 30*time.Second, func() bool {
		RescanFolder(folderID)
		return issue329Status(t, folderID).ErrorReason == issue329Unchecked
	})
	issue329ExpectCleanText(t, issue329Status(t, folderID).ErrorMessage, e.base)

	// The anchor reads again; no restart.
	failing.Store(false)
	waitFor(t, "the folder to go on once the anchor reads", 30*time.Second, func() bool {
		RescanFolder(folderID)
		st := issue329Status(t, folderID)
		return st.State == "idle" && st.ErrorReason == ""
	})
}

// The stop closes the engine's database before it takes the check away. A
// check over a database that has closed refuses as a read that failed, so an
// engine service still running after the stop's timeout meets a refusal,
// never no check at all.
func TestIssue329_ACheckOverAClosedDatabaseRefuses(t *testing.T) {
	const folderID = "issue329-closed"
	e, own := newIssue315Engine(t, folderID)
	e.expectRecord(own)
	folder, _, _ := getFolderConfig(folderID)
	store := issue315Store(t)
	mu.Lock()
	anchor := stStorageAnchor
	mu.Unlock()
	indexes := map[string]uint64{folderID: issue329Index(t, folderID)}
	if errMsg := StopSyncthing(); errMsg != "" {
		t.Fatalf("StopSyncthing() = %q", errMsg)
	}
	err := issue329Check(store, anchor, nil, indexes).answer(folder)
	if err == nil || !strings.HasPrefix(err.Error(), folderDirectoryUnchecked) {
		t.Fatalf("the check over a closed database = %v, want a refusal that begins %q", err, folderDirectoryUnchecked)
	}
}

// issue329FakeStore is the engine's database as far as the records ask it,
// with reads that fail as told.
type issue329FakeStore struct {
	record          []byte
	index           protocol.IndexID
	kvErr, indexErr error
}

func (s issue329FakeStore) GetKV(string) ([]byte, error) {
	if s.kvErr != nil {
		return nil, s.kvErr
	}
	return s.record, nil
}

func (s issue329FakeStore) PutKV(string, []byte) error { return nil }

func (s issue329FakeStore) GetIndexID(string, protocol.DeviceID) (protocol.IndexID, error) {
	return s.index, s.indexErr
}

func (s issue329FakeStore) Close() error { return nil }

// No refusal of this file carries a path (safety rules 6 and 10): each error
// a read can fail with here names a path, and none of it may reach a text
// the folder's error, a re-point or a write of the filters shows.
func TestIssue329_NoRefusalCarriesAPath(t *testing.T) {
	const anchor, index = 7, 11
	dir := t.TempDir()
	pathErr := func(name string) error {
		return &os.PathError{Op: "lstat", Path: filepath.Join(dir, name), Err: syscall.EACCES}
	}
	value, err := json.Marshal(folderStorageRecord{Marker: 1, Index: index, Anchor: anchor, Stfolder: 2, Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	folder := config.FolderConfiguration{ID: "issue329-texts", Path: filepath.Join(dir, "Vault")}
	// A marker whose number cannot be read: its directory has no search
	// permission.
	if err := os.MkdirAll(filepath.Join(folder.Path, config.DefaultMarkerName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(folderMarkerPath(folder, folder.Path), []byte("marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(folder.Path, config.DefaultMarkerName), 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Join(folder.Path, config.DefaultMarkerName), 0o700)

	good := issue329FakeStore{record: value, index: index}
	cases := []struct {
		name      string
		store     issue329FakeStore
		anchorErr error
	}{
		{"the record", issue329FakeStore{kvErr: pathErr("index-v2")}, nil},
		{"the anchor", good, pathErr(storageAnchorName)},
		{"the index ID", issue329FakeStore{record: value, indexErr: pathErr("index-v2")}, nil},
		{"the marker and its directory", good, nil},
	}
	var texts []string
	for _, c := range cases {
		verdict, unread := folderStorageVerdict(c.store, anchor, c.anchorErr, folder, folder.Path)
		if verdict != storageUnread {
			t.Fatalf("%s: verdict %d, want a read that failed", c.name, verdict)
		}
		texts = append(texts, unread, issue329Check(c.store, anchor, c.anchorErr, map[string]uint64{folder.ID: index}).answer(folder).Error())

		mu.Lock()
		savedDB, savedAnchor, savedErr := stDB, stStorageAnchor, stStorageAnchorErr
		stDB, stStorageAnchor, stStorageAnchorErr = c.store, anchor, c.anchorErr
		refusal := folderStorageRefusalLocked(folder, folder.Path)
		stDB, stStorageAnchor, stStorageAnchorErr = savedDB, savedAnchor, savedErr
		mu.Unlock()
		texts = append(texts, refusal)
	}
	texts = append(texts,
		folderStorageMismatch,
		errFolderNotItsDirectory.Error(),
		directoryRefusal(errFolderNotItsDirectory),
		ignoresWriteError(issue329Check(cases[1].store, anchor, cases[1].anchorErr, map[string]uint64{folder.ID: index}).answer(folder)),
	)
	for _, text := range texts {
		if text == "" {
			t.Fatal("a refusal came out empty: the test no longer reaches every text")
		}
		if strings.Contains(text, "/") {
			t.Errorf("the refusal %q carries a path", text)
		}
	}
}

// The false alarm the check must never raise: the vault's own folder,
// through what an install does with it — pulls from a peer, notes written on
// this iPhone, filter writes, the reconcile's note, stops and starts, its
// marker file made anew, a container move with the re-point that follows,
// and its marker file gone. The check is asked all along and never refuses,
// and the record changes only where the reconcile writes the marker's new
// number.
func TestIssue329_TheVaultsOwnFolderIsNeverRefused(t *testing.T) {
	if testing.Short() {
		t.Skip("real peer process; skipped in -short")
	}
	answers := observeIssue329(t)
	rig := newIssue315Rig(t)
	defer StopSyncthing()
	record, found := issue315Record(t, issue315FolderID)
	if !found {
		t.Fatal("the vault has no record after the accept")
	}
	dir := rig.own
	want := map[string]string{"kept.md": issue329Kept, "edited.md": issue315NoteV1}
	expectOwn := func(step string) {
		t.Helper()
		if got, _ := issue315Record(t, issue315FolderID); got != record {
			t.Fatalf("%s: the record changed without an act: %+v, was %+v", step, got, record)
		}
		if st := issue329Status(t, issue315FolderID); st.ErrorReason != "" {
			t.Fatalf("%s: the vault's own folder is in error: %q (%s)", step, st.ErrorReason, st.ErrorMessage)
		}
		answers.expectNoRefusal(t, step)
	}
	restart := func(step string) {
		t.Helper()
		issue315StopWithConfigOnDisk(t, rig.configDir, issue315FolderID)
		if errMsg := StartSyncthing(rig.configDir); errMsg != "" {
			t.Fatalf("%s: StartSyncthing() failed: %s", step, errMsg)
		}
		NoteFolderStorage()
		waitFor(t, step+": bridge connected to peer", 60*time.Second, func() bool {
			return bridgeConnected(rig.peer.id)
		})
	}

	for round := 1; round <= 3; round++ {
		step := fmt.Sprintf("round %d", round)
		fromPeer := fmt.Sprintf("peer-%d.md", round)
		want[fromPeer] = fmt.Sprintf("# From the peer\n\nround %d\n", round)
		rig.writePeer(t, fromPeer, want[fromPeer])
		rig.peer.scanAndSettle(t, issue315FolderID)
		rig.waitBridgeHolds(t, dir, want)

		fromPhone := fmt.Sprintf("phone-%d.md", round)
		want[fromPhone] = fmt.Sprintf("# From this iPhone\n\nround %d\n", round)
		if err := os.WriteFile(filepath.Join(dir, fromPhone), []byte(want[fromPhone]), 0o600); err != nil {
			t.Fatal(err)
		}
		if errMsg := RescanFolder(issue315FolderID); errMsg != "" {
			t.Fatalf("%s: RescanFolder = %q", step, errMsg)
		}
		waitFor(t, step+": the iPhone's note on the peer", 60*time.Second, func() bool {
			got, ok := rig.peerFile(t, fromPhone)
			return ok && got == want[fromPhone]
		})
		if errMsg := EnsureDefaultIgnores(issue315FolderID, issue371DefaultsJSON(t, issue371Defaults)); errMsg != "" {
			t.Fatalf("%s: EnsureDefaultIgnores = %q", step, errMsg)
		}
		NoteFolderStorage()
		rig.waitBridgeHolds(t, dir, want)
		expectOwn(step)

		restart(step + ", after a restart")
		rig.waitBridgeHolds(t, dir, want)
		expectOwn(step + ", after a restart")
	}

	// The marker file made anew in the folder's own .stfolder: the check lets
	// the folder go on, and the reconcile writes the marker's new number
	// under the same .stfolder.
	issue329MakeMarkerAnew(t, issue315FolderID, dir)
	if errMsg := RescanFolder(issue315FolderID); errMsg != "" {
		t.Fatalf("RescanFolder after the marker was made anew = %q", errMsg)
	}
	NoteFolderStorage()
	renoted, _ := issue315Record(t, issue315FolderID)
	if renoted.Stfolder != record.Stfolder || renoted.Marker == record.Marker {
		t.Fatalf("after the marker was made anew the record is %+v, want the new marker under the same .stfolder as %+v", renoted, record)
	}
	record = renoted
	expectOwn("the marker made anew")
	restart("the marker made anew, after a restart")
	rig.waitBridgeHolds(t, dir, want)
	expectOwn("the marker made anew, after a restart")

	// A container move while VaultSync is closed (#25): the folder's parent
	// gets another name, and the reconcile re-points the folder there.
	issue315StopWithConfigOnDisk(t, rig.configDir, issue315FolderID)
	container := filepath.Join(rig.configDir, "selected-1-after-the-move")
	if err := os.Rename(filepath.Dir(dir), container); err != nil {
		t.Fatal(err)
	}
	if errMsg := StartSyncthing(rig.configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() after the move failed: %s", errMsg)
	}
	NoteFolderStorage()
	dir = filepath.Join(container, "Vault")
	if errMsg := SetFolderPath(issue315FolderID, dir); errMsg != "" {
		t.Fatalf("SetFolderPath after the container move = %q", errMsg)
	}
	waitFor(t, "bridge connected to peer after the move", 60*time.Second, func() bool {
		return bridgeConnected(rig.peer.id)
	})
	want["after-the-move.md"] = "# After the move\n\nwritten on the peer\n"
	rig.writePeer(t, "after-the-move.md", want["after-the-move.md"])
	rig.peer.scanAndSettle(t, issue315FolderID)
	rig.waitBridgeHolds(t, dir, want)
	NoteFolderStorage()
	expectOwn("after the container move and the re-point")

	// The marker file gone from the folder's own .stfolder: nothing makes it
	// again, the folder goes on, and nothing is written.
	if err := os.Remove(folderMarkerPath(config.FolderConfiguration{ID: issue315FolderID}, dir)); err != nil {
		t.Fatal(err)
	}
	restart("the marker file gone, after a restart")
	want["marker-gone.md"] = "# Marker gone\n\nwritten on the peer\n"
	rig.writePeer(t, "marker-gone.md", want["marker-gone.md"])
	rig.peer.scanAndSettle(t, issue315FolderID)
	rig.waitBridgeHolds(t, dir, want)
	expectOwn("the marker file gone, after a restart")

	if asked := len(answers.asked(issue315FolderID)); asked == 0 {
		t.Fatal("the engine never asked the check: the test proves nothing")
	} else {
		t.Logf("the check was asked %d times for the vault, and never refused", asked)
	}
}

// What the check costs. CheckPath runs before every scan, every pull and
// every commit of scan results; the answer reads the record and the index ID
// from the engine's database and the marker's file number from the disk.
func TestIssue329_WhatTheCheckCosts(t *testing.T) {
	if testing.Short() {
		t.Skip("writes a few thousand files")
	}
	const folderID, files = "issue329-cost", 3000
	answers := observeIssue329(t)
	_, own := newIssue315Engine(t, folderID)
	defer StopSyncthing()
	notes := filepath.Join(own, "notes")
	if err := os.MkdirAll(notes, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < files; i++ {
		text := fmt.Sprintf("# Note %d\n\n%s\n", i, strings.Repeat("a line of a note. ", 10))
		if err := os.WriteFile(filepath.Join(notes, fmt.Sprintf("note-%04d.md", i)), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	scan := func(what string, wantFiles int) {
		t.Helper()
		before := len(answers.asked(folderID))
		began := time.Now()
		if errMsg := RescanFolder(folderID); errMsg != "" {
			t.Fatalf("RescanFolder = %q", errMsg)
		}
		took := time.Since(began)
		during := answers.asked(folderID)[before:]
		if st := issue329Status(t, folderID); st.LocalFiles != wantFiles {
			t.Fatalf("%s: the index holds %d files after the scan, want %d", what, st.LocalFiles, wantFiles)
		}
		slices.Sort(during)
		var total time.Duration
		for _, d := range during {
			total += d
		}
		median, slowest := time.Duration(0), time.Duration(0)
		if len(during) > 0 {
			median, slowest = during[len(during)/2], during[len(during)-1]
		}
		t.Logf("%s: the scan took %v; the check was asked %d times, %v in all, median %v, slowest %v",
			what, took, len(during), total, median, slowest)
		if len(during) > wantFiles/100+5 {
			t.Errorf("%s: the check was asked %d times for %d files — once per file, not once per commit", what, len(during), wantFiles)
		}
	}
	scan(fmt.Sprintf("one scan of %d new files", files), files+1)
	scan("a scan that finds nothing new", files+1)

	// One answer outside the engine, and the engine's whole CheckPath — its
	// own two looks at the disk and the answer — for comparison.
	folder, _, _ := getFolderConfig(folderID)
	indexes := map[string]uint64{folderID: issue329Index(t, folderID)}
	mu.Lock()
	check := issue329Check(stDB, stStorageAnchor, nil, indexes)
	mu.Unlock()
	const rounds = 1000
	began := time.Now()
	for i := 0; i < rounds; i++ {
		if err := check.answer(folder); err != nil {
			t.Fatalf("the check refused the folder's own directory: %v", err)
		}
	}
	perAnswer := time.Since(began) / rounds
	began = time.Now()
	for i := 0; i < rounds; i++ {
		if err := folder.CheckPath(); err != nil {
			t.Fatalf("CheckPath refused the folder's own directory: %v", err)
		}
	}
	perCheckPath := time.Since(began) / rounds
	t.Logf("one answer: %v on average over %d; one CheckPath with the answer: %v", perAnswer, rounds, perCheckPath)
}
