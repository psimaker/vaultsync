//go:build unix

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	var logged []string
	logf := func(f string, a ...any) { logged = append(logged, f) }

	// Taken: the handle is the controller's.
	held, err := lockAtStart(lay, logf)
	if err != nil || held == nil {
		t.Fatalf("a free lock: %v %v", held, err)
	}
	held.Release()

	// Held by another instance: the app ends with that error, as before.
	other, err := lock.Take(lay.TrayLock())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockAtStart(lay, logf); !errors.Is(err, tray.ErrAlreadyRunning) {
		t.Fatalf("a held lock: %v", err)
	}
	other.Release()

	// Any other failure — a directory left at tray.lock — is logged, and
	// the controller tries again at every refresh: the app starts.
	if err := os.Remove(lay.TrayLock()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lay.TrayLock(), 0o700); err != nil {
		t.Fatal(err)
	}
	held, err = lockAtStart(lay, logf)
	if err != nil || held != nil {
		t.Fatalf("a lock that fails for another reason: %v %v", held, err)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "trying again") {
		t.Fatalf("logged: %v", logged)
	}
	if _, err := os.Stat(filepath.Join(dir, "agent.json")); err != nil {
		t.Fatal(err)
	}
}
