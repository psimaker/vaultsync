package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/subtle"
	"errors"
	"time"
)

func diagnosticsLifecycleBaseFields(authorization diagnosticsPairingAuthorization, identity diagnosticsHelperCredentialIdentity, messageType uint64, issuedAt, expiresAt uint64, nonce []byte) ([]diagnosticsCBORField, error) {
	if len(nonce) != 32 || authorization.State != "active" {
		return nil, errDiagnosticsPairingInvalid
	}
	helperPrivate, err := diagnosticsSigningPrivateKey(identity.SigningSeed)
	if err != nil {
		return nil, err
	}
	helperPublic := append([]byte(nil), helperPrivate.Public().(ed25519.PublicKey)...)
	helperKeyID := diagnosticsKeyID(helperPublic)
	return []diagnosticsCBORField{
		diagnosticsCBORMapField(1, diagnosticsCBORTextValue(diagnosticsPairingCapabilityID)),
		diagnosticsCBORMapField(2, diagnosticsCBORUint(1)),
		diagnosticsCBORMapField(3, diagnosticsCBORUint(1)),
		diagnosticsCBORMapField(4, diagnosticsCBORUint(messageType)),
		diagnosticsCBORMapField(5, diagnosticsCBORBstr(authorization.HomeserverBinding)),
		diagnosticsCBORMapField(6, diagnosticsCBORBstr(authorization.FolderBinding)),
		diagnosticsCBORMapField(7, diagnosticsCBORBstr(authorization.AppPublicKey)),
		diagnosticsCBORMapField(8, diagnosticsCBORBstr(authorization.AppKeyID)),
		diagnosticsCBORMapField(11, diagnosticsCBORBstr(helperPublic)),
		diagnosticsCBORMapField(12, diagnosticsCBORBstr(helperKeyID[:])),
		diagnosticsCBORMapField(15, diagnosticsCBORBstr(authorization.TLSSPKIPin)),
		diagnosticsCBORMapField(17, diagnosticsCBORUint(authorization.AppEpoch)),
		diagnosticsCBORMapField(19, diagnosticsCBORUint(authorization.HelperEpoch)),
		diagnosticsCBORMapField(21, diagnosticsCBORUint(issuedAt)),
		diagnosticsCBORMapField(22, diagnosticsCBORUint(expiresAt)),
		diagnosticsCBORMapField(23, diagnosticsCBORBstr(nonce)),
		diagnosticsCBORMapField(26, diagnosticsCBORBstr(authorization.CurrentStateDigest)),
	}, nil
}

func buildDiagnosticsAppKeyRotationRequest(authorization diagnosticsPairingAuthorization, identity diagnosticsHelperCredentialIdentity, proposedPublic []byte, currentAppPrivate ed25519.PrivateKey, issuedAt, expiresAt uint64, nonce []byte) ([]byte, error) {
	if len(proposedPublic) != ed25519.PublicKeySize {
		return nil, errDiagnosticsPairingInvalid
	}
	fields, err := diagnosticsLifecycleBaseFields(authorization, identity, diagnosticsPairingAppKeyRotationRequest, issuedAt, expiresAt, nonce)
	if err != nil {
		return nil, err
	}
	proposedKeyID := diagnosticsKeyID(proposedPublic)
	fields = append(fields,
		diagnosticsCBORMapField(9, diagnosticsCBORBstr(proposedPublic)),
		diagnosticsCBORMapField(10, diagnosticsCBORBstr(proposedKeyID[:])),
		diagnosticsCBORMapField(18, diagnosticsCBORUint(authorization.AppEpoch+1)),
	)
	return signDiagnosticsPairingMessage(diagnosticsCBORMapValue(fields...), currentAppPrivate)
}

func buildDiagnosticsRevocationRequest(authorization diagnosticsPairingAuthorization, identity diagnosticsHelperCredentialIdentity, reason uint64, currentAppPrivate ed25519.PrivateKey, issuedAt, expiresAt uint64, nonce []byte) ([]byte, error) {
	fields, err := diagnosticsLifecycleBaseFields(authorization, identity, diagnosticsPairingRevocationRequest, issuedAt, expiresAt, nonce)
	if err != nil {
		return nil, err
	}
	fields = append(fields,
		diagnosticsCBORMapField(18, diagnosticsCBORUint(authorization.AppEpoch+1)),
		diagnosticsCBORMapField(25, diagnosticsCBORUint(reason)),
		diagnosticsCBORMapField(27, diagnosticsCBORUint(diagnosticsPairingRevocationSignedApp)),
	)
	return signDiagnosticsPairingMessage(diagnosticsCBORMapValue(fields...), currentAppPrivate)
}

