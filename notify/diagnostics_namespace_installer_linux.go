//go:build linux

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func prepareDiagnosticsNamespaceExplicit(request diagnosticsNamespacePreparationRequest) (diagnosticsNamespaceRootRecord, error) {
	if !request.operatorConfirmed || !request.ignore.valid() || request.stateStore == nil ||
		len(request.homeserverBinding) != 32 || len(request.folderBinding) != 32 ||
		!filepath.IsAbs(request.parentPath) || filepath.Clean(request.parentPath) != request.parentPath {
		return diagnosticsNamespaceRootRecord{}, errDiagnosticsNamespaceUnsupported
	}
	chain := diagnosticsNamespaceChain{Enablement: request.enablement, RootManifest: request.rootManifest}
	if err := validateDiagnosticsNamespaceChain(chain); err != nil {
		return diagnosticsNamespaceRootRecord{}, err
	}
	enablement, _ := decodeDiagnosticsNamespaceMessage(request.enablement)
	rootManifest, _ := decodeDiagnosticsNamespaceMessage(request.rootManifest)
	homeserver, _ := rootManifest.bytesField(5, 32)
	folder, _ := rootManifest.bytesField(6, 32)
	if !bytes.Equal(homeserver, request.homeserverBinding) || !bytes.Equal(folder, request.folderBinding) ||
		!diagnosticsNamespaceCommonBindingsEqual(enablement, rootManifest, 5, 6) {
		return diagnosticsNamespaceRootRecord{}, errDiagnosticsNamespaceInvalid
	}
	state, err := request.stateStore.snapshot()
	if err != nil {
		return diagnosticsNamespaceRootRecord{}, err
	}
	mountAlias, err := diagnosticsNamespaceNextMountAlias(state)
	if err != nil {
		return diagnosticsNamespaceRootRecord{}, err
	}
	parent, err := diagnosticsNamespaceOpenTrustedParent(request.parentPath)
	if err != nil {
		return diagnosticsNamespaceRootRecord{}, fmt.Errorf("trusted parent: %w", err)
	}
	defer parent.Close()
	if err := diagnosticsNamespaceValidateFolderMarker(parent); err != nil {
		return diagnosticsNamespaceRootRecord{}, fmt.Errorf("folder marker: %w", err)
	}
	if !diagnosticsNamespaceTrustedParentStillSelected(parent, request.parentPath) {
		return diagnosticsNamespaceRootRecord{}, errDiagnosticsNamespaceUnsupported
	}
	if _, err := parent.Lstat(diagnosticsNamespaceRootName); err == nil {
		return diagnosticsNamespaceRootRecord{}, errDiagnosticsNamespaceCollision
	} else if !errors.Is(err, fs.ErrNotExist) {
		return diagnosticsNamespaceRootRecord{}, errDiagnosticsNamespaceConflict
	}
	if err := parent.Mkdir(diagnosticsNamespaceRootName, 0o700); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return diagnosticsNamespaceRootRecord{}, errDiagnosticsNamespaceCollision
		}
		return diagnosticsNamespaceRootRecord{}, fmt.Errorf("root create: %w", errDiagnosticsNamespaceConflict)
	}
	if err := diagnosticsNamespaceSyncRoot(parent); err != nil {
		return diagnosticsNamespaceRootRecord{}, fmt.Errorf("parent sync: %w", err)
	}
	if !diagnosticsNamespaceTrustedParentStillSelected(parent, request.parentPath) {
		return diagnosticsNamespaceRootRecord{}, errDiagnosticsNamespaceUnsupported
	}
	if request.hooks.afterRootCreate != nil {
		if err := request.hooks.afterRootCreate(); err != nil {
			return diagnosticsNamespaceRootRecord{}, err
		}
	}
	if !diagnosticsNamespaceTrustedParentStillSelected(parent, request.parentPath) {
		return diagnosticsNamespaceRootRecord{}, errDiagnosticsNamespaceUnsupported
	}
	rootPath := filepath.Join(request.parentPath, diagnosticsNamespaceRootName)
	handle, err := openDiagnosticsNamespaceRoot(rootPath, nil)
	if err != nil {
		return diagnosticsNamespaceRootRecord{}, fmt.Errorf("root open: %w", err)
	}
	defer handle.Close()
	for _, directory := range []diagnosticsNamespacePath{
		{components: []string{diagnosticsNamespaceManifestEpochsName}, persistent: true},
		{components: []string{diagnosticsNamespaceInstallationsName}, persistent: true},
	} {
		if err := handle.CreateDirectory(directory); err != nil {
			return diagnosticsNamespaceRootRecord{}, fmt.Errorf("fixed directory: %w", err)
		}
	}
	if _, err := handle.CreateImmutable(diagnosticsNamespaceReadmePath(), []byte(diagnosticsNamespaceReadme)); err != nil {
		return diagnosticsNamespaceRootRecord{}, fmt.Errorf("readme create: %w", err)
	}
	if _, err := handle.CreateImmutable(diagnosticsNamespaceRootManifestPath(), request.rootManifest); err != nil {
		return diagnosticsNamespaceRootRecord{}, fmt.Errorf("root manifest create: %w", err)
	}
	if err := handle.ScanFixedLayout(); err != nil {
		return diagnosticsNamespaceRootRecord{}, fmt.Errorf("layout scan: %w", err)
	}
	if !diagnosticsNamespaceTrustedParentStillSelected(parent, request.parentPath) {
		return diagnosticsNamespaceRootRecord{}, errDiagnosticsNamespaceUnsupported
	}
	namespaceID, _ := rootManifest.bytesField(7, 32)
	rootDigest, _ := diagnosticsNamespaceRecordDigest(request.rootManifest)
	identity := handle.Identity()
	record := diagnosticsNamespaceRootRecord{
		HomeserverBinding: append([]byte(nil), homeserver...), FolderBinding: append([]byte(nil), folder...),
		NamespaceID: append([]byte(nil), namespaceID...), RootManifestDigest: append([]byte(nil), rootDigest[:]...),
		MountAlias: mountAlias, Device: identity.Device, Inode: identity.Inode,
	}
	if err := request.stateStore.registerRoot(record); err != nil {
		return diagnosticsNamespaceRootRecord{}, fmt.Errorf("state register: %w", err)
	}
	return record, nil
}

