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
// fails rather than go on without the lock (#490).
var stateLockWait = 5 * time.Second

// flock is flock(2), a package var so tests can stand in for a filesystem
// that has no file locks.
var flock = syscall.Flock

// geteuid is the effective user, a package var so tests can stand in for
// root or for another user.
var geteuid = os.Geteuid

// lockStateFile takes the lock that every read-modify-write of the state file
// holds, in whichever process: flock(2) on a lock file beside it. The state
// file cannot carry the lock itself, because each write replaces it by a
// rename. It returns the release.
func lockStateFile(statePath string) (func(), error) {
	lockPath := statePath + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("the Hub's state lock %s cannot be made (%v); nothing was changed", lockPath, err)
	}
	f, err := openStateLock(lockPath)
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
				return nil, fmt.Errorf("the folder of the Hub's state file does not support file locks (flock on %s: %v), which the Hub needs to keep its state whole; nothing was changed", lockPath, err)
			}
			return nil, fmt.Errorf("the Hub's state lock %s: %v; nothing was changed", lockPath, err)
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("the Hub's state file is busy: another vaultsync-hub has held its lock %s for %s; nothing was changed", lockPath, stateLockWait)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// openStateLock opens the lock file read-only: flock needs no write access, so
// a lock file another user made — root, through `docker compose exec -u root` —
// stays usable by the server. A new one is made 0644 past the umask: it is
// empty, and everyone who may write the state must be able to open it.
func openStateLock(lockPath string) (*os.File, error) {
	f, err := os.OpenFile(lockPath, os.O_RDONLY, 0)
	if errors.Is(err, os.ErrNotExist) {
		f, err = os.OpenFile(lockPath, os.O_RDONLY|os.O_CREATE, 0o644)
		if err == nil {
			// Fails harmlessly when another process made it a moment ago.
			_ = f.Chmod(0o644)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("the Hub's state lock %s cannot be opened (%v); nothing was changed", lockPath, err)
	}
	return f, nil
}

// stateFileOwner reads the state file's and its folder's owners and lets
// decideStateOwner decide.
func stateFileOwner(statePath string) (fileOwner, error) {
	var file, dir *ownerOf
	fi, err := os.Stat(statePath)
	switch {
	case err == nil:
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			file = &ownerOf{uid: int(st.Uid), gid: int(st.Gid)}
		}
	case !errors.Is(err, os.ErrNotExist):
		return fileOwner{}, err
	}
	if di, err := os.Stat(filepath.Dir(statePath)); err == nil {
		if st, ok := di.Sys().(*syscall.Stat_t); ok {
			dir = &ownerOf{uid: int(st.Uid), gid: int(st.Gid)}
		}
	}
	if fi != nil && file == nil {
		// No owner to read: leave the file to its writer.
		return fileOwner{}, nil
	}
	return decideStateOwner(statePath, geteuid(), file, dir)
}
