//go:build linux && diagnostics_m5_syncthing_e2e

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const diagnosticsM5SyncthingFolderID = "vaultsync-m5-upload"

type diagnosticsM5SyncthingInstance struct {
	binary        string
	home          string
	apiURL        string
	apiKey        string
	deviceID      string
	listenAddress string
	client        *http.Client
	command       *exec.Cmd
	done          chan struct{}
	stopOnce      sync.Once
}

type diagnosticsM5FreshUpload struct {
	operationID []byte
	request     diagnosticsUploadMessage
	query       diagnosticsUploadMessage
}

func TestDiagnosticsUploadThroughTwoEphemeralSyncthingInstances(t *testing.T) {
	binary := os.Getenv("VAULTSYNC_M5_SYNCTHING_BIN")
	if binary == "" {
		t.Skip("explicit local Syncthing test binary not provided")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("local Syncthing test binary must use an absolute path")
	}
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		t.Fatal("local Syncthing test binary is unavailable")
	}

	prepared := prepareDiagnosticsNamespaceLinuxFixture(t)
	defer prepared.handle.Close()
	helperParent := filepath.Join(t.TempDir(), "helper-vault")
	if err := os.Mkdir(helperParent, 0o700); err != nil || os.Mkdir(filepath.Join(helperParent, ".stfolder"), 0o700) != nil {
		t.Fatal("create isolated helper folder")
	}

	appSyncthing := startDiagnosticsM5Syncthing(t, binary, "app")
	helperSyncthing := startDiagnosticsM5Syncthing(t, binary, "helper")
	defer appSyncthing.stop()
	defer helperSyncthing.stop()
	configureDiagnosticsM5Syncthing(t, appSyncthing, helperSyncthing, prepared.parentPath)
	configureDiagnosticsM5Syncthing(t, helperSyncthing, appSyncthing, helperParent)
	waitForDiagnosticsM5SyncthingConnection(t, appSyncthing, helperSyncthing.deviceID)
	waitForDiagnosticsM5SyncthingConnection(t, helperSyncthing, appSyncthing.deviceID)

	helperRootPath := filepath.Join(helperParent, diagnosticsNamespaceRootName)
	helperHandle := waitForDiagnosticsM5Namespace(t, helperRootPath)
	defer helperHandle.Close()
	helperPrepared := prepared
	helperPrepared.parentPath = helperParent
	helperPrepared.rootPath = helperRootPath
	helperPrepared.handle = helperHandle

	fixture := loadDiagnosticsUploadGoldenFixture(t)
	golden := generateDiagnosticsUploadGoldenMessages(t, fixture)
	fresh := generateDiagnosticsM5FreshUpload(t, fixture, golden)
	requestPath := diagnosticsM5OperationPath(t, fixture, fresh.operationID, 1)
	if _, err := prepared.handle.CreateImmutable(requestPath, fresh.request.canonical); err != nil {
		t.Fatal("create fresh app-authored request")
	}
	waitForDiagnosticsM5Immutable(t, helperHandle, requestPath, fresh.request.canonical)

	now := time.Unix(int64(fixture.AttestationIssuedAt), 0)
	helperNonce := diagnosticsM5RandomNonzeroBytes(t, 32)
	attestor := newDiagnosticsUploadGoldenAttestor(
		t, helperPrepared, fixture, bytes.NewReader(helperNonce),
		func() time.Time { return now }, nil, nil,
	)
	result := attestor.attest(fresh.query.canonical)
	attestation, decodeErr := decodeDiagnosticsUploadMessage(result.attestation, golden.context)
	if result.disposition != diagnosticsUploadAccepted || decodeErr != nil ||
		validateDiagnosticsUploadChain(fresh.request, fresh.query, attestation) != nil {
		t.Fatalf("mock pinned helper response was not an exact D024 attestation: disposition=%d decode=%v", result.disposition, decodeErr)
	}
	if !acceptDiagnosticsM5MockPinnedUpload(
		fresh.request, fresh.query, fresh.query.canonical, result.attestation, golden.context, uint64(now.Unix()), true,
	) {
		t.Fatal("exact mock pinned helper response did not establish upload acceptance")
	}

	attestationPath := diagnosticsM5OperationPath(t, fixture, fresh.operationID, 2)
	copiedAttestation := waitForDiagnosticsM5Immutable(t, prepared.handle, attestationPath, result.attestation)
	if acceptDiagnosticsM5MockPinnedUpload(
		fresh.request, fresh.query, fresh.query.canonical, copiedAttestation, golden.context, uint64(now.Unix()), false,
	) {
		t.Fatal("Syncthing-delivered attestation copy was treated as pinned-channel upload evidence")
	}
	if err := prepared.handle.ScanFixedLayout(); err != nil || helperHandle.ScanFixedLayout() != nil {
		t.Fatal("synchronized namespace was not exact after upload-only attestation")
	}
}

