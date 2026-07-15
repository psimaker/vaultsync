package bridge

import (
	"path/filepath"
	"strings"

	stfs "github.com/syncthing/syncthing/lib/fs"
	"github.com/syncthing/syncthing/lib/ignore"
)

const diagnosticsNamespaceRoot = "VaultSync Diagnostics"

// DiagnosticsUploadPathAvailable performs the app-side filesystem and ignore
// preflight for one exact D024 upload request. It does not create directories,
// alter ignores, rescan, or otherwise mutate Syncthing configuration.
//
// The two components are opaque lowercase base32 encodings. Keeping the path
// construction here fixed prevents a caller-controlled relative path from
// crossing the gomobile boundary.
func DiagnosticsUploadPathAvailable(folderID, installationComponent, operationComponent string) bool {
	return diagnosticsUploadPathVerdict(folderID, installationComponent, operationComponent, true)
}

// DiagnosticsUploadPathAllowed rechecks the same fixed directories and real
// ignore matcher after the app has exclusively created its request. Existing
// operation artifacts are expected at that point and are verified separately
// by their canonical signed bytes.
func DiagnosticsUploadPathAllowed(folderID, installationComponent, operationComponent string) bool {
	return diagnosticsUploadPathVerdict(folderID, installationComponent, operationComponent, false)
}

func diagnosticsUploadPathVerdict(
	folderID, installationComponent, operationComponent string,
	requireEmpty bool,
) bool {
	folders := getFolderConfigs()
	if folders == nil {
		return false
	}
	folder, ok := folders[folderID]
	if !ok || !diagnosticsComponentValid(installationComponent) ||
		!diagnosticsComponentValid(operationComponent) {
		return false
	}
	return diagnosticsUploadPathAvailable(
		folder.Filesystem(),
		installationComponent,
		operationComponent,
		requireEmpty,
	)
}

func diagnosticsUploadPathAvailable(
	filesystem stfs.Filesystem,
	installationComponent, operationComponent string,
	requireEmpty bool,
) bool {
	if filesystem == nil || !diagnosticsComponentValid(installationComponent) ||
		!diagnosticsComponentValid(operationComponent) {
		return false
	}

	directories := []string{
		diagnosticsNamespaceRoot,
		filepath.Join(diagnosticsNamespaceRoot, "installations"),
		filepath.Join(diagnosticsNamespaceRoot, "installations", installationComponent),
		filepath.Join(diagnosticsNamespaceRoot, "installations", installationComponent, "operations"),
	}
	for _, path := range directories {
		info, err := filesystem.Lstat(path)
		if err != nil || !info.IsDir() || info.IsSymlink() {
			return false
		}
	}

	base := filepath.Join(directories[len(directories)-1], operationComponent)
	artifacts := []string{
		base + ".request.cbor",
		base + ".attestation.cbor",
		base + ".response.cbor",
	}
	if requireEmpty {
		for _, path := range artifacts {
			if _, err := filesystem.Lstat(path); err == nil || !stfs.IsNotExist(err) {
				return false
			}
		}
	}

	matcher := ignore.New(filesystem)
	defer matcher.Stop()
	if err := matcher.Load(".stignore"); err != nil && !stfs.IsNotExist(err) {
		return false
	}
	for _, path := range append(directories, artifacts...) {
		if matcher.Match(path).IsIgnored() {
			return false
		}
	}
	return true
}

func diagnosticsComponentValid(value string) bool {
	if len(value) != 52 || value != strings.ToLower(value) {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '2' || character > '7') {
			return false
		}
	}
	return true
}
