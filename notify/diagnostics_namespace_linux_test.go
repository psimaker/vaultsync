//go:build linux

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestDiagnosticsNamespaceExplicitPreparationAndLifecycle(t *testing.T) {
	prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
	defer prepared.handle.Close()

	if err := prepared.handle.ScanFixedLayout(); err != nil {
		t.Fatal(err)
	}
	rootInfo, err := os.Stat(prepared.rootPath)
	if err != nil || rootInfo.Mode().Perm() != 0o700 {
		t.Fatalf("root mode = %v, %v", rootInfo, err)
	}
	for _, persistent := range []diagnosticsNamespacePath{
		diagnosticsNamespaceReadmePath(), diagnosticsNamespaceRootManifestPath(),
	} {
		_, info, err := prepared.handle.ReadImmutable(persistent)
		if err != nil || info.MountID != prepared.handle.Identity().MountID {
			t.Fatalf("persistent record read = %#v, %v", info, err)
		}
	}
	state, err := prepared.store.snapshot()
	if err != nil || len(state.Roots) != 1 || state.Roots[0].MountAlias != "namespace-1" ||
		state.Roots[0].Device != prepared.handle.Identity().Device || state.Roots[0].Inode != prepared.handle.Identity().Inode {
		t.Fatalf("persisted root state = %#v, %v", state, err)
	}
	if _, _, err := prepared.handle.ReadImmutable(diagnosticsNamespacePath{components: []string{"..", "user-note.txt"}}); !errors.Is(err, errDiagnosticsNamespaceUnsupported) {
		t.Fatalf("parent traversal = %v", err)
	}
	note, err := os.ReadFile(filepath.Join(prepared.parentPath, "user-note.txt"))
	if err != nil || string(note) != "user note sentinel" {
		t.Fatal("parent note changed")
	}
}

func TestDiagnosticsNamespaceCleanupIsExactBoundedAndIdempotent(t *testing.T) {
	prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
	defer prepared.handle.Close()
	initial, _ := decodeDiagnosticsNamespaceMessage(prepared.fixture.chain.Authorizations[0][0])
	installation, _ := initial.bytesField(8, 32)
	operation := bytes.Repeat([]byte{0x81}, 32)
	bodies := [][]byte{[]byte("opaque request filesystem fixture"), []byte("opaque attestation filesystem fixture"), []byte("opaque response filesystem fixture")}
	artifacts := make([]diagnosticsNamespaceOwnedArtifact, 0, 3)
	for kind, body := range bodies {
		path, err := diagnosticsNamespaceOperationPath(installation, operation, uint64(kind+1))
		if err != nil {
			t.Fatal(err)
		}
		artifact, err := prepared.handle.CreateImmutable(path, body)
		if err != nil {
			t.Fatal(err)
		}
		artifacts = append(artifacts, artifact)
	}

	backupPath := filepath.Join(prepared.parentPath, "backup", diagnosticsNamespaceRootName, "opaque-backup-copy")
	versionPath := filepath.Join(prepared.parentPath, ".stversions", diagnosticsNamespaceRootName, "opaque-version-copy")
	for _, path := range []string{backupPath, versionPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil || os.WriteFile(path, bodies[0], 0o600) != nil {
			t.Fatalf("create retained copy %s", path)
		}
	}
	operationDirectory := filepath.Dir(diagnosticsNamespaceTestAbsolutePath(prepared.rootPath, artifacts[0].path))
	conflictCopy := filepath.Join(operationDirectory, filepath.Base(diagnosticsNamespaceTestAbsolutePath(prepared.rootPath, artifacts[0].path))+".sync-conflict-20260713-120000-DEVICE")
	if err := os.WriteFile(conflictCopy, []byte("conflict copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := prepared.handle.ScanFixedLayout(); !errors.Is(err, errDiagnosticsNamespaceConflict) {
		t.Fatalf("conflict copy scan = %v", err)
	}

	result, err := prepared.handle.CleanupOwned(artifacts)
	if err != nil || result.Removed != 3 || result.Missing != 0 || result.Conflicts != 0 {
		t.Fatalf("cleanup result = %#v, %v", result, err)
	}
	result, err = prepared.handle.CleanupOwned(artifacts)
	if err != nil || result.Missing != 3 || result.Removed != 0 || result.Conflicts != 0 {
		t.Fatalf("idempotent cleanup result = %#v, %v", result, err)
	}
	if _, err := prepared.handle.CleanupOwned(append(artifacts, artifacts[0])); !errors.Is(err, errDiagnosticsNamespaceLimit) {
		t.Fatalf("unbounded cleanup = %v", err)
	}
	for _, path := range []string{backupPath, versionPath, conflictCopy, filepath.Join(prepared.parentPath, "user-note.txt")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("cleanup touched retained or user file %s: %v", path, err)
		}
	}
	for _, persistent := range []diagnosticsNamespacePath{diagnosticsNamespaceReadmePath(), diagnosticsNamespaceRootManifestPath()} {
		if _, _, err := prepared.handle.ReadImmutable(persistent); err != nil {
			t.Fatalf("cleanup touched persistent record: %v", err)
		}
	}
}

