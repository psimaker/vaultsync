package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDiagnosticsRuntimeCBORMatchesDecision022BootstrapGolden(t *testing.T) {
	vector := loadDiagnosticsContractFixture(t).Vectors.BootstrapHMAC
	body := mustDecodeHex(t, vector.CanonicalBodyHex)
	decoded, err := decodeDiagnosticsCBOR(body)
	if err != nil {
		t.Fatalf("decode normative body: %v", err)
	}
	reencoded, err := encodeDiagnosticsCBOR(decoded)
	if err != nil || !bytes.Equal(reencoded, body) {
		t.Fatalf("runtime CBOR changed normative bytes: %v", err)
	}
	secret := mustDecodeHex(t, vector.SecretHex)
	mac, err := diagnosticsPairingBootstrapHMAC(secret, decoded)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(mac[:]); got != vector.ExpectedHMACHex {
		t.Fatalf("bootstrap HMAC = %s, want %s", got, vector.ExpectedHMACHex)
	}
}

func TestDiagnosticsCredentialStorePermissionsBindingsAndAtomicCrashSafety(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows diagnostics ACL support is intentionally unavailable")
	}
	root := filepath.Join(t.TempDir(), "diagnostics-state")
	deviceDigest := bytes.Repeat([]byte{0x31}, 32)
	store, err := openDiagnosticsCredentialStore(root, deviceDigest, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	directoryInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Stat(filepath.Join(root, diagnosticsCredentialStateFile))
	if err != nil {
		t.Fatal(err)
	}
	if directoryInfo.Mode().Perm() != 0o700 || fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("state permissions = %04o/%04o, want 0700/0600", directoryInfo.Mode().Perm(), fileInfo.Mode().Perm())
	}

	folderDigest := bytes.Repeat([]byte{0x41}, 32)
	binding, err := store.reserveFolderBinding(folderDigest)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := openDiagnosticsCredentialStore(root, deviceDigest, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	reopenedBinding, err := reopened.reserveFolderBinding(folderDigest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(binding, reopenedBinding) {
		t.Fatal("folder binding changed after reopen")
	}
	before, err := reopened.snapshot()
	if err != nil {
		t.Fatal(err)
	}

	crash := errors.New("injected pre-rename crash")
	reopened.hooks.beforeRename = func() error { return crash }
	if _, err := reopened.reserveFolderBinding(bytes.Repeat([]byte{0x42}, 32)); !errors.Is(err, crash) {
		t.Fatalf("pre-rename crash = %v, want injected error", err)
	}
	reopened.hooks.beforeRename = nil
	after, err := reopened.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision || len(after.Folders) != len(before.Folders) {
		t.Fatalf("pre-rename crash changed durable state: before=%d/%d after=%d/%d", before.Revision, len(before.Folders), after.Revision, len(after.Folders))
	}

	uncertain := errors.New("injected post-rename crash")
	reopened.hooks.afterRename = func() error { return uncertain }
	if _, err := reopened.reserveFolderBinding(bytes.Repeat([]byte{0x43}, 32)); !errors.Is(err, uncertain) {
		t.Fatalf("post-rename crash = %v, want injected error", err)
	}
	reopened.hooks.afterRename = nil
	recovered, err := reopened.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Revision != before.Revision+1 || len(recovered.Folders) != len(before.Folders)+1 {
		t.Fatalf("post-rename recovery lost committed state: before=%d/%d after=%d/%d", before.Revision, len(before.Folders), recovered.Revision, len(recovered.Folders))
	}
}

func TestDiagnosticsCredentialStoreConcurrentBindingReservationIsStable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows diagnostics ACL support is intentionally unavailable")
	}
	store, err := openDiagnosticsCredentialStore(filepath.Join(t.TempDir(), "state"), bytes.Repeat([]byte{0x21}, 32), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	folder := bytes.Repeat([]byte{0x22}, 32)
	results := make(chan []byte, 32)
	errorsChannel := make(chan error, 32)
	var group sync.WaitGroup
	for range 32 {
		group.Add(1)
		go func() {
			defer group.Done()
			binding, err := store.reserveFolderBinding(folder)
			results <- binding
			errorsChannel <- err
		}()
	}
	group.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	var expected []byte
	for binding := range results {
		if expected == nil {
			expected = binding
		}
		if !bytes.Equal(binding, expected) {
			t.Fatal("concurrent reservation produced multiple bindings")
		}
	}
}

func TestDiagnosticsCredentialStoreRejectsInsecureModeAndNewerFormat(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows diagnostics ACL support is intentionally unavailable")
	}
	root := filepath.Join(t.TempDir(), "state")
	device := bytes.Repeat([]byte{0x25}, 32)
	if _, err := openDiagnosticsCredentialStore(root, device, rand.Reader); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := openDiagnosticsCredentialStore(root, device, rand.Reader); !errors.Is(err, errDiagnosticsCredentialStateInvalid) {
		t.Fatalf("insecure directory accepted: %v", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, diagnosticsCredentialStateFile)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body = bytes.Replace(body, []byte(`"format_version":1`), []byte(`"format_version":2`), 1)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openDiagnosticsCredentialStore(root, device, rand.Reader); !errors.Is(err, errDiagnosticsCredentialStateNewer) {
		t.Fatalf("newer state format accepted: %v", err)
	}
}

