package main

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDiagnosticsRuntimeMountBindingMatchesInstallerVector(t *testing.T) {
	binding := diagnosticsRuntimeMountBinding(
		"vault-a", "/srv/vault", "namespace-1",
		diagnosticsNamespaceFileIdentity{Device: 41, Inode: 42},
	)
	const expected = "00120a2a9e059b9cf5c304a64e9d9c47f4a98172320ac0823369820d3166b790"
	if hex.EncodeToString(binding[:]) != expected {
		t.Fatalf("mount binding = %x", binding)
	}
	if parsed, ok := parseDiagnosticsRuntimeMountBinding(expected); !ok || parsed != binding {
		t.Fatalf("parsed mount binding = %x, %v", parsed, ok)
	}
	if _, ok := parseDiagnosticsRuntimeMountBinding(strings.ToUpper(expected)); ok {
		t.Fatal("non-canonical uppercase mount binding was accepted")
	}
}

func TestDiagnosticsRuntimeTestMountOverrideCannotBypassBinding(t *testing.T) {
	identity := diagnosticsNamespaceFileIdentity{Device: 41, Inode: 42}
	config := &diagnosticsRuntimeConfig{
		Folders:            []diagnosticsRuntimeFolderConfig{{FolderID: "vault-a", MountAlias: "namespace-1"}},
		mountBindings:      make(map[string][32]byte),
		mountPathOverrides: map[string]string{"namespace-1": t.TempDir()},
	}
	if config.runtimeMountBindingsValid() || config.mountBindingMatches("vault-a", "/srv/vault", "namespace-1", identity) {
		t.Fatal("test mount override bypassed a missing mount binding")
	}
	config.mountBindings["namespace-1"] = diagnosticsRuntimeMountBinding("vault-a", "/srv/vault", "namespace-1", identity)
	if !config.runtimeMountBindingsValid() || !config.mountBindingMatches("vault-a", "/srv/vault", "namespace-1", identity) {
		t.Fatal("exact test mount binding was rejected")
	}
	if config.mountBindingMatches("vault-a", "/srv/other", "namespace-1", identity) {
		t.Fatal("test mount override accepted a different Syncthing path")
	}
}

func TestDiagnosticsRuntimeMutationLockSerializesIndependentProcesses(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "credentials")
	deviceDigest := strings.Repeat("d", 32)
	first, err := openDiagnosticsCredentialStore(directory, []byte(deviceDigest), nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := openDiagnosticsCredentialStore(directory, []byte(deviceDigest), nil)
	if err != nil {
		t.Fatal(err)
	}
	enteredFirst := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- withDiagnosticsRuntimeMutationLock(first, func() error {
			close(enteredFirst)
			<-releaseFirst
			return nil
		})
	}()
	select {
	case <-enteredFirst:
	case <-time.After(2 * time.Second):
		t.Fatal("first runtime mutation lock was not acquired")
	}

	attemptingSecond := make(chan struct{})
	enteredSecond := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(attemptingSecond)
		secondDone <- withDiagnosticsRuntimeMutationLock(second, func() error {
			close(enteredSecond)
			return nil
		})
	}()
	<-attemptingSecond
	select {
	case <-enteredSecond:
		t.Fatal("independent runtime mutation entered while the lock was held")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-enteredSecond:
	case <-time.After(2 * time.Second):
		t.Fatal("second runtime mutation did not continue after release")
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(directory, diagnosticsRuntimeMutationLockFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("runtime mutation lock mode = %v, %v", info, err)
	}
}

