package main

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"sort"
	"time"
)

const (
	diagnosticsCapabilityQuery    = uint64(1)
	diagnosticsCapabilityResponse = uint64(2)
	diagnosticsCapabilityLifetime = uint64(120)
)

var (
	errDiagnosticsCapabilityInvalid = errors.New("invalid diagnostics capability message")
	diagnosticsCapabilityDomains    = map[uint64]string{
		diagnosticsCapabilityQuery:    "eu.vaultsync.roundtrip/v1/capability-query\x00",
		diagnosticsCapabilityResponse: "eu.vaultsync.roundtrip/v1/capability-response\x00",
	}
)

type diagnosticsCapabilityMessage struct {
	messageType uint64
	value       diagnosticsCBORValue
	canonical   []byte
	digest      [32]byte
}

func decodeDiagnosticsCapabilityMessage(data []byte, context diagnosticsUploadVerificationContext) (diagnosticsCapabilityMessage, error) {
	value, err := decodeDiagnosticsCBOR(data)
	if err != nil || value.kind != diagnosticsCBORMap || len(context.appPublicKey) != ed25519.PublicKeySize ||
		len(context.helperPublicKey) != ed25519.PublicKeySize {
		return diagnosticsCapabilityMessage{}, errDiagnosticsCapabilityInvalid
	}
	messageType, ok := diagnosticsUploadUintField(value, 4)
	if !ok || (messageType != diagnosticsCapabilityQuery && messageType != diagnosticsCapabilityResponse) ||
		validateDiagnosticsCapabilityValue(value, messageType, nil, context) != nil {
		return diagnosticsCapabilityMessage{}, errDiagnosticsCapabilityInvalid
	}
	publicKey := context.appPublicKey
	if messageType == diagnosticsCapabilityResponse {
		publicKey = context.helperPublicKey
	}
	signature, _ := diagnosticsUploadBytesField(value, 255, ed25519.SignatureSize)
	body, err := encodeDiagnosticsCBOR(diagnosticsCBORWithoutLabels(value, 255))
	if err != nil || !ed25519.Verify(publicKey, append([]byte(diagnosticsCapabilityDomains[messageType]), body...), signature) {
		return diagnosticsCapabilityMessage{}, errDiagnosticsCapabilityInvalid
	}
	return diagnosticsCapabilityMessage{
		messageType: messageType,
		value:       value,
		canonical:   append([]byte(nil), data...),
		digest:      diagnosticsDomainSHA256(diagnosticsCapabilityDomains[messageType], body),
	}, nil
}

func buildDiagnosticsCapabilityResponse(
	query diagnosticsCapabilityMessage,
	binding diagnosticsUploadBinding,
	now time.Time,
) ([]byte, error) {
	if query.messageType != diagnosticsCapabilityQuery || len(binding.helperPrivateKey) != ed25519.PrivateKeySize || now.Unix() < 0 {
		return nil, errDiagnosticsCapabilityInvalid
	}
	context := diagnosticsUploadVerificationContext{
		appPublicKey:    append(ed25519.PublicKey(nil), binding.appPublicKey...),
		helperPublicKey: append(ed25519.PublicKey(nil), binding.helperPrivateKey.Public().(ed25519.PublicKey)...),
	}
	if err := validateDiagnosticsCapabilityClock(query, now); err != nil {
		return nil, err
	}
	queryExpiry, _ := diagnosticsUploadUintField(query.value, 13)
	issuedAt := uint64(now.Unix())
	expiresAt := issuedAt + diagnosticsCapabilityLifetime
	if queryExpiry < expiresAt {
		expiresAt = queryExpiry
	}
	if expiresAt <= issuedAt {
		return nil, errDiagnosticsCapabilityInvalid
	}
	queryNonce, _ := diagnosticsUploadBytesField(query.value, 30, 32)
	appKeyID := diagnosticsKeyID(context.appPublicKey)
	helperKeyID := diagnosticsKeyID(context.helperPublicKey)
	value := diagnosticsCBORMapValue(
		diagnosticsCBORMapField(1, diagnosticsCBORTextValue(diagnosticsRoundtripCapabilityID)),
		diagnosticsCBORMapField(2, diagnosticsCBORUint(diagnosticsProtocolMajor)),
		diagnosticsCBORMapField(3, diagnosticsCBORUint(diagnosticsCryptographicSuite)),
		diagnosticsCBORMapField(4, diagnosticsCBORUint(diagnosticsCapabilityResponse)),
		diagnosticsCBORMapField(5, diagnosticsCBORBstr(binding.homeserverBinding)),
		diagnosticsCBORMapField(6, diagnosticsCBORBstr(binding.folderBinding)),
		diagnosticsCBORMapField(7, diagnosticsCBORBstr(appKeyID[:])),
		diagnosticsCBORMapField(8, diagnosticsCBORBstr(helperKeyID[:])),
		diagnosticsCBORMapField(9, diagnosticsCBORUint(binding.appEpoch)),
		diagnosticsCBORMapField(10, diagnosticsCBORUint(binding.helperEpoch)),
		diagnosticsCBORMapField(12, diagnosticsCBORUint(issuedAt)),
		diagnosticsCBORMapField(13, diagnosticsCBORUint(expiresAt)),
		diagnosticsCBORMapField(27, diagnosticsCBORUint(diagnosticsRoundtripRequiredBits)),
		diagnosticsCBORMapField(30, diagnosticsCBORBstr(queryNonce)),
		diagnosticsCBORMapField(31, diagnosticsCBORBstr(query.digest[:])),
	)
	return signDiagnosticsCapabilityMessage(value, diagnosticsCapabilityResponse, binding.helperPrivateKey, context)
}

