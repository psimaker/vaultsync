//go:build linux

package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

type diagnosticsNamespaceLinuxRoot struct {
	root                    *os.Root
	anchor                  *os.File
	alias                   string
	identity                diagnosticsNamespaceFileIdentity
	mutex                   sync.Mutex
	beforeFinalCleanupCheck func()
}

func openDiagnosticsNamespaceRoot(alias string, expected *diagnosticsNamespaceFileIdentity) (*diagnosticsNamespaceRootHandle, error) {
	pathInfo, err := os.Lstat(alias)
	if err != nil || !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errDiagnosticsNamespaceUnsupported
	}
	root, err := os.OpenRoot(alias)
	if err != nil {
		return nil, errDiagnosticsNamespaceUnsupported
	}
	anchor, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		return nil, errDiagnosticsNamespaceUnsupported
	}
	rootInfo, rootErr := root.Stat(".")
	anchorInfo, anchorErr := anchor.Stat()
	identity, identityErr := diagnosticsNamespaceLinuxDirectoryIdentity(anchor, anchorInfo)
	if rootErr != nil || anchorErr != nil || identityErr != nil || !os.SameFile(pathInfo, rootInfo) ||
		!os.SameFile(rootInfo, anchorInfo) || (expected != nil && identity != *expected) {
		_ = anchor.Close()
		_ = root.Close()
		return nil, errDiagnosticsNamespaceUnsupported
	}
	platform := &diagnosticsNamespaceLinuxRoot{root: root, anchor: anchor, alias: alias, identity: identity}
	return &diagnosticsNamespaceRootHandle{platform: platform, identity: identity}, nil
}

func (handle *diagnosticsNamespaceRootHandle) Close() error {
	platform, err := handle.linux()
	if err != nil {
		return err
	}
	platform.mutex.Lock()
	defer platform.mutex.Unlock()
	anchorErr := platform.anchor.Close()
	rootErr := platform.root.Close()
	if anchorErr != nil {
		return anchorErr
	}
	return rootErr
}

func (handle *diagnosticsNamespaceRootHandle) CreateDirectory(path diagnosticsNamespacePath) error {
	platform, err := handle.linux()
	if err != nil || !path.valid() {
		return errDiagnosticsNamespaceUnsupported
	}
	platform.mutex.Lock()
	defer platform.mutex.Unlock()
	if err := platform.verifyRoot(); err != nil {
		return err
	}
	parent, closeParent, err := platform.walkDirectories(path.components[:len(path.components)-1])
	if err != nil {
		return err
	}
	if closeParent {
		defer parent.Close()
	}
	name := path.components[len(path.components)-1]
	if _, err := parent.Lstat(name); err == nil {
		return errDiagnosticsNamespaceCollision
	} else if !errors.Is(err, fs.ErrNotExist) {
		return errDiagnosticsNamespaceConflict
	}
	if err := parent.Mkdir(name, 0o700); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return errDiagnosticsNamespaceCollision
		}
		return errDiagnosticsNamespaceConflict
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return errDiagnosticsNamespaceConflict
	}
	childAnchor, err := child.Open(".")
	if err != nil {
		_ = child.Close()
		return errDiagnosticsNamespaceConflict
	}
	childInfo, statErr := childAnchor.Stat()
	identity, identityErr := diagnosticsNamespaceLinuxDirectoryIdentity(childAnchor, childInfo)
	_ = childAnchor.Close()
	_ = child.Close()
	if statErr != nil || identityErr != nil || identity.Device != platform.identity.Device || identity.MountID != platform.identity.MountID {
		return errDiagnosticsNamespaceConflict
	}
	if err := diagnosticsNamespaceSyncRoot(parent); err != nil {
		return err
	}
	return platform.verifyRoot()
}

