package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

const diagnosticsPairingPath = "/api/v1/diagnostics/pairing"

var errDiagnosticsPairingInvalid = errors.New("invalid diagnostics pairing message")

const (
	diagnosticsPairingQR uint64 = iota
	diagnosticsPairingAppRequest
	diagnosticsPairingHelperAccept
	diagnosticsPairingFinalize
	diagnosticsPairingFinalizeAck
	diagnosticsPairingReceipt
	diagnosticsPairingReadyAck
	diagnosticsPairingActivate
	diagnosticsPairingActiveAck
	diagnosticsPairingAbort
	diagnosticsPairingAbortAck
	diagnosticsPairingAppKeyRotationRequest
	diagnosticsPairingAppKeyRotationNewProof
	diagnosticsPairingAppKeyRotationAccept
	diagnosticsPairingHelperKeyRotationPropose
	diagnosticsPairingHelperKeyRotationNewProof
	diagnosticsPairingHelperKeyRotationConfirm
	diagnosticsPairingTLSPinRotationPropose
	diagnosticsPairingTLSPinRotationConfirm
	diagnosticsPairingRevocationRequest
	diagnosticsPairingRevocationRecord
	diagnosticsPairingLifecycleFinalize
	diagnosticsPairingLifecycleActiveAck
	diagnosticsPairingLifecycleAbort
	diagnosticsPairingLifecycleAbortAck
)

const (
	diagnosticsPairingTransitionAppKey uint64 = iota + 1
	diagnosticsPairingTransitionHelperKey
	diagnosticsPairingTransitionTLSPin
)

const (
	diagnosticsPairingRevocationUserRequest uint64 = iota + 1
	diagnosticsPairingRevocationLostApp
	diagnosticsPairingRevocationFolderRemoved
	diagnosticsPairingRevocationSuspectedCompromise
)

const (
	diagnosticsPairingRevocationSignedApp uint64 = iota + 1
	diagnosticsPairingRevocationLocalHelperAdmin
)

var diagnosticsPairingDomains = map[uint64]string{
	diagnosticsPairingAppRequest:                "eu.vaultsync.helper-pairing/v1/app-request\x00",
	diagnosticsPairingHelperAccept:              "eu.vaultsync.helper-pairing/v1/helper-accept\x00",
	diagnosticsPairingFinalize:                  "eu.vaultsync.helper-pairing/v1/pairing-finalize\x00",
	diagnosticsPairingFinalizeAck:               "eu.vaultsync.helper-pairing/v1/pairing-finalize-ack\x00",
	diagnosticsPairingReceipt:                   "eu.vaultsync.helper-pairing/v1/pairing-receipt\x00",
	diagnosticsPairingReadyAck:                  "eu.vaultsync.helper-pairing/v1/pairing-ready-ack\x00",
	diagnosticsPairingActivate:                  "eu.vaultsync.helper-pairing/v1/pairing-activate\x00",
	diagnosticsPairingActiveAck:                 "eu.vaultsync.helper-pairing/v1/pairing-active-ack\x00",
	diagnosticsPairingAbort:                     "eu.vaultsync.helper-pairing/v1/pairing-abort\x00",
	diagnosticsPairingAbortAck:                  "eu.vaultsync.helper-pairing/v1/pairing-abort-ack\x00",
	diagnosticsPairingAppKeyRotationRequest:     "eu.vaultsync.helper-pairing/v1/app-key-rotation-request\x00",
	diagnosticsPairingAppKeyRotationNewProof:    "eu.vaultsync.helper-pairing/v1/app-key-rotation-new-proof\x00",
	diagnosticsPairingAppKeyRotationAccept:      "eu.vaultsync.helper-pairing/v1/app-key-rotation-accept\x00",
	diagnosticsPairingHelperKeyRotationPropose:  "eu.vaultsync.helper-pairing/v1/helper-key-rotation-propose\x00",
	diagnosticsPairingHelperKeyRotationNewProof: "eu.vaultsync.helper-pairing/v1/helper-key-rotation-new-proof\x00",
	diagnosticsPairingHelperKeyRotationConfirm:  "eu.vaultsync.helper-pairing/v1/helper-key-rotation-confirm\x00",
	diagnosticsPairingTLSPinRotationPropose:     "eu.vaultsync.helper-pairing/v1/tls-pin-rotation-propose\x00",
	diagnosticsPairingTLSPinRotationConfirm:     "eu.vaultsync.helper-pairing/v1/tls-pin-rotation-confirm\x00",
	diagnosticsPairingRevocationRequest:         "eu.vaultsync.helper-pairing/v1/revocation-request\x00",
	diagnosticsPairingRevocationRecord:          "eu.vaultsync.helper-pairing/v1/revocation-record\x00",
	diagnosticsPairingLifecycleFinalize:         "eu.vaultsync.helper-pairing/v1/lifecycle-finalize\x00",
	diagnosticsPairingLifecycleActiveAck:        "eu.vaultsync.helper-pairing/v1/lifecycle-active-ack\x00",
	diagnosticsPairingLifecycleAbort:            "eu.vaultsync.helper-pairing/v1/lifecycle-abort\x00",
	diagnosticsPairingLifecycleAbortAck:         "eu.vaultsync.helper-pairing/v1/lifecycle-abort-ack\x00",
}

