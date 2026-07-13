package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

const (
	diagnosticsPairingLifetime              = 5 * time.Minute
	diagnosticsPairingClockSkew             = 2 * time.Minute
	diagnosticsPairingTerminalReplay        = 24 * time.Hour
	diagnosticsPairingRequestLimitPerMinute = 30
	diagnosticsPairingInvitationAttempts    = 10
	diagnosticsPairingMaximumPending        = 4
)

var (
	errDiagnosticsPairingUnavailable = errors.New("diagnostics pairing unavailable")
	errDiagnosticsPairingExpired     = errors.New("diagnostics pairing expired")
	errDiagnosticsPairingRateLimited = errors.New("diagnostics pairing rate limited")
)

type diagnosticsPairingManager struct {
	store       *diagnosticsCredentialStore
	random      io.Reader
	now         func() time.Time
	mutex       sync.Mutex
	invitations map[string]*diagnosticsPairingInvitation
}

type diagnosticsPairingInvitation struct {
	message      diagnosticsPairingMessage
	secret       []byte
	folderDigest []byte
	deadline     time.Time
	attempts     int
}

func newDiagnosticsPairingManager(store *diagnosticsCredentialStore, random io.Reader, now func() time.Time) (*diagnosticsPairingManager, error) {
	if store == nil {
		return nil, errDiagnosticsPairingUnavailable
	}
	if random == nil {
		random = diagnosticsCryptoRandomReader()
	}
	if now == nil {
		now = time.Now
	}
	if _, err := store.snapshot(); err != nil {
		return nil, err
	}
	return &diagnosticsPairingManager{
		store:       store,
		random:      random,
		now:         now,
		invitations: make(map[string]*diagnosticsPairingInvitation),
	}, nil
}

func (manager *diagnosticsPairingManager) beginInvitation(folderIDDigest []byte, endpointHost string, endpointPort uint64) (string, error) {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	now := manager.now()
	manager.expireInvitations(now)
	if err := manager.expirePairingAuthorizations(now); err != nil {
		return "", err
	}
	if !validDiagnosticsEndpointHost(endpointHost) || endpointPort == 0 || endpointPort > 65535 {
		return "", errDiagnosticsPairingUnavailable
	}
	for _, invitation := range manager.invitations {
		if bytes.Equal(invitation.folderDigest, folderIDDigest) {
			return "", errDiagnosticsPairingUnavailable
		}
	}
	folderBinding, err := manager.store.reserveFolderBinding(folderIDDigest)
	if err != nil {
		return "", err
	}
	state, err := manager.store.snapshot()
	if err != nil {
		return "", err
	}
	pendingCount := len(manager.invitations)
	for _, authorization := range state.Authorizations {
		if !diagnosticsIsPairingPreactivation(authorization.State) {
			continue
		}
		pendingCount++
		if bytes.Equal(authorization.FolderBinding, folderBinding) {
			return "", errDiagnosticsPairingUnavailable
		}
	}
	if pendingCount >= diagnosticsPairingMaximumPending {
		return "", errDiagnosticsPairingUnavailable
	}
	helperPrivate, err := diagnosticsSigningPrivateKey(state.Identity.SigningSeed)
	if err != nil {
		return "", err
	}
	helperPublic := append([]byte(nil), helperPrivate.Public().(ed25519.PublicKey)...)
	helperKeyID := diagnosticsKeyID(helperPublic)
	tlsPin, err := diagnosticsTLSPrivateKeyPin(state.Identity.TLSPrivatePKCS8)
	if err != nil {
		return "", err
	}
	invitationNonce, err := readDiagnosticsRandom(manager.random, 32)
	if err != nil {
		return "", err
	}
	secret, err := readDiagnosticsRandom(manager.random, 32)
	if err != nil {
		return "", err
	}
	key := base64.RawURLEncoding.EncodeToString(invitationNonce)
	if _, exists := manager.invitations[key]; exists || diagnosticsInvitationNonceExists(state.Authorizations, invitationNonce) {
		clear(invitationNonce)
		clear(secret)
		return "", errDiagnosticsPairingUnavailable
	}
	issuedAt := uint64(now.Unix())
	expiresAt := uint64(now.Add(diagnosticsPairingLifetime).Unix())
	value := diagnosticsCBORMapValue(
		diagnosticsCBORMapField(1, diagnosticsCBORTextValue(diagnosticsPairingCapabilityID)),
		diagnosticsCBORMapField(2, diagnosticsCBORUint(1)),
		diagnosticsCBORMapField(3, diagnosticsCBORUint(1)),
		diagnosticsCBORMapField(4, diagnosticsCBORUint(diagnosticsPairingQR)),
		diagnosticsCBORMapField(5, diagnosticsCBORBstr(invitationNonce)),
		diagnosticsCBORMapField(6, diagnosticsCBORTextValue(endpointHost)),
		diagnosticsCBORMapField(7, diagnosticsCBORUint(endpointPort)),
		diagnosticsCBORMapField(8, diagnosticsCBORBstr(tlsPin)),
		diagnosticsCBORMapField(9, diagnosticsCBORBstr(helperPublic)),
		diagnosticsCBORMapField(10, diagnosticsCBORBstr(helperKeyID[:])),
		diagnosticsCBORMapField(11, diagnosticsCBORBstr(state.Identity.HomeserverBinding)),
		diagnosticsCBORMapField(12, diagnosticsCBORBstr(folderBinding)),
		diagnosticsCBORMapField(13, diagnosticsCBORBstr(state.Identity.DeviceIDDigest)),
		diagnosticsCBORMapField(14, diagnosticsCBORBstr(folderIDDigest)),
		diagnosticsCBORMapField(15, diagnosticsCBORUint(issuedAt)),
		diagnosticsCBORMapField(16, diagnosticsCBORUint(expiresAt)),
		diagnosticsCBORMapField(17, diagnosticsCBORBstr(secret)),
		diagnosticsCBORMapField(24, diagnosticsCBORUint(state.Identity.HelperEpoch)),
	)
	encoded, err := encodeDiagnosticsCBOR(value)
	if err != nil {
		return "", err
	}
	message, err := decodeDiagnosticsPairingMessage(encoded)
	if err != nil {
		return "", err
	}
	qr, err := encodeDiagnosticsPairingQR(value)
	if err != nil {
		return "", err
	}
	manager.invitations[key] = &diagnosticsPairingInvitation{
		message:      message,
		secret:       secret,
		folderDigest: append([]byte(nil), folderIDDigest...),
		deadline:     now.Add(diagnosticsPairingLifetime),
	}
	return qr, nil
}

