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

// ErrChanged: the file at the lock's name changed while it was being
// opened — swapped for another file in between — so what was opened is
// not what the name holds.
var ErrChanged = errors.New("the file at that name changed while it was being opened")

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
	loop := &os.PathError{Op: "open", Path: filepath.Join(root.Name(), name), Err: syscall.ELOOP}
	create := flag&os.O_CREATE != 0
	flag &^= os.O_CREATE
	for attempt := 0; ; attempt++ {
		if create {
			// Made new, never through a link: with O_EXCL no link is
			// followed and nothing is made at its target. A file that is
			// there is opened below, as what it is.
			f, err := root.OpenFile(name, flag|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
			if err == nil {
				return f, nil
			}
			if !errors.Is(err, os.ErrExist) {
				return nil, err
			}
		}
		fi, err := root.Lstat(name)
		if err != nil {
			if create && errors.Is(err, os.ErrNotExist) && attempt < 3 {
				// There a moment ago, gone again: made new after all.
				continue
			}
			return nil, err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, loop
		}
		f, err := root.OpenFile(name, flag|syscall.O_NOFOLLOW, 0o600)
		if err != nil {
			if create && errors.Is(err, os.ErrNotExist) && attempt < 3 {
				continue
			}
			return nil, err
		}
		f, err = checkOpened(root, name, f, loop)
		if err != nil && create && errors.Is(err, os.ErrNotExist) && attempt < 3 {
			continue
		}
		return f, err
	}
}

// checkOpened: the opened file is the one the name holds now — a link
// swapped in between the look and the open would have been followed, and
// any other file swapped in is ErrChanged. A name that holds nothing any
// more is a file gone, not a link.
func checkOpened(root *os.Root, name string, f *os.File, loop error) (*os.File, error) {
	opened, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	at, err := root.Lstat(name)
	if err != nil {
		f.Close()
		return nil, err
	}
	if !os.SameFile(opened, at) {
		f.Close()
		return nil, &os.PathError{Op: "open", Path: filepath.Join(root.Name(), name), Err: ErrChanged}
	}
	return f, nil
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

// TakeExistingIn is TakeIn for a file that is there: nothing is made,
// a missing file is os.ErrNotExist.
func TakeExistingIn(root *os.Root, name string) (*Handle, error) {
	f, err := openIn(root, name, os.O_RDWR)
	if err != nil {
		return nil, err
	}
	return takeOpened(f, filepath.Join(root.Name(), name))
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
	at, err := os.Lstat(h.path)
	if err != nil {
		return false
	}
	return os.SameFile(held, at)
}

// Release lets the lock go.
func (h *Handle) Release() { _ = h.f.Close() }

// Discard removes the lock file when it is still this handle's — while
// the flock is held nobody else has that file — then lets the lock go.
// For a lock taken in a folder that is being taken apart: nothing of the
// taker is left in it. removed says whether this call unlinked the file.
func (h *Handle) Discard() (removed bool, err error) {
	if h.Valid() {
		err = os.Remove(h.path)
		removed = err == nil
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
	}
	h.Release()
	return removed, err
}

// DiscardIn is Discard for a lock taken inside root (TakeIn,
// TakeExistingIn): the look and the unlink go through the root, never by
// a path something else may stand at by now. removed says whether this
// call unlinked the file — false when the name no longer held it.
func (h *Handle) DiscardIn(root *os.Root, name string) (removed bool, err error) {
	held, serr := h.f.Stat()
	at, aerr := root.Lstat(name)
	if serr == nil && aerr == nil && os.SameFile(held, at) {
		err = root.Remove(name)
		removed = err == nil
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
	}
	h.Release()
	return removed, err
}