func TestDiagnosticsCredentialStoreRejectsLinksAndTrailingState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows diagnostics ACL support is intentionally unavailable")
	}
	device := bytes.Repeat([]byte{0x26}, 32)
	newStore := func(t *testing.T) (string, string) {
		t.Helper()
		root := filepath.Join(t.TempDir(), "state")
		if _, err := openDiagnosticsCredentialStore(root, device, rand.Reader); err != nil {
			t.Fatal(err)
		}
		return root, filepath.Join(root, diagnosticsCredentialStateFile)
	}

	t.Run("state symlink", func(t *testing.T) {
		root, statePath := newStore(t)
		body, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(statePath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, statePath); err != nil {
			t.Fatal(err)
		}
		if _, err := openDiagnosticsCredentialStore(root, device, rand.Reader); !errors.Is(err, errDiagnosticsCredentialStateInvalid) {
			t.Fatalf("state symlink accepted: %v", err)
		}
	})

	t.Run("state hardlink", func(t *testing.T) {
		root, statePath := newStore(t)
		if err := os.Link(statePath, filepath.Join(root, "state-copy")); err != nil {
			t.Fatal(err)
		}
		if _, err := openDiagnosticsCredentialStore(root, device, rand.Reader); !errors.Is(err, errDiagnosticsCredentialStateInvalid) {
			t.Fatalf("state hardlink accepted: %v", err)
		}
	})

	t.Run("lock symlink", func(t *testing.T) {
		root, statePath := newStore(t)
		lockPath := filepath.Join(root, diagnosticsCredentialLockFile)
		if err := os.Remove(lockPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(statePath, lockPath); err != nil {
			t.Fatal(err)
		}
		if _, err := openDiagnosticsCredentialStore(root, device, rand.Reader); !errors.Is(err, errDiagnosticsCredentialStateInvalid) {
			t.Fatalf("lock symlink accepted: %v", err)
		}
	})

	t.Run("trailing JSON", func(t *testing.T) {
		root, statePath := newStore(t)
		body, err := os.ReadFile(statePath)
		if err != nil {
			t.Fatal(err)
		}
		body = append(body, []byte("{}")...)
		if err := os.WriteFile(statePath, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := openDiagnosticsCredentialStore(root, device, rand.Reader); !errors.Is(err, errDiagnosticsCredentialStateInvalid) {
			t.Fatalf("trailing state accepted: %v", err)
		}
	})
}

func TestDiagnosticsPairingPendingFinalizeReceiptActivateAbortAndReplay(t *testing.T) {
	manager, clock := newDiagnosticsTestPairingManager(t)
	folderDigest := bytes.Repeat([]byte{0x42}, 32)
	qr, err := manager.beginInvitation(folderDigest, "helper.test", 443)
	if err != nil {
		t.Fatal(err)
	}
	invitation, err := decodeDiagnosticsPairingQR(qr)
	if err != nil {
		t.Fatal(err)
	}
	_, appPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	appRequestBytes, err := buildDiagnosticsAppPairingRequest(invitation, appPrivate, bytes.Repeat([]byte{0x61}, 32))
	if err != nil {
		t.Fatal(err)
	}
	parsedAppRequest, err := decodeDiagnosticsPairingMessage(appRequestBytes)
	if err != nil {
		t.Fatalf("decode app request: %v", err)
	}
	if !diagnosticsPairingEchoMatches(invitation, parsedAppRequest) {
		t.Fatal("app request did not echo the invitation")
	}
	secret, _ := invitation.bytesField(17, 32)
	if !verifyDiagnosticsPairingBootstrapHMAC(secret, parsedAppRequest) {
		t.Fatal("app request bootstrap HMAC did not verify")
	}
	requestDigest, _ := parsedAppRequest.digest()
	invitationNonce, _ := parsedAppRequest.bytesField(5, 32)
	pending := manager.invitations[base64.RawURLEncoding.EncodeToString(invitationNonce)]
	_, candidateAuthorization, err := manager.makeHelperAcceptance(parsedAppRequest, requestDigest, pending, clock.current())
	if err != nil {
		t.Fatalf("make helper acceptance: %v", err)
	}
	if err := validateDiagnosticsAuthorization(candidateAuthorization); err != nil {
		t.Fatalf("validate candidate authorization: %v", err)
	}
	helperAcceptBytes, err := manager.acceptAppRequest(appRequestBytes)
	if err != nil {
		t.Fatal(err)
	}
	replayedAccept, err := manager.acceptAppRequest(appRequestBytes)
	if err != nil || !bytes.Equal(replayedAccept, helperAcceptBytes) {
		t.Fatalf("accept replay changed bytes: %v", err)
	}
	helperAccept, _ := decodeDiagnosticsPairingMessage(helperAcceptBytes)
	appRequest, _ := decodeDiagnosticsPairingMessage(appRequestBytes)
	appDigest, _ := appRequest.digest()
	helperDigest, _ := helperAccept.digest()
	fingerprint, err := diagnosticsPairingFingerprint(appDigest[:], helperDigest[:])
	if err != nil || len(fingerprint) != 12 || fingerprint != strings.ToUpper(fingerprint) {
		t.Fatalf("fingerprint = %q, err=%v", fingerprint, err)
	}
	state, err := manager.store.snapshot()
	if err != nil || len(state.Authorizations) != 1 {
		t.Fatalf("pending authorization snapshot = %d, %v", len(state.Authorizations), err)
	}
	pendingLine, err := diagnosticsAdminListLine(state.Authorizations[0])
	if err != nil || !strings.Contains(pendingLine, " state=pending namespace=no transcript="+fingerprint+"\n") {
		t.Fatalf("pending transcript fingerprint unavailable to operator: %q, %v", pendingLine, err)
	}
	invalidPending := state.Authorizations[0]
	invalidPending.CurrentStateDigest = nil
	if _, err := diagnosticsAdminListLine(invalidPending); err == nil {
		t.Fatal("operator list accepted a pending authorization without an exact helper-accept digest")
	}

	transition := func(prior diagnosticsPairingMessage, messageType uint64) diagnosticsPairingMessage {
		now := uint64(clock.current().Unix())
		encoded, err := buildDiagnosticsBootstrapTransition(prior, messageType, appPrivate, now, now+120)
		if err != nil {
			t.Fatalf("build type %d: %v", messageType, err)
		}
		response, err := manager.handleBootstrapTransition(encoded)
		if err != nil {
			t.Fatalf("handle type %d: %v", messageType, err)
		}
		replay, err := manager.handleBootstrapTransition(encoded)
		if err != nil || !bytes.Equal(replay, response) {
			t.Fatalf("type %d replay changed bytes: %v", messageType, err)
		}
		decoded, err := decodeDiagnosticsPairingMessage(response)
		if err != nil {
			t.Fatalf("decode response to type %d: %v", messageType, err)
		}
		return decoded
	}

	finalizeAck := transition(helperAccept, diagnosticsPairingFinalize)
	readyAck := transition(finalizeAck, diagnosticsPairingReceipt)
	activeAck := transition(readyAck, diagnosticsPairingActivate)
	_ = transition(activeAck, diagnosticsPairingAbort)
	state, err = manager.store.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Authorizations) != 1 || state.Authorizations[0].State != "revoked" || len(state.Revocations) != 1 {
		t.Fatalf("active abort did not revoke exactly one installation: %+v", state.Authorizations)
	}
}

