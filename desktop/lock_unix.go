//go:build unix

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/psimaker/vaultsync/desktop/internal/lock"
	"golang.org/x/sys/unix"
)

// lockFile takes an exclusive, non-blocking lock that the kernel drops when
// the process ends, so a crash never leaves a stale lock behind. One held
// by another process is ErrEngineRunning — the one word every caller
// checks, whichever lock it is.
func lockFile(path string) (func(), error) {
	h, err := lockHandle(path)
	if err != nil {
		return nil, err
	}
	return h.Release, nil
}

// folderToken names a folder by its identity — device and inode — for a
// consent given in a second request (control.go): the same token means
// the same folder, as os.SameFile would say.
func folderToken(fi os.FileInfo) string {
	if fi == nil {
		return ""
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d", st.Dev, st.Ino)
}

// sameDevice reports whether two files live on the same file system.
func sameDevice(a, b os.FileInfo) bool {
	sa, ok1 := a.Sys().(*syscall.Stat_t)
	sb, ok2 := b.Sys().(*syscall.Stat_t)
	return ok1 && ok2 && sa.Dev == sb.Dev
}

// removeDirAt removes the directory named name in the opened parent, and
// only when it is the one checked (same device and inode): the look and
// the act go through the parent's handle, and unlinkat with AT_REMOVEDIR
// takes a directory alone, never a link put in its place.
func removeDirAt(parent *os.File, name string, checked os.FileInfo) error {
	path := filepath.Join(parent.Name(), name)
	var st unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return &os.PathError{Op: "lstat", Path: path, Err: err}
	}
	cs, ok := checked.Sys().(*syscall.Stat_t)
	if !ok || uint64(st.Dev) != uint64(cs.Dev) || st.Ino != cs.Ino {
		return &os.PathError{Op: "remove", Path: path, Err: errNotTheFolderChecked}
	}
	if err := unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR); err != nil {
		return &os.PathError{Op: "remove", Path: path, Err: err}
	}
	return nil
}

// lockHandle is lockFile with the handle: for a taker that may have to
// discard its own file (lock.Handle.Discard).
func lockHandle(path string) (*lock.Handle, error) {
	h, _, err := lockHandleMaking(path)
	return h, err
}

// noUndo is the undo of a taking that made nothing to take away.
func noUndo() {}

// lockHandleMaking is lockHandle with its undo, for a taker that may
// refuse after taking: the undo discards the lock file and takes away the
// directories this taking made, each while empty. When the taking itself
// fails, what it made goes at once — the lock file its open may have made
// in a folder that was not there, and that folder.
func lockHandleMaking(path string) (h *lock.Handle, undo func(), err error) {
	dir := filepath.Dir(path)
	madeFrom, err := mkdirAllFrom(dir)
	if err != nil {
		return nil, noUndo, err
	}
	h, err = lockFileNoCreate(path)
	if err != nil {
		if madeFrom != "" {
			// The folder is this taking's own: a lock file in it is one
			// this taking's open made (a held one would be another
			// taker's, which cannot be — the folder was not there).
			if !errors.Is(err, ErrEngineRunning) {
				_ = os.Remove(path)
			}
			undoMadeDirs(dir, madeFrom)
		}
		return nil, noUndo, err
	}
	return h, func() {
		_, _ = h.Discard()
		if madeFrom != "" {
			undoMadeDirs(dir, madeFrom)
		}
	}, nil
}

// mkdirAllFrom makes dir with its missing parents and says which was the
// topmost it made ("" when dir was there): what an undo takes away again.
func mkdirAllFrom(dir string) (madeFrom string, err error) {
	switch err := os.Mkdir(dir, 0o700); {
	case err == nil:
		return dir, nil
	case errors.Is(err, fs.ErrExist):
		return "", nil
	case !errors.Is(err, fs.ErrNotExist):
		return "", err
	}
	// A parent is missing too: the topmost missing one is what this
	// call makes first.
	top := dir
	for {
		parent := filepath.Dir(top)
		if parent == top {
			break
		}
		if _, err := os.Lstat(parent); err == nil {
			break
		}
		top = parent
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return top, nil
}

// undoMadeDirs removes dir and its parents up to top, each with rmdir —
// a directory alone, and only while it is empty.
func undoMadeDirs(dir, top string) {
	for p := dir; ; p = filepath.Dir(p) {
		if syscall.Rmdir(p) != nil || p == top {
			return
		}
	}
}

// takeLockFile is the one take behind the agent's locks; a test injects
// one whose file is gone again the moment it is taken.
var takeLockFile = lock.Take

// lockFileNoCreate is lockHandle without making the folder: for a wait
// on a lock in a folder that must not come back should it go meanwhile
// (a run that was set up, while an uninstall takes the folder apart).
func lockFileNoCreate(path string) (*lock.Handle, error) {
	// A lock taken on a file that is no longer the one at its name — one
	// a removal discarded in the instant between this open and this
	// flock — guards nothing: taken again, at most a few times.
	for attempt := 0; ; attempt++ {
		h, err := takeLockFile(path)
		if errors.Is(err, lock.ErrHeld) {
			return nil, ErrEngineRunning
		}
		if err != nil {
			return nil, err
		}
		if h.Valid() {
			return h, nil
		}
		h.Release()
		if attempt >= 3 {
			return nil, fmt.Errorf("the lock file %s keeps changing under this process", path)
		}
	}
}

// lockFileWait takes the lock like lockFile, but waits for it instead of
// refusing: for the short, serialized updates of agent.json (updateState).
func lockFileWait(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	h, err := lock.Wait(path)
	if err != nil {
		return nil, err
	}
	return h.Release, nil
}