func buildDiagnosticsAppPairingRequest(invitation diagnosticsPairingMessage, appPrivate ed25519.PrivateKey, appNonce []byte) ([]byte, error) {
	if invitation.messageType != diagnosticsPairingQR || len(appPrivate) != ed25519.PrivateKeySize || len(appNonce) != 32 {
		return nil, errDiagnosticsPairingInvalid
	}
	secret, ok := invitation.bytesField(17, 32)
	if !ok {
		return nil, errDiagnosticsPairingInvalid
	}
	fields := make([]diagnosticsCBORField, 0, 22)
	for label := uint64(1); label <= 16; label++ {
		value, exists := diagnosticsCBORLookup(invitation.value, label)
		if !exists {
			return nil, errDiagnosticsPairingInvalid
		}
		if label == 4 {
			value = diagnosticsCBORUint(diagnosticsPairingAppRequest)
		}
		fields = append(fields, diagnosticsCBORMapField(label, cloneDiagnosticsCBOR(value)))
	}
	appPublic := append([]byte(nil), appPrivate.Public().(ed25519.PublicKey)...)
	appKeyID := diagnosticsKeyID(appPublic)
	helperEpoch, _ := invitation.uintField(24)
	fields = append(fields,
		diagnosticsCBORMapField(18, diagnosticsCBORBstr(appPublic)),
		diagnosticsCBORMapField(19, diagnosticsCBORBstr(appKeyID[:])),
		diagnosticsCBORMapField(20, diagnosticsCBORBstr(appNonce)),
		diagnosticsCBORMapField(23, diagnosticsCBORUint(1)),
		diagnosticsCBORMapField(24, diagnosticsCBORUint(helperEpoch)),
	)
	value := diagnosticsCBORMapValue(fields...)
	mac, err := diagnosticsPairingBootstrapHMAC(secret, value)
	if err != nil {
		return nil, err
	}
	value.fields = append(value.fields, diagnosticsCBORMapField(21, diagnosticsCBORBstr(mac[:])))
	return signDiagnosticsPairingMessage(value, appPrivate)
}

