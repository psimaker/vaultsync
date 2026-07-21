package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"sync"
	"time"
)

const (
	diagnosticsResponseRequestPathKind     uint64 = 1
	diagnosticsResponseAttestationPathKind uint64 = 2
	diagnosticsResponseArtifactPathKind    uint64 = 3
)

type diagnosticsResponseDisposition uint8

const (
	diagnosticsResponseAccepted diagnosticsResponseDisposition = iota
	diagnosticsResponseRejected
	diagnosticsResponseUnavailable
	diagnosticsResponseConflict
	diagnosticsResponseLimited
)

type diagnosticsResponseReason uint8

const (
	diagnosticsResponseReasonNone diagnosticsResponseReason = iota
	diagnosticsResponseReasonInvalidMessage
	diagnosticsResponseReasonStale
	diagnosticsResponseReasonAuthorization
	diagnosticsResponseReasonRateLimit
	diagnosticsResponseReasonNamespaceConflict
	diagnosticsResponseReasonPersistence
)

type diagnosticsResponseResult struct {
	disposition    diagnosticsResponseDisposition
	reason         diagnosticsResponseReason
	acknowledgment []byte
}

type diagnosticsResponseFoundationHooks struct {
	now                  func() time.Time
	random               io.Reader
	afterResponsePersist func() error
	afterCleanup         func() error
}

// diagnosticsResponseFoundation remains a transport-independent helper core.
// The explicit opt-in runtime constructs it only after exact D023
// authorization; the core itself owns no listener, HTTP transport, discovery,
// folder configuration, app evidence, or durable operation store.
type diagnosticsResponseFoundation struct {
	mutex       sync.Mutex
	binding     diagnosticsUploadBinding
	context     diagnosticsUploadVerificationContext
	coordinator *diagnosticsUploadCoordinator
	hooks       diagnosticsResponseFoundationHooks
	appID       [32]byte
}

type diagnosticsCleanupCandidate struct {
	kind          uint64
	path          diagnosticsNamespacePath
	identity      diagnosticsNamespaceFileIdentity
	fileDigest    [32]byte
	messageDigest [32]byte
	expiresAt     uint64
}

func newDiagnosticsResponseFoundation(
	binding diagnosticsUploadBinding,
	coordinator *diagnosticsUploadCoordinator,
	hooks diagnosticsResponseFoundationHooks,
) (*diagnosticsResponseFoundation, error) {
	if coordinator == nil || binding.namespaceHandle == nil || len(binding.installationBinding) != 32 ||
		len(binding.homeserverBinding) != 32 || len(binding.folderBinding) != 32 ||
		len(binding.appPublicKey) != ed25519.PublicKeySize || len(binding.helperPrivateKey) != ed25519.PrivateKeySize ||
		binding.appEpoch == 0 || binding.helperEpoch == 0 || binding.authorizationEpoch == 0 ||
		!nonzeroDiagnosticsBytes(binding.installationBinding) || !nonzeroDiagnosticsBytes(binding.homeserverBinding) ||
		!nonzeroDiagnosticsBytes(binding.folderBinding) ||
		!bytes.Equal(binding.helperPrivateKey, ed25519.NewKeyFromSeed(binding.helperPrivateKey[:ed25519.SeedSize])) {
		return nil, errDiagnosticsResponseInvalid
	}
	if hooks.now == nil {
		hooks.now = time.Now
	}
	if hooks.random == nil {
		hooks.random = rand.Reader
	}
	helperPublicKey := binding.helperPrivateKey.Public().(ed25519.PublicKey)
	appKeyID := diagnosticsKeyID(binding.appPublicKey)
	return &diagnosticsResponseFoundation{
		binding: diagnosticsUploadBinding{
			namespaceHandle:     binding.namespaceHandle,
			installationBinding: append([]byte(nil), binding.installationBinding...),
			homeserverBinding:   append([]byte(nil), binding.homeserverBinding...),
			folderBinding:       append([]byte(nil), binding.folderBinding...),
			appPublicKey:        append(ed25519.PublicKey(nil), binding.appPublicKey...),
			helperPrivateKey:    append(ed25519.PrivateKey(nil), binding.helperPrivateKey...),
			appEpoch:            binding.appEpoch,
			helperEpoch:         binding.helperEpoch,
			authorizationEpoch:  binding.authorizationEpoch,
		},
		context: diagnosticsUploadVerificationContext{
			appPublicKey:    append(ed25519.PublicKey(nil), binding.appPublicKey...),
			helperPublicKey: append(ed25519.PublicKey(nil), helperPublicKey...),
		},
		coordinator: coordinator,
		hooks:       hooks,
		appID:       appKeyID,
	}, nil
}

