package main

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"
)

type diagnosticsRuntimeSession struct {
	recordID            string
	stateDigest         [32]byte
	installationBinding [32]byte
	handle              *diagnosticsNamespaceRootHandle
	binding             diagnosticsUploadBinding
	attestor            *diagnosticsUploadAttestor
	response            *diagnosticsResponseFoundation
}

type diagnosticsRuntimeSessions struct {
	config          *diagnosticsRuntimeConfig
	credentialStore *diagnosticsCredentialStore
	namespaceStore  *diagnosticsNamespaceStateStore
	coordinator     *diagnosticsUploadCoordinator
	mutex           sync.Mutex
	sessions        map[string]*diagnosticsRuntimeSession
}

type diagnosticsLifecycleCapabilityConfirmation struct {
	recordID         string
	transitionDigest []byte
}

type diagnosticsRuntimeCapabilitySession struct {
	binding      diagnosticsUploadBinding
	confirmation *diagnosticsLifecycleCapabilityConfirmation
	release      func()
}

func newDiagnosticsRuntimeSessions(
	config *diagnosticsRuntimeConfig,
	credentialStore *diagnosticsCredentialStore,
	namespaceStore *diagnosticsNamespaceStateStore,
) *diagnosticsRuntimeSessions {
	return &diagnosticsRuntimeSessions{
		config: config, credentialStore: credentialStore, namespaceStore: namespaceStore,
		coordinator: newDiagnosticsUploadCoordinator(), sessions: make(map[string]*diagnosticsRuntimeSession),
	}
}

func (sessions *diagnosticsRuntimeSessions) sessionForMessage(data []byte) (*diagnosticsRuntimeSession, error) {
	value, err := decodeDiagnosticsCBOR(data)
	if err != nil || value.kind != diagnosticsCBORMap {
		return nil, errDiagnosticsCapabilityInvalid
	}
	folderBinding, okFolder := diagnosticsUploadBytesField(value, 6, 32)
	appKeyID, okApp := diagnosticsUploadBytesField(value, 7, 32)
	if !okFolder || !okApp {
		return nil, errDiagnosticsCapabilityInvalid
	}
	authorization, identity, err := sessions.activeAuthorization(appKeyID, folderBinding)
	if err != nil {
		return nil, err
	}
	return sessions.sessionForAuthorization(authorization, identity)
}

