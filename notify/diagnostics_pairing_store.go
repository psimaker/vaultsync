package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

const (
	diagnosticsCredentialStateFormat = 1
	diagnosticsCredentialStateFile   = "credentials-v1.json"
	diagnosticsCredentialLockFile    = ".credentials.lock"
	diagnosticsCredentialMaxBytes    = 1024 * 1024
)

var (
	errDiagnosticsCredentialStateInvalid = errors.New("diagnostics credential state unavailable")
	errDiagnosticsCredentialStateNewer   = errors.New("diagnostics credential state requires a newer helper")
)

type diagnosticsCredentialStoreHooks struct {
	beforeRename func() error
	afterRename  func() error
}

type diagnosticsCredentialStore struct {
	directory string
	statePath string
	random    io.Reader
	hooks     diagnosticsCredentialStoreHooks
	mutex     sync.Mutex
}

type diagnosticsCredentialState struct {
	FormatVersion  uint64                              `json:"format_version"`
	Revision       uint64                              `json:"revision"`
	Identity       diagnosticsHelperCredentialIdentity `json:"identity"`
	Folders        []diagnosticsFolderCredential       `json:"folders"`
	Authorizations []diagnosticsPairingAuthorization   `json:"authorizations"`
	Revocations    []diagnosticsPairingRevocation      `json:"revocations"`
	RateEvents     []int64                             `json:"rate_events"`
}

type diagnosticsHelperCredentialIdentity struct {
	SigningSeed       []byte `json:"signing_seed"`
	TLSPrivatePKCS8   []byte `json:"tls_private_pkcs8"`
	HelperEpoch       uint64 `json:"helper_epoch"`
	HomeserverBinding []byte `json:"homeserver_binding"`
	DeviceIDDigest    []byte `json:"device_id_digest"`
}

type diagnosticsFolderCredential struct {
	FolderIDDigest []byte `json:"folder_id_digest"`
	FolderBinding  []byte `json:"folder_binding"`
}

type diagnosticsPairingAuthorization struct {
	RecordID           string `json:"record_id"`
	State              string `json:"state"`
	HomeserverBinding  []byte `json:"homeserver_binding"`
	FolderBinding      []byte `json:"folder_binding"`
	AppPublicKey       []byte `json:"app_public_key"`
	AppKeyID           []byte `json:"app_key_id"`
	AppEpoch           uint64 `json:"app_epoch"`
	HelperEpoch        uint64 `json:"helper_epoch"`
	TLSSPKIPin         []byte `json:"tls_spki_pin"`
	InvitationNonce    []byte `json:"invitation_nonce,omitempty"`
	AppNonce           []byte `json:"app_nonce,omitempty"`
	HelperNonce        []byte `json:"helper_nonce,omitempty"`
	AppRequestDigest   []byte `json:"app_request_digest,omitempty"`
	CurrentStateDigest []byte `json:"current_state_digest,omitempty"`
	// NamespaceInitialAppKeyID remains stable across app-key rotation and is
	// set only after the exact initial dual-signed namespace authorization is
	// durably present. NamespaceAuthorizationEpoch identifies the exact current
	// immutable authorization record to revalidate after every restart.
	NamespaceInitialAppKeyID    []byte                        `json:"namespace_initial_app_key_id,omitempty"`
	NamespaceAuthorizationEpoch uint64                        `json:"namespace_authorization_epoch,omitempty"`
	ExpiresAt                   int64                         `json:"expires_at,omitempty"`
	TerminalReplyExpires        int64                         `json:"terminal_reply_expires,omitempty"`
	Replays                     []diagnosticsPairingReplay    `json:"replays,omitempty"`
	LifecycleNonces             [][]byte                      `json:"lifecycle_nonces,omitempty"`
	Transition                  *diagnosticsPairingTransition `json:"transition,omitempty"`
}

type diagnosticsPairingReplay struct {
	RequestDigest []byte `json:"request_digest"`
	Response      []byte `json:"response"`
	RetainUntil   int64  `json:"retain_until"`
}

