package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"sort"
)

const (
	diagnosticsUploadOperationRequest uint64 = 3
	diagnosticsUploadAttestationQuery uint64 = 4
	diagnosticsUploadAttestation      uint64 = 5

	diagnosticsUploadMaximumLifetimeSeconds  = uint64(600)
	diagnosticsUploadMaximumClockSkewSeconds = uint64(120)
	diagnosticsUploadPayloadBytes            = 256
)

var (
	errDiagnosticsUploadInvalid = errors.New("invalid diagnostics upload message")
	diagnosticsUploadDomains    = map[uint64]string{
		diagnosticsUploadOperationRequest: "eu.vaultsync.roundtrip/v1/operation-request\x00",
		diagnosticsUploadAttestationQuery: "eu.vaultsync.roundtrip/v1/attestation-query\x00",
		diagnosticsUploadAttestation:      "eu.vaultsync.roundtrip/v1/upload-attestation\x00",
	}
)

type diagnosticsUploadVerificationContext struct {
	appPublicKey    ed25519.PublicKey
	helperPublicKey ed25519.PublicKey
}

type diagnosticsUploadMessage struct {
	messageType uint64
	value       diagnosticsCBORValue
	canonical   []byte
	digest      [32]byte
}

func decodeDiagnosticsUploadMessage(data []byte, context diagnosticsUploadVerificationContext) (diagnosticsUploadMessage, error) {
	value, err := decodeDiagnosticsCBOR(data)
	if err != nil || value.kind != diagnosticsCBORMap ||
		len(context.appPublicKey) != ed25519.PublicKeySize || len(context.helperPublicKey) != ed25519.PublicKeySize {
		return diagnosticsUploadMessage{}, errDiagnosticsUploadInvalid
	}
	messageType, ok := diagnosticsUploadUintField(value, 4)
	if !ok || (messageType != diagnosticsUploadOperationRequest &&
		messageType != diagnosticsUploadAttestationQuery && messageType != diagnosticsUploadAttestation) {
		return diagnosticsUploadMessage{}, errDiagnosticsUploadInvalid
	}
	if err := validateDiagnosticsUploadValue(value, messageType, nil, context); err != nil {
		return diagnosticsUploadMessage{}, err
	}
	domain := diagnosticsUploadDomains[messageType]
	signature, _ := diagnosticsUploadBytesField(value, 255, ed25519.SignatureSize)
	publicKey := context.appPublicKey
	if messageType == diagnosticsUploadAttestation {
		publicKey = context.helperPublicKey
	}
	body, err := encodeDiagnosticsCBOR(diagnosticsCBORWithoutLabels(value, 255))
	if err != nil || !ed25519.Verify(publicKey, append([]byte(domain), body...), signature) {
		return diagnosticsUploadMessage{}, errDiagnosticsUploadInvalid
	}
	return diagnosticsUploadMessage{
		messageType: messageType,
		value:       value,
		canonical:   append([]byte(nil), data...),
		digest:      diagnosticsDomainSHA256(domain, body),
	}, nil
}

func signDiagnosticsUploadAttestation(value diagnosticsCBORValue, helperPrivateKey ed25519.PrivateKey, context diagnosticsUploadVerificationContext) ([]byte, error) {
	if len(helperPrivateKey) != ed25519.PrivateKeySize ||
		!bytes.Equal(helperPrivateKey.Public().(ed25519.PublicKey), context.helperPublicKey) {
		return nil, errDiagnosticsUploadInvalid
	}
	if err := validateDiagnosticsUploadValue(value, diagnosticsUploadAttestation, []uint64{255}, context); err != nil {
		return nil, err
	}
	body, err := encodeDiagnosticsCBOR(value)
	if err != nil {
		return nil, errDiagnosticsUploadInvalid
	}
	signed := cloneDiagnosticsCBOR(value)
	signed.fields = append(signed.fields, diagnosticsCBORMapField(255, diagnosticsCBORBstr(
		ed25519.Sign(helperPrivateKey, append([]byte(diagnosticsUploadDomains[diagnosticsUploadAttestation]), body...)),
	)))
	encoded, err := encodeDiagnosticsCBOR(signed)
	if err != nil {
		return nil, errDiagnosticsUploadInvalid
	}
	if _, err := decodeDiagnosticsUploadMessage(encoded, context); err != nil {
		return nil, err
	}
	return encoded, nil
}

