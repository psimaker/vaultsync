//go:build darwin || ios

package bridge

import (
	"errors"

	"golang.org/x/sys/unix"
)

// renameNoReplaceAt renames oldName to newName, both looked up in the
// directory dirfd, and fails with EEXIST when newName is taken.
func renameNoReplaceAt(dirfd int, oldName, newName string) error {
	err := unix.RenameatxNp(dirfd, oldName, dirfd, newName, unix.RENAME_EXCL)
	if errors.Is(err, unix.ENOTSUP) {
		return errNoReplaceUnsupported
	}
	return err
}
