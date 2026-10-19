//go:build unix

package main

import (
	"os"
	"strconv"
	"syscall"
)

// socketFallbackDir is a folder of the user's own, short enough for a
// socket address, for an agent whose folder lies too deep for one: the
// session's runtime directory on Linux (XDG_RUNTIME_DIR, else
// /run/user/<uid>), the per-user temporary directory on macOS (TMPDIR) —
// and only one that is the user's and nobody else's: owned by them, mode
// 0700, not a link. A shared /tmp is never used: another account could
// prepare the place there, or take it over once the agent is gone. Empty
// when there is none.
func socketFallbackDir(goos string, getenv func(string) string, uid int, lstat func(string) (os.FileInfo, error)) string {
	var candidates []string
	switch goos {
	case "linux":
		candidates = []string{getenv("XDG_RUNTIME_DIR"), "/run/user/" + strconv.Itoa(uid)}
	case "darwin":
		candidates = []string{getenv("TMPDIR")}
	}
	for _, dir := range candidates {
		if dir == "" {
			continue
		}
		fi, err := lstat(dir)
		if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 || fi.Mode().Perm() != 0o700 {
			continue
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != uid {
			continue
		}
		return dir
	}
	return ""
}