func buildDiagnosticsLifecycleContinuation(prior diagnosticsPairingMessage, messageType, transitionKind uint64, transitionDigest []byte, signer ed25519.PrivateKey, issuedAt, expiresAt uint64, nonce []byte) ([]byte, error) {
	if len(nonce) != 32 || len(signer) != ed25519.PrivateKeySize {
		return nil, errDiagnosticsPairingInvalid
	}
	priorDigest, err := prior.digest()
	if err != nil {
		return nil, err
	}
	templateFields := []diagnosticsCBORField{diagnosticsCBORMapField(4, diagnosticsCBORUint(messageType))}
	if messageType >= diagnosticsPairingLifecycleFinalize {
		templateFields = append(templateFields, diagnosticsCBORMapField(29, diagnosticsCBORUint(transitionKind)))
	}
	if messageType == diagnosticsPairingRevocationRecord {
		origin, _ := prior.uintField(27)
		templateFields = append(templateFields, diagnosticsCBORMapField(27, diagnosticsCBORUint(origin)))
	}
	template := diagnosticsPairingMessage{messageType: messageType, value: diagnosticsCBORMapValue(templateFields...)}
	expected, err := diagnosticsPairingExpectedLabels(template)
	if err != nil {
		return nil, err
	}
	fields := make([]diagnosticsCBORField, 0, len(expected)-1)
	for _, label := range expected {
		if label == 255 {
			continue
		}
		var value diagnosticsCBORValue
		switch label {
		case 4:
			value = diagnosticsCBORUint(messageType)
		case 21:
			value = diagnosticsCBORUint(issuedAt)
		case 22:
			value = diagnosticsCBORUint(expiresAt)
		case 23:
			value = diagnosticsCBORBstr(nonce)
		case 24:
			value = diagnosticsCBORBstr(priorDigest[:])
		case 28:
			if len(transitionDigest) != 32 {
				return nil, errDiagnosticsPairingInvalid
			}
			value = diagnosticsCBORBstr(transitionDigest)
		case 29:
			value = diagnosticsCBORUint(transitionKind)
		default:
			priorValue, ok := diagnosticsCBORLookup(prior.value, label)
			if !ok {
				return nil, errDiagnosticsPairingInvalid
			}
			value = cloneDiagnosticsCBOR(priorValue)
		}
		fields = append(fields, diagnosticsCBORMapField(label, value))
	}
	return signDiagnosticsPairingMessage(diagnosticsCBORMapValue(fields...), signer)
}

func (manager *diagnosticsPairingManager) handleLifecycleMessage(data []byte) ([]byte, error) {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	now := manager.now()
	if err := manager.consumeRequestRate(now); err != nil {
		return nil, err
	}
	if err := manager.expirePairingAuthorizations(now); err != nil {
		return nil, err
	}
	message, err := decodeDiagnosticsPairingMessage(data)
	if err != nil {
		return nil, errDiagnosticsPairingInvalid
	}
	switch message.messageType {
	case diagnosticsPairingAppKeyRotationRequest,
		diagnosticsPairingAppKeyRotationNewProof,
		diagnosticsPairingHelperKeyRotationConfirm,
		diagnosticsPairingTLSPinRotationConfirm,
		diagnosticsPairingRevocationRequest,
		diagnosticsPairingLifecycleFinalize,
		diagnosticsPairingLifecycleAbort:
	default:
		return nil, errDiagnosticsPairingInvalid
	}
	messageDigest, _ := message.digest()
	appKeyID, _ := message.bytesField(8, 32)
	folderBinding, _ := message.bytesField(6, 32)
	recordID := diagnosticsAuthorizationRecordID(appKeyID, folderBinding)
	var response []byte
	err = manager.store.update(func(state *diagnosticsCredentialState) error {
		index := diagnosticsAuthorizationIndex(state.Authorizations, recordID)
		if index < 0 {
			return errDiagnosticsPairingUnavailable
		}
		authorization := &state.Authorizations[index]
		if replay, ok := diagnosticsReplayResponse(*authorization, messageDigest[:], now.Unix()); ok {
			response = replay
			return nil
		}
		if diagnosticsLifecycleNoReplyReplay(*authorization, message.messageType, messageDigest[:]) {
			return nil
		}
		if authorization.State != "active" || !diagnosticsLifecycleTimeValid(message, now) ||
			!diagnosticsLifecycleCommonMatches(*authorization, state.Identity, message) {
			return errDiagnosticsPairingInvalid
		}
		nonce, _ := message.bytesField(23, 32)
		if !diagnosticsRememberLifecycleNonce(authorization, nonce) {
			return errDiagnosticsPairingInvalid
		}
		switch message.messageType {
		case diagnosticsPairingAppKeyRotationRequest:
			return manager.stageAppKeyRotationRequest(authorization, message, messageDigest, now)
		case diagnosticsPairingAppKeyRotationNewProof:
			response, err = manager.acceptAppKeyRotationProof(authorization, state.Identity, message, messageDigest, now)
			return err
		case diagnosticsPairingHelperKeyRotationConfirm:
			return manager.acceptLocalRotationConfirmation(authorization, message, messageDigest, diagnosticsPairingTransitionHelperKey, now)
		case diagnosticsPairingTLSPinRotationConfirm:
			return manager.acceptLocalRotationConfirmation(authorization, message, messageDigest, diagnosticsPairingTransitionTLSPin, now)
		case diagnosticsPairingRevocationRequest:
			response, err = manager.applySignedRevocation(state, authorization, message, messageDigest, now)
			return err
		case diagnosticsPairingLifecycleFinalize:
			response, err = manager.finalizeLifecycleTransition(authorization, state.Identity, message, messageDigest, now)
			return err
		case diagnosticsPairingLifecycleAbort:
			response, err = manager.abortLifecycleTransition(authorization, state.Identity, message, messageDigest, now)
			return err
		default:
			return errDiagnosticsPairingInvalid
		}
	})
	return response, err
}