func generateDiagnosticsM5FreshUpload(
	t testing.TB, fixture diagnosticsUploadGoldenFixture, golden diagnosticsUploadGoldenMessages,
) diagnosticsM5FreshUpload {
	t.Helper()
	homeserver := diagnosticsUploadMustHex(t, fixture.HomeserverBindingHex)
	folder := diagnosticsUploadMustHex(t, fixture.FolderBindingHex)
	operationID := diagnosticsM5RandomNonzeroBytes(t, 32)
	requestNonce := diagnosticsM5RandomNonzeroBytes(t, 32)
	queryNonce := diagnosticsM5RandomNonzeroBytes(t, 32)
	payload := diagnosticsM5RandomNonzeroBytes(t, diagnosticsUploadPayloadBytes)
	payloadDigest := sha256.Sum256(payload)
	appKeyID := diagnosticsKeyID(golden.context.appPublicKey)
	helperKeyID := diagnosticsKeyID(golden.context.helperPublicKey)
	common := func(messageType, issuedAt uint64) []diagnosticsCBORField {
		return []diagnosticsCBORField{
			diagnosticsCBORMapField(1, diagnosticsCBORTextValue(diagnosticsRoundtripCapabilityID)),
			diagnosticsCBORMapField(2, diagnosticsCBORUint(1)),
			diagnosticsCBORMapField(3, diagnosticsCBORUint(1)),
			diagnosticsCBORMapField(4, diagnosticsCBORUint(messageType)),
			diagnosticsCBORMapField(5, diagnosticsCBORBstr(homeserver)),
			diagnosticsCBORMapField(6, diagnosticsCBORBstr(folder)),
			diagnosticsCBORMapField(7, diagnosticsCBORBstr(appKeyID[:])),
			diagnosticsCBORMapField(8, diagnosticsCBORBstr(helperKeyID[:])),
			diagnosticsCBORMapField(9, diagnosticsCBORUint(fixture.AppEpoch)),
			diagnosticsCBORMapField(10, diagnosticsCBORUint(fixture.HelperEpoch)),
			diagnosticsCBORMapField(11, diagnosticsCBORBstr(operationID)),
			diagnosticsCBORMapField(12, diagnosticsCBORUint(issuedAt)),
			diagnosticsCBORMapField(13, diagnosticsCBORUint(fixture.ExpiresAt)),
		}
	}
	requestValue := diagnosticsCBORMapValue(append(
		common(diagnosticsUploadOperationRequest, fixture.RequestIssuedAt),
		diagnosticsCBORMapField(14, diagnosticsCBORBstr(requestNonce)),
		diagnosticsCBORMapField(15, diagnosticsCBORBstr(payload)),
		diagnosticsCBORMapField(16, diagnosticsCBORBstr(payloadDigest[:])),
	)...)
	requestBytes := signDiagnosticsUploadTestMessage(
		t, requestValue, diagnosticsUploadOperationRequest, golden.appPrivate, golden.context,
	)
	request, err := decodeDiagnosticsUploadMessage(requestBytes, golden.context)
	if err != nil {
		t.Fatal("decode fresh app-authored request")
	}
	queryValue := diagnosticsCBORMapValue(append(
		common(diagnosticsUploadAttestationQuery, fixture.QueryIssuedAt),
		diagnosticsCBORMapField(17, diagnosticsCBORBstr(request.digest[:])),
		diagnosticsCBORMapField(30, diagnosticsCBORBstr(queryNonce)),
	)...)
	queryBytes := signDiagnosticsUploadTestMessage(
		t, queryValue, diagnosticsUploadAttestationQuery, golden.appPrivate, golden.context,
	)
	query, err := decodeDiagnosticsUploadMessage(queryBytes, golden.context)
	if err != nil || validateDiagnosticsUploadRequestAndQuery(request, query) != nil {
		t.Fatal("decode fresh exact attestation query")
	}
	return diagnosticsM5FreshUpload{operationID: operationID, request: request, query: query}
}

