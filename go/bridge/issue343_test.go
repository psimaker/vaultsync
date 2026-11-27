package bridge

import (
	"encoding/json"
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
// bridge used to give that the same answer as a removal the engine refused
// (#343).
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
	if !strings.Contains(answer, "timed out") {
		t.Fatalf("RemoveFolder() = %q for a queued removal, want the wait's timeout", answer)
	}
	if !strings.HasPrefix(answer, "folder removal not confirmed") {
		t.Errorf("RemoveFolder() = %q for a removal that is still queued, want it told apart from a refusal", answer)
	}

	held.let()
	if err := <-blocker; err != nil {
		t.Fatalf("the unrelated change failed: %v", err)
	}
	waitFor(t, "the queued removal to land", 10*time.Second, func() bool {
		return !configuredFolderIDs(t)[id]
	})
}
