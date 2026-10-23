//go:build unix

package tray

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/psimaker/vaultsync/desktop/internal/layout"
	"github.com/psimaker/vaultsync/desktop/internal/lock"
)

func TestIssue176_TrayLeavesNothingInAFolderBeingRemoved(t *testing.T) {
	// uninstall --remove-data unlinks agent.json, then tray.lock, then the
	// folder. An app that looked at agent.json just before and takes the
	// lock just after would make a new tray.lock in the half-removed
	// folder: it notices, removes its own file and holds nothing.
	dir := t.TempDir()
	lay := layout.Layout{}.At(dir)
	if err := os.WriteFile(lay.State, []byte(`{"version":1,"guiPort":8384}`), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := takeLock(lay, func(path string) (*lock.Handle, error) {
		if err := os.Remove(lay.State); err != nil {
			t.Fatal(err)
		}
		return lock.Take(path)
	})
	if err != nil || l != nil {
		t.Fatalf("a lock in a folder being removed: %v %v", l, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tray.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the lock file was left behind: %v", err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatalf("the folder could not go: %v", err)
	}
}

// A lock's validity is read with a look that follows no link: a link put
// at the lock's name — even one pointing at the very file this lock holds
// — means the name no longer holds the file, and the app takes again.
func TestIssue176_TrayLockValidityFollowsNoLink(t *testing.T) {
	dir := t.TempDir()
	lay := layout.Layout{}.At(dir)
	if err := os.WriteFile(lay.State, []byte(`{"version":1,"guiPort":8384}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := TakeLock(lay)
	if err != nil || h == nil {
		t.Fatalf("taking: %v %v", h, err)
	}
	defer h.Release()
	if !h.Valid() {
		t.Fatal("a fresh lock is not valid")
	}
	real := lay.TrayLock() + ".real"
	if err := os.Rename(lay.TrayLock(), real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, lay.TrayLock()); err != nil {
		t.Fatal(err)
	}
	if h.Valid() {
		t.Fatal("a link at the lock's name, pointing at the held file, read as the file itself")
	}
}
