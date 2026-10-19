//go:build unix

package main

import (
	"errors"
	"os"
	"syscall"

	"github.com/psimaker/vaultsync/desktop/internal/lock"
)

// lockFile takes an exclusive, non-blocking lock that the kernel drops when
// the process ends, so a crash never leaves a stale lock behind. One held
// by another process is ErrEngineRunning — the one word every caller
// checks, whichever lock it is.
func lockFile(path string) (func(), error) {
	h, err := lock.Take(path)
	if errors.Is(err, lock.ErrHeld) {
		return nil, ErrEngineRunning
	}
	if err != nil {
		return nil, err
	}
	return h.Release, nil
}

// sameDevice reports whether two files live on the same file system.
func sameDevice(a, b os.FileInfo) bool {
	sa, ok1 := a.Sys().(*syscall.Stat_t)
	sb, ok2 := b.Sys().(*syscall.Stat_t)
	return ok1 && ok2 && sa.Dev == sb.Dev
}

// lockFileWait takes the lock like lockFile, but waits for it instead of
// refusing: for the short, serialized updates of agent.json (updateState).
func lockFileWait(path string) (func(), error) {
	h, err := lock.Wait(path)
	if err != nil {
		return nil, err
	}
	return h.Release, nil
}
