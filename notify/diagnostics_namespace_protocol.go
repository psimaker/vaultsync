package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"sort"
	"strconv"
	"strings"
)

const (
	diagnosticsNamespaceEnablement uint64 = iota + 1
	diagnosticsNamespaceRootManifest
	diagnosticsNamespaceHelperEpoch
	diagnosticsNamespaceInitialAuthorization
	diagnosticsNamespaceAuthorizationEpoch
)

const (
	diagnosticsNamespaceRootName                     = "VaultSync Diagnostics"
	diagnosticsNamespaceRootManifestName             = "root-manifest.cbor"
	diagnosticsNamespaceManifestEpochsName           = "manifest-epochs"
	diagnosticsNamespaceInstallationsName            = "installations"
	diagnosticsNamespaceAuthorizationName            = "authorization.cbor"
	diagnosticsNamespaceAuthorizationEpochsName      = "authorization-epochs"
	diagnosticsNamespaceOperationsName               = "operations"
	diagnosticsNamespaceRecordDigestDomain           = "eu.vaultsync.namespace/v1/record-digest\x00"
	diagnosticsNamespaceInstallationBindingDomain    = "eu.vaultsync.namespace/installation/v1\x00"
	diagnosticsNamespaceMaximumInstallations         = 8
	diagnosticsNamespaceMaximumHelperEpochs          = 8
	diagnosticsNamespaceMaximumAuthorizationEpochs   = 8
	diagnosticsNamespaceMaximumCandidateLifetimeSecs = 300
)

const diagnosticsNamespaceReadme = `VaultSync Diagnostics

EN: App-owned diagnostics infrastructure. It contains only opaque protocol
data. It is visible in file browsers, synchronized peers, backups, versions,
conflict copies, and deletion tombstones. Do not store notes here.

DE: App-eigene Diagnose-Infrastruktur. Sie enthaelt nur undurchsichtige
Protokolldaten. Sie ist in Dateibrowsern, auf synchronisierten Geraeten, in
Backups, Versionen, Konfliktkopien und Loesch-Tombstones sichtbar. Keine
Notizen hier speichern.

ES: Infraestructura de diagnostico propiedad de la aplicacion. Solo contiene
datos opacos del protocolo. Es visible en exploradores de archivos, pares
sincronizados, copias de seguridad, versiones, copias en conflicto y registros
de eliminacion. No guardes notas aqui.

ZH-HANS: VaultSync 诊断基础设施，仅包含不透明的协议数据。它会显示在文件浏览器、
同步设备、备份、版本、冲突副本和删除记录中。请勿在此存储笔记。
`

var (
	errDiagnosticsNamespaceInvalid = errors.New("invalid diagnostics namespace record")
	diagnosticsNamespaceBase32     = base32.StdEncoding.WithPadding(base32.NoPadding)
	diagnosticsNamespaceDomains    = map[uint64]struct {
		app           string
		helper        string
		priorHelper   string
		currentHelper string
	}{
		diagnosticsNamespaceEnablement: {
			app: "eu.vaultsync.namespace/v1/enablement-request\x00",
		},
		diagnosticsNamespaceRootManifest: {
			helper: "eu.vaultsync.namespace/v1/root-manifest\x00",
		},
		diagnosticsNamespaceHelperEpoch: {
			priorHelper:   "eu.vaultsync.namespace/v1/helper-epoch-prior\x00",
			currentHelper: "eu.vaultsync.namespace/v1/helper-epoch-current\x00",
		},
		diagnosticsNamespaceInitialAuthorization: {
			app:    "eu.vaultsync.namespace/v1/authorization-initial-app\x00",
			helper: "eu.vaultsync.namespace/v1/authorization-initial-helper\x00",
		},
		diagnosticsNamespaceAuthorizationEpoch: {
			app:    "eu.vaultsync.namespace/v1/authorization-epoch-app\x00",
			helper: "eu.vaultsync.namespace/v1/authorization-epoch-helper\x00",
		},
	}
)

type diagnosticsNamespaceMessage struct {
	messageType uint64
	value       diagnosticsCBORValue
	canonical   []byte
}

type diagnosticsNamespaceChain struct {
	Enablement     []byte
	RootManifest   []byte
	HelperEpochs   [][]byte
	Authorizations [][][]byte
}

