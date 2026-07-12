package main

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

type diagnosticsContractFixture struct {
	FixtureVersion  int                          `json:"fixture_version"`
	SourceDecisions []string                     `json:"source_decisions"`
	Limits          diagnosticsContractLimits    `json:"limits"`
	Capabilities    map[string]string            `json:"capabilities"`
	Registries      map[string]map[string]string `json:"registries"`
	Domains         map[string]string            `json:"domains"`
	DigestChains    map[string]string            `json:"digest_chains"`
	Vectors         diagnosticsContractVectors   `json:"vectors"`
}

type diagnosticsContractLimits struct {
	MaximumMessageBytes int `json:"maximum_message_bytes"`
	MaximumMapEntries   int `json:"maximum_map_entries"`
	MaximumArrayEntries int `json:"maximum_array_entries"`
	MaximumNestingDepth int `json:"maximum_nesting_depth"`
}

type diagnosticsContractVectors struct {
	RFC8032           diagnosticsRFC8032Vector       `json:"rfc8032"`
	RFC8032Additional []diagnosticsRFC8032Vector     `json:"rfc8032_additional"`
	BootstrapHMAC     diagnosticsBootstrapHMACVector `json:"bootstrap_hmac"`
	Derivations       diagnosticsDerivationVector    `json:"derivations"`
	ContractQuery     diagnosticsContractQueryVector `json:"contract_query"`
	Privacy           diagnosticsPrivacyVector       `json:"privacy"`
	RelayV1           diagnosticsRelayV1Vector       `json:"relay_v1"`
}

type diagnosticsRFC8032Vector struct {
	SeedHex      string `json:"seed_hex"`
	PublicKeyHex string `json:"public_key_hex"`
	MessageHex   string `json:"message_hex"`
	SignatureHex string `json:"signature_hex"`
}

type diagnosticsBootstrapHMACVector struct {
	SecretHex        string `json:"secret_hex"`
	CanonicalBodyHex string `json:"canonical_body_hex"`
	ExpectedHMACHex  string `json:"expected_hmac_hex"`
}

type diagnosticsDerivationVector struct {
	RawPublicKeyHex                    string `json:"raw_public_key_hex"`
	RawDeviceIDHex                     string `json:"raw_device_id_hex"`
	FolderID                           string `json:"folder_id"`
	TLSSPKIDERHex                      string `json:"tls_spki_der_hex"`
	HomeserverBindingHex               string `json:"homeserver_binding_hex"`
	FolderBindingHex                   string `json:"folder_binding_hex"`
	AppRequestDigestHex                string `json:"app_request_digest_hex"`
	HelperAcceptDigestHex              string `json:"helper_accept_digest_hex"`
	OperationIDHex                     string `json:"operation_id_hex"`
	ExpectedKeyIDHex                   string `json:"expected_key_id_hex"`
	ExpectedDeviceIDDigestHex          string `json:"expected_device_id_digest_hex"`
	ExpectedFolderIDDigestHex          string `json:"expected_folder_id_digest_hex"`
	ExpectedTLSSPKIPinHex              string `json:"expected_tls_spki_pin_hex"`
	ExpectedInstallationBindingHex     string `json:"expected_installation_binding_hex"`
	ExpectedTranscriptFingerprint      string `json:"expected_transcript_fingerprint"`
	ExpectedInstallationComponent      string `json:"expected_installation_component"`
	ExpectedOperationComponent         string `json:"expected_operation_component"`
	ExpectedRequestFilename            string `json:"expected_request_filename"`
	ExpectedAttestationFilename        string `json:"expected_attestation_filename"`
	ExpectedResponseFilename           string `json:"expected_response_filename"`
	ExpectedHelperEpochFilename        string `json:"expected_helper_epoch_filename"`
	ExpectedAuthorizationEpochFilename string `json:"expected_authorization_epoch_filename"`
	ExpectedPayloadDigestHex           string `json:"expected_payload_digest_hex"`
}