func (manager *diagnosticsPairingManager) stageAppKeyRotationRequest(authorization *diagnosticsPairingAuthorization, message diagnosticsPairingMessage, digest [32]byte, now time.Time) error {
	if authorization.Transition != nil {
		if bytes.Equal(authorization.Transition.LatestMessageDigest, digest[:]) {
			return nil
		}
		return errDiagnosticsPairingUnavailable
	}
	proposedPublic, _ := message.bytesField(9, 32)
	proposedKeyID, _ := message.bytesField(10, 32)
	proposedEpoch, _ := message.uintField(18)
	expires, _ := message.uintField(22)
	authorization.Transition = &diagnosticsPairingTransition{
		Kind:                 diagnosticsPairingTransitionAppKey,
		Stage:                "request",
		TransitionDigest:     append([]byte(nil), digest[:]...),
		LatestMessageDigest:  append([]byte(nil), digest[:]...),
		ProposedAppPublicKey: proposedPublic,
		ProposedAppKeyID:     proposedKeyID,
		ProposedAppEpoch:     proposedEpoch,
		ExpiresAt:            min(int64(expires), now.Add(diagnosticsPairingLifetime).Unix()),
	}
	return nil
}

func (manager *diagnosticsPairingManager) acceptAppKeyRotationProof(authorization *diagnosticsPairingAuthorization, identity diagnosticsHelperCredentialIdentity, message diagnosticsPairingMessage, digest [32]byte, now time.Time) ([]byte, error) {
	transition := authorization.Transition
	if transition == nil || transition.Kind != diagnosticsPairingTransitionAppKey || transition.Stage != "request" || now.Unix() > transition.ExpiresAt {
		return nil, errDiagnosticsPairingInvalid
	}
	prior, _ := message.bytesField(24, 32)
	proposedPublic, _ := message.bytesField(9, 32)
	proposedKeyID, _ := message.bytesField(10, 32)
	proposedEpoch, _ := message.uintField(18)
	if subtle.ConstantTimeCompare(prior, transition.LatestMessageDigest) != 1 ||
		subtle.ConstantTimeCompare(proposedPublic, transition.ProposedAppPublicKey) != 1 ||
		subtle.ConstantTimeCompare(proposedKeyID, transition.ProposedAppKeyID) != 1 || proposedEpoch != transition.ProposedAppEpoch {
		return nil, errDiagnosticsPairingInvalid
	}
	helperPrivate, err := diagnosticsSigningPrivateKey(identity.SigningSeed)
	if err != nil {
		return nil, err
	}
	nonce, err := manager.newLifecycleNonce(authorization)
	if err != nil {
		return nil, err
	}
	response, err := buildDiagnosticsLifecycleContinuation(message, diagnosticsPairingAppKeyRotationAccept, 0, nil, helperPrivate, uint64(now.Unix()), uint64(transition.ExpiresAt), nonce)
	if err != nil {
		return nil, err
	}
	responseMessage, _ := decodeDiagnosticsPairingMessage(response)
	responseDigest, _ := responseMessage.digest()
	transition.Stage = "accepted"
	transition.LatestMessageDigest = append([]byte(nil), responseDigest[:]...)
	authorization.Replays = append(authorization.Replays, diagnosticsPairingReplay{
		RequestDigest: append([]byte(nil), digest[:]...), Response: append([]byte(nil), response...), RetainUntil: transition.ExpiresAt,
	})
	return response, nil
}

func (manager *diagnosticsPairingManager) acceptLocalRotationConfirmation(authorization *diagnosticsPairingAuthorization, message diagnosticsPairingMessage, digest [32]byte, kind uint64, now time.Time) error {
	transition := authorization.Transition
	if transition == nil || transition.Kind != kind || transition.Stage != "proof" || now.Unix() > transition.ExpiresAt {
		return errDiagnosticsPairingInvalid
	}
	prior, _ := message.bytesField(24, 32)
	if subtle.ConstantTimeCompare(prior, transition.LatestMessageDigest) != 1 {
		return errDiagnosticsPairingInvalid
	}
	transition.Stage = "accepted"
	transition.LatestMessageDigest = append([]byte(nil), digest[:]...)
	return nil
}