func (sessions *diagnosticsRuntimeSessions) sessionForCapabilityMessage(
	data []byte,
	now time.Time,
) (diagnosticsRuntimeCapabilitySession, error) {
	value, err := decodeDiagnosticsCBOR(data)
	if err != nil || value.kind != diagnosticsCBORMap || now.Unix() < 0 {
		return diagnosticsRuntimeCapabilitySession{}, errDiagnosticsCapabilityInvalid
	}
	messageType, typeOK := diagnosticsUploadUintField(value, 4)
	folderBinding, folderOK := diagnosticsUploadBytesField(value, 6, 32)
	appKeyID, appOK := diagnosticsUploadBytesField(value, 7, 32)
	helperKeyID, helperOK := diagnosticsUploadBytesField(value, 8, 32)
	appEpoch, appEpochOK := diagnosticsUploadUintField(value, 9)
	helperEpoch, helperEpochOK := diagnosticsUploadUintField(value, 10)
	if !typeOK || messageType != diagnosticsCapabilityQuery || !folderOK || !appOK || !helperOK || !appEpochOK || !helperEpochOK {
		return diagnosticsRuntimeCapabilitySession{}, errDiagnosticsCapabilityInvalid
	}
	state, err := sessions.credentialStore.snapshot()
	if err != nil {
		return diagnosticsRuntimeCapabilitySession{}, errDiagnosticsPairingUnavailable
	}
	currentHelperPrivate, err := diagnosticsSigningPrivateKey(state.Identity.SigningSeed)
	if err != nil {
		return diagnosticsRuntimeCapabilitySession{}, errDiagnosticsPairingUnavailable
	}
	for _, authorization := range state.Authorizations {
		if authorization.State != "active" || !bytes.Equal(authorization.FolderBinding, folderBinding) {
			continue
		}
		expectedAppPublic := ed25519.PublicKey(authorization.AppPublicKey)
		expectedAppKeyID := authorization.AppKeyID
		expectedAppEpoch := authorization.AppEpoch
		expectedHelperPrivate := currentHelperPrivate
		expectedHelperKeyID := diagnosticsKeyID(currentHelperPrivate.Public().(ed25519.PublicKey))
		expectedHelperEpoch := authorization.HelperEpoch
		var confirmation *diagnosticsLifecycleCapabilityConfirmation
		if transition := authorization.Transition; transition != nil {
			if transition.Stage != "committed" || now.Unix() >= transition.ExpiresAt {
				continue
			}
			switch transition.Kind {
			case diagnosticsPairingTransitionAppKey:
				expectedAppPublic = ed25519.PublicKey(transition.ProposedAppPublicKey)
				expectedAppKeyID = transition.ProposedAppKeyID
				expectedAppEpoch = transition.ProposedAppEpoch
			case diagnosticsPairingTransitionHelperKey:
				expectedHelperPrivate, err = diagnosticsSigningPrivateKey(transition.ProposedHelperSeed)
				if err != nil {
					continue
				}
				copy(expectedHelperKeyID[:], transition.ProposedHelperKeyID)
				expectedHelperEpoch = transition.ProposedHelperEpoch
			case diagnosticsPairingTransitionTLSPin:
				if !allDiagnosticsAuthorizationsCommittedForTLS(state.Authorizations, transition, now.Unix()) {
					continue
				}
			default:
				continue
			}
			confirmation = &diagnosticsLifecycleCapabilityConfirmation{
				recordID: authorization.RecordID, transitionDigest: append([]byte(nil), transition.TransitionDigest...),
			}
		}
		if !bytes.Equal(appKeyID, expectedAppKeyID) || appEpoch != expectedAppEpoch ||
			!bytes.Equal(helperKeyID, expectedHelperKeyID[:]) || helperEpoch != expectedHelperEpoch {
			continue
		}

		baseAuthorization := authorization
		baseAuthorization.Transition = nil
		baseSession, sessionErr := sessions.sessionForAuthorization(baseAuthorization, state.Identity)
		var binding diagnosticsUploadBinding
		var release func()
		if sessionErr == nil {
			binding = cloneDiagnosticsRuntimeBinding(baseSession.binding)
		} else if authorization.Transition == nil {
			binding, release, sessionErr = sessions.capabilityOnlyBinding(authorization, state.Identity)
		}
		if sessionErr != nil {
			continue
		}
		binding.appPublicKey = append(ed25519.PublicKey(nil), expectedAppPublic...)
		binding.helperPrivateKey = append(ed25519.PrivateKey(nil), expectedHelperPrivate...)
		binding.appEpoch = expectedAppEpoch
		binding.helperEpoch = expectedHelperEpoch
		return diagnosticsRuntimeCapabilitySession{binding: binding, confirmation: confirmation, release: release}, nil
	}
	return diagnosticsRuntimeCapabilitySession{}, errDiagnosticsPairingUnavailable
}

func cloneDiagnosticsRuntimeBinding(binding diagnosticsUploadBinding) diagnosticsUploadBinding {
	binding.installationBinding = append([]byte(nil), binding.installationBinding...)
	binding.homeserverBinding = append([]byte(nil), binding.homeserverBinding...)
	binding.folderBinding = append([]byte(nil), binding.folderBinding...)
	binding.appPublicKey = append(ed25519.PublicKey(nil), binding.appPublicKey...)
	binding.helperPrivateKey = append(ed25519.PrivateKey(nil), binding.helperPrivateKey...)
	return binding
}

