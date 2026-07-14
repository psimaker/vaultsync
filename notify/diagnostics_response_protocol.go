package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"sort"
)

const (
	diagnosticsResponseAuthorization uint64 = 6
	diagnosticsResponseArtifact      uint64 = 7
	diagnosticsCleanupRequest        uint64 = 8
	diagnosticsCleanupAcknowledgment uint64 = 9

	diagnosticsResponsePayloadBytes  = 256
	diagnosticsCleanupMinimumTargets = 1
	diagnosticsCleanupMaximumTargets = 3

	diagnosticsCleanupDeleted          uint64 = 1
	diagnosticsCleanupAlreadyAbsent    uint64 = 2
	diagnosticsCleanupRetainedConflict uint64 = 3
	diagnosticsCleanupFailed           uint64 = 4
)

var (
	errDiagnosticsResponseInvalid = errors.New("invalid diagnostics response message")
	diagnosticsResponseDomains    = map[uint64]string{
		diagnosticsResponseAuthorization: "eu.vaultsync.roundtrip/v1/response-authorization\x00",
		diagnosticsResponseArtifact:      "eu.vaultsync.roundtrip/v1/response-artifact\x00",
		diagnosticsCleanupRequest:        "eu.vaultsync.roundtrip/v1/cleanup-request\x00",
		diagnosticsCleanupAcknowledgment: "eu.vaultsync.roundtrip/v1/cleanup-ack\x00",
	}
)

type diagnosticsResponseMessage struct {
	messageType uint64
	value       diagnosticsCBORValue
	canonical   []byte
	digest      [32]byte
}

func decodeDiagnosticsResponseMessage(data []byte, context diagnosticsUploadVerificationContext) (diagnosticsResponseMessage, error) {
	value, err := decodeDiagnosticsCBOR(data)
	if err != nil || value.kind != diagnosticsCBORMap ||
		len(context.appPublicKey) != ed25519.PublicKeySize || len(context.helperPublicKey) != ed25519.PublicKeySize {
		return diagnosticsResponseMessage{}, errDiagnosticsResponseInvalid
	}
	messageType, ok := diagnosticsUploadUintField(value, 4)
	if !ok || messageType < diagnosticsResponseAuthorization || messageType > diagnosticsCleanupAcknowledgment {
		return diagnosticsResponseMessage{}, errDiagnosticsResponseInvalid
	}
	if err := validateDiagnosticsResponseValue(value, messageType, nil, context); err != nil {
		return diagnosticsResponseMessage{}, err
	}
	domain := diagnosticsResponseDomains[messageType]
	signature, _ := diagnosticsUploadBytesField(value, 255, ed25519.SignatureSize)
	publicKey := context.appPublicKey
	if messageType == diagnosticsResponseArtifact || messageType == diagnosticsCleanupAcknowledgment {
		publicKey = context.helperPublicKey
	}
	body, err := encodeDiagnosticsCBOR(diagnosticsCBORWithoutLabels(value, 255))
	if err != nil || !ed25519.Verify(publicKey, append([]byte(domain), body...), signature) {
		return diagnosticsResponseMessage{}, errDiagnosticsResponseInvalid
	}
	return diagnosticsResponseMessage{
		messageType: messageType,
		value:       value,
		canonical:   append([]byte(nil), data...),
		digest:      diagnosticsDomainSHA256(domain, body),
	}, nil
}