func (manager *diagnosticsPairingManager) finalizeLifecycleTransition(authorization *diagnosticsPairingAuthorization, identity diagnosticsHelperCredentialIdentity, message diagnosticsPairingMessage, digest [32]byte, now time.Time) ([]byte, error) {
	transition := authorization.Transition
	kind, _ := message.uintField(29)
	prior, _ := message.bytesField(24, 32)
	transitionDigest, _ := message.bytesField(28, 32)
	if transition == nil || transition.Kind != kind || transition.Stage != "accepted" || now.Unix() > transition.ExpiresAt ||
		subtle.ConstantTimeCompare(prior, transition.LatestMessageDigest) != 1 ||
		subtle.ConstantTimeCompare(transitionDigest, transition.TransitionDigest) != 1 {
		return nil, errDiagnosticsPairingInvalid
	}
	var signer ed25519.PrivateKey
	var err error
	if kind == diagnosticsPairingTransitionHelperKey {
		signer, err = diagnosticsSigningPrivateKey(transition.ProposedHelperSeed)
	} else {
		signer, err = diagnosticsSigningPrivateKey(identity.SigningSeed)
	}
	if err != nil {
		return nil, err
	}
	nonce, err := manager.newLifecycleNonce(authorization)
	if err != nil {
		return nil, err
	}
	response, err := buildDiagnosticsLifecycleContinuation(message, diagnosticsPairingLifecycleActiveAck, kind, transition.TransitionDigest, signer, uint64(now.Unix()), uint64(transition.ExpiresAt), nonce)
	if err != nil {
		return nil, err
	}
	responseMessage, _ := decodeDiagnosticsPairingMessage(response)
	responseDigest, _ := responseMessage.digest()
	transition.Stage = "committed"
	transition.LatestMessageDigest = append([]byte(nil), responseDigest[:]...)
	authorization.Replays = append(authorization.Replays, diagnosticsPairingReplay{
		RequestDigest: append([]byte(nil), digest[:]...), Response: append([]byte(nil), response...), RetainUntil: transition.ExpiresAt,
	})
	authorization.TerminalReplyExpires = max(authorization.TerminalReplyExpires, transition.ExpiresAt)
	return response, nil
}

func (manager *diagnosticsPairingManager) abortLifecycleTransition(authorization *diagnosticsPairingAuthorization, identity diagnosticsHelperCredentialIdentity, message diagnosticsPairingMessage, digest [32]byte, now time.Time) ([]byte, error) {
	transition := authorization.Transition
	kind, _ := message.uintField(29)
	prior, _ := message.bytesField(24, 32)
	transitionDigest, _ := message.bytesField(28, 32)
	if transition == nil || transition.Kind != kind || transition.Stage == "committed" || now.Unix() > transition.ExpiresAt ||
		subtle.ConstantTimeCompare(prior, transition.LatestMessageDigest) != 1 ||
		subtle.ConstantTimeCompare(transitionDigest, transition.TransitionDigest) != 1 {
		return nil, errDiagnosticsPairingInvalid
	}
	helperPrivate, err := diagnosticsSigningPrivateKey(identity.SigningSeed)
	if err != nil {
		return nil, err
	}
	nonce, err := manager.newLifecycleNonce(authorization)
	if err != nil {
		return nil, err
	}
	response, err := buildDiagnosticsLifecycleContinuation(message, diagnosticsPairingLifecycleAbortAck, kind, transition.TransitionDigest, helperPrivate, uint64(now.Unix()), uint64(transition.ExpiresAt), nonce)
	if err != nil {
		return nil, err
	}
	authorization.Replays = append(authorization.Replays, diagnosticsPairingReplay{
		RequestDigest: append([]byte(nil), digest[:]...), Response: append([]byte(nil), response...), RetainUntil: transition.ExpiresAt,
	})
	authorization.TerminalReplyExpires = max(authorization.TerminalReplyExpires, transition.ExpiresAt)
	authorization.Transition = nil
	return response, nil
}

