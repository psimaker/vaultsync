package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDiagnosticsIgnoreVerdictUsesExpandedFirstMatchSemantics(t *testing.T) {
	if verdict := diagnosticsNamespaceIgnoreVerdictFromExpanded(nil); !verdict.valid() {
		t.Fatalf("empty expanded matcher = %+v", verdict)
	}
	if verdict := diagnosticsNamespaceIgnoreVerdictFromExpanded([]string{"VaultSync Diagnostics", "!VaultSync Diagnostics"}); verdict.valid() {
		t.Fatalf("first ignore match was overridden: %+v", verdict)
	}
	if verdict := diagnosticsNamespaceIgnoreVerdictFromExpanded([]string{"!VaultSync Diagnostics", "VaultSync Diagnostics"}); !verdict.valid() {
		t.Fatalf("first include match was ignored: %+v", verdict)
	}
	if verdict := diagnosticsNamespaceIgnoreVerdictFromExpanded([]string{"(?i)vaultsync diagnostics"}); verdict.valid() {
		t.Fatalf("case-folded ignore was missed: %+v", verdict)
	}
	if verdict := diagnosticsNamespaceIgnoreVerdictFromExpanded([]string{"["}); verdict.IncludesSupported {
		t.Fatalf("invalid expanded matcher was accepted: %+v", verdict)
	}
}

func TestDiagnosticsSyncthingPreflightResponsesAreBounded(t *testing.T) {
	patterns := make([]string, diagnosticsSyncthingPreflightMaximumPatterns+1)
	for index := range patterns {
		patterns[index] = "unmatched"
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-API-Key") != "test-key" {
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		switch request.URL.Path {
		case "/rest/config/folders":
			_, _ = writer.Write([]byte("[" + strings.Repeat(" ", int(diagnosticsSyncthingPreflightMaximumBytes)) + "]"))
		case "/rest/db/ignores":
			_ = json.NewEncoder(writer).Encode(syncthingIgnoreConfig{Expanded: patterns})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewSyncthingClient(server.URL, "test-key")
	if _, err := client.ListFolders(context.Background()); err == nil {
		t.Fatal("oversized Syncthing folder response was accepted")
	}
	if _, err := client.FolderIgnores(context.Background(), "folder"); err == nil {
		t.Fatal("excessive Syncthing expanded-ignore count was accepted")
	}

	longPatternServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(writer).Encode(syncthingIgnoreConfig{
			Expanded: []string{strings.Repeat("a", diagnosticsSyncthingPreflightMaximumPattern+1)},
		})
	}))
	defer longPatternServer.Close()
	if _, err := NewSyncthingClient(longPatternServer.URL, "test-key").FolderIgnores(context.Background(), "folder"); err == nil {
		t.Fatal("oversized Syncthing expanded-ignore pattern was accepted")
	}

	for _, response := range []string{`[] {}`, `[] SECRET-RESPONSE-BODY`} {
		response := response
		t.Run("trailing response rejected", func(t *testing.T) {
			trailingServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				_, _ = writer.Write([]byte(response))
			}))
			defer trailingServer.Close()
			_, err := NewSyncthingClient(trailingServer.URL, "test-key").ListFolders(context.Background())
			if err == nil {
				t.Fatal("trailing Syncthing response content was accepted")
			}
			if strings.Contains(err.Error(), "SECRET-RESPONSE-BODY") {
				t.Fatal("Syncthing response content reached the returned error")
			}
		})
	}
}

func TestDiagnosticsPairingNoBodyTransitionUsesAcceptedTransportStatus(t *testing.T) {
	manager, clock := newDiagnosticsTestPairingManager(t)
	recordID, appPrivate := activateDiagnosticsTestInstallation(
		t, manager, clock, bytes.Repeat([]byte{0x90}, 32), 0x90,
	)
	state, _ := manager.store.snapshot()
	authorization := state.Authorizations[diagnosticsAuthorizationIndex(state.Authorizations, recordID)]
	proposedPrivate := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x8f}, ed25519.SeedSize))
	now := uint64(clock.current().Unix())
	request, err := buildDiagnosticsAppKeyRotationRequest(
		authorization, state.Identity, proposedPrivate.Public().(ed25519.PublicKey), appPrivate,
		now, now+300, bytes.Repeat([]byte{0x8e}, 32),
	)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	(&diagnosticsRuntime{pairing: manager}).handlePairing(recorder, request)
	result := recorder.Result()
	defer result.Body.Close()
	if result.StatusCode != http.StatusAccepted || result.ContentLength != 0 || recorder.Body.Len() != 0 {
		t.Fatalf("no-body transition transport = %d/%d/%d", result.StatusCode, result.ContentLength, recorder.Body.Len())
	}
}

func TestDiagnosticsRuntimeRejectsAlternateEncodedFixedPaths(t *testing.T) {
	manager, _ := newDiagnosticsTestPairingManager(t)
	golden := diagnosticsPairingGoldenMessages(t)[diagnosticsPairingGoldenName(diagnosticsPairingAppRequest)]
	body, err := hex.DecodeString(golden)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"https://127.0.0.1/api/v1/diagnostics/%70airing",
		bytes.NewReader(body),
	)
	request.TLS = &tls.ConnectionState{Version: tls.VersionTLS13}
	request.Header.Set("Content-Type", "application/cbor")
	if request.URL.RawPath == "" || request.URL.Path != diagnosticsPairingPath {
		t.Fatalf("encoded-path fixture = %q / %q", request.URL.Path, request.URL.RawPath)
	}
	recorder := httptest.NewRecorder()
	(&diagnosticsRuntime{pairing: manager}).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("encoded diagnostics path status = %d", recorder.Code)
	}

	operatorRequest := httptest.NewRequest(
		http.MethodPost,
		"http://unix/v1/%70air",
		strings.NewReader(`{"folder_id":"configured"}`),
	)
	operatorRequest.Header.Set("Content-Type", "application/json")
	if operatorRequest.URL.RawPath == "" || operatorRequest.URL.Path != diagnosticsOperatorPairPath {
		t.Fatalf("encoded operator fixture = %q / %q", operatorRequest.URL.Path, operatorRequest.URL.RawPath)
	}
	operatorRecorder := httptest.NewRecorder()
	(&diagnosticsRuntime{}).serveOperatorHTTP(operatorRecorder, operatorRequest)
	if operatorRecorder.Code != http.StatusBadRequest {
		t.Fatalf("encoded operator path status = %d", operatorRecorder.Code)
	}

	wrongMediaType := httptest.NewRequest(
		http.MethodPost,
		"http://unix"+diagnosticsOperatorPairPath,
		strings.NewReader(`{"folder_id":"configured"}`),
	)
	wrongMediaType.Header.Set("Content-Type", "text/plain")
	mediaRecorder := httptest.NewRecorder()
	(&diagnosticsRuntime{}).serveOperatorHTTP(mediaRecorder, wrongMediaType)
	if mediaRecorder.Code != http.StatusBadRequest {
		t.Fatalf("operator media type status = %d", mediaRecorder.Code)
	}
}

