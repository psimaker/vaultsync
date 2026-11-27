package bridge

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/syncthing/syncthing/lib/config"
)

// Since #341 a removed vault's share is offered again inside the commit that
// removes it, so a person can tap Accept while the engine's configuration
// queue still holds the rest of that commit, or any other change. The bridge
// waits for an accept at most lifecycleTimeouts.configCommit. An accept still
// queued when the wait ended was answered with a failure although it landed
// later, a second tap queued a second accept of the same share, and the
// floors that keep two folders out of one directory did not see the queued
// accept at all.

// startEngineForAccepts starts an engine with nothing configured and returns
// its configuration directory and the selected folder the accepts go into.
func startEngineForAccepts(t *testing.T) (configDir, root string) {
	t.Helper()
	configDir = testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	root = filepath.Join(configDir, "selected")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return configDir, root
}

func addsFolder(id string) func(from, to config.Configuration) bool {
	return func(from, to config.Configuration) bool {
		_, _, had := from.Folder(id)
		_, _, has := to.Folder(id)
		return !had && has
	}
}

// holdTheQueueWithARemoval keeps the engine's configuration loop busy the way
// it is when a share is offered again: a removal the engine has taken into
// its configuration and whose services have not finished with it (#343).
// The bridge answers that removal with success after a short wait; the next
// change waits in the queue until the test lets the removal through.
func holdTheQueueWithARemoval(t *testing.T, configDir string) *heldCommitter {
	t.Helper()
	const busy = "issue341-busy"
	if errMsg := AddFolder(busy, "Busy", filepath.Join(configDir, "busy")); errMsg != "" {
		t.Fatalf("AddFolder(busy) = %q", errMsg)
	}
	held := newHeldCommitter(removesFolder(busy))
	subscribeHeld(t, held)
	saved := lifecycleTimeouts.configCommit
	lifecycleTimeouts.configCommit = 300 * time.Millisecond
	answer := RemoveFolder(busy)
	lifecycleTimeouts.configCommit = saved
	if answer != "" {
		t.Fatalf("RemoveFolder(busy) = %q, want success for a removal already applied", answer)
	}
	select {
	case <-held.entered:
	default:
		t.Fatal("the removal's commit is not under way: nothing holds the queue")
	}
	return held
}

// queueAnAccept accepts a share while the queue is held and requires the
// answer for an accept still queued.
func queueAnAccept(t *testing.T, id, path, root string) {
	t.Helper()
	saved := lifecycleTimeouts.configCommit
	lifecycleTimeouts.configCommit = 300 * time.Millisecond
	answer := AcceptPendingFolder(id, "Vault", path, root, false)
	lifecycleTimeouts.configCommit = saved
	if answer != FolderAcceptNotConfirmedError() {
		t.Fatalf("AcceptPendingFolder(%s) = %q for an accept still queued, want %q", id, answer, FolderAcceptNotConfirmedError())
	}
	if !AcceptInFlight(id) {
		t.Fatalf("AcceptInFlight(%s) = false for an accept still queued", id)
	}
}

// drainTheQueue makes one more change and waits for it: the engine takes
// changes in order, so everything queued before it has been taken then.
func drainTheQueue(t *testing.T) {
	t.Helper()
	saved := lifecycleTimeouts.configCommit
	lifecycleTimeouts.configCommit = 30 * time.Second
	defer func() { lifecycleTimeouts.configCommit = saved }()
	mu.Lock()
	defer mu.Unlock()
	err := commitConfigLocked(func(c *config.Configuration) {
		c.Options.MaxRecvKbps++
	})
	if err != nil {
		t.Fatalf("a change after the queued ones: %v", err)
	}
}

func configuredPath(t *testing.T, id string) (string, bool) {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	f, ok := stCfg.Folder(id)
	return f.Path, ok
}

func countFolders(t *testing.T, id string) int {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	n := 0
	for _, f := range stCfg.RawCopy().Folders {
		if f.ID == id {
			n++
		}
	}
	return n
}