type diagnosticsContractQueryVector struct {
	IssuedAt                 uint64 `json:"issued_at"`
	ExpiresAt                uint64 `json:"expires_at"`
	HomeserverByte           byte   `json:"homeserver_byte"`
	FolderByte               byte   `json:"folder_byte"`
	HelperPublicKeyByte      byte   `json:"helper_public_key_byte"`
	QueryNonceByte           byte   `json:"query_nonce_byte"`
	ExpectedCanonicalBodyHex string `json:"expected_canonical_body_hex"`
	ExpectedDigestHex        string `json:"expected_digest_hex"`
	ExpectedSignatureHex     string `json:"expected_signature_hex"`
}

type diagnosticsPrivacyVector struct {
	Sentinels                   []string `json:"sentinels"`
	ExpectedLogSnapshot         string   `json:"expected_log_snapshot"`
	ExpectedPersistenceSnapshot string   `json:"expected_persistence_snapshot"`
}

type diagnosticsRelayV1Vector struct {
	TriggerPath             string   `json:"trigger_path"`
	TriggerBody             string   `json:"trigger_body"`
	StatusPath              string   `json:"status_path"`
	StatusRequestFields     []string `json:"status_request_fields"`
	ForbiddenContractFields []string `json:"forbidden_contract_fields"`
}

func loadDiagnosticsContractFixture(t testing.TB) diagnosticsContractFixture {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve diagnostics fixture caller")
	}
	path := filepath.Join(filepath.Dir(currentFile), "..", "ios", "VaultSyncTests", "Fixtures", "diagnostics-contract-v1.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read canonical diagnostics fixture: %v", err)
	}
	var fixture diagnosticsContractFixture
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fixture); err != nil {
		t.Fatalf("decode canonical diagnostics fixture: %v", err)
	}
	return fixture
}