// capabilityOnlyBinding is the narrow reconciliation bridge after a key
// transition has become active but before the app's next immutable namespace
// authorization has arrived. It never constructs an attestor or response
// foundation, so every operation endpoint remains unavailable.
func (sessions *diagnosticsRuntimeSessions) capabilityOnlyBinding(
	authorization diagnosticsPairingAuthorization,
	identity diagnosticsHelperCredentialIdentity,
) (diagnosticsUploadBinding, func(), error) {
	rootRecord, folderConfig, err := sessions.rootAndFolder(authorization.FolderBinding)
	if err != nil {
		return diagnosticsUploadBinding{}, nil, err
	}
	mountPath, err := sessions.config.mountPath(folderConfig.MountAlias)
	if err != nil {
		return diagnosticsUploadBinding{}, nil, errDiagnosticsNamespaceUnsupported
	}
	handle, err := openDiagnosticsNamespaceRoot(mountPath, nil)
	if err != nil {
		return diagnosticsUploadBinding{}, nil, errDiagnosticsNamespaceUnsupported
	}
	release := func() { _ = handle.Close() }
	if handle.ValidateRootRecord(rootRecord) != nil {
		release()
		return diagnosticsUploadBinding{}, nil, errDiagnosticsNamespaceConflict
	}
	if scanErr := handle.ScanFixedLayout(); scanErr != nil && handle.ScanFixedLayoutDuringHelperRotation() != nil {
		release()
		return diagnosticsUploadBinding{}, nil, errDiagnosticsNamespaceConflict
	}
	installationBinding := diagnosticsRuntimeInstallationBinding(authorization)
	authorizationPath := diagnosticsRuntimeAuthorizationPath(installationBinding[:], authorization.NamespaceAuthorizationEpoch)
	body, _, err := handle.ReadImmutable(authorizationPath)
	if err != nil {
		release()
		return diagnosticsUploadBinding{}, nil, errDiagnosticsNamespaceConflict
	}
	record, err := decodeDiagnosticsNamespaceMessage(body)
	if err != nil {
		release()
		return diagnosticsUploadBinding{}, nil, errDiagnosticsNamespaceConflict
	}
	installation, _ := record.bytesField(8, 32)
	initialAppKeyID, _ := record.bytesField(9, 32)
	homeserver, _ := record.bytesField(5, 32)
	folder, _ := record.bytesField(6, 32)
	namespaceID, _ := record.bytesField(7, 32)
	recordAppPublic, _ := record.bytesField(10, 32)
	recordAppEpoch, _ := record.uintField(12)
	recordHelperPublic, _ := record.bytesField(13, 32)
	recordHelperEpoch, _ := record.uintField(15)
	recordAuthorizationEpoch, _ := record.uintField(31)
	recordCredentialDigest, _ := record.bytesField(25, 32)
	currentHelperPrivate, helperErr := diagnosticsSigningPrivateKey(identity.SigningSeed)
	if helperErr != nil {
		release()
		return diagnosticsUploadBinding{}, nil, errDiagnosticsPairingUnavailable
	}
	currentHelperPublic := currentHelperPrivate.Public().(ed25519.PublicKey)
	appAdvanced := authorization.AppEpoch == recordAppEpoch+1 && authorization.HelperEpoch == recordHelperEpoch &&
		bytes.Equal(recordHelperPublic, currentHelperPublic)
	helperAdvanced := authorization.HelperEpoch == recordHelperEpoch+1 && authorization.AppEpoch == recordAppEpoch &&
		bytes.Equal(recordAppPublic, authorization.AppPublicKey)
	stateOnlyAdvanced := authorization.AppEpoch == recordAppEpoch && authorization.HelperEpoch == recordHelperEpoch &&
		bytes.Equal(recordAppPublic, authorization.AppPublicKey) && bytes.Equal(recordHelperPublic, currentHelperPublic) &&
		!bytes.Equal(recordCredentialDigest, authorization.CurrentStateDigest)
	if !bytes.Equal(installation, installationBinding[:]) || !bytes.Equal(initialAppKeyID, authorization.NamespaceInitialAppKeyID) ||
		!bytes.Equal(homeserver, authorization.HomeserverBinding) || !bytes.Equal(folder, authorization.FolderBinding) ||
		!bytes.Equal(namespaceID, rootRecord.NamespaceID) || recordAuthorizationEpoch != authorization.NamespaceAuthorizationEpoch ||
		(!appAdvanced && !helperAdvanced && !stateOnlyAdvanced) {
		release()
		return diagnosticsUploadBinding{}, nil, errDiagnosticsNamespaceConflict
	}
	return diagnosticsUploadBinding{
		namespaceHandle: handle, installationBinding: append([]byte(nil), installationBinding[:]...),
		homeserverBinding: append([]byte(nil), authorization.HomeserverBinding...),
		folderBinding:     append([]byte(nil), authorization.FolderBinding...),
		appPublicKey:      append(ed25519.PublicKey(nil), authorization.AppPublicKey...),
		helperPrivateKey:  append(ed25519.PrivateKey(nil), currentHelperPrivate...),
		appEpoch:          authorization.AppEpoch, helperEpoch: authorization.HelperEpoch,
		authorizationEpoch: authorization.NamespaceAuthorizationEpoch,
	}, release, nil
}

