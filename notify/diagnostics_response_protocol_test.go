package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

const (
	diagnosticsResponseGoldenAuthorizationIssuedAt = uint64(1700000045)
	diagnosticsResponseGoldenResponseIssuedAt      = uint64(1700000050)
	diagnosticsResponseGoldenCleanupIssuedAt       = uint64(1700000055)
	diagnosticsResponseGoldenCleanupAckIssuedAt    = uint64(1700000060)
)

type diagnosticsResponseGoldenFixture struct {
	FixtureVersion             int      `json:"fixture_version"`
	SourceDecision             string   `json:"source_decision"`
	BaseFixture                string   `json:"base_fixture"`
	AuthorizationNonceHex      string   `json:"authorization_nonce_hex"`
	ResponseNonceHex           string   `json:"response_nonce_hex"`
	ResponsePayloadHex         string   `json:"response_payload_hex"`
	AuthorizationIssuedAt      uint64   `json:"authorization_issued_at"`
	ResponseIssuedAt           uint64   `json:"response_issued_at"`
	CleanupIssuedAt            uint64   `json:"cleanup_issued_at"`
	CleanupAckIssuedAt         uint64   `json:"cleanup_ack_issued_at"`
	AuthorizationBodyHex       string   `json:"authorization_body_hex"`
	AuthorizationDigestHex     string   `json:"authorization_digest_hex"`
	AuthorizationSignatureHex  string   `json:"authorization_signature_hex"`
	AuthorizationMessageHex    string   `json:"authorization_message_hex"`
	ResponseBodyHex            string   `json:"response_body_hex"`
	ResponseDigestHex          string   `json:"response_digest_hex"`
	ResponseSignatureHex       string   `json:"response_signature_hex"`
	ResponseMessageHex         string   `json:"response_message_hex"`
	CleanupTargetsHex          []string `json:"cleanup_targets_hex"`
	CleanupResults             []uint64 `json:"cleanup_results"`
	CleanupRequestBodyHex      string   `json:"cleanup_request_body_hex"`
	CleanupRequestDigestHex    string   `json:"cleanup_request_digest_hex"`
	CleanupRequestSignatureHex string   `json:"cleanup_request_signature_hex"`
	CleanupRequestMessageHex   string   `json:"cleanup_request_message_hex"`
	CleanupAckBodyHex          string   `json:"cleanup_ack_body_hex"`
	CleanupAckDigestHex        string   `json:"cleanup_ack_digest_hex"`
	CleanupAckSignatureHex     string   `json:"cleanup_ack_signature_hex"`
	CleanupAckMessageHex       string   `json:"cleanup_ack_message_hex"`
}

type diagnosticsResponseGoldenMessages struct {
	upload                  diagnosticsUploadGoldenMessages
	authorizationBody       []byte
	authorization           diagnosticsResponseMessage
	authorizationSignature  []byte
	responseBody            []byte
	response                diagnosticsResponseMessage
	responseSignature       []byte
	cleanupRequestBody      []byte
	cleanupRequest          diagnosticsResponseMessage
	cleanupRequestSignature []byte
	cleanupAckBody          []byte
	cleanupAck              diagnosticsResponseMessage
	cleanupAckSignature     []byte
	responseNonce           []byte
	responsePayload         []byte
	cleanupTargets          [][]byte
	cleanupResults          []uint64
}

