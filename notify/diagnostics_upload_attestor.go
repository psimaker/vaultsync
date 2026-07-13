package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"sync"
	"time"
)

const (
	diagnosticsUploadMaximumPolls             = 8
	diagnosticsUploadMaximumAppRequestsMinute = 30
	diagnosticsUploadMaximumAllRequestsMinute = 120
	diagnosticsUploadMaximumAppStartsHour     = 3
	diagnosticsUploadMaximumAppStartsDay      = 12
	diagnosticsUploadMaximumHelperStartsDay   = 60
	diagnosticsUploadMaximumHelperActive      = 8
)

type diagnosticsUploadDisposition uint8

const (
	diagnosticsUploadPending diagnosticsUploadDisposition = iota
	diagnosticsUploadAccepted
	diagnosticsUploadRejected
	diagnosticsUploadUnavailable
	diagnosticsUploadConflict
	diagnosticsUploadLimited
)

type diagnosticsUploadReason uint8

const (
	diagnosticsUploadReasonRequestPending diagnosticsUploadReason = iota
	diagnosticsUploadReasonInvalidMessage
	diagnosticsUploadReasonStale
	diagnosticsUploadReasonAuthorization
	diagnosticsUploadReasonTupleBusy
	diagnosticsUploadReasonRateLimit
	diagnosticsUploadReasonNamespaceConflict
	diagnosticsUploadReasonPersistence
)

type diagnosticsUploadResult struct {
	disposition diagnosticsUploadDisposition
	reason      diagnosticsUploadReason
	attestation []byte
}

type diagnosticsUploadBinding struct {
	namespaceHandle     *diagnosticsNamespaceRootHandle
	installationBinding []byte
	homeserverBinding   []byte
	folderBinding       []byte
	appPublicKey        ed25519.PublicKey
	helperPrivateKey    ed25519.PrivateKey
	appEpoch            uint64
	helperEpoch         uint64
	authorizationEpoch  uint64
}

type diagnosticsUploadAttestorHooks struct {
	now          func() time.Time
	random       io.Reader
	afterPersist func() error
}

type diagnosticsUploadActiveOperation struct {
	operationID []byte
	query       []byte
	deadline    time.Time
	polls       int
}

type diagnosticsUploadAttestor struct {
	mutex       sync.Mutex
	binding     diagnosticsUploadBinding
	context     diagnosticsUploadVerificationContext
	coordinator *diagnosticsUploadCoordinator
	hooks       diagnosticsUploadAttestorHooks
	active      *diagnosticsUploadActiveOperation
	appID       [32]byte
	startID     [32]byte
	tupleID     [32]byte
}

type diagnosticsUploadCoordinator struct {
	mutex            sync.Mutex
	requests         []time.Time
	requestsByApp    map[[32]byte][]time.Time
	starts           []time.Time
	startsHourByPair map[[32]byte][]time.Time
	startsDayByPair  map[[32]byte][]time.Time
	active           map[[32]byte]time.Time
}

func newDiagnosticsUploadCoordinator() *diagnosticsUploadCoordinator {
	return &diagnosticsUploadCoordinator{
		requestsByApp:    make(map[[32]byte][]time.Time),
		startsHourByPair: make(map[[32]byte][]time.Time),
		startsDayByPair:  make(map[[32]byte][]time.Time),
		active:           make(map[[32]byte]time.Time),
	}
}

