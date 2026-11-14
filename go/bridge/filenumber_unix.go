//go:build unix

package bridge

import (
	"os"
	"syscall"
)

// fileNumber returns the number the file system knows the entry at path by
// (its inode). The entry itself is asked, not what a link at path leads to:
// a link in a marker's place is not the marker.
func fileNumber(path string) (uint64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Ino == 0 {
		return 0, errNoFileNumbers
	}
	return uint64(stat.Ino), nil
}
