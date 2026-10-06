//go:build unix

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/psimaker/vaultsync/hub/syncthing"
)

// Codex review of #212, major 11: setup stops waiting for an engine whose
// supervisor already gave up, instead of waiting out the whole minute.
func TestIssue175_SetupNoticesAFailedTemporaryEngine(t *testing.T) {
	// A supervisor that gives up at once: another one holds the engine.
	eng, st := engineFixture(t)
	unlock, err := lockFile(eng.lay.Lock)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	start := time.Now()
	sup := superviseInBackground(context.Background(), eng, st)
	err = waitEngine(context.Background(), syncthing.NewClient("http://127.0.0.1:1", "k"), time.Minute, sup)
	if !errors.Is(err, ErrEngineRunning) {
		t.Fatalf("got %v", err)
	}
	// Stopping an engine that already ended does not wait out the grace.
	sup.stop(30 * time.Second)
	if time.Since(start) > 10*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
}

// Codex review of #212, round 2: `run` prepares the engine under the lock
// setup holds while it prepares — never at the same time.
func TestIssue175_RunWaitsForSetupToFinishPreparing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setup.lock")
	unlock, err := lockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(1500*time.Millisecond, unlock)
	start := time.Now()
	got, err := waitForLock(context.Background(), path, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	got()
	if waited := time.Since(start); waited < time.Second {
		t.Fatalf("took the lock while setup still held it (after %s)", waited)
	}
	hold, _ := lockFile(path)
	defer hold()
	if _, err := waitForLock(context.Background(), path, time.Second); !errors.Is(err, ErrEngineRunning) {
		t.Fatalf("a lock held past the timeout must be refused, got %v", err)
	}
}

// Codex review of #212, major 12: uninstall does not claim sync stopped while
// a `vaultsync run` in a terminal still owns the engine.
func TestIssue175_UninstallSeesAForegroundEngine(t *testing.T) {
	lay, err := layoutFor("linux", t.TempDir(), envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	if engineOwnerRunning(lay, 0) {
		t.Fatal("nothing installed, nothing running")
	}
	unlock, err := lockFile(lay.Lock)
	if err != nil {
		t.Fatal(err)
	}
	if !engineOwnerRunning(lay, 600*time.Millisecond) {
		t.Fatal("a held engine lock must count as a running engine")
	}
	unlock()
	if engineOwnerRunning(lay, 0) {
		t.Fatal("a released lock is no running engine")
	}
}

// Codex review of #212, major 13: the service runs with the folder setup
// resolved, whatever the service manager's environment says.
func TestIssue175_RunTakesTheStateDirSetupResolved(t *testing.T) {
	a := &app{goos: "linux", home: t.TempDir(), getenv: envOf(nil)}
	if err := a.run(context.Background(), []string{"--state-dir", "relative/dir"}); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("got %v", err)
	}
	lay, _ := layoutFor("linux", "/home/me", envOf(nil))
	moved := lay.at("/srv/state/vaultsync")
	if moved.Agent != "/srv/state/vaultsync/bin/vaultsync" || moved.Home != "/srv/state/vaultsync/syncthing" || moved.Lock != "/srv/state/vaultsync/agent.lock" {
		t.Fatalf("layout at: %+v", moved)
	}
}

// Codex review of #212, minor 16: `vaultsync` becomes callable by name
// through ~/.local/bin — without replacing anything already there.
func TestIssue175_CommandLinkIsOursOrNothing(t *testing.T) {
	home := t.TempDir()
	lay, _ := layoutFor("linux", home, envOf(nil))
	writeFile(t, lay.Agent, "#!/bin/sh\n")
	a := &app{goos: "linux", home: home, lay: lay}
	link := filepath.Join(home, ".local", "bin", "vaultsync")

	t.Setenv("PATH", "/usr/bin:/bin")
	if got := a.installCommand(&term{out: os.Stdout}); got != "~/.local/bin/vaultsync" {
		t.Fatalf("not on PATH: %q", got)
	}
	if target, err := os.Readlink(link); err != nil || target != lay.Agent {
		t.Fatalf("link: %q %v", target, err)
	}
	t.Setenv("PATH", filepath.Dir(link)+":/usr/bin:/bin")
	if err := os.Chmod(lay.Agent, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := a.installCommand(&term{out: os.Stdout}); got != "vaultsync" {
		t.Fatalf("on PATH: %q", got)
	}

	// Someone else's vaultsync stays where it is — and is never recommended,
	// even when it is the one on PATH.
	other := t.TempDir()
	lay2, _ := layoutFor("linux", other, envOf(nil))
	writeFile(t, lay2.Agent, "#!/bin/sh\n")
	foreign := filepath.Join(other, ".local", "bin", "vaultsync")
	writeFile(t, foreign, "#!/bin/sh\necho a different program\n")
	if err := os.Chmod(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	b := &app{goos: "linux", home: other, lay: lay2}
	t.Setenv("PATH", filepath.Dir(foreign)+":/usr/bin:/bin")
	if got := b.installCommand(&term{out: os.Stdout}); got != lay2.Agent {
		t.Fatalf("recommended %q instead of VaultSync's own %q", got, lay2.Agent)
	}
	if data, _ := os.ReadFile(foreign); !strings.Contains(string(data), "a different program") {
		t.Fatal("an existing ~/.local/bin/vaultsync was replaced")
	}
}

// Codex review of #212, round 3: `run` itself waits while setup prepares —
// it neither installs nor prepares anything during that time.
func TestIssue175_RunDoesNotPrepareWhileSetupHoldsTheLock(t *testing.T) {
	home := t.TempDir()
	lay, err := layoutFor("linux", home, envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	hold, err := lockFile(filepath.Join(lay.Base, "setup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	a := &app{goos: "linux", home: home, getenv: envOf(nil), lay: lay,
		svc: service{goos: "linux", home: home, getenv: envOf(nil), lay: lay, run: &fakeRunner{}}}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if err := a.run(ctx, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("run did not wait for setup: %v", err)
	}
	// Not even the download started: it would have created bin/.
	for _, p := range []string{lay.Bin, lay.Syncthing, lay.Home, lay.State} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("run touched %s while setup held the lock", p)
		}
	}
}
