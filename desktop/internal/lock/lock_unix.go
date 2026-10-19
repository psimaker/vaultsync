//go:build unix

package lock

import (
	"errors"
	"os"
	"syscall"
)

// ErrHeld: another process holds the lock.
var ErrHeld = errors.New("the lock is held by another process")

// Handle is a held lock: a file under an exclusive flock, which the kernel
// drops when the process ends, so a crash never leaves a stale lock.
type Handle struct {
	f    *os.File
	path string
}

// open makes the lock file, never its folder: a caller that wants the
// folder made says so itself, and one that must not (the menu-bar app,
// which never creates VaultSync's folder) gets an error when it is gone.
func open(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
}

// Take takes the lock at path without waiting: ErrHeld when another
// process has it.
func Take(path string) (*Handle, error) {
	f, err := open(path)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrHeld
		}
		return nil, err
	}
	return &Handle{f: f, path: path}, nil
}

// Wait takes the lock at path, waiting for it: for short, serialized
// updates.
func Wait(path string) (*Handle, error) {
	f, err := open(path)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return &Handle{f: f, path: path}, nil
}

// Valid: the file this lock holds is still the one at its place. A file
// taken away under a held lock (a folder removed) leaves the lock on an
// orphan nobody else can see; a caller that must stay the one instance
// takes the lock again then.
func (h *Handle) Valid() bool {
	held, err := h.f.Stat()
	if err != nil {
		return false
	}
	at, err := os.Stat(h.path)
	if err != nil {
		return false
	}
	return os.SameFile(held, at)
}

// Release lets the lock go.
func (h *Handle) Release() { _ = h.f.Close() }
