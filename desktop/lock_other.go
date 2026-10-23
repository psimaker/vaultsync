//go:build !unix

package main

import (
	"errors"
	"os"

	"github.com/psimaker/vaultsync/desktop/internal/lock"
)

func lockFile(string) (func(), error) {
	return nil, errors.New("the background engine runs on macOS and Linux only for now")
}

func removeDirAt(*os.File, string, os.FileInfo) error {
	return errors.New("the background engine runs on macOS and Linux only for now")
}

func lockHandleMaking(string) (*lock.Handle, bool, error) {
	return nil, false, errors.New("the background engine runs on macOS and Linux only for now")
}

func lockHandle(string) (*lock.Handle, error) {
	return nil, errors.New("the background engine runs on macOS and Linux only for now")
}

func lockFileNoCreate(string) (*lock.Handle, error) {
	return nil, errors.New("the background engine runs on macOS and Linux only for now")
}

func lockFileWait(string) (func(), error) {
	return nil, errors.New("the background engine runs on macOS and Linux only for now")
}

func sameDevice(os.FileInfo, os.FileInfo) bool { return false }

func folderToken(os.FileInfo) string { return "" }