func (manager *diagnosticsPairingManager) applySignedRevocation(state *diagnosticsCredentialState, authorization *diagnosticsPairingAuthorization, message diagnosticsPairingMessage, digest [32]byte, now time.Time) ([]byte, error) {
	helperPrivate, err := diagnosticsSigningPrivateKey(state.Identity.SigningSeed)
	if err != nil {
		return nil, err
	}
	nonce, err := manager.newLifecycleNonce(authorization)
	if err != nil {
		return nil, err
	}
	response, err := buildDiagnosticsLifecycleContinuation(message, diagnosticsPairingRevocationRecord, 0, nil, helperPrivate, uint64(now.Unix()), uint64(now.Add(diagnosticsPairingLifetime).Unix()), nonce)
	if err != nil {
		return nil, err
	}
	reason, _ := message.uintField(25)
	proposedEpoch, _ := message.uintField(18)
	responseMessage, err := decodeDiagnosticsPairingMessage(response)
	if err != nil {
		return nil, err
	}
	responseDigest, err := responseMessage.digest()
	if err != nil {
		return nil, err
	}
	authorization.State = "revoked"
	authorization.AppEpoch = proposedEpoch
	authorization.CurrentStateDigest = append([]byte(nil), responseDigest[:]...)
	authorization.TerminalReplyExpires = now.Add(diagnosticsPairingTerminalReplay).Unix()
	authorization.Transition = nil
	authorization.Replays = append(authorization.Replays, diagnosticsPairingReplay{
		RequestDigest: append([]byte(nil), digest[:]...), Response: append([]byte(nil), response...), RetainUntil: authorization.TerminalReplyExpires,
	})
	state.Revocations = append(state.Revocations, diagnosticsPairingRevocation{
		AppKeyID:           append([]byte(nil), authorization.AppKeyID...),
		FolderBinding:      append([]byte(nil), authorization.FolderBinding...),
		AuthorizationEpoch: proposedEpoch,
		Reason:             reason,
		RetainUntil:        now.Add(diagnosticsPairingTerminalReplay).Unix(),
	})
	return response, nil
}

func (manager *diagnosticsPairingManager) beginHelperKeyRotation(recordID string) ([]byte, []byte, error) {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	now := manager.now()
	if err := manager.expirePairingAuthorizations(now); err != nil {
		return nil, nil, err
	}
	var proposal, proof []byte
	err := manager.store.update(func(state *diagnosticsCredentialState) error {
		index := diagnosticsAuthorizationIndex(state.Authorizations, recordID)
		if index < 0 || state.Authorizations[index].Transition != nil {
			return errDiagnosticsPairingUnavailable
		}
		authorization := &state.Authorizations[index]
		var seed, proposedPublic, proposedKeyID []byte
		var proposedEpoch uint64
		for _, existing := range state.Authorizations {
			transition := existing.Transition
			if transition != nil && transition.Kind == diagnosticsPairingTransitionHelperKey {
				seed = append([]byte(nil), transition.ProposedHelperSeed...)
				privateKey, keyErr := diagnosticsSigningPrivateKey(seed)
				if keyErr != nil {
					return keyErr
				}
				proposedPublic = append([]byte(nil), privateKey.Public().(ed25519.PublicKey)...)
				proposedKeyID = append([]byte(nil), transition.ProposedHelperKeyID...)
				proposedEpoch = transition.ProposedHelperEpoch
				break
			}
		}
		if seed == nil {
			var err error
			seed, proposedPublic, proposedKeyID, err = newDiagnosticsSigningIdentity(manager.random)
			if err != nil {
				return err
			}
			proposedEpoch = authorization.HelperEpoch + 1
		}
		if proposedEpoch != authorization.HelperEpoch+1 {
			return errDiagnosticsPairingUnavailable
		}
		nonce, err := manager.newLifecycleNonce(authorization)
		if err != nil {
			return err
		}
		fields, err := diagnosticsLifecycleBaseFields(*authorization, state.Identity, diagnosticsPairingHelperKeyRotationPropose, uint64(now.Unix()), uint64(now.Add(diagnosticsPairingLifetime).Unix()), nonce)
		if err != nil {
			return err
		}
		fields = append(fields,
			diagnosticsCBORMapField(13, diagnosticsCBORBstr(proposedPublic)),
			diagnosticsCBORMapField(14, diagnosticsCBORBstr(proposedKeyID)),
			diagnosticsCBORMapField(20, diagnosticsCBORUint(proposedEpoch)),
		)
		currentPrivate, err := diagnosticsSigningPrivateKey(state.Identity.SigningSeed)
		if err != nil {
			return err
		}
		proposal, err = signDiagnosticsPairingMessage(diagnosticsCBORMapValue(fields...), currentPrivate)
		if err != nil {
			return err
		}
		proposalMessage, _ := decodeDiagnosticsPairingMessage(proposal)
		proposalDigest, _ := proposalMessage.digest()
		proposedPrivate, err := diagnosticsSigningPrivateKey(seed)
		if err != nil {
			return err
		}
		proofNonce, err := manager.newLifecycleNonce(authorization)
		if err != nil {
			return err
		}
		proof, err = buildDiagnosticsLifecycleContinuation(proposalMessage, diagnosticsPairingHelperKeyRotationNewProof, 0, nil, proposedPrivate, uint64(now.Unix()), uint64(now.Add(diagnosticsPairingLifetime).Unix()), proofNonce)
		if err != nil {
			return err
		}
		proofMessage, _ := decodeDiagnosticsPairingMessage(proof)
		proofDigest, _ := proofMessage.digest()
		authorization.Transition = &diagnosticsPairingTransition{
			Kind:                diagnosticsPairingTransitionHelperKey,
			Stage:               "proof",
			TransitionDigest:    append([]byte(nil), proposalDigest[:]...),
			LatestMessageDigest: append([]byte(nil), proofDigest[:]...),
			ProposedHelperSeed:  seed,
			ProposedHelperKeyID: proposedKeyID,
			ProposedHelperEpoch: proposedEpoch,
			ExpiresAt:           now.Add(diagnosticsPairingLifetime).Unix(),
		}
		return nil
	})
	return proposal, proof, err
}