func (handle *diagnosticsNamespaceRootHandle) CreateImmutable(path diagnosticsNamespacePath, body []byte) (diagnosticsNamespaceOwnedArtifact, error) {
	platform, err := handle.linux()
	if err != nil || !path.valid() || len(body) == 0 || len(body) > diagnosticsNamespaceMaximumArtifactBytes {
		return diagnosticsNamespaceOwnedArtifact{}, errDiagnosticsNamespaceUnsupported
	}
	platform.mutex.Lock()
	defer platform.mutex.Unlock()
	if err := platform.verifyRoot(); err != nil {
		return diagnosticsNamespaceOwnedArtifact{}, err
	}
	parent, closeParent, err := platform.walkDirectories(path.components[:len(path.components)-1])
	if err != nil {
		return diagnosticsNamespaceOwnedArtifact{}, err
	}
	if closeParent {
		defer parent.Close()
	}
	name := path.components[len(path.components)-1]
	if _, err := parent.Lstat(name); err == nil {
		return diagnosticsNamespaceOwnedArtifact{}, errDiagnosticsNamespaceCollision
	} else if !errors.Is(err, fs.ErrNotExist) {
		return diagnosticsNamespaceOwnedArtifact{}, errDiagnosticsNamespaceConflict
	}
	file, err := parent.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return diagnosticsNamespaceOwnedArtifact{}, errDiagnosticsNamespaceCollision
		}
		return diagnosticsNamespaceOwnedArtifact{}, errDiagnosticsNamespaceConflict
	}
	removeOnFailure := true
	defer func() {
		_ = file.Close()
		if removeOnFailure {
			_ = parent.Remove(name)
		}
	}()
	if _, err := file.Write(body); err != nil || file.Sync() != nil {
		return diagnosticsNamespaceOwnedArtifact{}, errDiagnosticsNamespaceConflict
	}
	info, err := file.Stat()
	identity, identityErr := diagnosticsNamespaceLinuxFileIdentity(file, info, len(body))
	if err != nil || identityErr != nil || identity.Device != platform.identity.Device || identity.MountID != platform.identity.MountID {
		return diagnosticsNamespaceOwnedArtifact{}, errDiagnosticsNamespaceConflict
	}
	if err := file.Close(); err != nil {
		return diagnosticsNamespaceOwnedArtifact{}, errDiagnosticsNamespaceConflict
	}
	removeOnFailure = false
	if err := diagnosticsNamespaceSyncRoot(parent); err != nil {
		return diagnosticsNamespaceOwnedArtifact{}, err
	}
	if err := platform.verifyRoot(); err != nil {
		return diagnosticsNamespaceOwnedArtifact{}, err
	}
	return diagnosticsNamespaceOwnedArtifact{path: cloneDiagnosticsNamespacePath(path), identity: identity, digest: sha256BytesArray(body)}, nil
}

func (handle *diagnosticsNamespaceRootHandle) ReadImmutable(path diagnosticsNamespacePath) ([]byte, diagnosticsNamespaceFileIdentity, error) {
	platform, err := handle.linux()
	if err != nil || !path.valid() {
		return nil, diagnosticsNamespaceFileIdentity{}, errDiagnosticsNamespaceUnsupported
	}
	platform.mutex.Lock()
	defer platform.mutex.Unlock()
	return platform.readImmutableLocked(path)
}