func (sessions *diagnosticsRuntimeSessions) activeAuthorization(
	appKeyID, folderBinding []byte,
) (diagnosticsPairingAuthorization, diagnosticsHelperCredentialIdentity, error) {
	state, err := sessions.credentialStore.snapshot()
	if err != nil {
		return diagnosticsPairingAuthorization{}, diagnosticsHelperCredentialIdentity{}, errDiagnosticsPairingUnavailable
	}
	for _, authorization := range state.Authorizations {
		if authorization.State == "active" && authorization.Transition == nil &&
			bytes.Equal(authorization.AppKeyID, appKeyID) && bytes.Equal(authorization.FolderBinding, folderBinding) {
			return authorization, state.Identity, nil
		}
	}
	return diagnosticsPairingAuthorization{}, diagnosticsHelperCredentialIdentity{}, errDiagnosticsPairingUnavailable
}

func (sessions *diagnosticsRuntimeSessions) sessionForAuthorization(
	authorization diagnosticsPairingAuthorization,
	identity diagnosticsHelperCredentialIdentity,
) (*diagnosticsRuntimeSession, error) {
	if len(authorization.NamespaceInitialAppKeyID) != 32 || authorization.NamespaceAuthorizationEpoch == 0 {
		return nil, errDiagnosticsPairingUnavailable
	}
	fingerprint := diagnosticsRuntimeSessionFingerprint(authorization, identity)
	sessions.mutex.Lock()
	defer sessions.mutex.Unlock()
	if existing := sessions.sessions[authorization.RecordID]; existing != nil && existing.stateDigest == fingerprint {
		return existing, nil
	}
	for recordID, existing := range sessions.sessions {
		if recordID == authorization.RecordID || existing.installationBinding == diagnosticsRuntimeInstallationBinding(authorization) {
			_ = existing.handle.Close()
			delete(sessions.sessions, recordID)
		}
	}
	session, err := sessions.openSession(authorization, identity, fingerprint)
	if err != nil {
		return nil, err
	}
	sessions.sessions[authorization.RecordID] = session
	return session, nil
}

func (sessions *diagnosticsRuntimeSessions) openSession(
	authorization diagnosticsPairingAuthorization,
	identity diagnosticsHelperCredentialIdentity,
	fingerprint [32]byte,
) (*diagnosticsRuntimeSession, error) {
	rootRecord, folderConfig, err := sessions.rootAndFolder(authorization.FolderBinding)
	if err != nil {
		return nil, err
	}
	mountPath, err := sessions.config.mountPath(folderConfig.MountAlias)
	if err != nil {
		return nil, errDiagnosticsNamespaceUnsupported
	}
	handle, err := openDiagnosticsNamespaceRoot(mountPath, nil)
	if err != nil {
		return nil, errDiagnosticsNamespaceUnsupported
	}
	keepHandle := false
	defer func() {
		if !keepHandle {
			_ = handle.Close()
		}
	}()
	if handle.ValidateRootRecord(rootRecord) != nil || handle.ScanFixedLayout() != nil {
		return nil, errDiagnosticsNamespaceConflict
	}
	installationBinding := diagnosticsRuntimeInstallationBinding(authorization)
	helperPrivate, err := diagnosticsSigningPrivateKey(identity.SigningSeed)
	if err != nil {
		return nil, errDiagnosticsPairingUnavailable
	}
	binding := diagnosticsUploadBinding{
		namespaceHandle:     handle,
		installationBinding: append([]byte(nil), installationBinding[:]...),
		homeserverBinding:   append([]byte(nil), authorization.HomeserverBinding...),
		folderBinding:       append([]byte(nil), authorization.FolderBinding...),
		appPublicKey:        append(ed25519.PublicKey(nil), authorization.AppPublicKey...),
		helperPrivateKey:    append(ed25519.PrivateKey(nil), helperPrivate...),
		appEpoch:            authorization.AppEpoch,
		helperEpoch:         authorization.HelperEpoch,
		authorizationEpoch:  authorization.NamespaceAuthorizationEpoch,
	}
	if err := validateDiagnosticsBindingAuthorization(binding, diagnosticsUploadVerificationContext{
		appPublicKey: binding.appPublicKey, helperPublicKey: helperPrivate.Public().(ed25519.PublicKey),
	}); err != nil {
		return nil, errDiagnosticsNamespaceConflict
	}
	if !diagnosticsRuntimeAuthorizationMatchesCredentialState(handle, installationBinding, authorization) {
		return nil, errDiagnosticsNamespaceConflict
	}
	attestor, err := newDiagnosticsUploadAttestor(binding, sessions.coordinator, diagnosticsUploadAttestorHooks{})
	if err != nil {
		return nil, err
	}
	response, err := newDiagnosticsResponseFoundation(binding, sessions.coordinator, diagnosticsResponseFoundationHooks{})
	if err != nil {
		return nil, err
	}
	keepHandle = true
	return &diagnosticsRuntimeSession{
		recordID: authorization.RecordID, stateDigest: fingerprint, installationBinding: installationBinding,
		handle: handle, binding: binding, attestor: attestor, response: response,
	}, nil
}