func newDiagnosticsUploadAttestor(binding diagnosticsUploadBinding, coordinator *diagnosticsUploadCoordinator, hooks diagnosticsUploadAttestorHooks) (*diagnosticsUploadAttestor, error) {
	if coordinator == nil || binding.namespaceHandle == nil || len(binding.installationBinding) != 32 ||
		len(binding.homeserverBinding) != 32 || len(binding.folderBinding) != 32 ||
		len(binding.appPublicKey) != ed25519.PublicKeySize || len(binding.helperPrivateKey) != ed25519.PrivateKeySize ||
		binding.appEpoch == 0 || binding.helperEpoch == 0 || binding.authorizationEpoch == 0 ||
		!nonzeroDiagnosticsBytes(binding.installationBinding) || !nonzeroDiagnosticsBytes(binding.homeserverBinding) ||
		!nonzeroDiagnosticsBytes(binding.folderBinding) ||
		!bytes.Equal(binding.helperPrivateKey, ed25519.NewKeyFromSeed(binding.helperPrivateKey[:ed25519.SeedSize])) {
		return nil, errDiagnosticsUploadInvalid
	}
	helperPublicKey := binding.helperPrivateKey.Public().(ed25519.PublicKey)
	appKeyID := diagnosticsKeyID(binding.appPublicKey)
	helperKeyID := diagnosticsKeyID(helperPublicKey)
	if hooks.now == nil {
		hooks.now = time.Now
	}
	if hooks.random == nil {
		hooks.random = rand.Reader
	}
	startBody := make([]byte, 0, 32*3)
	startBody = append(startBody, binding.homeserverBinding...)
	startBody = append(startBody, binding.folderBinding...)
	startBody = append(startBody, appKeyID[:]...)
	startID := diagnosticsDomainSHA256("eu.vaultsync.roundtrip/v1/start-scope\x00", startBody)
	tupleBody := make([]byte, 0, len(startBody)+32+24)
	tupleBody = append(tupleBody, startBody...)
	tupleBody = append(tupleBody, helperKeyID[:]...)
	epochs := make([]byte, 24)
	binary.BigEndian.PutUint64(epochs[0:8], binding.appEpoch)
	binary.BigEndian.PutUint64(epochs[8:16], binding.helperEpoch)
	binary.BigEndian.PutUint64(epochs[16:24], binding.authorizationEpoch)
	tupleBody = append(tupleBody, epochs...)
	attestor := &diagnosticsUploadAttestor{
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
		startID:     startID,
		tupleID:     diagnosticsDomainSHA256("eu.vaultsync.roundtrip/v1/tuple-lease\x00", tupleBody),
	}
	return attestor, nil
}