func expectedDiagnosticsRegistries() map[string]map[string]string {
	return map[string]map[string]string{
		"pairing_bootstrap": {
			"1": "capability:text=eu.vaultsync.diagnostics.helper-pairing/1", "2": "protocol:uint=1", "3": "suite:uint=1", "4": "message_type:uint-enum=0..10",
			"5": "invitation_nonce:bstr=32", "6": "endpoint_host:ascii=1..253", "7": "endpoint_port:uint=1..65535", "8": "tls_spki_pin:bstr=32",
			"9": "helper_public_key:bstr=32", "10": "helper_key_id:bstr=32", "11": "homeserver_binding:bstr=32", "12": "folder_binding:bstr=32",
			"13": "device_id_digest:bstr=32", "14": "folder_id_digest:bstr=32", "15": "issued_at:uint", "16": "expires_at:uint",
			"17": "bootstrap_secret:bstr=32;qr-only", "18": "app_public_key:bstr=32", "19": "app_key_id:bstr=32", "20": "app_nonce:bstr=32",
			"21": "bootstrap_hmac:bstr=32", "22": "app_request_digest:bstr=32", "23": "app_epoch:uint;initial=1", "24": "helper_epoch:uint",
			"25": "helper_nonce:bstr=32", "26": "prior_message_digest:bstr=32", "255": "signature:bstr=64",
		},
		"pairing_lifecycle": {
			"1": "capability:text=eu.vaultsync.diagnostics.helper-pairing/1", "2": "protocol:uint=1", "3": "suite:uint=1", "4": "message_type:uint-enum=11..24",
			"5": "homeserver_binding:bstr=32", "6": "folder_binding:bstr=32", "7": "current_app_public_key:bstr=32", "8": "current_app_key_id:bstr=32",
			"9": "proposed_app_public_key:bstr=32", "10": "proposed_app_key_id:bstr=32", "11": "current_helper_public_key:bstr=32", "12": "current_helper_key_id:bstr=32",
			"13": "proposed_helper_public_key:bstr=32", "14": "proposed_helper_key_id:bstr=32", "15": "current_tls_spki_pin:bstr=32", "16": "proposed_tls_spki_pin:bstr=32",
			"17": "current_app_epoch:uint", "18": "proposed_app_epoch:uint", "19": "current_helper_epoch:uint", "20": "proposed_helper_epoch:uint",
			"21": "issued_at:uint", "22": "expires_at:uint", "23": "message_nonce:bstr=32", "24": "prior_message_digest:bstr=32",
			"25": "revocation_reason:uint-enum=1..4", "26": "current_credential_state_digest:bstr=32", "27": "revocation_origin:uint-enum=1..2",
			"28": "transition_digest:bstr=32", "29": "transition_kind:uint-enum=1..3", "255": "signature:bstr=64",
		},
		"namespace": {
			"1": "capability:text=eu.vaultsync.diagnostics.namespace/1", "2": "protocol:uint=1", "3": "suite:uint=1", "4": "message_type:uint-enum=1..5",
			"5": "homeserver_binding:bstr=32", "6": "folder_binding:bstr=32", "7": "namespace_id:bstr=32", "8": "installation_binding:bstr=32",
			"9": "namespace_initial_app_key_id:bstr=32", "10": "current_app_public_key:bstr=32", "11": "current_app_key_id:bstr=32", "12": "current_app_epoch:uint",
			"13": "current_helper_public_key:bstr=32", "14": "current_helper_key_id:bstr=32", "15": "current_helper_epoch:uint", "16": "prior_helper_public_key:bstr=32",
			"17": "prior_helper_key_id:bstr=32", "18": "prior_helper_epoch:uint", "19": "enablement_nonce:bstr=32", "20": "enablement_request_digest:bstr=32",
			"21": "root_manifest_digest:bstr=32", "22": "prior_helper_manifest_digest:bstr=32", "23": "current_helper_manifest_digest:bstr=32",
			"24": "prior_authorization_digest:bstr=32", "25": "current_credential_state_digest:bstr=32", "26": "issued_at:uint", "27": "expires_at:uint",
			"28": "created_at:uint", "29": "readme_digest:bstr=32", "30": "authorization_nonce:bstr=32", "31": "authorization_epoch:uint;initial=1",
			"253": "app_signature:bstr=64", "254": "prior_helper_signature:bstr=64", "255": "helper_signature:bstr=64",
		},
		"roundtrip": {
			"1": "capability:text=eu.vaultsync.diagnostics.correlated-roundtrip/1", "2": "protocol_major:uint=1", "3": "suite:uint=1", "4": "message_type:uint-enum=1..9",
			"5": "homeserver_binding:bstr=32", "6": "folder_binding:bstr=32", "7": "app_key_id:bstr=32", "8": "helper_key_id:bstr=32",
			"9": "app_epoch:uint", "10": "helper_epoch:uint", "11": "operation_id:bstr=32", "12": "issued_at:uint", "13": "expires_at:uint",
			"14": "request_nonce:bstr=32", "15": "request_payload:bstr=256", "16": "request_payload_digest:bstr=32", "17": "request_digest:bstr=32",
			"18": "helper_nonce:bstr=32", "19": "observed_at:uint", "20": "attestation_digest:bstr=32", "21": "authorization_nonce:bstr=32",
			"22": "authorization_digest:bstr=32", "23": "response_nonce:bstr=32", "24": "response_payload:bstr=256", "25": "response_payload_digest:bstr=32",
			"27": "capability_flags:uint=15", "28": "cleanup_targets:array[bstr=32]=1..3;sorted;unique", "29": "cleanup_results:array[uint-enum=1..4]=1..3",
			"30": "query_nonce:bstr=32", "31": "prior_message_digest:bstr=32", "255": "signature:bstr=64",
		},
	}
}

