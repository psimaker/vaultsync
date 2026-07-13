package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type diagnosticsUploadGoldenFixture struct {
	FixtureVersion          int    `json:"fixture_version"`
	SourceDecision          string `json:"source_decision"`
	AppSeedHex              string `json:"app_seed_hex"`
	HelperSeedHex           string `json:"helper_seed_hex"`
	HomeserverBindingHex    string `json:"homeserver_binding_hex"`
	FolderBindingHex        string `json:"folder_binding_hex"`
	InstallationBindingHex  string `json:"installation_binding_hex"`
	AppEpoch                uint64 `json:"app_epoch"`
	HelperEpoch             uint64 `json:"helper_epoch"`
	AuthorizationEpoch      uint64 `json:"authorization_epoch"`
	OperationIDHex          string `json:"operation_id_hex"`
	RequestNonceHex         string `json:"request_nonce_hex"`
	QueryNonceHex           string `json:"query_nonce_hex"`
	HelperNonceHex          string `json:"helper_nonce_hex"`
	RequestIssuedAt         uint64 `json:"request_issued_at"`
	QueryIssuedAt           uint64 `json:"query_issued_at"`
	AttestationIssuedAt     uint64 `json:"attestation_issued_at"`
	ExpiresAt               uint64 `json:"expires_at"`
	RequestPayloadHex       string `json:"request_payload_hex"`
	RequestBodyHex          string `json:"request_body_hex"`
	RequestDigestHex        string `json:"request_digest_hex"`
	RequestSignatureHex     string `json:"request_signature_hex"`
	RequestMessageHex       string `json:"request_message_hex"`
	QueryBodyHex            string `json:"query_body_hex"`
	QueryDigestHex          string `json:"query_digest_hex"`
	QuerySignatureHex       string `json:"query_signature_hex"`
	QueryMessageHex         string `json:"query_message_hex"`
	AttestationBodyHex      string `json:"attestation_body_hex"`
	AttestationDigestHex    string `json:"attestation_digest_hex"`
	AttestationSignatureHex string `json:"attestation_signature_hex"`
	AttestationMessageHex   string `json:"attestation_message_hex"`
}

type diagnosticsUploadGoldenMessages struct {
	context              diagnosticsUploadVerificationContext
	appPrivate           ed25519.PrivateKey
	helperPrivate        ed25519.PrivateKey
	requestBody          []byte
	request              diagnosticsUploadMessage
	requestSignature     []byte
	queryBody            []byte
	query                diagnosticsUploadMessage
	querySignature       []byte
	attestationBody      []byte
	attestation          diagnosticsUploadMessage
	attestationSignature []byte
}

func TestDiagnosticsUploadDecision024CrossLanguageGoldenBytes(t *testing.T) {
	fixture := loadDiagnosticsUploadGoldenFixture(t)
	generated := generateDiagnosticsUploadGoldenMessages(t, fixture)
	checks := map[string][2]string{
		"request body":          {hex.EncodeToString(generated.requestBody), fixture.RequestBodyHex},
		"request digest":        {hex.EncodeToString(generated.request.digest[:]), fixture.RequestDigestHex},
		"request signature":     {hex.EncodeToString(generated.requestSignature), fixture.RequestSignatureHex},
		"request message":       {hex.EncodeToString(generated.request.canonical), fixture.RequestMessageHex},
		"query body":            {hex.EncodeToString(generated.queryBody), fixture.QueryBodyHex},
		"query digest":          {hex.EncodeToString(generated.query.digest[:]), fixture.QueryDigestHex},
		"query signature":       {hex.EncodeToString(generated.querySignature), fixture.QuerySignatureHex},
		"query message":         {hex.EncodeToString(generated.query.canonical), fixture.QueryMessageHex},
		"attestation body":      {hex.EncodeToString(generated.attestationBody), fixture.AttestationBodyHex},
		"attestation digest":    {hex.EncodeToString(generated.attestation.digest[:]), fixture.AttestationDigestHex},
		"attestation signature": {hex.EncodeToString(generated.attestationSignature), fixture.AttestationSignatureHex},
		"attestation message":   {hex.EncodeToString(generated.attestation.canonical), fixture.AttestationMessageHex},
	}
	for name, pair := range checks {
		if pair[0] != pair[1] {
			t.Fatalf("%s = %s, want %s", name, pair[0], pair[1])
		}
	}
	if err := validateDiagnosticsUploadRequestAndQuery(generated.request, generated.query); err != nil {
		t.Fatalf("request/query chain: %v", err)
	}
	if err := validateDiagnosticsUploadChain(generated.request, generated.query, generated.attestation); err != nil {
		t.Fatalf("upload chain: %v", err)
	}
}

