package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestDiagnosticsDecision022AllMessageBytesMatchCrossLanguageGolden(t *testing.T) {
	actual := diagnosticsPairingGoldenMessages(t)
	if os.Getenv("VAULTSYNC_PRINT_M3_PAIRING_FIXTURE") == "1" {
		encoded, err := json.MarshalIndent(actual, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		fmt.Println(string(encoded))
		return
	}
	path := filepath.Join(diagnosticsTestRepoRoot(t), "ios", "VaultSyncTests", "Fixtures", "diagnostics-pairing-m3.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]string
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("Decision 022 pairing bytes differ from the shared Go/Swift fixture\nactual=%#v\nexpected=%#v", actual, expected)
	}
	if len(actual) != int(diagnosticsPairingLifecycleAbortAck+1) {
		t.Fatalf("golden message count = %d, want %d", len(actual), diagnosticsPairingLifecycleAbortAck+1)
	}
}

func TestDiagnosticsDecision022DomainsAndSchemasAreExact(t *testing.T) {
	fixture := loadDiagnosticsContractFixture(t)
	expectedDomains := expectedDiagnosticsDomains()
	for messageType := diagnosticsPairingAppRequest; messageType <= diagnosticsPairingLifecycleAbortAck; messageType++ {
		name := diagnosticsPairingGoldenName(messageType)
		fixtureName := "pairing." + name[3:]
		if got, ok := diagnosticsPairingDomains[messageType]; !ok || got != expectedDomains[fixtureName] {
			t.Fatalf("message type %d domain = %q, want fixture %q", messageType, got, expectedDomains[fixtureName])
		}
	}
	if fixture.Registries["pairing_bootstrap"]["4"] != "message_type:uint-enum=0..10" ||
		fixture.Registries["pairing_lifecycle"]["4"] != "message_type:uint-enum=11..24" {
		t.Fatal("shared Decision 022 field registry changed")
	}
}

func diagnosticsPairingGoldenMessages(t testing.TB) map[string]string {
	t.Helper()
	currentApp := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x31}, ed25519.SeedSize))
	proposedApp := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x32}, ed25519.SeedSize))
	currentHelper := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x41}, ed25519.SeedSize))
	proposedHelper := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	currentAppPublic := currentApp.Public().(ed25519.PublicKey)
	currentAppKeyID := diagnosticsKeyID(currentAppPublic)
	currentHelperPublic := currentHelper.Public().(ed25519.PublicKey)
	currentHelperKeyID := diagnosticsKeyID(currentHelperPublic)
	issuedAt := uint64(1_700_000_000)
	expiresAt := issuedAt + 300

	qrValue := diagnosticsCBORMapValue(
		diagnosticsCBORMapField(1, diagnosticsCBORTextValue(diagnosticsPairingCapabilityID)),
		diagnosticsCBORMapField(2, diagnosticsCBORUint(1)), diagnosticsCBORMapField(3, diagnosticsCBORUint(1)),
		diagnosticsCBORMapField(4, diagnosticsCBORUint(diagnosticsPairingQR)),
		diagnosticsCBORMapField(5, diagnosticsCBORBstr(bytes.Repeat([]byte{0x05}, 32))),
		diagnosticsCBORMapField(6, diagnosticsCBORTextValue("helper.test")), diagnosticsCBORMapField(7, diagnosticsCBORUint(443)),
		diagnosticsCBORMapField(8, diagnosticsCBORBstr(bytes.Repeat([]byte{0x08}, 32))),
		diagnosticsCBORMapField(9, diagnosticsCBORBstr(currentHelperPublic)), diagnosticsCBORMapField(10, diagnosticsCBORBstr(currentHelperKeyID[:])),
		diagnosticsCBORMapField(11, diagnosticsCBORBstr(bytes.Repeat([]byte{0x11}, 32))),
		diagnosticsCBORMapField(12, diagnosticsCBORBstr(bytes.Repeat([]byte{0x12}, 32))),
		diagnosticsCBORMapField(13, diagnosticsCBORBstr(bytes.Repeat([]byte{0x13}, 32))),
		diagnosticsCBORMapField(14, diagnosticsCBORBstr(bytes.Repeat([]byte{0x14}, 32))),
		diagnosticsCBORMapField(15, diagnosticsCBORUint(issuedAt)), diagnosticsCBORMapField(16, diagnosticsCBORUint(expiresAt)),
		diagnosticsCBORMapField(17, diagnosticsCBORBstr(bytes.Repeat([]byte{0x17}, 32))),
		diagnosticsCBORMapField(24, diagnosticsCBORUint(1)),
	)
	qrBytes := mustDiagnosticsEncodePairingGolden(t, qrValue)
	qr := mustDiagnosticsDecodePairingGolden(t, qrBytes)
	appRequestBytes, err := buildDiagnosticsAppPairingRequest(qr, currentApp, bytes.Repeat([]byte{0x20}, 32))
	if err != nil {
		t.Fatal(err)
	}
	appRequest := mustDiagnosticsDecodePairingGolden(t, appRequestBytes)
	appRequestDigest, _ := appRequest.digest()
	helperAcceptValue := diagnosticsCBORMapValue(
		diagnosticsCBORMapField(1, diagnosticsCBORTextValue(diagnosticsPairingCapabilityID)),
		diagnosticsCBORMapField(2, diagnosticsCBORUint(1)), diagnosticsCBORMapField(3, diagnosticsCBORUint(1)),
		diagnosticsCBORMapField(4, diagnosticsCBORUint(diagnosticsPairingHelperAccept)),
		diagnosticsCBORMapField(5, diagnosticsCBORBstr(bytes.Repeat([]byte{0x05}, 32))),
		diagnosticsCBORMapField(9, diagnosticsCBORBstr(currentHelperPublic)), diagnosticsCBORMapField(10, diagnosticsCBORBstr(currentHelperKeyID[:])),
		diagnosticsCBORMapField(11, diagnosticsCBORBstr(bytes.Repeat([]byte{0x11}, 32))),
		diagnosticsCBORMapField(12, diagnosticsCBORBstr(bytes.Repeat([]byte{0x12}, 32))),
		diagnosticsCBORMapField(13, diagnosticsCBORBstr(bytes.Repeat([]byte{0x13}, 32))),
		diagnosticsCBORMapField(14, diagnosticsCBORBstr(bytes.Repeat([]byte{0x14}, 32))),
		diagnosticsCBORMapField(15, diagnosticsCBORUint(issuedAt)), diagnosticsCBORMapField(16, diagnosticsCBORUint(expiresAt)),
		diagnosticsCBORMapField(18, diagnosticsCBORBstr(currentAppPublic)), diagnosticsCBORMapField(19, diagnosticsCBORBstr(currentAppKeyID[:])),
		diagnosticsCBORMapField(20, diagnosticsCBORBstr(bytes.Repeat([]byte{0x20}, 32))),
		diagnosticsCBORMapField(22, diagnosticsCBORBstr(appRequestDigest[:])), diagnosticsCBORMapField(23, diagnosticsCBORUint(1)),
		diagnosticsCBORMapField(24, diagnosticsCBORUint(1)), diagnosticsCBORMapField(25, diagnosticsCBORBstr(bytes.Repeat([]byte{0x25}, 32))),
	)
	helperAcceptBytes, err := signDiagnosticsPairingMessage(helperAcceptValue, currentHelper)
	if err != nil {
		t.Fatal(err)
	}
	helperAccept := mustDiagnosticsDecodePairingGolden(t, helperAcceptBytes)

	messages := map[uint64][]byte{
		diagnosticsPairingQR:           qrBytes,
		diagnosticsPairingAppRequest:   appRequestBytes,
		diagnosticsPairingHelperAccept: helperAcceptBytes,
	}
	prior := helperAccept
	for _, messageType := range []uint64{
		diagnosticsPairingFinalize, diagnosticsPairingFinalizeAck,
		diagnosticsPairingReceipt, diagnosticsPairingReadyAck,
		diagnosticsPairingActivate, diagnosticsPairingActiveAck,
		diagnosticsPairingAbort, diagnosticsPairingAbortAck,
	} {
		signer := currentApp
		if messageType%2 == 0 {
			signer = currentHelper
		}
		encoded, err := buildDiagnosticsBootstrapTransition(prior, messageType, signer, issuedAt, expiresAt)
		if err != nil {
			t.Fatalf("build bootstrap type %d: %v", messageType, err)
		}
		messages[messageType] = encoded
		prior = mustDiagnosticsDecodePairingGolden(t, encoded)
	}

	activeAck := mustDiagnosticsDecodePairingGolden(t, messages[diagnosticsPairingActiveAck])
	activeAckDigest, _ := activeAck.digest()
	identity := diagnosticsHelperCredentialIdentity{SigningSeed: currentHelper.Seed(), HelperEpoch: 1}
	authorization := diagnosticsPairingAuthorization{
		State: "active", HomeserverBinding: bytes.Repeat([]byte{0x11}, 32), FolderBinding: bytes.Repeat([]byte{0x12}, 32),
		AppPublicKey: currentAppPublic, AppKeyID: currentAppKeyID[:], AppEpoch: 1, HelperEpoch: 1,
		TLSSPKIPin: bytes.Repeat([]byte{0x15}, 32), CurrentStateDigest: activeAckDigest[:],
	}

	appRotationBytes, err := buildDiagnosticsAppKeyRotationRequest(authorization, identity, proposedApp.Public().(ed25519.PublicKey), currentApp, issuedAt, expiresAt, bytes.Repeat([]byte{0x0b}, 32))
	if err != nil {
		t.Fatal(err)
	}
	appRotation := mustDiagnosticsDecodePairingGolden(t, appRotationBytes)
	appProofBytes := mustDiagnosticsLifecycleGolden(t, appRotation, diagnosticsPairingAppKeyRotationNewProof, 0, nil, proposedApp, issuedAt, expiresAt, 0x0c)
	appProof := mustDiagnosticsDecodePairingGolden(t, appProofBytes)
	appAcceptBytes := mustDiagnosticsLifecycleGolden(t, appProof, diagnosticsPairingAppKeyRotationAccept, 0, nil, currentHelper, issuedAt, expiresAt, 0x0d)

	proposedHelperPublic := proposedHelper.Public().(ed25519.PublicKey)
	proposedHelperKeyID := diagnosticsKeyID(proposedHelperPublic)
	helperRotationFields, _ := diagnosticsLifecycleBaseFields(authorization, identity, diagnosticsPairingHelperKeyRotationPropose, issuedAt, expiresAt, bytes.Repeat([]byte{0x0e}, 32))
	helperRotationFields = append(helperRotationFields,
		diagnosticsCBORMapField(13, diagnosticsCBORBstr(proposedHelperPublic)),
		diagnosticsCBORMapField(14, diagnosticsCBORBstr(proposedHelperKeyID[:])),
		diagnosticsCBORMapField(20, diagnosticsCBORUint(2)),
	)
	helperRotationBytes, err := signDiagnosticsPairingMessage(diagnosticsCBORMapValue(helperRotationFields...), currentHelper)
	if err != nil {
		t.Fatal(err)
	}
	helperRotation := mustDiagnosticsDecodePairingGolden(t, helperRotationBytes)
	helperProofBytes := mustDiagnosticsLifecycleGolden(t, helperRotation, diagnosticsPairingHelperKeyRotationNewProof, 0, nil, proposedHelper, issuedAt, expiresAt, 0x0f)
	helperProof := mustDiagnosticsDecodePairingGolden(t, helperProofBytes)
	helperConfirmBytes := mustDiagnosticsLifecycleGolden(t, helperProof, diagnosticsPairingHelperKeyRotationConfirm, 0, nil, currentApp, issuedAt, expiresAt, 0x10)

	tlsRotationFields, _ := diagnosticsLifecycleBaseFields(authorization, identity, diagnosticsPairingTLSPinRotationPropose, issuedAt, expiresAt, bytes.Repeat([]byte{0x11}, 32))
	tlsRotationFields = append(tlsRotationFields, diagnosticsCBORMapField(16, diagnosticsCBORBstr(bytes.Repeat([]byte{0x16}, 32))))
	tlsRotationBytes, err := signDiagnosticsPairingMessage(diagnosticsCBORMapValue(tlsRotationFields...), currentHelper)
	if err != nil {
		t.Fatal(err)
	}
	tlsRotation := mustDiagnosticsDecodePairingGolden(t, tlsRotationBytes)
	tlsConfirmBytes := mustDiagnosticsLifecycleGolden(t, tlsRotation, diagnosticsPairingTLSPinRotationConfirm, 0, nil, currentApp, issuedAt, expiresAt, 0x12)

	revocationBytes, err := buildDiagnosticsRevocationRequest(authorization, identity, diagnosticsPairingRevocationUserRequest, currentApp, issuedAt, expiresAt, bytes.Repeat([]byte{0x13}, 32))
	if err != nil {
		t.Fatal(err)
	}
	revocation := mustDiagnosticsDecodePairingGolden(t, revocationBytes)
	revocationRecordBytes := mustDiagnosticsLifecycleGolden(t, revocation, diagnosticsPairingRevocationRecord, 0, nil, currentHelper, issuedAt, expiresAt, 0x14)

	appRotationDigest, _ := appRotation.digest()
	appAccept := mustDiagnosticsDecodePairingGolden(t, appAcceptBytes)
	lifecycleFinalizeBytes := mustDiagnosticsLifecycleGolden(t, appAccept, diagnosticsPairingLifecycleFinalize, diagnosticsPairingTransitionAppKey, appRotationDigest[:], proposedApp, issuedAt, expiresAt, 0x15)
	lifecycleFinalize := mustDiagnosticsDecodePairingGolden(t, lifecycleFinalizeBytes)
	lifecycleAckBytes := mustDiagnosticsLifecycleGolden(t, lifecycleFinalize, diagnosticsPairingLifecycleActiveAck, diagnosticsPairingTransitionAppKey, appRotationDigest[:], currentHelper, issuedAt, expiresAt, 0x16)

	helperRotationDigest, _ := helperRotation.digest()
	helperConfirm := mustDiagnosticsDecodePairingGolden(t, helperConfirmBytes)
	lifecycleAbortBytes := mustDiagnosticsLifecycleGolden(t, helperConfirm, diagnosticsPairingLifecycleAbort, diagnosticsPairingTransitionHelperKey, helperRotationDigest[:], currentApp, issuedAt, expiresAt, 0x17)
	lifecycleAbort := mustDiagnosticsDecodePairingGolden(t, lifecycleAbortBytes)
	lifecycleAbortAckBytes := mustDiagnosticsLifecycleGolden(t, lifecycleAbort, diagnosticsPairingLifecycleAbortAck, diagnosticsPairingTransitionHelperKey, helperRotationDigest[:], currentHelper, issuedAt, expiresAt, 0x18)

	messages[diagnosticsPairingAppKeyRotationRequest] = appRotationBytes
	messages[diagnosticsPairingAppKeyRotationNewProof] = appProofBytes
	messages[diagnosticsPairingAppKeyRotationAccept] = appAcceptBytes
	messages[diagnosticsPairingHelperKeyRotationPropose] = helperRotationBytes
	messages[diagnosticsPairingHelperKeyRotationNewProof] = helperProofBytes
	messages[diagnosticsPairingHelperKeyRotationConfirm] = helperConfirmBytes
	messages[diagnosticsPairingTLSPinRotationPropose] = tlsRotationBytes
	messages[diagnosticsPairingTLSPinRotationConfirm] = tlsConfirmBytes
	messages[diagnosticsPairingRevocationRequest] = revocationBytes
	messages[diagnosticsPairingRevocationRecord] = revocationRecordBytes
	messages[diagnosticsPairingLifecycleFinalize] = lifecycleFinalizeBytes
	messages[diagnosticsPairingLifecycleActiveAck] = lifecycleAckBytes
	messages[diagnosticsPairingLifecycleAbort] = lifecycleAbortBytes
	messages[diagnosticsPairingLifecycleAbortAck] = lifecycleAbortAckBytes

	result := make(map[string]string, len(messages))
	for messageType, encoded := range messages {
		decoded := mustDiagnosticsDecodePairingGolden(t, encoded)
		if decoded.messageType != messageType {
			t.Fatalf("golden type %d decoded as %d", messageType, decoded.messageType)
		}
		result[diagnosticsPairingGoldenName(messageType)] = hex.EncodeToString(encoded)
	}
	return result
}