func validateDiagnosticsUploadValue(value diagnosticsCBORValue, messageType uint64, omitted []uint64, context diagnosticsUploadVerificationContext) error {
	if value.kind != diagnosticsCBORMap {
		return errDiagnosticsUploadInvalid
	}
	expected := diagnosticsUploadExpectedLabels(messageType)
	if expected == nil {
		return errDiagnosticsUploadInvalid
	}
	removed := make(map[uint64]struct{}, len(omitted))
	for _, label := range omitted {
		removed[label] = struct{}{}
	}
	wanted := make([]uint64, 0, len(expected))
	for _, label := range expected {
		if _, ok := removed[label]; !ok {
			wanted = append(wanted, label)
		}
	}
	if len(value.fields) != len(wanted) {
		return errDiagnosticsUploadInvalid
	}
	fields := append([]diagnosticsCBORField(nil), value.fields...)
	sort.Slice(fields, func(i, j int) bool { return fields[i].label < fields[j].label })
	for index, label := range wanted {
		if fields[index].label != label || !diagnosticsUploadFieldValid(label, fields[index].value) ||
			(index > 0 && fields[index-1].label == fields[index].label) {
			return errDiagnosticsUploadInvalid
		}
	}
	capability, _ := diagnosticsCBORLookup(value, 1)
	protocol, _ := diagnosticsUploadUintField(value, 2)
	suite, _ := diagnosticsUploadUintField(value, 3)
	actualType, _ := diagnosticsUploadUintField(value, 4)
	if capability.text != diagnosticsRoundtripCapabilityID || protocol != diagnosticsProtocolMajor ||
		suite != diagnosticsCryptographicSuite || actualType != messageType {
		return errDiagnosticsUploadInvalid
	}
	issuedAt, _ := diagnosticsUploadUintField(value, 12)
	expiresAt, _ := diagnosticsUploadUintField(value, 13)
	if issuedAt == 0 || expiresAt <= issuedAt || expiresAt-issuedAt > diagnosticsUploadMaximumLifetimeSeconds {
		return errDiagnosticsUploadInvalid
	}
	appEpoch, _ := diagnosticsUploadUintField(value, 9)
	helperEpoch, _ := diagnosticsUploadUintField(value, 10)
	if appEpoch == 0 || helperEpoch == 0 ||
		!diagnosticsUploadNonzeroBytes(value, 5, 32) || !diagnosticsUploadNonzeroBytes(value, 6, 32) ||
		!diagnosticsUploadNonzeroBytes(value, 11, 32) {
		return errDiagnosticsUploadInvalid
	}
	appKeyID, _ := diagnosticsUploadBytesField(value, 7, 32)
	helperKeyID, _ := diagnosticsUploadBytesField(value, 8, 32)
	derivedAppKeyID := diagnosticsKeyID(context.appPublicKey)
	derivedHelperKeyID := diagnosticsKeyID(context.helperPublicKey)
	if !bytes.Equal(appKeyID, derivedAppKeyID[:]) || !bytes.Equal(helperKeyID, derivedHelperKeyID[:]) {
		return errDiagnosticsUploadInvalid
	}
	if messageType == diagnosticsUploadOperationRequest {
		payload, _ := diagnosticsUploadBytesField(value, 15, diagnosticsUploadPayloadBytes)
		payloadDigest, _ := diagnosticsUploadBytesField(value, 16, sha256.Size)
		calculated := sha256.Sum256(payload)
		if !bytes.Equal(payloadDigest, calculated[:]) || !diagnosticsUploadNonzeroBytes(value, 14, 32) {
			return errDiagnosticsUploadInvalid
		}
	}
	if messageType == diagnosticsUploadAttestationQuery && !diagnosticsUploadNonzeroBytes(value, 30, 32) {
		return errDiagnosticsUploadInvalid
	}
	if messageType == diagnosticsUploadAttestation {
		observedAt, _ := diagnosticsUploadUintField(value, 19)
		if observedAt == 0 || observedAt > issuedAt || !diagnosticsUploadNonzeroBytes(value, 18, 32) ||
			!diagnosticsUploadNonzeroBytes(value, 30, 32) {
			return errDiagnosticsUploadInvalid
		}
	}
	return nil
}

func diagnosticsUploadExpectedLabels(messageType uint64) []uint64 {
	switch messageType {
	case diagnosticsUploadOperationRequest:
		return []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 255}
	case diagnosticsUploadAttestationQuery:
		return []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 17, 30, 255}
	case diagnosticsUploadAttestation:
		return []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 16, 17, 18, 19, 30, 31, 255}
	default:
		return nil
	}
}

func diagnosticsUploadFieldValid(label uint64, value diagnosticsCBORValue) bool {
	switch label {
	case 1:
		return value.kind == diagnosticsCBORText && value.text == diagnosticsRoundtripCapabilityID
	case 2, 3:
		return value.kind == diagnosticsCBORUnsigned && value.unsigned == 1
	case 4, 9, 10, 12, 13, 19:
		return value.kind == diagnosticsCBORUnsigned
	case 5, 6, 7, 8, 11, 14, 16, 17, 18, 30, 31:
		return value.kind == diagnosticsCBORBytes && len(value.bytes) == 32
	case 15:
		return value.kind == diagnosticsCBORBytes && len(value.bytes) == diagnosticsUploadPayloadBytes
	case 255:
		return value.kind == diagnosticsCBORBytes && len(value.bytes) == ed25519.SignatureSize
	default:
		return false
	}
}