func TestDiagnosticsUploadParserAndChainFailClosed(t *testing.T) {
	fixture := loadDiagnosticsUploadGoldenFixture(t)
	generated := generateDiagnosticsUploadGoldenMessages(t, fixture)
	for name, encoded := range map[string][]byte{
		"request":     generated.request.canonical,
		"query":       generated.query.canonical,
		"attestation": generated.attestation.canonical,
	} {
		for length := 0; length < len(encoded); length++ {
			if _, err := decodeDiagnosticsUploadMessage(encoded[:length], generated.context); err == nil {
				t.Fatalf("%s truncated to %d bytes was accepted", name, length)
			}
		}
		trailing := append(append([]byte(nil), encoded...), 0)
		if _, err := decodeDiagnosticsUploadMessage(trailing, generated.context); err == nil {
			t.Fatalf("%s with trailing input was accepted", name)
		}
	}

	requestValue := diagnosticsCBORWithoutLabels(generated.request.value, 255)
	diagnosticsUploadReplaceField(&requestValue, 15, diagnosticsCBORBstr(bytes.Repeat([]byte{0x99}, diagnosticsUploadPayloadBytes)))
	if validateDiagnosticsUploadValue(requestValue, diagnosticsUploadOperationRequest, []uint64{255}, generated.context) == nil {
		t.Fatal("payload mutation with stale digest was accepted")
	}

	wrongContext := generated.context
	wrongContext.helperPublicKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x55}, 32)).Public().(ed25519.PublicKey)
	if _, err := decodeDiagnosticsUploadMessage(generated.attestation.canonical, wrongContext); err == nil {
		t.Fatal("wrong helper key was accepted")
	}

	wrongQueryValue := diagnosticsCBORWithoutLabels(generated.query.value, 255)
	diagnosticsUploadReplaceField(&wrongQueryValue, 17, diagnosticsCBORBstr(bytes.Repeat([]byte{0xaa}, 32)))
	wrongQueryEncoded := signDiagnosticsUploadTestMessage(t, wrongQueryValue, diagnosticsUploadAttestationQuery, generated.appPrivate, generated.context)
	wrongQuery, err := decodeDiagnosticsUploadMessage(wrongQueryEncoded, generated.context)
	if err != nil {
		t.Fatal(err)
	}
	if validateDiagnosticsUploadRequestAndQuery(generated.request, wrongQuery) == nil {
		t.Fatal("wrong request digest was accepted")
	}

	wrongAttestationValue := cloneDiagnosticsCBOR(generated.attestation.value)
	diagnosticsUploadReplaceField(&wrongAttestationValue, 31, diagnosticsCBORBstr(bytes.Repeat([]byte{0xbb}, 32)))
	wrongAttestationEncoded, err := signDiagnosticsUploadAttestation(
		diagnosticsCBORWithoutLabels(wrongAttestationValue, 255), generated.helperPrivate, generated.context,
	)
	if err != nil {
		t.Fatal(err)
	}
	wrongAttestation, err := decodeDiagnosticsUploadMessage(wrongAttestationEncoded, generated.context)
	if err != nil {
		t.Fatal(err)
	}
	if validateDiagnosticsUploadChain(generated.request, generated.query, wrongAttestation) == nil {
		t.Fatal("wrong query digest was accepted")
	}
}

