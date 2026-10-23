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

// lockHandleMaking is lockHandle that says whether it made the folder
// (made) — for a taker that undoes its taking on a refusal and must know
// what was its own.
func lockHandleMaking(path string) (h *lock.Handle, made bool, err error) {
	dir := filepath.Dir(path)
	switch err := os.Mkdir(dir, 0o700); {
	case err == nil:
		made = true
	case errors.Is(err, fs.ErrExist):
	default:
		// The parents too: made, then.
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, false, err
		}
		made = true
	}
	h, err = lockFileNoCreate(path)
	if err != nil && made {
		// What this taking made and cannot use goes again: rmdir takes
		// the empty directory alone.
		_ = syscall.Rmdir(dir)
	}
	return h, made, err
}

// lockFileNoCreate is lockHandle without making the folder: for a wait
// on a lock in a folder that must not come back should it go meanwhile
// (a run that was set up, while an uninstall takes the folder apart).
func lockFileNoCreate(path string) (*lock.Handle, error) {
	// A lock taken on a file that is no longer the one at its name — one
	// a removal discarded in the instant between this open and this
	// flock — guards nothing: taken again, at most a few times.
	for attempt := 0; ; attempt++ {
		h, err := lock.Take(path)
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