func (foundation *diagnosticsResponseFoundation) authorizeResponse(authorizationBytes []byte) diagnosticsResponseResult {
	if foundation == nil {
		return diagnosticsResponseFixedResult(diagnosticsResponseUnavailable, diagnosticsResponseReasonAuthorization)
	}
	now := foundation.hooks.now()
	foundation.mutex.Lock()
	defer foundation.mutex.Unlock()
	if !foundation.coordinator.allowRequest(foundation.appID, now) {
		return diagnosticsResponseFixedResult(diagnosticsResponseLimited, diagnosticsResponseReasonRateLimit)
	}
	authorization, err := decodeDiagnosticsResponseMessage(authorizationBytes, foundation.context)
	if err != nil || authorization.messageType != diagnosticsResponseAuthorization {
		return diagnosticsResponseFixedResult(diagnosticsResponseRejected, diagnosticsResponseReasonInvalidMessage)
	}
	if now.Unix() < 0 || validateDiagnosticsResponseClock(authorization, uint64(now.Unix())) != nil {
		return diagnosticsResponseFixedResult(diagnosticsResponseRejected, diagnosticsResponseReasonStale)
	}
	if foundation.validateBoundResponseMessage(authorization) != nil ||
		validateDiagnosticsBindingAuthorization(foundation.binding, foundation.context) != nil {
		return diagnosticsResponseFixedResult(diagnosticsResponseUnavailable, diagnosticsResponseReasonAuthorization)
	}
	operationID, _ := diagnosticsUploadBytesField(authorization.value, 11, 32)
	requestPath, pathErr := diagnosticsNamespaceOperationPath(
		foundation.binding.installationBinding, operationID, diagnosticsResponseRequestPathKind,
	)
	if pathErr != nil {
		return diagnosticsResponseFixedResult(diagnosticsResponseRejected, diagnosticsResponseReasonInvalidMessage)
	}
	attestationPath, _ := diagnosticsNamespaceOperationPath(
		foundation.binding.installationBinding, operationID, diagnosticsResponseAttestationPathKind,
	)
	responsePath, _ := diagnosticsNamespaceOperationPath(
		foundation.binding.installationBinding, operationID, diagnosticsResponseArtifactPathKind,
	)
	requestBytes, _, requestErr := foundation.binding.namespaceHandle.ReadImmutable(requestPath)
	attestationBytes, _, attestationErr := foundation.binding.namespaceHandle.ReadImmutable(attestationPath)
	if errors.Is(requestErr, fs.ErrNotExist) || errors.Is(attestationErr, fs.ErrNotExist) {
		return diagnosticsResponseFixedResult(diagnosticsResponseRejected, diagnosticsResponseReasonAuthorization)
	}
	if requestErr != nil || attestationErr != nil {
		return diagnosticsResponseFixedResult(diagnosticsResponseConflict, diagnosticsResponseReasonNamespaceConflict)
	}
	request, requestErr := decodeDiagnosticsUploadMessage(requestBytes, foundation.context)
	attestation, attestationErr := decodeDiagnosticsUploadMessage(attestationBytes, foundation.context)
	nowSeconds := uint64(now.Unix())
	if requestErr != nil || attestationErr != nil || validateDiagnosticsUploadClock(request, nowSeconds) != nil ||
		validateDiagnosticsUploadClock(attestation, nowSeconds) != nil ||
		foundation.validateBoundUploadMessage(request) != nil || foundation.validateBoundUploadMessage(attestation) != nil ||
		validateDiagnosticsResponseAuthorizationChain(request, attestation, authorization) != nil {
		return diagnosticsResponseFixedResult(diagnosticsResponseRejected, diagnosticsResponseReasonInvalidMessage)
	}
	if existing, _, readErr := foundation.binding.namespaceHandle.ReadImmutable(responsePath); readErr == nil {
		return foundation.validatePersistedResponse(request, attestation, authorization, existing, nowSeconds)
	} else if !errors.Is(readErr, fs.ErrNotExist) {
		return diagnosticsResponseFixedResult(diagnosticsResponseConflict, diagnosticsResponseReasonNamespaceConflict)
	}

	expiresAt := minimumDiagnosticsExpiry(request.value, attestation.value, authorization.value)
	maximumResponseExpiry := nowSeconds + diagnosticsUploadMaximumLifetimeSeconds
	if expiresAt > maximumResponseExpiry {
		expiresAt = maximumResponseExpiry
	}
	if nowSeconds >= expiresAt {
		return diagnosticsResponseFixedResult(diagnosticsResponseRejected, diagnosticsResponseReasonStale)
	}
	responseNonce := make([]byte, 32)
	responsePayload := make([]byte, diagnosticsResponsePayloadBytes)
	if _, err := io.ReadFull(foundation.hooks.random, responseNonce); err != nil || !nonzeroDiagnosticsBytes(responseNonce) {
		return diagnosticsResponseFixedResult(diagnosticsResponseUnavailable, diagnosticsResponseReasonPersistence)
	}
	if _, err := io.ReadFull(foundation.hooks.random, responsePayload); err != nil {
		return diagnosticsResponseFixedResult(diagnosticsResponseUnavailable, diagnosticsResponseReasonPersistence)
	}
	responsePayloadDigest := sha256.Sum256(responsePayload)
	appKeyID := diagnosticsKeyID(foundation.context.appPublicKey)
	helperKeyID := diagnosticsKeyID(foundation.context.helperPublicKey)
	responseBody := diagnosticsCBORMapValue(
		diagnosticsCBORMapField(1, diagnosticsCBORTextValue(diagnosticsRoundtripCapabilityID)),
		diagnosticsCBORMapField(2, diagnosticsCBORUint(diagnosticsProtocolMajor)),
		diagnosticsCBORMapField(3, diagnosticsCBORUint(diagnosticsCryptographicSuite)),
		diagnosticsCBORMapField(4, diagnosticsCBORUint(diagnosticsResponseArtifact)),
		diagnosticsCBORMapField(5, diagnosticsCBORBstr(foundation.binding.homeserverBinding)),
		diagnosticsCBORMapField(6, diagnosticsCBORBstr(foundation.binding.folderBinding)),
		diagnosticsCBORMapField(7, diagnosticsCBORBstr(appKeyID[:])),
		diagnosticsCBORMapField(8, diagnosticsCBORBstr(helperKeyID[:])),
		diagnosticsCBORMapField(9, diagnosticsCBORUint(foundation.binding.appEpoch)),
		diagnosticsCBORMapField(10, diagnosticsCBORUint(foundation.binding.helperEpoch)),
		diagnosticsCBORMapField(11, diagnosticsCBORBstr(operationID)),
		diagnosticsCBORMapField(12, diagnosticsCBORUint(nowSeconds)),
		diagnosticsCBORMapField(13, diagnosticsCBORUint(expiresAt)),
		diagnosticsCBORMapField(17, diagnosticsCBORBstr(request.digest[:])),
		diagnosticsCBORMapField(20, diagnosticsCBORBstr(attestation.digest[:])),
		diagnosticsCBORMapField(22, diagnosticsCBORBstr(authorization.digest[:])),
		diagnosticsCBORMapField(23, diagnosticsCBORBstr(responseNonce)),
		diagnosticsCBORMapField(24, diagnosticsCBORBstr(responsePayload)),
		diagnosticsCBORMapField(25, diagnosticsCBORBstr(responsePayloadDigest[:])),
	)
	responseBytes, err := signDiagnosticsHelperResponseMessage(
		responseBody, diagnosticsResponseArtifact, foundation.binding.helperPrivateKey, foundation.context,
	)
	if err != nil {
		return diagnosticsResponseFixedResult(diagnosticsResponseRejected, diagnosticsResponseReasonInvalidMessage)
	}
	if _, err := foundation.binding.namespaceHandle.CreateImmutableAtomic(responsePath, responseBytes); err != nil {
		if errors.Is(err, errDiagnosticsNamespaceCollision) {
			existing, _, readErr := foundation.binding.namespaceHandle.ReadImmutable(responsePath)
			if readErr == nil {
				return foundation.validatePersistedResponse(request, attestation, authorization, existing, nowSeconds)
			}
		}
		return diagnosticsResponseFixedResult(diagnosticsResponseConflict, diagnosticsResponseReasonPersistence)
	}
	if foundation.hooks.afterResponsePersist != nil {
		if err := foundation.hooks.afterResponsePersist(); err != nil {
			return diagnosticsResponseFixedResult(diagnosticsResponseUnavailable, diagnosticsResponseReasonPersistence)
		}
	}
	return foundation.validatePersistedResponse(request, attestation, authorization, responseBytes, nowSeconds)
}

