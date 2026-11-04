//go:build unix

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/psimaker/vaultsync/desktop/internal/layout"
	"github.com/psimaker/vaultsync/desktop/internal/lock"
	"github.com/psimaker/vaultsync/desktop/tray"
)

func TestIssue176_TrayStartLocksLikeARefresh(t *testing.T) {
	dir := t.TempDir()
	lay := layout.Layout{}.At(dir)
	if err := os.WriteFile(lay.State, []byte(`{"version":1,"guiPort":8384}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Taken: the handle is the controller's.
	held, err := lockAtStart(lay)
	if err != nil || held == nil {
		t.Fatalf("a free lock: %v %v", held, err)
	}
	held.Release()

	// Held by another instance: the app ends with that error, as before.
	origDelay := tray.LockRetryDelay
	tray.LockRetryDelay = 300 * time.Millisecond
	defer func() { tray.LockRetryDelay = origDelay }()
	other, err := lock.Take(lay.TrayLock())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockAtStart(lay); !errors.Is(err, tray.ErrAlreadyRunning) {
		t.Fatalf("a held lock: %v", err)
	}
	// Held for an instant only — an uninstall's probe before it asks
	// anything: the second look takes it, and the app starts.
	go func() {
		time.Sleep(5 * time.Millisecond)
		other.Release()
	}()
	held, err = lockAtStart(lay)
	if err != nil || held == nil {
		t.Fatalf("a lock probed for an instant: %v %v", held, err)
	}
	held.Release()

	// Any other failure — a directory left at tray.lock — is the
	// controller's to log and try again at every refresh: the app starts.
	if err := os.Remove(lay.TrayLock()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lay.TrayLock(), 0o700); err != nil {
		t.Fatal(err)
	}
	held, err = lockAtStart(lay)
	if err != nil || held != nil {
		t.Fatalf("a lock that fails for another reason: %v %v", held, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "agent.json")); err != nil {
		t.Fatal(err)
	}
}

// A second menu-bar app ends without a failure (#254): its start at login
// keeps it alive only after an unsuccessful exit, so a second instance —
// beside a login item of the person's own — must not be started again
// every ten seconds.
func TestIssue254_ASecondMenuBarAppEndsWithoutAFailure(t *testing.T) {
	dir := t.TempDir()
	lay := layout.Layout{}.At(dir)
	if err := os.WriteFile(lay.State, []byte(`{"version":1,"guiPort":8384}`), 0o600); err != nil {
		t.Fatal(err)
	}
	origDelay := tray.LockRetryDelay
	tray.LockRetryDelay = 10 * time.Millisecond
	defer func() { tray.LockRetryDelay = origDelay }()
	first, err := lock.Take(lay.TrayLock())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	done := make(chan error, 1)
	go func() { done <- run([]string{"--state-dir", dir}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a second instance ended with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a second instance did not end")
	}
}

// The app's run ends without a failure on Quit, on the system ending it,
// and when another instance took the lock while it ran — the start at
// login restarts only a failure (#254); anything else is one.
func TestIssue254_TheRunEndsAsTheStartAtLoginExpects(t *testing.T) {
	quiet := log.New(io.Discard, "", 0)
	for _, err := range []error{nil, context.Canceled, fmt.Errorf("refresh: %w", tray.ErrAlreadyRunning)} {
		if got := endOfRun(err, quiet); got != nil {
			t.Errorf("%v ends as %v", err, got)
		}
	}
	other := errors.New("the menu could not be built")
	if got := endOfRun(other, quiet); got != other {
		t.Errorf("a failure ends as %v", got)
	}
}