func diagnosticsM5RandomNonzeroBytes(t testing.TB, size int) []byte {
	t.Helper()
	for {
		value := make([]byte, size)
		if _, err := io.ReadFull(rand.Reader, value); err != nil {
			t.Fatal("read fresh D024 randomness")
		}
		if nonzeroDiagnosticsBytes(value) {
			return value
		}
	}
}

func diagnosticsM5OperationPath(
	t testing.TB, fixture diagnosticsUploadGoldenFixture, operationID []byte, kind uint64,
) diagnosticsNamespacePath {
	t.Helper()
	path, err := diagnosticsNamespaceOperationPath(
		diagnosticsUploadMustHex(t, fixture.InstallationBindingHex), operationID, kind,
	)
	if err != nil {
		t.Fatal("derive exact fresh operation path")
	}
	return path
}

func startDiagnosticsM5Syncthing(t testing.TB, binary, role string) *diagnosticsM5SyncthingInstance {
	t.Helper()
	home := t.TempDir()
	if err := runDiagnosticsM5Command(binary, "generate", "--home", home, "--no-port-probing"); err != nil {
		t.Fatal("generate isolated Syncthing configuration")
	}
	listenAddress := reserveDiagnosticsM5LoopbackAddress(t)
	guiAddress := reserveDiagnosticsM5LoopbackAddress(t)
	if err := hardenDiagnosticsM5SyncthingConfig(filepath.Join(home, "config.xml"), listenAddress); err != nil {
		t.Fatal(err)
	}
	deviceContext, cancelDevice := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelDevice()
	deviceOutput, err := exec.CommandContext(deviceContext, binary, "device-id", "--home", home).Output()
	deviceID := strings.TrimSpace(string(deviceOutput))
	if err != nil || deviceID == "" || strings.ContainsAny(deviceID, " \t\r\n") {
		t.Fatal("read isolated Syncthing device ID")
	}
	instance := &diagnosticsM5SyncthingInstance{
		binary: binary, home: home, apiURL: "http://" + guiAddress, apiKey: "m5-local-" + role,
		deviceID: deviceID, listenAddress: listenAddress, client: &http.Client{Timeout: 2 * time.Second},
		done: make(chan struct{}),
	}
	instance.command = exec.Command(
		binary, "serve", "--home", home, "--gui-address=http://"+guiAddress, "--gui-apikey="+instance.apiKey,
		"--no-browser", "--no-restart", "--no-upgrade", "--no-port-probing", "--log-level=WARN", "--log-file=-",
	)
	instance.command.Stdout = io.Discard
	instance.command.Stderr = io.Discard
	if err := instance.command.Start(); err != nil {
		t.Fatal("start isolated Syncthing instance")
	}
	go func() {
		_ = instance.command.Wait()
		close(instance.done)
	}()
	t.Cleanup(instance.stop)
	waitForDiagnosticsM5API(t, instance)
	verifyDiagnosticsM5SyncthingIsolation(t, instance)
	return instance
}

func runDiagnosticsM5Command(binary string, arguments ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, arguments...)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
}