func TestDiagnosticsResponseDecision024CrossLanguageGoldenBytes(t *testing.T) {
	fixture := loadDiagnosticsResponseGoldenFixture(t)
	generated := generateDiagnosticsResponseGoldenMessages(t, loadDiagnosticsUploadGoldenFixture(t))
	checks := map[string][2]string{
		"authorization nonce":       {hex.EncodeToString(diagnosticsResponseGoldenAuthorizationNonce()), fixture.AuthorizationNonceHex},
		"response nonce":            {hex.EncodeToString(generated.responseNonce), fixture.ResponseNonceHex},
		"response payload":          {hex.EncodeToString(generated.responsePayload), fixture.ResponsePayloadHex},
		"authorization body":        {hex.EncodeToString(generated.authorizationBody), fixture.AuthorizationBodyHex},
		"authorization digest":      {hex.EncodeToString(generated.authorization.digest[:]), fixture.AuthorizationDigestHex},
		"authorization signature":   {hex.EncodeToString(generated.authorizationSignature), fixture.AuthorizationSignatureHex},
		"authorization message":     {hex.EncodeToString(generated.authorization.canonical), fixture.AuthorizationMessageHex},
		"response body":             {hex.EncodeToString(generated.responseBody), fixture.ResponseBodyHex},
		"response digest":           {hex.EncodeToString(generated.response.digest[:]), fixture.ResponseDigestHex},
		"response signature":        {hex.EncodeToString(generated.responseSignature), fixture.ResponseSignatureHex},
		"response message":          {hex.EncodeToString(generated.response.canonical), fixture.ResponseMessageHex},
		"cleanup request body":      {hex.EncodeToString(generated.cleanupRequestBody), fixture.CleanupRequestBodyHex},
		"cleanup request digest":    {hex.EncodeToString(generated.cleanupRequest.digest[:]), fixture.CleanupRequestDigestHex},
		"cleanup request signature": {hex.EncodeToString(generated.cleanupRequestSignature), fixture.CleanupRequestSignatureHex},
		"cleanup request message":   {hex.EncodeToString(generated.cleanupRequest.canonical), fixture.CleanupRequestMessageHex},
		"cleanup ack body":          {hex.EncodeToString(generated.cleanupAckBody), fixture.CleanupAckBodyHex},
		"cleanup ack digest":        {hex.EncodeToString(generated.cleanupAck.digest[:]), fixture.CleanupAckDigestHex},
		"cleanup ack signature":     {hex.EncodeToString(generated.cleanupAckSignature), fixture.CleanupAckSignatureHex},
		"cleanup ack message":       {hex.EncodeToString(generated.cleanupAck.canonical), fixture.CleanupAckMessageHex},
	}
	for name, pair := range checks {
		if pair[0] != pair[1] {
			t.Fatalf("%s = %s, want %s", name, pair[0], pair[1])
		}
	}
	if fixture.AuthorizationIssuedAt != diagnosticsResponseGoldenAuthorizationIssuedAt ||
		fixture.ResponseIssuedAt != diagnosticsResponseGoldenResponseIssuedAt ||
		fixture.CleanupIssuedAt != diagnosticsResponseGoldenCleanupIssuedAt ||
		fixture.CleanupAckIssuedAt != diagnosticsResponseGoldenCleanupAckIssuedAt {
		t.Fatal("M6 golden timestamps changed")
	}
	if len(fixture.CleanupTargetsHex) != len(generated.cleanupTargets) || len(fixture.CleanupResults) != len(generated.cleanupResults) {
		t.Fatal("M6 cleanup golden cardinality changed")
	}
	for index := range generated.cleanupTargets {
		if fixture.CleanupTargetsHex[index] != hex.EncodeToString(generated.cleanupTargets[index]) ||
			fixture.CleanupResults[index] != generated.cleanupResults[index] {
			t.Fatalf("M6 cleanup golden item %d changed", index)
		}
	}
}

func TestDiagnosticsResponseDecision024ExactTypesAndChains(t *testing.T) {
	generated := generateDiagnosticsResponseGoldenMessages(t, loadDiagnosticsUploadGoldenFixture(t))
	if generated.authorization.messageType != diagnosticsResponseAuthorization ||
		generated.response.messageType != diagnosticsResponseArtifact ||
		generated.cleanupRequest.messageType != diagnosticsCleanupRequest ||
		generated.cleanupAck.messageType != diagnosticsCleanupAcknowledgment {
		t.Fatal("D024 response/cleanup message type registry changed")
	}
	if err := validateDiagnosticsResponseAuthorizationChain(
		generated.upload.request, generated.upload.attestation, generated.authorization,
	); err != nil {
		t.Fatalf("authorization chain: %v", err)
	}
	if err := validateDiagnosticsResponseArtifactChain(
		generated.upload.request, generated.upload.attestation, generated.authorization, generated.response,
	); err != nil {
		t.Fatalf("response chain: %v", err)
	}
	if err := validateDiagnosticsCleanupAcknowledgmentChain(generated.cleanupRequest, generated.cleanupAck); err != nil {
		t.Fatalf("cleanup chain: %v", err)
	}
	payload, _ := diagnosticsUploadBytesField(generated.response.value, 24, diagnosticsResponsePayloadBytes)
	payloadDigest, _ := diagnosticsUploadBytesField(generated.response.value, 25, sha256.Size)
	expectedPayloadDigest := sha256.Sum256(payload)
	if len(payload) != diagnosticsResponsePayloadBytes || !bytes.Equal(payload, generated.responsePayload) ||
		!bytes.Equal(payloadDigest, expectedPayloadDigest[:]) {
		t.Fatal("response payload length/digest changed")
	}
	targets, _ := diagnosticsCleanupTargets(generated.cleanupAck.value)
	results, _ := diagnosticsCleanupResults(generated.cleanupAck.value)
	if len(targets) != 3 || len(results) != 3 {
		t.Fatalf("cleanup cardinality = %d/%d", len(targets), len(results))
	}
	for index := range targets {
		if !bytes.Equal(targets[index], generated.cleanupTargets[index]) || results[index] != generated.cleanupResults[index] {
			t.Fatalf("cleanup item %d changed", index)
		}
	}
}