func decodeDiagnosticsNamespaceMessage(data []byte) (diagnosticsNamespaceMessage, error) {
	value, err := decodeDiagnosticsCBOR(data)
	if err != nil || value.kind != diagnosticsCBORMap {
		return diagnosticsNamespaceMessage{}, errDiagnosticsNamespaceInvalid
	}
	messageTypeValue, ok := diagnosticsCBORLookup(value, 4)
	if !ok || messageTypeValue.kind != diagnosticsCBORUnsigned ||
		messageTypeValue.unsigned < diagnosticsNamespaceEnablement ||
		messageTypeValue.unsigned > diagnosticsNamespaceAuthorizationEpoch {
		return diagnosticsNamespaceMessage{}, errDiagnosticsNamespaceInvalid
	}
	message := diagnosticsNamespaceMessage{
		messageType: messageTypeValue.unsigned,
		value:       value,
		canonical:   append([]byte(nil), data...),
	}
	if err := validateDiagnosticsNamespaceValue(message.value, message.messageType, nil); err != nil {
		return diagnosticsNamespaceMessage{}, err
	}
	if err := message.verifySignatures(); err != nil {
		return diagnosticsNamespaceMessage{}, err
	}
	return message, nil
}

func signDiagnosticsNamespaceEnablement(value diagnosticsCBORValue, appPrivate ed25519.PrivateKey) ([]byte, error) {
	return signDiagnosticsNamespaceSingle(value, diagnosticsNamespaceEnablement, 253, diagnosticsNamespaceDomains[diagnosticsNamespaceEnablement].app, appPrivate)
}

func signDiagnosticsNamespaceRootManifest(value diagnosticsCBORValue, helperPrivate ed25519.PrivateKey) ([]byte, error) {
	return signDiagnosticsNamespaceSingle(value, diagnosticsNamespaceRootManifest, 255, diagnosticsNamespaceDomains[diagnosticsNamespaceRootManifest].helper, helperPrivate)
}