func TestDiagnosticsRuntimePreflightRechecksFolderAndIgnores(t *testing.T) {
	root := t.TempDir()
	stateDirectory := filepath.Join(root, "state")
	if err := os.Mkdir(stateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	rawDeviceID := bytes.Repeat([]byte{0x8b}, 32)
	deviceDigest, _ := diagnosticsDeviceIDDigest(rawDeviceID)
	deviceID := diagnosticsTestDeviceID(rawDeviceID)
	credentialStore, err := openDiagnosticsCredentialStore(
		filepath.Join(stateDirectory, "credentials"), deviceDigest[:], nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	clock := &diagnosticsTestClock{now: time.Now().Truncate(time.Second)}
	pairing, _ := newDiagnosticsPairingManager(credentialStore, nil, clock.current)
	folderID := "preflight-folder"
	folderDigest, _ := diagnosticsFolderIDDigest(folderID)
	recordID, _ := activateDiagnosticsTestInstallation(t, pairing, clock, folderDigest[:], 0x8c)
	credentialState, _ := credentialStore.snapshot()
	authorization := credentialState.Authorizations[diagnosticsAuthorizationIndex(credentialState.Authorizations, recordID)]
	namespaceStore, _ := openDiagnosticsNamespaceStateStore(filepath.Join(stateDirectory, "namespace"))
	config := &diagnosticsRuntimeConfig{
		FormatVersion: 1, ListenAddress: "127.0.0.1:8443", AdvertisedHost: "127.0.0.1", AdvertisedPort: 8443,
		Folders: []diagnosticsRuntimeFolderConfig{{FolderID: folderID, MountAlias: "namespace-1"}}, stateDirectory: stateDirectory,
		mountBindings: make(map[string][32]byte),
	}
	rootIdentity := diagnosticsNamespaceFileIdentity{Device: 41, Inode: 42, MountID: 43}
	config.mountBindings["namespace-1"] = diagnosticsRuntimeMountBinding(folderID, "/srv/vault", "namespace-1", rootIdentity)
	validServer := diagnosticsRuntimeSyncthingServer(t, folderID, "/srv/vault", nil, deviceID)
	defer validServer.Close()
	runtimeServer := &diagnosticsRuntime{
		config: config, credentialStore: credentialStore, namespaceStore: namespaceStore,
		syncthing: NewSyncthingClient(validServer.URL, "test-key"),
	}
	runtimeServer.sessions = newDiagnosticsRuntimeSessions(config, credentialStore, namespaceStore)
	runtimeServer.namespace = newDiagnosticsNamespaceRuntime(config, credentialStore, namespaceStore, runtimeServer.sessions)
	binding := diagnosticsUploadBinding{
		folderBinding:   authorization.FolderBinding,
		namespaceHandle: &diagnosticsNamespaceRootHandle{identity: rootIdentity},
	}
	if err := runtimeServer.preflightBinding(context.Background(), binding); err != nil {
		t.Fatalf("valid preflight: %v", err)
	}
	ignoredServer := diagnosticsRuntimeSyncthingServer(t, folderID, "/srv/vault", []string{"VaultSync Diagnostics/**"}, deviceID)
	defer ignoredServer.Close()
	runtimeServer.syncthing = NewSyncthingClient(ignoredServer.URL, "test-key")
	if err := runtimeServer.preflightBinding(context.Background(), binding); err == nil {
		t.Fatal("ignored diagnostics namespace passed runtime preflight")
	}
	ignoreErrorServer := diagnosticsRuntimeSyncthingServer(t, folderID, "/srv/vault", nil, deviceID, "parse error")
	defer ignoreErrorServer.Close()
	runtimeServer.syncthing = NewSyncthingClient(ignoreErrorServer.URL, "test-key")
	if err := runtimeServer.preflightBinding(context.Background(), binding); err == nil {
		t.Fatal("Syncthing ignore parser error passed runtime preflight")
	}
	changedPathServer := diagnosticsRuntimeSyncthingServer(t, folderID, "/srv/replaced-vault", nil, deviceID)
	defer changedPathServer.Close()
	runtimeServer.syncthing = NewSyncthingClient(changedPathServer.URL, "test-key")
	if err := runtimeServer.preflightBinding(context.Background(), binding); err == nil {
		t.Fatal("changed Syncthing folder path passed the ephemeral mount binding")
	}
	changedDeviceServer := diagnosticsRuntimeSyncthingServer(
		t, folderID, "/srv/vault", nil, diagnosticsTestDeviceID(bytes.Repeat([]byte{0x8c}, 32)),
	)
	defer changedDeviceServer.Close()
	runtimeServer.syncthing = NewSyncthingClient(changedDeviceServer.URL, "test-key")
	if err := runtimeServer.preflightBinding(context.Background(), binding); err == nil {
		t.Fatal("changed Syncthing Device ID passed the pinned runtime preflight")
	}
	var statusCalls atomic.Uint64
	changedDuringPreflight := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-API-Key") != "test-key" {
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		switch request.URL.Path {
		case "/rest/system/status":
			responseDeviceID := deviceID
			if statusCalls.Add(1) > 1 {
				responseDeviceID = diagnosticsTestDeviceID(bytes.Repeat([]byte{0x8c}, 32))
			}
			_ = json.NewEncoder(writer).Encode(map[string]string{"myID": responseDeviceID})
		case "/rest/config/folders":
			_ = json.NewEncoder(writer).Encode([]folderConfig{{ID: folderID, Path: "/srv/vault", Type: "sendreceive"}})
		case "/rest/db/ignores":
			_ = json.NewEncoder(writer).Encode(syncthingIgnoreConfig{})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer changedDuringPreflight.Close()
	runtimeServer.syncthing = NewSyncthingClient(changedDuringPreflight.URL, "test-key")
	if err := runtimeServer.preflightBinding(context.Background(), binding); err == nil {
		t.Fatal("Syncthing Device ID changed between preflight reads")
	}
}

func TestDiagnosticsAdminActionsRequireExactLocalFingerprint(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("diagnostics runtime packaging is Linux-only")
	}
	stateDirectory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(stateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	folderID := "admin-folder"
	config := &diagnosticsRuntimeConfig{
		FormatVersion: 1, ListenAddress: "127.0.0.1:8443", AdvertisedHost: "127.0.0.1", AdvertisedPort: 8443,
		Folders: []diagnosticsRuntimeFolderConfig{{FolderID: folderID}}, stateDirectory: stateDirectory,
	}
	deviceID := diagnosticsTestDeviceID(bytes.Repeat([]byte{0x8b}, 32))
	runtimeState, err := newDiagnosticsRuntime(config, deviceID)
	if err != nil {
		t.Fatal(err)
	}
	clock := &diagnosticsTestClock{now: time.Now().Truncate(time.Second)}
	runtimeState.pairing.now = clock.current
	folderDigest, _ := diagnosticsFolderIDDigest(folderID)
	recordID, _ := activateDiagnosticsTestInstallation(t, runtimeState.pairing, clock, folderDigest[:], 0x8a)
	state, _ := runtimeState.credentialStore.snapshot()
	authorization := state.Authorizations[diagnosticsAuthorizationIndex(state.Authorizations, recordID)]
	fingerprint := diagnosticsAdminAppFingerprint(authorization.AppKeyID)
	runtimeState.close()

	syncthingServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-API-Key") != "test-key" || request.URL.Path != "/rest/system/status" {
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]string{"myID": deviceID})
	}))
	defer syncthingServer.Close()
	appConfig := Config{
		SyncthingAPIURL: syncthingServer.URL, SyncthingAPIKey: "test-key", diagnosticsRuntime: config,
	}
	listing, err := runDiagnosticsAdminOperator(context.Background(), appConfig, diagnosticsAdminCommand{action: "list", folderID: folderID})
	if err != nil || !strings.Contains(listing, fingerprint+" state=active namespace=no") {
		t.Fatalf("admin list did not return the exact local fingerprint: %v", err)
	}
	if _, err := runDiagnosticsAdminOperator(context.Background(), appConfig, diagnosticsAdminCommand{
		action: "rotate-helper", folderID: folderID, appFingerprint: "000000000000",
	}); err == nil {
		t.Fatal("wrong local app fingerprint started helper rotation")
	}
	rotation, err := runDiagnosticsAdminOperator(context.Background(), appConfig, diagnosticsAdminCommand{
		action: "rotate-helper", folderID: folderID, appFingerprint: fingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(rotation), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "proposal=") || !strings.HasPrefix(lines[1], "proof=") {
		t.Fatal("helper rotation did not return the two explicit operator records")
	}
	for _, line := range lines {
		encoded := strings.SplitN(line, "=", 2)[1]
		if body, decodeErr := base64.RawURLEncoding.DecodeString(encoded); decodeErr != nil || len(body) == 0 {
			t.Fatal("operator rotation record was not canonical base64url CBOR")
		}
	}
}

