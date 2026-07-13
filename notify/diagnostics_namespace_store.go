package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
)

const (
	diagnosticsNamespaceStateFormat = 1
	diagnosticsNamespaceStateFile   = "namespace-v1.json"
	diagnosticsNamespaceLockFile    = ".namespace.lock"
	diagnosticsNamespaceStateBytes  = 256 * 1024
	diagnosticsNamespaceMaxRoots    = 8
)

var (
	errDiagnosticsNamespaceStateInvalid = errors.New("diagnostics namespace state unavailable")
	errDiagnosticsNamespaceStateNewer   = errors.New("diagnostics namespace state requires a newer helper")
)

type diagnosticsNamespaceStoreHooks struct {
	beforeRename func() error
	afterRename  func() error
}

type diagnosticsNamespaceStateStore struct {
	directory string
	statePath string
	hooks     diagnosticsNamespaceStoreHooks
	mutex     sync.Mutex
}

type diagnosticsNamespaceState struct {
	FormatVersion uint64                           `json:"format_version"`
	Revision      uint64                           `json:"revision"`
	Roots         []diagnosticsNamespaceRootRecord `json:"roots"`
}

type diagnosticsNamespaceRootRecord struct {
	HomeserverBinding  []byte `json:"homeserver_binding"`
	FolderBinding      []byte `json:"folder_binding"`
	NamespaceID        []byte `json:"namespace_id"`
	RootManifestDigest []byte `json:"root_manifest_digest"`
	MountAlias         string `json:"mount_alias"`
	Device             uint64 `json:"device"`
	Inode              uint64 `json:"inode"`
}

func openDiagnosticsNamespaceStateStore(directory string) (*diagnosticsNamespaceStateStore, error) {
	if directory == "" {
		return nil, errDiagnosticsNamespaceStateInvalid
	}
	clean := filepath.Clean(directory)
	store := &diagnosticsNamespaceStateStore{
		directory: clean,
		statePath: filepath.Join(clean, diagnosticsNamespaceStateFile),
	}
	if err := store.ensureDirectory(); err != nil {
		return nil, err
	}
	err := store.withLock(func() error {
		_, loadErr := store.loadUnlocked()
		if errors.Is(loadErr, os.ErrNotExist) {
			return store.saveUnlocked(diagnosticsNamespaceState{FormatVersion: diagnosticsNamespaceStateFormat, Revision: 1})
		}
		return loadErr
	})
	if err != nil {
		return nil, err
	}
	return store, nil
}

func (store *diagnosticsNamespaceStateStore) ensureDirectory() error {
	info, err := os.Lstat(store.directory)
	if errors.Is(err, os.ErrNotExist) {
		parent := filepath.Dir(store.directory)
		parentInfo, parentErr := os.Stat(parent)
		if parentErr != nil || !parentInfo.IsDir() || os.Mkdir(store.directory, 0o700) != nil || syncDiagnosticsDirectory(parent) != nil {
			return errDiagnosticsNamespaceStateInvalid
		}
		info, err = os.Lstat(store.directory)
	}
	if err != nil || !info.IsDir() || checkDiagnosticsPrivateDirectory(store.directory, info) != nil {
		return errDiagnosticsNamespaceStateInvalid
	}
	return nil
}

func (store *diagnosticsNamespaceStateStore) snapshot() (diagnosticsNamespaceState, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	var result diagnosticsNamespaceState
	err := store.withLock(func() error {
		state, err := store.loadUnlocked()
		if err != nil {
			return err
		}
		result = cloneDiagnosticsNamespaceState(state)
		return nil
	})
	return result, err
}

func (store *diagnosticsNamespaceStateStore) registerRoot(record diagnosticsNamespaceRootRecord) error {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	return store.withLock(func() error {
		state, err := store.loadUnlocked()
		if err != nil {
			return err
		}
		if err := validateDiagnosticsNamespaceRootRecord(record); err != nil {
			return err
		}
		for _, existing := range state.Roots {
			if bytes.Equal(existing.FolderBinding, record.FolderBinding) {
				if diagnosticsNamespaceRootRecordsEqual(existing, record) {
					return nil
				}
				return errDiagnosticsNamespaceStateInvalid
			}
			if existing.MountAlias == record.MountAlias || bytes.Equal(existing.NamespaceID, record.NamespaceID) {
				return errDiagnosticsNamespaceStateInvalid
			}
		}
		if len(state.Roots) >= diagnosticsNamespaceMaxRoots {
			return errDiagnosticsNamespaceStateInvalid
		}
		state.Roots = append(state.Roots, cloneDiagnosticsNamespaceRootRecord(record))
		sort.Slice(state.Roots, func(i, j int) bool {
			return bytes.Compare(state.Roots[i].FolderBinding, state.Roots[j].FolderBinding) < 0
		})
		state.Revision++
		return store.saveUnlocked(state)
	})
}

