package bridge

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/syncthing/syncthing/lib/config"
)

// heldCommitter holds every configuration change it matches until released,
// so a test can make a commit outlast the bridge's wait without a slow disk
// or a large database. A change it does not match passes at once.
type heldCommitter struct {
	matches func(from, to config.Configuration) bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	closing sync.Once
}

func newHeldCommitter(matches func(from, to config.Configuration) bool) *heldCommitter {
	return &heldCommitter{matches: matches, entered: make(chan struct{}), release: make(chan struct{})}
}

func (h *heldCommitter) CommitConfiguration(from, to config.Configuration) bool {
	if h.matches(from, to) {
		h.once.Do(func() { close(h.entered) })
		<-h.release
	}
	return true
}

func (h *heldCommitter) String() string { return "issue343-held-committer" }

func (h *heldCommitter) let() { h.closing.Do(func() { close(h.release) }) }

// subscribeHeld adds h to the running engine's configuration subscribers and
// releases it when the test ends, so no commit stays held into the stop.
func subscribeHeld(t *testing.T, h *heldCommitter) {
	t.Helper()
	mu.Lock()
	cfg := stCfg
	mu.Unlock()
	cfg.Subscribe(h)
	t.Cleanup(func() {
		h.let()
		cfg.Unsubscribe(h)
	})
}

func configuredFolderIDs(t *testing.T) map[string]bool {
	t.Helper()
	var folders []FolderInfo
	if err := json.Unmarshal([]byte(GetFoldersJSON()), &folders); err != nil {
		t.Fatalf("GetFoldersJSON() unmarshal: %v", err)
	}
	ids := make(map[string]bool, len(folders))
	for _, f := range folders {
		ids[f.ID] = true
	}
	return ids
}

func startWithFolder(t *testing.T, id string) {
	t.Helper()
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	if errMsg := AddFolder(id, "Vault", filepath.Join(configDir, "Vault")); errMsg != "" {
		t.Fatalf("AddFolder() = %q", errMsg)
	}
}

func removesFolder(id string) func(from, to config.Configuration) bool {
	return func(from, to config.Configuration) bool {
		_, _, had := from.Folder(id)
		_, _, has := to.Folder(id)
		return had && !has
	}
}

// The engine applies a removal to its configuration and then waits for every
// subscriber to finish with it: the model stops the folder and drops its
// database there. When that outlasts the bridge's wait, the folder is gone
// from the configuration the moment the bridge answers, and the bridge used
// to answer with a failure anyway (#343).
func TestIssue343_ARemovalAppliedBeforeTheWaitEndsIsNotAFailure(t *testing.T) {
	const id = "issue343-applied"
	startWithFolder(t, id)
	held := newHeldCommitter(removesFolder(id))
	subscribeHeld(t, held)

	defer overrideLifecycleTimeouts(t)()
	lifecycleTimeouts.configCommit = 300 * time.Millisecond

	answer := RemoveFolder(id)
	select {
	case <-held.entered:
	default:
		t.Fatal("the removal's commit was not under way when the bridge answered")
	}
	if configuredFolderIDs(t)[id] {
		t.Fatal("the folder is still configured although its removal was applied")
	}
	if answer != "" {
		t.Errorf("RemoveFolder() = %q for a removal already applied to the configuration, want success", answer)
	}

	held.let()
	if configuredFolderIDs(t)[id] {
		t.Fatal("the folder came back once the commit was let through")
	}
}