func signDiagnosticsCapabilityMessage(
	value diagnosticsCBORValue,
	messageType uint64,
	privateKey ed25519.PrivateKey,
	context diagnosticsUploadVerificationContext,
) ([]byte, error) {
	if len(privateKey) != ed25519.PrivateKeySize || validateDiagnosticsCapabilityValue(value, messageType, []uint64{255}, context) != nil {
		return nil, errDiagnosticsCapabilityInvalid
	}
	publicKey := context.appPublicKey
	if messageType == diagnosticsCapabilityResponse {
		publicKey = context.helperPublicKey
	}
	if !bytes.Equal(privateKey.Public().(ed25519.PublicKey), publicKey) {
		return nil, errDiagnosticsCapabilityInvalid
	}
	body, err := encodeDiagnosticsCBOR(value)
	if err != nil {
		return nil, errDiagnosticsCapabilityInvalid
	}
	signed := cloneDiagnosticsCBOR(value)
	signed.fields = append(signed.fields, diagnosticsCBORMapField(255, diagnosticsCBORBstr(
		ed25519.Sign(privateKey, append([]byte(diagnosticsCapabilityDomains[messageType]), body...)),
	)))
	encoded, err := encodeDiagnosticsCBOR(signed)
	if err != nil {
		return nil, errDiagnosticsCapabilityInvalid
	}
	if _, err := decodeDiagnosticsCapabilityMessage(encoded, context); err != nil {
		return nil, err
	}
	return encoded, nil
}

func validateDiagnosticsCapabilityValue(
	value diagnosticsCBORValue,
	messageType uint64,
	omitted []uint64,
	context diagnosticsUploadVerificationContext,
) error {
	expected := []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 12, 13, 30, 255}
	if messageType == diagnosticsCapabilityResponse {
		expected = []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 12, 13, 27, 30, 31, 255}
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
	fields := append([]diagnosticsCBORField(nil), value.fields...)
	sort.Slice(fields, func(i, j int) bool { return fields[i].label < fields[j].label })
	if value.kind != diagnosticsCBORMap || len(fields) != len(wanted) {
		return errDiagnosticsCapabilityInvalid
	}
	for index, label := range wanted {
		if fields[index].label != label || (index > 0 && fields[index-1].label == fields[index].label) {
			return errDiagnosticsCapabilityInvalid
		}
	}
	capability, ok := diagnosticsCBORLookup(value, 1)
	protocol, protocolOK := diagnosticsUploadUintField(value, 2)
	suite, suiteOK := diagnosticsUploadUintField(value, 3)
	actualType, typeOK := diagnosticsUploadUintField(value, 4)
	issuedAt, issuedOK := diagnosticsUploadUintField(value, 12)
	expiresAt, expiresOK := diagnosticsUploadUintField(value, 13)
	if !ok || capability.kind != diagnosticsCBORText || capability.text != diagnosticsRoundtripCapabilityID ||
		!protocolOK || protocol != diagnosticsProtocolMajor || !suiteOK || suite != diagnosticsCryptographicSuite ||
		!typeOK || actualType != messageType || !issuedOK || !expiresOK || issuedAt == 0 || expiresAt <= issuedAt ||
		expiresAt-issuedAt > diagnosticsCapabilityLifetime {
		return errDiagnosticsCapabilityInvalid
	}
	for _, label := range []uint64{5, 6, 7, 8, 30} {
		if body, ok := diagnosticsUploadBytesField(value, label, 32); !ok || !nonzeroDiagnosticsBytes(body) {
			return errDiagnosticsCapabilityInvalid
		}
	}
	appEpoch, appEpochOK := diagnosticsUploadUintField(value, 9)
	helperEpoch, helperEpochOK := diagnosticsUploadUintField(value, 10)
	appKeyID, _ := diagnosticsUploadBytesField(value, 7, 32)
	helperKeyID, _ := diagnosticsUploadBytesField(value, 8, 32)
	derivedAppKeyID := diagnosticsKeyID(context.appPublicKey)
	derivedHelperKeyID := diagnosticsKeyID(context.helperPublicKey)
	if !appEpochOK || !helperEpochOK || appEpoch == 0 || helperEpoch == 0 ||
		!bytes.Equal(appKeyID, derivedAppKeyID[:]) || !bytes.Equal(helperKeyID, derivedHelperKeyID[:]) {
		return errDiagnosticsCapabilityInvalid
	}
	if messageType == diagnosticsCapabilityResponse {
		flags, flagsOK := diagnosticsUploadUintField(value, 27)
		prior, priorOK := diagnosticsUploadBytesField(value, 31, 32)
		if !flagsOK || flags != diagnosticsRoundtripRequiredBits || !priorOK || !nonzeroDiagnosticsBytes(prior) {
			return errDiagnosticsCapabilityInvalid
		}
	}
	return nil
}

func validateDiagnosticsCapabilityClock(message diagnosticsCapabilityMessage, now time.Time) error {
	if now.Unix() < 0 {
		return errDiagnosticsCapabilityInvalid
	}
	issuedAt, _ := diagnosticsUploadUintField(message.value, 12)
	expiresAt, _ := diagnosticsUploadUintField(message.value, 13)
	nowSeconds := uint64(now.Unix())
	if issuedAt > nowSeconds+diagnosticsUploadMaximumClockSkewSeconds ||
		nowSeconds > expiresAt+diagnosticsUploadMaximumClockSkewSeconds {
		return errDiagnosticsCapabilityInvalid
	}
	return nil
}