func (handle *diagnosticsNamespaceRootHandle) CleanupOwned(artifacts []diagnosticsNamespaceOwnedArtifact) (diagnosticsNamespaceCleanupResult, error) {
	platform, err := handle.linux()
	if err != nil || len(artifacts) > diagnosticsNamespaceMaximumCleanupFiles {
		return diagnosticsNamespaceCleanupResult{}, errDiagnosticsNamespaceLimit
	}
	platform.mutex.Lock()
	defer platform.mutex.Unlock()
	result := diagnosticsNamespaceCleanupResult{}
	for _, artifact := range artifacts {
		if !diagnosticsNamespaceOperationArtifactPath(artifact.path) {
			return result, errDiagnosticsNamespaceConflict
		}
		body, identity, readErr := platform.readImmutableLocked(artifact.path)
		if errors.Is(readErr, fs.ErrNotExist) {
			result.Missing++
			continue
		}
		if readErr != nil || !artifact.matches(body, identity) {
			result.Conflicts++
			continue
		}
		parent, closeParent, walkErr := platform.walkDirectories(artifact.path.components[:len(artifact.path.components)-1])
		if walkErr != nil {
			result.Conflicts++
			continue
		}
		name := artifact.path.components[len(artifact.path.components)-1]
		if platform.beforeFinalCleanupCheck != nil {
			platform.beforeFinalCleanupCheck()
		}
		bodyImmediatelyBefore, identityImmediatelyBefore, verifyErr := platform.readImmutableFromParent(parent, name)
		if verifyErr != nil || !artifact.matches(bodyImmediatelyBefore, identityImmediatelyBefore) {
			if closeParent {
				_ = parent.Close()
			}
			result.Conflicts++
			continue
		}
		removeErr := parent.Remove(name)
		if removeErr == nil {
			removeErr = diagnosticsNamespaceSyncRoot(parent)
		}
		if closeParent {
			_ = parent.Close()
		}
		if errors.Is(removeErr, fs.ErrNotExist) {
			result.Missing++
		} else if removeErr != nil {
			result.Conflicts++
		} else {
			result.Removed++
		}
	}
	if result.Conflicts > 0 {
		return result, errDiagnosticsNamespaceConflict
	}
	return result, nil
}

