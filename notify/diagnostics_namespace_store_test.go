package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
)

func TestDiagnosticsNamespaceStateStoreIsPrivateAtomicAndPathFree(t *testing.T) {
	parent := t.TempDir()
	directory := filepath.Join(parent, "namespace-state")
	store, err := openDiagnosticsNamespaceStateStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	directoryInfo, _ := os.Stat(directory)
	stateInfo, _ := os.Stat(filepath.Join(directory, diagnosticsNamespaceStateFile))
	lockInfo, _ := os.Stat(filepath.Join(directory, diagnosticsNamespaceLockFile))
	if directoryInfo.Mode().Perm() != 0o700 || stateInfo.Mode().Perm() != 0o600 || lockInfo.Mode().Perm() != 0o600 {
		t.Fatalf("private modes = directory %o state %o lock %o", directoryInfo.Mode().Perm(), stateInfo.Mode().Perm(), lockInfo.Mode().Perm())
	}

	record := diagnosticsNamespaceTestRootRecord(1)
	if err := store.registerRoot(record); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(directory, diagnosticsNamespaceStateFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range [][]byte{
		[]byte(parent), []byte("folder-name-sentinel"), []byte("vault-path-sentinel"),
		[]byte("config.xml"), []byte("api-key-sentinel"), []byte("VaultSync Diagnostics"),
	} {
		if bytes.Contains(before, forbidden) {
			t.Fatalf("namespace state persisted forbidden value %q", forbidden)
		}
	}

	crash := errors.New("simulated crash before rename")
	store.hooks.beforeRename = func() error { return crash }
	if err := store.registerRoot(diagnosticsNamespaceTestRootRecord(2)); !errors.Is(err, crash) {
		t.Fatalf("before-rename crash = %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(directory, diagnosticsNamespaceStateFile))
	if !bytes.Equal(before, after) {
		t.Fatal("failed atomic update changed persisted state")
	}
	temporary, err := filepath.Glob(filepath.Join(directory, ".namespace-v1-*.tmp"))
	if err != nil || len(temporary) != 0 {
		t.Fatalf("temporary files after crash = %v, %v", temporary, err)
	}

	store.hooks.beforeRename = nil
	crashAfter := errors.New("simulated crash after rename")
	store.hooks.afterRename = func() error { return crashAfter }
	if err := store.registerRoot(diagnosticsNamespaceTestRootRecord(2)); !errors.Is(err, crashAfter) {
		t.Fatalf("after-rename crash = %v", err)
	}
	reopened, err := openDiagnosticsNamespaceStateStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reopened.snapshot()
	if err != nil || len(snapshot.Roots) != 2 || snapshot.Revision != 3 {
		t.Fatalf("recovered state = %#v, %v", snapshot, err)
	}
}

func TestDiagnosticsNamespaceStateStoreSerializesRacesAndCapsRoots(t *testing.T) {
	store, err := openDiagnosticsNamespaceStateStore(filepath.Join(t.TempDir(), "namespace-state"))
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	errorsByIndex := make([]error, diagnosticsNamespaceMaxRoots)
	for index := range diagnosticsNamespaceMaxRoots {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsByIndex[index] = store.registerRoot(diagnosticsNamespaceTestRootRecord(byte(index + 1)))
		}()
	}
	wait.Wait()
	for index, err := range errorsByIndex {
		if err != nil {
			t.Fatalf("concurrent root %d: %v", index, err)
		}
	}
	snapshot, err := store.snapshot()
	if err != nil || len(snapshot.Roots) != diagnosticsNamespaceMaxRoots {
		t.Fatalf("snapshot roots = %d, %v", len(snapshot.Roots), err)
	}
	if _, err := diagnosticsNamespaceNextMountAlias(snapshot); err == nil {
		t.Fatal("full store returned another mount alias")
	}
	if err := store.registerRoot(diagnosticsNamespaceTestRootRecord(9)); err == nil {
		t.Fatal("store accepted more than eight roots")
	}

	duplicate := diagnosticsNamespaceTestRootRecord(1)
	duplicate.RootManifestDigest[0] ^= 1
	if err := store.registerRoot(duplicate); err == nil {
		t.Fatal("store replaced an existing folder binding")
	}
	if err := store.registerRoot(snapshot.Roots[0]); err != nil {
		t.Fatalf("exact replay was not idempotent: %v", err)
	}
}

func TestDiagnosticsNamespaceStateStoreRejectsLinksUnknownAndNewerState(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "namespace-state")
	store, err := openDiagnosticsNamespaceStateStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(directory, diagnosticsNamespaceStateFile)
	hardlink := filepath.Join(directory, "state-hardlink")
	if err := os.Link(statePath, hardlink); err != nil {
		t.Fatal(err)
	}
	if _, err := store.snapshot(); err == nil {
		t.Fatal("hard-linked state file was accepted")
	}
	if err := os.Remove(hardlink); err != nil {
		t.Fatal(err)
	}

	valid, _ := os.ReadFile(statePath)
	if err := os.WriteFile(statePath, append(valid[:len(valid)-1], []byte(`,"unknown":1}`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.snapshot(); err == nil {
		t.Fatal("unknown state field was accepted")
	}
	if err := os.WriteFile(statePath, []byte(`{"format_version":2,"revision":1,"roots":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.snapshot(); !errors.Is(err, errDiagnosticsNamespaceStateNewer) {
		t.Fatalf("newer state error = %v", err)
	}

	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte(`{"format_version":1,"revision":1,"roots":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, statePath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.snapshot(); err == nil {
		t.Fatal("symlinked state file was accepted")
	}
}

func TestDiagnosticsNamespaceStateRemainsSeparateFromCredentialsAndRollback(t *testing.T) {
	parent := t.TempDir()
	credentialDirectory := filepath.Join(parent, "credentials")
	namespaceDirectory := filepath.Join(parent, "namespace")
	deviceDigest := bytes.Repeat([]byte{0xd1}, 32)
	credentialStore, err := openDiagnosticsCredentialStore(credentialDirectory, deviceDigest, bytes.NewReader(bytes.Repeat([]byte{0xa1}, 4096)))
	if err != nil {
		t.Fatal(err)
	}
	credentialBefore, err := credentialStore.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	namespaceStore, err := openDiagnosticsNamespaceStateStore(namespaceDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if err := namespaceStore.registerRoot(diagnosticsNamespaceTestRootRecord(1)); err != nil {
		t.Fatal(err)
	}
	credentialAfter, err := credentialStore.snapshot()
	if err != nil || !reflect.DeepEqual(credentialBefore, credentialAfter) {
		t.Fatal("namespace state changed credential state")
	}
	if _, err := os.Stat(filepath.Join(credentialDirectory, diagnosticsNamespaceStateFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("namespace state leaked into credential store")
	}
	if _, err := os.Stat(filepath.Join(namespaceDirectory, diagnosticsCredentialStateFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("credential state leaked into namespace store")
	}
	if _, err := os.Stat(filepath.Join(namespaceDirectory, diagnosticsNamespaceStateFile)); err != nil {
		t.Fatal("rollback must leave namespace state intact")
	}
}

func diagnosticsNamespaceTestRootRecord(value byte) diagnosticsNamespaceRootRecord {
	return diagnosticsNamespaceRootRecord{
		HomeserverBinding: bytes.Repeat([]byte{0x51}, 32),
		FolderBinding:     bytes.Repeat([]byte{value}, 32), NamespaceID: bytes.Repeat([]byte{value + 0x10}, 32),
		RootManifestDigest: bytes.Repeat([]byte{value + 0x20}, 32), MountAlias: "namespace-" + strconv.Itoa(int(value)),
		Device: uint64(value) + 100, Inode: uint64(value) + 200,
	}
}