func signDiagnosticsNamespaceHelperEpoch(value diagnosticsCBORValue, priorHelperPrivate, currentHelperPrivate ed25519.PrivateKey) ([]byte, error) {
	if len(priorHelperPrivate) != ed25519.PrivateKeySize || len(currentHelperPrivate) != ed25519.PrivateKeySize {
		return nil, errDiagnosticsNamespaceInvalid
	}
	if err := validateDiagnosticsNamespaceValue(value, diagnosticsNamespaceHelperEpoch, []uint64{254, 255}); err != nil {
		return nil, err
	}
	body, err := encodeDiagnosticsCBOR(value)
	if err != nil {
		return nil, errDiagnosticsNamespaceInvalid
	}
	priorSignature := ed25519.Sign(priorHelperPrivate, append([]byte(diagnosticsNamespaceDomains[diagnosticsNamespaceHelperEpoch].priorHelper), body...))
	withPrior := cloneDiagnosticsCBOR(value)
	withPrior.fields = append(withPrior.fields, diagnosticsCBORMapField(254, diagnosticsCBORBstr(priorSignature)))
	currentBody, err := encodeDiagnosticsCBOR(withPrior)
	if err != nil {
		return nil, errDiagnosticsNamespaceInvalid
	}
	currentSignature := ed25519.Sign(currentHelperPrivate, append([]byte(diagnosticsNamespaceDomains[diagnosticsNamespaceHelperEpoch].currentHelper), currentBody...))
	withPrior.fields = append(withPrior.fields, diagnosticsCBORMapField(255, diagnosticsCBORBstr(currentSignature)))
	encoded, err := encodeDiagnosticsCBOR(withPrior)
	if err != nil {
		return nil, errDiagnosticsNamespaceInvalid
	}
	if _, err := decodeDiagnosticsNamespaceMessage(encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}

func signDiagnosticsNamespaceAuthorizationCandidate(value diagnosticsCBORValue, appPrivate ed25519.PrivateKey) ([]byte, error) {
	messageType, ok := diagnosticsNamespaceMessageType(value)
	if !ok || (messageType != diagnosticsNamespaceInitialAuthorization && messageType != diagnosticsNamespaceAuthorizationEpoch) ||
		len(appPrivate) != ed25519.PrivateKeySize {
		return nil, errDiagnosticsNamespaceInvalid
	}
	if err := validateDiagnosticsNamespaceValue(value, messageType, []uint64{253, 255}); err != nil {
		return nil, err
	}
	body, err := encodeDiagnosticsCBOR(value)
	if err != nil {
		return nil, errDiagnosticsNamespaceInvalid
	}
	signature := ed25519.Sign(appPrivate, append([]byte(diagnosticsNamespaceDomains[messageType].app), body...))
	candidate := cloneDiagnosticsCBOR(value)
	candidate.fields = append(candidate.fields, diagnosticsCBORMapField(253, diagnosticsCBORBstr(signature)))
	if err := validateDiagnosticsNamespaceValue(candidate, messageType, []uint64{255}); err != nil {
		return nil, err
	}
	encoded, err := encodeDiagnosticsCBOR(candidate)
	if err != nil {
		return nil, errDiagnosticsNamespaceInvalid
	}
	if err := verifyDiagnosticsNamespaceAppSignature(candidate, messageType); err != nil {
		return nil, err
	}
	return encoded, nil
}

func countersignDiagnosticsNamespaceAuthorization(candidate []byte, helperPrivate ed25519.PrivateKey) ([]byte, error) {
	value, err := decodeDiagnosticsCBOR(candidate)
	if err != nil || len(helperPrivate) != ed25519.PrivateKeySize {
		return nil, errDiagnosticsNamespaceInvalid
	}
	messageType, ok := diagnosticsNamespaceMessageType(value)
	if !ok || (messageType != diagnosticsNamespaceInitialAuthorization && messageType != diagnosticsNamespaceAuthorizationEpoch) {
		return nil, errDiagnosticsNamespaceInvalid
	}
	if err := validateDiagnosticsNamespaceValue(value, messageType, []uint64{255}); err != nil {
		return nil, err
	}
	if err := verifyDiagnosticsNamespaceAppSignature(value, messageType); err != nil {
		return nil, err
	}
	body, err := encodeDiagnosticsCBOR(value)
	if err != nil {
		return nil, errDiagnosticsNamespaceInvalid
	}
	signature := ed25519.Sign(helperPrivate, append([]byte(diagnosticsNamespaceDomains[messageType].helper), body...))
	complete := cloneDiagnosticsCBOR(value)
	complete.fields = append(complete.fields, diagnosticsCBORMapField(255, diagnosticsCBORBstr(signature)))
	encoded, err := encodeDiagnosticsCBOR(complete)
	if err != nil {
		return nil, errDiagnosticsNamespaceInvalid
	}
	if _, err := decodeDiagnosticsNamespaceMessage(encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}

func signDiagnosticsNamespaceSingle(value diagnosticsCBORValue, messageType, signatureLabel uint64, domain string, privateKey ed25519.PrivateKey) ([]byte, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, errDiagnosticsNamespaceInvalid
	}
	if actual, ok := diagnosticsNamespaceMessageType(value); !ok || actual != messageType {
		return nil, errDiagnosticsNamespaceInvalid
	}
	if err := validateDiagnosticsNamespaceValue(value, messageType, []uint64{signatureLabel}); err != nil {
		return nil, err
	}
	body, err := encodeDiagnosticsCBOR(value)
	if err != nil {
		return nil, errDiagnosticsNamespaceInvalid
	}
	signature := ed25519.Sign(privateKey, append([]byte(domain), body...))
	signed := cloneDiagnosticsCBOR(value)
	signed.fields = append(signed.fields, diagnosticsCBORMapField(signatureLabel, diagnosticsCBORBstr(signature)))
	encoded, err := encodeDiagnosticsCBOR(signed)
	if err != nil {
		return nil, errDiagnosticsNamespaceInvalid
	}
	if _, err := decodeDiagnosticsNamespaceMessage(encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}

func (message diagnosticsNamespaceMessage) verifySignatures() error {
	switch message.messageType {
	case diagnosticsNamespaceEnablement:
		return verifyDiagnosticsNamespaceSignature(message.value, 10, 253, diagnosticsNamespaceDomains[message.messageType].app, 253)
	case diagnosticsNamespaceRootManifest:
		return verifyDiagnosticsNamespaceSignature(message.value, 13, 255, diagnosticsNamespaceDomains[message.messageType].helper, 255)
	case diagnosticsNamespaceHelperEpoch:
		if err := verifyDiagnosticsNamespaceSignature(message.value, 16, 254, diagnosticsNamespaceDomains[message.messageType].priorHelper, 254, 255); err != nil {
			return err
		}
		return verifyDiagnosticsNamespaceSignature(message.value, 13, 255, diagnosticsNamespaceDomains[message.messageType].currentHelper, 255)
	case diagnosticsNamespaceInitialAuthorization, diagnosticsNamespaceAuthorizationEpoch:
		if err := verifyDiagnosticsNamespaceAppSignature(message.value, message.messageType); err != nil {
			return err
		}
		return verifyDiagnosticsNamespaceSignature(message.value, 13, 255, diagnosticsNamespaceDomains[message.messageType].helper, 255)
	default:
		return errDiagnosticsNamespaceInvalid
	}
}

func verifyDiagnosticsNamespaceAppSignature(value diagnosticsCBORValue, messageType uint64) error {
	return verifyDiagnosticsNamespaceSignature(value, 10, 253, diagnosticsNamespaceDomains[messageType].app, 253, 254, 255)
}

func verifyDiagnosticsNamespaceSignature(value diagnosticsCBORValue, publicKeyLabel, signatureLabel uint64, domain string, omitted ...uint64) error {
	publicKey, ok := diagnosticsNamespaceBytesField(value, publicKeyLabel, ed25519.PublicKeySize)
	if !ok {
		return errDiagnosticsNamespaceInvalid
	}
	signature, ok := diagnosticsNamespaceBytesField(value, signatureLabel, ed25519.SignatureSize)
	if !ok {
		return errDiagnosticsNamespaceInvalid
	}
	body, err := encodeDiagnosticsCBOR(diagnosticsCBORWithoutLabels(value, omitted...))
	if err != nil || !ed25519.Verify(ed25519.PublicKey(publicKey), append([]byte(domain), body...), signature) {
		return errDiagnosticsNamespaceInvalid
	}
	return nil
}

func validateDiagnosticsNamespaceValue(value diagnosticsCBORValue, messageType uint64, omitted []uint64) error {
	if value.kind != diagnosticsCBORMap {
		return errDiagnosticsNamespaceInvalid
	}
	expected := diagnosticsNamespaceExpectedLabels(messageType)
	if expected == nil {
		return errDiagnosticsNamespaceInvalid
	}
	remove := make(map[uint64]struct{}, len(omitted))
	for _, label := range omitted {
		remove[label] = struct{}{}
	}
	wanted := make([]uint64, 0, len(expected))
	for _, label := range expected {
		if _, ok := remove[label]; !ok {
			wanted = append(wanted, label)
		}
	}
	if len(value.fields) != len(wanted) {
		return errDiagnosticsNamespaceInvalid
	}
	fields := append([]diagnosticsCBORField(nil), value.fields...)
	sort.Slice(fields, func(i, j int) bool { return fields[i].label < fields[j].label })
	for index, label := range wanted {
		if fields[index].label != label || !diagnosticsNamespaceFieldValid(label, fields[index].value) ||
			(index > 0 && fields[index-1].label == fields[index].label) {
			return errDiagnosticsNamespaceInvalid
		}
	}
	capability, _ := diagnosticsCBORLookup(value, 1)
	protocol, _ := diagnosticsNamespaceUintField(value, 2)
	suite, _ := diagnosticsNamespaceUintField(value, 3)
	actualType, _ := diagnosticsNamespaceUintField(value, 4)
	if capability.text != diagnosticsNamespaceCapabilityID || protocol != 1 || suite != 1 || actualType != messageType {
		return errDiagnosticsNamespaceInvalid
	}
	if err := validateDiagnosticsNamespaceRelationships(value, messageType); err != nil {
		return err
	}
	return nil
}

func validateDiagnosticsNamespaceRelationships(value diagnosticsCBORValue, messageType uint64) error {
	if messageType == diagnosticsNamespaceEnablement || messageType == diagnosticsNamespaceInitialAuthorization || messageType == diagnosticsNamespaceAuthorizationEpoch {
		issuedAt, okIssued := diagnosticsNamespaceUintField(value, 26)
		expiresAt, okExpires := diagnosticsNamespaceUintField(value, 27)
		if !okIssued || !okExpires || issuedAt == 0 || expiresAt <= issuedAt || expiresAt-issuedAt > diagnosticsNamespaceMaximumCandidateLifetimeSecs {
			return errDiagnosticsNamespaceInvalid
		}
	}
	if messageType == diagnosticsNamespaceRootManifest || messageType == diagnosticsNamespaceHelperEpoch {
		createdAt, _ := diagnosticsNamespaceUintField(value, 28)
		if createdAt == 0 {
			return errDiagnosticsNamespaceInvalid
		}
	}
	for _, label := range []uint64{12, 15, 18} {
		if epoch, present := diagnosticsNamespaceUintField(value, label); present && epoch == 0 {
			return errDiagnosticsNamespaceInvalid
		}
	}
	for _, pair := range [][2]uint64{{10, 11}, {13, 14}, {16, 17}} {
		publicKey, hasPublic := diagnosticsNamespaceBytesField(value, pair[0], ed25519.PublicKeySize)
		keyID, hasKeyID := diagnosticsNamespaceBytesField(value, pair[1], 32)
		if hasPublic != hasKeyID {
			return errDiagnosticsNamespaceInvalid
		}
		if hasPublic {
			derived := diagnosticsKeyID(publicKey)
			if !bytes.Equal(derived[:], keyID) {
				return errDiagnosticsNamespaceInvalid
			}
		}
	}
	if messageType == diagnosticsNamespaceEnablement || messageType == diagnosticsNamespaceInitialAuthorization {
		initial, _ := diagnosticsNamespaceBytesField(value, 9, 32)
		current, _ := diagnosticsNamespaceBytesField(value, 11, 32)
		if !bytes.Equal(initial, current) {
			return errDiagnosticsNamespaceInvalid
		}
	}
	if messageType == diagnosticsNamespaceInitialAuthorization || messageType == diagnosticsNamespaceAuthorizationEpoch {
		initial, _ := diagnosticsNamespaceBytesField(value, 9, 32)
		homeserver, _ := diagnosticsNamespaceBytesField(value, 5, 32)
		folder, _ := diagnosticsNamespaceBytesField(value, 6, 32)
		installation, _ := diagnosticsNamespaceBytesField(value, 8, 32)
		derived, err := diagnosticsNamespaceInstallationBinding(initial, homeserver, folder)
		if err != nil || !bytes.Equal(derived[:], installation) {
			return errDiagnosticsNamespaceInvalid
		}
		epoch, _ := diagnosticsNamespaceUintField(value, 31)
		if (messageType == diagnosticsNamespaceInitialAuthorization && epoch != 1) ||
			(messageType == diagnosticsNamespaceAuthorizationEpoch && (epoch < 2 || epoch > diagnosticsNamespaceMaximumAuthorizationEpochs+1)) {
			return errDiagnosticsNamespaceInvalid
		}
	}
	if messageType == diagnosticsNamespaceHelperEpoch {
		priorEpoch, _ := diagnosticsNamespaceUintField(value, 18)
		currentEpoch, _ := diagnosticsNamespaceUintField(value, 15)
		if priorEpoch == ^uint64(0) || currentEpoch != priorEpoch+1 {
			return errDiagnosticsNamespaceInvalid
		}
	}
	if messageType == diagnosticsNamespaceRootManifest || messageType == diagnosticsNamespaceHelperEpoch {
		readmeDigest, _ := diagnosticsNamespaceBytesField(value, 29, 32)
		expected := sha256.Sum256([]byte(diagnosticsNamespaceReadme))
		if !bytes.Equal(readmeDigest, expected[:]) {
			return errDiagnosticsNamespaceInvalid
		}
	}
	return nil
}

func diagnosticsNamespaceExpectedLabels(messageType uint64) []uint64 {
	switch messageType {
	case diagnosticsNamespaceEnablement:
		return []uint64{1, 2, 3, 4, 5, 6, 9, 10, 11, 12, 13, 14, 15, 19, 26, 27, 253}
	case diagnosticsNamespaceRootManifest:
		return []uint64{1, 2, 3, 4, 5, 6, 7, 13, 14, 15, 19, 20, 28, 29, 255}
	case diagnosticsNamespaceHelperEpoch:
		return []uint64{1, 2, 3, 4, 5, 6, 7, 13, 14, 15, 16, 17, 18, 21, 22, 28, 29, 254, 255}
	case diagnosticsNamespaceInitialAuthorization:
		return []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 21, 23, 25, 26, 27, 30, 31, 253, 255}
	case diagnosticsNamespaceAuthorizationEpoch:
		return []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 21, 23, 24, 25, 26, 27, 30, 31, 253, 255}
	default:
		return nil
	}
}

