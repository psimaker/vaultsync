//go:build !unix

package main

import (
	"errors"
	"os"
)

func lockFile(string) (func(), error) {
	return nil, errors.New("the background engine runs on macOS and Linux only for now")
}

func sameDevice(os.FileInfo, os.FileInfo) bool { return false }