func appendDiagnosticsNamespaceHelperEpoch(handle *diagnosticsNamespaceRootHandle, chain diagnosticsNamespaceChain) error {
	if handle == nil || len(chain.HelperEpochs) == 0 || validateDiagnosticsNamespaceChain(chain) != nil {
		return errDiagnosticsNamespaceInvalid
	}
	rootBody, _, err := handle.ReadImmutable(diagnosticsNamespaceRootManifestPath())
	if err != nil || !bytes.Equal(rootBody, chain.RootManifest) {
		return errDiagnosticsNamespaceConflict
	}
	for index, expected := range chain.HelperEpochs {
		epochRecord, _ := decodeDiagnosticsNamespaceMessage(expected)
		epoch, _ := epochRecord.uintField(15)
		path, pathErr := diagnosticsNamespaceHelperEpochPath(epoch)
		if pathErr != nil {
			return pathErr
		}
		actual, _, readErr := handle.ReadImmutable(path)
		switch {
		case readErr == nil && bytes.Equal(actual, expected):
			continue
		case readErr == nil:
			return errDiagnosticsNamespaceConflict
		case !errors.Is(readErr, fs.ErrNotExist) || index != len(chain.HelperEpochs)-1:
			return errDiagnosticsNamespaceConflict
		}
		if _, err := handle.CreateImmutable(path, expected); err != nil {
			return err
		}
	}
	return handle.ScanFixedLayout()
}

func installDiagnosticsNamespaceAuthorization(handle *diagnosticsNamespaceRootHandle, chain diagnosticsNamespaceChain, installationIndex int) error {
	if handle == nil || validateDiagnosticsNamespaceChain(chain) != nil || installationIndex < 0 || installationIndex >= len(chain.Authorizations) ||
		len(chain.Authorizations[installationIndex]) != 1 {
		return errDiagnosticsNamespaceInvalid
	}
	if err := handle.ScanFixedLayout(); err != nil {
		return err
	}
	count, err := handle.InstallationCount()
	if err != nil {
		return err
	}
	if count >= diagnosticsNamespaceMaximumInstallations {
		return errDiagnosticsNamespaceLimit
	}
	authorization, _ := decodeDiagnosticsNamespaceMessage(chain.Authorizations[installationIndex][0])
	installationBinding, _ := authorization.bytesField(8, 32)
	paths, err := diagnosticsNamespaceAuthorizationPaths(installationBinding)
	if err != nil {
		return err
	}
	installationDirectory := diagnosticsNamespacePath{components: paths[0].components[:2], persistent: true}
	for _, directory := range []diagnosticsNamespacePath{installationDirectory, paths[1], paths[2]} {
		if err := handle.CreateDirectory(directory); err != nil {
			return err
		}
	}
	if _, err := handle.CreateImmutable(paths[0], chain.Authorizations[installationIndex][0]); err != nil {
		return err
	}
	return handle.ScanFixedLayout()
}