func diagnosticsNamespaceFieldValid(label uint64, value diagnosticsCBORValue) bool {
	switch label {
	case 1:
		return value.kind == diagnosticsCBORText && value.text == diagnosticsNamespaceCapabilityID
	case 2, 3:
		return value.kind == diagnosticsCBORUnsigned && value.unsigned == 1
	case 4, 12, 15, 18, 26, 27, 28, 31:
		return value.kind == diagnosticsCBORUnsigned
	case 5, 6, 7, 8, 9, 10, 11, 13, 14, 16, 17, 19, 20, 21, 22, 23, 24, 25, 29, 30:
		return value.kind == diagnosticsCBORBytes && len(value.bytes) == 32
	case 253, 254, 255:
		return value.kind == diagnosticsCBORBytes && len(value.bytes) == ed25519.SignatureSize
	default:
		return false
	}
}

func (message diagnosticsNamespaceMessage) bytesField(label uint64, length int) ([]byte, bool) {
	return diagnosticsNamespaceBytesField(message.value, label, length)
}

func (message diagnosticsNamespaceMessage) uintField(label uint64) (uint64, bool) {
	return diagnosticsNamespaceUintField(message.value, label)
}

func diagnosticsNamespaceBytesField(value diagnosticsCBORValue, label uint64, length int) ([]byte, bool) {
	field, ok := diagnosticsCBORLookup(value, label)
	if !ok || field.kind != diagnosticsCBORBytes || len(field.bytes) != length {
		return nil, false
	}
	return append([]byte(nil), field.bytes...), true
}