func (manager *diagnosticsPairingManager) acceptAppRequest(data []byte) ([]byte, error) {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	now := manager.now()
	if err := manager.consumeRequestRate(now); err != nil {
		return nil, err
	}
	if err := manager.expirePairingAuthorizations(now); err != nil {
		return nil, err
	}
	request, err := decodeDiagnosticsPairingMessage(data)
	if err != nil || request.messageType != diagnosticsPairingAppRequest {
		return nil, errDiagnosticsPairingInvalid
	}
	requestDigest, err := request.digest()
	if err != nil {
		return nil, err
	}
	if response, ok, err := manager.lookupReplay(requestDigest[:], now); err != nil || ok {
		return response, err
	}
	invitationNonce, _ := request.bytesField(5, 32)
	key := base64.RawURLEncoding.EncodeToString(invitationNonce)
	invitation, ok := manager.invitations[key]
	if !ok {
		return nil, errDiagnosticsPairingUnavailable
	}
	invitation.attempts++
	if invitation.attempts > diagnosticsPairingInvitationAttempts {
		delete(manager.invitations, key)
		return nil, errDiagnosticsPairingRateLimited
	}
	if !now.Before(invitation.deadline) {
		delete(manager.invitations, key)
		return nil, errDiagnosticsPairingExpired
	}
	if !diagnosticsPairingEchoMatches(invitation.message, request) ||
		!verifyDiagnosticsPairingBootstrapHMAC(invitation.secret, request) {
		return nil, errDiagnosticsPairingInvalid
	}
	response, authorization, err := manager.makeHelperAcceptance(request, requestDigest, invitation, now)
	if err != nil {
		return nil, err
	}
	err = manager.store.update(func(state *diagnosticsCredentialState) error {
		pendingCount := 0
		for _, existing := range state.Authorizations {
			if existing.RecordID == authorization.RecordID {
				return errDiagnosticsPairingInvalid
			}
			if diagnosticsIsPairingPreactivation(existing.State) {
				pendingCount++
				if bytes.Equal(existing.FolderBinding, authorization.FolderBinding) {
					return errDiagnosticsPairingUnavailable
				}
			}
		}
		if pendingCount >= diagnosticsPairingMaximumPending {
			return errDiagnosticsPairingUnavailable
		}
		count := 0
		for _, existing := range state.Authorizations {
			if bytes.Equal(existing.FolderBinding, authorization.FolderBinding) && existing.State != "inactive" {
				count++
			}
		}
		if count >= 8 {
			return errDiagnosticsPairingUnavailable
		}
		state.Authorizations = append(state.Authorizations, authorization)
		sortDiagnosticsAuthorizations(state.Authorizations)
		return nil
	})
	if err != nil {
		if replay, found, reconcileErr := manager.lookupReplay(requestDigest[:], now); reconcileErr == nil && found {
			delete(manager.invitations, key)
			clear(invitation.secret)
			return replay, nil
		}
		return nil, err
	}
	delete(manager.invitations, key)
	clear(invitation.secret)
	return response, nil
}

