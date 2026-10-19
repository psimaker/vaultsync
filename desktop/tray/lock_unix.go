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

// Lock is the one-instance lock: tray.lock in VaultSync's folder, held
// through an exclusive flock the kernel drops when the process ends.
type Lock struct {
	f    *os.File
	path string
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
	path := filepath.Join(stateDir, "tray.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
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
	return &Lock{f: f, path: path}, nil
}

// Valid: the file this lock holds is still the one at its place. An
// uninstall --remove-data takes the file away while the lock is held (the
// lock then guards an orphan nobody else can see), so a lock that is no
// longer valid is released and taken again.
func (l *Lock) Valid() bool {
	held, err := l.f.Stat()
	if err != nil {
		return false
	}
	at, err := os.Stat(l.path)
	if err != nil {
		return false
	}
	return os.SameFile(held, at)
}

// Release lets the lock go.
func (l *Lock) Release() { _ = l.f.Close() }