func (manager *diagnosticsPairingManager) beginTLSPinRotation(recordID string) ([]byte, error) {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	now := manager.now()
	if err := manager.expirePairingAuthorizations(now); err != nil {
		return nil, err
	}
	var proposal []byte
	err := manager.store.update(func(state *diagnosticsCredentialState) error {
		index := diagnosticsAuthorizationIndex(state.Authorizations, recordID)
		if index < 0 || state.Authorizations[index].Transition != nil {
			return errDiagnosticsPairingUnavailable
		}
		authorization := &state.Authorizations[index]
		var privatePKCS8, pin []byte
		for _, existing := range state.Authorizations {
			transition := existing.Transition
			if transition != nil && transition.Kind == diagnosticsPairingTransitionTLSPin {
				privatePKCS8 = append([]byte(nil), transition.ProposedTLSPrivate...)
				pin = append([]byte(nil), transition.ProposedTLSPin...)
				break
			}
		}
		if privatePKCS8 == nil {
			var err error
			privatePKCS8, _, pin, err = newDiagnosticsTLSIdentity(manager.random)
			if err != nil {
				return err
			}
		}
		nonce, err := manager.newLifecycleNonce(authorization)
		if err != nil {
			return err
		}
		fields, err := diagnosticsLifecycleBaseFields(*authorization, state.Identity, diagnosticsPairingTLSPinRotationPropose, uint64(now.Unix()), uint64(now.Add(diagnosticsPairingLifetime).Unix()), nonce)
		if err != nil {
			return err
		}
		fields = append(fields, diagnosticsCBORMapField(16, diagnosticsCBORBstr(pin)))
		currentPrivate, err := diagnosticsSigningPrivateKey(state.Identity.SigningSeed)
		if err != nil {
			return err
		}
		proposal, err = signDiagnosticsPairingMessage(diagnosticsCBORMapValue(fields...), currentPrivate)
		if err != nil {
			return err
		}
		message, _ := decodeDiagnosticsPairingMessage(proposal)
		digest, _ := message.digest()
		authorization.Transition = &diagnosticsPairingTransition{
			Kind:                diagnosticsPairingTransitionTLSPin,
			Stage:               "proof",
			TransitionDigest:    append([]byte(nil), digest[:]...),
			LatestMessageDigest: append([]byte(nil), digest[:]...),
			ProposedTLSPrivate:  privatePKCS8,
			ProposedTLSPin:      pin,
			ExpiresAt:           now.Add(diagnosticsPairingLifetime).Unix(),
		}
		return nil
	})
	return proposal, err
}