func diagnosticsNamespaceUintField(value diagnosticsCBORValue, label uint64) (uint64, bool) {
	field, ok := diagnosticsCBORLookup(value, label)
	if !ok || field.kind != diagnosticsCBORUnsigned {
		return 0, false
	}
	return field.unsigned, true
}

func diagnosticsNamespaceMessageType(value diagnosticsCBORValue) (uint64, bool) {
	return diagnosticsNamespaceUintField(value, 4)
}

func diagnosticsNamespaceRecordDigest(encoded []byte) ([32]byte, error) {
	if _, err := decodeDiagnosticsNamespaceMessage(encoded); err != nil {
		return [32]byte{}, errDiagnosticsNamespaceInvalid
	}
	return diagnosticsDomainSHA256(diagnosticsNamespaceRecordDigestDomain, encoded), nil
}

func diagnosticsNamespaceInstallationBinding(initialAppKeyID, homeserverBinding, folderBinding []byte) ([32]byte, error) {
	if len(initialAppKeyID) != 32 || len(homeserverBinding) != 32 || len(folderBinding) != 32 {
		return [32]byte{}, errDiagnosticsNamespaceInvalid
	}
	body := make([]byte, 0, 96)
	body = append(body, initialAppKeyID...)
	body = append(body, homeserverBinding...)
	body = append(body, folderBinding...)
	return diagnosticsDomainSHA256(diagnosticsNamespaceInstallationBindingDomain, body), nil
}