// A removal queued behind another change has not reached the configuration
// when the wait ends, and it lands once the change before it is through. The
// bridge used to give that the same answer as a removal the engine refused;
// it now says the removal is not confirmed yet (#343).
func TestIssue343_ARemovalQueuedBehindAnotherCommitLandsLater(t *testing.T) {
	const id = "issue343-queued"
	startWithFolder(t, id)

	// An unrelated change whose commit is held keeps the configuration loop
	// busy: the next change waits in its queue.
	const marker = 34343
	held := newHeldCommitter(func(from, to config.Configuration) bool {
		return to.Options.MaxRecvKbps == marker && from.Options.MaxRecvKbps != marker
	})
	subscribeHeld(t, held)
	mu.Lock()
	cfg := stCfg
	mu.Unlock()
	blocker := make(chan error, 1)
	go func() {
		waiter, err := cfg.Modify(func(c *config.Configuration) { c.Options.MaxRecvKbps = marker })
		if err == nil {
			waiter.Wait()
		}
		blocker <- err
	}()
	select {
	case <-held.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the unrelated change never reached its commit")
	}

	defer overrideLifecycleTimeouts(t)()
	lifecycleTimeouts.configCommit = 300 * time.Millisecond

	answer := RemoveFolder(id)
	if !configuredFolderIDs(t)[id] {
		t.Fatal("the folder is gone although its removal was still queued")
	}
	if answer != FolderRemovalNotConfirmedError() {
		t.Errorf("RemoveFolder() = %q for a removal that is still queued, want %q", answer, FolderRemovalNotConfirmedError())
	}

	held.let()
	if err := <-blocker; err != nil {
		t.Fatalf("the unrelated change failed: %v", err)
	}
	waitFor(t, "the queued removal to land", 10*time.Second, func() bool {
		return !configuredFolderIDs(t)[id]
	})

	// Asked again once it has landed, the bridge says the folder is not
	// configured; the app takes exactly this text for a removal that is done.
	if again := RemoveFolder(id); again != FolderNotFoundError() {
		t.Fatalf("RemoveFolder() after the removal landed = %q, want %q", again, FolderNotFoundError())
	}
}

// The app tells a removal that is done by the text the bridge exports for a
// folder that is not configured, so RemoveFolder must answer with exactly
// that text (#343).
func TestIssue343_AFolderThatIsNotConfiguredIsAnsweredWithTheExportedText(t *testing.T) {
	startWithFolder(t, "issue343-configured")
	if answer := RemoveFolder("issue343-never-configured"); answer != FolderNotFoundError() {
		t.Fatalf("RemoveFolder() for a folder that is not configured = %q, want %q", answer, FolderNotFoundError())
	}
	if FolderNotFoundError() == "" || FolderNotFoundError() == FolderRemovalNotConfirmedError() {
		t.Fatalf("FolderNotFoundError() = %q, want a text of its own", FolderNotFoundError())
	}
}

// refusingVerifier rejects every change that removes its folder, the way the
// engine refuses a configuration it cannot take: nothing changes.
type refusingVerifier struct{ id string }

func (r refusingVerifier) VerifyConfiguration(from, to config.Configuration) error {
	if removesFolder(r.id)(from, to) {
		return errors.New("issue343 refuses this removal")
	}
	return nil
}

func (refusingVerifier) CommitConfiguration(_, _ config.Configuration) bool { return true }

func (refusingVerifier) String() string { return "issue343-refusing-verifier" }

// A removal the engine refuses never lands: its answer stays the refusal,
// not the text for a removal that may still land (#343).
func TestIssue343_ARemovalTheEngineRefusesKeepsItsRefusal(t *testing.T) {
	const id = "issue343-refused"
	startWithFolder(t, id)
	mu.Lock()
	cfg := stCfg
	mu.Unlock()
	verifier := refusingVerifier{id: id}
	cfg.Subscribe(verifier)
	t.Cleanup(func() { cfg.Unsubscribe(verifier) })

	answer := RemoveFolder(id)
	if !strings.HasPrefix(answer, "modify config: ") || !strings.Contains(answer, "issue343 refuses this removal") {
		t.Fatalf("RemoveFolder() = %q for a refused removal, want the engine's refusal", answer)
	}
	if !configuredFolderIDs(t)[id] {
		t.Fatal("the folder is gone although the engine refused its removal")
	}
}

// The wait's timeout keeps the text it always had, and callers can now tell
// it from a refusal.
func TestIssue343_TheWaitsTimeoutIsToldApart(t *testing.T) {
	err := awaitWithin("probe", 0, func() error { return nil })
	if err == nil || err.Error() != "probe: timed out after 0s" {
		t.Fatalf("awaitWithin() = %v, want %q", err, "probe: timed out after 0s")
	}
	if !errors.Is(err, errWaitTimedOut) {
		t.Fatalf("awaitWithin()'s timeout does not wrap errWaitTimedOut: %v", err)
	}
	refusal := errors.New("refused")
	if err := awaitWithin("probe", time.Second, func() error { return refusal }); errors.Is(err, errWaitTimedOut) {
		t.Fatalf("awaitWithin() passed a refusal on as a timeout: %v", err)
	}
}