func expectedDiagnosticsDomains() map[string]string {
	return map[string]string{
		"pairing.app_request": "eu.vaultsync.helper-pairing/v1/app-request\x00", "pairing.helper_accept": "eu.vaultsync.helper-pairing/v1/helper-accept\x00",
		"pairing.finalize": "eu.vaultsync.helper-pairing/v1/pairing-finalize\x00", "pairing.finalize_ack": "eu.vaultsync.helper-pairing/v1/pairing-finalize-ack\x00",
		"pairing.receipt": "eu.vaultsync.helper-pairing/v1/pairing-receipt\x00", "pairing.ready_ack": "eu.vaultsync.helper-pairing/v1/pairing-ready-ack\x00",
		"pairing.activate": "eu.vaultsync.helper-pairing/v1/pairing-activate\x00", "pairing.active_ack": "eu.vaultsync.helper-pairing/v1/pairing-active-ack\x00",
		"pairing.abort": "eu.vaultsync.helper-pairing/v1/pairing-abort\x00", "pairing.abort_ack": "eu.vaultsync.helper-pairing/v1/pairing-abort-ack\x00",
		"pairing.app_key_rotation_request":      "eu.vaultsync.helper-pairing/v1/app-key-rotation-request\x00",
		"pairing.app_key_rotation_new_proof":    "eu.vaultsync.helper-pairing/v1/app-key-rotation-new-proof\x00",
		"pairing.app_key_rotation_accept":       "eu.vaultsync.helper-pairing/v1/app-key-rotation-accept\x00",
		"pairing.helper_key_rotation_propose":   "eu.vaultsync.helper-pairing/v1/helper-key-rotation-propose\x00",
		"pairing.helper_key_rotation_new_proof": "eu.vaultsync.helper-pairing/v1/helper-key-rotation-new-proof\x00",
		"pairing.helper_key_rotation_confirm":   "eu.vaultsync.helper-pairing/v1/helper-key-rotation-confirm\x00",
		"pairing.tls_pin_rotation_propose":      "eu.vaultsync.helper-pairing/v1/tls-pin-rotation-propose\x00",
		"pairing.tls_pin_rotation_confirm":      "eu.vaultsync.helper-pairing/v1/tls-pin-rotation-confirm\x00",
		"pairing.revocation_request":            "eu.vaultsync.helper-pairing/v1/revocation-request\x00", "pairing.revocation_record": "eu.vaultsync.helper-pairing/v1/revocation-record\x00",
		"pairing.lifecycle_finalize": "eu.vaultsync.helper-pairing/v1/lifecycle-finalize\x00", "pairing.lifecycle_active_ack": "eu.vaultsync.helper-pairing/v1/lifecycle-active-ack\x00",
		"pairing.lifecycle_abort": "eu.vaultsync.helper-pairing/v1/lifecycle-abort\x00", "pairing.lifecycle_abort_ack": "eu.vaultsync.helper-pairing/v1/lifecycle-abort-ack\x00",
		"namespace.enablement_request": "eu.vaultsync.namespace/v1/enablement-request\x00", "namespace.root_manifest": "eu.vaultsync.namespace/v1/root-manifest\x00",
		"namespace.helper_epoch_prior": "eu.vaultsync.namespace/v1/helper-epoch-prior\x00", "namespace.helper_epoch_current": "eu.vaultsync.namespace/v1/helper-epoch-current\x00",
		"namespace.authorization_initial_app":    "eu.vaultsync.namespace/v1/authorization-initial-app\x00",
		"namespace.authorization_initial_helper": "eu.vaultsync.namespace/v1/authorization-initial-helper\x00",
		"namespace.authorization_epoch_app":      "eu.vaultsync.namespace/v1/authorization-epoch-app\x00",
		"namespace.authorization_epoch_helper":   "eu.vaultsync.namespace/v1/authorization-epoch-helper\x00",
		"roundtrip.capability_query":             "eu.vaultsync.roundtrip/v1/capability-query\x00", "roundtrip.capability_response": "eu.vaultsync.roundtrip/v1/capability-response\x00",
		"roundtrip.operation_request": "eu.vaultsync.roundtrip/v1/operation-request\x00", "roundtrip.attestation_query": "eu.vaultsync.roundtrip/v1/attestation-query\x00",
		"roundtrip.upload_attestation": "eu.vaultsync.roundtrip/v1/upload-attestation\x00", "roundtrip.response_authorization": "eu.vaultsync.roundtrip/v1/response-authorization\x00",
		"roundtrip.response_artifact": "eu.vaultsync.roundtrip/v1/response-artifact\x00", "roundtrip.cleanup_request": "eu.vaultsync.roundtrip/v1/cleanup-request\x00",
		"roundtrip.cleanup_ack": "eu.vaultsync.roundtrip/v1/cleanup-ack\x00",
	}
}

