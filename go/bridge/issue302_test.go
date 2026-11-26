package bridge

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A change committed through the bridge is on disk when StopSyncthing
// returns (#302). The configuration wrapper saves on a five-second timer and
// once more as its Serve loop returns; the stop cancels the supervisor that
// runs that loop. On the iPhone a background run stops the engine and then
// completes its task, and iOS may suspend the process at once: a save still
// pending in another goroutine is frozen there, and lost if the process is
// then terminated. The next start loads what config.xml held — a vault added
// in that run missing, a vault removed in it back.
//
// Each round reads config.xml the moment StopSyncthing returns, then waits
// for the file to settle before the next start, so a late save and a lost
// one tell apart and no round starts over the previous one's pending write.
const issue302Rounds = 10

func TestIssue302_AnAddedVaultIsOnDiskWhenTheStopReturns(t *testing.T) {
	configDir := testConfigDir(t)

	late, slowest := 0, time.Duration(0)
	for i := 0; i < issue302Rounds; i++ {
		if errMsg := StartSyncthing(configDir); errMsg != "" {
			t.Fatalf("round %d: StartSyncthing() = %q", i, errMsg)
		}
		id := fmt.Sprintf("issue302-add-%d", i)
		if errMsg := AddFolder(id, "Vault", filepath.Join(configDir, id)); errMsg != "" {
			t.Fatalf("round %d: AddFolder() = %q", i, errMsg)
		}
		if errMsg := StopSyncthing(); errMsg != "" {
			t.Fatalf("round %d: StopSyncthing() = %q", i, errMsg)
		}
		if !issue302Persisted(configDir, id) {
			late++
		}
		slowest = max(slowest, issue302SettleTime(t, "config.xml to hold the added vault", func() bool {
			return issue302Persisted(configDir, id)
		}))
	}
	t.Logf("slowest round: config.xml held the change %v after StopSyncthing returned", slowest)
	if late > 0 {
		t.Fatalf("%d of %d rounds: config.xml lacked the vault added just before StopSyncthing returned (the save came later)", late, issue302Rounds)
	}
}

func TestIssue302_ARemovedVaultIsGoneFromDiskWhenTheStopReturns(t *testing.T) {
	configDir := testConfigDir(t)

	late, slowest := 0, time.Duration(0)
	for i := 0; i < issue302Rounds; i++ {
		id := fmt.Sprintf("issue302-remove-%d", i)

		// The vault exists on disk before the round's removal.
		if errMsg := StartSyncthing(configDir); errMsg != "" {
			t.Fatalf("round %d: StartSyncthing() = %q", i, errMsg)
		}
		if errMsg := AddFolder(id, "Vault", filepath.Join(configDir, id)); errMsg != "" {
			t.Fatalf("round %d: AddFolder() = %q", i, errMsg)
		}
		if errMsg := StopSyncthing(); errMsg != "" {
			t.Fatalf("round %d: StopSyncthing() = %q", i, errMsg)
		}
		waitFor(t, "config.xml to hold the vault before its removal", 10*time.Second, func() bool {
			return issue302Persisted(configDir, id)
		})

		if errMsg := StartSyncthing(configDir); errMsg != "" {
			t.Fatalf("round %d: restart StartSyncthing() = %q", i, errMsg)
		}
		if errMsg := RemoveFolder(id); errMsg != "" {
			t.Fatalf("round %d: RemoveFolder() = %q", i, errMsg)
		}
		if errMsg := StopSyncthing(); errMsg != "" {
			t.Fatalf("round %d: StopSyncthing() after the removal = %q", i, errMsg)
		}
		if !issue302PersistedWithout(configDir, id) {
			late++
		}
		slowest = max(slowest, issue302SettleTime(t, "config.xml to drop the removed vault", func() bool {
			return issue302PersistedWithout(configDir, id)
		}))
	}
	t.Logf("slowest round: config.xml held the change %v after StopSyncthing returned", slowest)
	if late > 0 {
		t.Fatalf("%d of %d rounds: config.xml still held the vault removed just before StopSyncthing returned (the save came later)", late, issue302Rounds)
	}
}

// A stop that overran its deadline finishes in the background, and the next
// start waits for it (#181). The save belongs to that finish: once the
// pending stop has released, the change is on disk, so the start that waited
// for it loads the change.
func TestIssue302_AStopThatOverranItsDeadlineSavesBeforeItReleases(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() = %q", errMsg)
	}
	const id = "issue302-overrun"
	if errMsg := AddFolder(id, "Vault", filepath.Join(configDir, id)); errMsg != "" {
		t.Fatalf("AddFolder() = %q", errMsg)
	}

	restore := overrideLifecycleTimeouts(t)
	// A zero deadline expires at once: the stop is reported as overrun
	// while the real stop runs on in the background.
	lifecycleTimeouts.stop = 0
	errMsg := StopSyncthing()
	restore()
	if !strings.Contains(errMsg, "stop: timed out") {
		t.Fatalf("StopSyncthing() = %q, want a stop timeout", errMsg)
	}
	mu.Lock()
	pending := stStopPending
	mu.Unlock()
	if pending == nil {
		t.Fatal("no pending stop after an overrun stop")
	}
	select {
	case <-pending:
	case <-time.After(30 * time.Second):
		t.Fatal("the pending stop never released")
	}
	if !issue302Persisted(configDir, id) {
		t.Fatal("config.xml lacked the vault added before the stop when the pending stop released")
	}
}

// issue302SettleTime waits for cond, polling every millisecond, and returns
// how long it took from the call — taken right after StopSyncthing returned.
func issue302SettleTime(t *testing.T, what string, cond func() bool) time.Duration {
	t.Helper()
	start := time.Now()
	for !cond() {
		if time.Since(start) > 10*time.Second {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
	return time.Since(start)
}

// issue302Persisted reports whether config.xml — what the next start loads
// — names the folder. issue302PersistedWithout reports whether it is readable
// and does not. A file that cannot be read proves neither, so it never passes
// as "on disk" or as "gone from disk".
func issue302Persisted(configDir, id string) bool {
	ids, ok := issue302PersistedFolderIDs(configDir)
	return ok && ids[id]
}

func issue302PersistedWithout(configDir, id string) bool {
	ids, ok := issue302PersistedFolderIDs(configDir)
	return ok && !ids[id]
}

func issue302PersistedFolderIDs(configDir string) (map[string]bool, bool) {
	raw, err := os.ReadFile(filepath.Join(configDir, "config.xml"))
	if err != nil {
		return nil, false
	}
	var cfg struct {
		Folders []struct {
			ID string `xml:"id,attr"`
		} `xml:"folder"`
	}
	if err := xml.Unmarshal(raw, &cfg); err != nil {
		return nil, false
	}
	ids := map[string]bool{}
	for _, f := range cfg.Folders {
		ids[f.ID] = true
	}
	return ids, true
}