type diagnosticsPairingMessage struct {
	messageType uint64
	value       diagnosticsCBORValue
	canonical   []byte
}

func decodeDiagnosticsPairingMessage(data []byte) (diagnosticsPairingMessage, error) {
	value, err := decodeDiagnosticsCBOR(data)
	if err != nil || value.kind != diagnosticsCBORMap {
		return diagnosticsPairingMessage{}, fmt.Errorf("pairing structure: %w", errDiagnosticsPairingInvalid)
	}
	typeValue, ok := diagnosticsCBORLookup(value, 4)
	if !ok || typeValue.kind != diagnosticsCBORUnsigned || typeValue.unsigned > diagnosticsPairingLifecycleAbortAck {
		return diagnosticsPairingMessage{}, fmt.Errorf("pairing type: %w", errDiagnosticsPairingInvalid)
	}
	message := diagnosticsPairingMessage{
		messageType: typeValue.unsigned,
		value:       value,
		canonical:   append([]byte(nil), data...),
	}
	if err := validateDiagnosticsPairingSchema(message); err != nil {
		return diagnosticsPairingMessage{}, fmt.Errorf("pairing schema: %w", err)
	}
	if message.messageType != diagnosticsPairingQR {
		if err := message.verifySignature(); err != nil {
			return diagnosticsPairingMessage{}, fmt.Errorf("pairing signature: %w", err)
		}
	}
	return message, nil
}

func signDiagnosticsPairingMessage(value diagnosticsCBORValue, privateKey ed25519.PrivateKey) ([]byte, error) {
	typeValue, ok := diagnosticsCBORLookup(value, 4)
	if !ok || typeValue.kind != diagnosticsCBORUnsigned || typeValue.unsigned == diagnosticsPairingQR {
		return nil, errDiagnosticsPairingInvalid
	}
	if _, exists := diagnosticsCBORLookup(value, 255); exists || len(privateKey) != ed25519.PrivateKeySize {
		return nil, errDiagnosticsPairingInvalid
	}
	domain, ok := diagnosticsPairingDomains[typeValue.unsigned]
	if !ok {
		return nil, errDiagnosticsPairingInvalid
	}
	body, err := encodeDiagnosticsCBOR(value)
	if err != nil {
		return nil, errDiagnosticsPairingInvalid
	}
	signature := ed25519.Sign(privateKey, append([]byte(domain), body...))
	fields := append([]diagnosticsCBORField(nil), value.fields...)
	fields = append(fields, diagnosticsCBORMapField(255, diagnosticsCBORBstr(signature)))
	signed := diagnosticsCBORMapValue(fields...)
	encoded, err := encodeDiagnosticsCBOR(signed)
	if err != nil {
		return nil, errDiagnosticsPairingInvalid
	}
	if _, err := decodeDiagnosticsPairingMessage(encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}

func (message diagnosticsPairingMessage) verifySignature() error {
	domain, ok := diagnosticsPairingDomains[message.messageType]
	if !ok {
		return errDiagnosticsPairingInvalid
	}
	publicKey, ok := message.signerPublicKey()
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return errDiagnosticsPairingInvalid
	}
	signature, ok := message.bytesField(255, ed25519.SignatureSize)
	if !ok {
		return errDiagnosticsPairingInvalid
	}
	body, err := encodeDiagnosticsCBOR(diagnosticsCBORWithoutLabels(message.value, 255))
	if err != nil || !ed25519.Verify(ed25519.PublicKey(publicKey), append([]byte(domain), body...), signature) {
		return errDiagnosticsPairingInvalid
	}
	return nil
}