func expectedDiagnosticsDigestChains() map[string]string {
	return map[string]string{
		"pairing.finalize": "pairing.helper_accept", "pairing.finalize_ack": "pairing.finalize", "pairing.receipt": "pairing.finalize_ack",
		"pairing.ready_ack": "pairing.receipt", "pairing.activate": "pairing.ready_ack", "pairing.active_ack": "pairing.activate",
		"pairing.abort": "latest_pending_or_active_pairing_message", "pairing.abort_ack": "pairing.abort",
		"pairing.app_key_rotation_new_proof": "pairing.app_key_rotation_request", "pairing.app_key_rotation_accept": "pairing.app_key_rotation_new_proof",
		"pairing.helper_key_rotation_new_proof": "pairing.helper_key_rotation_propose", "pairing.helper_key_rotation_confirm": "pairing.helper_key_rotation_new_proof",
		"pairing.tls_pin_rotation_confirm": "pairing.tls_pin_rotation_propose", "pairing.revocation_record_signed_app": "pairing.revocation_request",
		"pairing.lifecycle_finalize": "accepted_rotation_transition", "pairing.lifecycle_active_ack": "pairing.lifecycle_finalize",
		"pairing.lifecycle_abort": "latest_pending_lifecycle_message", "pairing.lifecycle_abort_ack": "pairing.lifecycle_abort",
		"namespace.root_manifest": "namespace.enablement_request", "namespace.helper_epoch": "root_or_immediately_prior_helper_manifest",
		"namespace.authorization_initial": "root_and_current_helper_manifest_and_credential_state",
		"namespace.authorization_epoch":   "immediately_prior_authorization_and_current_helper_manifest_and_credential_state",
		"roundtrip.capability_response":   "roundtrip.capability_query", "roundtrip.upload_attestation": "roundtrip.operation_request_and_attestation_query",
		"roundtrip.response_authorization": "roundtrip.operation_request_and_upload_attestation",
		"roundtrip.response_artifact":      "roundtrip.operation_request_and_upload_attestation_and_response_authorization",
		"roundtrip.cleanup_ack":            "roundtrip.cleanup_request",
	}
}

func TestDiagnosticsContractFixtureCatalogsAreExact(t *testing.T) {
	fixture := loadDiagnosticsContractFixture(t)
	if fixture.FixtureVersion != 1 || !reflect.DeepEqual(fixture.SourceDecisions, []string{"022", "023", "024"}) {
		t.Fatalf("unexpected fixture identity: version=%d decisions=%v", fixture.FixtureVersion, fixture.SourceDecisions)
	}
	if fixture.Limits != (diagnosticsContractLimits{16384, 32, 8, 4}) {
		t.Fatalf("unexpected deterministic-CBOR limits: %+v", fixture.Limits)
	}
	if !reflect.DeepEqual(fixture.Registries, expectedDiagnosticsRegistries()) {
		t.Fatal("Decision 022-024 field registry differs from the canonical test model")
	}
	if !reflect.DeepEqual(fixture.Domains, expectedDiagnosticsDomains()) {
		t.Fatal("Decision 022-024 signature domain catalog differs from the canonical test model")
	}
	if !reflect.DeepEqual(fixture.DigestChains, expectedDiagnosticsDigestChains()) {
		t.Fatal("Decision 022-024 digest-chain catalog differs from the canonical test model")
	}
	for name, domain := range fixture.Domains {
		if !strings.HasSuffix(domain, "\x00") || strings.Count(domain, "\x00") != 1 {
			t.Fatalf("domain %s does not have exactly one trailing NUL", name)
		}
	}
}

func TestDiagnosticsRFC8032Vector(t *testing.T) {
	fixture := loadDiagnosticsContractFixture(t)
	vectors := append([]diagnosticsRFC8032Vector{fixture.Vectors.RFC8032}, fixture.Vectors.RFC8032Additional...)
	if len(vectors) < 2 {
		t.Fatal("at least two RFC 8032 vectors are required")
	}
	for index, vector := range vectors {
		seed := mustDecodeHex(t, vector.SeedHex)
		publicKey := mustDecodeHex(t, vector.PublicKeyHex)
		message := mustDecodeHex(t, vector.MessageHex)
		expectedSignature := mustDecodeHex(t, vector.SignatureHex)
		privateKey := ed25519.NewKeyFromSeed(seed)
		if got := privateKey.Public().(ed25519.PublicKey); !reflect.DeepEqual([]byte(got), publicKey) {
			t.Fatalf("RFC 8032 vector %d public key = %x, want %x", index, got, publicKey)
		}
		signature := ed25519.Sign(privateKey, message)
		if !reflect.DeepEqual(signature, expectedSignature) {
			t.Fatalf("RFC 8032 vector %d signature = %x, want %x", index, signature, expectedSignature)
		}
		if !ed25519.Verify(ed25519.PublicKey(publicKey), message, signature) {
			t.Fatalf("RFC 8032 vector %d signature did not verify", index)
		}
	}
}