func (manager *diagnosticsPairingManager) makeHelperAcceptance(request diagnosticsPairingMessage, requestDigest [32]byte, invitation *diagnosticsPairingInvitation, now time.Time) ([]byte, diagnosticsPairingAuthorization, error) {
	state, err := manager.store.snapshot()
	if err != nil {
		return nil, diagnosticsPairingAuthorization{}, err
	}
	helperPrivate, err := diagnosticsSigningPrivateKey(state.Identity.SigningSeed)
	if err != nil {
		return nil, diagnosticsPairingAuthorization{}, err
	}
	helperNonce, err := readDiagnosticsRandom(manager.random, 32)
	if err != nil {
		return nil, diagnosticsPairingAuthorization{}, err
	}
	expires := now.Add(diagnosticsPairingLifetime)
	if expires.After(invitation.deadline) {
		expires = invitation.deadline
	}
	fields := make([]diagnosticsCBORField, 0, 20)
	for _, label := range []uint64{1, 2, 3, 5, 9, 10, 11, 12, 13, 14, 18, 19, 20, 23, 24} {
		value, ok := diagnosticsCBORLookup(request.value, label)
		if !ok {
			return nil, diagnosticsPairingAuthorization{}, fmt.Errorf("helper acceptance field %d: %w", label, errDiagnosticsPairingInvalid)
		}
		fields = append(fields, diagnosticsCBORMapField(label, cloneDiagnosticsCBOR(value)))
	}
	fields = append(fields,
		diagnosticsCBORMapField(4, diagnosticsCBORUint(diagnosticsPairingHelperAccept)),
		diagnosticsCBORMapField(15, diagnosticsCBORUint(uint64(now.Unix()))),
		diagnosticsCBORMapField(16, diagnosticsCBORUint(uint64(expires.Unix()))),
		diagnosticsCBORMapField(22, diagnosticsCBORBstr(requestDigest[:])),
		diagnosticsCBORMapField(25, diagnosticsCBORBstr(helperNonce)),
	)
	response, err := signDiagnosticsPairingMessage(diagnosticsCBORMapValue(fields...), helperPrivate)
	if err != nil {
		return nil, diagnosticsPairingAuthorization{}, err
	}
	responseMessage, _ := decodeDiagnosticsPairingMessage(response)
	responseDigest, _ := responseMessage.digest()
	appPublic, _ := request.bytesField(18, ed25519.PublicKeySize)
	appKeyID, _ := request.bytesField(19, 32)
	homeserverBinding, _ := request.bytesField(11, 32)
	folderBinding, _ := request.bytesField(12, 32)
	tlsPin, _ := request.bytesField(8, 32)
	appNonce, _ := request.bytesField(20, 32)
	invitationNonce, _ := request.bytesField(5, 32)
	return response, diagnosticsPairingAuthorization{
		RecordID:           diagnosticsAuthorizationRecordID(appKeyID, folderBinding),
		State:              "pending",
		HomeserverBinding:  homeserverBinding,
		FolderBinding:      folderBinding,
		AppPublicKey:       appPublic,
		AppKeyID:           appKeyID,
		AppEpoch:           1,
		HelperEpoch:        state.Identity.HelperEpoch,
		TLSSPKIPin:         tlsPin,
		InvitationNonce:    invitationNonce,
		AppNonce:           appNonce,
		HelperNonce:        helperNonce,
		AppRequestDigest:   append([]byte(nil), requestDigest[:]...),
		CurrentStateDigest: append([]byte(nil), responseDigest[:]...),
		ExpiresAt:          invitation.deadline.Unix(),
		Replays: []diagnosticsPairingReplay{{
			RequestDigest: append([]byte(nil), requestDigest[:]...),
			Response:      append([]byte(nil), response...),
			RetainUntil:   invitation.deadline.Unix(),
		}},
	}, nil
}

func buildDiagnosticsBootstrapTransition(prior diagnosticsPairingMessage, messageType uint64, privateKey ed25519.PrivateKey, issuedAt, expiresAt uint64) ([]byte, error) {
	if prior.messageType < diagnosticsPairingHelperAccept || prior.messageType > diagnosticsPairingAbort ||
		messageType < diagnosticsPairingFinalize || messageType > diagnosticsPairingAbortAck {
		return nil, errDiagnosticsPairingInvalid
	}
	priorDigest, err := prior.digest()
	if err != nil {
		return nil, err
	}
	fields := make([]diagnosticsCBORField, 0, 21)
	for _, label := range []uint64{1, 2, 3, 5, 9, 10, 11, 12, 13, 14, 18, 19, 20, 22, 23, 24, 25} {
		value, ok := diagnosticsCBORLookup(prior.value, label)
		if !ok {
			return nil, errDiagnosticsPairingInvalid
		}
		fields = append(fields, diagnosticsCBORMapField(label, cloneDiagnosticsCBOR(value)))
	}
	fields = append(fields,
		diagnosticsCBORMapField(4, diagnosticsCBORUint(messageType)),
		diagnosticsCBORMapField(15, diagnosticsCBORUint(issuedAt)),
		diagnosticsCBORMapField(16, diagnosticsCBORUint(expiresAt)),
		diagnosticsCBORMapField(26, diagnosticsCBORBstr(priorDigest[:])),
	)
	return signDiagnosticsPairingMessage(diagnosticsCBORMapValue(fields...), privateKey)
}