func TestDiagnosticsUploadClockTTLAndPayloadBounds(t *testing.T) {
	generated := generateDiagnosticsUploadGoldenMessages(t, loadDiagnosticsUploadGoldenFixture(t))
	issuedAt, _ := diagnosticsUploadUintField(generated.request.value, 12)
	expiresAt, _ := diagnosticsUploadUintField(generated.request.value, 13)
	for _, validNow := range []uint64{issuedAt - diagnosticsUploadMaximumClockSkewSeconds, issuedAt, expiresAt, expiresAt + diagnosticsUploadMaximumClockSkewSeconds} {
		if err := validateDiagnosticsUploadClock(generated.request, validNow); err != nil {
			t.Fatalf("valid clock boundary %d: %v", validNow, err)
		}
	}
	for _, invalidNow := range []uint64{issuedAt - diagnosticsUploadMaximumClockSkewSeconds - 1, expiresAt + diagnosticsUploadMaximumClockSkewSeconds + 1} {
		if err := validateDiagnosticsUploadClock(generated.request, invalidNow); err == nil {
			t.Fatalf("invalid clock boundary %d was accepted", invalidNow)
		}
	}
	for _, length := range []int{0, 1, 255, 257, diagnosticsMaximumMessageBytes} {
		value := diagnosticsCBORWithoutLabels(generated.request.value, 255)
		diagnosticsUploadReplaceField(&value, 15, diagnosticsCBORBstr(bytes.Repeat([]byte{0x44}, length)))
		if validateDiagnosticsUploadValue(value, diagnosticsUploadOperationRequest, []uint64{255}, generated.context) == nil {
			t.Fatalf("request payload length %d was accepted", length)
		}
	}

	// The app and helper clocks may differ within D024's allowed skew. Causality
	// comes from the signed digest chain, never from comparing their raw wall
	// times as if the clocks were identical.
	helperIssuedAt := issuedAt - 60
	attestationValue := diagnosticsCBORWithoutLabels(generated.attestation.value, 255)
	diagnosticsUploadReplaceField(&attestationValue, 12, diagnosticsCBORUint(helperIssuedAt))
	diagnosticsUploadReplaceField(&attestationValue, 13, diagnosticsCBORUint(helperIssuedAt+diagnosticsUploadMaximumLifetimeSeconds))
	diagnosticsUploadReplaceField(&attestationValue, 19, diagnosticsCBORUint(helperIssuedAt))
	skewedBytes, err := signDiagnosticsUploadAttestation(attestationValue, generated.helperPrivate, generated.context)
	if err != nil {
		t.Fatal(err)
	}
	skewed, err := decodeDiagnosticsUploadMessage(skewedBytes, generated.context)
	if err != nil || validateDiagnosticsUploadClock(skewed, issuedAt) != nil ||
		validateDiagnosticsUploadChain(generated.request, generated.query, skewed) != nil {
		t.Fatalf("valid cross-clock skew was rejected: decode=%v", err)
	}
}

func FuzzDiagnosticsUploadDecoder(f *testing.F) {
	fixture := loadDiagnosticsUploadGoldenFixture(f)
	generated := generateDiagnosticsUploadGoldenMessages(f, fixture)
	for _, encoded := range [][]byte{generated.request.canonical, generated.query.canonical, generated.attestation.canonical, nil, {0xff}} {
		f.Add(encoded)
	}
	f.Fuzz(func(t *testing.T, encoded []byte) {
		message, err := decodeDiagnosticsUploadMessage(encoded, generated.context)
		if err == nil {
			reencoded, encodeErr := encodeDiagnosticsCBOR(message.value)
			if encodeErr != nil || !bytes.Equal(reencoded, encoded) {
				t.Fatalf("accepted non-roundtripping input: %v", encodeErr)
			}
		}
	})
}