func diagnosticsUploadBytesField(value diagnosticsCBORValue, label uint64, length int) ([]byte, bool) {
	field, ok := diagnosticsCBORLookup(value, label)
	if !ok || field.kind != diagnosticsCBORBytes || len(field.bytes) != length {
		return nil, false
	}
	return append([]byte(nil), field.bytes...), true
}

func diagnosticsUploadUintField(value diagnosticsCBORValue, label uint64) (uint64, bool) {
	field, ok := diagnosticsCBORLookup(value, label)
	if !ok || field.kind != diagnosticsCBORUnsigned {
		return 0, false
	}
	return field.unsigned, true
}

func diagnosticsUploadNonzeroBytes(value diagnosticsCBORValue, label uint64, length int) bool {
	field, ok := diagnosticsUploadBytesField(value, label, length)
	if !ok {
		return false
	}
	var combined byte
	for _, item := range field {
		combined |= item
	}
	return combined != 0
}

func validateDiagnosticsUploadClock(message diagnosticsUploadMessage, now uint64) error {
	issuedAt, _ := diagnosticsUploadUintField(message.value, 12)
	expiresAt, _ := diagnosticsUploadUintField(message.value, 13)
	if issuedAt > now && issuedAt-now > diagnosticsUploadMaximumClockSkewSeconds {
		return errDiagnosticsUploadInvalid
	}
	if now > expiresAt && now-expiresAt > diagnosticsUploadMaximumClockSkewSeconds {
		return errDiagnosticsUploadInvalid
	}
	return nil
}

func validateDiagnosticsUploadRequestAndQuery(request, query diagnosticsUploadMessage) error {
	if request.messageType != diagnosticsUploadOperationRequest || query.messageType != diagnosticsUploadAttestationQuery ||
		!diagnosticsUploadFieldsEqual(request, query, 1, 2, 3, 5, 6, 7, 8, 9, 10, 11) {
		return errDiagnosticsUploadInvalid
	}
	requestDigest, _ := diagnosticsUploadBytesField(query.value, 17, 32)
	requestIssuedAt, _ := diagnosticsUploadUintField(request.value, 12)
	requestExpiresAt, _ := diagnosticsUploadUintField(request.value, 13)
	queryIssuedAt, _ := diagnosticsUploadUintField(query.value, 12)
	queryExpiresAt, _ := diagnosticsUploadUintField(query.value, 13)
	if !bytes.Equal(requestDigest, request.digest[:]) || queryIssuedAt < requestIssuedAt ||
		queryExpiresAt > requestExpiresAt {
		return errDiagnosticsUploadInvalid
	}
	return nil
}

func validateDiagnosticsUploadChain(request, query, attestation diagnosticsUploadMessage) error {
	if err := validateDiagnosticsUploadRequestAndQuery(request, query); err != nil ||
		attestation.messageType != diagnosticsUploadAttestation ||
		!diagnosticsUploadFieldsEqual(request, attestation, 1, 2, 3, 5, 6, 7, 8, 9, 10, 11) {
		return errDiagnosticsUploadInvalid
	}
	requestPayloadDigest, _ := diagnosticsUploadBytesField(request.value, 16, 32)
	attestedPayloadDigest, _ := diagnosticsUploadBytesField(attestation.value, 16, 32)
	attestedRequestDigest, _ := diagnosticsUploadBytesField(attestation.value, 17, 32)
	queryNonce, _ := diagnosticsUploadBytesField(query.value, 30, 32)
	attestedQueryNonce, _ := diagnosticsUploadBytesField(attestation.value, 30, 32)
	attestedQueryDigest, _ := diagnosticsUploadBytesField(attestation.value, 31, 32)
	requestExpiresAt, _ := diagnosticsUploadUintField(request.value, 13)
	queryExpiresAt, _ := diagnosticsUploadUintField(query.value, 13)
	attestationExpiresAt, _ := diagnosticsUploadUintField(attestation.value, 13)
	if !bytes.Equal(requestPayloadDigest, attestedPayloadDigest) ||
		!bytes.Equal(request.digest[:], attestedRequestDigest) ||
		!bytes.Equal(queryNonce, attestedQueryNonce) || !bytes.Equal(query.digest[:], attestedQueryDigest) ||
		attestationExpiresAt > requestExpiresAt || attestationExpiresAt > queryExpiresAt {
		return errDiagnosticsUploadInvalid
	}
	return nil
}

func diagnosticsUploadFieldsEqual(left, right diagnosticsUploadMessage, labels ...uint64) bool {
	for _, label := range labels {
		leftValue, leftOK := diagnosticsCBORLookup(left.value, label)
		rightValue, rightOK := diagnosticsCBORLookup(right.value, label)
		if !leftOK || !rightOK || !diagnosticsCBORValuesEqual(leftValue, rightValue) {
			return false
		}
	}
	return true
}