func TestDiagnosticsResponseParserAndCausalDigestsFailClosed(t *testing.T) {
	generated := generateDiagnosticsResponseGoldenMessages(t, loadDiagnosticsUploadGoldenFixture(t))
	for name, encoded := range map[string][]byte{
		"authorization":   generated.authorization.canonical,
		"response":        generated.response.canonical,
		"cleanup request": generated.cleanupRequest.canonical,
		"cleanup ack":     generated.cleanupAck.canonical,
	} {
		for length := 0; length < len(encoded); length++ {
			if _, err := decodeDiagnosticsResponseMessage(encoded[:length], generated.upload.context); err == nil {
				t.Fatalf("%s truncated to %d bytes was accepted", name, length)
			}
		}
		trailing := append(append([]byte(nil), encoded...), 0)
		if _, err := decodeDiagnosticsResponseMessage(trailing, generated.upload.context); err == nil {
			t.Fatalf("%s with trailing input was accepted", name)
		}
	}

	wrongContext := generated.upload.context
	wrongContext.helperPublicKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x55}, 32)).Public().(ed25519.PublicKey)
	if _, err := decodeDiagnosticsResponseMessage(generated.response.canonical, wrongContext); err == nil {
		t.Fatal("wrong helper key was accepted")
	}

	wrongAuthorizationValue := diagnosticsCBORWithoutLabels(generated.authorization.value, 255)
	diagnosticsUploadReplaceField(&wrongAuthorizationValue, 20, diagnosticsCBORBstr(bytes.Repeat([]byte{0xa0}, 32)))
	wrongAuthorizationBytes := signDiagnosticsResponseTestMessage(
		t, wrongAuthorizationValue, diagnosticsResponseAuthorization, generated.upload.appPrivate, generated.upload.context,
	)
	wrongAuthorization, err := decodeDiagnosticsResponseMessage(wrongAuthorizationBytes, generated.upload.context)
	if err != nil {
		t.Fatal(err)
	}
	if validateDiagnosticsResponseAuthorizationChain(generated.upload.request, generated.upload.attestation, wrongAuthorization) == nil {
		t.Fatal("wrong attestation digest was accepted")
	}

	wrongResponseValue := diagnosticsCBORWithoutLabels(generated.response.value, 255)
	diagnosticsUploadReplaceField(&wrongResponseValue, 22, diagnosticsCBORBstr(bytes.Repeat([]byte{0xa2}, 32)))
	wrongResponseBytes, err := signDiagnosticsHelperResponseMessage(
		wrongResponseValue, diagnosticsResponseArtifact, generated.upload.helperPrivate, generated.upload.context,
	)
	if err != nil {
		t.Fatal(err)
	}
	wrongResponse, err := decodeDiagnosticsResponseMessage(wrongResponseBytes, generated.upload.context)
	if err != nil {
		t.Fatal(err)
	}
	if validateDiagnosticsResponseArtifactChain(
		generated.upload.request, generated.upload.attestation, generated.authorization, wrongResponse,
	) == nil {
		t.Fatal("wrong authorization digest was accepted")
	}

	stalePayloadValue := diagnosticsCBORWithoutLabels(generated.response.value, 255)
	diagnosticsUploadReplaceField(&stalePayloadValue, 24, diagnosticsCBORBstr(bytes.Repeat([]byte{0x99}, diagnosticsResponsePayloadBytes)))
	if validateDiagnosticsResponseValue(
		stalePayloadValue, diagnosticsResponseArtifact, []uint64{255}, generated.upload.context,
	) == nil {
		t.Fatal("response payload mutation with stale digest was accepted")
	}

	crossDomainValue := diagnosticsCBORWithoutLabels(generated.authorization.value, 255)
	crossDomainBody, err := encodeDiagnosticsCBOR(crossDomainValue)
	if err != nil {
		t.Fatal(err)
	}
	crossDomainSigned := cloneDiagnosticsCBOR(crossDomainValue)
	crossDomainSigned.fields = append(crossDomainSigned.fields, diagnosticsCBORMapField(255, diagnosticsCBORBstr(
		ed25519.Sign(generated.upload.appPrivate, append([]byte(diagnosticsResponseDomains[diagnosticsCleanupRequest]), crossDomainBody...)),
	)))
	crossDomainEncoded, err := encodeDiagnosticsCBOR(crossDomainSigned)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeDiagnosticsResponseMessage(crossDomainEncoded, generated.upload.context); err == nil {
		t.Fatal("cross-domain authorization signature was accepted")
	}
}