func generateDiagnosticsUploadGoldenMessages(t testing.TB, fixture diagnosticsUploadGoldenFixture) diagnosticsUploadGoldenMessages {
	t.Helper()
	appSeed := diagnosticsUploadMustHex(t, fixture.AppSeedHex)
	helperSeed := diagnosticsUploadMustHex(t, fixture.HelperSeedHex)
	appPrivate := ed25519.NewKeyFromSeed(appSeed)
	helperPrivate := ed25519.NewKeyFromSeed(helperSeed)
	appPublic := appPrivate.Public().(ed25519.PublicKey)
	helperPublic := helperPrivate.Public().(ed25519.PublicKey)
	context := diagnosticsUploadVerificationContext{appPublicKey: appPublic, helperPublicKey: helperPublic}
	homeserver := diagnosticsUploadMustHex(t, fixture.HomeserverBindingHex)
	folder := diagnosticsUploadMustHex(t, fixture.FolderBindingHex)
	operationID := diagnosticsUploadMustHex(t, fixture.OperationIDHex)
	requestNonce := diagnosticsUploadMustHex(t, fixture.RequestNonceHex)
	queryNonce := diagnosticsUploadMustHex(t, fixture.QueryNonceHex)
	helperNonce := diagnosticsUploadMustHex(t, fixture.HelperNonceHex)
	payload := diagnosticsUploadMustHex(t, fixture.RequestPayloadHex)
	payloadDigest := sha256.Sum256(payload)
	appKeyID := diagnosticsKeyID(appPublic)
	helperKeyID := diagnosticsKeyID(helperPublic)
	common := func(messageType uint64, issuedAt uint64) []diagnosticsCBORField {
		return []diagnosticsCBORField{
			diagnosticsCBORMapField(1, diagnosticsCBORTextValue(diagnosticsRoundtripCapabilityID)),
			diagnosticsCBORMapField(2, diagnosticsCBORUint(1)), diagnosticsCBORMapField(3, diagnosticsCBORUint(1)),
			diagnosticsCBORMapField(4, diagnosticsCBORUint(messageType)),
			diagnosticsCBORMapField(5, diagnosticsCBORBstr(homeserver)), diagnosticsCBORMapField(6, diagnosticsCBORBstr(folder)),
			diagnosticsCBORMapField(7, diagnosticsCBORBstr(appKeyID[:])), diagnosticsCBORMapField(8, diagnosticsCBORBstr(helperKeyID[:])),
			diagnosticsCBORMapField(9, diagnosticsCBORUint(fixture.AppEpoch)), diagnosticsCBORMapField(10, diagnosticsCBORUint(fixture.HelperEpoch)),
			diagnosticsCBORMapField(11, diagnosticsCBORBstr(operationID)),
			diagnosticsCBORMapField(12, diagnosticsCBORUint(issuedAt)), diagnosticsCBORMapField(13, diagnosticsCBORUint(fixture.ExpiresAt)),
		}
	}
	requestFields := common(diagnosticsUploadOperationRequest, fixture.RequestIssuedAt)
	requestFields = append(requestFields,
		diagnosticsCBORMapField(14, diagnosticsCBORBstr(requestNonce)),
		diagnosticsCBORMapField(15, diagnosticsCBORBstr(payload)),
		diagnosticsCBORMapField(16, diagnosticsCBORBstr(payloadDigest[:])),
	)
	requestEncoded := signDiagnosticsUploadTestMessage(t, diagnosticsCBORMapValue(requestFields...), diagnosticsUploadOperationRequest, appPrivate, context)
	request, err := decodeDiagnosticsUploadMessage(requestEncoded, context)
	if err != nil {
		t.Fatal(err)
	}
	requestBody, _ := encodeDiagnosticsCBOR(diagnosticsCBORWithoutLabels(request.value, 255))
	requestSignature, _ := diagnosticsUploadBytesField(request.value, 255, ed25519.SignatureSize)

	queryFields := common(diagnosticsUploadAttestationQuery, fixture.QueryIssuedAt)
	queryFields = append(queryFields,
		diagnosticsCBORMapField(17, diagnosticsCBORBstr(request.digest[:])),
		diagnosticsCBORMapField(30, diagnosticsCBORBstr(queryNonce)),
	)
	queryEncoded := signDiagnosticsUploadTestMessage(t, diagnosticsCBORMapValue(queryFields...), diagnosticsUploadAttestationQuery, appPrivate, context)
	query, err := decodeDiagnosticsUploadMessage(queryEncoded, context)
	if err != nil {
		t.Fatal(err)
	}
	queryBody, _ := encodeDiagnosticsCBOR(diagnosticsCBORWithoutLabels(query.value, 255))
	querySignature, _ := diagnosticsUploadBytesField(query.value, 255, ed25519.SignatureSize)

	attestationFields := common(diagnosticsUploadAttestation, fixture.AttestationIssuedAt)
	attestationFields = append(attestationFields,
		diagnosticsCBORMapField(16, diagnosticsCBORBstr(payloadDigest[:])),
		diagnosticsCBORMapField(17, diagnosticsCBORBstr(request.digest[:])),
		diagnosticsCBORMapField(18, diagnosticsCBORBstr(helperNonce)),
		diagnosticsCBORMapField(19, diagnosticsCBORUint(fixture.AttestationIssuedAt)),
		diagnosticsCBORMapField(30, diagnosticsCBORBstr(queryNonce)),
		diagnosticsCBORMapField(31, diagnosticsCBORBstr(query.digest[:])),
	)
	attestationEncoded, err := signDiagnosticsUploadAttestation(diagnosticsCBORMapValue(attestationFields...), helperPrivate, context)
	if err != nil {
		t.Fatal(err)
	}
	attestation, err := decodeDiagnosticsUploadMessage(attestationEncoded, context)
	if err != nil {
		t.Fatal(err)
	}
	attestationBody, _ := encodeDiagnosticsCBOR(diagnosticsCBORWithoutLabels(attestation.value, 255))
	attestationSignature, _ := diagnosticsUploadBytesField(attestation.value, 255, ed25519.SignatureSize)
	return diagnosticsUploadGoldenMessages{
		context: context, appPrivate: appPrivate, helperPrivate: helperPrivate,
		requestBody: requestBody, request: request, requestSignature: requestSignature,
		queryBody: queryBody, query: query, querySignature: querySignature,
		attestationBody: attestationBody, attestation: attestation, attestationSignature: attestationSignature,
	}
}