func (manager *diagnosticsPairingManager) confirmLifecycleTransition(recordID string, transitionDigest []byte) error {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	nowUnix := manager.now().Unix()
	return manager.store.update(func(state *diagnosticsCredentialState) error {
		index := diagnosticsAuthorizationIndex(state.Authorizations, recordID)
		if index < 0 {
			return errDiagnosticsPairingUnavailable
		}
		authorization := &state.Authorizations[index]
		transition := authorization.Transition
		if transition == nil || transition.Stage != "committed" || nowUnix > transition.ExpiresAt ||
			subtle.ConstantTimeCompare(transition.TransitionDigest, transitionDigest) != 1 {
			return errDiagnosticsPairingInvalid
		}
		switch transition.Kind {
		case diagnosticsPairingTransitionAppKey:
			authorization.AppPublicKey = append([]byte(nil), transition.ProposedAppPublicKey...)
			authorization.AppKeyID = append([]byte(nil), transition.ProposedAppKeyID...)
			authorization.AppEpoch = transition.ProposedAppEpoch
			authorization.RecordID = diagnosticsAuthorizationRecordID(authorization.AppKeyID, authorization.FolderBinding)
		case diagnosticsPairingTransitionHelperKey:
			if !allDiagnosticsAuthorizationsCommittedForHelper(state.Authorizations, transition, nowUnix) {
				return errDiagnosticsPairingUnavailable
			}
			state.Identity.SigningSeed = append([]byte(nil), transition.ProposedHelperSeed...)
			state.Identity.HelperEpoch = transition.ProposedHelperEpoch
			for item := range state.Authorizations {
				candidate := state.Authorizations[item].Transition
				if candidate != nil && candidate.Kind == diagnosticsPairingTransitionHelperKey && candidate.Stage == "committed" {
					state.Authorizations[item].HelperEpoch = candidate.ProposedHelperEpoch
					state.Authorizations[item].CurrentStateDigest = append([]byte(nil), candidate.LatestMessageDigest...)
					state.Authorizations[item].Transition = nil
				}
			}
			return nil
		case diagnosticsPairingTransitionTLSPin:
			if !allDiagnosticsAuthorizationsCommittedForTLS(state.Authorizations, transition, nowUnix) {
				return errDiagnosticsPairingUnavailable
			}
			state.Identity.TLSPrivatePKCS8 = append([]byte(nil), transition.ProposedTLSPrivate...)
			for item := range state.Authorizations {
				candidate := state.Authorizations[item].Transition
				if candidate != nil && candidate.Kind == diagnosticsPairingTransitionTLSPin && candidate.Stage == "committed" {
					state.Authorizations[item].TLSSPKIPin = append([]byte(nil), candidate.ProposedTLSPin...)
					state.Authorizations[item].CurrentStateDigest = append([]byte(nil), candidate.LatestMessageDigest...)
					state.Authorizations[item].Transition = nil
				}
			}
			return nil
		default:
			return errDiagnosticsPairingInvalid
		}
		authorization.CurrentStateDigest = append([]byte(nil), transition.LatestMessageDigest...)
		authorization.Transition = nil
		sortDiagnosticsAuthorizations(state.Authorizations)
		return nil
	})
}

func diagnosticsLifecycleCommonMatches(authorization diagnosticsPairingAuthorization, identity diagnosticsHelperCredentialIdentity, message diagnosticsPairingMessage) bool {
	helperPrivate, err := diagnosticsSigningPrivateKey(identity.SigningSeed)
	if err != nil {
		return false
	}
	helperPublic := helperPrivate.Public().(ed25519.PublicKey)
	helperKeyID := diagnosticsKeyID(helperPublic)
	bytesChecks := []struct {
		label uint64
		want  []byte
	}{
		{5, authorization.HomeserverBinding}, {6, authorization.FolderBinding},
		{7, authorization.AppPublicKey}, {8, authorization.AppKeyID},
		{11, helperPublic}, {12, helperKeyID[:]}, {15, authorization.TLSSPKIPin},
		{26, authorization.CurrentStateDigest},
	}
	for _, check := range bytesChecks {
		got, ok := message.bytesField(check.label, 32)
		if !ok || subtle.ConstantTimeCompare(got, check.want) != 1 {
			return false
		}
	}
	appEpoch, _ := message.uintField(17)
	helperEpoch, _ := message.uintField(19)
	return appEpoch == authorization.AppEpoch && helperEpoch == authorization.HelperEpoch
}

func diagnosticsLifecycleTimeValid(message diagnosticsPairingMessage, now time.Time) bool {
	issued, issuedOK := message.uintField(21)
	expires, expiresOK := message.uintField(22)
	if !issuedOK || !expiresOK || expires <= issued || expires-issued > uint64(diagnosticsPairingLifetime/time.Second) {
		return false
	}
	nowUnix := now.Unix()
	return int64(issued) <= nowUnix+int64(diagnosticsPairingClockSkew/time.Second) &&
		int64(expires)+int64(diagnosticsPairingClockSkew/time.Second) >= nowUnix
}

func diagnosticsLifecycleNoReplyReplay(authorization diagnosticsPairingAuthorization, messageType uint64, digest []byte) bool {
	transition := authorization.Transition
	if transition == nil || subtle.ConstantTimeCompare(transition.LatestMessageDigest, digest) != 1 {
		return false
	}
	switch messageType {
	case diagnosticsPairingAppKeyRotationRequest:
		return transition.Kind == diagnosticsPairingTransitionAppKey && transition.Stage == "request"
	case diagnosticsPairingHelperKeyRotationConfirm:
		return transition.Kind == diagnosticsPairingTransitionHelperKey && transition.Stage == "accepted"
	case diagnosticsPairingTLSPinRotationConfirm:
		return transition.Kind == diagnosticsPairingTransitionTLSPin && transition.Stage == "accepted"
	default:
		return false
	}
}

func diagnosticsRememberLifecycleNonce(authorization *diagnosticsPairingAuthorization, nonce []byte) bool {
	if len(nonce) != 32 || len(authorization.LifecycleNonces) >= 256 {
		return false
	}
	for _, existing := range authorization.LifecycleNonces {
		if subtle.ConstantTimeCompare(existing, nonce) == 1 {
			return false
		}
	}
	authorization.LifecycleNonces = append(authorization.LifecycleNonces, append([]byte(nil), nonce...))
	return true
}