func (handle *diagnosticsNamespaceRootHandle) ScanFixedLayout() error {
	platform, err := handle.linux()
	if err != nil {
		return err
	}
	platform.mutex.Lock()
	defer platform.mutex.Unlock()
	if err := platform.verifyRoot(); err != nil {
		return err
	}
	rootEntries, err := platform.readDirectoryEntries(platform.root, diagnosticsNamespaceMaximumRootEntries)
	if err != nil || !diagnosticsNamespaceExactNames(rootEntries, []string{
		"README.txt", diagnosticsNamespaceRootManifestName, diagnosticsNamespaceManifestEpochsName, diagnosticsNamespaceInstallationsName,
	}) {
		return errDiagnosticsNamespaceConflict
	}
	readme, _, err := platform.readImmutableLocked(diagnosticsNamespaceReadmePath())
	if err != nil || !bytes.Equal(readme, []byte(diagnosticsNamespaceReadme)) {
		return errDiagnosticsNamespaceConflict
	}
	rootManifest, _, err := platform.readImmutableLocked(diagnosticsNamespaceRootManifestPath())
	if err != nil {
		return errDiagnosticsNamespaceConflict
	}
	if message, decodeErr := decodeDiagnosticsNamespaceMessage(rootManifest); decodeErr != nil || message.messageType != diagnosticsNamespaceRootManifest {
		return errDiagnosticsNamespaceConflict
	}
	manifestEpochs, closeEpochs, err := platform.walkDirectories([]string{diagnosticsNamespaceManifestEpochsName})
	if err != nil {
		return err
	}
	epochEntries, err := platform.readDirectoryEntries(manifestEpochs, diagnosticsNamespaceMaximumHelperEpochs)
	if closeEpochs {
		_ = manifestEpochs.Close()
	}
	if err != nil {
		return err
	}
	type epochEntry struct {
		epoch uint64
	}
	helperEntries := make([]epochEntry, 0, len(epochEntries))
	for _, name := range epochEntries {
		epochNumber, valid := diagnosticsNamespaceParseEpochEntry(name, false)
		if !valid {
			return errDiagnosticsNamespaceConflict
		}
		helperEntries = append(helperEntries, epochEntry{epoch: epochNumber})
	}
	sort.Slice(helperEntries, func(i, j int) bool { return helperEntries[i].epoch < helperEntries[j].epoch })
	helperBodies := make([][]byte, 0, len(helperEntries))
	for _, entry := range helperEntries {
		path, _ := diagnosticsNamespaceHelperEpochPath(entry.epoch)
		body, _, readErr := platform.readImmutableLocked(path)
		message, decodeErr := decodeDiagnosticsNamespaceMessage(body)
		recordEpoch, _ := message.uintField(15)
		if readErr != nil || decodeErr != nil || message.messageType != diagnosticsNamespaceHelperEpoch || recordEpoch != entry.epoch {
			return errDiagnosticsNamespaceConflict
		}
		helperBodies = append(helperBodies, body)
	}
	installations, closeInstallations, err := platform.walkDirectories([]string{diagnosticsNamespaceInstallationsName})
	if err != nil {
		return err
	}
	installationEntries, err := platform.readDirectoryEntries(installations, diagnosticsNamespaceMaximumInstallations)
	if closeInstallations {
		_ = installations.Close()
	}
	if err != nil {
		return err
	}
	sort.Strings(installationEntries)
	authorizationChains := make([][][]byte, 0, len(installationEntries))
	for _, installation := range installationEntries {
		installationBinding, err := parseDiagnosticsNamespaceComponent(installation)
		if err != nil {
			return errDiagnosticsNamespaceConflict
		}
		installationRoot, closeInstallation, err := platform.walkDirectories([]string{diagnosticsNamespaceInstallationsName, installation})
		if err != nil {
			return err
		}
		entries, err := platform.readDirectoryEntries(installationRoot, diagnosticsNamespaceMaximumInstallEntries)
		if closeInstallation {
			_ = installationRoot.Close()
		}
		if err != nil || !diagnosticsNamespaceExactNames(entries, []string{
			diagnosticsNamespaceAuthorizationName, diagnosticsNamespaceAuthorizationEpochsName, diagnosticsNamespaceOperationsName,
		}) {
			return errDiagnosticsNamespaceConflict
		}
		paths, _ := diagnosticsNamespaceAuthorizationPaths(installationBinding)
		authorizationBody, _, readErr := platform.readImmutableLocked(paths[0])
		authorization, decodeErr := decodeDiagnosticsNamespaceMessage(authorizationBody)
		boundInstallation, bound := authorization.bytesField(8, 32)
		if readErr != nil || decodeErr != nil || authorization.messageType != diagnosticsNamespaceInitialAuthorization ||
			!bound || !bytes.Equal(boundInstallation, installationBinding) {
			return errDiagnosticsNamespaceConflict
		}
		authorizationBodies := [][]byte{authorizationBody}
		authorizationEpochs, closeAuthorizationEpochs, err := platform.walkDirectories([]string{
			diagnosticsNamespaceInstallationsName, installation, diagnosticsNamespaceAuthorizationEpochsName,
		})
		if err != nil {
			return err
		}
		authorizationEntries, err := platform.readDirectoryEntries(authorizationEpochs, diagnosticsNamespaceMaximumAuthorizationEpochs)
		if closeAuthorizationEpochs {
			_ = authorizationEpochs.Close()
		}
		if err != nil {
			return err
		}
		authorizationEpochEntries := make([]epochEntry, 0, len(authorizationEntries))
		for _, name := range authorizationEntries {
			epochNumber, valid := diagnosticsNamespaceParseEpochEntry(name, true)
			if !valid {
				return errDiagnosticsNamespaceConflict
			}
			authorizationEpochEntries = append(authorizationEpochEntries, epochEntry{epoch: epochNumber})
		}
		sort.Slice(authorizationEpochEntries, func(i, j int) bool {
			return authorizationEpochEntries[i].epoch < authorizationEpochEntries[j].epoch
		})
		for _, entry := range authorizationEpochEntries {
			path, _ := diagnosticsNamespaceAuthorizationEpochPath(installationBinding, entry.epoch)
			body, _, readErr := platform.readImmutableLocked(path)
			message, decodeErr := decodeDiagnosticsNamespaceMessage(body)
			recordEpoch, _ := message.uintField(31)
			if readErr != nil || decodeErr != nil || message.messageType != diagnosticsNamespaceAuthorizationEpoch || recordEpoch != entry.epoch {
				return errDiagnosticsNamespaceConflict
			}
			authorizationBodies = append(authorizationBodies, body)
		}
		operations, closeOperations, err := platform.walkDirectories([]string{
			diagnosticsNamespaceInstallationsName, installation, diagnosticsNamespaceOperationsName,
		})
		if err != nil {
			return err
		}
		operationEntries, err := platform.readDirectoryEntries(operations, diagnosticsNamespaceMaximumInstallEntries)
		if closeOperations {
			_ = operations.Close()
		}
		if err != nil {
			return err
		}
		for _, entry := range operationEntries {
			if !diagnosticsNamespaceValidOperationEntry(entry) {
				return errDiagnosticsNamespaceConflict
			}
		}
		authorizationChains = append(authorizationChains, authorizationBodies)
	}
	if err := validateDiagnosticsNamespacePersistentChain(rootManifest, helperBodies, authorizationChains); err != nil {
		return errDiagnosticsNamespaceConflict
	}
	return platform.verifyRoot()
}