func appendDiagnosticsNamespaceAuthorizationEpoch(handle *diagnosticsNamespaceRootHandle, chain diagnosticsNamespaceChain, installationIndex int) error {
	if handle == nil || validateDiagnosticsNamespaceChain(chain) != nil || installationIndex < 0 || installationIndex >= len(chain.Authorizations) ||
		len(chain.Authorizations[installationIndex]) < 2 {
		return errDiagnosticsNamespaceInvalid
	}
	records := chain.Authorizations[installationIndex]
	initial, _ := decodeDiagnosticsNamespaceMessage(records[0])
	installationBinding, _ := initial.bytesField(8, 32)
	paths, _ := diagnosticsNamespaceAuthorizationPaths(installationBinding)
	initialBody, _, err := handle.ReadImmutable(paths[0])
	if err != nil || !bytes.Equal(initialBody, records[0]) {
		return errDiagnosticsNamespaceConflict
	}
	for index, expected := range records[1:] {
		epochRecord, _ := decodeDiagnosticsNamespaceMessage(expected)
		epoch, _ := epochRecord.uintField(31)
		path, pathErr := diagnosticsNamespaceAuthorizationEpochPath(installationBinding, epoch)
		if pathErr != nil {
			return pathErr
		}
		actual, _, readErr := handle.ReadImmutable(path)
		switch {
		case readErr == nil && bytes.Equal(actual, expected):
			continue
		case readErr == nil:
			return errDiagnosticsNamespaceConflict
		case !errors.Is(readErr, fs.ErrNotExist) || index != len(records)-2:
			return errDiagnosticsNamespaceConflict
		}
		if _, err := handle.CreateImmutable(path, expected); err != nil {
			return err
		}
	}
	return handle.ScanFixedLayout()
}

func diagnosticsNamespaceOpenTrustedParent(path string) (*os.Root, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errDiagnosticsNamespaceUnsupported
	}
	current, err := os.OpenRoot("/")
	if err != nil {
		return nil, errDiagnosticsNamespaceUnsupported
	}
	components := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, component := range components {
		if component == "" {
			continue
		}
		info, err := current.Lstat(component)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			_ = current.Close()
			return nil, errDiagnosticsNamespaceUnsupported
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			_ = current.Close()
			return nil, errDiagnosticsNamespaceUnsupported
		}
		nextInfo, statErr := next.Stat(".")
		if statErr != nil || !os.SameFile(info, nextInfo) {
			_ = next.Close()
			_ = current.Close()
			return nil, errDiagnosticsNamespaceUnsupported
		}
		_ = current.Close()
		current = next
	}
	return current, nil
}

func diagnosticsNamespaceValidateFolderMarker(parent *os.Root) error {
	info, err := parent.Lstat(".stfolder")
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errDiagnosticsNamespaceUnsupported
	}
	marker, err := parent.OpenRoot(".stfolder")
	if err != nil {
		return errDiagnosticsNamespaceUnsupported
	}
	defer marker.Close()
	markerInfo, err := marker.Stat(".")
	if err != nil || !os.SameFile(info, markerInfo) {
		return errDiagnosticsNamespaceUnsupported
	}
	return nil
}

func diagnosticsNamespaceTrustedParentStillSelected(parent *os.Root, path string) bool {
	pathInfo, pathErr := os.Lstat(path)
	rootInfo, rootErr := parent.Stat(".")
	return pathErr == nil && rootErr == nil && pathInfo.IsDir() && pathInfo.Mode()&os.ModeSymlink == 0 && os.SameFile(pathInfo, rootInfo)
}