func TestDiagnosticsBootstrapHMACVector(t *testing.T) {
	vector := loadDiagnosticsContractFixture(t).Vectors.BootstrapHMAC
	secret := mustDecodeHex(t, vector.SecretHex)
	body := mustDecodeHex(t, vector.CanonicalBodyHex)
	if _, err := decodeTestContractCBOR(body); err != nil {
		t.Fatalf("normative bootstrap body is not canonical CBOR: %v", err)
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("eu.vaultsync.helper-pairing/v1/bootstrap-hmac\x00"))
	mac.Write(body)
	if got := hex.EncodeToString(mac.Sum(nil)); got != vector.ExpectedHMACHex {
		t.Fatalf("bootstrap HMAC = %s, want %s", got, vector.ExpectedHMACHex)
	}
}

func TestDiagnosticsDerivationAndCrossLanguageGoldenBytes(t *testing.T) {
	fixture := loadDiagnosticsContractFixture(t)
	vector := fixture.Vectors.Derivations
	actual := generatedDiagnosticsDerivations(t, vector)
	expected := map[string]string{
		"key_id": vector.ExpectedKeyIDHex, "device_id_digest": vector.ExpectedDeviceIDDigestHex,
		"folder_id_digest": vector.ExpectedFolderIDDigestHex, "tls_spki_pin": vector.ExpectedTLSSPKIPinHex,
		"installation_binding": vector.ExpectedInstallationBindingHex, "transcript_fingerprint": vector.ExpectedTranscriptFingerprint,
		"installation_component": vector.ExpectedInstallationComponent, "operation_component": vector.ExpectedOperationComponent,
		"request_filename": vector.ExpectedRequestFilename, "attestation_filename": vector.ExpectedAttestationFilename,
		"response_filename": vector.ExpectedResponseFilename, "helper_epoch_filename": vector.ExpectedHelperEpochFilename,
		"authorization_epoch_filename": vector.ExpectedAuthorizationEpochFilename, "payload_digest": vector.ExpectedPayloadDigestHex,
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("derivation fixture is stale\nactual:   %#v\nexpected: %#v", actual, expected)
	}

	body, digest, signature := generatedDiagnosticsCapabilityQuery(t, fixture)
	query := fixture.Vectors.ContractQuery
	if hex.EncodeToString(body) != query.ExpectedCanonicalBodyHex || hex.EncodeToString(digest) != query.ExpectedDigestHex || hex.EncodeToString(signature) != query.ExpectedSignatureHex {
		t.Fatalf("capability-query fixture is stale\nbody=%x\ndigest=%x\nsignature=%x", body, digest, signature)
	}
	decoded, err := decodeTestContractCBOR(body)
	if err != nil {
		t.Fatalf("decode capability-query golden: %v", err)
	}
	if err := validateDiagnosticsCapabilityQuery(decoded, fixture); err != nil {
		t.Fatalf("validate capability-query golden: %v", err)
	}
	publicKey := mustDecodeHex(t, fixture.Vectors.RFC8032.PublicKeyHex)
	input := append([]byte(fixture.Domains["roundtrip.capability_query"]), body...)
	if !ed25519.Verify(ed25519.PublicKey(publicKey), input, signature) {
		t.Fatal("capability-query golden signature did not verify")
	}
}

func TestDiagnosticsAllDomainsHaveGoldenDigestsAndSeparation(t *testing.T) {
	fixture := loadDiagnosticsContractFixture(t)
	seed := mustDecodeHex(t, fixture.Vectors.RFC8032.SeedHex)
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	emptyMap := []byte{0xa0}
	seen := make(map[string]string, len(fixture.Domains))
	for name, domain := range fixture.Domains {
		digest := sha256.Sum256(append([]byte(domain), emptyMap...))
		digestHex := hex.EncodeToString(digest[:])
		if prior, exists := seen[digestHex]; exists {
			t.Fatalf("domains %s and %s produced the same digest", name, prior)
		}
		seen[digestHex] = name
		input := append([]byte(domain), emptyMap...)
		signature := ed25519.Sign(privateKey, input)
		if !ed25519.Verify(publicKey, input, signature) {
			t.Fatalf("signature domain %s failed verification", name)
		}
	}
}