func TestDiagnosticsPairingCrashExpiryRateAndMultiInstallation(t *testing.T) {
	manager, clock := newDiagnosticsTestPairingManager(t)
	folder := bytes.Repeat([]byte{0x71}, 32)
	qr, err := manager.beginInvitation(folder, "10.0.0.2", 8443)
	if err != nil {
		t.Fatal(err)
	}
	invitation, _ := decodeDiagnosticsPairingQR(qr)
	_, appPrivate, _ := ed25519.GenerateKey(rand.Reader)
	request, err := buildDiagnosticsAppPairingRequest(invitation, appPrivate, bytes.Repeat([]byte{0x72}, 32))
	if err != nil {
		t.Fatal(err)
	}
	accept, err := manager.acceptAppRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := newDiagnosticsPairingManager(manager.store, rand.Reader, clock.current)
	if err != nil {
		t.Fatal(err)
	}
	acceptMessage, _ := decodeDiagnosticsPairingMessage(accept)
	now := uint64(clock.current().Unix())
	finalize, err := buildDiagnosticsBootstrapTransition(acceptMessage, diagnosticsPairingFinalize, appPrivate, now, now+120)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.handleBootstrapTransition(finalize); err != nil {
		t.Fatalf("durable pending state did not survive restart: %v", err)
	}

	multiManager, multiClock := newDiagnosticsTestPairingManager(t)
	for installation := 0; installation < 8; installation++ {
		activateDiagnosticsTestInstallation(t, multiManager, multiClock, folder, byte(installation+1))
		multiClock.advance(10 * time.Second)
	}
	qr, err = multiManager.beginInvitation(folder, "helper.test", 443)
	if err != nil {
		t.Fatal(err)
	}
	ninthInvitation, _ := decodeDiagnosticsPairingQR(qr)
	_, ninthPrivate, _ := ed25519.GenerateKey(rand.Reader)
	ninth, _ := buildDiagnosticsAppPairingRequest(ninthInvitation, ninthPrivate, bytes.Repeat([]byte{0x79}, 32))
	if _, err := multiManager.acceptAppRequest(ninth); !errors.Is(err, errDiagnosticsPairingUnavailable) {
		t.Fatalf("ninth installation accepted: %v", err)
	}

	expiring, expiryClock := newDiagnosticsTestPairingManager(t)
	qr, err = expiring.beginInvitation(bytes.Repeat([]byte{0x55}, 32), "helper.test", 443)
	if err != nil {
		t.Fatal(err)
	}
	expiredInvitation, _ := decodeDiagnosticsPairingQR(qr)
	_, expiredPrivate, _ := ed25519.GenerateKey(rand.Reader)
	expiredRequest, _ := buildDiagnosticsAppPairingRequest(expiredInvitation, expiredPrivate, bytes.Repeat([]byte{0x56}, 32))
	expiryClock.advance(5 * time.Minute)
	if _, err := expiring.acceptAppRequest(expiredRequest); !errors.Is(err, errDiagnosticsPairingExpired) && !errors.Is(err, errDiagnosticsPairingUnavailable) {
		t.Fatalf("expired invitation accepted: %v", err)
	}

	rateManager, _ := newDiagnosticsTestPairingManager(t)
	for attempt := 0; attempt < diagnosticsPairingRequestLimitPerMinute; attempt++ {
		_, _ = rateManager.acceptAppRequest([]byte{0xa0})
	}
	if _, err := rateManager.acceptAppRequest([]byte{0xa0}); !errors.Is(err, errDiagnosticsPairingRateLimited) {
		t.Fatalf("helper-wide rate limit did not engage: %v", err)
	}
}

func TestDiagnosticsPairingPendingLimitsExpiryAndFirstRequestRace(t *testing.T) {
	manager, clock := newDiagnosticsTestPairingManager(t)
	folder := bytes.Repeat([]byte{0x7a}, 32)
	qr, err := manager.beginInvitation(folder, "helper.test", 443)
	if err != nil {
		t.Fatal(err)
	}
	invitation, _ := decodeDiagnosticsPairingQR(qr)
	_, appPrivate, _ := ed25519.GenerateKey(rand.Reader)
	request, err := buildDiagnosticsAppPairingRequest(invitation, appPrivate, bytes.Repeat([]byte{0x7b}, 32))
	if err != nil {
		t.Fatal(err)
	}

	responses := make(chan []byte, 8)
	errorsChannel := make(chan error, 8)
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			response, err := manager.acceptAppRequest(request)
			responses <- response
			errorsChannel <- err
		}()
	}
	group.Wait()
	close(responses)
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("first-request race failed: %v", err)
		}
	}
	var expected []byte
	for response := range responses {
		if expected == nil {
			expected = response
		}
		if !bytes.Equal(expected, response) {
			t.Fatal("first-request race produced distinct acceptances")
		}
	}
	state, err := manager.store.snapshot()
	if err != nil || len(state.Authorizations) != 1 {
		t.Fatalf("first-request race persisted %d authorizations: %v", len(state.Authorizations), err)
	}
	if _, err := manager.beginInvitation(folder, "helper.test", 443); !errors.Is(err, errDiagnosticsPairingUnavailable) {
		t.Fatalf("second pending pairing for the same folder started: %v", err)
	}
	for index := byte(0); index < 3; index++ {
		if _, err := manager.beginInvitation(bytes.Repeat([]byte{0x80 + index}, 32), "helper.test", 443); err != nil {
			t.Fatalf("pending invitation %d: %v", index+2, err)
		}
	}
	if _, err := manager.beginInvitation(bytes.Repeat([]byte{0x89}, 32), "helper.test", 443); !errors.Is(err, errDiagnosticsPairingUnavailable) {
		t.Fatalf("fifth helper-wide pending pairing started: %v", err)
	}

	clock.advance(diagnosticsPairingLifetime + time.Second)
	if _, err := manager.beginInvitation(folder, "helper.test", 443); err != nil {
		t.Fatalf("expired pending state did not release its folder: %v", err)
	}
	state, err = manager.store.snapshot()
	if err != nil || state.Authorizations[0].State != "inactive" {
		t.Fatalf("expired pending authorization was not tombstoned: state=%q err=%v", state.Authorizations[0].State, err)
	}
}