func diagnosticsRuntimeAuthorizationMatchesCredentialState(
	handle *diagnosticsNamespaceRootHandle,
	installationBinding [32]byte,
	authorization diagnosticsPairingAuthorization,
) bool {
	if handle == nil || len(authorization.NamespaceInitialAppKeyID) != 32 ||
		len(authorization.CurrentStateDigest) != 32 || authorization.NamespaceAuthorizationEpoch == 0 {
		return false
	}
	path := diagnosticsRuntimeAuthorizationPath(installationBinding[:], authorization.NamespaceAuthorizationEpoch)
	body, _, err := handle.ReadImmutable(path)
	if err != nil {
		return false
	}
	record, err := decodeDiagnosticsNamespaceMessage(body)
	if err != nil {
		return false
	}
	initialAppKeyID, _ := record.bytesField(9, 32)
	credentialDigest, _ := record.bytesField(25, 32)
	authorizationEpoch, _ := record.uintField(31)
	return bytes.Equal(initialAppKeyID, authorization.NamespaceInitialAppKeyID) &&
		bytes.Equal(credentialDigest, authorization.CurrentStateDigest) &&
		authorizationEpoch == authorization.NamespaceAuthorizationEpoch
}

func (sessions *diagnosticsRuntimeSessions) rootAndFolder(
	folderBinding []byte,
) (diagnosticsNamespaceRootRecord, diagnosticsRuntimeFolderConfig, error) {
	credentialState, err := sessions.credentialStore.snapshot()
	if err != nil {
		return diagnosticsNamespaceRootRecord{}, diagnosticsRuntimeFolderConfig{}, errDiagnosticsPairingUnavailable
	}
	namespaceState, err := sessions.namespaceStore.snapshot()
	if err != nil {
		return diagnosticsNamespaceRootRecord{}, diagnosticsRuntimeFolderConfig{}, errDiagnosticsNamespaceStateInvalid
	}
	var root diagnosticsNamespaceRootRecord
	rootFound := false
	for _, candidate := range namespaceState.Roots {
		if bytes.Equal(candidate.FolderBinding, folderBinding) {
			root = candidate
			rootFound = true
			break
		}
	}
	if !rootFound {
		return diagnosticsNamespaceRootRecord{}, diagnosticsRuntimeFolderConfig{}, errDiagnosticsNamespaceUnsupported
	}
	for _, folder := range sessions.config.Folders {
		digest, digestErr := diagnosticsFolderIDDigest(folder.FolderID)
		if digestErr != nil {
			continue
		}
		for _, stored := range credentialState.Folders {
			if bytes.Equal(stored.FolderIDDigest, digest[:]) && bytes.Equal(stored.FolderBinding, folderBinding) &&
				folder.MountAlias == root.MountAlias {
				return root, folder, nil
			}
		}
	}
	return diagnosticsNamespaceRootRecord{}, diagnosticsRuntimeFolderConfig{}, errDiagnosticsNamespaceUnsupported
}

func diagnosticsRuntimeInstallationBinding(authorization diagnosticsPairingAuthorization) [32]byte {
	binding, err := diagnosticsNamespaceInstallationBinding(
		authorization.NamespaceInitialAppKeyID, authorization.HomeserverBinding, authorization.FolderBinding,
	)
	if err != nil {
		return [32]byte{}
	}
	return binding
}

