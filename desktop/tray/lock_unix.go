//go:build unix

package tray

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/psimaker/vaultsync/desktop/internal/lock"
)

// ErrAlreadyRunning: another menu-bar app of this user holds the lock.
var ErrAlreadyRunning = errors.New("the VaultSync menu-bar app is already running")

// Lock is the one-instance lock: tray.lock in VaultSync's folder.
type Lock struct {
	*lock.Handle
}

// TakeLock makes this the one menu-bar app for this user. A folder that
// does not exist yet (nothing set up) is not created for it — the lock is
// nil, and the caller tries again later (the controller does, at every
// refresh); a folder that cannot be looked at is an error, not a second
// instance.
func TakeLock(stateDir string) (*Lock, error) {
	if _, err := os.Stat(stateDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	h, err := lock.Take(filepath.Join(stateDir, "tray.lock"))
	if errors.Is(err, lock.ErrHeld) {
		return nil, ErrAlreadyRunning
	}
	if errors.Is(err, os.ErrNotExist) {
		// Gone between the look and the lock (an uninstall): nothing to
		// lock, and nothing is made in its place.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &Lock{h}, nil
}
