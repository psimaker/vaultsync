//go:build unix

package tray

import (
	"errors"
	"os"
	"time"

	"github.com/psimaker/vaultsync/desktop/internal/layout"
	"github.com/psimaker/vaultsync/desktop/internal/lock"
)

// TakeLock makes this the one menu-bar app for this user: tray.lock in
// VaultSync's folder. It is taken only while VaultSync is set up there
// (agent.json present): before setup, and while an uninstall takes the
// folder apart, nothing is made in it — the handle is nil, and the caller
// tries again later (the controller does, at every refresh); a folder
// that cannot be looked at is an error, not a second instance.
func TakeLock(lay layout.Layout) (LockHandle, error) {
	h, err := takeLock(lay, lock.Take)
	if errors.Is(err, ErrAlreadyRunning) {
		// Held for an instant by an uninstall's probe, or for good by
		// another instance: one more look after a moment tells which.
		time.Sleep(LockRetryDelay)
		h, err = takeLock(lay, lock.Take)
	}
	return h, err
}

// takeLock is TakeLock with the lock's taking injected: a test removes
// agent.json between the look and the lock.
func takeLock(lay layout.Layout, take func(string) (*lock.Handle, error)) (LockHandle, error) {
	if _, err := os.Stat(lay.State); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	path := lay.TrayLock()
	h, err := take(path)
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
	if _, err := os.Stat(lay.State); err != nil {
		// agent.json went between the look and the lock: an uninstall is
		// taking the folder apart, and the file this lock made would be
		// left in it — discarded, nothing of this app stays.
		_, _ = h.Discard()
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return h, nil
}