func TestDiagnosticsNamespaceRuntimeRequiresSignedAppThenLocalOperatorThenAuthorization(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Docker host-bind namespace installation is Linux-only")
	}
	root := t.TempDir()
	stateDirectory := filepath.Join(root, "state")
	credentialDirectory := filepath.Join(stateDirectory, "credentials")
	namespaceDirectory := filepath.Join(stateDirectory, "namespace")
	if err := os.Mkdir(stateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	folderID := "vault-runtime-test"
	folderDigest, _ := diagnosticsFolderIDDigest(folderID)
	rawDeviceID := bytes.Repeat([]byte{0x8b}, 32)
	deviceDigest, _ := diagnosticsDeviceIDDigest(rawDeviceID)
	credentialStore, err := openDiagnosticsCredentialStore(credentialDirectory, deviceDigest[:], nil)
	if err != nil {
		t.Fatal(err)
	}
	clock := &diagnosticsTestClock{now: time.Now().Truncate(time.Second)}
	pairing, err := newDiagnosticsPairingManager(credentialStore, nil, clock.current)
	if err != nil {
		t.Fatal(err)
	}
	recordID, appPrivate := activateDiagnosticsTestInstallation(t, pairing, clock, folderDigest[:], 0x92)
	namespaceStore, err := openDiagnosticsNamespaceStateStore(namespaceDirectory)
	if err != nil {
		t.Fatal(err)
	}
	config := &diagnosticsRuntimeConfig{
		FormatVersion: 1, ListenAddress: "127.0.0.1:8443", AdvertisedHost: "127.0.0.1", AdvertisedPort: 8443,
		Folders: []diagnosticsRuntimeFolderConfig{{FolderID: folderID}}, stateDirectory: stateDirectory,
		mountPathOverrides: make(map[string]string),
	}
	sessions := newDiagnosticsRuntimeSessions(config, credentialStore, namespaceStore)
	defer sessions.close()
	namespaceRuntime := newDiagnosticsNamespaceRuntime(config, credentialStore, namespaceStore, sessions)
	namespaceRuntime.now = clock.current

	credentialState, _ := credentialStore.snapshot()
	authorization := credentialState.Authorizations[diagnosticsAuthorizationIndex(credentialState.Authorizations, recordID)]
	enablement := diagnosticsRuntimeTestEnablement(t, authorization, credentialState.Identity, appPrivate, clock.current())
	if _, _, err := namespaceRuntime.pendingForFolder(folderID); err == nil {
		t.Fatal("operator could fetch a request before the signed app enablement")
	}
	if err := namespaceRuntime.acceptEnablement(enablement); err != nil {
		t.Fatal(err)
	}
	pending, _, err := namespaceRuntime.pendingForFolder(folderID)
	if err != nil || !bytes.Equal(pending, enablement) {
		t.Fatalf("pending enablement = %x, %v", pending, err)
	}

	parent := filepath.Join(root, "vault")
	if err := os.Mkdir(parent, 0o700); err != nil || os.Mkdir(filepath.Join(parent, ".stfolder"), 0o700) != nil {
		t.Fatal("create folder fixture")
	}
	server := diagnosticsRuntimeSyncthingServer(t, folderID, parent, nil, diagnosticsTestDeviceID(rawDeviceID))
	defer server.Close()
	syncthing := NewSyncthingClient(server.URL, "test-key")
	parentHandle, err := openDiagnosticsNamespaceRoot(parent, nil)
	if err != nil {
		t.Fatal(err)
	}
	parentIdentity := parentHandle.Identity()
	_ = parentHandle.Close()
	if _, err := prepareDiagnosticsNamespaceForOperator(
		context.Background(), config, credentialStore, namespaceStore, syncthing,
		folderID, parent, parent, parentIdentity.Device, parentIdentity.Inode, nil, true,
	); !errors.Is(err, errDiagnosticsNamespaceUnsupported) {
		t.Fatalf("recovery without registered root = %v", err)
	}
	if _, err := os.Stat(filepath.Join(parent, diagnosticsNamespaceRootName)); !os.IsNotExist(err) {
		t.Fatal("recovery-only request created an unregistered namespace")
	}
	var installerStatusCalls atomic.Uint64
	changedInstallerDevice := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-API-Key") != "test-key" {
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		switch request.URL.Path {
		case "/rest/system/status":
			responseDeviceID := diagnosticsTestDeviceID(rawDeviceID)
			if installerStatusCalls.Add(1) > 1 {
				responseDeviceID = diagnosticsTestDeviceID(bytes.Repeat([]byte{0x8c}, 32))
			}
			_ = json.NewEncoder(writer).Encode(map[string]string{"myID": responseDeviceID})
		case "/rest/config/folders":
			_ = json.NewEncoder(writer).Encode([]folderConfig{{ID: folderID, Path: parent, Type: "sendreceive"}})
		case "/rest/db/ignores":
			_ = json.NewEncoder(writer).Encode(syncthingIgnoreConfig{})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	changingSyncthing := NewSyncthingClient(changedInstallerDevice.URL, "test-key")
	if _, err := prepareDiagnosticsNamespaceForOperator(
		context.Background(), config, credentialStore, namespaceStore, changingSyncthing,
		folderID, parent, parent, parentIdentity.Device, parentIdentity.Inode, pending, true,
	); err == nil {
		t.Fatal("namespace installer accepted a Device ID change during preflight")
	}
	changedInstallerDevice.Close()
	if _, err := os.Stat(filepath.Join(parent, diagnosticsNamespaceRootName)); !os.IsNotExist(err) {
		t.Fatal("failed installer preflight created a namespace")
	}
	ignoreErrorInstallerServer := diagnosticsRuntimeSyncthingServer(
		t, folderID, parent, nil, diagnosticsTestDeviceID(rawDeviceID), "parse error",
	)
	if _, err := prepareDiagnosticsNamespaceForOperator(
		context.Background(), config, credentialStore, namespaceStore,
		NewSyncthingClient(ignoreErrorInstallerServer.URL, "test-key"),
		folderID, parent, parent, parentIdentity.Device, parentIdentity.Inode, pending, true,
	); !errors.Is(err, errDiagnosticsNamespaceUnsupported) {
		t.Fatalf("installer accepted a Syncthing ignore parser error: %v", err)
	}
	ignoreErrorInstallerServer.Close()
	if _, err := os.Stat(filepath.Join(parent, diagnosticsNamespaceRootName)); !os.IsNotExist(err) {
		t.Fatal("ignore parser error created a namespace")
	}
	registrationCrash := errors.New("simulated crash after namespace state registration")
	namespaceStore.hooks.afterRename = func() error { return registrationCrash }
	if _, err := prepareDiagnosticsNamespaceForOperator(
		context.Background(), config, credentialStore, namespaceStore, syncthing,
		folderID, parent, parent, parentIdentity.Device, parentIdentity.Inode, pending, true,
	); !errors.Is(err, registrationCrash) {
		t.Fatalf("post-registration crash result = %v", err)
	}
	registeredState, err := namespaceStore.snapshot()
	if err != nil || len(registeredState.Roots) != 1 {
		t.Fatalf("post-registration durable state = %+v, %v", registeredState, err)
	}
	registeredRecord := registeredState.Roots[0]
	namespaceStore.hooks = diagnosticsNamespaceStoreHooks{}
	recoveredAfterRestart, err := prepareDiagnosticsNamespaceForOperator(
		context.Background(), config, credentialStore, namespaceStore, syncthing,
		folderID, parent, parent, parentIdentity.Device, parentIdentity.Inode, nil, true,
	)
	if err != nil || !diagnosticsNamespaceRootRecordsEqual(recoveredAfterRestart, registeredRecord) {
		t.Fatalf("explicit post-restart recovery = %+v, %v", recoveredAfterRestart, err)
	}
	if diagnosticsAdminRotationReady(&diagnosticsRuntime{
		namespaceStore: namespaceStore, sessions: sessions,
	}, authorization, credentialState.Identity) {
		t.Fatal("helper rotation was ready while a registered root still lacked authorization")
	}
	record, err := prepareDiagnosticsNamespaceForOperator(
		context.Background(), config, credentialStore, namespaceStore, syncthing,
		folderID, parent, parent, parentIdentity.Device, parentIdentity.Inode, pending, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !diagnosticsNamespaceRootRecordsEqual(record, registeredRecord) {
		t.Fatalf("exact retry changed the registered root: retry=%+v registered=%+v", record, registeredRecord)
	}
	rootPath := filepath.Join(parent, diagnosticsNamespaceRootName)
	config.Folders[0].MountAlias = record.MountAlias
	config.mountPathOverrides[record.MountAlias] = rootPath

	candidate := diagnosticsRuntimeTestInitialAuthorization(t, authorization, credentialState.Identity, appPrivate, record, rootPath, clock.current())
	namespaceRuntime.preflight = func(context.Context, []byte, *diagnosticsNamespaceRootHandle) error { return nil }
	authorizationCrash := errors.New("simulated crash after authorization state registration")
	credentialStore.hooks.afterRename = func() error { return authorizationCrash }
	if err := namespaceRuntime.authorize(context.Background(), candidate); !errors.Is(err, authorizationCrash) {
		t.Fatalf("post-authorization registration crash = %v", err)
	}
	credentialStore.hooks = diagnosticsCredentialStoreHooks{}
	if err := namespaceRuntime.authorize(context.Background(), candidate); err != nil {
		t.Fatalf("exact post-registration authorization retry = %v", err)
	}
	credentialState, _ = credentialStore.snapshot()
	authorization = credentialState.Authorizations[diagnosticsAuthorizationIndex(credentialState.Authorizations, recordID)]
	appKeyID := diagnosticsKeyID(appPrivate.Public().(ed25519.PublicKey))
	if !bytes.Equal(authorization.NamespaceInitialAppKeyID, appKeyID[:]) || authorization.NamespaceAuthorizationEpoch != 1 {
		t.Fatalf("namespace authorization state = %+v", authorization)
	}
	installation, _ := diagnosticsNamespaceInstallationBinding(appKeyID[:], authorization.HomeserverBinding, authorization.FolderBinding)
	paths, _ := diagnosticsNamespaceAuthorizationPaths(installation[:])
	handle, err := openDiagnosticsNamespaceRoot(rootPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	if body, _, err := handle.ReadImmutable(paths[0]); err != nil || len(body) == 0 {
		t.Fatalf("authorization artifact = %x, %v", body, err)
	}
	clock.advance(diagnosticsPairingLifetime + time.Second)
	if err := namespaceRuntime.authorize(context.Background(), candidate); err != nil {
		t.Fatalf("expired exact initial-authorization retry = %v", err)
	}

	session, err := sessions.sessionForAuthorization(authorization, credentialState.Identity)
	if err != nil || session.binding.authorizationEpoch != 1 {
		t.Fatalf("restart session = %+v, %v", session, err)
	}
	stateOnlyAdvanced := authorization
	stateOnlyAdvanced.CurrentStateDigest = bytes.Repeat([]byte{0xfe}, 32)
	if _, err := sessions.sessionForAuthorization(stateOnlyAdvanced, credentialState.Identity); err == nil {
		t.Fatal("credential-state-only rotation reused a stale namespace authorization for operations")
	}
	capabilityBinding, releaseCapability, err := sessions.capabilityOnlyBinding(stateOnlyAdvanced, credentialState.Identity)
	if err != nil || capabilityBinding.authorizationEpoch != 1 {
		t.Fatalf("credential-state-only capability reconciliation = %+v, %v", capabilityBinding, err)
	}
	releaseCapability()

	proposedPrivate := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x96}, ed25519.SeedSize))
	nowSeconds := uint64(clock.current().Unix())
	rotationRequest, err := buildDiagnosticsAppKeyRotationRequest(
		authorization, credentialState.Identity, proposedPrivate.Public().(ed25519.PublicKey), appPrivate,
		nowSeconds, nowSeconds+300, bytes.Repeat([]byte{0x97}, 32),
	)
	if err != nil {
		t.Fatal(err)
	}
	if response, err := pairing.handleLifecycleMessage(rotationRequest); err != nil || response != nil {
		t.Fatalf("stage app rotation = %x, %v", response, err)
	}
	requestMessage, _ := decodeDiagnosticsPairingMessage(rotationRequest)
	rotationProof, err := buildDiagnosticsLifecycleContinuation(
		requestMessage, diagnosticsPairingAppKeyRotationNewProof, 0, nil, proposedPrivate,
		nowSeconds, nowSeconds+300, bytes.Repeat([]byte{0x98}, 32),
	)
	if err != nil {
		t.Fatal(err)
	}
	acceptBytes, err := pairing.handleLifecycleMessage(rotationProof)
	if err != nil {
		t.Fatal(err)
	}
	acceptMessage, _ := decodeDiagnosticsPairingMessage(acceptBytes)
	transitionDigest, _ := requestMessage.digest()
	finalize, err := buildDiagnosticsLifecycleContinuation(
		acceptMessage, diagnosticsPairingLifecycleFinalize, diagnosticsPairingTransitionAppKey, transitionDigest[:], proposedPrivate,
		nowSeconds, nowSeconds+300, bytes.Repeat([]byte{0x99}, 32),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pairing.handleLifecycleMessage(finalize); err != nil {
		t.Fatal(err)
	}
	proposedBinding := cloneDiagnosticsRuntimeBinding(session.binding)
	proposedBinding.appPublicKey = proposedPrivate.Public().(ed25519.PublicKey)
	proposedBinding.appEpoch++
	queryBytes := diagnosticsRuntimeTestCapabilityQuery(t, proposedBinding, proposedPrivate, clock.current())
	capabilitySession, err := sessions.sessionForCapabilityMessage(queryBytes, clock.current())
	if err != nil || capabilitySession.confirmation == nil {
		t.Fatalf("proposed app capability session = %+v, %v", capabilitySession, err)
	}
	queryContext := diagnosticsUploadVerificationContext{
		appPublicKey:    capabilitySession.binding.appPublicKey,
		helperPublicKey: capabilitySession.binding.helperPrivateKey.Public().(ed25519.PublicKey),
	}
	query, err := decodeDiagnosticsCapabilityMessage(queryBytes, queryContext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildDiagnosticsCapabilityResponse(query, capabilitySession.binding, clock.current()); err != nil {
		t.Fatal(err)
	}
	if committed, err := pairing.observeLifecycleCapability(
		capabilitySession.confirmation.recordID, capabilitySession.confirmation.transitionDigest, nil,
	); err != nil || !committed {
		t.Fatalf("app rotation confirmation = %v, %v", committed, err)
	}

	credentialState, _ = credentialStore.snapshot()
	proposedKeyID := diagnosticsKeyID(proposedPrivate.Public().(ed25519.PublicKey))
	newRecordID := diagnosticsAuthorizationRecordID(proposedKeyID[:], authorization.FolderBinding)
	rotated := credentialState.Authorizations[diagnosticsAuthorizationIndex(credentialState.Authorizations, newRecordID)]
	retrySession, err := sessions.sessionForCapabilityMessage(queryBytes, clock.current())
	if err != nil || retrySession.release == nil || retrySession.confirmation != nil {
		t.Fatalf("post-commit capability reconciliation = %+v, %v", retrySession, err)
	}
	retrySession.release()
	rotatedFingerprint := diagnosticsAdminAppFingerprint(rotated.AppKeyID)
	adminConfig := Config{
		SyncthingAPIURL: server.URL, SyncthingAPIKey: "test-key", diagnosticsRuntime: config,
	}
	if _, err := runDiagnosticsAdminOperator(context.Background(), adminConfig, diagnosticsAdminCommand{
		action: "rotate-helper", folderID: folderID, appFingerprint: rotatedFingerprint,
	}); !errors.Is(err, errDiagnosticsPairingUnavailable) {
		t.Fatalf("stale namespace authorization started helper rotation: %v", err)
	}
	credentialState, _ = credentialStore.snapshot()
	rotated = credentialState.Authorizations[diagnosticsAuthorizationIndex(credentialState.Authorizations, newRecordID)]
	if rotated.Transition != nil {
		t.Fatal("rejected stale namespace rotation changed credential state")
	}

	epochCandidate := diagnosticsRuntimeTestAuthorizationEpoch(
		t, rotated, credentialState.Identity, proposedPrivate, rootPath, 2, clock.current(),
	)
	epochCrash := errors.New("simulated crash after authorization epoch state registration")
	credentialStore.hooks.afterRename = func() error { return epochCrash }
	if err := namespaceRuntime.authorize(context.Background(), epochCandidate); !errors.Is(err, epochCrash) {
		t.Fatalf("post-epoch registration crash = %v", err)
	}
	credentialStore.hooks = diagnosticsCredentialStoreHooks{}
	if err := namespaceRuntime.authorize(context.Background(), epochCandidate); err != nil {
		t.Fatalf("exact post-registration authorization epoch retry = %v", err)
	}
	credentialState, _ = credentialStore.snapshot()
	rotated = credentialState.Authorizations[diagnosticsAuthorizationIndex(credentialState.Authorizations, newRecordID)]
	if rotated.NamespaceAuthorizationEpoch != 2 {
		t.Fatalf("rotated namespace authorization epoch = %d", rotated.NamespaceAuthorizationEpoch)
	}
	clock.advance(diagnosticsPairingLifetime + time.Second)
	if err := namespaceRuntime.authorize(context.Background(), epochCandidate); err != nil {
		t.Fatalf("expired exact authorization-epoch retry = %v", err)
	}
	rotatedSession, err := sessions.sessionForAuthorization(rotated, credentialState.Identity)
	if err != nil {
		t.Fatalf("rotated normal runtime session: %v", err)
	}

	nowSeconds = uint64(clock.current().Unix())
	helperProposalBytes, helperProofBytes, err := pairing.beginHelperKeyRotation(newRecordID)
	if err != nil {
		t.Fatal(err)
	}
	helperProposal, _ := decodeDiagnosticsPairingMessage(helperProposalBytes)
	helperProposalDigest, _ := helperProposal.digest()
	helperProof, _ := decodeDiagnosticsPairingMessage(helperProofBytes)
	helperConfirm, err := buildDiagnosticsLifecycleContinuation(
		helperProof, diagnosticsPairingHelperKeyRotationConfirm, 0, nil, proposedPrivate,
		nowSeconds, nowSeconds+300, bytes.Repeat([]byte{0xaa}, 32),
	)
	if err != nil {
		t.Fatal(err)
	}
	if response, err := pairing.handleLifecycleMessage(helperConfirm); err != nil || response != nil {
		t.Fatalf("helper rotation confirmation = %x, %v", response, err)
	}
	helperConfirmMessage, _ := decodeDiagnosticsPairingMessage(helperConfirm)
	helperFinalize, err := buildDiagnosticsLifecycleContinuation(
		helperConfirmMessage, diagnosticsPairingLifecycleFinalize, diagnosticsPairingTransitionHelperKey, helperProposalDigest[:], proposedPrivate,
		nowSeconds, nowSeconds+300, bytes.Repeat([]byte{0xab}, 32),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pairing.handleLifecycleMessage(helperFinalize); err != nil {
		t.Fatal(err)
	}
	credentialState, _ = credentialStore.snapshot()
	rotated = credentialState.Authorizations[diagnosticsAuthorizationIndex(credentialState.Authorizations, newRecordID)]
	proposedHelperPrivate, _ := diagnosticsSigningPrivateKey(rotated.Transition.ProposedHelperSeed)
	helperBinding := cloneDiagnosticsRuntimeBinding(rotatedSession.binding)
	helperBinding.helperPrivateKey = proposedHelperPrivate
	helperBinding.helperEpoch++
	helperQueryBytes := diagnosticsRuntimeTestCapabilityQuery(t, helperBinding, proposedPrivate, clock.current())
	helperCapabilitySession, err := sessions.sessionForCapabilityMessage(helperQueryBytes, clock.current())
	if err != nil || helperCapabilitySession.confirmation == nil {
		t.Fatalf("proposed helper capability session = %+v, %v", helperCapabilitySession, err)
	}
	serverRuntime := &diagnosticsRuntime{
		config: config, credentialStore: credentialStore, namespaceStore: namespaceStore,
		pairing: pairing, sessions: sessions, namespace: namespaceRuntime, syncthing: syncthing,
	}
	blockedRotationServer := diagnosticsRuntimeSyncthingServer(
		t, folderID, parent, []string{"VaultSync Diagnostics/**"}, diagnosticsTestDeviceID(rawDeviceID),
	)
	serverRuntime.syncthing = NewSyncthingClient(blockedRotationServer.URL, "test-key")
	if committed, err := pairing.observeLifecycleCapability(
		helperCapabilitySession.confirmation.recordID,
		helperCapabilitySession.confirmation.transitionDigest,
		serverRuntime.prepareLifecycleCommit,
	); err == nil || committed {
		t.Fatalf("helper rotation bypassed changed Syncthing ignore preflight = %v, %v", committed, err)
	}
	blockedRotationServer.Close()
	serverRuntime.syncthing = syncthing
	if committed, err := pairing.observeLifecycleCapability(
		helperCapabilitySession.confirmation.recordID,
		helperCapabilitySession.confirmation.transitionDigest,
		serverRuntime.prepareLifecycleCommit,
	); err != nil || !committed {
		t.Fatalf("helper rotation confirmation = %v, %v", committed, err)
	}
	credentialState, _ = credentialStore.snapshot()
	rotated = credentialState.Authorizations[diagnosticsAuthorizationIndex(credentialState.Authorizations, newRecordID)]
	if credentialState.Identity.HelperEpoch != 2 || rotated.HelperEpoch != 2 || rotated.Transition != nil {
		t.Fatalf("helper rotation state = %+v / %+v", credentialState.Identity, rotated)
	}
	helperRetry, err := sessions.sessionForCapabilityMessage(helperQueryBytes, clock.current())
	if err != nil || helperRetry.release == nil {
		t.Fatalf("post-helper-commit capability reconciliation = %+v, %v", helperRetry, err)
	}
	helperRetry.release()
	helperEpochCandidate := diagnosticsRuntimeTestAuthorizationEpoch(
		t, rotated, credentialState.Identity, proposedPrivate, rootPath, 3, clock.current(),
	)
	if err := namespaceRuntime.authorize(context.Background(), helperEpochCandidate); err != nil {
		t.Fatal(err)
	}
	credentialState, _ = credentialStore.snapshot()
	rotated = credentialState.Authorizations[diagnosticsAuthorizationIndex(credentialState.Authorizations, newRecordID)]
	if rotated.NamespaceAuthorizationEpoch != 3 {
		t.Fatalf("helper-rotated namespace authorization epoch = %d", rotated.NamespaceAuthorizationEpoch)
	}
	if _, err := sessions.sessionForAuthorization(rotated, credentialState.Identity); err != nil {
		t.Fatalf("helper-rotated normal runtime session: %v", err)
	}

	// A separately paired second installation joins the already authenticated
	// root without another namespace creation or trust transfer.
	clock.advance(diagnosticsPairingLifetime + time.Second)
	secondRecordID, secondAppPrivate := activateDiagnosticsTestInstallation(
		t, pairing, clock, folderDigest[:], 0xb0,
	)
	credentialState, _ = credentialStore.snapshot()
	secondAuthorization := credentialState.Authorizations[diagnosticsAuthorizationIndex(credentialState.Authorizations, secondRecordID)]
	secondEnablement := diagnosticsRuntimeTestEnablement(
		t, secondAuthorization, credentialState.Identity, secondAppPrivate, clock.current(),
	)
	if err := namespaceRuntime.acceptEnablement(secondEnablement); err != nil {
		t.Fatalf("second installation enablement: %v", err)
	}
	secondCandidate := diagnosticsRuntimeTestInitialAuthorization(
		t, secondAuthorization, credentialState.Identity, secondAppPrivate, record, rootPath, clock.current(),
	)
	if err := namespaceRuntime.authorize(context.Background(), secondCandidate); err != nil {
		t.Fatalf("second installation authorization: %v", err)
	}
	credentialState, _ = credentialStore.snapshot()
	secondAuthorization = credentialState.Authorizations[diagnosticsAuthorizationIndex(credentialState.Authorizations, secondRecordID)]
	if len(secondAuthorization.NamespaceInitialAppKeyID) != 32 ||
		bytes.Equal(secondAuthorization.NamespaceInitialAppKeyID, rotated.NamespaceInitialAppKeyID) {
		t.Fatal("second installation did not receive an independent stable namespace identity")
	}
	if count, err := handle.InstallationCount(); err != nil || count != 2 {
		t.Fatalf("independent installation count = %d, %v", count, err)
	}
	secondSession, err := sessions.sessionForAuthorization(secondAuthorization, credentialState.Identity)
	if err != nil {
		t.Fatalf("second installation runtime session: %v", err)
	}
	firstTLSSession, err := sessions.sessionForAuthorization(rotated, credentialState.Identity)
	if err != nil {
		t.Fatalf("first installation pre-TLS session: %v", err)
	}
	priorTLSPrivate := append([]byte(nil), credentialState.Identity.TLSPrivatePKCS8...)
	nowTLS := uint64(clock.current().Unix())
	stageTLSRotation := func(recordID string, appPrivate ed25519.PrivateKey, confirmationByte, finalizeByte byte) {
		t.Helper()
		proposalBytes, err := pairing.beginTLSPinRotation(recordID)
		if err != nil {
			t.Fatal(err)
		}
		proposal, err := decodeDiagnosticsPairingMessage(proposalBytes)
		if err != nil {
			t.Fatal(err)
		}
		proposalDigest, err := proposal.digest()
		if err != nil {
			t.Fatal(err)
		}
		confirmationBytes, err := buildDiagnosticsLifecycleContinuation(
			proposal, diagnosticsPairingTLSPinRotationConfirm, 0, nil, appPrivate,
			nowTLS, nowTLS+300, bytes.Repeat([]byte{confirmationByte}, 32),
		)
		if err != nil {
			t.Fatal(err)
		}
		if response, err := pairing.handleLifecycleMessage(confirmationBytes); err != nil || response != nil {
			t.Fatalf("TLS rotation confirmation = %x, %v", response, err)
		}
		confirmation, err := decodeDiagnosticsPairingMessage(confirmationBytes)
		if err != nil {
			t.Fatal(err)
		}
		finalize, err := buildDiagnosticsLifecycleContinuation(
			confirmation, diagnosticsPairingLifecycleFinalize, diagnosticsPairingTransitionTLSPin,
			proposalDigest[:], appPrivate, nowTLS, nowTLS+300, bytes.Repeat([]byte{finalizeByte}, 32),
		)
		if err != nil {
			t.Fatal(err)
		}
		if response, err := pairing.handleLifecycleMessage(finalize); err != nil || len(response) == 0 {
			t.Fatalf("TLS rotation finalize = %x, %v", response, err)
		}
	}
	stageTLSRotation(newRecordID, proposedPrivate, 0xc1, 0xc2)
	stageTLSRotation(secondRecordID, secondAppPrivate, 0xc3, 0xc4)

	// A global TLS transition must not yield terminal capability success to
	// the first installation while another active installation has not yet
	// confirmed. Its exact proposed-state query is remembered, transport stays
	// unavailable, the final peer commits globally, and the first peer then
	// recovers with an exact retry.
	firstTLSQuery := diagnosticsRuntimeTestCapabilityQuery(t, firstTLSSession.binding, proposedPrivate, clock.current())
	firstTLSRecorder := httptest.NewRecorder()
	serverRuntime.handleCapabilityAt(context.Background(), firstTLSRecorder, firstTLSQuery, clock.current())
	if firstTLSRecorder.Code != http.StatusNotFound || firstTLSRecorder.Body.Len() != 0 {
		t.Fatalf("premature global TLS capability response = %d/%d", firstTLSRecorder.Code, firstTLSRecorder.Body.Len())
	}
	secondTLSQuery := diagnosticsRuntimeTestCapabilityQuery(t, secondSession.binding, secondAppPrivate, clock.current())
	secondTLSRecorder := httptest.NewRecorder()
	serverRuntime.handleCapabilityAt(context.Background(), secondTLSRecorder, secondTLSQuery, clock.current())
	if secondTLSRecorder.Code != http.StatusOK || secondTLSRecorder.Body.Len() == 0 {
		t.Fatalf("terminal global TLS capability response = %d/%d", secondTLSRecorder.Code, secondTLSRecorder.Body.Len())
	}
	firstTLSRetryRecorder := httptest.NewRecorder()
	serverRuntime.handleCapabilityAt(context.Background(), firstTLSRetryRecorder, firstTLSQuery, clock.current())
	if firstTLSRetryRecorder.Code != http.StatusOK || firstTLSRetryRecorder.Body.Len() == 0 {
		t.Fatalf("first TLS capability retry = %d/%d", firstTLSRetryRecorder.Code, firstTLSRetryRecorder.Body.Len())
	}
	credentialState, _ = credentialStore.snapshot()
	rotated = credentialState.Authorizations[diagnosticsAuthorizationIndex(credentialState.Authorizations, newRecordID)]
	secondAuthorization = credentialState.Authorizations[diagnosticsAuthorizationIndex(credentialState.Authorizations, secondRecordID)]
	if bytes.Equal(credentialState.Identity.TLSPrivatePKCS8, priorTLSPrivate) ||
		rotated.Transition != nil || secondAuthorization.Transition != nil {
		t.Fatal("global TLS rotation did not commit exactly after every proposed-state query")
	}

	// TLS state completion changes each credential-state digest. Operations stay
	// unavailable until each installation appends its own next D023 record.
	firstTLSAuthorization := diagnosticsRuntimeTestAuthorizationEpoch(
		t, rotated, credentialState.Identity, proposedPrivate, rootPath, 4, clock.current(),
	)
	if err := namespaceRuntime.authorize(context.Background(), firstTLSAuthorization); err != nil {
		t.Fatalf("first post-TLS namespace authorization: %v", err)
	}
	credentialState, _ = credentialStore.snapshot()
	secondAuthorization = credentialState.Authorizations[diagnosticsAuthorizationIndex(credentialState.Authorizations, secondRecordID)]
	secondTLSAuthorization := diagnosticsRuntimeTestAuthorizationEpoch(
		t, secondAuthorization, credentialState.Identity, secondAppPrivate, rootPath, 2, clock.current(),
	)
	if err := namespaceRuntime.authorize(context.Background(), secondTLSAuthorization); err != nil {
		t.Fatalf("second post-TLS namespace authorization: %v", err)
	}
	credentialState, _ = credentialStore.snapshot()
	rotated = credentialState.Authorizations[diagnosticsAuthorizationIndex(credentialState.Authorizations, newRecordID)]
	secondAuthorization = credentialState.Authorizations[diagnosticsAuthorizationIndex(credentialState.Authorizations, secondRecordID)]
	if rotated.NamespaceAuthorizationEpoch != 4 || secondAuthorization.NamespaceAuthorizationEpoch != 2 {
		t.Fatalf("post-TLS authorization epochs = %d/%d", rotated.NamespaceAuthorizationEpoch, secondAuthorization.NamespaceAuthorizationEpoch)
	}

	// Revoking the first app leaves its immutable authorization history in
	// place. It must neither block a later helper-key rotation for the second
	// app nor be silently rewritten to the new epoch.
	if _, err := pairing.revokeLocally(newRecordID, diagnosticsPairingRevocationLostApp); err != nil {
		t.Fatalf("revoke first installation: %v", err)
	}
	helper3ProposalBytes, helper3ProofBytes, err := pairing.beginHelperKeyRotation(secondRecordID)
	if err != nil {
		t.Fatal(err)
	}
	helper3Proposal, _ := decodeDiagnosticsPairingMessage(helper3ProposalBytes)
	helper3ProposalDigest, _ := helper3Proposal.digest()
	helper3Proof, _ := decodeDiagnosticsPairingMessage(helper3ProofBytes)
	now3 := uint64(clock.current().Unix())
	helper3Confirm, err := buildDiagnosticsLifecycleContinuation(
		helper3Proof, diagnosticsPairingHelperKeyRotationConfirm, 0, nil, secondAppPrivate,
		now3, now3+300, bytes.Repeat([]byte{0xb1}, 32),
	)
	if err != nil {
		t.Fatal(err)
	}
	if response, err := pairing.handleLifecycleMessage(helper3Confirm); err != nil || response != nil {
		t.Fatalf("third helper epoch confirmation = %x, %v", response, err)
	}
	helper3ConfirmMessage, _ := decodeDiagnosticsPairingMessage(helper3Confirm)
	helper3Finalize, err := buildDiagnosticsLifecycleContinuation(
		helper3ConfirmMessage, diagnosticsPairingLifecycleFinalize, diagnosticsPairingTransitionHelperKey,
		helper3ProposalDigest[:], secondAppPrivate, now3, now3+300, bytes.Repeat([]byte{0xb2}, 32),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pairing.handleLifecycleMessage(helper3Finalize); err != nil {
		t.Fatal(err)
	}
	credentialState, _ = credentialStore.snapshot()
	secondAuthorization = credentialState.Authorizations[diagnosticsAuthorizationIndex(credentialState.Authorizations, secondRecordID)]
	proposedHelper3, _ := diagnosticsSigningPrivateKey(secondAuthorization.Transition.ProposedHelperSeed)
	helper3Binding := cloneDiagnosticsRuntimeBinding(secondSession.binding)
	helper3Binding.helperPrivateKey = proposedHelper3
	helper3Binding.helperEpoch++
	helper3Query := diagnosticsRuntimeTestCapabilityQuery(t, helper3Binding, secondAppPrivate, clock.current())
	helper3Capability, err := sessions.sessionForCapabilityMessage(helper3Query, clock.current())
	if err != nil || helper3Capability.confirmation == nil {
		t.Fatalf("third helper epoch capability = %+v, %v", helper3Capability, err)
	}
	if committed, err := pairing.observeLifecycleCapability(
		helper3Capability.confirmation.recordID,
		helper3Capability.confirmation.transitionDigest,
		serverRuntime.prepareLifecycleCommit,
	); err != nil || !committed {
		t.Fatalf("third helper epoch commit = %v, %v", committed, err)
	}
	credentialState, _ = credentialStore.snapshot()
	secondAuthorization = credentialState.Authorizations[diagnosticsAuthorizationIndex(credentialState.Authorizations, secondRecordID)]
	revoked := credentialState.Authorizations[diagnosticsAuthorizationIndex(credentialState.Authorizations, newRecordID)]
	if credentialState.Identity.HelperEpoch != 3 || secondAuthorization.HelperEpoch != 3 ||
		revoked.State != "revoked" || revoked.HelperEpoch != 2 || revoked.NamespaceAuthorizationEpoch != 4 {
		t.Fatalf("revoked/history helper rotation state = %+v / %+v / %+v", credentialState.Identity, secondAuthorization, revoked)
	}
	helper3ManifestPath, _ := diagnosticsNamespaceHelperEpochPath(3)
	if manifest, _, err := handle.ReadImmutable(helper3ManifestPath); err != nil || len(manifest) == 0 {
		t.Fatalf("third helper epoch manifest = %x, %v", manifest, err)
	}
	if err := handle.ScanFixedLayout(); err != nil {
		t.Fatalf("revoked historical authorization invalidated fixed layout: %v", err)
	}
	secondEpochCandidate := diagnosticsRuntimeTestAuthorizationEpoch(
		t, secondAuthorization, credentialState.Identity, secondAppPrivate, rootPath, 3, clock.current(),
	)
	if err := namespaceRuntime.authorize(context.Background(), secondEpochCandidate); err != nil {
		t.Fatalf("second installation helper-epoch authorization: %v", err)
	}
	credentialState, _ = credentialStore.snapshot()
	secondAuthorization = credentialState.Authorizations[diagnosticsAuthorizationIndex(credentialState.Authorizations, secondRecordID)]
	if _, err := sessions.sessionForAuthorization(secondAuthorization, credentialState.Identity); err != nil {
		t.Fatalf("active app could not resume beside revoked historical authorization: %v", err)
	}
}

func TestDiagnosticsRuntimeActualListenerUsesPinnedTLS13AndPrivateOperatorSocket(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("diagnostics runtime packaging is Linux-only")
	}
	port := diagnosticsRuntimeTestPort(t)
	shortRoot, err := os.MkdirTemp("/tmp", "vs-diagnostics-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(shortRoot) })
	stateDirectory := filepath.Join(shortRoot, "state")
	if err := os.Mkdir(stateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	config := &diagnosticsRuntimeConfig{
		FormatVersion: 1, ListenAddress: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		AdvertisedHost: "127.0.0.1", AdvertisedPort: uint64(port),
		Folders: []diagnosticsRuntimeFolderConfig{{FolderID: "vault-listener-test"}}, stateDirectory: stateDirectory,
	}
	raw := bytes.Repeat([]byte{0x93}, 32)
	syncthingServer := diagnosticsRuntimeSyncthingServer(
		t, "vault-listener-test", "/srv/vault-listener-test", nil, diagnosticsTestDeviceID(raw),
	)
	defer syncthingServer.Close()
	runtime, err := newDiagnosticsRuntime(
		config,
		diagnosticsTestDeviceID(raw),
		NewSyncthingClient(syncthingServer.URL, "test-key"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.start(); err != nil {
		t.Fatal(err)
	}
	defer runtime.close()
	operatorInfo, err := os.Lstat(filepath.Join(stateDirectory, "operator.sock"))
	if err != nil || operatorInfo.Mode()&os.ModeSocket == 0 || operatorInfo.Mode().Perm() != 0o600 {
		t.Fatalf("operator socket = %v, %v", operatorInfo, err)
	}
	invitation, err := requestDiagnosticsPairingInvitation(context.Background(), config, "vault-listener-test")
	if err != nil || invitation == "" {
		t.Fatalf("local pairing invitation = %q, %v", invitation, err)
	}

	address := config.ListenAddress
	legacy, err := tls.Dial("tcp", address, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12}) // #nosec G402 -- negative TLS-version test only
	if err == nil {
		_ = legacy.Close()
		t.Fatal("TLS 1.2 connection succeeded")
	}
	credentialState, _ := runtime.credentialStore.snapshot()
	pin, _ := diagnosticsTLSPrivateKeyPin(credentialState.Identity.TLSPrivatePKCS8)
	pinned, _ := diagnosticsPinnedTLSConfig(pin)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: pinned, ForceAttemptHTTP2: false}, Timeout: 5 * time.Second}
	response, err := client.Post("https://"+address+diagnosticsPairingPath, "application/cbor", bytes.NewReader([]byte{0xa0}))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || response.ContentLength != 0 {
		t.Fatalf("invalid fixed response = %d/%d", response.StatusCode, response.ContentLength)
	}
}

func diagnosticsRuntimeTestEnablement(
	t testing.TB,
	authorization diagnosticsPairingAuthorization,
	identity diagnosticsHelperCredentialIdentity,
	appPrivate ed25519.PrivateKey,
	now time.Time,
) []byte {
	t.Helper()
	helperPrivate, _ := diagnosticsSigningPrivateKey(identity.SigningSeed)
	helperPublic := helperPrivate.Public().(ed25519.PublicKey)
	helperKeyID := diagnosticsKeyID(helperPublic)
	value := diagnosticsCBORMapValue(
		diagnosticsCBORMapField(1, diagnosticsCBORTextValue(diagnosticsNamespaceCapabilityID)),
		diagnosticsCBORMapField(2, diagnosticsCBORUint(1)), diagnosticsCBORMapField(3, diagnosticsCBORUint(1)),
		diagnosticsCBORMapField(4, diagnosticsCBORUint(diagnosticsNamespaceEnablement)),
		diagnosticsCBORMapField(5, diagnosticsCBORBstr(authorization.HomeserverBinding)),
		diagnosticsCBORMapField(6, diagnosticsCBORBstr(authorization.FolderBinding)),
		diagnosticsCBORMapField(9, diagnosticsCBORBstr(authorization.AppKeyID)),
		diagnosticsCBORMapField(10, diagnosticsCBORBstr(authorization.AppPublicKey)),
		diagnosticsCBORMapField(11, diagnosticsCBORBstr(authorization.AppKeyID)),
		diagnosticsCBORMapField(12, diagnosticsCBORUint(authorization.AppEpoch)),
		diagnosticsCBORMapField(13, diagnosticsCBORBstr(helperPublic)),
		diagnosticsCBORMapField(14, diagnosticsCBORBstr(helperKeyID[:])),
		diagnosticsCBORMapField(15, diagnosticsCBORUint(authorization.HelperEpoch)),
		diagnosticsCBORMapField(19, diagnosticsCBORBstr(bytes.Repeat([]byte{0x94}, 32))),
		diagnosticsCBORMapField(26, diagnosticsCBORUint(uint64(now.Unix()))),
		diagnosticsCBORMapField(27, diagnosticsCBORUint(uint64(now.Add(5*time.Minute).Unix()))),
	)
	encoded, err := signDiagnosticsNamespaceEnablement(value, appPrivate)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func diagnosticsRuntimeTestInitialAuthorization(
	t testing.TB,
	authorization diagnosticsPairingAuthorization,
	identity diagnosticsHelperCredentialIdentity,
	appPrivate ed25519.PrivateKey,
	record diagnosticsNamespaceRootRecord,
	rootPath string,
	now time.Time,
) []byte {
	t.Helper()
	helperPrivate, _ := diagnosticsSigningPrivateKey(identity.SigningSeed)
	helperPublic := helperPrivate.Public().(ed25519.PublicKey)
	helperKeyID := diagnosticsKeyID(helperPublic)
	appKeyID := diagnosticsKeyID(appPrivate.Public().(ed25519.PublicKey))
	installation, _ := diagnosticsNamespaceInstallationBinding(appKeyID[:], authorization.HomeserverBinding, authorization.FolderBinding)
	rootBody, err := os.ReadFile(filepath.Join(rootPath, "root-manifest.cbor"))
	if err != nil {
		t.Fatal(err)
	}
	rootDigest, _ := diagnosticsNamespaceRecordDigest(rootBody)
	manifestDigest := rootDigest
	if authorization.HelperEpoch > 1 {
		manifestPath, pathErr := diagnosticsNamespaceHelperEpochPath(authorization.HelperEpoch)
		if pathErr != nil {
			t.Fatal(pathErr)
		}
		manifestBody, _, readErr := diagnosticsRuntimeTestReadNamespace(rootPath, manifestPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		manifestDigest, _ = diagnosticsNamespaceRecordDigest(manifestBody)
	}
	value := diagnosticsNamespaceAuthorizationBody(
		diagnosticsNamespaceInitialAuthorization,
		authorization.HomeserverBinding, authorization.FolderBinding, record.NamespaceID, installation[:], appKeyID[:],
		authorization.AppPublicKey, authorization.AppKeyID, authorization.AppEpoch,
		helperPublic, helperKeyID[:], authorization.HelperEpoch,
		rootDigest[:], manifestDigest[:], nil, authorization.CurrentStateDigest,
		uint64(now.Unix()), uint64(now.Add(5*time.Minute).Unix()), bytes.Repeat([]byte{0x95}, 32), 1,
	)
	candidate, err := signDiagnosticsNamespaceAuthorizationCandidate(value, appPrivate)
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

func diagnosticsRuntimeTestAuthorizationEpoch(
	t testing.TB,
	authorization diagnosticsPairingAuthorization,
	identity diagnosticsHelperCredentialIdentity,
	appPrivate ed25519.PrivateKey,
	rootPath string,
	authorizationEpoch uint64,
	now time.Time,
) []byte {
	t.Helper()
	helperPrivate, _ := diagnosticsSigningPrivateKey(identity.SigningSeed)
	helperPublic := helperPrivate.Public().(ed25519.PublicKey)
	helperKeyID := diagnosticsKeyID(helperPublic)
	appKeyID := diagnosticsKeyID(appPrivate.Public().(ed25519.PublicKey))
	installation, _ := diagnosticsNamespaceInstallationBinding(
		authorization.NamespaceInitialAppKeyID, authorization.HomeserverBinding, authorization.FolderBinding,
	)
	rootBody, err := os.ReadFile(filepath.Join(rootPath, diagnosticsNamespaceRootManifestName))
	if err != nil {
		t.Fatal(err)
	}
	rootMessage, _ := decodeDiagnosticsNamespaceMessage(rootBody)
	namespaceID, _ := rootMessage.bytesField(7, 32)
	rootDigest, _ := diagnosticsNamespaceRecordDigest(rootBody)
	manifestDigest := rootDigest
	if authorization.HelperEpoch > 1 {
		manifestPath, _ := diagnosticsNamespaceHelperEpochPath(authorization.HelperEpoch)
		manifestBody, _, readErr := diagnosticsRuntimeTestReadNamespace(rootPath, manifestPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		manifestDigest, _ = diagnosticsNamespaceRecordDigest(manifestBody)
	}
	priorPath := diagnosticsRuntimeAuthorizationPath(installation[:], authorizationEpoch-1)
	priorBody, _, err := diagnosticsRuntimeTestReadNamespace(rootPath, priorPath)
	if err != nil {
		t.Fatal(err)
	}
	priorDigest, _ := diagnosticsNamespaceRecordDigest(priorBody)
	value := diagnosticsNamespaceAuthorizationBody(
		diagnosticsNamespaceAuthorizationEpoch,
		authorization.HomeserverBinding, authorization.FolderBinding, namespaceID, installation[:], authorization.NamespaceInitialAppKeyID,
		appPrivate.Public().(ed25519.PublicKey), appKeyID[:], authorization.AppEpoch,
		helperPublic, helperKeyID[:], authorization.HelperEpoch,
		rootDigest[:], manifestDigest[:], priorDigest[:], authorization.CurrentStateDigest,
		uint64(now.Unix()), uint64(now.Add(5*time.Minute).Unix()), bytes.Repeat([]byte{byte(0xa0 + authorizationEpoch)}, 32), authorizationEpoch,
	)
	candidate, err := signDiagnosticsNamespaceAuthorizationCandidate(value, appPrivate)
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

func diagnosticsRuntimeTestReadNamespace(
	rootPath string,
	path diagnosticsNamespacePath,
) ([]byte, diagnosticsNamespaceFileIdentity, error) {
	handle, err := openDiagnosticsNamespaceRoot(rootPath, nil)
	if err != nil {
		return nil, diagnosticsNamespaceFileIdentity{}, err
	}
	defer handle.Close()
	return handle.ReadImmutable(path)
}

func diagnosticsRuntimeTestCapabilityQuery(
	t testing.TB,
	binding diagnosticsUploadBinding,
	appPrivate ed25519.PrivateKey,
	now time.Time,
) []byte {
	t.Helper()
	appKeyID := diagnosticsKeyID(binding.appPublicKey)
	helperPublic := binding.helperPrivateKey.Public().(ed25519.PublicKey)
	helperKeyID := diagnosticsKeyID(helperPublic)
	value := diagnosticsCBORMapValue(
		diagnosticsCBORMapField(1, diagnosticsCBORTextValue(diagnosticsRoundtripCapabilityID)),
		diagnosticsCBORMapField(2, diagnosticsCBORUint(1)), diagnosticsCBORMapField(3, diagnosticsCBORUint(1)),
		diagnosticsCBORMapField(4, diagnosticsCBORUint(diagnosticsCapabilityQuery)),
		diagnosticsCBORMapField(5, diagnosticsCBORBstr(binding.homeserverBinding)),
		diagnosticsCBORMapField(6, diagnosticsCBORBstr(binding.folderBinding)),
		diagnosticsCBORMapField(7, diagnosticsCBORBstr(appKeyID[:])), diagnosticsCBORMapField(8, diagnosticsCBORBstr(helperKeyID[:])),
		diagnosticsCBORMapField(9, diagnosticsCBORUint(binding.appEpoch)), diagnosticsCBORMapField(10, diagnosticsCBORUint(binding.helperEpoch)),
		diagnosticsCBORMapField(12, diagnosticsCBORUint(uint64(now.Unix()))),
		diagnosticsCBORMapField(13, diagnosticsCBORUint(uint64(now.Add(2*time.Minute).Unix()))),
		diagnosticsCBORMapField(30, diagnosticsCBORBstr(bytes.Repeat([]byte{0xa9}, 32))),
	)
	context := diagnosticsUploadVerificationContext{appPublicKey: binding.appPublicKey, helperPublicKey: helperPublic}
	encoded, err := signDiagnosticsCapabilityMessage(value, diagnosticsCapabilityQuery, appPrivate, context)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func diagnosticsRuntimeSyncthingServer(
	t testing.TB,
	folderID, folderPath string,
	expanded []string,
	deviceID string,
	ignoreErrors ...string,
) *httptest.Server {
	t.Helper()
	ignoreError := ""
	if len(ignoreErrors) > 0 {
		ignoreError = ignoreErrors[0]
	}
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-API-Key") != "test-key" {
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		switch request.URL.Path {
		case "/rest/system/status":
			_ = json.NewEncoder(writer).Encode(map[string]string{"myID": deviceID})
		case "/rest/config/folders":
			_ = json.NewEncoder(writer).Encode([]folderConfig{{ID: folderID, Path: folderPath, Type: "sendreceive"}})
		case "/rest/db/ignores":
			if request.URL.Query().Get("folder") != folderID {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(writer).Encode(syncthingIgnoreConfig{Expanded: expanded, Error: ignoreError})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
}

func diagnosticsRuntimeTestPort(t testing.TB) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	if port == 0 {
		t.Fatal(fmt.Errorf("invalid test port %q", portText))
	}
	return port
}