func diagnosticsNamespaceComponent(identifier []byte) (string, error) {
	if len(identifier) != 32 {
		return "", errDiagnosticsNamespaceInvalid
	}
	return strings.ToLower(diagnosticsNamespaceBase32.EncodeToString(identifier)), nil
}

func parseDiagnosticsNamespaceComponent(component string) ([]byte, error) {
	if len(component) != 52 || component != strings.ToLower(component) || strings.ContainsAny(component, ".%/\\:") {
		return nil, errDiagnosticsNamespaceInvalid
	}
	decoded, err := diagnosticsNamespaceBase32.DecodeString(strings.ToUpper(component))
	if err != nil || len(decoded) != 32 {
		return nil, errDiagnosticsNamespaceInvalid
	}
	canonical, _ := diagnosticsNamespaceComponent(decoded)
	if canonical != component {
		return nil, errDiagnosticsNamespaceInvalid
	}
	return decoded, nil
}

func diagnosticsNamespaceEpochFilename(epoch uint64, authorization bool) (string, error) {
	if epoch < 2 || (authorization && epoch > diagnosticsNamespaceMaximumAuthorizationEpochs+1) {
		return "", errDiagnosticsNamespaceInvalid
	}
	if authorization {
		return strconv.FormatUint(epoch, 10) + ".authorization.cbor", nil
	}
	return strconv.FormatUint(epoch, 10) + ".helper-manifest.cbor", nil
}

func diagnosticsNamespaceOperationFilenames(operationID []byte) ([3]string, error) {
	component, err := diagnosticsNamespaceComponent(operationID)
	if err != nil {
		return [3]string{}, err
	}
	return [3]string{
		component + ".request.cbor",
		component + ".attestation.cbor",
		component + ".response.cbor",
	}, nil
}