func (foundation *diagnosticsResponseFoundation) cleanup(requestBytes []byte) diagnosticsResponseResult {
	if foundation == nil {
		return diagnosticsResponseFixedResult(diagnosticsResponseUnavailable, diagnosticsResponseReasonAuthorization)
	}
	now := foundation.hooks.now()
	foundation.mutex.Lock()
	defer foundation.mutex.Unlock()
	if !foundation.coordinator.allowRequest(foundation.appID, now) {
		return diagnosticsResponseFixedResult(diagnosticsResponseLimited, diagnosticsResponseReasonRateLimit)
	}
	request, err := decodeDiagnosticsResponseMessage(requestBytes, foundation.context)
	if err != nil || request.messageType != diagnosticsCleanupRequest {
		return diagnosticsResponseFixedResult(diagnosticsResponseRejected, diagnosticsResponseReasonInvalidMessage)
	}
	if now.Unix() < 0 || validateDiagnosticsResponseClock(request, uint64(now.Unix())) != nil {
		return diagnosticsResponseFixedResult(diagnosticsResponseRejected, diagnosticsResponseReasonStale)
	}
	nowSeconds := uint64(now.Unix())
	requestExpiresAt, _ := diagnosticsUploadUintField(request.value, 13)
	// A cleanup that cannot produce a live acknowledgement must not mutate the
	// namespace. Wall-clock skew permits message verification near a boundary;
	// it never extends the signed operation lifetime or authorizes deletion.
	if nowSeconds >= requestExpiresAt {
		return diagnosticsResponseFixedResult(diagnosticsResponseRejected, diagnosticsResponseReasonStale)
	}
	if foundation.validateBoundResponseMessage(request) != nil ||
		validateDiagnosticsBindingAuthorization(foundation.binding, foundation.context) != nil {
		return diagnosticsResponseFixedResult(diagnosticsResponseUnavailable, diagnosticsResponseReasonAuthorization)
	}
	operationID, _ := diagnosticsUploadBytesField(request.value, 11, 32)
	targets, _ := diagnosticsCleanupTargets(request.value)
	candidates, hasConflict := foundation.readCleanupCandidates(operationID)
	results := make([]uint64, len(targets))
	for index, target := range targets {
		candidateIndex := -1
		for item := range candidates {
			if bytes.Equal(candidates[item].messageDigest[:], target) {
				candidateIndex = item
				break
			}
		}
		if candidateIndex < 0 {
			if hasConflict {
				results[index] = diagnosticsCleanupRetainedConflict
			} else {
				results[index] = diagnosticsCleanupAlreadyAbsent
			}
			continue
		}
		candidate := candidates[candidateIndex]
		if candidate.kind == diagnosticsResponseRequestPathKind && !diagnosticsArtifactExpired(candidate.expiresAt, nowSeconds) {
			results[index] = diagnosticsCleanupRetainedConflict
			continue
		}
		cleanup, cleanupErr := foundation.binding.namespaceHandle.CleanupOwned([]diagnosticsNamespaceOwnedArtifact{{
			path: candidate.path, identity: candidate.identity, digest: candidate.fileDigest,
		}})
		switch {
		case cleanupErr == nil && cleanup.Removed == 1:
			results[index] = diagnosticsCleanupDeleted
		case cleanupErr == nil && cleanup.Missing == 1:
			results[index] = diagnosticsCleanupAlreadyAbsent
		case errors.Is(cleanupErr, errDiagnosticsNamespaceConflict) || cleanup.Conflicts > 0:
			results[index] = diagnosticsCleanupRetainedConflict
		default:
			results[index] = diagnosticsCleanupFailed
		}
	}
	if foundation.hooks.afterCleanup != nil {
		if err := foundation.hooks.afterCleanup(); err != nil {
			return diagnosticsResponseFixedResult(diagnosticsResponseUnavailable, diagnosticsResponseReasonPersistence)
		}
	}

	expiresAt := requestExpiresAt
	maximumAcknowledgmentExpiry := nowSeconds + diagnosticsUploadMaximumLifetimeSeconds
	if expiresAt > maximumAcknowledgmentExpiry {
		expiresAt = maximumAcknowledgmentExpiry
	}
	targetValues := make([]diagnosticsCBORValue, len(targets))
	resultValues := make([]diagnosticsCBORValue, len(results))
	for index := range targets {
		targetValues[index] = diagnosticsCBORBstr(targets[index])
		resultValues[index] = diagnosticsCBORUint(results[index])
	}
	appKeyID := diagnosticsKeyID(foundation.context.appPublicKey)
	helperKeyID := diagnosticsKeyID(foundation.context.helperPublicKey)
	acknowledgmentBody := diagnosticsCBORMapValue(
		diagnosticsCBORMapField(1, diagnosticsCBORTextValue(diagnosticsRoundtripCapabilityID)),
		diagnosticsCBORMapField(2, diagnosticsCBORUint(diagnosticsProtocolMajor)),
		diagnosticsCBORMapField(3, diagnosticsCBORUint(diagnosticsCryptographicSuite)),
		diagnosticsCBORMapField(4, diagnosticsCBORUint(diagnosticsCleanupAcknowledgment)),
		diagnosticsCBORMapField(5, diagnosticsCBORBstr(foundation.binding.homeserverBinding)),
		diagnosticsCBORMapField(6, diagnosticsCBORBstr(foundation.binding.folderBinding)),
		diagnosticsCBORMapField(7, diagnosticsCBORBstr(appKeyID[:])),
		diagnosticsCBORMapField(8, diagnosticsCBORBstr(helperKeyID[:])),
		diagnosticsCBORMapField(9, diagnosticsCBORUint(foundation.binding.appEpoch)),
		diagnosticsCBORMapField(10, diagnosticsCBORUint(foundation.binding.helperEpoch)),
		diagnosticsCBORMapField(11, diagnosticsCBORBstr(operationID)),
		diagnosticsCBORMapField(12, diagnosticsCBORUint(nowSeconds)),
		diagnosticsCBORMapField(13, diagnosticsCBORUint(expiresAt)),
		diagnosticsCBORMapField(28, diagnosticsCBORArrayValue(targetValues...)),
		diagnosticsCBORMapField(29, diagnosticsCBORArrayValue(resultValues...)),
		diagnosticsCBORMapField(31, diagnosticsCBORBstr(request.digest[:])),
	)
	acknowledgment, err := signDiagnosticsHelperResponseMessage(
		acknowledgmentBody, diagnosticsCleanupAcknowledgment, foundation.binding.helperPrivateKey, foundation.context,
	)
	if err != nil {
		return diagnosticsResponseFixedResult(diagnosticsResponseUnavailable, diagnosticsResponseReasonPersistence)
	}
	decoded, err := decodeDiagnosticsResponseMessage(acknowledgment, foundation.context)
	if err != nil || validateDiagnosticsCleanupAcknowledgmentChain(request, decoded) != nil {
		return diagnosticsResponseFixedResult(diagnosticsResponseUnavailable, diagnosticsResponseReasonPersistence)
	}
	return diagnosticsResponseResult{
		disposition:    diagnosticsResponseAccepted,
		acknowledgment: append([]byte(nil), acknowledgment...),
	}
}

