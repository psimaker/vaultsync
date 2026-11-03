//go:build unix

package main

import (
	"os"
	"syscall"
)

// ownedByMe: the file or folder at p belongs to this account — its owner,
// not whether it may be changed right now (a bundle locked in the Finder is
// still its owner's). The setup link decides "this account's app" the same
// way.
func ownedByMe(p string) bool {
	fi, err := os.Lstat(p)
	if err != nil {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