func TestDiagnosticsNamespaceLinksChangesAndCleanupRaceFailClosed(t *testing.T) {
	prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
	defer prepared.handle.Close()
	initial, _ := decodeDiagnosticsNamespaceMessage(prepared.fixture.chain.Authorizations[0][0])
	installation, _ := initial.bytesField(8, 32)

	changedPath, _ := diagnosticsNamespaceOperationPath(installation, bytes.Repeat([]byte{0x82}, 32), 1)
	changedArtifact, err := prepared.handle.CreateImmutable(changedPath, []byte("original body"))
	if err != nil {
		t.Fatal(err)
	}
	changedAbsolute := diagnosticsNamespaceTestAbsolutePath(prepared.rootPath, changedPath)
	if err := os.WriteFile(changedAbsolute, []byte("replacement body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result, err := prepared.handle.CleanupOwned([]diagnosticsNamespaceOwnedArtifact{changedArtifact}); !errors.Is(err, errDiagnosticsNamespaceConflict) || result.Conflicts != 1 {
		t.Fatalf("changed cleanup = %#v, %v", result, err)
	}
	if body, _ := os.ReadFile(changedAbsolute); string(body) != "replacement body" {
		t.Fatal("changed artifact was deleted or overwritten")
	}

	hardlinkPath, _ := diagnosticsNamespaceOperationPath(installation, bytes.Repeat([]byte{0x83}, 32), 1)
	hardlinkArtifact, err := prepared.handle.CreateImmutable(hardlinkPath, []byte("hardlink body"))
	if err != nil {
		t.Fatal(err)
	}
	hardlinkAbsolute := diagnosticsNamespaceTestAbsolutePath(prepared.rootPath, hardlinkPath)
	hardlinkCopy := hardlinkAbsolute + ".backup"
	if err := os.Link(hardlinkAbsolute, hardlinkCopy); err != nil {
		t.Fatal(err)
	}
	if result, err := prepared.handle.CleanupOwned([]diagnosticsNamespaceOwnedArtifact{hardlinkArtifact}); !errors.Is(err, errDiagnosticsNamespaceConflict) || result.Conflicts != 1 {
		t.Fatalf("hardlink cleanup = %#v, %v", result, err)
	}
	for _, path := range []string{hardlinkAbsolute, hardlinkCopy} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("hardlink target changed: %v", err)
		}
	}

	symlinkPath, _ := diagnosticsNamespaceOperationPath(installation, bytes.Repeat([]byte{0x84}, 32), 1)
	symlinkArtifact, err := prepared.handle.CreateImmutable(symlinkPath, []byte("symlink original"))
	if err != nil {
		t.Fatal(err)
	}
	symlinkAbsolute := diagnosticsNamespaceTestAbsolutePath(prepared.rootPath, symlinkPath)
	if err := os.Remove(symlinkAbsolute); err != nil || os.Symlink(filepath.Join(prepared.parentPath, "user-note.txt"), symlinkAbsolute) != nil {
		t.Fatal("replace operation with symlink")
	}
	if result, err := prepared.handle.CleanupOwned([]diagnosticsNamespaceOwnedArtifact{symlinkArtifact}); !errors.Is(err, errDiagnosticsNamespaceConflict) || result.Conflicts != 1 {
		t.Fatalf("symlink cleanup = %#v, %v", result, err)
	}
	note, _ := os.ReadFile(filepath.Join(prepared.parentPath, "user-note.txt"))
	if string(note) != "user note sentinel" {
		t.Fatal("symlink escaped to parent note")
	}

	racePath, _ := diagnosticsNamespaceOperationPath(installation, bytes.Repeat([]byte{0x85}, 32), 1)
	raceArtifact, err := prepared.handle.CreateImmutable(racePath, []byte("race original"))
	if err != nil {
		t.Fatal(err)
	}
	raceAbsolute := diagnosticsNamespaceTestAbsolutePath(prepared.rootPath, racePath)
	replacement := raceAbsolute + ".replacement"
	originalSaved := raceAbsolute + ".original"
	if err := os.WriteFile(replacement, []byte("race replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	platform, _ := prepared.handle.linux()
	platform.beforeFinalCleanupCheck = func() {
		platform.beforeFinalCleanupCheck = nil
		if err := os.Rename(raceAbsolute, originalSaved); err != nil {
			t.Errorf("save raced artifact: %v", err)
			return
		}
		if err := os.Rename(replacement, raceAbsolute); err != nil {
			t.Errorf("install raced replacement: %v", err)
		}
	}
	if result, err := prepared.handle.CleanupOwned([]diagnosticsNamespaceOwnedArtifact{raceArtifact}); !errors.Is(err, errDiagnosticsNamespaceConflict) || result.Conflicts != 1 {
		t.Fatalf("raced cleanup = %#v, %v", result, err)
	}
	for _, path := range []string{raceAbsolute, originalSaved} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("raced file was deleted: %s: %v", path, err)
		}
	}
}