func verifyDiagnosticsM5SyncthingIsolation(t testing.TB, instance *diagnosticsM5SyncthingInstance) {
	t.Helper()
	var configuration struct {
		Options struct {
			ListenAddresses       []string `json:"listenAddresses"`
			GlobalAnnounceEnabled bool     `json:"globalAnnounceEnabled"`
			LocalAnnounceEnabled  bool     `json:"localAnnounceEnabled"`
			RelaysEnabled         bool     `json:"relaysEnabled"`
			NATEnabled            bool     `json:"natEnabled"`
			URAccepted            int      `json:"urAccepted"`
			AutoUpgradeIntervalH  int      `json:"autoUpgradeIntervalH"`
			CrashReportingEnabled bool     `json:"crashReportingEnabled"`
		} `json:"options"`
	}
	if err := instance.request(context.Background(), http.MethodGet, "/rest/config", nil, &configuration); err != nil {
		t.Fatal("read isolated Syncthing security configuration")
	}
	wantedListen := "tcp://" + instance.listenAddress
	options := configuration.Options
	if len(options.ListenAddresses) != 1 || options.ListenAddresses[0] != wantedListen ||
		options.GlobalAnnounceEnabled || options.LocalAnnounceEnabled || options.RelaysEnabled || options.NATEnabled ||
		options.URAccepted != 0 || options.AutoUpgradeIntervalH != 0 || options.CrashReportingEnabled {
		t.Fatal("isolated Syncthing external-network settings were not disabled")
	}
}

func reserveDiagnosticsM5LoopbackAddress(t testing.TB) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("reserve loopback address")
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal("release loopback address reservation")
	}
	return address
}

func hardenDiagnosticsM5SyncthingConfig(path, listenAddress string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return errors.New("read isolated Syncthing configuration")
	}
	replacements := [][2]string{
		{"<listenAddress>default</listenAddress>", "<listenAddress>tcp://" + listenAddress + "</listenAddress>"},
		{"<globalAnnounceEnabled>true</globalAnnounceEnabled>", "<globalAnnounceEnabled>false</globalAnnounceEnabled>"},
		{"<localAnnounceEnabled>true</localAnnounceEnabled>", "<localAnnounceEnabled>false</localAnnounceEnabled>"},
		{"<relaysEnabled>true</relaysEnabled>", "<relaysEnabled>false</relaysEnabled>"},
		{"<natEnabled>true</natEnabled>", "<natEnabled>false</natEnabled>"},
		{"<autoUpgradeIntervalH>12</autoUpgradeIntervalH>", "<autoUpgradeIntervalH>0</autoUpgradeIntervalH>"},
		{"<crashReportingEnabled>true</crashReportingEnabled>", "<crashReportingEnabled>false</crashReportingEnabled>"},
	}
	for _, replacement := range replacements {
		if bytes.Count(body, []byte(replacement[0])) != 1 {
			return errors.New("unexpected isolated Syncthing configuration shape")
		}
		body = bytes.Replace(body, []byte(replacement[0]), []byte(replacement[1]), 1)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return errors.New("write isolated Syncthing configuration")
	}
	return nil
}

func waitForDiagnosticsM5API(t testing.TB, instance *diagnosticsM5SyncthingInstance) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-instance.done:
			t.Fatal("isolated Syncthing exited before API readiness")
		default:
		}
		var status struct {
			MyID string `json:"myID"`
		}
		if instance.request(context.Background(), http.MethodGet, "/rest/system/status", nil, &status) == nil && status.MyID == instance.deviceID {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("isolated Syncthing API readiness timed out")
}