func (message diagnosticsPairingMessage) signerPublicKey() ([]byte, bool) {
	switch message.messageType {
	case diagnosticsPairingAppRequest,
		diagnosticsPairingFinalize,
		diagnosticsPairingReceipt,
		diagnosticsPairingActivate,
		diagnosticsPairingAbort:
		return message.bytesField(18, ed25519.PublicKeySize)
	case diagnosticsPairingHelperAccept,
		diagnosticsPairingFinalizeAck,
		diagnosticsPairingReadyAck,
		diagnosticsPairingActiveAck,
		diagnosticsPairingAbortAck:
		return message.bytesField(9, ed25519.PublicKeySize)
	case diagnosticsPairingAppKeyRotationRequest,
		diagnosticsPairingHelperKeyRotationConfirm,
		diagnosticsPairingTLSPinRotationConfirm,
		diagnosticsPairingRevocationRequest,
		diagnosticsPairingLifecycleAbort:
		return message.bytesField(7, ed25519.PublicKeySize)
	case diagnosticsPairingAppKeyRotationNewProof:
		return message.bytesField(9, ed25519.PublicKeySize)
	case diagnosticsPairingAppKeyRotationAccept,
		diagnosticsPairingHelperKeyRotationPropose,
		diagnosticsPairingTLSPinRotationPropose,
		diagnosticsPairingRevocationRecord,
		diagnosticsPairingLifecycleAbortAck:
		return message.bytesField(11, ed25519.PublicKeySize)
	case diagnosticsPairingHelperKeyRotationNewProof:
		return message.bytesField(13, ed25519.PublicKeySize)
	case diagnosticsPairingLifecycleFinalize:
		kind, ok := message.uintField(29)
		if ok && kind == diagnosticsPairingTransitionAppKey {
			return message.bytesField(9, ed25519.PublicKeySize)
		}
		return message.bytesField(7, ed25519.PublicKeySize)
	case diagnosticsPairingLifecycleActiveAck:
		kind, ok := message.uintField(29)
		if ok && kind == diagnosticsPairingTransitionHelperKey {
			return message.bytesField(13, ed25519.PublicKeySize)
		}
		return message.bytesField(11, ed25519.PublicKeySize)
	default:
		return nil, false
	}
}

func (message diagnosticsPairingMessage) digest() ([32]byte, error) {
	domain, ok := diagnosticsPairingDomains[message.messageType]
	if !ok {
		return [32]byte{}, errDiagnosticsPairingInvalid
	}
	body, err := encodeDiagnosticsCBOR(diagnosticsCBORWithoutLabels(message.value, 255))
	if err != nil {
		return [32]byte{}, errDiagnosticsPairingInvalid
	}
	return diagnosticsDomainSHA256(domain, body), nil
}

func (message diagnosticsPairingMessage) bytesField(label uint64, length int) ([]byte, bool) {
	value, ok := diagnosticsCBORLookup(message.value, label)
	if !ok || value.kind != diagnosticsCBORBytes || (length >= 0 && len(value.bytes) != length) {
		return nil, false
	}
	return append([]byte(nil), value.bytes...), true
}

func (message diagnosticsPairingMessage) uintField(label uint64) (uint64, bool) {
	value, ok := diagnosticsCBORLookup(message.value, label)
	if !ok || value.kind != diagnosticsCBORUnsigned {
		return 0, false
	}
	return value.unsigned, true
}

