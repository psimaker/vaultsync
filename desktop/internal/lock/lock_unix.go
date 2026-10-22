//go:build unix

package lock

import (
	"errors"
	"os"
	"path/filepath"
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
// A link in the lock's place is not followed (nothing outside the folder
// is opened, locked or made): the error comes back as it is.
func open(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
}

// openIn opens name inside root. os.Root follows a link that stays inside
// the root even with O_NOFOLLOW (it resolves the link within the root),
// so a link in the lock's place is refused here first, as open refuses
// it: ELOOP, nothing opened, locked or made through it.
func openIn(root *os.Root, name string, flag int) (*os.File, error) {
	if fi, err := root.Lstat(name); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return nil, &os.PathError{Op: "open", Path: filepath.Join(root.Name(), name), Err: syscall.ELOOP}
	}
	return root.OpenFile(name, flag|syscall.O_NOFOLLOW, 0o600)
}

// takeOpened takes the lock on an opened file without waiting: ErrHeld
// when another process has it.
func takeOpened(f *os.File, path string) (*Handle, error) {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrHeld
		}
		return nil, err
	}
	return &Handle{f: f, path: path}, nil
}

// probeOpened tells whether another process holds the lock on an opened
// file, taking it for an instant to tell.
func probeOpened(f *os.File) (held bool, err error) {
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return true, nil
		}
		return false, err
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false, nil
}

// Take takes the lock at path without waiting: ErrHeld when another
// process has it.
func Take(path string) (*Handle, error) {
	f, err := open(path)
	if err != nil {
		return nil, err
	}
	return takeOpened(f, path)
}

// TakeIn takes the lock named name inside root, without waiting: the file
// is opened through the root (never through a link swapped in above it),
// and made there if missing. ErrHeld when another process has it.
func TakeIn(root *os.Root, name string) (*Handle, error) {
	f, err := openIn(root, name, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return nil, err
	}
	return takeOpened(f, filepath.Join(root.Name(), name))
}

// Probe tells whether another process holds the lock at path, making
// nothing: a missing file is a lock nobody holds. It takes the lock for
// an instant to tell, so a holder-to-be racing it may see it held.
func Probe(path string) (held bool, err error) {
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return probeOpened(f)
}

// ProbeIn is Probe for a lock named name inside root.
func ProbeIn(root *os.Root, name string) (held bool, err error) {
	f, err := openIn(root, name, os.O_RDWR)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return probeOpened(f)
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