func (attestor *diagnosticsUploadAttestor) attest(queryBytes []byte) diagnosticsUploadResult {
	if attestor == nil {
		return diagnosticsUploadFixedResult(diagnosticsUploadUnavailable, diagnosticsUploadReasonAuthorization)
	}
	now := attestor.hooks.now()
	attestor.mutex.Lock()
	defer attestor.mutex.Unlock()
	// The local paired channel already scopes this attestor to one app key. Count
	// every call, including malformed, stale, and otherwise invalid bodies,
	// before parsing so invalid traffic cannot bypass either D024 request limit.
	if !attestor.coordinator.allowRequest(attestor.appID, now) {
		return diagnosticsUploadFixedResult(diagnosticsUploadLimited, diagnosticsUploadReasonRateLimit)
	}
	query, err := decodeDiagnosticsUploadMessage(queryBytes, attestor.context)
	if err != nil || query.messageType != diagnosticsUploadAttestationQuery {
		return diagnosticsUploadFixedResult(diagnosticsUploadRejected, diagnosticsUploadReasonInvalidMessage)
	}
	if now.Unix() < 0 || validateDiagnosticsUploadClock(query, uint64(now.Unix())) != nil {
		return diagnosticsUploadFixedResult(diagnosticsUploadRejected, diagnosticsUploadReasonStale)
	}
	operationID, _ := diagnosticsUploadBytesField(query.value, 11, 32)
	if result, ok := attestor.activateOperation(operationID, query.canonical, query, now); !ok {
		return result
	}
	attestor.active.polls++
	if attestor.active.polls > diagnosticsUploadMaximumPolls {
		return diagnosticsUploadFixedResult(diagnosticsUploadLimited, diagnosticsUploadReasonRateLimit)
	}
	if err := attestor.validateCurrentNamespaceAuthorization(); err != nil {
		return diagnosticsUploadFixedResult(diagnosticsUploadUnavailable, diagnosticsUploadReasonAuthorization)
	}

	requestPath, err := diagnosticsNamespaceOperationPath(attestor.binding.installationBinding, operationID, 1)
	if err != nil {
		return diagnosticsUploadFixedResult(diagnosticsUploadRejected, diagnosticsUploadReasonInvalidMessage)
	}
	requestBytes, _, err := attestor.binding.namespaceHandle.ReadImmutable(requestPath)
	if errors.Is(err, fs.ErrNotExist) {
		return diagnosticsUploadFixedResult(diagnosticsUploadPending, diagnosticsUploadReasonRequestPending)
	}
	if err != nil {
		return diagnosticsUploadFixedResult(diagnosticsUploadConflict, diagnosticsUploadReasonNamespaceConflict)
	}
	request, err := decodeDiagnosticsUploadMessage(requestBytes, attestor.context)
	if err != nil || request.messageType != diagnosticsUploadOperationRequest ||
		validateDiagnosticsUploadClock(request, uint64(now.Unix())) != nil ||
		validateDiagnosticsUploadRequestAndQuery(request, query) != nil ||
		attestor.validateBoundUploadMessage(request) != nil || attestor.validateBoundUploadMessage(query) != nil {
		return diagnosticsUploadFixedResult(diagnosticsUploadRejected, diagnosticsUploadReasonInvalidMessage)
	}

	attestationPath, _ := diagnosticsNamespaceOperationPath(attestor.binding.installationBinding, operationID, 2)
	if existing, _, readErr := attestor.binding.namespaceHandle.ReadImmutable(attestationPath); readErr == nil {
		return attestor.validatePersistedAttestation(request, query, existing, uint64(now.Unix()))
	} else if !errors.Is(readErr, fs.ErrNotExist) {
		return diagnosticsUploadFixedResult(diagnosticsUploadConflict, diagnosticsUploadReasonNamespaceConflict)
	}

	requestExpiresAt, _ := diagnosticsUploadUintField(request.value, 13)
	queryExpiresAt, _ := diagnosticsUploadUintField(query.value, 13)
	expiresAt := requestExpiresAt
	if queryExpiresAt < expiresAt {
		expiresAt = queryExpiresAt
	}
	nowSeconds := uint64(now.Unix())
	maximumAttestationExpiry := nowSeconds + diagnosticsUploadMaximumLifetimeSeconds
	if expiresAt > maximumAttestationExpiry {
		expiresAt = maximumAttestationExpiry
	}
	if nowSeconds >= expiresAt {
		return diagnosticsUploadFixedResult(diagnosticsUploadRejected, diagnosticsUploadReasonStale)
	}
	helperNonce := make([]byte, 32)
	if _, err := io.ReadFull(attestor.hooks.random, helperNonce); err != nil || !nonzeroDiagnosticsBytes(helperNonce) {
		return diagnosticsUploadFixedResult(diagnosticsUploadUnavailable, diagnosticsUploadReasonPersistence)
	}
	payloadDigest, _ := diagnosticsUploadBytesField(request.value, 16, 32)
	queryNonce, _ := diagnosticsUploadBytesField(query.value, 30, 32)
	appKeyID := diagnosticsKeyID(attestor.context.appPublicKey)
	helperKeyID := diagnosticsKeyID(attestor.context.helperPublicKey)
	attestationBody := diagnosticsCBORMapValue(
		diagnosticsCBORMapField(1, diagnosticsCBORTextValue(diagnosticsRoundtripCapabilityID)),
		diagnosticsCBORMapField(2, diagnosticsCBORUint(1)),
		diagnosticsCBORMapField(3, diagnosticsCBORUint(1)),
		diagnosticsCBORMapField(4, diagnosticsCBORUint(diagnosticsUploadAttestation)),
		diagnosticsCBORMapField(5, diagnosticsCBORBstr(attestor.binding.homeserverBinding)),
		diagnosticsCBORMapField(6, diagnosticsCBORBstr(attestor.binding.folderBinding)),
		diagnosticsCBORMapField(7, diagnosticsCBORBstr(appKeyID[:])),
		diagnosticsCBORMapField(8, diagnosticsCBORBstr(helperKeyID[:])),
		diagnosticsCBORMapField(9, diagnosticsCBORUint(attestor.binding.appEpoch)),
		diagnosticsCBORMapField(10, diagnosticsCBORUint(attestor.binding.helperEpoch)),
		diagnosticsCBORMapField(11, diagnosticsCBORBstr(operationID)),
		diagnosticsCBORMapField(12, diagnosticsCBORUint(nowSeconds)),
		diagnosticsCBORMapField(13, diagnosticsCBORUint(expiresAt)),
		diagnosticsCBORMapField(16, diagnosticsCBORBstr(payloadDigest)),
		diagnosticsCBORMapField(17, diagnosticsCBORBstr(request.digest[:])),
		diagnosticsCBORMapField(18, diagnosticsCBORBstr(helperNonce)),
		diagnosticsCBORMapField(19, diagnosticsCBORUint(nowSeconds)),
		diagnosticsCBORMapField(30, diagnosticsCBORBstr(queryNonce)),
		diagnosticsCBORMapField(31, diagnosticsCBORBstr(query.digest[:])),
	)
	attestationBytes, err := signDiagnosticsUploadAttestation(attestationBody, attestor.binding.helperPrivateKey, attestor.context)
	if err != nil {
		return diagnosticsUploadFixedResult(diagnosticsUploadRejected, diagnosticsUploadReasonInvalidMessage)
	}
	if _, err := attestor.binding.namespaceHandle.CreateImmutableAtomic(attestationPath, attestationBytes); err != nil {
		if errors.Is(err, errDiagnosticsNamespaceCollision) {
			existing, _, readErr := attestor.binding.namespaceHandle.ReadImmutable(attestationPath)
			if readErr == nil {
				return attestor.validatePersistedAttestation(request, query, existing, nowSeconds)
			}
		}
		return diagnosticsUploadFixedResult(diagnosticsUploadConflict, diagnosticsUploadReasonPersistence)
	}
	if attestor.hooks.afterPersist != nil {
		if err := attestor.hooks.afterPersist(); err != nil {
			return diagnosticsUploadFixedResult(diagnosticsUploadUnavailable, diagnosticsUploadReasonPersistence)
		}
	}
	return attestor.validatePersistedAttestation(request, query, attestationBytes, nowSeconds)
}

