//go:build unix

package tray

import (
	"errors"
	"os"

	"github.com/psimaker/vaultsync/desktop/internal/layout"
	"github.com/psimaker/vaultsync/desktop/internal/lock"
)

// ErrAlreadyRunning: another menu-bar app of this user holds the lock.
var ErrAlreadyRunning = errors.New("the VaultSync menu-bar app is already running (or VaultSync is being removed right now)")

// TakeLock makes this the one menu-bar app for this user: tray.lock in
// VaultSync's folder. It is taken only while VaultSync is set up there
// (agent.json present): before setup, and while an uninstall takes the
// folder apart, nothing is made in it — the handle is nil, and the caller
// tries again later (the controller does, at every refresh); a folder
// that cannot be looked at is an error, not a second instance.
func TakeLock(lay layout.Layout) (LockHandle, error) {
	if _, err := os.Stat(lay.State); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	h, err := lock.Take(lay.TrayLock())
	if errors.Is(err, lock.ErrHeld) {
		return nil, ErrAlreadyRunning
	}
	if errors.Is(err, os.ErrNotExist) {
		// The folder went between the look and the lock (an uninstall):
		// nothing to lock, and nothing is made in its place.
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return h, nil
}