func generatedDiagnosticsDerivations(t testing.TB, vector diagnosticsDerivationVector) map[string]string {
	t.Helper()
	publicKey := mustDecodeHex(t, vector.RawPublicKeyHex)
	deviceID := mustDecodeHex(t, vector.RawDeviceIDHex)
	spki := mustDecodeHex(t, vector.TLSSPKIDERHex)
	homeserverBinding := mustDecodeHex(t, vector.HomeserverBindingHex)
	folderBinding := mustDecodeHex(t, vector.FolderBindingHex)
	appRequestDigest := mustDecodeHex(t, vector.AppRequestDigestHex)
	helperAcceptDigest := mustDecodeHex(t, vector.HelperAcceptDigestHex)
	operationID := mustDecodeHex(t, vector.OperationIDHex)

	keyID := testDomainSHA256("eu.vaultsync.key-id/ed25519/v1\x00", publicKey)
	deviceDigest := testDomainSHA256("eu.vaultsync.binding/syncthing-device/v1\x00", deviceID)
	folderInput := make([]byte, 4, 4+len(vector.FolderID))
	binary.BigEndian.PutUint32(folderInput, uint32(len(vector.FolderID)))
	folderInput = append(folderInput, []byte(vector.FolderID)...)
	folderDigest := testDomainSHA256("eu.vaultsync.binding/syncthing-folder/v1\x00", folderInput)
	spkiPin := sha256.Sum256(spki)
	installationInput := append(append(append([]byte(nil), keyID...), homeserverBinding...), folderBinding...)
	installationBinding := testDomainSHA256("eu.vaultsync.namespace/installation/v1\x00", installationInput)
	fingerprintInput := append(append([]byte(nil), appRequestDigest...), helperAcceptDigest...)
	fingerprint := testDomainSHA256("eu.vaultsync.helper-pairing/v1/transcript-fingerprint\x00", fingerprintInput)
	installationComponent := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(installationBinding))
	operationComponent := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(operationID))
	payload := make([]byte, 256)
	for index := range payload {
		payload[index] = byte(index)
	}
	payloadDigest := sha256.Sum256(payload)
	return map[string]string{
		"key_id": hex.EncodeToString(keyID), "device_id_digest": hex.EncodeToString(deviceDigest),
		"folder_id_digest": hex.EncodeToString(folderDigest), "tls_spki_pin": hex.EncodeToString(spkiPin[:]),
		"installation_binding": hex.EncodeToString(installationBinding), "transcript_fingerprint": strings.ToUpper(hex.EncodeToString(fingerprint[:6])),
		"installation_component": installationComponent, "operation_component": operationComponent,
		"request_filename": operationComponent + ".request.cbor", "attestation_filename": operationComponent + ".attestation.cbor",
		"response_filename": operationComponent + ".response.cbor", "helper_epoch_filename": "1.helper-manifest.cbor",
		"authorization_epoch_filename": "1.authorization.cbor", "payload_digest": hex.EncodeToString(payloadDigest[:]),
	}
}