func TestDiagnosticsDockerInstallerPinsSupportedLeastPrivilegeBoundary(t *testing.T) {
	path := filepath.Join(diagnosticsTestRepoRoot(t), "notify", "scripts", "diagnostics-docker.sh")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	script := string(body)
	for _, required := range []string{
		"VAULTSYNC_DIAGNOSTICS_SUPPORTED_HOST_CONFIRMED",
		"VAULTSYNC_DIAGNOSTICS_ENABLE_CONFIRMED",
		"exact namespace: %s",
		"accepting opaque retention in peers, backups, versions, conflicts, and tombstones",
		"docker image inspect --format '{{.Id}}'",
		"--network host --read-only --cap-drop ALL --security-opt no-new-privileges",
		"dst=/config/runtime.json,readonly",
		"dst=/syncthing/config.xml,readonly",
		"dst=/diagnostics/$alias_value",
		"eu.vaultsync.runtime/v1/mount-binding\\000",
		"VAULTSYNC_DIAGNOSTICS_MOUNT_BINDING_$mount_slot",
		"--diagnostics-source-device",
		"--diagnostics-source-inode",
		"SYNCTHING_API_URL must be an explicit loopback HTTP endpoint on this host",
		"diagnostics packaging is unsupported on Windows/WSL",
		"remote Docker daemons and non-Unix Docker endpoints remain unsupported",
		"remote Docker contexts and non-Unix Docker endpoints remain unsupported",
		"Docker Desktop remains unsupported",
		"remote, NAS, FUSE, and desktop-virtualized filesystems remain unsupported",
		"Docker named volumes and volume subpaths remain unsupported",
		"a root-owned Syncthing config is unsupported",
		"container name must start with an ASCII alphanumeric",
		"the non-root diagnostics listener port must be between 1024 and 65535",
		"Docker bind source paths containing commas are unsupported",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("diagnostics installer lost boundary %q", required)
		}
	}
	for _, forbidden := range []string{"--publish", "docker volume", "/rest/config", "SetFolder", "SetDevice", "mDNS", "UPnP"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("diagnostics installer contains forbidden mutation/discovery %q", forbidden)
		}
	}
}

func TestDiagnosticsRuntimeHasNoImplicitConfiguration(t *testing.T) {
	t.Setenv(diagnosticsRuntimeConfigEnvironment, "")
	t.Setenv(diagnosticsRuntimeStateEnvironment, "")
	config, err := loadDiagnosticsRuntimeConfig()
	if err != nil || config != nil {
		t.Fatalf("implicit diagnostics config = %+v, %v", config, err)
	}
}

func TestDiagnosticsRuntimeRejectsUnsupportedPlatform(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("Linux is the one supported diagnostics runtime platform")
	}
	state := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	config := &diagnosticsRuntimeConfig{
		FormatVersion: 1, ListenAddress: "127.0.0.1:8443", AdvertisedHost: "127.0.0.1", AdvertisedPort: 8443,
		stateDirectory: state,
	}
	if _, err := newDiagnosticsRuntime(config, "unused"); err == nil {
		t.Fatalf("diagnostics runtime activated on unsupported platform %s", runtime.GOOS)
	}
}

func TestDiagnosticsRuntimeConfigRequiresExplicitPrivateEndpointAndExactAliases(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "runtime.json")
	body := `{
  "format_version": 1,
  "listen_address": "127.0.0.1:8443",
  "advertised_host": "helper.test",
  "advertised_port": 8443,
  "folders": [{"folder_id":"vault-a","mount_alias":"namespace-1"}]
}`
	if err := os.WriteFile(path, []byte(body), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Setenv(diagnosticsRuntimeConfigEnvironment, path)
	t.Setenv(diagnosticsRuntimeStateEnvironment, state)
	t.Setenv(diagnosticsRuntimeMountBindingEnvironmentPrefix+"1", strings.Repeat("1", 64))
	config, err := loadDiagnosticsRuntimeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.ListenAddress != "127.0.0.1:8443" || config.AdvertisedHost != "helper.test" || config.AdvertisedPort != 8443 {
		t.Fatalf("endpoint changed: %+v", config)
	}
	folder, ok := config.folder("vault-a")
	if !ok || folder.MountAlias != "namespace-1" {
		t.Fatalf("folder mapping = %+v, %v", folder, ok)
	}
	if mount, err := config.mountPath(folder.MountAlias); err != nil || mount != "/diagnostics/namespace-1" {
		t.Fatalf("mount = %q, %v", mount, err)
	}

	for _, mutation := range []string{
		strings.Replace(body, "127.0.0.1:8443", "0.0.0.0:8443", 1),
		strings.Replace(body, "127.0.0.1:8443", "8.8.8.8:8443", 1),
		strings.Replace(body, `"advertised_port": 8443`, `"advertised_port": 9443`, 1),
		strings.Replace(body, "namespace-1", "../vault", 1),
		strings.Replace(body, `"folders":`, `"unknown":true,"folders":`, 1),
	} {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(mutation), 0o400); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o400); err != nil {
			t.Fatal(err)
		}
		if _, err := loadDiagnosticsRuntimeConfig(); err == nil {
			t.Fatalf("invalid runtime configuration accepted: %s", mutation)
		}
	}
}

