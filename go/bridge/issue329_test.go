package bridge

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/syncthing/syncthing/lib/config"
)

// A copy put in a vault's own place, under the vault's own path (#329). In
// the Files app the vault's folder is deleted and an earlier duplicate is
// renamed to its name. The folder's path is alive and holds a `.stfolder`, so
// no path repair runs: the engine scans the copy as the folder, changed by
// hand, and sends what the copy lacks out as deletions and its older notes
// as the current versions.

const (
	issue329Kept  = "# Kept\n\nnever touched again\n"
	issue329Later = "# Later\n\nwritten after the copy was made\n"
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
	rig.waitBridgeHolds(t, rig.own, map[string]string{
		"kept.md": issue329Kept, "edited.md": issue315NoteV2, "later.md": issue329Later,
	})
	return rig, earlier
}

// putInPlace deletes the vault's own folder and renames the copy to its name.
func (r *issue315Rig) putInPlace(t *testing.T, earlier string) {
	t.Helper()
	if err := os.RemoveAll(r.own); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(earlier, r.own); err != nil {
		t.Fatal(err)
	}
}

// expectPeerWhole fails if the peer loses what synced after the copy was
// made, within the time a scan of the folder takes to reach it.
func (r *issue315Rig) expectPeerWhole(t *testing.T) {
	t.Helper()
	lost := waitForCondition(30*time.Second, func() bool {
		edited, _ := r.peerFile(t, "edited.md")
		_, hasLater := r.peerFile(t, "later.md")
		return edited != issue315NoteV2 || !hasLater
	})
	if lost {
		edited, _ := r.peerFile(t, "edited.md")
		_, hasLater := r.peerFile(t, "later.md")
		t.Fatalf("the peer lost what had synced after the copy was made: edited.md holds the second version = %v (the first = %v), later.md exists = %v",
			edited == issue315NoteV2, edited == issue315NoteV1, hasLater)
	}
}

// The reported case: the copy is put in place while VaultSync is closed, and
// the next launch starts the engine over it.
func TestIssue329_ACopyInTheVaultsOwnPlaceKeepsThePeerWhole(t *testing.T) {
	if testing.Short() {
		t.Skip("real peer process; skipped in -short")
	}
	rig, earlier := issue329Rig(t)
	before := issue315MarkerNumber(t, issue315FolderID, rig.own)
	issue315StopWithConfigOnDisk(t, rig.configDir, rig.own)
	rig.putInPlace(t, earlier)
	if after := issue315MarkerNumber(t, issue315FolderID, rig.own); after == before {
		t.Fatalf("the copy's marker has the original's file number (%d) — the test no longer builds the reported state", after)
	}

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
	rig.expectPeerWhole(t)
}

// The same while the engine runs: any scan reaches the copy — the watcher's,
// the interval's, the one a return to the app asks for.
func TestIssue329_ACopyPutInPlaceWhileTheEngineRunsKeepsThePeerWhole(t *testing.T) {
	if testing.Short() {
		t.Skip("real peer process; skipped in -short")
	}
	rig, earlier := issue329Rig(t)
	defer StopSyncthing()
	before := issue315MarkerNumber(t, issue315FolderID, rig.own)
	rig.putInPlace(t, earlier)
	if after := issue315MarkerNumber(t, issue315FolderID, rig.own); after == before {
		t.Fatalf("the copy's marker has the original's file number (%d) — the test no longer builds the reported state", after)
	}
	RescanFolder(issue315FolderID)
	rig.expectPeerWhole(t)
}

// A copy that carries the original's marker file itself — `.stfolder` moved
// over from the vault's own folder before it was deleted — has the file
// number the record names. Nothing tells it from the vault's own folder with
// its notes changed by hand.
func TestIssue329_ACopyWithTheOriginalsMarkerKeepsThePeerWhole(t *testing.T) {
	if testing.Short() {
		t.Skip("real peer process; skipped in -short")
	}
	rig, earlier := issue329Rig(t)
	before := issue315MarkerNumber(t, issue315FolderID, rig.own)
	issue315StopWithConfigOnDisk(t, rig.configDir, rig.own)
	if err := os.RemoveAll(filepath.Join(earlier, config.DefaultMarkerName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(rig.own, config.DefaultMarkerName), filepath.Join(earlier, config.DefaultMarkerName)); err != nil {
		t.Fatal(err)
	}
	rig.putInPlace(t, earlier)
	if after := issue315MarkerNumber(t, issue315FolderID, rig.own); after != before {
		t.Fatalf("the moved marker has file number %d, was %d — the test no longer builds the state it is about", after, before)
	}

	if errMsg := StartSyncthing(rig.configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()
	NoteFolderStorage()
	waitFor(t, "bridge connected to peer after the restart", 60*time.Second, func() bool {
		return bridgeConnected(rig.peer.id)
	})
	rig.expectPeerWhole(t)
}
