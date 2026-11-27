//go:build darwin || ios || linux

package bridge

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// openNoBlock opens a file for the conflict functions without waiting on it.
const openNoBlock = syscall.O_NONBLOCK

// clearNoBlock puts a file opened with openNoBlock back into blocking mode,
// for reads as usual.
func clearNoBlock(file *os.File) error {
	conn, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var setErr error
	if err := conn.Control(func(fd uintptr) {
		setErr = unix.SetNonblock(int(fd), false)
	}); err != nil {
		return err
	}
	return setErr
}

// removeFileIn removes the name in dir. It removes a file and never a
// directory (unlinkat without AT_REMOVEDIR).
func removeFileIn(dir *os.Root, name string) error {
	return withDirectoryHandle(dir, func(fd int) error {
		return unix.Unlinkat(fd, name, 0)
	})
}

// readDirectoryIn reads the entries of dir with the types the directory read
// gives, without looking at each entry again.
func readDirectoryIn(dir *os.Root) ([]os.DirEntry, error) {
	var entries []os.DirEntry
	err := withDirectoryHandle(dir, func(fd int) error {
		listed, err := unix.Openat(fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		file := os.NewFile(uintptr(listed), ".")
		defer file.Close()
		entries, err = file.ReadDir(-1)
		return err
	})
	return entries, err
}
