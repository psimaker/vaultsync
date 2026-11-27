//go:build !darwin && !ios && !linux

package bridge

import (
	"io/fs"
	"os"
)

const openNoBlock = 0

func clearNoBlock(*os.File) error {
	return nil
}

func removeFileIn(dir *os.Root, name string) error {
	return dir.Remove(name)
}

func readDirectoryIn(dir *os.Root) ([]os.DirEntry, error) {
	return fs.ReadDir(dir.FS(), ".")
}
