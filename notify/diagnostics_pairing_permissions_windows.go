//go:build windows

package main

import (
	"io"
	"io/fs"
	"path/filepath"
)

// Current Windows packages run with broad per-user access and do not yet prove
// a diagnostics-specific DACL. Fail closed instead of claiming POSIX modes are
// equivalent to an audited Windows ACL.
func checkDiagnosticsPrivateDirectory(_ string, _ fs.FileInfo) error {
	return errDiagnosticsCredentialStateInvalid
}

func checkDiagnosticsPrivateFile(_ string, _ fs.FileInfo) error {
	return errDiagnosticsCredentialStateInvalid
}

func withDiagnosticsCredentialFileLock(path string, action func() error) error {
	_ = filepath.Clean(path)
	_ = action
	return errDiagnosticsCredentialStateInvalid
}

func diagnosticsCryptoRandomReader() io.Reader {
	return nil
}