func diagnosticsNamespaceNextMountAlias(state diagnosticsNamespaceState) (string, error) {
	used := make(map[string]struct{}, len(state.Roots))
	for _, root := range state.Roots {
		used[root.MountAlias] = struct{}{}
	}
	for slot := 1; slot <= diagnosticsNamespaceMaxRoots; slot++ {
		alias := "namespace-" + strconv.Itoa(slot)
		if _, exists := used[alias]; !exists {
			return alias, nil
		}
	}
	return "", errDiagnosticsNamespaceStateInvalid
}

func (store *diagnosticsNamespaceStateStore) loadUnlocked() (diagnosticsNamespaceState, error) {
	pathInfo, err := os.Lstat(store.statePath)
	if err != nil {
		return diagnosticsNamespaceState{}, err
	}
	if !pathInfo.Mode().IsRegular() {
		return diagnosticsNamespaceState{}, errDiagnosticsNamespaceStateInvalid
	}
	file, err := os.Open(store.statePath)
	if err != nil {
		return diagnosticsNamespaceState{}, err
	}
	defer file.Close()
	info, statErr := file.Stat()
	currentPathInfo, pathErr := os.Lstat(store.statePath)
	if statErr != nil || pathErr != nil || !os.SameFile(pathInfo, info) || !os.SameFile(currentPathInfo, info) ||
		!info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > diagnosticsNamespaceStateBytes ||
		checkDiagnosticsPrivateFile(store.statePath, info) != nil {
		return diagnosticsNamespaceState{}, errDiagnosticsNamespaceStateInvalid
	}
	body, err := io.ReadAll(io.LimitReader(file, diagnosticsNamespaceStateBytes+1))
	if err != nil || len(body) > diagnosticsNamespaceStateBytes {
		return diagnosticsNamespaceState{}, errDiagnosticsNamespaceStateInvalid
	}
	var state diagnosticsNamespaceState
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return diagnosticsNamespaceState{}, errDiagnosticsNamespaceStateInvalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return diagnosticsNamespaceState{}, errDiagnosticsNamespaceStateInvalid
	}
	if state.FormatVersion > diagnosticsNamespaceStateFormat {
		return diagnosticsNamespaceState{}, errDiagnosticsNamespaceStateNewer
	}
	if err := validateDiagnosticsNamespaceState(state); err != nil {
		return diagnosticsNamespaceState{}, err
	}
	return state, nil
}

func (store *diagnosticsNamespaceStateStore) saveUnlocked(state diagnosticsNamespaceState) error {
	if err := validateDiagnosticsNamespaceState(state); err != nil {
		return err
	}
	body, err := json.Marshal(state)
	if err != nil || len(body) > diagnosticsNamespaceStateBytes {
		return errDiagnosticsNamespaceStateInvalid
	}
	temporary, err := os.CreateTemp(store.directory, ".namespace-v1-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if temporary.Chmod(0o600) != nil || writeAndSyncDiagnosticsNamespaceState(temporary, body) != nil || temporary.Close() != nil {
		return errDiagnosticsNamespaceStateInvalid
	}
	if store.hooks.beforeRename != nil {
		if err := store.hooks.beforeRename(); err != nil {
			return err
		}
	}
	if err := os.Rename(temporaryPath, store.statePath); err != nil {
		return err
	}
	removeTemporary = false
	if err := syncDiagnosticsDirectory(store.directory); err != nil {
		return err
	}
	if store.hooks.afterRename != nil {
		if err := store.hooks.afterRename(); err != nil {
			return err
		}
	}
	return nil
}