func diagnosticsRuntimeSessionFingerprint(
	authorization diagnosticsPairingAuthorization,
	identity diagnosticsHelperCredentialIdentity,
) [32]byte {
	body := make([]byte, 0, 32*5+24)
	body = append(body, authorization.CurrentStateDigest...)
	body = append(body, authorization.NamespaceInitialAppKeyID...)
	body = append(body, authorization.AppKeyID...)
	helperPrivate, _ := diagnosticsSigningPrivateKey(identity.SigningSeed)
	if len(helperPrivate) == ed25519.PrivateKeySize {
		helperKeyID := diagnosticsKeyID(helperPrivate.Public().(ed25519.PublicKey))
		body = append(body, helperKeyID[:]...)
	}
	body = append(body, authorization.FolderBinding...)
	body = append(body,
		byte(authorization.AppEpoch>>56), byte(authorization.AppEpoch>>48), byte(authorization.AppEpoch>>40), byte(authorization.AppEpoch>>32),
		byte(authorization.AppEpoch>>24), byte(authorization.AppEpoch>>16), byte(authorization.AppEpoch>>8), byte(authorization.AppEpoch),
		byte(authorization.HelperEpoch>>56), byte(authorization.HelperEpoch>>48), byte(authorization.HelperEpoch>>40), byte(authorization.HelperEpoch>>32),
		byte(authorization.HelperEpoch>>24), byte(authorization.HelperEpoch>>16), byte(authorization.HelperEpoch>>8), byte(authorization.HelperEpoch),
		byte(authorization.NamespaceAuthorizationEpoch>>56), byte(authorization.NamespaceAuthorizationEpoch>>48),
		byte(authorization.NamespaceAuthorizationEpoch>>40), byte(authorization.NamespaceAuthorizationEpoch>>32),
		byte(authorization.NamespaceAuthorizationEpoch>>24), byte(authorization.NamespaceAuthorizationEpoch>>16),
		byte(authorization.NamespaceAuthorizationEpoch>>8), byte(authorization.NamespaceAuthorizationEpoch),
	)
	return diagnosticsDomainSHA256("eu.vaultsync.runtime/v1/session\x00", body)
}

func (sessions *diagnosticsRuntimeSessions) close() error {
	sessions.mutex.Lock()
	defer sessions.mutex.Unlock()
	var result error
	for recordID, session := range sessions.sessions {
		if err := session.handle.Close(); err != nil && result == nil {
			result = err
		}
		delete(sessions.sessions, recordID)
	}
	return result
}

func (sessions *diagnosticsRuntimeSessions) markNamespaceAuthorization(
	recordID string,
	initialAppKeyID []byte,
	authorizationEpoch uint64,
	expectedAuthorization diagnosticsPairingAuthorization,
	expectedIdentity diagnosticsHelperCredentialIdentity,
) error {
	if len(initialAppKeyID) != 32 || authorizationEpoch == 0 {
		return errDiagnosticsNamespaceInvalid
	}
	return sessions.credentialStore.update(func(state *diagnosticsCredentialState) error {
		index := diagnosticsAuthorizationIndex(state.Authorizations, recordID)
		if index < 0 {
			return errDiagnosticsPairingUnavailable
		}
		authorization := &state.Authorizations[index]
		if authorization.State != "active" || authorization.Transition != nil ||
			!reflect.DeepEqual(*authorization, expectedAuthorization) || !reflect.DeepEqual(state.Identity, expectedIdentity) {
			return errDiagnosticsPairingUnavailable
		}
		if len(authorization.NamespaceInitialAppKeyID) == 0 {
			if authorizationEpoch != 1 || !bytes.Equal(initialAppKeyID, authorization.AppKeyID) {
				return errDiagnosticsNamespaceInvalid
			}
		} else if !bytes.Equal(authorization.NamespaceInitialAppKeyID, initialAppKeyID) ||
			authorizationEpoch != authorization.NamespaceAuthorizationEpoch+1 {
			return errDiagnosticsNamespaceConflict
		}
		authorization.NamespaceInitialAppKeyID = append([]byte(nil), initialAppKeyID...)
		authorization.NamespaceAuthorizationEpoch = authorizationEpoch
		return nil
	})
}

func diagnosticsRuntimeStateError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errDiagnosticsNamespaceUnsupported) || errors.Is(err, errDiagnosticsNamespaceConflict) ||
		errors.Is(err, errDiagnosticsPairingUnavailable) {
		return err
	}
	return fmt.Errorf("diagnostics runtime state: %w", err)
}