func (handle *diagnosticsNamespaceRootHandle) ValidateRootRecord(record diagnosticsNamespaceRootRecord) error {
	platform, err := handle.linux()
	if err != nil || validateDiagnosticsNamespaceRootRecord(record) != nil {
		return errDiagnosticsNamespaceUnsupported
	}
	platform.mutex.Lock()
	defer platform.mutex.Unlock()
	if platform.identity.Device != record.Device || platform.identity.Inode != record.Inode {
		return errDiagnosticsNamespaceUnsupported
	}
	body, _, err := platform.readImmutableLocked(diagnosticsNamespaceRootManifestPath())
	if err != nil {
		return err
	}
	digest, err := diagnosticsNamespaceRecordDigest(body)
	if err != nil || !bytes.Equal(digest[:], record.RootManifestDigest) {
		return errDiagnosticsNamespaceConflict
	}
	message, _ := decodeDiagnosticsNamespaceMessage(body)
	homeserver, _ := message.bytesField(5, 32)
	folder, _ := message.bytesField(6, 32)
	namespaceID, _ := message.bytesField(7, 32)
	if !bytes.Equal(homeserver, record.HomeserverBinding) || !bytes.Equal(folder, record.FolderBinding) ||
		!bytes.Equal(namespaceID, record.NamespaceID) {
		return errDiagnosticsNamespaceConflict
	}
	return nil
}

func (handle *diagnosticsNamespaceRootHandle) InstallationCount() (int, error) {
	platform, err := handle.linux()
	if err != nil {
		return 0, err
	}
	platform.mutex.Lock()
	defer platform.mutex.Unlock()
	if err := platform.verifyRoot(); err != nil {
		return 0, err
	}
	installations, closeInstallations, err := platform.walkDirectories([]string{diagnosticsNamespaceInstallationsName})
	if err != nil {
		return 0, err
	}
	entries, err := platform.readDirectoryEntries(installations, diagnosticsNamespaceMaximumInstallations)
	if closeInstallations {
		_ = installations.Close()
	}
	if err != nil {
		return 0, err
	}
	return len(entries), nil
}

func (handle *diagnosticsNamespaceRootHandle) linux() (*diagnosticsNamespaceLinuxRoot, error) {
	if handle == nil {
		return nil, errDiagnosticsNamespaceUnsupported
	}
	platform, ok := handle.platform.(*diagnosticsNamespaceLinuxRoot)
	if !ok || platform == nil || platform.root == nil || platform.anchor == nil {
		return nil, errDiagnosticsNamespaceUnsupported
	}
	return platform, nil
}