func (foundation *diagnosticsResponseFoundation) validatePersistedResponse(
	request, attestation diagnosticsUploadMessage,
	authorization diagnosticsResponseMessage,
	encoded []byte,
	now uint64,
) diagnosticsResponseResult {
	response, err := decodeDiagnosticsResponseMessage(encoded, foundation.context)
	if err != nil || validateDiagnosticsResponseClock(response, now) != nil ||
		validateDiagnosticsResponseArtifactChain(request, attestation, authorization, response) != nil ||
		foundation.validateBoundResponseMessage(response) != nil {
		return diagnosticsResponseFixedResult(diagnosticsResponseConflict, diagnosticsResponseReasonNamespaceConflict)
	}
	return diagnosticsResponseFixedResult(diagnosticsResponseAccepted, diagnosticsResponseReasonNone)
}

func (foundation *diagnosticsResponseFoundation) validateBoundUploadMessage(message diagnosticsUploadMessage) error {
	return foundation.validateBoundValue(message.value)
}

func (foundation *diagnosticsResponseFoundation) validateBoundResponseMessage(message diagnosticsResponseMessage) error {
	return foundation.validateBoundValue(message.value)
}

func (foundation *diagnosticsResponseFoundation) validateBoundValue(value diagnosticsCBORValue) error {
	homeserver, _ := diagnosticsUploadBytesField(value, 5, 32)
	folder, _ := diagnosticsUploadBytesField(value, 6, 32)
	appEpoch, _ := diagnosticsUploadUintField(value, 9)
	helperEpoch, _ := diagnosticsUploadUintField(value, 10)
	if !bytes.Equal(homeserver, foundation.binding.homeserverBinding) ||
		!bytes.Equal(folder, foundation.binding.folderBinding) || appEpoch != foundation.binding.appEpoch ||
		helperEpoch != foundation.binding.helperEpoch {
		return errDiagnosticsResponseInvalid
	}
	return nil
}