func signDiagnosticsHelperResponseMessage(
	value diagnosticsCBORValue,
	messageType uint64,
	helperPrivateKey ed25519.PrivateKey,
	context diagnosticsUploadVerificationContext,
) ([]byte, error) {
	if (messageType != diagnosticsResponseArtifact && messageType != diagnosticsCleanupAcknowledgment) ||
		len(helperPrivateKey) != ed25519.PrivateKeySize ||
		!bytes.Equal(helperPrivateKey.Public().(ed25519.PublicKey), context.helperPublicKey) {
		return nil, errDiagnosticsResponseInvalid
	}
	if err := validateDiagnosticsResponseValue(value, messageType, []uint64{255}, context); err != nil {
		return nil, err
	}
	body, err := encodeDiagnosticsCBOR(value)
	if err != nil {
		return nil, errDiagnosticsResponseInvalid
	}
	signed := cloneDiagnosticsCBOR(value)
	signed.fields = append(signed.fields, diagnosticsCBORMapField(255, diagnosticsCBORBstr(
		ed25519.Sign(helperPrivateKey, append([]byte(diagnosticsResponseDomains[messageType]), body...)),
	)))
	encoded, err := encodeDiagnosticsCBOR(signed)
	if err != nil {
		return nil, errDiagnosticsResponseInvalid
	}
	if _, err := decodeDiagnosticsResponseMessage(encoded, context); err != nil {
		return nil, err
	}
	return encoded, nil
}

func validateDiagnosticsResponseValue(
	value diagnosticsCBORValue,
	messageType uint64,
	omitted []uint64,
	context diagnosticsUploadVerificationContext,
) error {
	if value.kind != diagnosticsCBORMap {
		return errDiagnosticsResponseInvalid
	}
	expected := diagnosticsResponseExpectedLabels(messageType)
	if expected == nil {
		return errDiagnosticsResponseInvalid
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
		return errDiagnosticsResponseInvalid
	}
	fields := append([]diagnosticsCBORField(nil), value.fields...)
	sort.Slice(fields, func(i, j int) bool { return fields[i].label < fields[j].label })
	for index, label := range wanted {
		if fields[index].label != label || !diagnosticsResponseFieldValid(label, fields[index].value) ||
			(index > 0 && fields[index-1].label == fields[index].label) {
			return errDiagnosticsResponseInvalid
		}
	}
	capability, _ := diagnosticsCBORLookup(value, 1)
	protocol, _ := diagnosticsUploadUintField(value, 2)
	suite, _ := diagnosticsUploadUintField(value, 3)
	actualType, _ := diagnosticsUploadUintField(value, 4)
	if capability.text != diagnosticsRoundtripCapabilityID || protocol != diagnosticsProtocolMajor ||
		suite != diagnosticsCryptographicSuite || actualType != messageType {
		return errDiagnosticsResponseInvalid
	}
	issuedAt, _ := diagnosticsUploadUintField(value, 12)
	expiresAt, _ := diagnosticsUploadUintField(value, 13)
	appEpoch, _ := diagnosticsUploadUintField(value, 9)
	helperEpoch, _ := diagnosticsUploadUintField(value, 10)
	if issuedAt == 0 || expiresAt <= issuedAt || expiresAt-issuedAt > diagnosticsUploadMaximumLifetimeSeconds ||
		appEpoch == 0 || helperEpoch == 0 ||
		!diagnosticsUploadNonzeroBytes(value, 5, 32) || !diagnosticsUploadNonzeroBytes(value, 6, 32) ||
		!diagnosticsUploadNonzeroBytes(value, 11, 32) {
		return errDiagnosticsResponseInvalid
	}
	appKeyID, _ := diagnosticsUploadBytesField(value, 7, 32)
	helperKeyID, _ := diagnosticsUploadBytesField(value, 8, 32)
	derivedAppKeyID := diagnosticsKeyID(context.appPublicKey)
	derivedHelperKeyID := diagnosticsKeyID(context.helperPublicKey)
	if !bytes.Equal(appKeyID, derivedAppKeyID[:]) || !bytes.Equal(helperKeyID, derivedHelperKeyID[:]) {
		return errDiagnosticsResponseInvalid
	}

	switch messageType {
	case diagnosticsResponseAuthorization:
		if !diagnosticsUploadNonzeroBytes(value, 17, 32) || !diagnosticsUploadNonzeroBytes(value, 20, 32) ||
			!diagnosticsUploadNonzeroBytes(value, 21, 32) {
			return errDiagnosticsResponseInvalid
		}
	case diagnosticsResponseArtifact:
		payload, _ := diagnosticsUploadBytesField(value, 24, diagnosticsResponsePayloadBytes)
		payloadDigest, _ := diagnosticsUploadBytesField(value, 25, sha256.Size)
		calculated := sha256.Sum256(payload)
		if !bytes.Equal(payloadDigest, calculated[:]) || !diagnosticsUploadNonzeroBytes(value, 17, 32) ||
			!diagnosticsUploadNonzeroBytes(value, 20, 32) || !diagnosticsUploadNonzeroBytes(value, 22, 32) ||
			!diagnosticsUploadNonzeroBytes(value, 23, 32) {
			return errDiagnosticsResponseInvalid
		}
	case diagnosticsCleanupRequest:
		if _, ok := diagnosticsCleanupTargets(value); !ok {
			return errDiagnosticsResponseInvalid
		}
	case diagnosticsCleanupAcknowledgment:
		targets, targetsOK := diagnosticsCleanupTargets(value)
		results, resultsOK := diagnosticsCleanupResults(value)
		if !targetsOK || !resultsOK || len(targets) != len(results) ||
			!diagnosticsUploadNonzeroBytes(value, 31, 32) {
			return errDiagnosticsResponseInvalid
		}
	default:
		return errDiagnosticsResponseInvalid
	}
	return nil
}

