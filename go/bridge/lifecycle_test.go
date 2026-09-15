package bridge

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/syncthing/syncthing/lib/svcutil"
)

// waitForCondition polls cond until it holds or the timeout expires.
func waitForCondition(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// The engine's main supervisor can exit on its own — a fatal service error
// terminates the supervisor tree, Syncthing's App then closes the database
// and reports itself stopped. The bridge's liveness flag used to stay true
// regardless, so the app kept polling a dead engine and a restart attempt
// bounced off "already running" (#181).
func TestIssue181_IsRunningFollowsSupervisorExit(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	if !IsRunning() {
		t.Fatal("IsRunning() = false after start")
	}

	// Terminate the supervisor from the inside, bypassing the bridge — the
	// same channel a fatal service error uses (stopWithErr → cancel → wait).
	mu.Lock()
	app := stApp
	mu.Unlock()
	if app == nil {
		t.Fatal("no engine instance after start")
	}
	app.Stop(svcutil.ExitError)

	if !waitForCondition(3*time.Second, func() bool { return !IsRunning() }) {
		t.Fatal("IsRunning() stayed true after the engine supervisor exited")
	}
	if gen := EventStreamGeneration(); gen != 0 {
		t.Fatalf("EventStreamGeneration() = %d after supervisor exit, want 0", gen)
	}
	if id := DeviceID(); id != "" {
		t.Fatalf("DeviceID() = %q after supervisor exit, want empty", id)
	}
	if reason := EngineExitReason(); !strings.Contains(reason, "status 1") {
		t.Fatalf("EngineExitReason() = %q, want the supervisor's exit status", reason)
	}

	// A dead engine is not "already running": the restart the app performs
	// after detecting the death (decision 009) must succeed.
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() over a dead engine = %q, want success", errMsg)
	}
	if !IsRunning() {
		t.Fatal("IsRunning() = false after restart over a dead engine")
	}
	if id := DeviceID(); id == "" {
		t.Fatal("DeviceID() empty after restart over a dead engine")
	}
	if reason := EngineExitReason(); reason != "" {
		t.Fatalf("EngineExitReason() = %q after restart, want empty", reason)
	}
	if errMsg := StopSyncthing(); errMsg != "" {
		t.Fatalf("StopSyncthing() = %q, want success", errMsg)
	}
	if IsRunning() {
		t.Fatal("IsRunning() = true after stop")
	}
}

// A config commit waits on the early supervisor's config loop. When that loop
// is gone, the wait never returns — and because the caller holds the bridge
// lock while waiting, every later bridge call blocks behind it (#181).
func TestIssue181_ConfigCommitWaitHasDeadline(t *testing.T) {
	configDir, err := os.MkdirTemp("", "vaultsync-test-*")
	if err != nil {
		t.Fatal(err)
	}
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer overrideLifecycleTimeouts(t)()
	lifecycleTimeouts.configCommit = 200 * time.Millisecond

	// Kill the early supervisor (event logger + config loop) underneath the
	// running engine.
	mu.Lock()
	stEarlyCancel()
	mu.Unlock()

	done := make(chan string, 1)
	go func() { done <- SetDiscoveryEnabled(false, false) }()
	select {
	case errMsg := <-done:
		if !strings.Contains(errMsg, "config commit: timed out") {
			t.Fatalf("SetDiscoveryEnabled() = %q, want a config commit timeout", errMsg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SetDiscoveryEnabled() did not return within 3s — the config commit wait has no deadline")
	}

	// The bridge lock must be free again: a later call answers promptly.
	done2 := make(chan bool, 1)
	go func() { done2 <- IsRunning() }()
	select {
	case <-done2:
	case <-time.After(3 * time.Second):
		t.Fatal("IsRunning() blocked behind the timed-out config commit")
	}

	StopSyncthing()
	time.Sleep(100 * time.Millisecond)
	os.RemoveAll(configDir)
}

// overrideLifecycleTimeouts returns a restore func for lifecycleTimeouts.
func overrideLifecycleTimeouts(t *testing.T) func() {
	t.Helper()
	saved := lifecycleTimeouts
	return func() { lifecycleTimeouts = saved }
}

// The deadline helper must return when the waited-for goroutine never does,
// and its error must name the phase so the app can show what timed out.
func TestIssue181_AwaitWithinBoundsHangingWait(t *testing.T) {
	block := make(chan struct{})
	defer close(block)

	started := time.Now()
	err := awaitWithin("probe", 50*time.Millisecond, func() error {
		<-block
		return nil
	})
	if err == nil {
		t.Fatal("awaitWithin() = nil for a hanging wait, want timeout error")
	}
	if !strings.Contains(err.Error(), "probe: timed out after 50ms") {
		t.Fatalf("awaitWithin() error = %q, want phase and deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("awaitWithin() took %s, want roughly the 50ms deadline", elapsed)
	}

	// A wait that completes in time passes its result through unchanged.
	if err := awaitWithin("probe", time.Second, func() error { return nil }); err != nil {
		t.Fatalf("awaitWithin() = %v for a completing wait, want nil", err)
	}
}

// A stop that overruns its deadline must not wedge the bridge, and it must
// not let a second engine start over the one still shutting down: the two
// would share the database and every folder. The start is refused until
// the pending stop has released everything, then succeeds.
func TestIssue181_StopOverrunNeverStartsSecondEngine(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}

	restore := overrideLifecycleTimeouts(t)
	// A zero deadline expires immediately, so the stop is reported as overrun
	// while the real stop still runs in the background.
	lifecycleTimeouts.stop = 0
	errMsg := StopSyncthing()
	restore()
	if !strings.Contains(errMsg, "stop: timed out") {
		t.Fatalf("StopSyncthing() = %q, want a stop timeout", errMsg)
	}
	if IsRunning() {
		t.Fatal("IsRunning() = true after an overrun stop")
	}
	if errMsg := StopSyncthing(); errMsg != "" {
		t.Fatalf("second StopSyncthing() = %q, want no-op", errMsg)
	}

	// The next start waits for the pending stop, never runs alongside it.
	var last string
	ok := waitForCondition(20*time.Second, func() bool {
		last = StartSyncthing(configDir)
		if last == "" {
			return true
		}
		if last != "start: the previous engine is still stopping" {
			t.Fatalf("StartSyncthing() = %q while a stop is pending, want the pending refusal", last)
		}
		return false
	})
	if !ok {
		t.Fatalf("StartSyncthing() never succeeded after the pending stop finished: %q", last)
	}
	if !IsRunning() {
		t.Fatal("IsRunning() = false after the restart")
	}
}

// The pending-stop guard on its own, without timing: a start over a pending
// stop is refused with a stable message and succeeds once the stop releases.
func TestIssue181_StartRefusedWhileStopPending(t *testing.T) {
	configDir := testConfigDir(t)

	pending := make(chan struct{})
	mu.Lock()
	stStopPending = pending
	mu.Unlock()

	if errMsg := StartSyncthing(configDir); errMsg != "start: the previous engine is still stopping" {
		close(pending)
		t.Fatalf("StartSyncthing() = %q while a stop is pending, want refusal", errMsg)
	}
	if IsRunning() {
		close(pending)
		t.Fatal("IsRunning() = true after a refused start")
	}

	close(pending)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() after the pending stop released = %q, want success", errMsg)
	}
	if !IsRunning() {
		t.Fatal("IsRunning() = false after start")
	}
}