func TestDiagnosticsNamespacePreparationCollisionsCrashAndIgnoreAreNonMutating(t *testing.T) {
	fixture := diagnosticsNamespaceGoldenFixture(t)
	for name, setup := range map[string]func(string) error{
		"existing-file": func(parent string) error {
			return os.WriteFile(filepath.Join(parent, diagnosticsNamespaceRootName), []byte("user collision"), 0o600)
		},
		"existing-directory": func(parent string) error {
			if err := os.Mkdir(filepath.Join(parent, diagnosticsNamespaceRootName), 0o700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(parent, diagnosticsNamespaceRootName, "user-note.md"), []byte("user collision"), 0o600)
		},
		"existing-symlink": func(parent string) error {
			outside := filepath.Join(filepath.Dir(parent), "outside-collision")
			if err := os.WriteFile(outside, []byte("outside collision"), 0o600); err != nil {
				return err
			}
			return os.Symlink(outside, filepath.Join(parent, diagnosticsNamespaceRootName))
		},
	} {
		t.Run(name, func(t *testing.T) {
			parent, store := diagnosticsNamespaceLinuxParentAndStore(t)
			if err := setup(parent); err != nil {
				t.Fatal(err)
			}
			before := diagnosticsNamespaceTestTree(t, filepath.Join(parent, diagnosticsNamespaceRootName))
			request := diagnosticsNamespaceLinuxRequest(fixture, parent, store)
			if _, err := prepareDiagnosticsNamespaceExplicit(request); !errors.Is(err, errDiagnosticsNamespaceCollision) {
				t.Fatalf("collision result = %v", err)
			}
			after := diagnosticsNamespaceTestTree(t, filepath.Join(parent, diagnosticsNamespaceRootName))
			if before != after {
				t.Fatalf("collision tree changed: before %q after %q", before, after)
			}
		})
	}

	t.Run("crash-after-root-create", func(t *testing.T) {
		parent, store := diagnosticsNamespaceLinuxParentAndStore(t)
		request := diagnosticsNamespaceLinuxRequest(fixture, parent, store)
		crash := errors.New("simulated installer crash")
		request.hooks.afterRootCreate = func() error { return crash }
		if _, err := prepareDiagnosticsNamespaceExplicit(request); !errors.Is(err, crash) {
			t.Fatalf("crash result = %v", err)
		}
		root := filepath.Join(parent, diagnosticsNamespaceRootName)
		if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
			t.Fatalf("crash root = %v, %v", entries, err)
		}
		request.hooks = diagnosticsNamespaceInstallerHooks{}
		if _, err := prepareDiagnosticsNamespaceExplicit(request); !errors.Is(err, errDiagnosticsNamespaceCollision) {
			t.Fatalf("crash rerun result = %v", err)
		}
		if entries, _ := os.ReadDir(root); len(entries) != 0 {
			t.Fatal("rerun adopted or changed partial root")
		}
	})

	t.Run("parent-swap-after-create", func(t *testing.T) {
		parent, store := diagnosticsNamespaceLinuxParentAndStore(t)
		request := diagnosticsNamespaceLinuxRequest(fixture, parent, store)
		originalParent := parent + ".original"
		request.hooks.afterRootCreate = func() error {
			if err := os.Rename(parent, originalParent); err != nil {
				return err
			}
			if err := os.Mkdir(parent, 0o700); err != nil {
				return err
			}
			return os.Mkdir(filepath.Join(parent, ".stfolder"), 0o700)
		}
		if _, err := prepareDiagnosticsNamespaceExplicit(request); !errors.Is(err, errDiagnosticsNamespaceUnsupported) {
			t.Fatalf("parent swap result = %v", err)
		}
		originalRoot := filepath.Join(originalParent, diagnosticsNamespaceRootName)
		if entries, err := os.ReadDir(originalRoot); err != nil || len(entries) != 0 {
			t.Fatalf("original root after parent swap = %v, %v", entries, err)
		}
		if _, err := os.Stat(filepath.Join(parent, diagnosticsNamespaceRootName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("replacement parent was written")
		}
	})

	for name, mutate := range map[string]func(*diagnosticsNamespacePreparationRequest){
		"declined":                func(request *diagnosticsNamespacePreparationRequest) { request.operatorConfirmed = false },
		"ignore-match":            func(request *diagnosticsNamespacePreparationRequest) { request.ignore.AnyMatched = true },
		"include-unsupported":     func(request *diagnosticsNamespacePreparationRequest) { request.ignore.IncludesSupported = false },
		"unreadable-ignore":       func(request *diagnosticsNamespacePreparationRequest) { request.ignore.Evaluated = false },
		"wrong-ignore-generation": func(request *diagnosticsNamespacePreparationRequest) { request.ignore.Fingerprint[0] ^= 1 },
	} {
		t.Run(name, func(t *testing.T) {
			parent, store := diagnosticsNamespaceLinuxParentAndStore(t)
			request := diagnosticsNamespaceLinuxRequest(fixture, parent, store)
			mutate(&request)
			if _, err := prepareDiagnosticsNamespaceExplicit(request); !errors.Is(err, errDiagnosticsNamespaceUnsupported) {
				t.Fatalf("unsupported result = %v", err)
			}
			if _, err := os.Stat(filepath.Join(parent, diagnosticsNamespaceRootName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("declined or unsupported preparation created a namespace")
			}
		})
	}
}

func TestDiagnosticsNamespaceRootAndComponentSwapsInterrupt(t *testing.T) {
	prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
	defer prepared.handle.Close()
	original := prepared.rootPath + ".original"
	if err := os.Rename(prepared.rootPath, original); err != nil || os.Mkdir(prepared.rootPath, 0o700) != nil {
		t.Fatal("swap root")
	}
	if _, _, err := prepared.handle.ReadImmutable(diagnosticsNamespaceRootManifestPath()); !errors.Is(err, errDiagnosticsNamespaceUnsupported) {
		t.Fatalf("root swap = %v", err)
	}
	if _, err := os.Stat(filepath.Join(prepared.rootPath, diagnosticsNamespaceRootManifestName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("replacement root was accessed")
	}
	if _, err := os.Stat(filepath.Join(original, diagnosticsNamespaceRootManifestName)); err != nil {
		t.Fatal("original root was changed")
	}
}

func TestDiagnosticsNamespaceFixedLayoutRejectsEpochGapsAndDetachedChains(t *testing.T) {
	t.Run("helper-epoch-filename-mismatch", func(t *testing.T) {
		prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
		defer prepared.handle.Close()
		source, _ := diagnosticsNamespaceHelperEpochPath(2)
		destination, _ := diagnosticsNamespaceHelperEpochPath(3)
		if err := os.Rename(
			diagnosticsNamespaceTestAbsolutePath(prepared.rootPath, source),
			diagnosticsNamespaceTestAbsolutePath(prepared.rootPath, destination),
		); err != nil {
			t.Fatal(err)
		}
		if err := prepared.handle.ScanFixedLayout(); !errors.Is(err, errDiagnosticsNamespaceConflict) {
			t.Fatalf("renamed helper epoch result = %v", err)
		}
	})

	t.Run("authorization-detached-from-missing-helper-epoch", func(t *testing.T) {
		prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
		defer prepared.handle.Close()
		helperEpoch, _ := diagnosticsNamespaceHelperEpochPath(2)
		if err := os.Remove(diagnosticsNamespaceTestAbsolutePath(prepared.rootPath, helperEpoch)); err != nil {
			t.Fatal(err)
		}
		if err := prepared.handle.ScanFixedLayout(); !errors.Is(err, errDiagnosticsNamespaceConflict) {
			t.Fatalf("detached authorization result = %v", err)
		}
	})

	t.Run("authorization-epoch-fork", func(t *testing.T) {
		prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
		defer prepared.handle.Close()
		initial, _ := decodeDiagnosticsNamespaceMessage(prepared.fixture.chain.Authorizations[0][0])
		installation, _ := initial.bytesField(8, 32)
		source, _ := diagnosticsNamespaceAuthorizationEpochPath(installation, 2)
		destination, _ := diagnosticsNamespaceAuthorizationEpochPath(installation, 3)
		body, err := os.ReadFile(diagnosticsNamespaceTestAbsolutePath(prepared.rootPath, source))
		if err != nil || os.WriteFile(diagnosticsNamespaceTestAbsolutePath(prepared.rootPath, destination), body, 0o600) != nil {
			t.Fatal("create signed authorization fork")
		}
		if err := prepared.handle.ScanFixedLayout(); !errors.Is(err, errDiagnosticsNamespaceConflict) {
			t.Fatalf("authorization fork result = %v", err)
		}
	})
}

func TestDiagnosticsNamespaceSparseAndMagicLinkEntriesFailClosed(t *testing.T) {
	prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
	defer prepared.handle.Close()
	initial, _ := decodeDiagnosticsNamespaceMessage(prepared.fixture.chain.Authorizations[0][0])
	installation, _ := initial.bytesField(8, 32)
	sparsePath, _ := diagnosticsNamespaceOperationPath(installation, bytes.Repeat([]byte{0x86}, 32), 1)
	sparseAbsolute := diagnosticsNamespaceTestAbsolutePath(prepared.rootPath, sparsePath)
	sparse, err := os.OpenFile(sparseAbsolute, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sparse.Seek(8191, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := sparse.Write([]byte{1}); err != nil || sparse.Close() != nil {
		t.Fatal("create sparse fixture")
	}
	if _, _, err := prepared.handle.ReadImmutable(sparsePath); !errors.Is(err, errDiagnosticsNamespaceConflict) {
		t.Fatalf("sparse artifact = %v", err)
	}
	if _, err := os.Stat(sparseAbsolute); err != nil {
		t.Fatal("sparse artifact was changed")
	}

	paths, _ := diagnosticsNamespaceAuthorizationPaths(installation)
	operationsAbsolute := diagnosticsNamespaceTestAbsolutePath(prepared.rootPath, paths[2])
	originalOperations := operationsAbsolute + ".original"
	if err := os.Rename(operationsAbsolute, originalOperations); err != nil || os.Symlink("/proc/self/fd/0", operationsAbsolute) != nil {
		t.Fatal("create magic-link component")
	}
	probePath, _ := diagnosticsNamespaceOperationPath(installation, bytes.Repeat([]byte{0x87}, 32), 1)
	if _, _, err := prepared.handle.ReadImmutable(probePath); !errors.Is(err, errDiagnosticsNamespaceConflict) {
		t.Fatalf("magic-link component = %v", err)
	}
	if _, err := os.Stat(originalOperations); err != nil {
		t.Fatal("original operations directory was changed")
	}
}

func TestDiagnosticsNamespaceConcurrentPreparationCreatesExactlyOneRoot(t *testing.T) {
	fixture := diagnosticsNamespaceGoldenFixture(t)
	base := t.TempDir()
	parent := filepath.Join(base, "vault")
	if err := os.Mkdir(parent, 0o700); err != nil || os.Mkdir(filepath.Join(parent, ".stfolder"), 0o700) != nil {
		t.Fatal("create parent")
	}
	stores := make([]*diagnosticsNamespaceStateStore, 2)
	for index := range stores {
		var err error
		stores[index], err = openDiagnosticsNamespaceStateStore(filepath.Join(base, fmt.Sprintf("state-%d", index)))
		if err != nil {
			t.Fatal(err)
		}
	}
	errs := make([]error, 2)
	var wait sync.WaitGroup
	for index := range errs {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, errs[index] = prepareDiagnosticsNamespaceExplicit(diagnosticsNamespaceLinuxRequest(fixture, parent, stores[index]))
		}()
	}
	wait.Wait()
	successes, collisions := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, errDiagnosticsNamespaceCollision):
			collisions++
		default:
			t.Fatalf("unexpected concurrent result %v", err)
		}
	}
	if successes != 1 || collisions != 1 {
		t.Fatalf("concurrent results success=%d collision=%d", successes, collisions)
	}
}