func TestDiagnosticsCleanupArraysAreSortedUniqueBoundedAndExact(t *testing.T) {
	generated := generateDiagnosticsResponseGoldenMessages(t, loadDiagnosticsUploadGoldenFixture(t))
	requestValue := diagnosticsCBORWithoutLabels(generated.cleanupRequest.value, 255)
	targetValues := func(targets ...[]byte) diagnosticsCBORValue {
		values := make([]diagnosticsCBORValue, len(targets))
		for index := range targets {
			values[index] = diagnosticsCBORBstr(targets[index])
		}
		return diagnosticsCBORArrayValue(values...)
	}
	first := generated.cleanupTargets[0]
	second := generated.cleanupTargets[1]
	third := generated.cleanupTargets[2]
	invalidTargets := map[string]diagnosticsCBORValue{
		"empty":     diagnosticsCBORArrayValue(),
		"duplicate": targetValues(first, first),
		"reordered": targetValues(second, first),
		"zero":      targetValues(bytes.Repeat([]byte{0}, 32)),
		"four":      targetValues(first, second, third, bytes.Repeat([]byte{0xff}, 32)),
	}
	for name, targets := range invalidTargets {
		value := cloneDiagnosticsCBOR(requestValue)
		diagnosticsUploadReplaceField(&value, 28, targets)
		if validateDiagnosticsResponseValue(value, diagnosticsCleanupRequest, []uint64{255}, generated.upload.context) == nil {
			t.Fatalf("%s cleanup targets were accepted", name)
		}
	}

	ackValue := diagnosticsCBORWithoutLabels(generated.cleanupAck.value, 255)
	for name, results := range map[string]diagnosticsCBORValue{
		"missing": diagnosticsCBORArrayValue(diagnosticsCBORUint(1), diagnosticsCBORUint(1)),
		"unknown": diagnosticsCBORArrayValue(diagnosticsCBORUint(1), diagnosticsCBORUint(2), diagnosticsCBORUint(5)),
	} {
		value := cloneDiagnosticsCBOR(ackValue)
		diagnosticsUploadReplaceField(&value, 29, results)
		if validateDiagnosticsResponseValue(value, diagnosticsCleanupAcknowledgment, []uint64{255}, generated.upload.context) == nil {
			t.Fatalf("%s cleanup results were accepted", name)
		}
	}
}

