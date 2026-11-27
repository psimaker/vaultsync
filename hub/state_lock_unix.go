//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// stateLockWait bounds the wait for the state lock. The lock is held for one
// small file write, so a longer wait means a writer that hangs; the write then
// fails rather than go on without the lock (decision 097).
var stateLockWait = 5 * time.Second

// flock is flock(2), a package var so tests can stand in for storage that has
// no file locks.
var flock = syscall.Flock

// geteuid and getegid are the effective user and group, package vars so tests
// can stand in for root or for another user.
var (
	geteuid = os.Geteuid
	getegid = os.Getegid
)

// lockStateFile takes the lock that every read-modify-write of the state file
// holds, in whichever process: flock(2) on a lock file beside it. The state
// file cannot carry the lock itself, because each write replaces it by a
// rename. It returns the release.
func lockStateFile(statePath string) (func(), error) {
	lockPath := statePath + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("the Hub's state lock %s cannot be made (%v); nothing was changed", lockPath, err)
	}
	want, err := stateOwnerWanted(statePath)
	if err != nil {
		return nil, err
	}
	f, err := openStateLock(lockPath, want)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(stateLockWait)
	for {
		err := flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EOPNOTSUPP) || errors.Is(err, syscall.ENOLCK) || errors.Is(err, syscall.EINVAL) {
				return nil, fmt.Errorf("the Hub's state is on storage that does not support the file lock the Hub needs to keep it whole (flock on %s: %v), so nothing was changed. Keep the Hub's folder on a local disk, or mount its storage with lock support (for NFS, with its lock service running)", lockPath, err)
			}
			return nil, fmt.Errorf("the Hub's state lock %s: %v; nothing was changed", lockPath, err)
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("the Hub's state is busy: its lock %s has been held for %s, by another vaultsync-hub or another program on this host; nothing was changed", lockPath, stateLockWait)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// openStateLock opens the lock file for reading and writing (an exclusive
// lock on NFS needs a descriptor open for writing), never through a link,
// and only if it is a regular file. A new one is made 0600 for the state's
// owner; a stray one with another mode or, under root, another owner is put
// right on the descriptor, so nothing is changed at any other path. A writer
// that is neither root nor the lock's owner is refused like one for the
// state file (decideOwner).
func openStateLock(lockPath string, want ownerOf) (*os.File, error) {
	flags := os.O_RDWR | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	f, err := os.OpenFile(lockPath, flags, 0)
	if errors.Is(err, os.ErrNotExist) {
		f, err = os.OpenFile(lockPath, flags|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			// Made by another writer a moment ago.
			f, err = os.OpenFile(lockPath, flags, 0)
		}
	}
	if err != nil {
		switch {
		case errors.Is(err, syscall.EISDIR):
			return nil, fmt.Errorf("the Hub's state lock %s is not a regular file; nothing was changed. Remove it", lockPath)
		case errors.Is(err, syscall.ELOOP):
			return nil, fmt.Errorf("the Hub's state lock %s is a link, which the Hub never follows; nothing was changed. Remove the link", lockPath)
		case errors.Is(err, os.ErrPermission):
			return nil, fmt.Errorf("the Hub's state lock %s cannot be opened by this user (%v); nothing was changed. %s", lockPath, err, handBack)
		}
		return nil, fmt.Errorf("the Hub's state lock %s cannot be opened (%v); nothing was changed", lockPath, err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("the Hub's state lock %s: %v; nothing was changed", lockPath, err)
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("the Hub's state lock %s is not a regular file; nothing was changed. Remove it", lockPath)
	}
	have := ownerOf{uid: -1, gid: -1}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		have = ownerOf{uid: int(st.Uid), gid: int(st.Gid)}
	}
	owner, err := decideOwner(lockPath, geteuid(), have, want)
	if err != nil {
		f.Close()
		return nil, err
	}
	if owner.change {
		if err := f.Chown(owner.uid, owner.gid); err != nil {
			f.Close()
			return nil, fmt.Errorf("the Hub's state lock %s cannot be given to uid %d (%v); nothing was changed", lockPath, owner.uid, err)
		}
	}
	if fi.Mode().Perm() != 0o600 {
		if err := f.Chmod(0o600); err != nil {
			f.Close()
			return nil, fmt.Errorf("the Hub's state lock %s cannot be made private (%v); nothing was changed", lockPath, err)
		}
	}
	return f, nil
}

// stateOwnerWanted reads the state file's and its folder's owners and returns
// who both files must belong to (stateTarget).
func stateOwnerWanted(statePath string) (ownerOf, error) {
	var file, dir *ownerOf
	fi, err := os.Stat(statePath)
	switch {
	case err == nil:
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			file = &ownerOf{uid: int(st.Uid), gid: int(st.Gid)}
		}
	case !errors.Is(err, os.ErrNotExist):
		return ownerOf{}, err
	}
	if di, err := os.Stat(filepath.Dir(statePath)); err == nil {
		if st, ok := di.Sys().(*syscall.Stat_t); ok {
			dir = &ownerOf{uid: int(st.Uid), gid: int(st.Gid)}
		}
	}
	return stateTarget(geteuid(), file, dir), nil
}

// stateFileOwner decides who a write leaves the new state file to: it is made
// by the writer and must belong to stateTarget's user.
func stateFileOwner(statePath string) (fileOwner, error) {
	want, err := stateOwnerWanted(statePath)
	if err != nil {
		return fileOwner{}, err
	}
	return decideOwner(statePath, geteuid(), ownerOf{uid: geteuid(), gid: getegid()}, want)
}