func TestDiagnosticsNamespaceDockerInstallerPhase(t *testing.T) {
	parent := os.Getenv("VAULTSYNC_M4_DOCKER_PARENT")
	statePath := os.Getenv("VAULTSYNC_M4_DOCKER_STATE")
	if parent == "" || statePath == "" {
		t.Skip("Docker installer proof environment not provided")
	}
	fixture := diagnosticsNamespaceGoldenFixture(t)
	store, err := openDiagnosticsNamespaceStateStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	request := diagnosticsNamespaceLinuxRequest(fixture, parent, store)
	if _, err := prepareDiagnosticsNamespaceExplicit(request); err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(parent, diagnosticsNamespaceRootName)
	handle, err := openDiagnosticsNamespaceRoot(rootPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if err := appendDiagnosticsNamespaceHelperEpoch(handle, fixture.chain); err != nil {
		t.Fatal(err)
	}
	initialChain := fixture.chain
	initialChain.Authorizations = [][][]byte{{fixture.chain.Authorizations[0][0]}}
	if err := installDiagnosticsNamespaceAuthorization(handle, initialChain, 0); err != nil {
		t.Fatal(err)
	}
	if err := appendDiagnosticsNamespaceAuthorizationEpoch(handle, fixture.chain, 0); err != nil {
		t.Fatal(err)
	}
}

func TestDiagnosticsNamespaceDockerRuntimeConfinement(t *testing.T) {
	rootPath := os.Getenv("VAULTSYNC_M4_DOCKER_ROOT")
	statePath := os.Getenv("VAULTSYNC_M4_DOCKER_STATE")
	configPath := os.Getenv("VAULTSYNC_M4_DOCKER_CONFIG")
	if rootPath == "" || statePath == "" || configPath == "" {
		t.Skip("Docker runtime proof environment not provided")
	}
	store, err := openDiagnosticsNamespaceStateStore(statePath)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.snapshot()
	if err != nil || len(state.Roots) != 1 {
		t.Fatalf("state = %#v, %v", state, err)
	}
	handle, err := openDiagnosticsNamespaceRoot(rootPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	identity := handle.Identity()
	if identity.Device != state.Roots[0].Device || identity.Inode != state.Roots[0].Inode {
		t.Fatalf("runtime root identity changed: runtime=%#v state=%#v", identity, state.Roots[0])
	}
	if err := handle.ValidateRootRecord(state.Roots[0]); err != nil {
		t.Fatalf("runtime root binding validation: %v", err)
	}
	if err := handle.ScanFixedLayout(); err != nil {
		t.Fatal(err)
	}
	configBefore, err := os.ReadFile(configPath)
	if err != nil || len(configBefore) == 0 {
		t.Fatalf("read-only config fixture unavailable: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("must fail"), 0o600); err == nil {
		t.Fatal("config mount is writable")
	}
	fixture := diagnosticsNamespaceGoldenFixture(t)
	initial, _ := decodeDiagnosticsNamespaceMessage(fixture.chain.Authorizations[0][0])
	installation, _ := initial.bytesField(8, 32)
	operationPath, _ := diagnosticsNamespaceOperationPath(installation, bytes.Repeat([]byte{0x91}, 32), 1)
	artifact, err := handle.CreateImmutable(operationPath, []byte("Docker host-bind operation fixture"))
	if err != nil {
		t.Fatal(err)
	}
	if result, err := handle.CleanupOwned([]diagnosticsNamespaceOwnedArtifact{artifact}); err != nil || result.Removed != 1 {
		t.Fatalf("Docker exact-bind cleanup = %#v, %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(rootPath, diagnosticsNamespaceStateFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("state store is not separate from synchronized namespace")
	}
	if _, err := os.Stat(filepath.Join(rootPath, "..", "user-note.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("runtime can see the parent vault")
	}
	if err := os.WriteFile("/runtime-root-write", []byte("must fail"), 0o600); err == nil {
		t.Fatal("container root filesystem is writable")
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil || !bytes.Contains(status, []byte("CapEff:\t0000000000000000")) || !bytes.Contains(status, []byte("NoNewPrivs:\t1")) {
		t.Fatalf("capability/no-new-privileges status unavailable: %v", err)
	}
	configAfter, err := os.ReadFile(configPath)
	if err != nil || !bytes.Equal(configBefore, configAfter) {
		t.Fatal("runtime changed read-only config fixture")
	}
}

func TestDiagnosticsNamespaceDockerMountSwapRejected(t *testing.T) {
	rootPath := os.Getenv("VAULTSYNC_M4_DOCKER_MOUNT_SWAP_ROOT")
	if rootPath == "" {
		t.Skip("Docker mount-swap proof environment not provided")
	}
	handle, err := openDiagnosticsNamespaceRoot(rootPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if err := handle.ScanFixedLayout(); !errors.Is(err, errDiagnosticsNamespaceConflict) {
		t.Fatalf("child mount boundary result = %v", err)
	}
}

type diagnosticsNamespaceLinuxFixture struct {
	parentPath string
	rootPath   string
	store      *diagnosticsNamespaceStateStore
	handle     *diagnosticsNamespaceRootHandle
	fixture    diagnosticsNamespaceGoldenData
}

func prepareDiagnosticsNamespaceLinuxFixture(t testing.TB) diagnosticsNamespaceLinuxFixture {
	t.Helper()
	fixture := diagnosticsNamespaceGoldenFixture(t)
	parent, store := diagnosticsNamespaceLinuxParentAndStore(t)
	request := diagnosticsNamespaceLinuxRequest(fixture, parent, store)
	if _, err := prepareDiagnosticsNamespaceExplicit(request); err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(parent, diagnosticsNamespaceRootName)
	handle, err := openDiagnosticsNamespaceRoot(rootPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendDiagnosticsNamespaceHelperEpoch(handle, fixture.chain); err != nil {
		t.Fatal(err)
	}
	initialChain := fixture.chain
	initialChain.Authorizations = [][][]byte{{fixture.chain.Authorizations[0][0]}}
	if err := installDiagnosticsNamespaceAuthorization(handle, initialChain, 0); err != nil {
		t.Fatal(err)
	}
	if err := appendDiagnosticsNamespaceAuthorizationEpoch(handle, fixture.chain, 0); err != nil {
		t.Fatal(err)
	}
	return diagnosticsNamespaceLinuxFixture{parentPath: parent, rootPath: rootPath, store: store, handle: handle, fixture: fixture}
}

func diagnosticsNamespaceLinuxParentAndStore(t testing.TB) (string, *diagnosticsNamespaceStateStore) {
	t.Helper()
	base := t.TempDir()
	parent := filepath.Join(base, "vault")
	if err := os.Mkdir(parent, 0o700); err != nil || os.Mkdir(filepath.Join(parent, ".stfolder"), 0o700) != nil ||
		os.WriteFile(filepath.Join(parent, "user-note.txt"), []byte("user note sentinel"), 0o600) != nil {
		t.Fatal("create Syncthing fixture")
	}
	store, err := openDiagnosticsNamespaceStateStore(filepath.Join(base, "state"))
	if err != nil {
		t.Fatal(err)
	}
	return parent, store
}

func diagnosticsNamespaceLinuxRequest(fixture diagnosticsNamespaceGoldenData, parent string, store *diagnosticsNamespaceStateStore) diagnosticsNamespacePreparationRequest {
	return diagnosticsNamespacePreparationRequest{
		parentPath: parent, operatorConfirmed: true,
		homeserverBinding: bytes.Repeat([]byte{0x05}, 32), folderBinding: bytes.Repeat([]byte{0x06}, 32),
		enablement: fixture.chain.Enablement, rootManifest: fixture.chain.RootManifest,
		ignore: diagnosticsNamespaceIgnoreVerdict{
			Evaluated: true, IncludesSupported: true, Fingerprint: diagnosticsNamespaceIgnoreFingerprint(),
		},
		stateStore: store,
	}
}

func diagnosticsNamespaceTestAbsolutePath(root string, path diagnosticsNamespacePath) string {
	return filepath.Join(append([]string{root}, path.components...)...)
}

func diagnosticsNamespaceTestTree(t testing.TB, root string) string {
	t.Helper()
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return "missing"
	}
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		body, _ := os.ReadFile(root)
		return fmt.Sprintf("file:%o:%s", info.Mode().Perm(), body)
	}
	var entries []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(root, path)
		entries = append(entries, relative+":"+entry.Type().String())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return strings.Join(entries, "|")
}