func (platform *diagnosticsNamespaceLinuxRoot) verifyRoot() error {
	pathInfo, pathErr := os.Lstat(platform.alias)
	rootInfo, rootErr := platform.root.Stat(".")
	anchorInfo, anchorErr := platform.anchor.Stat()
	identity, identityErr := diagnosticsNamespaceLinuxDirectoryIdentity(platform.anchor, anchorInfo)
	if pathErr != nil || rootErr != nil || anchorErr != nil || identityErr != nil || pathInfo.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(pathInfo, rootInfo) || !os.SameFile(rootInfo, anchorInfo) || identity != platform.identity {
		return errDiagnosticsNamespaceUnsupported
	}
	return nil
}

func (platform *diagnosticsNamespaceLinuxRoot) walkDirectories(components []string) (*os.Root, bool, error) {
	current := platform.root
	closeCurrent := false
	for _, component := range components {
		if component == "" || component == "." || component == ".." || strings.ContainsAny(component, "/\\") {
			if closeCurrent {
				_ = current.Close()
			}
			return nil, false, errDiagnosticsNamespaceConflict
		}
		pathInfo, err := current.Lstat(component)
		if err != nil || !pathInfo.IsDir() || pathInfo.Mode()&os.ModeSymlink != 0 {
			if closeCurrent {
				_ = current.Close()
			}
			if errors.Is(err, fs.ErrNotExist) {
				return nil, false, err
			}
			return nil, false, errDiagnosticsNamespaceConflict
		}
		child, err := current.OpenRoot(component)
		if err != nil {
			if closeCurrent {
				_ = current.Close()
			}
			return nil, false, errDiagnosticsNamespaceConflict
		}
		anchor, err := child.Open(".")
		if err != nil {
			_ = child.Close()
			if closeCurrent {
				_ = current.Close()
			}
			return nil, false, errDiagnosticsNamespaceConflict
		}
		childInfo, childStatErr := anchor.Stat()
		identity, identityErr := diagnosticsNamespaceLinuxDirectoryIdentity(anchor, childInfo)
		rootInfo, rootStatErr := child.Stat(".")
		_ = anchor.Close()
		if childStatErr != nil || identityErr != nil || rootStatErr != nil || !os.SameFile(pathInfo, childInfo) ||
			!os.SameFile(childInfo, rootInfo) || identity.Device != platform.identity.Device || identity.MountID != platform.identity.MountID {
			_ = child.Close()
			if closeCurrent {
				_ = current.Close()
			}
			return nil, false, errDiagnosticsNamespaceConflict
		}
		if closeCurrent {
			_ = current.Close()
		}
		current = child
		closeCurrent = true
	}
	return current, closeCurrent, nil
}

func (platform *diagnosticsNamespaceLinuxRoot) readImmutableLocked(path diagnosticsNamespacePath) ([]byte, diagnosticsNamespaceFileIdentity, error) {
	if err := platform.verifyRoot(); err != nil {
		return nil, diagnosticsNamespaceFileIdentity{}, err
	}
	parent, closeParent, err := platform.walkDirectories(path.components[:len(path.components)-1])
	if err != nil {
		return nil, diagnosticsNamespaceFileIdentity{}, err
	}
	if closeParent {
		defer parent.Close()
	}
	body, identity, err := platform.readImmutableFromParent(parent, path.components[len(path.components)-1])
	if err != nil {
		return nil, diagnosticsNamespaceFileIdentity{}, err
	}
	if err := platform.verifyRoot(); err != nil {
		return nil, diagnosticsNamespaceFileIdentity{}, err
	}
	return body, identity, nil
}