func (attestor *diagnosticsUploadAttestor) validatePersistedAttestation(request, query diagnosticsUploadMessage, encoded []byte, now uint64) diagnosticsUploadResult {
	attestation, err := decodeDiagnosticsUploadMessage(encoded, attestor.context)
	if err != nil || validateDiagnosticsUploadClock(attestation, now) != nil ||
		validateDiagnosticsUploadChain(request, query, attestation) != nil {
		return diagnosticsUploadFixedResult(diagnosticsUploadConflict, diagnosticsUploadReasonNamespaceConflict)
	}
	return diagnosticsUploadResult{
		disposition: diagnosticsUploadAccepted,
		attestation: append([]byte(nil), encoded...),
	}
}

func (attestor *diagnosticsUploadAttestor) validateBoundUploadMessage(message diagnosticsUploadMessage) error {
	homeserver, _ := diagnosticsUploadBytesField(message.value, 5, 32)
	folder, _ := diagnosticsUploadBytesField(message.value, 6, 32)
	appEpoch, _ := diagnosticsUploadUintField(message.value, 9)
	helperEpoch, _ := diagnosticsUploadUintField(message.value, 10)
	if !bytes.Equal(homeserver, attestor.binding.homeserverBinding) ||
		!bytes.Equal(folder, attestor.binding.folderBinding) || appEpoch != attestor.binding.appEpoch ||
		helperEpoch != attestor.binding.helperEpoch {
		return errDiagnosticsUploadInvalid
	}
	return nil
}

func (attestor *diagnosticsUploadAttestor) validateCurrentNamespaceAuthorization() error {
	if err := attestor.binding.namespaceHandle.ScanFixedLayout(); err != nil {
		return err
	}
	paths, err := diagnosticsNamespaceAuthorizationPaths(attestor.binding.installationBinding)
	if err != nil {
		return err
	}
	authorizationPath := paths[0]
	if attestor.binding.authorizationEpoch > 1 {
		authorizationPath, err = diagnosticsNamespaceAuthorizationEpochPath(attestor.binding.installationBinding, attestor.binding.authorizationEpoch)
		if err != nil {
			return err
		}
	}
	body, _, err := attestor.binding.namespaceHandle.ReadImmutable(authorizationPath)
	if err != nil {
		return err
	}
	authorization, err := decodeDiagnosticsNamespaceMessage(body)
	if err != nil {
		return err
	}
	installation, _ := authorization.bytesField(8, 32)
	homeserver, _ := authorization.bytesField(5, 32)
	folder, _ := authorization.bytesField(6, 32)
	appPublicKey, _ := authorization.bytesField(10, ed25519.PublicKeySize)
	helperPublicKey, _ := authorization.bytesField(13, ed25519.PublicKeySize)
	appEpoch, _ := authorization.uintField(12)
	helperEpoch, _ := authorization.uintField(15)
	authorizationEpoch, _ := authorization.uintField(31)
	if !bytes.Equal(installation, attestor.binding.installationBinding) ||
		!bytes.Equal(homeserver, attestor.binding.homeserverBinding) || !bytes.Equal(folder, attestor.binding.folderBinding) ||
		!bytes.Equal(appPublicKey, attestor.context.appPublicKey) || !bytes.Equal(helperPublicKey, attestor.context.helperPublicKey) ||
		appEpoch != attestor.binding.appEpoch || helperEpoch != attestor.binding.helperEpoch ||
		authorizationEpoch != attestor.binding.authorizationEpoch {
		return errDiagnosticsUploadInvalid
	}
	return nil
}