func validateDiagnosticsNamespaceChain(chain diagnosticsNamespaceChain) error {
	enablement, err := decodeDiagnosticsNamespaceMessage(chain.Enablement)
	if err != nil || enablement.messageType != diagnosticsNamespaceEnablement {
		return errDiagnosticsNamespaceInvalid
	}
	root, err := decodeDiagnosticsNamespaceMessage(chain.RootManifest)
	if err != nil || root.messageType != diagnosticsNamespaceRootManifest {
		return errDiagnosticsNamespaceInvalid
	}
	if !diagnosticsNamespaceCommonBindingsEqual(enablement, root, 5, 6, 13, 14, 15, 19) {
		return errDiagnosticsNamespaceInvalid
	}
	enablementDigest, _ := diagnosticsNamespaceRecordDigest(chain.Enablement)
	boundEnablement, _ := root.bytesField(20, 32)
	if !bytes.Equal(enablementDigest[:], boundEnablement) {
		return errDiagnosticsNamespaceInvalid
	}
	return validateDiagnosticsNamespacePersistentChain(chain.RootManifest, chain.HelperEpochs, chain.Authorizations)
}

func validateDiagnosticsNamespacePersistentChain(rootEncoded []byte, helperEpochs [][]byte, authorizations [][][]byte) error {
	root, err := decodeDiagnosticsNamespaceMessage(rootEncoded)
	if err != nil || root.messageType != diagnosticsNamespaceRootManifest {
		return errDiagnosticsNamespaceInvalid
	}
	rootDigest, _ := diagnosticsNamespaceRecordDigest(rootEncoded)
	currentManifest := root
	currentManifestDigest := rootDigest
	manifestByDigest := map[string]diagnosticsNamespaceMessage{string(rootDigest[:]): root}
	if len(helperEpochs) > diagnosticsNamespaceMaximumHelperEpochs {
		return errDiagnosticsNamespaceInvalid
	}
	for _, encoded := range helperEpochs {
		epoch, decodeErr := decodeDiagnosticsNamespaceMessage(encoded)
		if decodeErr != nil || epoch.messageType != diagnosticsNamespaceHelperEpoch ||
			!diagnosticsNamespaceCommonBindingsEqual(root, epoch, 5, 6, 7) {
			return errDiagnosticsNamespaceInvalid
		}
		boundRoot, _ := epoch.bytesField(21, 32)
		boundPrior, _ := epoch.bytesField(22, 32)
		priorPublic, _ := epoch.bytesField(16, 32)
		priorKeyID, _ := epoch.bytesField(17, 32)
		priorEpoch, _ := epoch.uintField(18)
		currentPublic, _ := currentManifest.bytesField(13, 32)
		currentKeyID, _ := currentManifest.bytesField(14, 32)
		currentEpoch, _ := currentManifest.uintField(15)
		if !bytes.Equal(boundRoot, rootDigest[:]) || !bytes.Equal(boundPrior, currentManifestDigest[:]) ||
			!bytes.Equal(priorPublic, currentPublic) || !bytes.Equal(priorKeyID, currentKeyID) || priorEpoch != currentEpoch {
			return errDiagnosticsNamespaceInvalid
		}
		currentManifest = epoch
		currentManifestDigest, _ = diagnosticsNamespaceRecordDigest(encoded)
		manifestByDigest[string(currentManifestDigest[:])] = epoch
	}
	if len(authorizations) > diagnosticsNamespaceMaximumInstallations {
		return errDiagnosticsNamespaceInvalid
	}
	seenInstallations := make(map[string]struct{}, len(authorizations))
	for _, records := range authorizations {
		if len(records) == 0 || len(records) > diagnosticsNamespaceMaximumAuthorizationEpochs+1 {
			return errDiagnosticsNamespaceInvalid
		}
		var priorDigest [32]byte
		var latestBoundManifest []byte
		for index, encoded := range records {
			authorization, decodeErr := decodeDiagnosticsNamespaceMessage(encoded)
			expectedType := diagnosticsNamespaceInitialAuthorization
			if index > 0 {
				expectedType = diagnosticsNamespaceAuthorizationEpoch
			}
			if decodeErr != nil || authorization.messageType != expectedType ||
				!diagnosticsNamespaceCommonBindingsEqual(root, authorization, 5, 6, 7) {
				return errDiagnosticsNamespaceInvalid
			}
			boundRoot, _ := authorization.bytesField(21, 32)
			boundManifest, _ := authorization.bytesField(23, 32)
			manifest, manifestExists := manifestByDigest[string(boundManifest)]
			authorizationEpoch, _ := authorization.uintField(31)
			if !bytes.Equal(boundRoot, rootDigest[:]) || !manifestExists ||
				!diagnosticsNamespaceCommonBindingsEqual(manifest, authorization, 13, 14, 15) || authorizationEpoch != uint64(index+1) {
				return errDiagnosticsNamespaceInvalid
			}
			latestBoundManifest = boundManifest
			if index == 0 {
				installation, _ := authorization.bytesField(8, 32)
				key := string(installation)
				if _, exists := seenInstallations[key]; exists {
					return errDiagnosticsNamespaceInvalid
				}
				seenInstallations[key] = struct{}{}
			} else {
				boundPrior, _ := authorization.bytesField(24, 32)
				if !bytes.Equal(boundPrior, priorDigest[:]) || !diagnosticsNamespaceAuthorizationIdentityEqual(records[0], authorization) {
					return errDiagnosticsNamespaceInvalid
				}
			}
			priorDigest, _ = diagnosticsNamespaceRecordDigest(encoded)
		}
		if !bytes.Equal(latestBoundManifest, currentManifestDigest[:]) {
			return errDiagnosticsNamespaceInvalid
		}
	}
	return nil
}

