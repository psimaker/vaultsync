//go:build unix

package tray

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// ErrAlreadyRunning: another menu-bar app of this user holds the lock.
var ErrAlreadyRunning = errors.New("the VaultSync menu-bar app is already running")

// Lock makes this the one menu-bar app for this user: an exclusive lock
// on tray.lock in VaultSync's folder, which the kernel drops when the
// process ends. A folder that does not exist yet (nothing set up) is not
// created for it — the lock is skipped, and the menu says what to do; a
// folder that cannot be looked at is an error, not a second instance.
func Lock(stateDir string) (release func(), err error) {
	if _, err := os.Stat(stateDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return func() {}, nil
		}
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(stateDir, "tray.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrAlreadyRunning
		}
		return nil, err
	}
	return func() { f.Close() }, nil
}