func TestDiagnosticsRuntimeConfigRequiresBothReadOnlyConfigAndSeparateState(t *testing.T) {
	t.Setenv(diagnosticsRuntimeConfigEnvironment, "/tmp/runtime.json")
	t.Setenv(diagnosticsRuntimeStateEnvironment, "")
	if _, err := loadDiagnosticsRuntimeConfig(); err == nil {
		t.Fatal("config without state was accepted")
	}
	t.Setenv(diagnosticsRuntimeConfigEnvironment, "")
	t.Setenv(diagnosticsRuntimeStateEnvironment, "/tmp/state")
	if _, err := loadDiagnosticsRuntimeConfig(); err == nil {
		t.Fatal("state without config was accepted")
	}
}

func TestDiagnosticsRuntimeConfigRejectsWritableOrStateContainedConfig(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"format_version":1,"listen_address":"127.0.0.1:8443","advertised_host":"127.0.0.1","advertised_port":8443,"folders":[]}`)
	writable := filepath.Join(root, "writable.json")
	if err := os.WriteFile(writable, body, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(diagnosticsRuntimeConfigEnvironment, writable)
	t.Setenv(diagnosticsRuntimeStateEnvironment, state)
	if _, err := loadDiagnosticsRuntimeConfig(); err == nil {
		t.Fatal("writable runtime configuration was accepted")
	}
	contained := filepath.Join(state, "runtime.json")
	if err := os.WriteFile(contained, body, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Setenv(diagnosticsRuntimeConfigEnvironment, contained)
	if _, err := loadDiagnosticsRuntimeConfig(); err == nil {
		t.Fatal("configuration inside writable state was accepted")
	}
	aliased := filepath.Join(state, "aliased.json")
	if err := os.WriteFile(aliased, body, 0o400); err != nil {
		t.Fatal(err)
	}
	stateAlias := filepath.Join(root, "state-alias")
	if err := os.Symlink(state, stateAlias); err != nil {
		t.Logf("parent-symlink regression unavailable on this platform: %v", err)
	} else {
		t.Setenv(diagnosticsRuntimeConfigEnvironment, filepath.Join(stateAlias, "aliased.json"))
		if _, err := loadDiagnosticsRuntimeConfig(); err == nil {
			t.Fatal("configuration physically contained by state through a parent symlink was accepted")
		}
	}
	external := filepath.Join(root, "external.json")
	if err := os.WriteFile(external, body, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(external, filepath.Join(state, "config-hardlink.json")); err != nil {
		t.Fatal(err)
	}
	t.Setenv(diagnosticsRuntimeConfigEnvironment, external)
	if _, err := loadDiagnosticsRuntimeConfig(); err == nil {
		t.Fatal("read-only configuration hard-linked into writable state was accepted")
	}
}

func TestDiagnosticsRuntimeConfigRequiresExactUsedMountBindings(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "runtime.json")
	body := []byte(`{"format_version":1,"listen_address":"127.0.0.1:8443","advertised_host":"127.0.0.1","advertised_port":8443,"folders":[{"folder_id":"vault-a","mount_alias":"namespace-1"}]}`)
	if err := os.WriteFile(path, body, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Setenv(diagnosticsRuntimeConfigEnvironment, path)
	t.Setenv(diagnosticsRuntimeStateEnvironment, state)
	if _, err := loadDiagnosticsRuntimeConfig(); err == nil {
		t.Fatal("enabled namespace without an ephemeral mount binding was accepted")
	}
	t.Setenv(diagnosticsRuntimeMountBindingEnvironmentPrefix+"1", strings.Repeat("a", 64))
	if _, err := loadDiagnosticsRuntimeConfig(); err != nil {
		t.Fatalf("exact used mount binding: %v", err)
	}
	t.Setenv(diagnosticsRuntimeMountBindingEnvironmentPrefix+"2", strings.Repeat("b", 64))
	if _, err := loadDiagnosticsRuntimeConfig(); err == nil {
		t.Fatal("unused mount binding was accepted")
	}
}