func validateDiagnosticsNamespacePersistentChainDuringHelperRotation(
	rootEncoded []byte,
	helperEpochs [][]byte,
	authorizations [][][]byte,
) error {
	return validateDiagnosticsNamespacePersistentChainWithHistoricalAuthorizations(
		rootEncoded,
		helperEpochs,
		authorizations,
	)
}

// validateDiagnosticsNamespacePersistentChainWithHistoricalAuthorizations
// validates the complete append-only helper chain while allowing an immutable
// installation authorization to end at any helper epoch that was current when
// that app last authorized it. This is required for revoked or lost apps: their
// signed record cannot be silently rewritten during a later helper rotation.
// Runtime session construction separately requires the selected active app's
// exact current credential-state digest, app/helper keys, and epochs.
func validateDiagnosticsNamespacePersistentChainWithHistoricalAuthorizations(
	rootEncoded []byte,
	helperEpochs [][]byte,
	authorizations [][][]byte,
) error {
	if validateDiagnosticsNamespacePersistentChain(rootEncoded, helperEpochs, authorizations) == nil {
		return nil
	}
	if len(authorizations) > diagnosticsNamespaceMaximumInstallations ||
		validateDiagnosticsNamespacePersistentChain(rootEncoded, helperEpochs, nil) != nil {
		return errDiagnosticsNamespaceInvalid
	}
	seenInstallations := make(map[string]struct{}, len(authorizations))
	for _, records := range authorizations {
		if len(records) == 0 {
			return errDiagnosticsNamespaceInvalid
		}
		initial, err := decodeDiagnosticsNamespaceMessage(records[0])
		if err != nil || initial.messageType != diagnosticsNamespaceInitialAuthorization {
			return errDiagnosticsNamespaceInvalid
		}
		installation, ok := initial.bytesField(8, 32)
		if !ok {
			return errDiagnosticsNamespaceInvalid
		}
		if _, exists := seenInstallations[string(installation)]; exists {
			return errDiagnosticsNamespaceInvalid
		}
		seenInstallations[string(installation)] = struct{}{}
		validAtHistoricalEpoch := false
		for epochCount := 0; epochCount <= len(helperEpochs); epochCount++ {
			if validateDiagnosticsNamespacePersistentChain(
				rootEncoded,
				helperEpochs[:epochCount],
				[][][]byte{records},
			) == nil {
				validAtHistoricalEpoch = true
				break
			}
		}
		if !validAtHistoricalEpoch {
			return errDiagnosticsNamespaceInvalid
		}
	}
	return nil
}

func diagnosticsNamespaceCommonBindingsEqual(left, right diagnosticsNamespaceMessage, labels ...uint64) bool {
	for _, label := range labels {
		leftValue, leftOK := diagnosticsCBORLookup(left.value, label)
		rightValue, rightOK := diagnosticsCBORLookup(right.value, label)
		if !leftOK || !rightOK || !diagnosticsCBORValuesEqual(leftValue, rightValue) {
			return false
		}
	}
	return true
}

func diagnosticsNamespaceAuthorizationIdentityEqual(initialEncoded []byte, current diagnosticsNamespaceMessage) bool {
	initial, err := decodeDiagnosticsNamespaceMessage(initialEncoded)
	if err != nil {
		return false
	}
	return diagnosticsNamespaceCommonBindingsEqual(initial, current, 5, 6, 7, 8, 9)
}

func diagnosticsCBORValuesEqual(left, right diagnosticsCBORValue) bool {
	leftBytes, leftErr := encodeDiagnosticsCBOR(left)
	rightBytes, rightErr := encodeDiagnosticsCBOR(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftBytes, rightBytes)
}
