//go:build unix

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/psimaker/vaultsync/desktop/internal/lock"
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

// lockHandle is lockFile with the handle: for a taker that may have to
// discard its own file (lock.Handle.Discard).
func lockHandle(path string) (*lock.Handle, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return lockFileNoCreate(path)
}

// lockFileNoCreate is lockHandle without making the folder: for a wait
// on a lock in a folder that must not come back should it go meanwhile
// (a run that was set up, while an uninstall takes the folder apart).
func lockFileNoCreate(path string) (*lock.Handle, error) {
	h, err := lock.Take(path)
	if errors.Is(err, lock.ErrHeld) {
		return nil, ErrEngineRunning
	}
	return h, err
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