func diagnosticsResponseExpectedLabels(messageType uint64) []uint64 {
	switch messageType {
	case diagnosticsResponseAuthorization:
		return []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 17, 20, 21, 255}
	case diagnosticsResponseArtifact:
		return []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 17, 20, 22, 23, 24, 25, 255}
	case diagnosticsCleanupRequest:
		return []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 28, 255}
	case diagnosticsCleanupAcknowledgment:
		return []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 28, 29, 31, 255}
	default:
		return nil
	}
}

func diagnosticsResponseFieldValid(label uint64, value diagnosticsCBORValue) bool {
	switch label {
	case 1:
		return value.kind == diagnosticsCBORText && value.text == diagnosticsRoundtripCapabilityID
	case 2, 3:
		return value.kind == diagnosticsCBORUnsigned && value.unsigned == 1
	case 4, 9, 10, 12, 13:
		return value.kind == diagnosticsCBORUnsigned
	case 5, 6, 7, 8, 11, 17, 20, 21, 22, 23, 25, 31:
		return value.kind == diagnosticsCBORBytes && len(value.bytes) == 32
	case 24:
		return value.kind == diagnosticsCBORBytes && len(value.bytes) == diagnosticsResponsePayloadBytes
	case 28:
		return diagnosticsCleanupTargetArrayValid(value)
	case 29:
		return diagnosticsCleanupResultArrayValid(value)
	case 255:
		return value.kind == diagnosticsCBORBytes && len(value.bytes) == ed25519.SignatureSize
	default:
		return false
	}
}

func diagnosticsCleanupTargetArrayValid(value diagnosticsCBORValue) bool {
	if value.kind != diagnosticsCBORArray || len(value.array) < diagnosticsCleanupMinimumTargets ||
		len(value.array) > diagnosticsCleanupMaximumTargets {
		return false
	}
	var prior []byte
	for _, item := range value.array {
		if item.kind != diagnosticsCBORBytes || len(item.bytes) != sha256.Size || !nonzeroDiagnosticsBytes(item.bytes) ||
			(prior != nil && bytes.Compare(prior, item.bytes) >= 0) {
			return false
		}
		prior = item.bytes
	}
	return true
}

func diagnosticsCleanupResultArrayValid(value diagnosticsCBORValue) bool {
	if value.kind != diagnosticsCBORArray || len(value.array) < diagnosticsCleanupMinimumTargets ||
		len(value.array) > diagnosticsCleanupMaximumTargets {
		return false
	}
	for _, item := range value.array {
		if item.kind != diagnosticsCBORUnsigned || item.unsigned < diagnosticsCleanupDeleted || item.unsigned > diagnosticsCleanupFailed {
			return false
		}
	}
	return true
}