func writeAndSyncDiagnosticsNamespaceState(file *os.File, body []byte) error {
	if _, err := file.Write(body); err != nil {
		return err
	}
	return file.Sync()
}

func validateDiagnosticsNamespaceState(state diagnosticsNamespaceState) error {
	if state.FormatVersion != diagnosticsNamespaceStateFormat || state.Revision == 0 || len(state.Roots) > diagnosticsNamespaceMaxRoots {
		return errDiagnosticsNamespaceStateInvalid
	}
	seenFolders := make(map[string]struct{}, len(state.Roots))
	seenNamespaces := make(map[string]struct{}, len(state.Roots))
	seenAliases := make(map[string]struct{}, len(state.Roots))
	for _, record := range state.Roots {
		if err := validateDiagnosticsNamespaceRootRecord(record); err != nil {
			return err
		}
		folderKey := string(record.FolderBinding)
		namespaceKey := string(record.NamespaceID)
		if _, exists := seenFolders[folderKey]; exists {
			return errDiagnosticsNamespaceStateInvalid
		}
		if _, exists := seenNamespaces[namespaceKey]; exists {
			return errDiagnosticsNamespaceStateInvalid
		}
		if _, exists := seenAliases[record.MountAlias]; exists {
			return errDiagnosticsNamespaceStateInvalid
		}
		seenFolders[folderKey] = struct{}{}
		seenNamespaces[namespaceKey] = struct{}{}
		seenAliases[record.MountAlias] = struct{}{}
	}
	return nil
}

func validateDiagnosticsNamespaceRootRecord(record diagnosticsNamespaceRootRecord) error {
	if len(record.HomeserverBinding) != 32 || len(record.FolderBinding) != 32 || len(record.NamespaceID) != 32 ||
		len(record.RootManifestDigest) != 32 || record.Device == 0 || record.Inode == 0 {
		return errDiagnosticsNamespaceStateInvalid
	}
	validAlias := false
	for slot := 1; slot <= diagnosticsNamespaceMaxRoots; slot++ {
		if record.MountAlias == "namespace-"+strconv.Itoa(slot) {
			validAlias = true
			break
		}
	}
	if !validAlias {
		return errDiagnosticsNamespaceStateInvalid
	}
	return nil
}

func diagnosticsNamespaceRootRecordsEqual(left, right diagnosticsNamespaceRootRecord) bool {
	return bytes.Equal(left.HomeserverBinding, right.HomeserverBinding) &&
		bytes.Equal(left.FolderBinding, right.FolderBinding) && bytes.Equal(left.NamespaceID, right.NamespaceID) &&
		bytes.Equal(left.RootManifestDigest, right.RootManifestDigest) && left.MountAlias == right.MountAlias &&
		left.Device == right.Device && left.Inode == right.Inode
}

func cloneDiagnosticsNamespaceRootRecord(record diagnosticsNamespaceRootRecord) diagnosticsNamespaceRootRecord {
	return diagnosticsNamespaceRootRecord{
		HomeserverBinding: append([]byte(nil), record.HomeserverBinding...), FolderBinding: append([]byte(nil), record.FolderBinding...),
		NamespaceID: append([]byte(nil), record.NamespaceID...), RootManifestDigest: append([]byte(nil), record.RootManifestDigest...),
		MountAlias: record.MountAlias, Device: record.Device, Inode: record.Inode,
	}
}

func cloneDiagnosticsNamespaceState(state diagnosticsNamespaceState) diagnosticsNamespaceState {
	clone := diagnosticsNamespaceState{FormatVersion: state.FormatVersion, Revision: state.Revision, Roots: make([]diagnosticsNamespaceRootRecord, len(state.Roots))}
	for index, record := range state.Roots {
		clone.Roots[index] = cloneDiagnosticsNamespaceRootRecord(record)
	}
	return clone
}

func (store *diagnosticsNamespaceStateStore) withLock(action func() error) error {
	if err := withDiagnosticsCredentialFileLock(filepath.Join(store.directory, diagnosticsNamespaceLockFile), action); err != nil {
		if errors.Is(err, errDiagnosticsNamespaceStateNewer) {
			return err
		}
		if errors.Is(err, errDiagnosticsCredentialStateInvalid) {
			return errDiagnosticsNamespaceStateInvalid
		}
		return err
	}
	return nil
}