func validateDiagnosticsPairingSchema(message diagnosticsPairingMessage) error {
	capability, ok := diagnosticsCBORLookup(message.value, 1)
	if !ok || capability.kind != diagnosticsCBORText || capability.text != diagnosticsPairingCapabilityID {
		return errDiagnosticsPairingInvalid
	}
	for _, label := range []uint64{2, 3} {
		value, ok := message.uintField(label)
		if !ok || value != 1 {
			return errDiagnosticsPairingInvalid
		}
	}

	expected, err := diagnosticsPairingExpectedLabels(message)
	if err != nil || !diagnosticsLabelsEqual(message.value.fields, expected) {
		return errDiagnosticsPairingInvalid
	}
	if message.messageType <= diagnosticsPairingAbortAck {
		return validateDiagnosticsBootstrapFields(message)
	}
	return validateDiagnosticsLifecycleFields(message)
}

func diagnosticsPairingExpectedLabels(message diagnosticsPairingMessage) ([]uint64, error) {
	switch message.messageType {
	case diagnosticsPairingQR:
		return diagnosticsLabelRangeWith(1, 17, 24), nil
	case diagnosticsPairingAppRequest:
		return diagnosticsLabels(1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 18, 19, 20, 21, 23, 24, 255), nil
	case diagnosticsPairingHelperAccept:
		return diagnosticsLabels(1, 2, 3, 4, 5, 9, 10, 11, 12, 13, 14, 15, 16, 18, 19, 20, 22, 23, 24, 25, 255), nil
	case diagnosticsPairingFinalize, diagnosticsPairingFinalizeAck, diagnosticsPairingReceipt,
		diagnosticsPairingReadyAck, diagnosticsPairingActivate, diagnosticsPairingActiveAck,
		diagnosticsPairingAbort, diagnosticsPairingAbortAck:
		return diagnosticsLabels(1, 2, 3, 4, 5, 9, 10, 11, 12, 13, 14, 15, 16, 18, 19, 20, 22, 23, 24, 25, 26, 255), nil
	case diagnosticsPairingAppKeyRotationRequest:
		return diagnosticsLifecycleLabels(9, 10, 18), nil
	case diagnosticsPairingAppKeyRotationNewProof, diagnosticsPairingAppKeyRotationAccept:
		return diagnosticsLifecycleLabels(9, 10, 18, 24), nil
	case diagnosticsPairingHelperKeyRotationPropose:
		return diagnosticsLifecycleLabels(13, 14, 20), nil
	case diagnosticsPairingHelperKeyRotationNewProof, diagnosticsPairingHelperKeyRotationConfirm:
		return diagnosticsLifecycleLabels(13, 14, 20, 24), nil
	case diagnosticsPairingTLSPinRotationPropose:
		return diagnosticsLifecycleLabels(16), nil
	case diagnosticsPairingTLSPinRotationConfirm:
		return diagnosticsLifecycleLabels(16, 24), nil
	case diagnosticsPairingRevocationRequest:
		return diagnosticsLifecycleLabels(18, 25, 27), nil
	case diagnosticsPairingRevocationRecord:
		origin, ok := message.uintField(27)
		if !ok {
			return nil, errDiagnosticsPairingInvalid
		}
		if origin == diagnosticsPairingRevocationSignedApp {
			return diagnosticsLifecycleLabels(18, 24, 25, 27), nil
		}
		return diagnosticsLifecycleLabels(18, 25, 27), nil
	case diagnosticsPairingLifecycleFinalize, diagnosticsPairingLifecycleActiveAck,
		diagnosticsPairingLifecycleAbort, diagnosticsPairingLifecycleAbortAck:
		kind, ok := message.uintField(29)
		if !ok {
			return nil, errDiagnosticsPairingInvalid
		}
		switch kind {
		case diagnosticsPairingTransitionAppKey:
			return diagnosticsLifecycleLabels(9, 10, 18, 24, 28, 29), nil
		case diagnosticsPairingTransitionHelperKey:
			return diagnosticsLifecycleLabels(13, 14, 20, 24, 28, 29), nil
		case diagnosticsPairingTransitionTLSPin:
			return diagnosticsLifecycleLabels(16, 24, 28, 29), nil
		default:
			return nil, errDiagnosticsPairingInvalid
		}
	default:
		return nil, errDiagnosticsPairingInvalid
	}
}