func generatedDiagnosticsCapabilityQuery(t testing.TB, fixture diagnosticsContractFixture) ([]byte, []byte, []byte) {
	t.Helper()
	vector := fixture.Vectors.ContractQuery
	publicKey := mustDecodeHex(t, fixture.Vectors.RFC8032.PublicKeyHex)
	helperPublicKey := repeatTestByte(vector.HelperPublicKeyByte, 32)
	appKeyID := testDomainSHA256("eu.vaultsync.key-id/ed25519/v1\x00", publicKey)
	helperKeyID := testDomainSHA256("eu.vaultsync.key-id/ed25519/v1\x00", helperPublicKey)
	bodyValue := testCBORMapValue(
		testCBORField(1, testCBORTextValue(fixture.Capabilities["roundtrip"])),
		testCBORField(2, testCBORUint(1)), testCBORField(3, testCBORUint(1)), testCBORField(4, testCBORUint(1)),
		testCBORField(5, testCBORBstr(repeatTestByte(vector.HomeserverByte, 32))),
		testCBORField(6, testCBORBstr(repeatTestByte(vector.FolderByte, 32))),
		testCBORField(7, testCBORBstr(appKeyID)), testCBORField(8, testCBORBstr(helperKeyID)),
		testCBORField(9, testCBORUint(1)), testCBORField(10, testCBORUint(1)),
		testCBORField(12, testCBORUint(vector.IssuedAt)), testCBORField(13, testCBORUint(vector.ExpiresAt)),
		testCBORField(30, testCBORBstr(repeatTestByte(vector.QueryNonceByte, 32))),
	)
	body, err := encodeTestContractCBOR(bodyValue)
	if err != nil {
		t.Fatalf("encode capability query: %v", err)
	}
	input := append([]byte(fixture.Domains["roundtrip.capability_query"]), body...)
	digest := sha256.Sum256(input)
	privateKey := ed25519.NewKeyFromSeed(mustDecodeHex(t, fixture.Vectors.RFC8032.SeedHex))
	return body, digest[:], ed25519.Sign(privateKey, input)
}

func validateDiagnosticsCapabilityQuery(value testCBORValue, fixture diagnosticsContractFixture) error {
	wantLabels := []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 12, 13, 30}
	if value.kind != testCBORMap || len(value.entries) != len(wantLabels) {
		return errorsForDiagnostics("capability query has wrong map shape")
	}
	for index, entry := range value.entries {
		if entry.label != wantLabels[index] {
			return errorsForDiagnostics("capability query has an unknown or missing field")
		}
	}
	for _, label := range []uint64{2, 3, 4, 9, 10} {
		field, _ := testCBORMapLookup(value, label)
		if field.kind != testCBORUnsigned || field.unsigned != 1 {
			return errorsForDiagnostics("capability query has a wrong uint field")
		}
	}
	capability, _ := testCBORMapLookup(value, 1)
	if capability.kind != testCBORText || capability.text != fixture.Capabilities["roundtrip"] {
		return errorsForDiagnostics("capability query has a wrong capability")
	}
	for _, label := range []uint64{5, 6, 7, 8, 30} {
		field, _ := testCBORMapLookup(value, label)
		if field.kind != testCBORBytes || len(field.bytes) != 32 {
			return errorsForDiagnostics("capability query has a wrong byte-string length")
		}
	}
	issued, _ := testCBORMapLookup(value, 12)
	expires, _ := testCBORMapLookup(value, 13)
	if issued.kind != testCBORUnsigned || expires.kind != testCBORUnsigned || expires.unsigned <= issued.unsigned || expires.unsigned-issued.unsigned > 120 {
		return errorsForDiagnostics("capability query has invalid time bounds")
	}
	return nil
}

type diagnosticsValidationError string

func (err diagnosticsValidationError) Error() string { return string(err) }

func errorsForDiagnostics(message string) error { return diagnosticsValidationError(message) }

func TestPrintDiagnosticsGeneratedFixtureValues(t *testing.T) {
	if os.Getenv("VAULTSYNC_PRINT_DIAGNOSTICS_FIXTURE") != "1" {
		t.Skip("set VAULTSYNC_PRINT_DIAGNOSTICS_FIXTURE=1 to print generated values")
	}
	fixture := loadDiagnosticsContractFixture(t)
	derivations := generatedDiagnosticsDerivations(t, fixture.Vectors.Derivations)
	body, digest, signature := generatedDiagnosticsCapabilityQuery(t, fixture)
	output := map[string]any{
		"derivations": derivations,
		"contract_query": map[string]string{
			"expected_canonical_body_hex": hex.EncodeToString(body),
			"expected_digest_hex":         hex.EncodeToString(digest),
			"expected_signature_hex":      hex.EncodeToString(signature),
		},
	}
	encoded, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(string(encoded))
}

func mustDecodeHex(t testing.TB, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("decode hex fixture: %v", err)
	}
	return decoded
}

func testDomainSHA256(domain string, body []byte) []byte {
	digest := sha256.Sum256(append([]byte(domain), body...))
	return digest[:]
}

func repeatTestByte(value byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return result
}
