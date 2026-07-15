//go:build linux && diagnostics_m5_syncthing_e2e

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDiagnosticsDownloadThroughTwoEphemeralSyncthingInstances proves the M6
// response leg over the real transport: the exact upload chain propagates from
// the app namespace to the helper, the real helper response foundation creates
// the signed response artifact only from a valid authorization, and that exact
// artifact propagates back to the app namespace byte-identically where the full
// D024 chain validates. Download acceptance itself (fresh ItemFinished plus
// baseline gates) is the app runtime's claim and is proven in the Swift suite.
func TestDiagnosticsDownloadThroughTwoEphemeralSyncthingInstances(t *testing.T) {
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
	golden := generateDiagnosticsResponseGoldenMessages(t, fixture)

	// The app-authored request and the helper attestation reach the helper
	// runtime only through the synchronized namespace.
	requestPath := diagnosticsUploadOperationPath(t, fixture, 1)
	attestationPath := diagnosticsUploadOperationPath(t, fixture, 2)
	responsePath := diagnosticsUploadOperationPath(t, fixture, 3)
	if _, err := prepared.handle.CreateImmutable(requestPath, golden.upload.request.canonical); err != nil {
		t.Fatal("create exact app-authored request")
	}
	if _, err := prepared.handle.CreateImmutable(attestationPath, golden.upload.attestation.canonical); err != nil {
		t.Fatal("create exact attestation artifact")
	}
	waitForDiagnosticsM5Immutable(t, helperHandle, requestPath, golden.upload.request.canonical)
	waitForDiagnosticsM5Immutable(t, helperHandle, attestationPath, golden.upload.attestation.canonical)

	// The real helper response foundation, reconstructing solely from its
	// synchronized namespace, accepts the exact authorization and creates the
	// one signed response artifact.
	foundation := newDiagnosticsResponseGoldenFoundation(
		t, helperPrepared, fixture, diagnosticsResponseGoldenRandom(golden),
		func() time.Time { return time.Unix(int64(diagnosticsResponseGoldenResponseIssuedAt), 0) },
		nil, newDiagnosticsUploadCoordinator(),
	)
	result := foundation.authorizeResponse(golden.authorization.canonical)
	if result.disposition != diagnosticsResponseAccepted || result.reason != diagnosticsResponseReasonNone {
		t.Fatalf("helper authorization result = %#v", result)
	}
	persisted, _, err := helperHandle.ReadImmutable(responsePath)
	if err != nil || !bytes.Equal(persisted, golden.response.canonical) {
		t.Fatalf("helper-side response artifact = %v", err)
	}

	// The exact helper-authored bytes must become readable in the app
	// namespace through Syncthing and validate through the full D024 chain.
	synced := waitForDiagnosticsM5Immutable(t, prepared.handle, responsePath, golden.response.canonical)
	response, decodeErr := decodeDiagnosticsResponseMessage(synced, golden.upload.context)
	if decodeErr != nil {
		t.Fatal("decode synchronized response artifact")
	}
	if chainErr := validateDiagnosticsResponseArtifactChain(
		golden.upload.request, golden.upload.attestation, golden.authorization, response,
	); chainErr != nil {
		t.Fatalf("synchronized response failed the causal chain: %v", chainErr)
	}

	// A helper restart replays idempotently and never rewrites the artifact.
	restarted := newDiagnosticsResponseGoldenFoundation(
		t, helperPrepared, fixture, bytes.NewReader(bytes.Repeat([]byte{0xee}, 288)),
		func() time.Time { return time.Unix(int64(diagnosticsResponseGoldenResponseIssuedAt+1), 0) },
		nil, newDiagnosticsUploadCoordinator(),
	)
	if replay := restarted.authorizeResponse(golden.authorization.canonical); replay.disposition != diagnosticsResponseAccepted {
		t.Fatalf("idempotent helper replay = %#v", replay)
	}
	replayed, _, err := helperHandle.ReadImmutable(responsePath)
	if err != nil || !bytes.Equal(replayed, golden.response.canonical) {
		t.Fatal("helper replay changed the persisted response artifact")
	}
	if err := prepared.handle.ScanFixedLayout(); err != nil || helperHandle.ScanFixedLayout() != nil {
		t.Fatal("synchronized namespaces were not exact after the response leg")
	}
}