func diagnosticsLifecycleLabels(additional ...uint64) []uint64 {
	common := []uint64{1, 2, 3, 4, 5, 6, 7, 8, 11, 12, 15, 17, 19, 21, 22, 23, 26, 255}
	return diagnosticsLabels(append(common, additional...)...)
}

func diagnosticsLabelRangeWith(first, last uint64, additional ...uint64) []uint64 {
	labels := make([]uint64, 0, last-first+1+uint64(len(additional)))
	for label := first; label <= last; label++ {
		labels = append(labels, label)
	}
	return diagnosticsLabels(append(labels, additional...)...)
}

func diagnosticsLabels(labels ...uint64) []uint64 {
	result := append([]uint64(nil), labels...)
	for i := 1; i < len(result); i++ {
		for j := i; j > 0 && result[j] < result[j-1]; j-- {
			result[j], result[j-1] = result[j-1], result[j]
		}
	}
	return result
}

func diagnosticsLabelsEqual(fields []diagnosticsCBORField, expected []uint64) bool {
	if len(fields) != len(expected) {
		return false
	}
	for index := range fields {
		if fields[index].label != expected[index] {
			return false
		}
	}
	return true
}

func validateDiagnosticsBootstrapFields(message diagnosticsPairingMessage) error {
	uintLabels := []uint64{2, 3, 4, 15, 16, 24}
	if message.messageType <= diagnosticsPairingAppRequest {
		uintLabels = append(uintLabels, 7)
	}
	if message.messageType != diagnosticsPairingQR {
		uintLabels = append(uintLabels, 23)
	}
	for _, label := range uintLabels {
		if _, ok := message.uintField(label); !ok {
			return errDiagnosticsPairingInvalid
		}
	}
	for _, field := range message.value.fields {
		if field.label == 1 || field.label == 6 || field.label == 255 {
			continue
		}
		if diagnosticsContainsLabel(uintLabels, field.label) {
			continue
		}
		if field.value.kind != diagnosticsCBORBytes || len(field.value.bytes) != 32 {
			return errDiagnosticsPairingInvalid
		}
	}
	if message.messageType != diagnosticsPairingQR {
		if signature, ok := diagnosticsCBORLookup(message.value, 255); !ok || signature.kind != diagnosticsCBORBytes || len(signature.bytes) != ed25519.SignatureSize {
			return errDiagnosticsPairingInvalid
		}
	}
	if message.messageType <= diagnosticsPairingAppRequest {
		host, ok := diagnosticsCBORLookup(message.value, 6)
		port, portOK := message.uintField(7)
		if !ok || host.kind != diagnosticsCBORText || !validDiagnosticsEndpointHost(host.text) || !portOK || port == 0 || port > 65535 {
			return errDiagnosticsPairingInvalid
		}
	}
	issued, issuedOK := message.uintField(15)
	expires, expiresOK := message.uintField(16)
	if !issuedOK || !expiresOK || expires <= issued || expires-issued > 300 {
		return errDiagnosticsPairingInvalid
	}
	if message.messageType != diagnosticsPairingQR {
		appEpoch, _ := message.uintField(23)
		if appEpoch != 1 {
			return errDiagnosticsPairingInvalid
		}
		appPublic, _ := message.bytesField(18, ed25519.PublicKeySize)
		appKeyID, _ := message.bytesField(19, 32)
		derivedAppKeyID := diagnosticsKeyID(appPublic)
		if subtle.ConstantTimeCompare(derivedAppKeyID[:], appKeyID) != 1 {
			return errDiagnosticsPairingInvalid
		}
	}
	helperPublic, _ := message.bytesField(9, ed25519.PublicKeySize)
	helperKeyID, _ := message.bytesField(10, 32)
	derivedHelperKeyID := diagnosticsKeyID(helperPublic)
	if subtle.ConstantTimeCompare(derivedHelperKeyID[:], helperKeyID) != 1 {
		return errDiagnosticsPairingInvalid
	}
	return nil
}