func configureDiagnosticsM5Syncthing(t testing.TB, local, peer *diagnosticsM5SyncthingInstance, folderPath string) {
	t.Helper()
	var configuration map[string]any
	if err := local.request(context.Background(), http.MethodGet, "/rest/config", nil, &configuration); err != nil {
		t.Fatal("read isolated Syncthing configuration API")
	}
	var device map[string]any
	if err := local.request(context.Background(), http.MethodGet, "/rest/config/defaults/device", nil, &device); err != nil {
		t.Fatal("read isolated Syncthing device defaults")
	}
	device["deviceID"] = peer.deviceID
	device["name"] = "m5-local-peer"
	device["addresses"] = []string{"tcp://" + peer.listenAddress}
	devices, ok := configuration["devices"].([]any)
	if !ok {
		t.Fatal("isolated Syncthing device configuration shape changed")
	}
	configuration["devices"] = append(devices, device)

	var folder map[string]any
	if err := local.request(context.Background(), http.MethodGet, "/rest/config/defaults/folder", nil, &folder); err != nil {
		t.Fatal("read isolated Syncthing folder defaults")
	}
	folder["id"] = diagnosticsM5SyncthingFolderID
	folder["label"] = "M5 Local Upload Proof"
	folder["path"] = folderPath
	folder["type"] = "sendreceive"
	folder["rescanIntervalS"] = 1
	folder["fsWatcherEnabled"] = true
	folder["fsWatcherDelayS"] = 1
	folderDevices, ok := folder["devices"].([]any)
	if !ok {
		t.Fatal("isolated Syncthing folder configuration shape changed")
	}
	folder["devices"] = append(folderDevices, map[string]any{
		"deviceID": peer.deviceID, "introducedBy": "", "encryptionPassword": "",
	})
	folders, ok := configuration["folders"].([]any)
	if !ok {
		t.Fatal("isolated Syncthing folder list shape changed")
	}
	configuration["folders"] = append(folders, folder)
	if err := local.request(context.Background(), http.MethodPut, "/rest/config", configuration, nil); err != nil {
		t.Fatal("apply isolated Syncthing peer/folder configuration")
	}
}

func waitForDiagnosticsM5SyncthingConnection(t testing.TB, instance *diagnosticsM5SyncthingInstance, peerID string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var state struct {
			Connections map[string]struct {
				Connected bool `json:"connected"`
			} `json:"connections"`
		}
		if instance.request(context.Background(), http.MethodGet, "/rest/system/connections", nil, &state) == nil && state.Connections[peerID].Connected {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("isolated Syncthing peer connection timed out")
}

func waitForDiagnosticsM5Namespace(t testing.TB, rootPath string) *diagnosticsNamespaceRootHandle {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		handle, err := openDiagnosticsNamespaceRoot(rootPath, nil)
		if err == nil {
			if scanErr := handle.ScanFixedLayout(); scanErr == nil {
				return handle
			}
			_ = handle.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("exact authenticated namespace did not synchronize")
	return nil
}

func waitForDiagnosticsM5Immutable(
	t testing.TB, handle *diagnosticsNamespaceRootHandle, path diagnosticsNamespacePath, expected []byte,
) []byte {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		body, _, err := handle.ReadImmutable(path)
		if err == nil && bytes.Equal(body, expected) && handle.ScanFixedLayout() == nil {
			return body
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("exact immutable upload artifact did not synchronize")
	return nil
}

func acceptDiagnosticsM5MockPinnedUpload(
	request, query diagnosticsUploadMessage,
	exactQuery, response []byte,
	verification diagnosticsUploadVerificationContext,
	now uint64,
	pinned bool,
) bool {
	if !pinned || !bytes.Equal(exactQuery, query.canonical) || validateDiagnosticsUploadClock(request, now) != nil ||
		validateDiagnosticsUploadClock(query, now) != nil {
		return false
	}
	attestation, err := decodeDiagnosticsUploadMessage(response, verification)
	return err == nil && validateDiagnosticsUploadClock(attestation, now) == nil &&
		validateDiagnosticsUploadChain(request, query, attestation) == nil
}

func (instance *diagnosticsM5SyncthingInstance) request(
	ctx context.Context, method, path string, input any, output any,
) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, instance.apiURL+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("X-API-Key", instance.apiKey)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := instance.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return errors.New("isolated Syncthing API rejected request")
	}
	if output == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return err
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output)
}

func (instance *diagnosticsM5SyncthingInstance) stop() {
	instance.stopOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = instance.request(ctx, http.MethodPost, "/rest/system/shutdown", nil, nil)
		select {
		case <-instance.done:
			return
		case <-time.After(3 * time.Second):
		}
		if instance.command != nil && instance.command.Process != nil {
			_ = instance.command.Process.Kill()
		}
		select {
		case <-instance.done:
		case <-time.After(2 * time.Second):
		}
	})
}