func (manager *diagnosticsPairingManager) handleBootstrapTransition(data []byte) ([]byte, error) {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	now := manager.now()
	if err := manager.consumeRequestRate(now); err != nil {
		return nil, err
	}
	if err := manager.expirePairingAuthorizations(now); err != nil {
		return nil, err
	}
	request, err := decodeDiagnosticsPairingMessage(data)
	if err != nil || (request.messageType != diagnosticsPairingFinalize && request.messageType != diagnosticsPairingReceipt &&
		request.messageType != diagnosticsPairingActivate && request.messageType != diagnosticsPairingAbort) {
		return nil, errDiagnosticsPairingInvalid
	}
	requestDigest, _ := request.digest()
	appKeyID, _ := request.bytesField(19, 32)
	folderBinding, _ := request.bytesField(12, 32)
	recordID := diagnosticsAuthorizationRecordID(appKeyID, folderBinding)
	var result []byte
	err = manager.store.update(func(state *diagnosticsCredentialState) error {
		index := diagnosticsAuthorizationIndex(state.Authorizations, recordID)
		if index < 0 {
			return errDiagnosticsPairingUnavailable
		}
		authorization := &state.Authorizations[index]
		if replay, ok := diagnosticsReplayResponse(*authorization, requestDigest[:], now.Unix()); ok {
			result = replay
			return nil
		}
		if now.Unix() > authorization.ExpiresAt || !diagnosticsBootstrapTimeValid(request, now, authorization.ExpiresAt) ||
			!manager.bootstrapRequestMatches(*authorization, state.Identity, request) {
			return errDiagnosticsPairingExpired
		}
		expectedState := map[uint64]string{
			diagnosticsPairingFinalize: "pending",
			diagnosticsPairingReceipt:  "finalize_pending",
			diagnosticsPairingActivate: "awaiting_activation",
		}
		if request.messageType != diagnosticsPairingAbort && authorization.State != expectedState[request.messageType] {
			return errDiagnosticsPairingInvalid
		}
		if request.messageType == diagnosticsPairingAbort && authorization.State != "pending" &&
			authorization.State != "finalize_pending" && authorization.State != "awaiting_activation" && authorization.State != "active" {
			return errDiagnosticsPairingInvalid
		}
		priorDigest, _ := request.bytesField(26, 32)
		if subtle.ConstantTimeCompare(priorDigest, authorization.CurrentStateDigest) != 1 {
			return errDiagnosticsPairingInvalid
		}
		response, responseDigest, err := manager.makeBootstrapReply(*authorization, state.Identity, request, requestDigest, now)
		if err != nil {
			return err
		}
		retainUntil := authorization.ExpiresAt
		if request.messageType == diagnosticsPairingActivate || request.messageType == diagnosticsPairingAbort {
			retainUntil = now.Add(diagnosticsPairingTerminalReplay).Unix()
		}
		authorization.Replays = append(authorization.Replays, diagnosticsPairingReplay{
			RequestDigest: append([]byte(nil), requestDigest[:]...),
			Response:      append([]byte(nil), response...),
			RetainUntil:   retainUntil,
		})
		authorization.CurrentStateDigest = append([]byte(nil), responseDigest...)
		switch request.messageType {
		case diagnosticsPairingFinalize:
			authorization.State = "finalize_pending"
		case diagnosticsPairingReceipt:
			authorization.State = "awaiting_activation"
		case diagnosticsPairingActivate:
			authorization.State = "active"
			authorization.TerminalReplyExpires = now.Add(diagnosticsPairingTerminalReplay).Unix()
		case diagnosticsPairingAbort:
			if authorization.State == "active" {
				authorization.State = "revoked"
				authorization.AppEpoch++
				state.Revocations = append(state.Revocations, diagnosticsPairingRevocation{
					AppKeyID:           append([]byte(nil), authorization.AppKeyID...),
					FolderBinding:      append([]byte(nil), authorization.FolderBinding...),
					AuthorizationEpoch: authorization.AppEpoch,
					Reason:             diagnosticsPairingRevocationUserRequest,
					RetainUntil:        now.Add(diagnosticsPairingTerminalReplay).Unix(),
				})
			} else {
				authorization.State = "inactive"
			}
			authorization.TerminalReplyExpires = now.Add(diagnosticsPairingTerminalReplay).Unix()
		}
		result = response
		return nil
	})
	return result, err
}