func TestDiagnosticsResponseClockTTLAndTupleIsolation(t *testing.T) {
	generated := generateDiagnosticsResponseGoldenMessages(t, loadDiagnosticsUploadGoldenFixture(t))
	issuedAt, _ := diagnosticsUploadUintField(generated.authorization.value, 12)
	expiresAt, _ := diagnosticsUploadUintField(generated.authorization.value, 13)
	for _, validNow := range []uint64{issuedAt - diagnosticsUploadMaximumClockSkewSeconds, issuedAt, expiresAt, expiresAt + diagnosticsUploadMaximumClockSkewSeconds} {
		if err := validateDiagnosticsResponseClock(generated.authorization, validNow); err != nil {
			t.Fatalf("valid clock boundary %d: %v", validNow, err)
		}
	}
	for _, invalidNow := range []uint64{issuedAt - diagnosticsUploadMaximumClockSkewSeconds - 1, expiresAt + diagnosticsUploadMaximumClockSkewSeconds + 1} {
		if err := validateDiagnosticsResponseClock(generated.authorization, invalidNow); err == nil {
			t.Fatalf("invalid clock boundary %d was accepted", invalidNow)
		}
	}

	for _, label := range []uint64{5, 6, 11} {
		value := diagnosticsCBORWithoutLabels(generated.authorization.value, 255)
		diagnosticsUploadReplaceField(&value, label, diagnosticsCBORBstr(bytes.Repeat([]byte{byte(label + 0x80)}, 32)))
		encoded := signDiagnosticsResponseTestMessage(
			t, value, diagnosticsResponseAuthorization, generated.upload.appPrivate, generated.upload.context,
		)
		message, err := decodeDiagnosticsResponseMessage(encoded, generated.upload.context)
		if err != nil {
			t.Fatal(err)
		}
		if validateDiagnosticsResponseAuthorizationChain(generated.upload.request, generated.upload.attestation, message) == nil {
			t.Fatalf("cross-tuple label %d was accepted", label)
		}
	}
	for _, label := range []uint64{9, 10} {
		value := diagnosticsCBORWithoutLabels(generated.authorization.value, 255)
		diagnosticsUploadReplaceField(&value, label, diagnosticsCBORUint(3))
		encoded := signDiagnosticsResponseTestMessage(
			t, value, diagnosticsResponseAuthorization, generated.upload.appPrivate, generated.upload.context,
		)
		message, err := decodeDiagnosticsResponseMessage(encoded, generated.upload.context)
		if err != nil {
			t.Fatal(err)
		}
		if validateDiagnosticsResponseAuthorizationChain(generated.upload.request, generated.upload.attestation, message) == nil {
			t.Fatalf("cross-tuple epoch %d was accepted", label)
		}
	}
	for _, label := range []uint64{7, 8} {
		value := diagnosticsCBORWithoutLabels(generated.authorization.value, 255)
		diagnosticsUploadReplaceField(&value, label, diagnosticsCBORBstr(bytes.Repeat([]byte{byte(label + 0x80)}, 32)))
		if validateDiagnosticsResponseValue(value, diagnosticsResponseAuthorization, []uint64{255}, generated.upload.context) == nil {
			t.Fatalf("wrong key id label %d was accepted", label)
		}
	}
}

func FuzzDiagnosticsResponseDecoder(f *testing.F) {
	generated := generateDiagnosticsResponseGoldenMessages(f, loadDiagnosticsUploadGoldenFixture(f))
	for _, encoded := range [][]byte{
		generated.authorization.canonical, generated.response.canonical,
		generated.cleanupRequest.canonical, generated.cleanupAck.canonical, nil, {0xff},
	} {
		f.Add(encoded)
	}
	f.Fuzz(func(t *testing.T, encoded []byte) {
		message, err := decodeDiagnosticsResponseMessage(encoded, generated.upload.context)
		if err == nil {
			reencoded, encodeErr := encodeDiagnosticsCBOR(message.value)
			if encodeErr != nil || !bytes.Equal(reencoded, encoded) {
				t.Fatalf("accepted non-roundtripping input: %v", encodeErr)
			}
		}
	})
}