func diagnosticsCleanupTargets(value diagnosticsCBORValue) ([][]byte, bool) {
	field, ok := diagnosticsCBORLookup(value, 28)
	if !ok || !diagnosticsCleanupTargetArrayValid(field) {
		return nil, false
	}
	targets := make([][]byte, len(field.array))
	for index := range field.array {
		targets[index] = append([]byte(nil), field.array[index].bytes...)
	}
	return targets, true
}

func diagnosticsCleanupResults(value diagnosticsCBORValue) ([]uint64, bool) {
	field, ok := diagnosticsCBORLookup(value, 29)
	if !ok || !diagnosticsCleanupResultArrayValid(field) {
		return nil, false
	}
	results := make([]uint64, len(field.array))
	for index := range field.array {
		results[index] = field.array[index].unsigned
	}
	return results, true
}

func validateDiagnosticsResponseClock(message diagnosticsResponseMessage, now uint64) error {
	issuedAt, _ := diagnosticsUploadUintField(message.value, 12)
	expiresAt, _ := diagnosticsUploadUintField(message.value, 13)
	if issuedAt > now && issuedAt-now > diagnosticsUploadMaximumClockSkewSeconds {
		return errDiagnosticsResponseInvalid
	}
	if now > expiresAt && now-expiresAt > diagnosticsUploadMaximumClockSkewSeconds {
		return errDiagnosticsResponseInvalid
	}
	return nil
}

func validateDiagnosticsResponseAuthorizationChain(
	request, attestation diagnosticsUploadMessage,
	authorization diagnosticsResponseMessage,
) error {
	if request.messageType != diagnosticsUploadOperationRequest || attestation.messageType != diagnosticsUploadAttestation ||
		authorization.messageType != diagnosticsResponseAuthorization ||
		!diagnosticsUploadFieldsEqual(request, attestation, 1, 2, 3, 5, 6, 7, 8, 9, 10, 11) ||
		!diagnosticsUploadAndResponseFieldsEqual(request, authorization, 1, 2, 3, 5, 6, 7, 8, 9, 10, 11) {
		return errDiagnosticsResponseInvalid
	}
	requestPayloadDigest, _ := diagnosticsUploadBytesField(request.value, 16, 32)
	attestedPayloadDigest, _ := diagnosticsUploadBytesField(attestation.value, 16, 32)
	attestedRequestDigest, _ := diagnosticsUploadBytesField(attestation.value, 17, 32)
	authorizedRequestDigest, _ := diagnosticsUploadBytesField(authorization.value, 17, 32)
	authorizedAttestationDigest, _ := diagnosticsUploadBytesField(authorization.value, 20, 32)
	requestIssuedAt, _ := diagnosticsUploadUintField(request.value, 12)
	requestExpiresAt, _ := diagnosticsUploadUintField(request.value, 13)
	attestationExpiresAt, _ := diagnosticsUploadUintField(attestation.value, 13)
	authorizationIssuedAt, _ := diagnosticsUploadUintField(authorization.value, 12)
	authorizationExpiresAt, _ := diagnosticsUploadUintField(authorization.value, 13)
	if !bytes.Equal(requestPayloadDigest, attestedPayloadDigest) || !bytes.Equal(request.digest[:], attestedRequestDigest) ||
		!bytes.Equal(request.digest[:], authorizedRequestDigest) || !bytes.Equal(attestation.digest[:], authorizedAttestationDigest) ||
		authorizationIssuedAt < requestIssuedAt || attestationExpiresAt > requestExpiresAt ||
		authorizationExpiresAt > requestExpiresAt || authorizationExpiresAt > attestationExpiresAt {
		return errDiagnosticsResponseInvalid
	}
	return nil
}

