//go:build unix

package main

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"
)

// A test that runs the agent and ends before it stops the run — a t.Fatal
// on the way — takes the run's engine down with it (#256). The engine is a
// child process, and on macOS nothing else stops it: fake engines of a
// failed TestIssue176_RunServesTheSocketWhileTheEngineRuns were found
// looping days later, their temporary folders long gone.
func TestIssue256_ARunTestThatEndsEarlyLeavesNoEngineRunning(t *testing.T) {
	a, _ := runFixture(t)
	t.Run("ends before it stops the run", func(t *testing.T) {
		inBackground(t, func(ctx context.Context) error { return a.run(ctx, nil) })
		e2eWait(t, "the engine", 15*time.Second, func() bool { return len(fakeEnginePIDs(t, a.lay)) > 0 })
		t.Skip("ends here, before the run is stopped — the way a t.Fatal leaves")
	})
	pids := fakeEnginePIDs(t, a.lay)
	if len(pids) == 0 {
		t.Fatal("the engine never started")
	}
	for _, pid := range pids {
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Errorf("the engine (pid %d) still runs after its test ended (kill -0: %v)", pid, err)
			_ = syscall.Kill(pid, syscall.SIGTERM) // and leaves nothing behind itself
		}
	}
}