// An accept still queued when the bridge's wait ends is told apart from a
// refusal: it lands once the change before it is through. Until then a
// second accept of the same share — also into another folder — gets the
// same answer and creates and queues nothing, so the share is configured
// once, where the first accept put it. The share counts as in flight until
// the engine has taken the change, and not after.
func TestIssue341_AnAcceptQueuedBehindAnotherChangeIsToldApartAndNotSentTwice(t *testing.T) {
	const id = "issue341-queued-accept"
	configDir, root := startEngineForAccepts(t)
	held := holdTheQueueWithARemoval(t, configDir)
	defer overrideLifecycleTimeouts(t)()

	first := filepath.Join(root, "Vault")
	queueAnAccept(t, id, first, root)
	if _, ok := configuredPath(t, id); ok {
		t.Fatal("the share is configured although its accept was still queued")
	}

	other := filepath.Join(root, "Other")
	lifecycleTimeouts.configCommit = 300 * time.Millisecond
	if answer := AcceptPendingFolder(id, "Other", other, root, false); answer != FolderAcceptNotConfirmedError() {
		t.Fatalf("a second AcceptPendingFolder() = %q while the first is queued, want %q", answer, FolderAcceptNotConfirmedError())
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatalf("the second accept made its folder (%v): it got past the guard", err)
	}

	held.let()
	drainTheQueue(t)
	if path, ok := configuredPath(t, id); !ok || path != first {
		t.Fatalf("the share is configured at %q (%v), want the first accept's %q", path, ok, first)
	}
	if n := countFolders(t, id); n != 1 {
		t.Fatalf("the share is configured %d times, want once", n)
	}
	if AcceptInFlight(id) {
		t.Fatal("the share still counts as in flight after its accept landed")
	}
	if answer := AcceptPendingFolder(id, "Vault", first, root, false); answer != "folder already exists" {
		t.Fatalf("AcceptPendingFolder() once it landed = %q, want %q", answer, "folder already exists")
	}
}

// The floors that keep two folders out of one directory count a queued
// accept as a folder (#341, decision 001): another share accepted into its
// folder — merge consent or not —, a folder added in, at or around it, and a
// repair onto it are all refused with the refusal a configured folder gets.
// Without that, both would land in one directory, and only the launch
// shield would see it at the next start.
func TestIssue341_AQueuedAcceptsFolderIsTakenByEveryOverlapFloor(t *testing.T) {
	const queued = "issue341-queued"
	configDir, root := startEngineForAccepts(t)
	// A folder whose directory is moved below, for the repair: its marker
	// moves with it and keeps its file number, so only the overlap is left
	// to refuse the repair.
	const moved = "issue341-moved"
	ownDir := filepath.Join(configDir, "moved")
	if errMsg := AddFolder(moved, "Moved", ownDir); errMsg != "" {
		t.Fatalf("AddFolder(moved) = %q", errMsg)
	}
	waitFor(t, "the moved folder's marker", 10*time.Second, func() bool {
		folder, _ := configuredFolder(moved)
		_, err := os.Stat(folderMarkerPath(folder, ownDir))
		return err == nil
	})
	held := holdTheQueueWithARemoval(t, configDir)
	defer overrideLifecycleTimeouts(t)()

	target := filepath.Join(root, "Vault")
	queueAnAccept(t, queued, target, root)

	t.Run("an accept of another share", func(t *testing.T) {
		for _, consent := range []bool{false, true} {
			got := AcceptPendingFolder("issue341-second", "Vault", target, root, consent)
			if want := folderPathOverlapError(target, target); got != want {
				t.Errorf("AcceptPendingFolder(another share, merge consent %v) into a queued accept's folder = %q, want %q", consent, got, want)
			}
		}
		if AcceptInFlight("issue341-second") {
			t.Error("the refused accept was queued")
		}
	})

	t.Run("a folder added", func(t *testing.T) {
		for _, path := range []string{target, filepath.Join(target, "Inner"), root} {
			got := AddFolder("issue341-added", "Added", path)
			if want := folderPathOverlapError(target, path); got != want || want == "" {
				t.Errorf("AddFolder at %q beside a queued accept's %q = %q, want %q", path, target, got, want)
			}
		}
		if got := AddFolder(queued, "Vault", filepath.Join(configDir, "elsewhere")); got != "folder already exists" {
			t.Errorf("AddFolder of the queued accept's own share = %q, want %q", got, "folder already exists")
		}
	})

	t.Run("a repair", func(t *testing.T) {
		inside := filepath.Join(target, "Moved")
		if err := os.Rename(ownDir, inside); err != nil {
			t.Fatal(err)
		}
		if got := SetFolderPath(moved, inside); got != FolderPathOverlapError() {
			t.Errorf("SetFolderPath into a queued accept's folder = %q, want %q", got, FolderPathOverlapError())
		}
		if path, _ := configuredPath(t, moved); path != ownDir {
			t.Errorf("the refused repair moved the folder to %q", path)
		}
	})

	held.let()
	drainTheQueue(t)
	if n := len(configuredFolderIDs(t)); n != 2 {
		t.Fatalf("%d folders configured once the queue drained, want the queued accept's and the moved one", n)
	}
}

