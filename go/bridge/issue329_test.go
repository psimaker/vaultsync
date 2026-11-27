package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/syncthing/syncthing/lib/config"
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
	failingKV := issue315FailingStore{kvErr: errors.New("disk I/O error")}
	failingKV.storageRecords, failingKV.Closer = database, database
	failingIndex := issue315FailingStore{indexErr: errors.New("disk I/O error")}
	failingIndex.storageRecords, failingIndex.Closer = database, database

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
	expectUnchecked("the record", folderDirectoryCheck{store: failingKV, anchor: anchor}.answer(folder))
	expectUnchecked("the index ID", folderDirectoryCheck{store: failingIndex, anchor: anchor}.answer(folder))
	expectUnchecked("the anchor", folderDirectoryCheck{store: database, anchorErr: errors.New("disk I/O error")}.answer(folder))
	stfolder := filepath.Join(own, config.DefaultMarkerName)
	if err := os.Chmod(stfolder, 0); err != nil {
		t.Fatal(err)
	}
	err := folderDirectoryCheck{store: database, anchor: anchor}.answer(folder)
	if chmodErr := os.Chmod(stfolder, 0o755); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	expectUnchecked("the marker's file number", err)
	if err := (folderDirectoryCheck{store: database, anchor: anchor}).answer(folder); err != nil {
		t.Fatalf("the check once everything reads = %v, want nil", err)
	}

	// Through the engine: the folder shows the read's reason, and once the
	// read works, the next check lets the folder go on.
	swap := func(store storageRecords) {
		mu.Lock()
		defer mu.Unlock()
		installed := installDirectoryCheckLocked(store, anchor, nil)
		removeDirectoryCheck(stDirectoryCheck)
		stDirectoryCheck = installed
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

// The false alarm the check must never raise: the vault's own folder,
// through what an install does with it — pulls from a peer, notes written on
// this iPhone, filter writes, the reconcile's note, stops and starts, and a
// container move with the re-point that follows. The check is asked all
// along and never refuses, and the record never changes.
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
	mu.Lock()
	check := folderDirectoryCheck{store: stDB, anchor: stStorageAnchor}
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