type diagnosticsPairingTransition struct {
	Kind                 uint64 `json:"kind"`
	Stage                string `json:"stage"`
	TransitionDigest     []byte `json:"transition_digest"`
	LatestMessageDigest  []byte `json:"latest_message_digest"`
	ProposedAppPublicKey []byte `json:"proposed_app_public_key,omitempty"`
	ProposedAppKeyID     []byte `json:"proposed_app_key_id,omitempty"`
	ProposedAppEpoch     uint64 `json:"proposed_app_epoch,omitempty"`
	ProposedHelperSeed   []byte `json:"proposed_helper_seed,omitempty"`
	ProposedHelperKeyID  []byte `json:"proposed_helper_key_id,omitempty"`
	ProposedHelperEpoch  uint64 `json:"proposed_helper_epoch,omitempty"`
	ProposedTLSPrivate   []byte `json:"proposed_tls_private_pkcs8,omitempty"`
	ProposedTLSPin       []byte `json:"proposed_tls_pin,omitempty"`
	// ProposedStateConfirmed is set only after an exact capability query has
	// been authenticated under the committed proposed key/epoch (or, for TLS,
	// received while the globally committed proposed certificate is active).
	// It is persisted so a crash cannot silently switch an unconfirmed peer.
	ProposedStateConfirmed bool  `json:"proposed_state_confirmed,omitempty"`
	ExpiresAt              int64 `json:"expires_at"`
}

type diagnosticsPairingRevocation struct {
	AppKeyID           []byte `json:"app_key_id"`
	FolderBinding      []byte `json:"folder_binding"`
	AuthorizationEpoch uint64 `json:"authorization_epoch"`
	Reason             uint64 `json:"reason"`
	RetainUntil        int64  `json:"retain_until"`
}