func (manager *diagnosticsPairingManager) newLifecycleNonce(authorization *diagnosticsPairingAuthorization) ([]byte, error) {
	for range 4 {
		nonce, err := readDiagnosticsRandom(manager.random, 32)
		if err != nil {
			return nil, err
		}
		if diagnosticsRememberLifecycleNonce(authorization, nonce) {
			return nonce, nil
		}
		clear(nonce)
	}
	return nil, errDiagnosticsPairingUnavailable
}

func allDiagnosticsAuthorizationsCommittedForHelper(authorizations []diagnosticsPairingAuthorization, transition *diagnosticsPairingTransition, nowUnix int64) bool {
	for _, authorization := range authorizations {
		if authorization.State != "active" {
			continue
		}
		candidate := authorization.Transition
		if candidate == nil || candidate.Kind != diagnosticsPairingTransitionHelperKey || candidate.Stage != "committed" || nowUnix > candidate.ExpiresAt ||
			candidate.ProposedHelperEpoch != transition.ProposedHelperEpoch ||
			subtle.ConstantTimeCompare(candidate.ProposedHelperKeyID, transition.ProposedHelperKeyID) != 1 {
			return false
		}
	}
	return true
}

func allDiagnosticsAuthorizationsCommittedForTLS(authorizations []diagnosticsPairingAuthorization, transition *diagnosticsPairingTransition, nowUnix int64) bool {
	for _, authorization := range authorizations {
		if authorization.State != "active" {
			continue
		}
		candidate := authorization.Transition
		if candidate == nil || candidate.Kind != diagnosticsPairingTransitionTLSPin || candidate.Stage != "committed" || nowUnix > candidate.ExpiresAt ||
			subtle.ConstantTimeCompare(candidate.ProposedTLSPin, transition.ProposedTLSPin) != 1 {
			return false
		}
	}
	return true
}

func (manager *diagnosticsPairingManager) revokeLocally(recordID string, reason uint64) ([]byte, error) {
	if reason < 1 || reason > 4 {
		return nil, errDiagnosticsPairingInvalid
	}
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	now := manager.now()
	var record []byte
	err := manager.store.update(func(state *diagnosticsCredentialState) error {
		index := diagnosticsAuthorizationIndex(state.Authorizations, recordID)
		if index < 0 {
			return errDiagnosticsPairingUnavailable
		}
		authorization := &state.Authorizations[index]
		if authorization.State == "revoked" {
			return nil
		}
		if authorization.State != "active" {
			return errDiagnosticsPairingUnavailable
		}
		nonce, err := manager.newLifecycleNonce(authorization)
		if err != nil {
			return err
		}
		fields, err := diagnosticsLifecycleBaseFields(*authorization, state.Identity, diagnosticsPairingRevocationRecord, uint64(now.Unix()), uint64(now.Add(diagnosticsPairingLifetime).Unix()), nonce)
		if err != nil {
			return err
		}
		fields = append(fields,
			diagnosticsCBORMapField(18, diagnosticsCBORUint(authorization.AppEpoch+1)),
			diagnosticsCBORMapField(25, diagnosticsCBORUint(reason)),
			diagnosticsCBORMapField(27, diagnosticsCBORUint(diagnosticsPairingRevocationLocalHelperAdmin)),
		)
		helperPrivate, err := diagnosticsSigningPrivateKey(state.Identity.SigningSeed)
		if err != nil {
			return err
		}
		record, err = signDiagnosticsPairingMessage(diagnosticsCBORMapValue(fields...), helperPrivate)
		if err != nil {
			return err
		}
		message, err := decodeDiagnosticsPairingMessage(record)
		if err != nil {
			return err
		}
		digest, err := message.digest()
		if err != nil {
			return err
		}
		authorization.State = "revoked"
		authorization.AppEpoch++
		authorization.CurrentStateDigest = append([]byte(nil), digest[:]...)
		authorization.TerminalReplyExpires = now.Add(diagnosticsPairingTerminalReplay).Unix()
		authorization.Transition = nil
		state.Revocations = append(state.Revocations, diagnosticsPairingRevocation{
			AppKeyID:           append([]byte(nil), authorization.AppKeyID...),
			FolderBinding:      append([]byte(nil), authorization.FolderBinding...),
			AuthorizationEpoch: authorization.AppEpoch,
			Reason:             reason,
			RetainUntil:        now.Add(diagnosticsPairingTerminalReplay).Unix(),
		})
		return nil
	})
	return record, err
}

func diagnosticsTransitionDigest(message diagnosticsPairingMessage) ([]byte, error) {
	digest, err := message.digest()
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), digest[:]...), nil
}

func diagnosticsIsPairingUnavailable(err error) bool {
	return errors.Is(err, errDiagnosticsPairingUnavailable) || errors.Is(err, errDiagnosticsPairingExpired)
}
