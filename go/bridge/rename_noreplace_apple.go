//go:build darwin || ios

package bridge

import (
	"errors"

	"golang.org/x/sys/unix"
)

func renameNoReplace(oldPath, newPath string) error {
	err := unix.RenamexNp(oldPath, newPath, unix.RENAME_EXCL)
	if errors.Is(err, unix.ENOTSUP) {
		return errNoReplaceUnsupported
	}
	return err
}