func (foundation *diagnosticsResponseFoundation) readCleanupCandidates(operationID []byte) ([]diagnosticsCleanupCandidate, bool) {
	candidates := make([]diagnosticsCleanupCandidate, 0, 3)
	hasConflict := false
	for kind := diagnosticsResponseRequestPathKind; kind <= diagnosticsResponseArtifactPathKind; kind++ {
		path, err := diagnosticsNamespaceOperationPath(foundation.binding.installationBinding, operationID, kind)
		if err != nil {
			return candidates, true
		}
		body, identity, err := foundation.binding.namespaceHandle.ReadImmutable(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			hasConflict = true
			continue
		}
		candidate := diagnosticsCleanupCandidate{
			kind: kind, path: path, identity: identity, fileDigest: sha256.Sum256(body),
		}
		switch kind {
		case diagnosticsResponseRequestPathKind, diagnosticsResponseAttestationPathKind:
			message, decodeErr := decodeDiagnosticsUploadMessage(body, foundation.context)
			expectedType := diagnosticsUploadOperationRequest
			if kind == diagnosticsResponseAttestationPathKind {
				expectedType = diagnosticsUploadAttestation
			}
			if decodeErr != nil || message.messageType != expectedType || foundation.validateBoundUploadMessage(message) != nil ||
				!diagnosticsMessageOperationEqual(message.value, operationID) {
				hasConflict = true
				continue
			}
			candidate.messageDigest = message.digest
			candidate.expiresAt, _ = diagnosticsUploadUintField(message.value, 13)
		case diagnosticsResponseArtifactPathKind:
			message, decodeErr := decodeDiagnosticsResponseMessage(body, foundation.context)
			if decodeErr != nil || message.messageType != diagnosticsResponseArtifact ||
				foundation.validateBoundResponseMessage(message) != nil || !diagnosticsMessageOperationEqual(message.value, operationID) {
				hasConflict = true
				continue
			}
			candidate.messageDigest = message.digest
			candidate.expiresAt, _ = diagnosticsUploadUintField(message.value, 13)
		}
		candidates = append(candidates, candidate)
	}
	return candidates, hasConflict
}

func diagnosticsMessageOperationEqual(value diagnosticsCBORValue, operationID []byte) bool {
	actual, ok := diagnosticsUploadBytesField(value, 11, 32)
	return ok && bytes.Equal(actual, operationID)
}

func diagnosticsArtifactExpired(expiresAt, now uint64) bool {
	return now > expiresAt && now-expiresAt > diagnosticsUploadMaximumClockSkewSeconds
}

func minimumDiagnosticsExpiry(values ...diagnosticsCBORValue) uint64 {
	minimum := ^uint64(0)
	for _, value := range values {
		expiresAt, ok := diagnosticsUploadUintField(value, 13)
		if !ok {
			return 0
		}
		if expiresAt < minimum {
			minimum = expiresAt
		}
	}
	return minimum
}

func diagnosticsResponseFixedResult(disposition diagnosticsResponseDisposition, reason diagnosticsResponseReason) diagnosticsResponseResult {
	return diagnosticsResponseResult{disposition: disposition, reason: reason}
}