func TestDiagnosticsPairingCrossManagerPendingRacePersistsOneAuthorization(t *testing.T) {
	clock := &diagnosticsTestClock{now: time.Unix(1_700_000_000, 0)}
	store, err := openDiagnosticsCredentialStore(filepath.Join(t.TempDir(), "state"), bytes.Repeat([]byte{0x7c}, 32), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	managerA, err := newDiagnosticsPairingManager(store, rand.Reader, clock.current)
	if err != nil {
		t.Fatal(err)
	}
	managerB, err := newDiagnosticsPairingManager(store, rand.Reader, clock.current)
	if err != nil {
		t.Fatal(err)
	}
	folder := bytes.Repeat([]byte{0x7d}, 32)
	qrA, err := managerA.beginInvitation(folder, "helper.test", 443)
	if err != nil {
		t.Fatal(err)
	}
	qrB, err := managerB.beginInvitation(folder, "helper.test", 443)
	if err != nil {
		t.Fatal(err)
	}
	invitationA, _ := decodeDiagnosticsPairingQR(qrA)
	invitationB, _ := decodeDiagnosticsPairingQR(qrB)
	_, privateA, _ := ed25519.GenerateKey(rand.Reader)
	_, privateB, _ := ed25519.GenerateKey(rand.Reader)
	requestA, _ := buildDiagnosticsAppPairingRequest(invitationA, privateA, bytes.Repeat([]byte{0x7e}, 32))
	requestB, _ := buildDiagnosticsAppPairingRequest(invitationB, privateB, bytes.Repeat([]byte{0x7f}, 32))
	if _, err := managerA.acceptAppRequest(requestA); err != nil {
		t.Fatal(err)
	}
	if _, err := managerB.acceptAppRequest(requestB); !errors.Is(err, errDiagnosticsPairingUnavailable) {
		t.Fatalf("cross-manager second pending authorization accepted: %v", err)
	}
	state, err := store.snapshot()
	if err != nil || len(state.Authorizations) != 1 {
		t.Fatalf("cross-manager race persisted %d authorizations: %v", len(state.Authorizations), err)
	}
}

func TestDiagnosticsPairingSecretAndAppPrivateKeyNeverPersist(t *testing.T) {
	manager, _ := newDiagnosticsTestPairingManager(t)
	qr, err := manager.beginInvitation(bytes.Repeat([]byte{0x81}, 32), "helper.test", 443)
	if err != nil {
		t.Fatal(err)
	}
	invitation, _ := decodeDiagnosticsPairingQR(qr)
	secret, _ := invitation.bytesField(17, 32)
	seed := bytes.Repeat([]byte{0x82}, ed25519.SeedSize)
	appPrivate := ed25519.NewKeyFromSeed(seed)
	request, err := buildDiagnosticsAppPairingRequest(invitation, appPrivate, bytes.Repeat([]byte{0x83}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.acceptAppRequest(request); err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(manager.store.statePath)
	if err != nil {
		t.Fatal(err)
	}
	for name, forbidden := range map[string][]byte{
		"bootstrap secret": secret,
		"app private seed": seed,
		"raw app request":  request,
	} {
		if bytes.Contains(persisted, forbidden) || bytes.Contains(persisted, []byte(base64.StdEncoding.EncodeToString(forbidden))) || bytes.Contains(persisted, []byte(hex.EncodeToString(forbidden))) {
			t.Fatalf("%s entered credential persistence", name)
		}
	}
}

func TestDiagnosticsPinnedTLSAndEndpointPolicyAreExplicitAndFailClosed(t *testing.T) {
	privatePKCS8, spki, pin, err := newDiagnosticsTLSIdentity(rand.Reader)
	if err != nil || len(privatePKCS8) == 0 {
		t.Fatal(err)
	}
	config, err := diagnosticsPinnedTLSConfig(pin)
	if err != nil {
		t.Fatal(err)
	}
	if config.MinVersion != tls.VersionTLS13 || config.VerifyConnection == nil {
		t.Fatal("TLS policy is not pinned TLS 1.3")
	}
	certificate := &x509.Certificate{RawSubjectPublicKeyInfo: spki}
	if err := config.VerifyConnection(tls.ConnectionState{Version: tls.VersionTLS13, PeerCertificates: []*x509.Certificate{certificate}}); err != nil {
		t.Fatalf("correct pin rejected: %v", err)
	}
	if err := config.VerifyConnection(tls.ConnectionState{Version: tls.VersionTLS13, PeerCertificates: []*x509.Certificate{certificate, {}}}); err != nil {
		t.Fatalf("correct leaf pin with a chain rejected: %v", err)
	}
	wrong := *certificate
	wrong.RawSubjectPublicKeyInfo = append([]byte(nil), spki...)
	wrong.RawSubjectPublicKeyInfo[len(wrong.RawSubjectPublicKeyInfo)-1] ^= 1
	if err := config.VerifyConnection(tls.ConnectionState{Version: tls.VersionTLS13, PeerCertificates: []*x509.Certificate{&wrong}}); !errors.Is(err, errDiagnosticsTLSPinMismatch) {
		t.Fatalf("wrong pin accepted: %v", err)
	}
	for _, host := range []string{
		"HELPER.test", "helper.test/path", "user@helper.test", "helper.test.", "fe80::1%en0", "",
		"010.0.0.1", "999.999.999.999", "123",
	} {
		if validDiagnosticsEndpointHost(host) {
			t.Fatalf("unsafe endpoint host accepted: %q", host)
		}
	}
	for _, host := range []string{"helper.test", "10.0.0.2", "2001:db8::1"} {
		if !validDiagnosticsEndpointHost(host) {
			t.Fatalf("explicit canonical endpoint rejected: %q", host)
		}
	}
	if _, err := decodeDiagnosticsPairingQR(strings.Repeat("a", base64.RawURLEncoding.EncodedLen(diagnosticsMaximumMessageBytes)+1)); !errors.Is(err, errDiagnosticsPairingInvalid) {
		t.Fatalf("oversized QR accepted: %v", err)
	}
}

func TestDiagnosticsAppKeyRotationAbortFinalizeRevocationAndReplay(t *testing.T) {
	manager, clock := newDiagnosticsTestPairingManager(t)
	recordID, currentAppPrivate := activateDiagnosticsTestInstallation(t, manager, clock, bytes.Repeat([]byte{0x91}, 32), 0x91)
	state, err := manager.store.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	authorization := state.Authorizations[diagnosticsAuthorizationIndex(state.Authorizations, recordID)]
	_, proposedPrivate, _ := ed25519.GenerateKey(rand.Reader)
	proposedPublic := proposedPrivate.Public().(ed25519.PublicKey)
	now := uint64(clock.current().Unix())
	requestBytes, err := buildDiagnosticsAppKeyRotationRequest(authorization, state.Identity, proposedPublic, currentAppPrivate, now, now+300, bytes.Repeat([]byte{0x92}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if response, err := manager.handleLifecycleMessage(requestBytes); err != nil || response != nil {
		t.Fatalf("stage app rotation = %x, %v", response, err)
	}
	if response, err := manager.handleLifecycleMessage(requestBytes); err != nil || response != nil {
		t.Fatalf("stage app rotation replay = %x, %v", response, err)
	}
	request, _ := decodeDiagnosticsPairingMessage(requestBytes)
	proofBytes, err := buildDiagnosticsLifecycleContinuation(request, diagnosticsPairingAppKeyRotationNewProof, 0, nil, proposedPrivate, now, now+300, bytes.Repeat([]byte{0x93}, 32))
	if err != nil {
		t.Fatal(err)
	}
	acceptBytes, err := manager.handleLifecycleMessage(proofBytes)
	if err != nil {
		t.Fatal(err)
	}
	replayedAccept, err := manager.handleLifecycleMessage(proofBytes)
	if err != nil || !bytes.Equal(replayedAccept, acceptBytes) {
		t.Fatalf("rotation acceptance replay changed: %v", err)
	}
	accept, _ := decodeDiagnosticsPairingMessage(acceptBytes)
	transitionDigest, _ := request.digest()
	finalizeBytes, err := buildDiagnosticsLifecycleContinuation(accept, diagnosticsPairingLifecycleFinalize, diagnosticsPairingTransitionAppKey, transitionDigest[:], proposedPrivate, now, now+300, bytes.Repeat([]byte{0x94}, 32))
	if err != nil {
		t.Fatal(err)
	}
	ackBytes, err := manager.handleLifecycleMessage(finalizeBytes)
	if err != nil {
		t.Fatal(err)
	}
	if ack, err := decodeDiagnosticsPairingMessage(ackBytes); err != nil || ack.messageType != diagnosticsPairingLifecycleActiveAck {
		t.Fatalf("invalid lifecycle ack: type=%d err=%v", ack.messageType, err)
	}
	if err := manager.confirmLifecycleTransition(recordID, transitionDigest[:]); err != nil {
		t.Fatal(err)
	}
	state, err = manager.store.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	proposedKeyID := diagnosticsKeyID(proposedPublic)
	newRecordID := diagnosticsAuthorizationRecordID(proposedKeyID[:], authorization.FolderBinding)
	index := diagnosticsAuthorizationIndex(state.Authorizations, newRecordID)
	if index < 0 || state.Authorizations[index].AppEpoch != authorization.AppEpoch+1 || state.Authorizations[index].Transition != nil {
		t.Fatalf("app rotation did not activate exact proposed epoch: %+v", state.Authorizations)
	}

	rotatedAuthorization := state.Authorizations[index]
	_, nextPrivate, _ := ed25519.GenerateKey(rand.Reader)
	reusedNonceRequest, err := buildDiagnosticsAppKeyRotationRequest(rotatedAuthorization, state.Identity, nextPrivate.Public().(ed25519.PublicKey), proposedPrivate, now, now+300, bytes.Repeat([]byte{0x92}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.handleLifecycleMessage(reusedNonceRequest); !errors.Is(err, errDiagnosticsPairingInvalid) {
		t.Fatalf("reused lifecycle nonce accepted: %v", err)
	}
	revocationBytes, err := buildDiagnosticsRevocationRequest(rotatedAuthorization, state.Identity, diagnosticsPairingRevocationSuspectedCompromise, proposedPrivate, now, now+300, bytes.Repeat([]byte{0x95}, 32))
	if err != nil {
		t.Fatal(err)
	}
	revocationAck, err := manager.handleLifecycleMessage(revocationBytes)
	if err != nil {
		t.Fatal(err)
	}
	if message, err := decodeDiagnosticsPairingMessage(revocationAck); err != nil || message.messageType != diagnosticsPairingRevocationRecord {
		t.Fatalf("invalid revocation record: type=%d err=%v", message.messageType, err)
	}
	state, _ = manager.store.snapshot()
	if state.Authorizations[index].State != "revoked" || len(state.Revocations) != 1 {
		t.Fatal("signed revocation did not fail closed")
	}

	abortManager, abortClock := newDiagnosticsTestPairingManager(t)
	abortRecordID, abortPrivate := activateDiagnosticsTestInstallation(t, abortManager, abortClock, bytes.Repeat([]byte{0x96}, 32), 0x96)
	abortState, _ := abortManager.store.snapshot()
	abortAuthorization := abortState.Authorizations[diagnosticsAuthorizationIndex(abortState.Authorizations, abortRecordID)]
	_, abortProposed, _ := ed25519.GenerateKey(rand.Reader)
	abortNow := uint64(abortClock.current().Unix())
	abortRequestBytes, _ := buildDiagnosticsAppKeyRotationRequest(abortAuthorization, abortState.Identity, abortProposed.Public().(ed25519.PublicKey), abortPrivate, abortNow, abortNow+300, bytes.Repeat([]byte{0x97}, 32))
	if _, err := abortManager.handleLifecycleMessage(abortRequestBytes); err != nil {
		t.Fatal(err)
	}
	abortRequest, _ := decodeDiagnosticsPairingMessage(abortRequestBytes)
	abortDigest, _ := abortRequest.digest()
	abortBytes, err := buildDiagnosticsLifecycleContinuation(abortRequest, diagnosticsPairingLifecycleAbort, diagnosticsPairingTransitionAppKey, abortDigest[:], abortPrivate, abortNow, abortNow+300, bytes.Repeat([]byte{0x98}, 32))
	if err != nil {
		t.Fatal(err)
	}
	abortAck, err := abortManager.handleLifecycleMessage(abortBytes)
	if err != nil {
		t.Fatal(err)
	}
	if message, err := decodeDiagnosticsPairingMessage(abortAck); err != nil || message.messageType != diagnosticsPairingLifecycleAbortAck {
		t.Fatalf("invalid lifecycle abort ack: type=%d err=%v", message.messageType, err)
	}
	abortState, _ = abortManager.store.snapshot()
	if abortState.Authorizations[diagnosticsAuthorizationIndex(abortState.Authorizations, abortRecordID)].Transition != nil {
		t.Fatal("pre-commit abort left pending transition state")
	}
}

func TestDiagnosticsConfirmedLifecycleReconcilesForwardAfterSignedExpiry(t *testing.T) {
	manager, clock := newDiagnosticsTestPairingManager(t)
	recordID, currentAppPrivate := activateDiagnosticsTestInstallation(
		t, manager, clock, bytes.Repeat([]byte{0xa1}, 32), 0xa1,
	)
	state, _ := manager.store.snapshot()
	authorization := state.Authorizations[diagnosticsAuthorizationIndex(state.Authorizations, recordID)]
	_, proposedPrivate, _ := ed25519.GenerateKey(rand.Reader)
	now := uint64(clock.current().Unix())
	requestBytes, err := buildDiagnosticsAppKeyRotationRequest(
		authorization, state.Identity, proposedPrivate.Public().(ed25519.PublicKey), currentAppPrivate,
		now, now+300, bytes.Repeat([]byte{0xa2}, 32),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.handleLifecycleMessage(requestBytes); err != nil {
		t.Fatal(err)
	}
	request, _ := decodeDiagnosticsPairingMessage(requestBytes)
	proof, err := buildDiagnosticsLifecycleContinuation(
		request, diagnosticsPairingAppKeyRotationNewProof, 0, nil, proposedPrivate,
		now, now+300, bytes.Repeat([]byte{0xa3}, 32),
	)
	if err != nil {
		t.Fatal(err)
	}
	acceptBytes, err := manager.handleLifecycleMessage(proof)
	if err != nil {
		t.Fatal(err)
	}
	accept, _ := decodeDiagnosticsPairingMessage(acceptBytes)
	transitionDigest, _ := request.digest()
	finalize, err := buildDiagnosticsLifecycleContinuation(
		accept, diagnosticsPairingLifecycleFinalize, diagnosticsPairingTransitionAppKey, transitionDigest[:], proposedPrivate,
		now, now+300, bytes.Repeat([]byte{0xa4}, 32),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.handleLifecycleMessage(finalize); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("simulated post-confirmation preparation outage")
	if committed, err := manager.observeLifecycleCapability(
		recordID, transitionDigest[:], func(diagnosticsLifecycleCommitPlan) error { return injected },
	); committed || !errors.Is(err, injected) {
		t.Fatalf("injected post-confirmation result = %v, %v", committed, err)
	}
	confirmed, _ := manager.store.snapshot()
	transition := confirmed.Authorizations[diagnosticsAuthorizationIndex(confirmed.Authorizations, recordID)].Transition
	if transition == nil || !transition.ProposedStateConfirmed {
		t.Fatal("valid proposed-state capability confirmation was not persisted")
	}
	clock.advance(10 * time.Minute)
	if err := manager.reconcileConfirmedLifecycle(nil); err != nil {
		t.Fatalf("forward reconciliation after signed expiry: %v", err)
	}
	committed, _ := manager.store.snapshot()
	proposedKeyID := diagnosticsKeyID(proposedPrivate.Public().(ed25519.PublicKey))
	newRecordID := diagnosticsAuthorizationRecordID(proposedKeyID[:], authorization.FolderBinding)
	index := diagnosticsAuthorizationIndex(committed.Authorizations, newRecordID)
	if index < 0 || committed.Authorizations[index].Transition != nil || committed.Authorizations[index].AppEpoch != authorization.AppEpoch+1 {
		t.Fatal("confirmed transition did not reconcile forward exactly once")
	}
}

func TestDiagnosticsLifecyclePreparationDoesNotLockOrCommitStaleState(t *testing.T) {
	manager, clock := newDiagnosticsTestPairingManager(t)
	recordID, currentAppPrivate := activateDiagnosticsTestInstallation(
		t, manager, clock, bytes.Repeat([]byte{0xb1}, 32), 0xb1,
	)
	state, _ := manager.store.snapshot()
	authorization := state.Authorizations[diagnosticsAuthorizationIndex(state.Authorizations, recordID)]
	_, proposedPrivate, _ := ed25519.GenerateKey(rand.Reader)
	now := uint64(clock.current().Unix())
	requestBytes, err := buildDiagnosticsAppKeyRotationRequest(
		authorization, state.Identity, proposedPrivate.Public().(ed25519.PublicKey), currentAppPrivate,
		now, now+300, bytes.Repeat([]byte{0xb2}, 32),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.handleLifecycleMessage(requestBytes); err != nil {
		t.Fatal(err)
	}
	request, _ := decodeDiagnosticsPairingMessage(requestBytes)
	proof, err := buildDiagnosticsLifecycleContinuation(
		request, diagnosticsPairingAppKeyRotationNewProof, 0, nil, proposedPrivate,
		now, now+300, bytes.Repeat([]byte{0xb3}, 32),
	)
	if err != nil {
		t.Fatal(err)
	}
	acceptBytes, err := manager.handleLifecycleMessage(proof)
	if err != nil {
		t.Fatal(err)
	}
	accept, _ := decodeDiagnosticsPairingMessage(acceptBytes)
	transitionDigest, _ := request.digest()
	finalize, err := buildDiagnosticsLifecycleContinuation(
		accept, diagnosticsPairingLifecycleFinalize, diagnosticsPairingTransitionAppKey, transitionDigest[:], proposedPrivate,
		now, now+300, bytes.Repeat([]byte{0xb4}, 32),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.handleLifecycleMessage(finalize); err != nil {
		t.Fatal(err)
	}

	prepareStarted := make(chan struct{})
	releasePrepare := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releasePrepare) })
	type observationResult struct {
		committed bool
		err       error
	}
	observed := make(chan observationResult, 1)
	go func() {
		committed, observeErr := manager.observeLifecycleCapability(
			recordID,
			transitionDigest[:],
			func(diagnosticsLifecycleCommitPlan) error {
				close(prepareStarted)
				<-releasePrepare
				return nil
			},
		)
		observed <- observationResult{committed: committed, err: observeErr}
	}()
	select {
	case <-prepareStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("lifecycle preparation did not start")
	}

	revoked := make(chan error, 1)
	go func() {
		_, revokeErr := manager.revokeLocally(recordID, diagnosticsPairingRevocationLostApp)
		revoked <- revokeErr
	}()
	select {
	case revokeErr := <-revoked:
		if revokeErr != nil {
			t.Fatalf("concurrent local revocation: %v", revokeErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("slow lifecycle preparation retained the pairing manager lock")
	}
	releaseOnce.Do(func() { close(releasePrepare) })
	result := <-observed
	if result.committed || !errors.Is(result.err, errDiagnosticsPairingUnavailable) {
		t.Fatalf("stale lifecycle plan result = %v, %v", result.committed, result.err)
	}
	finalState, _ := manager.store.snapshot()
	finalAuthorization := finalState.Authorizations[diagnosticsAuthorizationIndex(finalState.Authorizations, recordID)]
	proposedKeyID := diagnosticsKeyID(proposedPrivate.Public().(ed25519.PublicKey))
	if finalAuthorization.State != "revoked" || finalAuthorization.Transition != nil ||
		bytes.Equal(finalAuthorization.AppKeyID, proposedKeyID[:]) {
		t.Fatal("stale prepared app key replaced the concurrent terminal state")
	}
}

func TestDiagnosticsLocalRevocationProducesSignedOriginTwoRecord(t *testing.T) {
	manager, clock := newDiagnosticsTestPairingManager(t)
	recordID, appPrivate := activateDiagnosticsTestInstallation(t, manager, clock, bytes.Repeat([]byte{0x99}, 32), 0x99)
	stateBefore, _ := manager.store.snapshot()
	authorizationBefore := stateBefore.Authorizations[diagnosticsAuthorizationIndex(stateBefore.Authorizations, recordID)]
	_, proposedPrivate, _ := ed25519.GenerateKey(rand.Reader)
	now := uint64(clock.current().Unix())
	rotation, err := buildDiagnosticsAppKeyRotationRequest(authorizationBefore, stateBefore.Identity, proposedPrivate.Public().(ed25519.PublicKey), appPrivate, now, now+300, bytes.Repeat([]byte{0x9a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.handleLifecycleMessage(rotation); err != nil {
		t.Fatal(err)
	}
	record, err := manager.revokeLocally(recordID, diagnosticsPairingRevocationLostApp)
	if err != nil {
		t.Fatal(err)
	}
	message, err := decodeDiagnosticsPairingMessage(record)
	if err != nil || message.messageType != diagnosticsPairingRevocationRecord {
		t.Fatalf("local revocation record invalid: type=%d err=%v", message.messageType, err)
	}
	origin, _ := message.uintField(27)
	if origin != diagnosticsPairingRevocationLocalHelperAdmin {
		t.Fatalf("local revocation origin = %d", origin)
	}
	if _, hasPrior := diagnosticsCBORLookup(message.value, 24); hasPrior {
		t.Fatal("local revocation unexpectedly has a prior-message digest")
	}
	digest, _ := message.digest()
	state, err := manager.store.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	authorization := state.Authorizations[diagnosticsAuthorizationIndex(state.Authorizations, recordID)]
	if authorization.State != "revoked" || !bytes.Equal(authorization.CurrentStateDigest, digest[:]) ||
		len(state.Revocations) != 1 || state.Revocations[0].AuthorizationEpoch != authorization.AppEpoch {
		t.Fatal("local revocation did not persist the signed terminal state")
	}
}

func TestDiagnosticsLifecycleExpiryDropsPrecommitButNeverConfirmsLate(t *testing.T) {
	manager, clock := newDiagnosticsTestPairingManager(t)
	recordID, appPrivate := activateDiagnosticsTestInstallation(t, manager, clock, bytes.Repeat([]byte{0x9b}, 32), 0x9b)
	state, _ := manager.store.snapshot()
	authorization := state.Authorizations[diagnosticsAuthorizationIndex(state.Authorizations, recordID)]
	_, proposedPrivate, _ := ed25519.GenerateKey(rand.Reader)
	now := uint64(clock.current().Unix())
	request, err := buildDiagnosticsAppKeyRotationRequest(authorization, state.Identity, proposedPrivate.Public().(ed25519.PublicKey), appPrivate, now, now+300, bytes.Repeat([]byte{0x9c}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.handleLifecycleMessage(request); err != nil {
		t.Fatal(err)
	}
	clock.advance(diagnosticsPairingLifetime + time.Second)
	if _, err := manager.handleLifecycleMessage(request); err == nil {
		t.Fatal("expired lifecycle request replayed as active")
	}
	state, err = manager.store.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	authorization = state.Authorizations[diagnosticsAuthorizationIndex(state.Authorizations, recordID)]
	if authorization.State != "active" || authorization.Transition != nil || authorization.AppEpoch != 1 {
		t.Fatal("precommit expiry changed active credentials")
	}
}

func TestDiagnosticsHelperAndTLSPinRotationRequireFinalizeAndConfirmation(t *testing.T) {
	manager, clock := newDiagnosticsTestPairingManager(t)
	recordID, appPrivate := activateDiagnosticsTestInstallation(t, manager, clock, bytes.Repeat([]byte{0xa1}, 32), 0xa1)
	stateBefore, _ := manager.store.snapshot()
	oldHelperKeyID := diagnosticsKeyID(ed25519.NewKeyFromSeed(stateBefore.Identity.SigningSeed).Public().(ed25519.PublicKey))
	oldPin, _ := diagnosticsTLSPrivateKeyPin(stateBefore.Identity.TLSPrivatePKCS8)

	proposalBytes, proofBytes, err := manager.beginHelperKeyRotation(recordID)
	if err != nil {
		t.Fatal(err)
	}
	proposal, _ := decodeDiagnosticsPairingMessage(proposalBytes)
	proof, _ := decodeDiagnosticsPairingMessage(proofBytes)
	proposalDigest, _ := proposal.digest()
	now := uint64(clock.current().Unix())
	confirmBytes, err := buildDiagnosticsLifecycleContinuation(proof, diagnosticsPairingHelperKeyRotationConfirm, 0, nil, appPrivate, now, now+300, bytes.Repeat([]byte{0xa2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if response, err := manager.handleLifecycleMessage(confirmBytes); err != nil || response != nil {
		t.Fatalf("helper-key confirmation = %x, %v", response, err)
	}
	if response, err := manager.handleLifecycleMessage(confirmBytes); err != nil || response != nil {
		t.Fatalf("helper-key confirmation replay = %x, %v", response, err)
	}
	confirm, _ := decodeDiagnosticsPairingMessage(confirmBytes)
	finalizeBytes, err := buildDiagnosticsLifecycleContinuation(confirm, diagnosticsPairingLifecycleFinalize, diagnosticsPairingTransitionHelperKey, proposalDigest[:], appPrivate, now, now+300, bytes.Repeat([]byte{0xa3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.handleLifecycleMessage(finalizeBytes); err != nil {
		t.Fatal(err)
	}
	statePending, _ := manager.store.snapshot()
	if bytes.Equal(statePending.Identity.SigningSeed, statePending.Authorizations[0].Transition.ProposedHelperSeed) {
		t.Fatal("helper key switched before proposed-state confirmation")
	}
	if err := manager.confirmLifecycleTransition(recordID, proposalDigest[:]); err != nil {
		t.Fatal(err)
	}
	stateAfterHelper, _ := manager.store.snapshot()
	newHelperKeyID := diagnosticsKeyID(ed25519.NewKeyFromSeed(stateAfterHelper.Identity.SigningSeed).Public().(ed25519.PublicKey))
	if newHelperKeyID == oldHelperKeyID || stateAfterHelper.Identity.HelperEpoch != stateBefore.Identity.HelperEpoch+1 || stateAfterHelper.Authorizations[0].Transition != nil {
		t.Fatal("helper key rotation did not commit exact proposed state")
	}

	tlsProposalBytes, err := manager.beginTLSPinRotation(recordID)
	if err != nil {
		t.Fatal(err)
	}
	tlsProposal, _ := decodeDiagnosticsPairingMessage(tlsProposalBytes)
	tlsProposalDigest, _ := tlsProposal.digest()
	tlsConfirmBytes, err := buildDiagnosticsLifecycleContinuation(tlsProposal, diagnosticsPairingTLSPinRotationConfirm, 0, nil, appPrivate, now, now+300, bytes.Repeat([]byte{0xa4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.handleLifecycleMessage(tlsConfirmBytes); err != nil {
		t.Fatal(err)
	}
	if response, err := manager.handleLifecycleMessage(tlsConfirmBytes); err != nil || response != nil {
		t.Fatalf("TLS-pin confirmation replay = %x, %v", response, err)
	}
	tlsConfirm, _ := decodeDiagnosticsPairingMessage(tlsConfirmBytes)
	tlsFinalizeBytes, err := buildDiagnosticsLifecycleContinuation(tlsConfirm, diagnosticsPairingLifecycleFinalize, diagnosticsPairingTransitionTLSPin, tlsProposalDigest[:], appPrivate, now, now+300, bytes.Repeat([]byte{0xa5}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.handleLifecycleMessage(tlsFinalizeBytes); err != nil {
		t.Fatal(err)
	}
	if err := manager.confirmLifecycleTransition(recordID, tlsProposalDigest[:]); err != nil {
		t.Fatal(err)
	}
	stateAfterTLS, _ := manager.store.snapshot()
	newPin, _ := diagnosticsTLSPrivateKeyPin(stateAfterTLS.Identity.TLSPrivatePKCS8)
	if bytes.Equal(oldPin, newPin) || !bytes.Equal(newPin, stateAfterTLS.Authorizations[0].TLSSPKIPin) || stateAfterTLS.Authorizations[0].Transition != nil {
		t.Fatal("TLS pin rotation did not commit exact proposed state")
	}
}

func TestDiagnosticsHelperRotationStagesEveryInstallationBeforeGlobalCommit(t *testing.T) {
	manager, clock := newDiagnosticsTestPairingManager(t)
	recordA, appA := activateDiagnosticsTestInstallation(t, manager, clock, bytes.Repeat([]byte{0xaa}, 32), 0xaa)
	recordB, appB := activateDiagnosticsTestInstallation(t, manager, clock, bytes.Repeat([]byte{0xaa}, 32), 0xab)

	stage := func(recordID string, appPrivate ed25519.PrivateKey, nonce byte) []byte {
		proposalBytes, proofBytes, err := manager.beginHelperKeyRotation(recordID)
		if err != nil {
			t.Fatal(err)
		}
		proposal, _ := decodeDiagnosticsPairingMessage(proposalBytes)
		proposalDigest, _ := proposal.digest()
		proof, _ := decodeDiagnosticsPairingMessage(proofBytes)
		now := uint64(clock.current().Unix())
		confirmationBytes, err := buildDiagnosticsLifecycleContinuation(proof, diagnosticsPairingHelperKeyRotationConfirm, 0, nil, appPrivate, now, now+300, bytes.Repeat([]byte{nonce}, 32))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.handleLifecycleMessage(confirmationBytes); err != nil {
			t.Fatal(err)
		}
		confirmation, _ := decodeDiagnosticsPairingMessage(confirmationBytes)
		finalizeBytes, err := buildDiagnosticsLifecycleContinuation(confirmation, diagnosticsPairingLifecycleFinalize, diagnosticsPairingTransitionHelperKey, proposalDigest[:], appPrivate, now, now+300, bytes.Repeat([]byte{nonce + 1}, 32))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.handleLifecycleMessage(finalizeBytes); err != nil {
			t.Fatal(err)
		}
		return append([]byte(nil), proposalDigest[:]...)
	}

	digestA := stage(recordA, appA, 0xac)
	if err := manager.confirmLifecycleTransition(recordA, digestA); !errors.Is(err, errDiagnosticsPairingUnavailable) {
		t.Fatalf("global helper key committed before installation B: %v", err)
	}
	digestB := stage(recordB, appB, 0xae)
	if err := manager.confirmLifecycleTransition(recordA, digestA); !errors.Is(err, errDiagnosticsPairingUnavailable) {
		t.Fatalf("global helper key committed before installation B capability confirmation: %v", err)
	}
	if err := manager.confirmLifecycleTransition(recordB, digestB); err != nil {
		t.Fatal(err)
	}
	state, err := manager.store.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Authorizations) != 2 {
		t.Fatalf("authorizations = %d, want 2", len(state.Authorizations))
	}
	for _, authorization := range state.Authorizations {
		if authorization.Transition != nil || authorization.HelperEpoch != state.Identity.HelperEpoch {
			t.Fatalf("installation did not enter global helper epoch: %+v", authorization)
		}
	}
	if bytes.Equal(digestA, digestB) {
		t.Fatal("per-installation transitions unexpectedly shared a message digest")
	}
}

func TestDiagnosticsWholeStateRecoveryAndDowngradePreserveIdentityWithoutTrustTransfer(t *testing.T) {
	manager, clock := newDiagnosticsTestPairingManager(t)
	recordID, _ := activateDiagnosticsTestInstallation(t, manager, clock, bytes.Repeat([]byte{0xb1}, 32), 0xb1)
	before, err := manager.store.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	persistedBefore, err := os.ReadFile(manager.store.statePath)
	if err != nil {
		t.Fatal(err)
	}

	// Opening the store and constructing the dormant manager model a helper
	// downgrade/re-upgrade. Neither operation rewrites or deletes credentials.
	reopened, err := openDiagnosticsCredentialStore(manager.store.directory, before.Identity.DeviceIDDigest, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newDiagnosticsPairingManager(reopened, rand.Reader, clock.current); err != nil {
		t.Fatal(err)
	}
	persistedAfter, err := os.ReadFile(manager.store.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(persistedBefore, persistedAfter) {
		t.Fatal("downgrade/re-upgrade inspection rewrote credential state")
	}

	backupRoot := filepath.Join(t.TempDir(), "restored-state")
	if err := os.Mkdir(backupRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backupRoot, diagnosticsCredentialStateFile), persistedBefore, 0o600); err != nil {
		t.Fatal(err)
	}
	restored, err := openDiagnosticsCredentialStore(backupRoot, before.Identity.DeviceIDDigest, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	restoredState, err := restored.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restoredState.Identity.SigningSeed, before.Identity.SigningSeed) ||
		!bytes.Equal(restoredState.Identity.HomeserverBinding, before.Identity.HomeserverBinding) ||
		diagnosticsAuthorizationIndex(restoredState.Authorizations, recordID) < 0 {
		t.Fatal("whole-state restore changed helper identity or authorization")
	}

	lostStore, err := openDiagnosticsCredentialStore(filepath.Join(t.TempDir(), "new-identity"), before.Identity.DeviceIDDigest, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	lostState, _ := lostStore.snapshot()
	if bytes.Equal(lostState.Identity.SigningSeed, before.Identity.SigningSeed) || bytes.Equal(lostState.Identity.HomeserverBinding, before.Identity.HomeserverBinding) || len(lostState.Authorizations) != 0 {
		t.Fatal("helper state loss silently transferred old trust")
	}
}

func FuzzDiagnosticsRuntimeCBOR(f *testing.F) {
	f.Add([]byte{0xa0})
	f.Add([]byte{0x00})
	f.Add(mustDecodeHex(f, loadDiagnosticsContractFixture(f).Vectors.BootstrapHMAC.CanonicalBodyHex))
	f.Fuzz(func(t *testing.T, input []byte) {
		value, err := decodeDiagnosticsCBOR(input)
		if err != nil {
			return
		}
		reencoded, err := encodeDiagnosticsCBOR(value)
		if err != nil {
			t.Fatalf("decoded value could not be re-encoded: %v", err)
		}
		if !bytes.Equal(reencoded, input) {
			t.Fatal("accepted CBOR was not deterministic")
		}
	})
}

func FuzzDiagnosticsPairingDecoder(f *testing.F) {
	for _, encoded := range diagnosticsPairingGoldenMessages(f) {
		f.Add(mustDecodeHex(f, encoded))
	}
	f.Fuzz(func(t *testing.T, input []byte) {
		message, err := decodeDiagnosticsPairingMessage(input)
		if err != nil {
			return
		}
		reencoded, err := encodeDiagnosticsCBOR(message.value)
		if err != nil {
			t.Fatalf("decoded message could not be re-encoded: %v", err)
		}
		if !bytes.Equal(reencoded, input) {
			t.Fatal("accepted pairing message was not deterministic")
		}
	})
}

type diagnosticsTestClock struct {
	mutex sync.Mutex
	now   time.Time
}

func (clock *diagnosticsTestClock) current() time.Time {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	return clock.now
}

func (clock *diagnosticsTestClock) advance(duration time.Duration) {
	clock.mutex.Lock()
	clock.now = clock.now.Add(duration)
	clock.mutex.Unlock()
}

func activateDiagnosticsTestInstallation(t *testing.T, manager *diagnosticsPairingManager, clock *diagnosticsTestClock, folderDigest []byte, nonceByte byte) (string, ed25519.PrivateKey) {
	t.Helper()
	qr, err := manager.beginInvitation(folderDigest, "helper.test", 443)
	if err != nil {
		t.Fatal(err)
	}
	invitation, err := decodeDiagnosticsPairingQR(qr)
	if err != nil {
		t.Fatal(err)
	}
	_, appPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	requestBytes, err := buildDiagnosticsAppPairingRequest(invitation, appPrivate, bytes.Repeat([]byte{nonceByte}, 32))
	if err != nil {
		t.Fatal(err)
	}
	acceptBytes, err := manager.acceptAppRequest(requestBytes)
	if err != nil {
		t.Fatal(err)
	}
	prior, err := decodeDiagnosticsPairingMessage(acceptBytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, messageType := range []uint64{diagnosticsPairingFinalize, diagnosticsPairingReceipt, diagnosticsPairingActivate} {
		now := uint64(clock.current().Unix())
		request, err := buildDiagnosticsBootstrapTransition(prior, messageType, appPrivate, now, now+120)
		if err != nil {
			t.Fatal(err)
		}
		response, err := manager.handleBootstrapTransition(request)
		if err != nil {
			t.Fatal(err)
		}
		prior, err = decodeDiagnosticsPairingMessage(response)
		if err != nil {
			t.Fatal(err)
		}
	}
	appKeyID := diagnosticsKeyID(appPrivate.Public().(ed25519.PublicKey))
	folderBinding, _ := invitation.bytesField(12, 32)
	return diagnosticsAuthorizationRecordID(appKeyID[:], folderBinding), appPrivate
}

func newDiagnosticsTestPairingManager(t *testing.T) (*diagnosticsPairingManager, *diagnosticsTestClock) {
	t.Helper()
	clock := &diagnosticsTestClock{now: time.Unix(1_700_000_000, 0)}
	store, err := openDiagnosticsCredentialStore(filepath.Join(t.TempDir(), "state"), bytes.Repeat([]byte{0x11}, 32), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := newDiagnosticsPairingManager(store, rand.Reader, clock.current)
	if err != nil {
		t.Fatal(err)
	}
	return manager, clock
}
