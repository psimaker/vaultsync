//go:build linux

package bridge

import (
	"errors"

	"golang.org/x/sys/unix"
)

// renameNoReplaceAt renames oldName to newName, both looked up in the
// directory dirfd, and fails with EEXIST when newName is taken.
func renameNoReplaceAt(dirfd int, oldName, newName string) error {
	err := unix.Renameat2(dirfd, oldName, dirfd, newName, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
		return errNoReplaceUnsupported
	}
	return err
}
