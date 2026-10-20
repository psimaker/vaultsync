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