func (manager *diagnosticsPairingManager) makeBootstrapReply(authorization diagnosticsPairingAuthorization, identity diagnosticsHelperCredentialIdentity, request diagnosticsPairingMessage, requestDigest [32]byte, now time.Time) ([]byte, []byte, error) {
	helperPrivate, err := diagnosticsSigningPrivateKey(identity.SigningSeed)
	if err != nil {
		return nil, nil, err
	}
	responseType := request.messageType + 1
	expires := now.Add(diagnosticsPairingLifetime).Unix()
	if expires > authorization.ExpiresAt {
		expires = authorization.ExpiresAt
	}
	fields := make([]diagnosticsCBORField, 0, 21)
	for _, label := range []uint64{1, 2, 3, 5, 9, 10, 11, 12, 13, 14, 18, 19, 20, 22, 23, 24, 25} {
		value, ok := diagnosticsCBORLookup(request.value, label)
		if !ok {
			return nil, nil, errDiagnosticsPairingInvalid
		}
		fields = append(fields, diagnosticsCBORMapField(label, cloneDiagnosticsCBOR(value)))
	}
	fields = append(fields,
		diagnosticsCBORMapField(4, diagnosticsCBORUint(responseType)),
		diagnosticsCBORMapField(15, diagnosticsCBORUint(uint64(now.Unix()))),
		diagnosticsCBORMapField(16, diagnosticsCBORUint(uint64(expires))),
		diagnosticsCBORMapField(26, diagnosticsCBORBstr(requestDigest[:])),
	)
	response, err := signDiagnosticsPairingMessage(diagnosticsCBORMapValue(fields...), helperPrivate)
	if err != nil {
		return nil, nil, err
	}
	message, _ := decodeDiagnosticsPairingMessage(response)
	digest, _ := message.digest()
	return response, append([]byte(nil), digest[:]...), nil
}

func (manager *diagnosticsPairingManager) bootstrapRequestMatches(authorization diagnosticsPairingAuthorization, identity diagnosticsHelperCredentialIdentity, request diagnosticsPairingMessage) bool {
	comparisons := [][2][]byte{
		{authorization.HomeserverBinding, diagnosticsMustBytes(request, 11)},
		{authorization.FolderBinding, diagnosticsMustBytes(request, 12)},
		{authorization.AppPublicKey, diagnosticsMustBytes(request, 18)},
		{authorization.AppKeyID, diagnosticsMustBytes(request, 19)},
		{authorization.InvitationNonce, diagnosticsMustBytes(request, 5)},
		{authorization.AppNonce, diagnosticsMustBytes(request, 20)},
		{authorization.HelperNonce, diagnosticsMustBytes(request, 25)},
		{authorization.AppRequestDigest, diagnosticsMustBytes(request, 22)},
	}
	for _, comparison := range comparisons {
		if subtle.ConstantTimeCompare(comparison[0], comparison[1]) != 1 {
			return false
		}
	}
	helperPrivate, err := diagnosticsSigningPrivateKey(identity.SigningSeed)
	if err != nil {
		return false
	}
	helperPublic := helperPrivate.Public().(ed25519.PublicKey)
	helperKeyID := diagnosticsKeyID(helperPublic)
	requestHelperPublic, _ := request.bytesField(9, 32)
	requestHelperKeyID, _ := request.bytesField(10, 32)
	appEpoch, _ := request.uintField(23)
	helperEpoch, _ := request.uintField(24)
	return appEpoch == authorization.AppEpoch && helperEpoch == authorization.HelperEpoch &&
		subtle.ConstantTimeCompare(helperPublic, requestHelperPublic) == 1 &&
		subtle.ConstantTimeCompare(helperKeyID[:], requestHelperKeyID) == 1
}

func (manager *diagnosticsPairingManager) consumeRequestRate(now time.Time) error {
	cutoff := now.Add(-time.Minute).UnixNano()
	return manager.store.update(func(state *diagnosticsCredentialState) error {
		kept := state.RateEvents[:0]
		for _, event := range state.RateEvents {
			if event > cutoff {
				kept = append(kept, event)
			}
		}
		state.RateEvents = kept
		if len(state.RateEvents) >= diagnosticsPairingRequestLimitPerMinute {
			return errDiagnosticsPairingRateLimited
		}
		state.RateEvents = append(state.RateEvents, now.UnixNano())
		return nil
	})
}

func (manager *diagnosticsPairingManager) lookupReplay(requestDigest []byte, now time.Time) ([]byte, bool, error) {
	state, err := manager.store.snapshot()
	if err != nil {
		return nil, false, err
	}
	for _, authorization := range state.Authorizations {
		if response, ok := diagnosticsReplayResponse(authorization, requestDigest, now.Unix()); ok {
			return response, true, nil
		}
	}
	return nil, false, nil
}

