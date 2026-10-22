//go:build unix

package main

import (
	"errors"
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
	lockRetryDelay = 20 * time.Millisecond
	defer func() { lockRetryDelay = 500 * time.Millisecond }()
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