func (platform *diagnosticsNamespaceLinuxRoot) readImmutableFromParent(parent *os.Root, name string) ([]byte, diagnosticsNamespaceFileIdentity, error) {
	pathInfo, err := parent.Lstat(name)
	if err != nil {
		return nil, diagnosticsNamespaceFileIdentity{}, err
	}
	if !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 || pathInfo.Size() <= 0 || pathInfo.Size() > diagnosticsNamespaceMaximumArtifactBytes {
		return nil, diagnosticsNamespaceFileIdentity{}, errDiagnosticsNamespaceConflict
	}
	file, err := parent.Open(name)
	if err != nil {
		return nil, diagnosticsNamespaceFileIdentity{}, errDiagnosticsNamespaceConflict
	}
	defer file.Close()
	infoBefore, err := file.Stat()
	identity, identityErr := diagnosticsNamespaceLinuxFileIdentity(file, infoBefore, int(pathInfo.Size()))
	if err != nil || identityErr != nil || !os.SameFile(pathInfo, infoBefore) || identity.Device != platform.identity.Device || identity.MountID != platform.identity.MountID {
		return nil, diagnosticsNamespaceFileIdentity{}, errDiagnosticsNamespaceConflict
	}
	body, err := io.ReadAll(io.LimitReader(file, diagnosticsNamespaceMaximumArtifactBytes+1))
	if err != nil || len(body) == 0 || len(body) > diagnosticsNamespaceMaximumArtifactBytes {
		return nil, diagnosticsNamespaceFileIdentity{}, errDiagnosticsNamespaceConflict
	}
	infoAfter, err := file.Stat()
	identityAfter, identityErr := diagnosticsNamespaceLinuxFileIdentity(file, infoAfter, len(body))
	if err != nil || identityErr != nil || !os.SameFile(infoBefore, infoAfter) || identityAfter != identity {
		return nil, diagnosticsNamespaceFileIdentity{}, errDiagnosticsNamespaceConflict
	}
	return body, identity, nil
}

func (platform *diagnosticsNamespaceLinuxRoot) readDirectoryEntries(root *os.Root, limit int) ([]string, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, errDiagnosticsNamespaceConflict
	}
	defer directory.Close()
	entries, err := directory.ReadDir(limit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, errDiagnosticsNamespaceConflict
	}
	if len(entries) > limit {
		return nil, errDiagnosticsNamespaceLimit
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, errDiagnosticsNamespaceConflict
		}
		names = append(names, entry.Name())
	}
	return names, nil
}

func diagnosticsNamespaceLinuxDirectoryIdentity(file *os.File, info fs.FileInfo) (diagnosticsNamespaceFileIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0o700 || stat.Uid != uint32(os.Geteuid()) {
		return diagnosticsNamespaceFileIdentity{}, errDiagnosticsNamespaceConflict
	}
	mountID, err := diagnosticsNamespaceLinuxMountID(file)
	if err != nil {
		return diagnosticsNamespaceFileIdentity{}, err
	}
	return diagnosticsNamespaceFileIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, MountID: mountID}, nil
}

func diagnosticsNamespaceLinuxFileIdentity(file *os.File, info fs.FileInfo, expectedSize int) (diagnosticsNamespaceFileIdentity, error) {
	if info == nil {
		return diagnosticsNamespaceFileIdentity{}, errDiagnosticsNamespaceConflict
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || stat.Uid != uint32(os.Geteuid()) ||
		stat.Nlink != 1 || info.Size() != int64(expectedSize) || info.Size() <= 0 || info.Size() > diagnosticsNamespaceMaximumArtifactBytes ||
		!diagnosticsNamespaceLinuxFileAllocated(file, stat, info.Size()) {
		return diagnosticsNamespaceFileIdentity{}, errDiagnosticsNamespaceConflict
	}
	mountID, err := diagnosticsNamespaceLinuxMountID(file)
	if err != nil {
		return diagnosticsNamespaceFileIdentity{}, err
	}
	return diagnosticsNamespaceFileIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, MountID: mountID}, nil
}