func (attestor *diagnosticsUploadAttestor) activateOperation(operationID, queryBytes []byte, query diagnosticsUploadMessage, now time.Time) (diagnosticsUploadResult, bool) {
	if attestor.active != nil && !now.Before(attestor.active.deadline) {
		attestor.coordinator.finish(attestor.tupleID)
		attestor.active = nil
	}
	if attestor.active != nil {
		if !bytes.Equal(attestor.active.operationID, operationID) || !bytes.Equal(attestor.active.query, queryBytes) {
			return diagnosticsUploadFixedResult(diagnosticsUploadRejected, diagnosticsUploadReasonTupleBusy), false
		}
		return diagnosticsUploadResult{}, true
	}
	expiresAt, _ := diagnosticsUploadUintField(query.value, 13)
	wallRemaining := time.Unix(int64(expiresAt), 0).Sub(now)
	if wallRemaining <= 0 {
		return diagnosticsUploadFixedResult(diagnosticsUploadRejected, diagnosticsUploadReasonStale), false
	}
	deadlineDuration := time.Duration(diagnosticsUploadMaximumLifetimeSeconds) * time.Second
	if wallRemaining < deadlineDuration {
		deadlineDuration = wallRemaining
	}
	deadline := now.Add(deadlineDuration)
	if !attestor.coordinator.begin(attestor.tupleID, attestor.startID, now, deadline) {
		return diagnosticsUploadFixedResult(diagnosticsUploadLimited, diagnosticsUploadReasonRateLimit), false
	}
	attestor.active = &diagnosticsUploadActiveOperation{
		operationID: append([]byte(nil), operationID...),
		query:       append([]byte(nil), queryBytes...),
		deadline:    deadline,
	}
	return diagnosticsUploadResult{}, true
}

func (coordinator *diagnosticsUploadCoordinator) allowRequest(appID [32]byte, now time.Time) bool {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	appRequests := coordinator.requestsByApp[appID]
	appAllowed := allowDiagnosticsUploadWindow(&appRequests, now, time.Minute, diagnosticsUploadMaximumAppRequestsMinute)
	coordinator.requestsByApp[appID] = appRequests
	helperAllowed := allowDiagnosticsUploadWindow(&coordinator.requests, now, time.Minute, diagnosticsUploadMaximumAllRequestsMinute)
	return appAllowed && helperAllowed
}

func (coordinator *diagnosticsUploadCoordinator) begin(tupleID, startID [32]byte, now, deadline time.Time) bool {
	coordinator.mutex.Lock()
	defer coordinator.mutex.Unlock()
	for key, expiry := range coordinator.active {
		if !now.Before(expiry) {
			delete(coordinator.active, key)
		}
	}
	hourStarts := pruneDiagnosticsUploadWindow(coordinator.startsHourByPair[startID], now, time.Hour)
	dayStarts := pruneDiagnosticsUploadWindow(coordinator.startsDayByPair[startID], now, 24*time.Hour)
	helperStarts := pruneDiagnosticsUploadWindow(coordinator.starts, now, 24*time.Hour)
	coordinator.startsHourByPair[startID] = hourStarts
	coordinator.startsDayByPair[startID] = dayStarts
	coordinator.starts = helperStarts
	if _, exists := coordinator.active[tupleID]; exists || len(coordinator.active) >= diagnosticsUploadMaximumHelperActive ||
		len(hourStarts) >= diagnosticsUploadMaximumAppStartsHour || len(dayStarts) >= diagnosticsUploadMaximumAppStartsDay ||
		len(helperStarts) >= diagnosticsUploadMaximumHelperStartsDay {
		return false
	}
	coordinator.startsHourByPair[startID] = append(hourStarts, now)
	coordinator.startsDayByPair[startID] = append(dayStarts, now)
	coordinator.starts = append(helperStarts, now)
	coordinator.active[tupleID] = deadline
	return true
}

func (coordinator *diagnosticsUploadCoordinator) finish(tupleID [32]byte) {
	coordinator.mutex.Lock()
	delete(coordinator.active, tupleID)
	coordinator.mutex.Unlock()
}

func allowDiagnosticsUploadWindow(events *[]time.Time, now time.Time, window time.Duration, limit int) bool {
	kept := pruneDiagnosticsUploadWindow(*events, now, window)
	*events = kept
	if len(*events) >= limit {
		return false
	}
	*events = append(*events, now)
	return true
}

func pruneDiagnosticsUploadWindow(events []time.Time, now time.Time, window time.Duration) []time.Time {
	cutoff := now.Add(-window)
	kept := events[:0]
	for _, event := range events {
		if event.After(cutoff) {
			kept = append(kept, event)
		}
	}
	return kept
}

func diagnosticsUploadFixedResult(disposition diagnosticsUploadDisposition, reason diagnosticsUploadReason) diagnosticsUploadResult {
	return diagnosticsUploadResult{disposition: disposition, reason: reason}
}

func nonzeroDiagnosticsBytes(value []byte) bool {
	var combined byte
	for _, item := range value {
		combined |= item
	}
	return combined != 0
}