func generateDiagnosticsResponseGoldenMessages(t testing.TB, fixture diagnosticsUploadGoldenFixture) diagnosticsResponseGoldenMessages {
	t.Helper()
	upload := generateDiagnosticsUploadGoldenMessages(t, fixture)
	homeserver := diagnosticsUploadMustHex(t, fixture.HomeserverBindingHex)
	folder := diagnosticsUploadMustHex(t, fixture.FolderBindingHex)
	operationID := diagnosticsUploadMustHex(t, fixture.OperationIDHex)
	appKeyID := diagnosticsKeyID(upload.context.appPublicKey)
	helperKeyID := diagnosticsKeyID(upload.context.helperPublicKey)
	common := func(messageType, issuedAt uint64) []diagnosticsCBORField {
		return []diagnosticsCBORField{
			diagnosticsCBORMapField(1, diagnosticsCBORTextValue(diagnosticsRoundtripCapabilityID)),
			diagnosticsCBORMapField(2, diagnosticsCBORUint(diagnosticsProtocolMajor)),
			diagnosticsCBORMapField(3, diagnosticsCBORUint(diagnosticsCryptographicSuite)),
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

	authorizationNonce := diagnosticsResponseGoldenAuthorizationNonce()
	authorizationFields := common(diagnosticsResponseAuthorization, diagnosticsResponseGoldenAuthorizationIssuedAt)
	authorizationFields = append(authorizationFields,
		diagnosticsCBORMapField(17, diagnosticsCBORBstr(upload.request.digest[:])),
		diagnosticsCBORMapField(20, diagnosticsCBORBstr(upload.attestation.digest[:])),
		diagnosticsCBORMapField(21, diagnosticsCBORBstr(authorizationNonce)),
	)
	authorizationEncoded := signDiagnosticsResponseTestMessage(
		t, diagnosticsCBORMapValue(authorizationFields...), diagnosticsResponseAuthorization, upload.appPrivate, upload.context,
	)
	authorization, err := decodeDiagnosticsResponseMessage(authorizationEncoded, upload.context)
	if err != nil {
		t.Fatal(err)
	}
	authorizationBody, _ := encodeDiagnosticsCBOR(diagnosticsCBORWithoutLabels(authorization.value, 255))
	authorizationSignature, _ := diagnosticsUploadBytesField(authorization.value, 255, ed25519.SignatureSize)

	responseNonce := bytes.Repeat([]byte{0x76}, 32)
	responsePayload := make([]byte, diagnosticsResponsePayloadBytes)
	for index := range responsePayload {
		responsePayload[index] = byte(255 - index)
	}
	responsePayloadDigest := sha256.Sum256(responsePayload)
	responseFields := common(diagnosticsResponseArtifact, diagnosticsResponseGoldenResponseIssuedAt)
	responseFields = append(responseFields,
		diagnosticsCBORMapField(17, diagnosticsCBORBstr(upload.request.digest[:])),
		diagnosticsCBORMapField(20, diagnosticsCBORBstr(upload.attestation.digest[:])),
		diagnosticsCBORMapField(22, diagnosticsCBORBstr(authorization.digest[:])),
		diagnosticsCBORMapField(23, diagnosticsCBORBstr(responseNonce)),
		diagnosticsCBORMapField(24, diagnosticsCBORBstr(responsePayload)),
		diagnosticsCBORMapField(25, diagnosticsCBORBstr(responsePayloadDigest[:])),
	)
	responseEncoded, err := signDiagnosticsHelperResponseMessage(
		diagnosticsCBORMapValue(responseFields...), diagnosticsResponseArtifact, upload.helperPrivate, upload.context,
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := decodeDiagnosticsResponseMessage(responseEncoded, upload.context)
	if err != nil {
		t.Fatal(err)
	}
	responseBody, _ := encodeDiagnosticsCBOR(diagnosticsCBORWithoutLabels(response.value, 255))
	responseSignature, _ := diagnosticsUploadBytesField(response.value, 255, ed25519.SignatureSize)

	cleanupTargets := [][]byte{
		append([]byte(nil), upload.request.digest[:]...),
		append([]byte(nil), upload.attestation.digest[:]...),
		append([]byte(nil), response.digest[:]...),
	}
	sort.Slice(cleanupTargets, func(i, j int) bool { return bytes.Compare(cleanupTargets[i], cleanupTargets[j]) < 0 })
	targetValues := make([]diagnosticsCBORValue, len(cleanupTargets))
	for index := range cleanupTargets {
		targetValues[index] = diagnosticsCBORBstr(cleanupTargets[index])
	}
	cleanupRequestFields := common(diagnosticsCleanupRequest, diagnosticsResponseGoldenCleanupIssuedAt)
	cleanupRequestFields = append(cleanupRequestFields,
		diagnosticsCBORMapField(28, diagnosticsCBORArrayValue(targetValues...)),
	)
	cleanupRequestEncoded := signDiagnosticsResponseTestMessage(
		t, diagnosticsCBORMapValue(cleanupRequestFields...), diagnosticsCleanupRequest, upload.appPrivate, upload.context,
	)
	cleanupRequest, err := decodeDiagnosticsResponseMessage(cleanupRequestEncoded, upload.context)
	if err != nil {
		t.Fatal(err)
	}
	cleanupRequestBody, _ := encodeDiagnosticsCBOR(diagnosticsCBORWithoutLabels(cleanupRequest.value, 255))
	cleanupRequestSignature, _ := diagnosticsUploadBytesField(cleanupRequest.value, 255, ed25519.SignatureSize)

	cleanupResults := make([]uint64, len(cleanupTargets))
	resultValues := make([]diagnosticsCBORValue, len(cleanupTargets))
	for index, target := range cleanupTargets {
		cleanupResults[index] = diagnosticsCleanupDeleted
		if bytes.Equal(target, upload.request.digest[:]) {
			cleanupResults[index] = diagnosticsCleanupRetainedConflict
		}
		resultValues[index] = diagnosticsCBORUint(cleanupResults[index])
	}
	cleanupAckFields := common(diagnosticsCleanupAcknowledgment, diagnosticsResponseGoldenCleanupAckIssuedAt)
	cleanupAckFields = append(cleanupAckFields,
		diagnosticsCBORMapField(28, diagnosticsCBORArrayValue(targetValues...)),
		diagnosticsCBORMapField(29, diagnosticsCBORArrayValue(resultValues...)),
		diagnosticsCBORMapField(31, diagnosticsCBORBstr(cleanupRequest.digest[:])),
	)
	cleanupAckEncoded, err := signDiagnosticsHelperResponseMessage(
		diagnosticsCBORMapValue(cleanupAckFields...), diagnosticsCleanupAcknowledgment, upload.helperPrivate, upload.context,
	)
	if err != nil {
		t.Fatal(err)
	}
	cleanupAck, err := decodeDiagnosticsResponseMessage(cleanupAckEncoded, upload.context)
	if err != nil {
		t.Fatal(err)
	}
	cleanupAckBody, _ := encodeDiagnosticsCBOR(diagnosticsCBORWithoutLabels(cleanupAck.value, 255))
	cleanupAckSignature, _ := diagnosticsUploadBytesField(cleanupAck.value, 255, ed25519.SignatureSize)
	return diagnosticsResponseGoldenMessages{
		upload:            upload,
		authorizationBody: authorizationBody, authorization: authorization, authorizationSignature: authorizationSignature,
		responseBody: responseBody, response: response, responseSignature: responseSignature,
		cleanupRequestBody: cleanupRequestBody, cleanupRequest: cleanupRequest, cleanupRequestSignature: cleanupRequestSignature,
		cleanupAckBody: cleanupAckBody, cleanupAck: cleanupAck, cleanupAckSignature: cleanupAckSignature,
		responseNonce: responseNonce, responsePayload: responsePayload,
		cleanupTargets: cleanupTargets, cleanupResults: cleanupResults,
	}
}

func signDiagnosticsResponseTestMessage(
	t testing.TB,
	value diagnosticsCBORValue,
	messageType uint64,
	privateKey ed25519.PrivateKey,
	context diagnosticsUploadVerificationContext,
) []byte {
	t.Helper()
	if err := validateDiagnosticsResponseValue(value, messageType, []uint64{255}, context); err != nil {
		t.Fatal(err)
	}
	body, err := encodeDiagnosticsCBOR(value)
	if err != nil {
		t.Fatal(err)
	}
	signed := cloneDiagnosticsCBOR(value)
	signed.fields = append(signed.fields, diagnosticsCBORMapField(255, diagnosticsCBORBstr(
		ed25519.Sign(privateKey, append([]byte(diagnosticsResponseDomains[messageType]), body...)),
	)))
	encoded, err := encodeDiagnosticsCBOR(signed)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func diagnosticsResponseGoldenAuthorizationNonce() []byte {
	return bytes.Repeat([]byte{0x75}, 32)
}

func loadDiagnosticsResponseGoldenFixture(t testing.TB) diagnosticsResponseGoldenFixture {
	t.Helper()
	path := filepath.Join(diagnosticsTestRepoRoot(t), "ios", "VaultSyncTests", "Fixtures", "diagnostics-response-m6.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture diagnosticsResponseGoldenFixture
	if err := json.Unmarshal(body, &fixture); err != nil || fixture.FixtureVersion != 1 || fixture.SourceDecision != "024" ||
		fixture.BaseFixture != "diagnostics-upload-m5.json" {
		t.Fatalf("load M6 fixture: %v", err)
	}
	return fixture
}