func diagnosticsNamespaceLinuxFileAllocated(file *os.File, stat *syscall.Stat_t, size int64) bool {
	if stat.Blocks*512 >= size {
		return true
	}
	const (
		seekData = 3
		seekHole = 4
	)
	dataOffset, dataErr := syscall.Seek(int(file.Fd()), 0, seekData)
	holeOffset, holeErr := syscall.Seek(int(file.Fd()), 0, seekHole)
	_, resetErr := syscall.Seek(int(file.Fd()), 0, io.SeekStart)
	return dataErr == nil && holeErr == nil && resetErr == nil && dataOffset == 0 && holeOffset >= size
}

func diagnosticsNamespaceLinuxMountID(file *os.File) (uint64, error) {
	body, err := os.ReadFile(fmt.Sprintf("/proc/self/fdinfo/%d", file.Fd()))
	if err != nil {
		return 0, errDiagnosticsNamespaceUnsupported
	}
	for line := range strings.SplitSeq(string(body), "\n") {
		if value, found := strings.CutPrefix(line, "mnt_id:"); found {
			mountID, parseErr := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
			if parseErr == nil && mountID != 0 {
				return mountID, nil
			}
		}
	}
	return 0, errDiagnosticsNamespaceUnsupported
}

func diagnosticsNamespaceSyncRoot(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return errDiagnosticsNamespaceConflict
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return errDiagnosticsNamespaceConflict
	}
	return nil
}

func diagnosticsNamespaceOperationArtifactPath(path diagnosticsNamespacePath) bool {
	if path.persistent || len(path.components) != 4 || path.components[0] != diagnosticsNamespaceInstallationsName ||
		path.components[2] != diagnosticsNamespaceOperationsName {
		return false
	}
	if _, err := parseDiagnosticsNamespaceComponent(path.components[1]); err != nil {
		return false
	}
	name := path.components[3]
	for _, suffix := range []string{".request.cbor", ".attestation.cbor", ".response.cbor"} {
		if component, found := strings.CutSuffix(name, suffix); found {
			_, err := parseDiagnosticsNamespaceComponent(component)
			return err == nil
		}
	}
	return false
}

func diagnosticsNamespaceExactNames(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	wanted := make(map[string]struct{}, len(expected))
	for _, name := range expected {
		wanted[name] = struct{}{}
	}
	for _, name := range actual {
		if _, exists := wanted[name]; !exists {
			return false
		}
	}
	return true
}

func diagnosticsNamespaceValidEpochEntry(name string, authorization bool) bool {
	_, valid := diagnosticsNamespaceParseEpochEntry(name, authorization)
	return valid
}

func diagnosticsNamespaceParseEpochEntry(name string, authorization bool) (uint64, bool) {
	suffix := ".helper-manifest.cbor"
	if authorization {
		suffix = ".authorization.cbor"
	}
	component, found := strings.CutSuffix(name, suffix)
	if !found || component == "" || (len(component) > 1 && component[0] == '0') {
		return 0, false
	}
	epoch, err := strconv.ParseUint(component, 10, 64)
	return epoch, err == nil && epoch >= 2 && (!authorization || epoch <= diagnosticsNamespaceMaximumAuthorizationEpochs+1)
}

func diagnosticsNamespaceValidOperationEntry(name string) bool {
	for _, suffix := range []string{".request.cbor", ".attestation.cbor", ".response.cbor"} {
		if component, found := strings.CutSuffix(name, suffix); found {
			_, err := parseDiagnosticsNamespaceComponent(component)
			return err == nil
		}
	}
	return false
}

func cloneDiagnosticsNamespacePath(path diagnosticsNamespacePath) diagnosticsNamespacePath {
	return diagnosticsNamespacePath{components: append([]string(nil), path.components...), persistent: path.persistent}
}

func sha256BytesArray(value []byte) [32]byte {
	return sha256.Sum256(value)
}