func signDiagnosticsUploadTestMessage(t testing.TB, value diagnosticsCBORValue, messageType uint64, privateKey ed25519.PrivateKey, context diagnosticsUploadVerificationContext) []byte {
	t.Helper()
	if err := validateDiagnosticsUploadValue(value, messageType, []uint64{255}, context); err != nil {
		t.Fatal(err)
	}
	body, err := encodeDiagnosticsCBOR(value)
	if err != nil {
		t.Fatal(err)
	}
	signed := cloneDiagnosticsCBOR(value)
	signed.fields = append(signed.fields, diagnosticsCBORMapField(255, diagnosticsCBORBstr(
		ed25519.Sign(privateKey, append([]byte(diagnosticsUploadDomains[messageType]), body...)),
	)))
	encoded, err := encodeDiagnosticsCBOR(signed)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func loadDiagnosticsUploadGoldenFixture(t testing.TB) diagnosticsUploadGoldenFixture {
	t.Helper()
	path := filepath.Join(diagnosticsTestRepoRoot(t), "ios", "VaultSyncTests", "Fixtures", "diagnostics-upload-m5.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture diagnosticsUploadGoldenFixture
	if err := json.Unmarshal(body, &fixture); err != nil || fixture.FixtureVersion != 1 || fixture.SourceDecision != "024" {
		t.Fatalf("load M5 fixture: %v", err)
	}
	return fixture
}

func diagnosticsUploadMustHex(t testing.TB, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func diagnosticsUploadReplaceField(value *diagnosticsCBORValue, label uint64, replacement diagnosticsCBORValue) {
	for index := range value.fields {
		if value.fields[index].label == label {
			value.fields[index].value = replacement
			return
		}
	}
}