func openDiagnosticsCredentialStore(directory string, deviceIDDigest []byte, random io.Reader) (*diagnosticsCredentialStore, error) {
	if directory == "" || len(deviceIDDigest) != 32 {
		return nil, errDiagnosticsCredentialStateInvalid
	}
	store := &diagnosticsCredentialStore{
		directory: filepath.Clean(directory),
		statePath: filepath.Join(filepath.Clean(directory), diagnosticsCredentialStateFile),
		random:    random,
	}
	if store.random == nil {
		store.random = diagnosticsCryptoRandomReader()
	}
	if err := store.ensureDirectory(); err != nil {
		return nil, err
	}
	err := store.withLock(func() error {
		state, err := store.loadUnlocked()
		if errors.Is(err, os.ErrNotExist) {
			state, err = newDiagnosticsCredentialState(deviceIDDigest, store.random)
			if err != nil {
				return err
			}
			return store.saveUnlocked(state)
		}
		if err != nil {
			return err
		}
		if !bytes.Equal(state.Identity.DeviceIDDigest, deviceIDDigest) {
			return errDiagnosticsCredentialStateInvalid
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return store, nil
}

func (store *diagnosticsCredentialStore) ensureDirectory() error {
	info, err := os.Lstat(store.directory)
	if errors.Is(err, os.ErrNotExist) {
		parent := filepath.Dir(store.directory)
		if parentInfo, parentErr := os.Stat(parent); parentErr != nil || !parentInfo.IsDir() {
			return errDiagnosticsCredentialStateInvalid
		}
		if err := os.Mkdir(store.directory, 0o700); err != nil {
			return err
		}
		if err := syncDiagnosticsDirectory(parent); err != nil {
			return err
		}
		info, err = os.Lstat(store.directory)
	}
	if err != nil || !info.IsDir() {
		return errDiagnosticsCredentialStateInvalid
	}
	return checkDiagnosticsPrivateDirectory(store.directory, info)
}

func newDiagnosticsCredentialState(deviceIDDigest []byte, random io.Reader) (diagnosticsCredentialState, error) {
	seed, _, _, err := newDiagnosticsSigningIdentity(random)
	if err != nil {
		return diagnosticsCredentialState{}, err
	}
	tlsPrivate, _, _, err := newDiagnosticsTLSIdentity(random)
	if err != nil {
		return diagnosticsCredentialState{}, err
	}
	homeserverBinding, err := readDiagnosticsRandom(random, 32)
	if err != nil {
		return diagnosticsCredentialState{}, err
	}
	return diagnosticsCredentialState{
		FormatVersion: diagnosticsCredentialStateFormat,
		Revision:      1,
		Identity: diagnosticsHelperCredentialIdentity{
			SigningSeed:       seed,
			TLSPrivatePKCS8:   tlsPrivate,
			HelperEpoch:       1,
			HomeserverBinding: homeserverBinding,
			DeviceIDDigest:    append([]byte(nil), deviceIDDigest...),
		},
	}, nil
}

func (store *diagnosticsCredentialStore) snapshot() (diagnosticsCredentialState, error) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	var result diagnosticsCredentialState
	err := store.withLock(func() error {
		state, err := store.loadUnlocked()
		if err != nil {
			return err
		}
		result = cloneDiagnosticsCredentialState(state)
		return nil
	})
	return result, err
}

func (store *diagnosticsCredentialStore) update(mutator func(*diagnosticsCredentialState) error) error {
	return store.updateIfChanged(func(state *diagnosticsCredentialState) (bool, error) {
		if err := mutator(state); err != nil {
			return false, err
		}
		return true, nil
	})
}

func (store *diagnosticsCredentialStore) updateIfChanged(mutator func(*diagnosticsCredentialState) (bool, error)) error {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	return store.withLock(func() error {
		state, err := store.loadUnlocked()
		if err != nil {
			return err
		}
		changed, err := mutator(&state)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		state.Revision++
		return store.saveUnlocked(state)
	})
}

func (store *diagnosticsCredentialStore) reserveFolderBinding(folderIDDigest []byte) ([]byte, error) {
	if len(folderIDDigest) != 32 {
		return nil, errDiagnosticsCredentialStateInvalid
	}
	var result []byte
	err := store.update(func(state *diagnosticsCredentialState) error {
		for _, folder := range state.Folders {
			if bytes.Equal(folder.FolderIDDigest, folderIDDigest) {
				result = append([]byte(nil), folder.FolderBinding...)
				return nil
			}
		}
		binding, err := readDiagnosticsRandom(store.random, 32)
		if err != nil {
			return err
		}
		state.Folders = append(state.Folders, diagnosticsFolderCredential{
			FolderIDDigest: append([]byte(nil), folderIDDigest...),
			FolderBinding:  binding,
		})
		sort.Slice(state.Folders, func(i, j int) bool {
			return bytes.Compare(state.Folders[i].FolderIDDigest, state.Folders[j].FolderIDDigest) < 0
		})
		result = append([]byte(nil), binding...)
		return nil
	})
	return result, err
}

func (store *diagnosticsCredentialStore) loadUnlocked() (diagnosticsCredentialState, error) {
	pathInfo, err := os.Lstat(store.statePath)
	if err != nil {
		return diagnosticsCredentialState{}, err
	}
	if !pathInfo.Mode().IsRegular() {
		return diagnosticsCredentialState{}, errDiagnosticsCredentialStateInvalid
	}
	file, err := os.Open(store.statePath)
	if err != nil {
		return diagnosticsCredentialState{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	currentPathInfo, pathErr := os.Lstat(store.statePath)
	if err != nil || pathErr != nil || !os.SameFile(pathInfo, info) || !os.SameFile(currentPathInfo, info) ||
		!info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > diagnosticsCredentialMaxBytes {
		return diagnosticsCredentialState{}, errDiagnosticsCredentialStateInvalid
	}
	if err := checkDiagnosticsPrivateFile(store.statePath, info); err != nil {
		return diagnosticsCredentialState{}, err
	}
	body, err := io.ReadAll(io.LimitReader(file, diagnosticsCredentialMaxBytes+1))
	if err != nil || len(body) > diagnosticsCredentialMaxBytes {
		return diagnosticsCredentialState{}, errDiagnosticsCredentialStateInvalid
	}
	var state diagnosticsCredentialState
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return diagnosticsCredentialState{}, errDiagnosticsCredentialStateInvalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return diagnosticsCredentialState{}, errDiagnosticsCredentialStateInvalid
	}
	if state.FormatVersion > diagnosticsCredentialStateFormat {
		return diagnosticsCredentialState{}, errDiagnosticsCredentialStateNewer
	}
	if err := validateDiagnosticsCredentialState(state); err != nil {
		return diagnosticsCredentialState{}, err
	}
	return state, nil
}

func (store *diagnosticsCredentialStore) saveUnlocked(state diagnosticsCredentialState) error {
	if err := validateDiagnosticsCredentialState(state); err != nil {
		return err
	}
	body, err := json.Marshal(state)
	if err != nil || len(body) > diagnosticsCredentialMaxBytes {
		return errDiagnosticsCredentialStateInvalid
	}
	temporary, err := os.CreateTemp(store.directory, ".credentials-v1-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(body); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if store.hooks.beforeRename != nil {
		if err := store.hooks.beforeRename(); err != nil {
			return err
		}
	}
	if err := os.Rename(temporaryPath, store.statePath); err != nil {
		return err
	}
	removeTemporary = false
	if err := syncDiagnosticsDirectory(store.directory); err != nil {
		return err
	}
	if store.hooks.afterRename != nil {
		if err := store.hooks.afterRename(); err != nil {
			return err
		}
	}
	return nil
}

func validateDiagnosticsCredentialState(state diagnosticsCredentialState) error {
	if state.FormatVersion != diagnosticsCredentialStateFormat || state.Revision == 0 ||
		len(state.Identity.SigningSeed) != ed25519.SeedSize || len(state.Identity.HomeserverBinding) != 32 ||
		len(state.Identity.DeviceIDDigest) != 32 || state.Identity.HelperEpoch == 0 {
		return errDiagnosticsCredentialStateInvalid
	}
	if _, err := diagnosticsSigningPrivateKey(state.Identity.SigningSeed); err != nil {
		return errDiagnosticsCredentialStateInvalid
	}
	tlsPrivate, err := x509.ParsePKCS8PrivateKey(state.Identity.TLSPrivatePKCS8)
	if err != nil {
		return errDiagnosticsCredentialStateInvalid
	}
	tlsKey, ok := tlsPrivate.(*ecdsa.PrivateKey)
	if !ok || tlsKey.Curve != elliptic.P256() {
		return errDiagnosticsCredentialStateInvalid
	}
	seenFolders := make(map[string]struct{}, len(state.Folders))
	seenFolderBindings := make(map[string]struct{}, len(state.Folders))
	for _, folder := range state.Folders {
		if len(folder.FolderIDDigest) != 32 || len(folder.FolderBinding) != 32 {
			return errDiagnosticsCredentialStateInvalid
		}
		key := base64.RawURLEncoding.EncodeToString(folder.FolderIDDigest)
		if _, exists := seenFolders[key]; exists {
			return errDiagnosticsCredentialStateInvalid
		}
		seenFolders[key] = struct{}{}
		bindingKey := base64.RawURLEncoding.EncodeToString(folder.FolderBinding)
		if _, exists := seenFolderBindings[bindingKey]; exists {
			return errDiagnosticsCredentialStateInvalid
		}
		seenFolderBindings[bindingKey] = struct{}{}
	}
	installationCount := make(map[string]int)
	seenAuthorizations := make(map[string]struct{}, len(state.Authorizations))
	for _, authorization := range state.Authorizations {
		if err := validateDiagnosticsAuthorization(authorization); err != nil {
			return err
		}
		if !bytes.Equal(authorization.HomeserverBinding, state.Identity.HomeserverBinding) {
			return errDiagnosticsCredentialStateInvalid
		}
		folderBindingKey := base64.RawURLEncoding.EncodeToString(authorization.FolderBinding)
		if _, exists := seenFolderBindings[folderBindingKey]; !exists {
			return errDiagnosticsCredentialStateInvalid
		}
		if authorization.State != "revoked" && authorization.State != "inactive" &&
			authorization.HelperEpoch != state.Identity.HelperEpoch {
			return errDiagnosticsCredentialStateInvalid
		}
		if _, exists := seenAuthorizations[authorization.RecordID]; exists {
			return errDiagnosticsCredentialStateInvalid
		}
		seenAuthorizations[authorization.RecordID] = struct{}{}
		folder := base64.RawURLEncoding.EncodeToString(authorization.FolderBinding)
		installationCount[folder]++
		if installationCount[folder] > 8 {
			return errDiagnosticsCredentialStateInvalid
		}
	}
	for _, revocation := range state.Revocations {
		if len(revocation.AppKeyID) != 32 || len(revocation.FolderBinding) != 32 ||
			revocation.AuthorizationEpoch == 0 || revocation.Reason < 1 || revocation.Reason > 4 || revocation.RetainUntil <= 0 {
			return errDiagnosticsCredentialStateInvalid
		}
	}
	if len(state.RateEvents) > 120 {
		return errDiagnosticsCredentialStateInvalid
	}
	return nil
}

func validateDiagnosticsAuthorization(authorization diagnosticsPairingAuthorization) error {
	if authorization.RecordID == "" || len(authorization.HomeserverBinding) != 32 ||
		len(authorization.FolderBinding) != 32 || len(authorization.AppPublicKey) != ed25519.PublicKeySize ||
		len(authorization.AppKeyID) != 32 || authorization.AppEpoch == 0 || authorization.HelperEpoch == 0 ||
		len(authorization.TLSSPKIPin) != 32 || len(authorization.CurrentStateDigest) != 32 || authorization.ExpiresAt <= 0 {
		return errDiagnosticsCredentialStateInvalid
	}
	derivedKeyID := diagnosticsKeyID(authorization.AppPublicKey)
	if !bytes.Equal(derivedKeyID[:], authorization.AppKeyID) {
		return errDiagnosticsCredentialStateInvalid
	}
	if authorization.RecordID != diagnosticsAuthorizationRecordID(authorization.AppKeyID, authorization.FolderBinding) {
		return errDiagnosticsCredentialStateInvalid
	}
	if (len(authorization.NamespaceInitialAppKeyID) == 0) != (authorization.NamespaceAuthorizationEpoch == 0) {
		return errDiagnosticsCredentialStateInvalid
	}
	if len(authorization.NamespaceInitialAppKeyID) != 0 &&
		(len(authorization.NamespaceInitialAppKeyID) != 32 ||
			authorization.NamespaceAuthorizationEpoch < 1 ||
			authorization.NamespaceAuthorizationEpoch > diagnosticsNamespaceMaximumAuthorizationEpochs+1 ||
			!nonzeroDiagnosticsBytes(authorization.NamespaceInitialAppKeyID)) {
		return errDiagnosticsCredentialStateInvalid
	}
	for _, field := range [][]byte{
		authorization.InvitationNonce, authorization.AppNonce, authorization.HelperNonce, authorization.AppRequestDigest,
	} {
		if len(field) != 32 {
			return errDiagnosticsCredentialStateInvalid
		}
	}
	switch authorization.State {
	case "pending", "finalize_pending", "awaiting_activation", "active", "revoked", "inactive":
	default:
		return errDiagnosticsCredentialStateInvalid
	}
	if len(authorization.Replays) > 32 {
		return errDiagnosticsCredentialStateInvalid
	}
	seenReplays := make(map[string]struct{}, len(authorization.Replays))
	for _, replay := range authorization.Replays {
		if len(replay.RequestDigest) != 32 || len(replay.Response) == 0 || len(replay.Response) > diagnosticsMaximumMessageBytes || replay.RetainUntil <= 0 {
			return errDiagnosticsCredentialStateInvalid
		}
		if _, err := decodeDiagnosticsPairingMessage(replay.Response); err != nil {
			return errDiagnosticsCredentialStateInvalid
		}
		replayKey := base64.RawURLEncoding.EncodeToString(replay.RequestDigest)
		if _, exists := seenReplays[replayKey]; exists {
			return errDiagnosticsCredentialStateInvalid
		}
		seenReplays[replayKey] = struct{}{}
	}
	if len(authorization.LifecycleNonces) > 256 {
		return errDiagnosticsCredentialStateInvalid
	}
	seenLifecycleNonces := make(map[string]struct{}, len(authorization.LifecycleNonces))
	for _, nonce := range authorization.LifecycleNonces {
		if len(nonce) != 32 {
			return errDiagnosticsCredentialStateInvalid
		}
		key := base64.RawURLEncoding.EncodeToString(nonce)
		if _, exists := seenLifecycleNonces[key]; exists {
			return errDiagnosticsCredentialStateInvalid
		}
		seenLifecycleNonces[key] = struct{}{}
	}
	if authorization.Transition != nil {
		return validateDiagnosticsTransition(authorization)
	}
	return nil
}

func validateDiagnosticsTransition(authorization diagnosticsPairingAuthorization) error {
	transition := authorization.Transition
	if authorization.State != "active" || transition == nil || transition.Kind < 1 || transition.Kind > 3 ||
		len(transition.TransitionDigest) != 32 || len(transition.LatestMessageDigest) != 32 || transition.ExpiresAt <= 0 {
		return errDiagnosticsCredentialStateInvalid
	}
	switch transition.Stage {
	case "request", "proof", "accepted", "committed":
	default:
		return errDiagnosticsCredentialStateInvalid
	}
	if transition.ProposedStateConfirmed && transition.Stage != "committed" {
		return errDiagnosticsCredentialStateInvalid
	}
	switch transition.Kind {
	case diagnosticsPairingTransitionAppKey:
		if len(transition.ProposedAppPublicKey) != ed25519.PublicKeySize || len(transition.ProposedAppKeyID) != 32 ||
			transition.ProposedAppEpoch != authorization.AppEpoch+1 || len(transition.ProposedHelperSeed) != 0 ||
			len(transition.ProposedHelperKeyID) != 0 || transition.ProposedHelperEpoch != 0 ||
			len(transition.ProposedTLSPrivate) != 0 || len(transition.ProposedTLSPin) != 0 {
			return errDiagnosticsCredentialStateInvalid
		}
		keyID := diagnosticsKeyID(transition.ProposedAppPublicKey)
		if !bytes.Equal(keyID[:], transition.ProposedAppKeyID) {
			return errDiagnosticsCredentialStateInvalid
		}
	case diagnosticsPairingTransitionHelperKey:
		if len(transition.ProposedHelperSeed) != ed25519.SeedSize || len(transition.ProposedHelperKeyID) != 32 ||
			transition.ProposedHelperEpoch != authorization.HelperEpoch+1 || len(transition.ProposedAppPublicKey) != 0 ||
			len(transition.ProposedAppKeyID) != 0 || transition.ProposedAppEpoch != 0 ||
			len(transition.ProposedTLSPrivate) != 0 || len(transition.ProposedTLSPin) != 0 {
			return errDiagnosticsCredentialStateInvalid
		}
		privateKey, err := diagnosticsSigningPrivateKey(transition.ProposedHelperSeed)
		if err != nil {
			return errDiagnosticsCredentialStateInvalid
		}
		keyID := diagnosticsKeyID(privateKey.Public().(ed25519.PublicKey))
		if !bytes.Equal(keyID[:], transition.ProposedHelperKeyID) {
			return errDiagnosticsCredentialStateInvalid
		}
	case diagnosticsPairingTransitionTLSPin:
		if len(transition.ProposedTLSPrivate) == 0 || len(transition.ProposedTLSPin) != 32 ||
			len(transition.ProposedAppPublicKey) != 0 || len(transition.ProposedAppKeyID) != 0 || transition.ProposedAppEpoch != 0 ||
			len(transition.ProposedHelperSeed) != 0 || len(transition.ProposedHelperKeyID) != 0 || transition.ProposedHelperEpoch != 0 {
			return errDiagnosticsCredentialStateInvalid
		}
		pin, err := diagnosticsTLSPrivateKeyPin(transition.ProposedTLSPrivate)
		if err != nil || !bytes.Equal(pin, transition.ProposedTLSPin) {
			return errDiagnosticsCredentialStateInvalid
		}
	}
	return nil
}

func (store *diagnosticsCredentialStore) withLock(action func() error) error {
	return withDiagnosticsCredentialFileLock(filepath.Join(store.directory, diagnosticsCredentialLockFile), action)
}

func syncDiagnosticsDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func readDiagnosticsRandom(random io.Reader, length int) ([]byte, error) {
	if random == nil || length <= 0 {
		return nil, errDiagnosticsCredentialStateInvalid
	}
	result := make([]byte, length)
	if _, err := io.ReadFull(random, result); err != nil {
		return nil, err
	}
	return result, nil
}

func diagnosticsAuthorizationRecordID(appKeyID, folderBinding []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("eu.vaultsync.helper-pairing/v1/authorization-record\x00"))
	_, _ = hash.Write(appKeyID)
	_, _ = hash.Write(folderBinding)
	return base64.RawURLEncoding.EncodeToString(hash.Sum(nil))
}

func cloneDiagnosticsCredentialState(state diagnosticsCredentialState) diagnosticsCredentialState {
	body, err := json.Marshal(state)
	if err != nil {
		panic(fmt.Sprintf("clone diagnostics credential state: %v", err))
	}
	var clone diagnosticsCredentialState
	if err := json.Unmarshal(body, &clone); err != nil {
		panic(fmt.Sprintf("clone diagnostics credential state: %v", err))
	}
	return clone
}
