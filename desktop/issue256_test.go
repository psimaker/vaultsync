//go:build unix

package main

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
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
		if runsFakeEngine(pid, a.lay) {
			t.Errorf("the engine (pid %d) still runs after its test ended", pid)
			_ = syscall.Kill(pid, syscall.SIGTERM) // and leaves nothing behind itself
		}
	}
}

// runsFakeEngine reports whether process pid is still this test's fake
// engine: gone, or its ID already another process's (its command line no
// longer names this test's engine), is no.
func runsFakeEngine(pid int, lay layout) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return false
	}
	out, err := exec.Command("ps", "-ww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && strings.Contains(string(out), lay.Syncthing)
}
