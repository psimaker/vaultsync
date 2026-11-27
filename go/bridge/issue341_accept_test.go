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
// later, and a second tap queued a second accept of the same share.

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

// holdAnUnrelatedChange keeps the engine's configuration loop busy with a
// change of its own until the test lets it through: the next change waits
// in the queue. The returned channel carries that change's result.
func holdAnUnrelatedChange(t *testing.T) (*heldCommitter, chan error) {
	t.Helper()
	const marker = 34141
	held := newHeldCommitter(func(from, to config.Configuration) bool {
		return to.Options.MaxRecvKbps == marker && from.Options.MaxRecvKbps != marker
	})
	subscribeHeld(t, held)
	mu.Lock()
	cfg := stCfg
	mu.Unlock()
	done := make(chan error, 1)
	go func() {
		waiter, err := cfg.Modify(func(c *config.Configuration) { c.Options.MaxRecvKbps = marker })
		if err == nil {
			waiter.Wait()
		}
		done <- err
	}()
	select {
	case <-held.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the unrelated change never reached its commit")
	}
	return held, done
}

func configuredPath(t *testing.T, id string) (string, bool) {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	f, ok := stCfg.Folder(id)
	return f.Path, ok
}

// An accept still queued when the bridge's wait ends is told apart from a
// refusal: it lands once the change before it is through. Until then a
// second accept of the same share — also into another folder — gets the
// same answer and queues nothing, so the share is configured once, where
// the first accept put it.
func TestIssue341_AnAcceptQueuedBehindAnotherChangeIsToldApartAndNotSentTwice(t *testing.T) {
	const id = "issue341-queued-accept"
	_, root := startEngineForAccepts(t)
	held, blocker := holdAnUnrelatedChange(t)
	defer overrideLifecycleTimeouts(t)()
	lifecycleTimeouts.configCommit = 300 * time.Millisecond

	first := filepath.Join(root, "Vault")
	if answer := AcceptPendingFolder(id, "Vault", first, root, false); answer != FolderAcceptNotConfirmedError() {
		t.Fatalf("AcceptPendingFolder() = %q for an accept still queued, want %q", answer, FolderAcceptNotConfirmedError())
	}
	if _, ok := configuredPath(t, id); ok {
		t.Fatal("the share is configured although its accept was still queued")
	}

	other := filepath.Join(root, "Other")
	if answer := AcceptPendingFolder(id, "Other", other, root, false); answer != FolderAcceptNotConfirmedError() {
		t.Fatalf("a second AcceptPendingFolder() = %q while the first is queued, want %q", answer, FolderAcceptNotConfirmedError())
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatalf("the second accept made its folder (%v): it got past the guard", err)
	}

	held.let()
	if err := <-blocker; err != nil {
		t.Fatalf("the unrelated change failed: %v", err)
	}
	waitFor(t, "the queued accept to land", 10*time.Second, func() bool {
		_, ok := configuredPath(t, id)
		return ok
	})
	// Let anything else that was queued land too.
	time.Sleep(500 * time.Millisecond)
	if path, _ := configuredPath(t, id); path != first {
		t.Fatalf("the share is configured at %q, want the first accept's %q", path, first)
	}
	if n := countFolders(t, id); n != 1 {
		t.Fatalf("the share is configured %d times, want once", n)
	}
	if answer := AcceptPendingFolder(id, "Vault", first, root, false); answer != "folder already exists" {
		t.Fatalf("AcceptPendingFolder() once it landed = %q, want %q", answer, "folder already exists")
	}
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
	held.let()
}

// An accept a stopped engine never took from its queue is gone with it: the
// next engine lets the share be accepted again.
func TestIssue341_ANewEngineForgetsAnAcceptTheLastOneNeverTook(t *testing.T) {
	const id = "issue341-lost-accept"
	configDir, root := startEngineForAccepts(t)
	markAcceptInFlight(id)
	if answer := AcceptPendingFolder(id, "Vault", filepath.Join(root, "Vault"), root, false); answer != FolderAcceptNotConfirmedError() {
		t.Fatalf("AcceptPendingFolder() = %q while an accept of it is in flight, want %q", answer, FolderAcceptNotConfirmedError())
	}

	if errMsg := StopSyncthing(); errMsg != "" {
		t.Fatalf("StopSyncthing() = %q", errMsg)
	}
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() again = %q", errMsg)
	}
	if acceptInFlight(id) {
		t.Fatal("the new engine still counts an accept the last one never took")
	}
	if answer := AcceptPendingFolder(id, "Vault", filepath.Join(root, "Vault"), root, false); answer != "" {
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