func validateDiagnosticsResponseArtifactChain(
	request, attestation diagnosticsUploadMessage,
	authorization, response diagnosticsResponseMessage,
) error {
	if validateDiagnosticsResponseAuthorizationChain(request, attestation, authorization) != nil ||
		response.messageType != diagnosticsResponseArtifact ||
		!diagnosticsResponseFieldsEqual(authorization, response, 1, 2, 3, 5, 6, 7, 8, 9, 10, 11) {
		return errDiagnosticsResponseInvalid
	}
	responseRequestDigest, _ := diagnosticsUploadBytesField(response.value, 17, 32)
	responseAttestationDigest, _ := diagnosticsUploadBytesField(response.value, 20, 32)
	responseAuthorizationDigest, _ := diagnosticsUploadBytesField(response.value, 22, 32)
	requestExpiresAt, _ := diagnosticsUploadUintField(request.value, 13)
	attestationExpiresAt, _ := diagnosticsUploadUintField(attestation.value, 13)
	authorizationExpiresAt, _ := diagnosticsUploadUintField(authorization.value, 13)
	responseExpiresAt, _ := diagnosticsUploadUintField(response.value, 13)
	if !bytes.Equal(request.digest[:], responseRequestDigest) || !bytes.Equal(attestation.digest[:], responseAttestationDigest) ||
		!bytes.Equal(authorization.digest[:], responseAuthorizationDigest) || responseExpiresAt > requestExpiresAt ||
		responseExpiresAt > attestationExpiresAt || responseExpiresAt > authorizationExpiresAt {
		return errDiagnosticsResponseInvalid
	}
	return nil
}

func validateDiagnosticsCleanupAcknowledgmentChain(request, acknowledgment diagnosticsResponseMessage) error {
	if request.messageType != diagnosticsCleanupRequest || acknowledgment.messageType != diagnosticsCleanupAcknowledgment ||
		!diagnosticsResponseFieldsEqual(request, acknowledgment, 1, 2, 3, 5, 6, 7, 8, 9, 10, 11) {
		return errDiagnosticsResponseInvalid
	}
	requestTargets, _ := diagnosticsCleanupTargets(request.value)
	acknowledgmentTargets, _ := diagnosticsCleanupTargets(acknowledgment.value)
	priorDigest, _ := diagnosticsUploadBytesField(acknowledgment.value, 31, 32)
	requestExpiresAt, _ := diagnosticsUploadUintField(request.value, 13)
	acknowledgmentExpiresAt, _ := diagnosticsUploadUintField(acknowledgment.value, 13)
	if len(requestTargets) != len(acknowledgmentTargets) || !bytes.Equal(request.digest[:], priorDigest) ||
		acknowledgmentExpiresAt > requestExpiresAt {
		return errDiagnosticsResponseInvalid
	}
	for index := range requestTargets {
		if !bytes.Equal(requestTargets[index], acknowledgmentTargets[index]) {
			return errDiagnosticsResponseInvalid
		}
	}
	return nil
}

func diagnosticsUploadAndResponseFieldsEqual(upload diagnosticsUploadMessage, response diagnosticsResponseMessage, labels ...uint64) bool {
	return diagnosticsRoundtripValueFieldsEqual(upload.value, response.value, labels...)
}

func diagnosticsResponseFieldsEqual(left, right diagnosticsResponseMessage, labels ...uint64) bool {
	return diagnosticsRoundtripValueFieldsEqual(left.value, right.value, labels...)
}

func diagnosticsRoundtripValueFieldsEqual(left, right diagnosticsCBORValue, labels ...uint64) bool {
	for _, label := range labels {
		leftValue, leftOK := diagnosticsCBORLookup(left, label)
		rightValue, rightOK := diagnosticsCBORLookup(right, label)
		if !leftOK || !rightOK || !diagnosticsCBORValuesEqual(leftValue, rightValue) {
			return false
		}
	}
	return true
}