func validateDiagnosticsLifecycleFields(message diagnosticsPairingMessage) error {
	uintLabels := []uint64{2, 3, 4, 17, 19, 21, 22}
	for _, optional := range []uint64{18, 20, 25, 27, 29} {
		if _, ok := diagnosticsCBORLookup(message.value, optional); ok {
			uintLabels = append(uintLabels, optional)
		}
	}
	for _, field := range message.value.fields {
		if field.label == 1 || field.label == 255 || diagnosticsContainsLabel(uintLabels, field.label) {
			continue
		}
		if field.value.kind != diagnosticsCBORBytes || len(field.value.bytes) != 32 {
			return errDiagnosticsPairingInvalid
		}
	}
	for _, label := range uintLabels {
		if _, ok := message.uintField(label); !ok {
			return errDiagnosticsPairingInvalid
		}
	}
	signature, ok := diagnosticsCBORLookup(message.value, 255)
	if !ok || signature.kind != diagnosticsCBORBytes || len(signature.bytes) != ed25519.SignatureSize {
		return errDiagnosticsPairingInvalid
	}
	issued, _ := message.uintField(21)
	expires, _ := message.uintField(22)
	if expires <= issued || expires-issued > 300 {
		return errDiagnosticsPairingInvalid
	}
	if reason, ok := message.uintField(25); ok && (reason < 1 || reason > 4) {
		return errDiagnosticsPairingInvalid
	}
	if origin, ok := message.uintField(27); ok && (origin < 1 || origin > 2) {
		return errDiagnosticsPairingInvalid
	}
	if kind, ok := message.uintField(29); ok && (kind < 1 || kind > 3) {
		return errDiagnosticsPairingInvalid
	}
	for _, keyPair := range [][2]uint64{{7, 8}, {9, 10}, {11, 12}, {13, 14}} {
		publicKey, publicOK := message.bytesField(keyPair[0], ed25519.PublicKeySize)
		keyID, idOK := message.bytesField(keyPair[1], 32)
		derivedKeyID := diagnosticsKeyID(publicKey)
		if publicOK != idOK || (publicOK && subtle.ConstantTimeCompare(derivedKeyID[:], keyID) != 1) {
			return errDiagnosticsPairingInvalid
		}
	}
	currentAppEpoch, _ := message.uintField(17)
	if proposed, ok := message.uintField(18); ok && proposed != currentAppEpoch+1 {
		return errDiagnosticsPairingInvalid
	}
	currentHelperEpoch, _ := message.uintField(19)
	if proposed, ok := message.uintField(20); ok && proposed != currentHelperEpoch+1 {
		return errDiagnosticsPairingInvalid
	}
	return nil
}

func diagnosticsContainsLabel(labels []uint64, label uint64) bool {
	for _, candidate := range labels {
		if candidate == label {
			return true
		}
	}
	return false
}

func validDiagnosticsEndpointHost(host string) bool {
	if host == "" || len(host) > 253 || host != strings.ToLower(host) || !isRestrictedDiagnosticsASCII(host) {
		return false
	}
	if address, err := netip.ParseAddr(host); err == nil {
		return address.String() == host && address.Zone() == ""
	}
	if strings.Contains(host, ":") || diagnosticsLooksLikeIPv4Literal(host) {
		return false
	}
	if strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for index := range len(label) {
			character := label[index]
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

func diagnosticsLooksLikeIPv4Literal(host string) bool {
	for index := range len(host) {
		if (host[index] < '0' || host[index] > '9') && host[index] != '.' {
			return false
		}
	}
	return true
}

func isRestrictedDiagnosticsASCII(value string) bool {
	for index := range len(value) {
		if value[index] < 0x21 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

func diagnosticsPairingEchoMatches(invitation, request diagnosticsPairingMessage) bool {
	if invitation.messageType != diagnosticsPairingQR || request.messageType != diagnosticsPairingAppRequest {
		return false
	}
	for label := uint64(1); label <= 16; label++ {
		if label == 4 {
			continue
		}
		left, leftOK := diagnosticsCBORLookup(invitation.value, label)
		right, rightOK := diagnosticsCBORLookup(request.value, label)
		if !leftOK || !rightOK {
			return false
		}
		leftBytes, leftErr := encodeDiagnosticsCBOR(left)
		rightBytes, rightErr := encodeDiagnosticsCBOR(right)
		if leftErr != nil || rightErr != nil || !bytes.Equal(leftBytes, rightBytes) {
			return false
		}
	}
	return true
}
