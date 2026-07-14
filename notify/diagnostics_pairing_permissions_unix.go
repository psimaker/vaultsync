//go:build unix

package main

import (
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"os"
	"syscall"
)

func checkDiagnosticsPrivateDirectory(_ string, info fs.FileInfo) error {
	if info.Mode().Perm() != 0o700 || !diagnosticsOwnedByCurrentUser(info) {
		return errDiagnosticsCredentialStateInvalid
	}
	return nil
}

func checkDiagnosticsPrivateFile(_ string, info fs.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if info.Mode().Perm() != 0o600 || !diagnosticsOwnedByCurrentUser(info) || !ok || stat.Nlink != 1 {
		return errDiagnosticsCredentialStateInvalid
	}
	return nil
}

func checkDiagnosticsReadOnlyConfigFile(_ string, info fs.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if info.Mode().Perm() != 0o400 || !diagnosticsOwnedByCurrentUser(info) || !ok || stat.Nlink != 1 {
		return errDiagnosticsCredentialStateInvalid
	}
	return nil
}

func diagnosticsOwnedByCurrentUser(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func withDiagnosticsCredentialFileLock(path string, action func() error) error {
	file, err := openDiagnosticsCredentialLock(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }()
	if err := validateDiagnosticsCredentialLock(path, file); err != nil {
		return err
	}
	return action()
}

func openDiagnosticsCredentialLock(path string) (*os.File, error) {
	for range 3 {
		pathInfo, err := os.Lstat(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			file, openErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
			if errors.Is(openErr, os.ErrExist) {
				continue
			}
			if openErr != nil {
				return nil, openErr
			}
			if validateDiagnosticsCredentialLock(path, file) != nil {
				_ = file.Close()
				return nil, errDiagnosticsCredentialStateInvalid
			}
			return file, nil
		case err != nil:
			return nil, err
		case !pathInfo.Mode().IsRegular():
			return nil, errDiagnosticsCredentialStateInvalid
		}

		file, openErr := os.OpenFile(path, os.O_RDWR, 0)
		if errors.Is(openErr, os.ErrNotExist) {
			continue
		}
		if openErr != nil {
			return nil, openErr
		}
		info, statErr := file.Stat()
		if statErr != nil || !os.SameFile(pathInfo, info) || validateDiagnosticsCredentialLock(path, file) != nil {
			_ = file.Close()
			return nil, errDiagnosticsCredentialStateInvalid
		}
		return file, nil
	}
	return nil, errDiagnosticsCredentialStateInvalid
}

func validateDiagnosticsCredentialLock(path string, file *os.File) error {
	info, err := file.Stat()
	if err != nil || checkDiagnosticsPrivateFile(path, info) != nil {
		return errDiagnosticsCredentialStateInvalid
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(pathInfo, info) {
		return errDiagnosticsCredentialStateInvalid
	}
	return nil
}

func diagnosticsCryptoRandomReader() io.Reader {
	return rand.Reader
}
