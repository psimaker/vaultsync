//go:build unix

package bridge

import (
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
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

// markerNumbers reads the file number of the directory that holds a
// folder's marker (`.stfolder`) and the marker's, through one open handle of
// that directory: the marker's number is always one read inside the
// directory whose number comes with it (#329, decision 084).
//
// The directory is opened without following a symbolic link in its place:
// the engine makes `.stfolder` as a directory and never syncs it, so a link
// there was put by hand, and what it leads to is not the directory the
// folder synced in. Opening one fails with ELOOP, a file in its place with
// ENOTDIR. The marker is read without following a link either.
func markerNumbers(markerPath string) (markerReading, error) {
	dirPath := filepath.Dir(markerPath)
	fd, err := unix.Open(dirPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return markerReading{}, &os.PathError{Op: "open", Path: dirPath, Err: err}
	}
	defer unix.Close(fd)

	var dir unix.Stat_t
	if err := unix.Fstat(fd, &dir); err != nil {
		return markerReading{}, &os.PathError{Op: "fstat", Path: dirPath, Err: err}
	}
	if dir.Ino == 0 {
		return markerReading{}, errNoFileNumbers
	}
	reading := markerReading{dir: uint64(dir.Ino)}

	var marker unix.Stat_t
	switch err := unix.Fstatat(fd, filepath.Base(markerPath), &marker, unix.AT_SYMLINK_NOFOLLOW); {
	case err != nil:
		reading.markerErr = &os.PathError{Op: "fstatat", Path: markerPath, Err: err}
	case marker.Ino == 0:
		reading.markerErr = errNoFileNumbers
	default:
		reading.marker = uint64(marker.Ino)
	}
	return reading, nil
}