func diagnosticsReplayResponse(authorization diagnosticsPairingAuthorization, requestDigest []byte, nowUnix int64) ([]byte, bool) {
	for _, replay := range authorization.Replays {
		if replay.RetainUntil >= nowUnix && subtle.ConstantTimeCompare(replay.RequestDigest, requestDigest) == 1 {
			return append([]byte(nil), replay.Response...), true
		}
	}
	return nil, false
}

func (manager *diagnosticsPairingManager) expireInvitations(now time.Time) {
	for key, invitation := range manager.invitations {
		if !now.Before(invitation.deadline) {
			clear(invitation.secret)
			delete(manager.invitations, key)
		}
	}
}

func (manager *diagnosticsPairingManager) expirePairingAuthorizations(now time.Time) error {
	return manager.store.updateIfChanged(func(state *diagnosticsCredentialState) (bool, error) {
		changed := false
		for index := range state.Authorizations {
			authorization := &state.Authorizations[index]
			keptReplays := authorization.Replays[:0]
			for _, replay := range authorization.Replays {
				if replay.RetainUntil >= now.Unix() {
					keptReplays = append(keptReplays, replay)
				} else {
					changed = true
				}
			}
			authorization.Replays = keptReplays
			if authorization.Transition != nil && authorization.Transition.Stage != "committed" &&
				now.Unix() > authorization.Transition.ExpiresAt {
				authorization.Transition = nil
				changed = true
			}
			if diagnosticsIsPairingPreactivation(authorization.State) && now.Unix() > authorization.ExpiresAt {
				authorization.State = "inactive"
				authorization.Transition = nil
				changed = true
			}
		}
		return changed, nil
	})
}

func diagnosticsIsPairingPreactivation(state string) bool {
	switch state {
	case "pending", "finalize_pending", "awaiting_activation":
		return true
	default:
		return false
	}
}

func diagnosticsInvitationNonceExists(authorizations []diagnosticsPairingAuthorization, nonce []byte) bool {
	for _, authorization := range authorizations {
		if subtle.ConstantTimeCompare(authorization.InvitationNonce, nonce) == 1 {
			return true
		}
	}
	return false
}

func diagnosticsBootstrapTimeValid(message diagnosticsPairingMessage, now time.Time, hardExpiry int64) bool {
	issued, issuedOK := message.uintField(15)
	expires, expiresOK := message.uintField(16)
	if !issuedOK || !expiresOK || expires <= issued || expires-issued > uint64(diagnosticsPairingLifetime/time.Second) ||
		expires > uint64(hardExpiry) {
		return false
	}
	nowUnix := now.Unix()
	return int64(issued) <= nowUnix+int64(diagnosticsPairingClockSkew/time.Second) &&
		int64(expires)+int64(diagnosticsPairingClockSkew/time.Second) >= nowUnix
}

func diagnosticsAuthorizationIndex(authorizations []diagnosticsPairingAuthorization, recordID string) int {
	for index := range authorizations {
		if authorizations[index].RecordID == recordID {
			return index
		}
	}
	return -1
}

func sortDiagnosticsAuthorizations(authorizations []diagnosticsPairingAuthorization) {
	for index := 1; index < len(authorizations); index++ {
		for position := index; position > 0 && authorizations[position].RecordID < authorizations[position-1].RecordID; position-- {
			authorizations[position], authorizations[position-1] = authorizations[position-1], authorizations[position]
		}
	}
}

func diagnosticsMustBytes(message diagnosticsPairingMessage, label uint64) []byte {
	value, _ := message.bytesField(label, 32)
	return value
}

func diagnosticsTLSPrivateKeyPin(privatePKCS8 []byte) ([]byte, error) {
	privateKey, err := x509.ParsePKCS8PrivateKey(privatePKCS8)
	if err != nil {
		return nil, errDiagnosticsPairingInvalid
	}
	ecdsaKey, ok := privateKey.(*ecdsa.PrivateKey)
	if !ok || ecdsaKey.Curve != elliptic.P256() {
		return nil, errDiagnosticsPairingInvalid
	}
	spki, err := x509.MarshalPKIXPublicKey(&ecdsaKey.PublicKey)
	if err != nil {
		return nil, errDiagnosticsPairingInvalid
	}
	pin := sha256.Sum256(spki)
	return append([]byte(nil), pin[:]...), nil
}