func configuredFolder(id string) (config.FolderConfiguration, bool) {
	mu.Lock()
	defer mu.Unlock()
	return stCfg.Folder(id)
}

// Only a folder at the accept's own path is the accept: a second accept of
// the same share that got past the guard in the instant between the engine
// taking the first one and the swap is refused by the engine as a duplicate,
// and must not be answered as if it had landed where it asked. The instant
// cannot be hit on purpose, so the test clears the record by hand to stand
// for it.
func TestIssue341_ADuplicateTheEngineRefusedIsNotTakenForTheAccept(t *testing.T) {
	const id = "issue341-duplicate"
	configDir, root := startEngineForAccepts(t)
	held := holdTheQueueWithARemoval(t, configDir)
	defer overrideLifecycleTimeouts(t)()

	first := filepath.Join(root, "Vault")
	queueAnAccept(t, id, first, root)
	clearAcceptInFlight(id)

	lifecycleTimeouts.configCommit = 20 * time.Second
	go func() {
		time.Sleep(200 * time.Millisecond)
		held.let()
	}()
	second := filepath.Join(root, "Other")
	if got := AcceptPendingFolder(id, "Other", second, root, false); got != "folder already exists" {
		t.Fatalf("the duplicate accept = %q, want %q: the first accept's folder was taken for it", got, "folder already exists")
	}
	if path, _ := configuredPath(t, id); path != first {
		t.Fatalf("the share is configured at %q, want the first accept's %q", path, first)
	}
}

// The engine takes an accept into its configuration first and then waits
// for its services. An accept that reached the configuration before the
// wait ended is accepted, whatever the services still do.
func TestIssue341_AnAcceptAppliedBeforeTheWaitEndsIsAccepted(t *testing.T) {
	const id = "issue341-applied-accept"
	_, root := startEngineForAccepts(t)
	held := newHeldCommitter(addsFolder(id))
	subscribeHeld(t, held)
	defer overrideLifecycleTimeouts(t)()
	lifecycleTimeouts.configCommit = 300 * time.Millisecond

	path := filepath.Join(root, "Vault")
	answer := AcceptPendingFolder(id, "Vault", path, root, false)
	select {
	case <-held.entered:
	default:
		t.Fatal("the accept's commit was not under way when the bridge answered")
	}
	if answer != "" {
		t.Fatalf("AcceptPendingFolder() = %q for an accept already in the configuration, want success", answer)
	}
	if got, ok := configuredPath(t, id); !ok || got != path {
		t.Fatalf("configured at %q (%v), want %q", got, ok, path)
	}
	if AcceptInFlight(id) {
		t.Fatal("an accept already in the configuration still counts as in flight")
	}
	held.let()
}

// The engine works through its queue while it stops and saves what it
// applied; a stop that ends before a queued accept is taken loses it. Here
// the queue is held through the stop, so the accept is lost: the next engine
// starts without it and without its record, and the share may be accepted
// again.
func TestIssue341_AnAcceptAStopNeverTookIsGoneWithThatEngine(t *testing.T) {
	const id = "issue341-lost-accept"
	configDir, root := startEngineForAccepts(t)
	holdTheQueueWithARemoval(t, configDir)
	defer overrideLifecycleTimeouts(t)()
	path := filepath.Join(root, "Vault")
	queueAnAccept(t, id, path, root)

	if errMsg := StopSyncthing(); errMsg != "" {
		t.Fatalf("StopSyncthing() = %q", errMsg)
	}
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() again = %q", errMsg)
	}
	if AcceptInFlight(id) {
		t.Fatal("the new engine still counts an accept the last one never took")
	}
	if _, ok := configuredPath(t, id); ok {
		t.Fatal("the accept the stopped engine never took is configured")
	}
	if answer := AcceptPendingFolder(id, "Vault", path, root, false); answer != "" {
		t.Fatalf("AcceptPendingFolder() on the new engine = %q, want success", answer)
	}
}

// The app reads the text from the bridge instead of keeping a copy, so the
// answer must be exactly what the export returns.
func TestIssue341_TheAcceptsAnswerIsTheExportedText(t *testing.T) {
	if FolderAcceptNotConfirmedError() != folderAcceptNotConfirmed || folderAcceptNotConfirmed == "" {
		t.Fatalf("FolderAcceptNotConfirmedError() = %q", FolderAcceptNotConfirmedError())
	}
}