func mustDiagnosticsEncodePairingGolden(t testing.TB, value diagnosticsCBORValue) []byte {
	t.Helper()
	encoded, err := encodeDiagnosticsCBOR(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func mustDiagnosticsDecodePairingGolden(t testing.TB, encoded []byte) diagnosticsPairingMessage {
	t.Helper()
	message, err := decodeDiagnosticsPairingMessage(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func mustDiagnosticsLifecycleGolden(t testing.TB, prior diagnosticsPairingMessage, messageType, kind uint64, transitionDigest []byte, signer ed25519.PrivateKey, issuedAt, expiresAt uint64, nonce byte) []byte {
	t.Helper()
	encoded, err := buildDiagnosticsLifecycleContinuation(prior, messageType, kind, transitionDigest, signer, issuedAt, expiresAt, bytes.Repeat([]byte{nonce}, 32))
	if err != nil {
		t.Fatalf("build lifecycle type %d: %v", messageType, err)
	}
	return encoded
}

func diagnosticsPairingGoldenName(messageType uint64) string {
	names := []string{
		"00_qr", "01_app_request", "02_helper_accept", "03_finalize", "04_finalize_ack",
		"05_receipt", "06_ready_ack", "07_activate", "08_active_ack", "09_abort", "10_abort_ack",
		"11_app_key_rotation_request", "12_app_key_rotation_new_proof", "13_app_key_rotation_accept",
		"14_helper_key_rotation_propose", "15_helper_key_rotation_new_proof", "16_helper_key_rotation_confirm",
		"17_tls_pin_rotation_propose", "18_tls_pin_rotation_confirm", "19_revocation_request", "20_revocation_record",
		"21_lifecycle_finalize", "22_lifecycle_active_ack", "23_lifecycle_abort", "24_lifecycle_abort_ack",
	}
	if messageType >= uint64(len(names)) {
		return ""
	}
	return names[messageType]
}

func TestDiagnosticsPairingGoldenNamesAreOrderedAndUnique(t *testing.T) {
	names := make([]string, 0, diagnosticsPairingLifecycleAbortAck+1)
	for messageType := diagnosticsPairingQR; messageType <= diagnosticsPairingLifecycleAbortAck; messageType++ {
		names = append(names, diagnosticsPairingGoldenName(messageType))
	}
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	if !reflect.DeepEqual(names, sorted) {
		t.Fatal("pairing golden names do not preserve message-type order")
	}
}
