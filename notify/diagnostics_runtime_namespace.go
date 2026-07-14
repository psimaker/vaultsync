package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"sync"
	"time"
)

const (
	diagnosticsNamespaceControlRequestsMinute = 30
	diagnosticsNamespaceEnablementStartsDay   = 3
	diagnosticsNamespacePendingAttempts       = 10
)

type diagnosticsNamespacePendingEnablement struct {
	recordID string
	folderID string
	body     []byte
	digest   [32]byte
	deadline time.Time
	attempts int
}

type diagnosticsNamespaceRuntime struct {
	config          *diagnosticsRuntimeConfig
	credentialStore *diagnosticsCredentialStore
	namespaceStore  *diagnosticsNamespaceStateStore
	sessions        *diagnosticsRuntimeSessions
	now             func() time.Time
	random          io.Reader
	mutex           sync.Mutex
	pending         map[string]*diagnosticsNamespacePendingEnablement
	requests        []time.Time
	startsByFolder  map[[32]byte][]time.Time
	preflight       func(context.Context, []byte, *diagnosticsNamespaceRootHandle) error
}

func newDiagnosticsNamespaceRuntime(
	config *diagnosticsRuntimeConfig,
	credentialStore *diagnosticsCredentialStore,
	namespaceStore *diagnosticsNamespaceStateStore,
	sessions *diagnosticsRuntimeSessions,
) *diagnosticsNamespaceRuntime {
	return &diagnosticsNamespaceRuntime{
		config: config, credentialStore: credentialStore, namespaceStore: namespaceStore, sessions: sessions,
		now: time.Now, random: rand.Reader, pending: make(map[string]*diagnosticsNamespacePendingEnablement),
		startsByFolder: make(map[[32]byte][]time.Time),
	}
}

func (runtime *diagnosticsNamespaceRuntime) acceptEnablement(body []byte) error {
	now := runtime.now()
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	runtime.expirePending(now)
	if !allowDiagnosticsUploadWindow(&runtime.requests, now, time.Minute, diagnosticsNamespaceControlRequestsMinute) {
		return errDiagnosticsPairingRateLimited
	}
	message, authorization, _, folderID, err := runtime.validateEnablement(body, now)
	if err != nil {
		return err
	}
	digest, err := diagnosticsNamespaceRecordDigest(body)
	if err != nil {
		return errDiagnosticsNamespaceInvalid
	}
	if existing := runtime.pending[authorization.RecordID]; existing != nil {
		existing.attempts++
		if existing.attempts > diagnosticsNamespacePendingAttempts {
			delete(runtime.pending, authorization.RecordID)
			return errDiagnosticsPairingRateLimited
		}
		if bytes.Equal(existing.body, body) {
			return nil
		}
		return errDiagnosticsNamespaceConflict
	}
	folderBinding, _ := message.bytesField(6, 32)
	var folderKey [32]byte
	copy(folderKey[:], folderBinding)
	starts := pruneDiagnosticsUploadWindow(runtime.startsByFolder[folderKey], now, 24*time.Hour)
	if len(starts) >= diagnosticsNamespaceEnablementStartsDay {
		runtime.startsByFolder[folderKey] = starts
		return errDiagnosticsPairingRateLimited
	}
	for _, pending := range runtime.pending {
		pendingMessage, _ := decodeDiagnosticsNamespaceMessage(pending.body)
		pendingFolder, _ := pendingMessage.bytesField(6, 32)
		if bytes.Equal(pendingFolder, folderBinding) {
			return errDiagnosticsNamespaceConflict
		}
	}
	expiresAt, _ := message.uintField(27)
	deadline := now.Add(diagnosticsPairingLifetime)
	signedDeadline := time.Unix(int64(expiresAt), 0)
	if signedDeadline.Before(deadline) {
		deadline = signedDeadline
	}
	runtime.startsByFolder[folderKey] = append(starts, now)
	runtime.pending[authorization.RecordID] = &diagnosticsNamespacePendingEnablement{
		recordID: authorization.RecordID, folderID: folderID, body: append([]byte(nil), body...),
		digest: digest, deadline: deadline, attempts: 1,
	}
	return nil
}

func (runtime *diagnosticsNamespaceRuntime) validateEnablement(
	body []byte,
	now time.Time,
) (diagnosticsNamespaceMessage, diagnosticsPairingAuthorization, diagnosticsHelperCredentialIdentity, string, error) {
	message, err := decodeDiagnosticsNamespaceMessage(body)
	if err != nil || message.messageType != diagnosticsNamespaceEnablement || now.Unix() < 0 {
		return diagnosticsNamespaceMessage{}, diagnosticsPairingAuthorization{}, diagnosticsHelperCredentialIdentity{}, "", errDiagnosticsNamespaceInvalid
	}
	issuedAt, _ := message.uintField(26)
	expiresAt, _ := message.uintField(27)
	nowSeconds := uint64(now.Unix())
	if issuedAt > nowSeconds+diagnosticsPairingClockSkewSeconds() || nowSeconds >= expiresAt {
		return diagnosticsNamespaceMessage{}, diagnosticsPairingAuthorization{}, diagnosticsHelperCredentialIdentity{}, "", errDiagnosticsNamespaceInvalid
	}
	appKeyID, _ := message.bytesField(11, 32)
	folderBinding, _ := message.bytesField(6, 32)
	authorization, identity, err := runtime.sessions.activeAuthorization(appKeyID, folderBinding)
	if err != nil || len(authorization.NamespaceInitialAppKeyID) != 0 {
		return diagnosticsNamespaceMessage{}, diagnosticsPairingAuthorization{}, diagnosticsHelperCredentialIdentity{}, "", errDiagnosticsPairingUnavailable
	}
	helperPrivate, err := diagnosticsSigningPrivateKey(identity.SigningSeed)
	if err != nil {
		return diagnosticsNamespaceMessage{}, diagnosticsPairingAuthorization{}, diagnosticsHelperCredentialIdentity{}, "", errDiagnosticsPairingUnavailable
	}
	helperPublic := helperPrivate.Public().(ed25519.PublicKey)
	helperKeyID := diagnosticsKeyID(helperPublic)
	homeserver, _ := message.bytesField(5, 32)
	initialAppKeyID, _ := message.bytesField(9, 32)
	appPublic, _ := message.bytesField(10, 32)
	appEpoch, _ := message.uintField(12)
	messageHelperPublic, _ := message.bytesField(13, 32)
	messageHelperKeyID, _ := message.bytesField(14, 32)
	helperEpoch, _ := message.uintField(15)
	if !bytes.Equal(homeserver, authorization.HomeserverBinding) ||
		!bytes.Equal(initialAppKeyID, authorization.AppKeyID) || !bytes.Equal(appPublic, authorization.AppPublicKey) ||
		appEpoch != authorization.AppEpoch || !bytes.Equal(messageHelperPublic, helperPublic) ||
		!bytes.Equal(messageHelperKeyID, helperKeyID[:]) || helperEpoch != authorization.HelperEpoch {
		return diagnosticsNamespaceMessage{}, diagnosticsPairingAuthorization{}, diagnosticsHelperCredentialIdentity{}, "", errDiagnosticsNamespaceInvalid
	}
	folderID, err := runtime.folderIDForBinding(folderBinding)
	if err != nil {
		return diagnosticsNamespaceMessage{}, diagnosticsPairingAuthorization{}, diagnosticsHelperCredentialIdentity{}, "", err
	}
	return message, authorization, identity, folderID, nil
}

func diagnosticsPairingClockSkewSeconds() uint64 {
	return uint64(diagnosticsPairingClockSkew / time.Second)
}

func (runtime *diagnosticsNamespaceRuntime) folderIDForBinding(folderBinding []byte) (string, error) {
	state, err := runtime.credentialStore.snapshot()
	if err != nil {
		return "", errDiagnosticsPairingUnavailable
	}
	for _, configured := range runtime.config.Folders {
		digest, digestErr := diagnosticsFolderIDDigest(configured.FolderID)
		if digestErr != nil {
			continue
		}
		for _, stored := range state.Folders {
			if bytes.Equal(stored.FolderIDDigest, digest[:]) && bytes.Equal(stored.FolderBinding, folderBinding) {
				return configured.FolderID, nil
			}
		}
	}
	return "", errDiagnosticsNamespaceUnsupported
}

func (runtime *diagnosticsNamespaceRuntime) pendingForFolder(folderID string) ([]byte, [32]byte, error) {
	now := runtime.now()
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	runtime.expirePending(now)
	for _, pending := range runtime.pending {
		if pending.folderID == folderID {
			return append([]byte(nil), pending.body...), pending.digest, nil
		}
	}
	return nil, [32]byte{}, errDiagnosticsPairingUnavailable
}

func (runtime *diagnosticsNamespaceRuntime) expirePending(now time.Time) {
	for recordID, pending := range runtime.pending {
		if !now.Before(pending.deadline) {
			delete(runtime.pending, recordID)
		}
	}
}

func (runtime *diagnosticsNamespaceRuntime) authorize(ctx context.Context, body []byte) error {
	now := runtime.now()
	runtime.mutex.Lock()
	defer runtime.mutex.Unlock()
	if !allowDiagnosticsUploadWindow(&runtime.requests, now, time.Minute, diagnosticsNamespaceControlRequestsMinute) {
		return errDiagnosticsPairingRateLimited
	}
	value, err := decodeDiagnosticsCBOR(body)
	if err != nil || value.kind != diagnosticsCBORMap {
		return errDiagnosticsNamespaceInvalid
	}
	messageType, ok := diagnosticsNamespaceMessageType(value)
	if !ok || (messageType != diagnosticsNamespaceInitialAuthorization && messageType != diagnosticsNamespaceAuthorizationEpoch) ||
		validateDiagnosticsNamespaceValue(value, messageType, []uint64{255}) != nil ||
		verifyDiagnosticsNamespaceAppSignature(value, messageType) != nil || now.Unix() < 0 {
		return errDiagnosticsNamespaceInvalid
	}
	issuedAt, _ := diagnosticsNamespaceUintField(value, 26)
	expiresAt, _ := diagnosticsNamespaceUintField(value, 27)
	nowSeconds := uint64(now.Unix())
	appKeyID, _ := diagnosticsNamespaceBytesField(value, 11, 32)
	folderBinding, _ := diagnosticsNamespaceBytesField(value, 6, 32)
	authorization, identity, err := runtime.sessions.activeAuthorization(appKeyID, folderBinding)
	if err != nil {
		return err
	}
	rootRecord, folderConfig, err := runtime.sessions.rootAndFolder(folderBinding)
	if err != nil || folderConfig.MountAlias == "" {
		return errDiagnosticsNamespaceUnsupported
	}
	mountPath, _ := runtime.config.mountPath(folderConfig.MountAlias)
	handle, err := openDiagnosticsNamespaceRoot(mountPath, nil)
	if err != nil {
		return errDiagnosticsNamespaceUnsupported
	}
	defer handle.Close()
	scanErr := handle.ScanFixedLayout()
	if scanErr != nil && messageType == diagnosticsNamespaceAuthorizationEpoch {
		scanErr = handle.ScanFixedLayoutDuringHelperRotation()
	}
	if handle.ValidateRootRecord(rootRecord) != nil || scanErr != nil {
		return errDiagnosticsNamespaceConflict
	}
	if runtime.preflight == nil || runtime.preflight(ctx, folderBinding, handle) != nil {
		return errDiagnosticsNamespaceUnsupported
	}
	rootBody, _, err := handle.ReadImmutable(diagnosticsNamespaceRootManifestPath())
	if err != nil {
		return errDiagnosticsNamespaceConflict
	}
	rootMessage, _ := decodeDiagnosticsNamespaceMessage(rootBody)
	rootDigest, _ := diagnosticsNamespaceRecordDigest(rootBody)
	currentManifestDigest := rootDigest
	if authorization.HelperEpoch > 1 {
		manifestPath, pathErr := diagnosticsNamespaceHelperEpochPath(authorization.HelperEpoch)
		if pathErr != nil {
			return errDiagnosticsNamespaceConflict
		}
		manifestBody, _, readErr := handle.ReadImmutable(manifestPath)
		if readErr != nil {
			return errDiagnosticsNamespaceConflict
		}
		currentManifestDigest, _ = diagnosticsNamespaceRecordDigest(manifestBody)
	}
	helperPrivate, err := diagnosticsSigningPrivateKey(identity.SigningSeed)
	if err != nil {
		return errDiagnosticsPairingUnavailable
	}
	complete, err := countersignDiagnosticsNamespaceAuthorization(body, helperPrivate)
	if err != nil {
		return err
	}
	if runtime.authorizationAlreadyApplied(
		handle, value, complete, authorization, identity, rootMessage, rootDigest, currentManifestDigest,
	) {
		// The immutable record and protected authorization epoch were already
		// committed while the signed candidate was valid. A lost HTTP response
		// must remain recoverable by the byte-identical request after restart or
		// wall-clock expiry; this branch can neither create nor advance state.
		return nil
	}
	if issuedAt > nowSeconds+diagnosticsPairingClockSkewSeconds() || nowSeconds >= expiresAt {
		return errDiagnosticsNamespaceInvalid
	}
	if !runtime.authorizationMatchesCurrent(value, authorization, identity, rootMessage, rootDigest, currentManifestDigest) {
		return errDiagnosticsNamespaceInvalid
	}
	initialAppKeyID, _ := diagnosticsNamespaceBytesField(value, 9, 32)
	authorizationEpoch, _ := diagnosticsNamespaceUintField(value, 31)
	if err := installDiagnosticsRuntimeAuthorization(handle, complete, authorization, messageType); err != nil {
		return err
	}
	return runtime.sessions.markNamespaceAuthorization(authorization.RecordID, initialAppKeyID, authorizationEpoch)
}

func (runtime *diagnosticsNamespaceRuntime) authorizationMatchesCurrent(
	value diagnosticsCBORValue,
	authorization diagnosticsPairingAuthorization,
	identity diagnosticsHelperCredentialIdentity,
	root diagnosticsNamespaceMessage,
	rootDigest, currentManifestDigest [32]byte,
) bool {
	if !diagnosticsRuntimeAuthorizationBindingsMatch(value, authorization, identity, root, rootDigest, currentManifestDigest) {
		return false
	}
	messageType, _ := diagnosticsNamespaceMessageType(value)
	initialAppKeyID, _ := diagnosticsNamespaceBytesField(value, 9, 32)
	installation, _ := diagnosticsNamespaceBytesField(value, 8, 32)
	authorizationEpoch, _ := diagnosticsNamespaceUintField(value, 31)
	if messageType == diagnosticsNamespaceInitialAuthorization {
		return len(authorization.NamespaceInitialAppKeyID) == 0 && authorizationEpoch == 1 &&
			bytes.Equal(initialAppKeyID, authorization.AppKeyID)
	}
	if len(authorization.NamespaceInitialAppKeyID) != 32 ||
		!bytes.Equal(initialAppKeyID, authorization.NamespaceInitialAppKeyID) ||
		authorizationEpoch != authorization.NamespaceAuthorizationEpoch+1 {
		return false
	}
	wantInstallation := diagnosticsRuntimeInstallationBinding(authorization)
	return bytes.Equal(installation, wantInstallation[:])
}

func diagnosticsRuntimeAuthorizationBindingsMatch(
	value diagnosticsCBORValue,
	authorization diagnosticsPairingAuthorization,
	identity diagnosticsHelperCredentialIdentity,
	root diagnosticsNamespaceMessage,
	rootDigest, currentManifestDigest [32]byte,
) bool {
	homeserver, _ := diagnosticsNamespaceBytesField(value, 5, 32)
	folder, _ := diagnosticsNamespaceBytesField(value, 6, 32)
	namespaceID, _ := diagnosticsNamespaceBytesField(value, 7, 32)
	appPublic, _ := diagnosticsNamespaceBytesField(value, 10, 32)
	appKeyID, _ := diagnosticsNamespaceBytesField(value, 11, 32)
	appEpoch, _ := diagnosticsNamespaceUintField(value, 12)
	helperPublic, _ := diagnosticsNamespaceBytesField(value, 13, 32)
	helperKeyID, _ := diagnosticsNamespaceBytesField(value, 14, 32)
	helperEpoch, _ := diagnosticsNamespaceUintField(value, 15)
	boundRoot, _ := diagnosticsNamespaceBytesField(value, 21, 32)
	boundManifest, _ := diagnosticsNamespaceBytesField(value, 23, 32)
	credentialDigest, _ := diagnosticsNamespaceBytesField(value, 25, 32)
	rootNamespaceID, _ := root.bytesField(7, 32)
	currentHelperPrivate, err := diagnosticsSigningPrivateKey(identity.SigningSeed)
	if err != nil {
		return false
	}
	currentHelperPublic := currentHelperPrivate.Public().(ed25519.PublicKey)
	currentHelperKeyID := diagnosticsKeyID(currentHelperPublic)
	if !bytes.Equal(homeserver, authorization.HomeserverBinding) || !bytes.Equal(folder, authorization.FolderBinding) ||
		!bytes.Equal(namespaceID, rootNamespaceID) || !bytes.Equal(appPublic, authorization.AppPublicKey) ||
		!bytes.Equal(appKeyID, authorization.AppKeyID) || appEpoch != authorization.AppEpoch ||
		!bytes.Equal(helperPublic, currentHelperPublic) || !bytes.Equal(helperKeyID, currentHelperKeyID[:]) ||
		helperEpoch != authorization.HelperEpoch || !bytes.Equal(boundRoot, rootDigest[:]) ||
		!bytes.Equal(boundManifest, currentManifestDigest[:]) || !bytes.Equal(credentialDigest, authorization.CurrentStateDigest) {
		return false
	}
	return true
}

func (runtime *diagnosticsNamespaceRuntime) authorizationAlreadyApplied(
	handle *diagnosticsNamespaceRootHandle,
	value diagnosticsCBORValue,
	complete []byte,
	authorization diagnosticsPairingAuthorization,
	identity diagnosticsHelperCredentialIdentity,
	root diagnosticsNamespaceMessage,
	rootDigest, currentManifestDigest [32]byte,
) bool {
	if handle == nil || !diagnosticsRuntimeAuthorizationBindingsMatch(
		value, authorization, identity, root, rootDigest, currentManifestDigest,
	) {
		return false
	}
	initialAppKeyID, _ := diagnosticsNamespaceBytesField(value, 9, 32)
	installation, _ := diagnosticsNamespaceBytesField(value, 8, 32)
	authorizationEpoch, _ := diagnosticsNamespaceUintField(value, 31)
	if len(authorization.NamespaceInitialAppKeyID) != 32 ||
		!bytes.Equal(initialAppKeyID, authorization.NamespaceInitialAppKeyID) ||
		authorizationEpoch != authorization.NamespaceAuthorizationEpoch {
		return false
	}
	wantInstallation := diagnosticsRuntimeInstallationBinding(authorization)
	if !bytes.Equal(installation, wantInstallation[:]) {
		return false
	}
	path := diagnosticsRuntimeAuthorizationPath(installation, authorizationEpoch)
	existing, _, err := handle.ReadImmutable(path)
	return err == nil && bytes.Equal(existing, complete)
}

func diagnosticsRuntimeAuthorizationPath(installationBinding []byte, epoch uint64) diagnosticsNamespacePath {
	paths, _ := diagnosticsNamespaceAuthorizationPaths(installationBinding)
	if epoch <= 1 {
		return paths[0]
	}
	path, _ := diagnosticsNamespaceAuthorizationEpochPath(installationBinding, epoch)
	return path
}

func installDiagnosticsRuntimeAuthorization(
	handle *diagnosticsNamespaceRootHandle,
	complete []byte,
	authorization diagnosticsPairingAuthorization,
	messageType uint64,
) error {
	message, err := decodeDiagnosticsNamespaceMessage(complete)
	if err != nil || message.messageType != messageType {
		return errDiagnosticsNamespaceInvalid
	}
	initialAppKeyID, _ := message.bytesField(9, 32)
	installationBinding, _ := message.bytesField(8, 32)
	authorizationEpoch, _ := message.uintField(31)
	paths, err := diagnosticsNamespaceAuthorizationPaths(installationBinding)
	if err != nil {
		return err
	}
	path := paths[0]
	if messageType == diagnosticsNamespaceAuthorizationEpoch {
		path, err = diagnosticsNamespaceAuthorizationEpochPath(installationBinding, authorizationEpoch)
		if err != nil {
			return err
		}
		wantInstallation := diagnosticsRuntimeInstallationBinding(authorization)
		if !bytes.Equal(initialAppKeyID, authorization.NamespaceInitialAppKeyID) ||
			!bytes.Equal(installationBinding, wantInstallation[:]) || authorizationEpoch != authorization.NamespaceAuthorizationEpoch+1 {
			return errDiagnosticsNamespaceInvalid
		}
		priorPath := diagnosticsRuntimeAuthorizationPath(installationBinding, authorization.NamespaceAuthorizationEpoch)
		priorBody, _, readErr := handle.ReadImmutable(priorPath)
		if readErr != nil {
			return errDiagnosticsNamespaceConflict
		}
		priorDigest, digestErr := diagnosticsNamespaceRecordDigest(priorBody)
		boundPrior, _ := message.bytesField(24, 32)
		if digestErr != nil || !bytes.Equal(boundPrior, priorDigest[:]) {
			return errDiagnosticsNamespaceInvalid
		}
	}
	if existing, _, readErr := handle.ReadImmutable(path); readErr == nil {
		if bytes.Equal(existing, complete) {
			return nil
		}
		return errDiagnosticsNamespaceConflict
	} else if !errors.Is(readErr, fs.ErrNotExist) {
		return errDiagnosticsNamespaceConflict
	}
	if messageType == diagnosticsNamespaceInitialAuthorization {
		count, countErr := handle.InstallationCount()
		if countErr != nil || count >= diagnosticsNamespaceMaximumInstallations {
			return errDiagnosticsNamespaceLimit
		}
		installationDirectory := diagnosticsNamespacePath{components: paths[0].components[:2], persistent: true}
		for _, directory := range []diagnosticsNamespacePath{installationDirectory, paths[1], paths[2]} {
			if err := handle.CreateDirectory(directory); err != nil {
				return err
			}
		}
	}
	if _, err := handle.CreateImmutable(path, complete); err != nil {
		return err
	}
	if messageType == diagnosticsNamespaceAuthorizationEpoch {
		if err := handle.ScanFixedLayout(); err == nil {
			return nil
		}
		return handle.ScanFixedLayoutDuringHelperRotation()
	}
	return handle.ScanFixedLayout()
}

func prepareDiagnosticsNamespaceForOperator(
	ctx context.Context,
	config *diagnosticsRuntimeConfig,
	credentialStore *diagnosticsCredentialStore,
	namespaceStore *diagnosticsNamespaceStateStore,
	syncthing *SyncthingClient,
	folderID, sourcePath, mountedParent string,
	sourceDevice, sourceInode uint64,
	enablement []byte,
	operatorConfirmed bool,
) (diagnosticsNamespaceRootRecord, error) {
	if !operatorConfirmed || config == nil || credentialStore == nil || namespaceStore == nil || syncthing == nil ||
		sourceDevice == 0 || sourceInode == 0 ||
		!filepath.IsAbs(sourcePath) || filepath.Clean(sourcePath) != sourcePath ||
		!filepath.IsAbs(mountedParent) || filepath.Clean(mountedParent) != mountedParent {
		return diagnosticsNamespaceRootRecord{}, errDiagnosticsNamespaceUnsupported
	}
	configured, allowed := config.folder(folderID)
	if !allowed || configured.MountAlias != "" {
		return diagnosticsNamespaceRootRecord{}, errDiagnosticsNamespaceUnsupported
	}
	credentialState, err := credentialStore.snapshot()
	if err != nil {
		return diagnosticsNamespaceRootRecord{}, errDiagnosticsNamespaceUnsupported
	}
	deviceDigest, err := diagnosticsSyncthingDeviceIDDigest(ctx, syncthing)
	if err != nil || !bytes.Equal(deviceDigest[:], credentialState.Identity.DeviceIDDigest) {
		return diagnosticsNamespaceRootRecord{}, errDiagnosticsNamespaceUnsupported
	}
	folder, err := syncthing.Folder(ctx, folderID)
	if err != nil || folder.ID != folderID || folder.Path != sourcePath || folder.Paused || folder.Type != "sendreceive" {
		return diagnosticsNamespaceRootRecord{}, errDiagnosticsNamespaceUnsupported
	}
	ignores, err := syncthing.FolderIgnores(ctx, folderID)
	if err != nil || ignores.Error != "" {
		return diagnosticsNamespaceRootRecord{}, errDiagnosticsNamespaceUnsupported
	}
	verdict := diagnosticsNamespaceIgnoreVerdictFromExpanded(ignores.Expanded)
	if !verdict.valid() {
		return diagnosticsNamespaceRootRecord{}, errDiagnosticsNamespaceUnsupported
	}
	confirmedDeviceDigest, err := diagnosticsSyncthingDeviceIDDigest(ctx, syncthing)
	if err != nil || !bytes.Equal(confirmedDeviceDigest[:], deviceDigest[:]) ||
		!bytes.Equal(confirmedDeviceDigest[:], credentialState.Identity.DeviceIDDigest) {
		return diagnosticsNamespaceRootRecord{}, errDiagnosticsNamespaceUnsupported
	}
	if len(enablement) == 0 {
		folderDigest, digestErr := diagnosticsFolderIDDigest(folderID)
		if digestErr != nil {
			return diagnosticsNamespaceRootRecord{}, errDiagnosticsNamespaceUnsupported
		}
		var folderBinding []byte
		for _, stored := range credentialState.Folders {
			if bytes.Equal(stored.FolderIDDigest, folderDigest[:]) {
				folderBinding = append([]byte(nil), stored.FolderBinding...)
				break
			}
		}
		active := false
		for _, authorization := range credentialState.Authorizations {
			if authorization.State == "active" && authorization.Transition == nil &&
				bytes.Equal(authorization.FolderBinding, folderBinding) {
				active = true
				break
			}
		}
		helperPrivate, helperErr := diagnosticsSigningPrivateKey(credentialState.Identity.SigningSeed)
		if !active || len(folderBinding) != 32 || helperErr != nil {
			return diagnosticsNamespaceRootRecord{}, errDiagnosticsNamespaceUnsupported
		}
		return prepareDiagnosticsNamespaceExplicit(diagnosticsNamespacePreparationRequest{
			parentPath: mountedParent, parentDevice: sourceDevice, parentInode: sourceInode,
			operatorConfirmed: true, recoveryOnly: true,
			homeserverBinding: credentialState.Identity.HomeserverBinding, folderBinding: folderBinding,
			helperPublicKey: helperPrivate.Public().(ed25519.PublicKey), helperEpoch: credentialState.Identity.HelperEpoch,
			ignore: verdict, stateStore: namespaceStore,
		})
	}

	sessions := newDiagnosticsRuntimeSessions(config, credentialStore, namespaceStore)
	defer sessions.close()
	namespaceRuntime := newDiagnosticsNamespaceRuntime(config, credentialStore, namespaceStore, sessions)
	message, _, identity, resolvedFolderID, err := namespaceRuntime.validateEnablement(enablement, time.Now())
	if err != nil || resolvedFolderID != folderID {
		return diagnosticsNamespaceRootRecord{}, errDiagnosticsNamespaceInvalid
	}
	rootManifest, err := buildDiagnosticsNamespaceRoot(message, identity, time.Now(), rand.Reader)
	if err != nil {
		return diagnosticsNamespaceRootRecord{}, err
	}
	homeserver, _ := message.bytesField(5, 32)
	folderBinding, _ := message.bytesField(6, 32)
	return prepareDiagnosticsNamespaceExplicit(diagnosticsNamespacePreparationRequest{
		parentPath: mountedParent, parentDevice: sourceDevice, parentInode: sourceInode, operatorConfirmed: true,
		homeserverBinding: homeserver, folderBinding: folderBinding,
		enablement: enablement, rootManifest: rootManifest, ignore: verdict, stateStore: namespaceStore,
	})
}

func buildDiagnosticsNamespaceRoot(
	enablement diagnosticsNamespaceMessage,
	identity diagnosticsHelperCredentialIdentity,
	now time.Time,
	random io.Reader,
) ([]byte, error) {
	if enablement.messageType != diagnosticsNamespaceEnablement || random == nil || now.Unix() < 0 {
		return nil, errDiagnosticsNamespaceInvalid
	}
	namespaceID, err := readDiagnosticsRandom(random, 32)
	if err != nil || !nonzeroDiagnosticsBytes(namespaceID) {
		return nil, errDiagnosticsNamespaceInvalid
	}
	helperPrivate, err := diagnosticsSigningPrivateKey(identity.SigningSeed)
	if err != nil {
		return nil, err
	}
	helperPublic := helperPrivate.Public().(ed25519.PublicKey)
	helperKeyID := diagnosticsKeyID(helperPublic)
	homeserver, _ := enablement.bytesField(5, 32)
	folder, _ := enablement.bytesField(6, 32)
	enablementNonce, _ := enablement.bytesField(19, 32)
	enablementDigest, _ := diagnosticsNamespaceRecordDigest(enablement.canonical)
	readmeDigest := sha256.Sum256([]byte(diagnosticsNamespaceReadme))
	body := diagnosticsCBORMapValue(
		diagnosticsCBORMapField(1, diagnosticsCBORTextValue(diagnosticsNamespaceCapabilityID)),
		diagnosticsCBORMapField(2, diagnosticsCBORUint(1)), diagnosticsCBORMapField(3, diagnosticsCBORUint(1)),
		diagnosticsCBORMapField(4, diagnosticsCBORUint(diagnosticsNamespaceRootManifest)),
		diagnosticsCBORMapField(5, diagnosticsCBORBstr(homeserver)), diagnosticsCBORMapField(6, diagnosticsCBORBstr(folder)),
		diagnosticsCBORMapField(7, diagnosticsCBORBstr(namespaceID)),
		diagnosticsCBORMapField(13, diagnosticsCBORBstr(helperPublic)), diagnosticsCBORMapField(14, diagnosticsCBORBstr(helperKeyID[:])),
		diagnosticsCBORMapField(15, diagnosticsCBORUint(identity.HelperEpoch)),
		diagnosticsCBORMapField(19, diagnosticsCBORBstr(enablementNonce)),
		diagnosticsCBORMapField(20, diagnosticsCBORBstr(enablementDigest[:])),
		diagnosticsCBORMapField(28, diagnosticsCBORUint(uint64(now.Unix()))),
		diagnosticsCBORMapField(29, diagnosticsCBORBstr(readmeDigest[:])),
	)
	return signDiagnosticsNamespaceRootManifest(body, helperPrivate)
}

type diagnosticsPreparedHelperManifest struct {
	handle   *diagnosticsNamespaceRootHandle
	path     diagnosticsNamespacePath
	body     []byte
	existing bool
}

func (runtime *diagnosticsRuntime) prepareLifecycleCommit(plan diagnosticsLifecycleCommitPlan) error {
	if plan.Kind != diagnosticsPairingTransitionHelperKey {
		return nil
	}
	return runtime.ensureHelperEpochManifests(plan)
}

// ensureHelperEpochManifests appends the exact dual-signed D023 manifest to
// every enabled namespace before the global helper key becomes current. A
// crash can leave a valid one-step mixed chain; retries accept only that exact
// append-only state and complete forward. Nothing is deleted or overwritten.
func (runtime *diagnosticsRuntime) ensureHelperEpochManifests(plan diagnosticsLifecycleCommitPlan) error {
	priorPrivate, err := diagnosticsSigningPrivateKey(plan.Identity.SigningSeed)
	if err != nil || plan.ProposedHelperEpoch != plan.Identity.HelperEpoch+1 ||
		plan.ProposedHelperEpoch-1 > diagnosticsNamespaceMaximumHelperEpochs {
		return errDiagnosticsNamespaceUnsupported
	}
	currentPrivate, err := diagnosticsSigningPrivateKey(plan.ProposedHelperSeed)
	if err != nil {
		return errDiagnosticsNamespaceUnsupported
	}
	currentKeyID := diagnosticsKeyID(currentPrivate.Public().(ed25519.PublicKey))
	if !bytes.Equal(currentKeyID[:], plan.ProposedHelperKeyID) {
		return errDiagnosticsNamespaceInvalid
	}

	enabledFolders := make(map[string]diagnosticsPairingAuthorization)
	for _, authorization := range plan.Authorizations {
		if len(authorization.NamespaceInitialAppKeyID) == 0 {
			continue
		}
		// The helper manifest belongs to the namespace, not to one app
		// authorization. Revoked/inactive immutable app records remain unchanged,
		// but must not permanently prevent the namespace's helper key from moving
		// forward for another explicitly authorized app.
		enabledFolders[string(authorization.FolderBinding)] = authorization
	}

	prepared := make([]diagnosticsPreparedHelperManifest, 0, len(enabledFolders))
	defer func() {
		for _, item := range prepared {
			_ = item.handle.Close()
		}
	}()
	preflightContext, cancelPreflight := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelPreflight()
	for _, authorization := range enabledFolders {
		rootRecord, folderConfig, err := runtime.sessions.rootAndFolder(authorization.FolderBinding)
		if err != nil {
			return err
		}
		mountPath, err := runtime.config.mountPath(folderConfig.MountAlias)
		if err != nil {
			return errDiagnosticsNamespaceUnsupported
		}
		handle, err := openDiagnosticsNamespaceRoot(mountPath, nil)
		if err != nil {
			return errDiagnosticsNamespaceUnsupported
		}
		item := diagnosticsPreparedHelperManifest{handle: handle}
		prepared = append(prepared, item)
		if handle.ValidateRootRecord(rootRecord) != nil {
			return errDiagnosticsNamespaceConflict
		}
		strictErr := handle.ScanFixedLayout()
		rotationErr := handle.ScanFixedLayoutDuringHelperRotation()
		if strictErr != nil && rotationErr != nil {
			return errDiagnosticsNamespaceConflict
		}
		// A helper-key manifest is a synchronized namespace mutation, so it
		// receives the same pinned Device ID, exact Syncthing folder/ignore, and
		// deployment-mount preflight as capability and operation traffic.
		if runtime.preflightNamespaceHandle(preflightContext, authorization.FolderBinding, handle) != nil {
			return errDiagnosticsNamespaceUnsupported
		}
		rootBody, _, err := handle.ReadImmutable(diagnosticsNamespaceRootManifestPath())
		if err != nil {
			return errDiagnosticsNamespaceConflict
		}
		rootMessage, err := decodeDiagnosticsNamespaceMessage(rootBody)
		if err != nil || rootMessage.messageType != diagnosticsNamespaceRootManifest {
			return errDiagnosticsNamespaceConflict
		}
		rootDigest, _ := diagnosticsNamespaceRecordDigest(rootBody)
		priorBody := rootBody
		if plan.Identity.HelperEpoch > 1 {
			priorPath, pathErr := diagnosticsNamespaceHelperEpochPath(plan.Identity.HelperEpoch)
			if pathErr != nil {
				return pathErr
			}
			priorBody, _, err = handle.ReadImmutable(priorPath)
			if err != nil {
				return errDiagnosticsNamespaceConflict
			}
		}
		priorMessage, err := decodeDiagnosticsNamespaceMessage(priorBody)
		priorPublic, _ := priorMessage.bytesField(13, 32)
		priorEpoch, _ := priorMessage.uintField(15)
		if err != nil || !bytes.Equal(priorPublic, priorPrivate.Public().(ed25519.PublicKey)) || priorEpoch != plan.Identity.HelperEpoch {
			return errDiagnosticsNamespaceConflict
		}
		priorDigest, _ := diagnosticsNamespaceRecordDigest(priorBody)
		path, err := diagnosticsNamespaceHelperEpochPath(plan.ProposedHelperEpoch)
		if err != nil {
			return err
		}
		prepared[len(prepared)-1].path = path
		if existing, _, readErr := handle.ReadImmutable(path); readErr == nil {
			if !diagnosticsRuntimeHelperManifestMatches(
				existing, rootMessage, rootDigest, priorDigest, priorPrivate.Public().(ed25519.PublicKey),
				plan.Identity.HelperEpoch, currentPrivate.Public().(ed25519.PublicKey), plan.ProposedHelperEpoch,
			) {
				return errDiagnosticsNamespaceConflict
			}
			prepared[len(prepared)-1].body = existing
			prepared[len(prepared)-1].existing = true
			continue
		} else if !errors.Is(readErr, fs.ErrNotExist) {
			return errDiagnosticsNamespaceConflict
		}
		manifest, err := buildDiagnosticsRuntimeHelperManifest(
			rootMessage, rootDigest, priorDigest, priorPrivate, currentPrivate,
			plan.Identity.HelperEpoch, plan.ProposedHelperEpoch, time.Now(),
		)
		if err != nil {
			return err
		}
		prepared[len(prepared)-1].body = manifest
	}

	for _, item := range prepared {
		if !item.existing {
			if _, err := item.handle.CreateImmutable(item.path, item.body); err != nil {
				return err
			}
		}
		if err := item.handle.ScanFixedLayoutDuringHelperRotation(); err != nil {
			return errDiagnosticsNamespaceConflict
		}
	}
	return nil
}

func buildDiagnosticsRuntimeHelperManifest(
	root diagnosticsNamespaceMessage,
	rootDigest, priorDigest [32]byte,
	priorPrivate, currentPrivate ed25519.PrivateKey,
	priorEpoch, currentEpoch uint64,
	now time.Time,
) ([]byte, error) {
	if root.messageType != diagnosticsNamespaceRootManifest || now.Unix() <= 0 || currentEpoch != priorEpoch+1 {
		return nil, errDiagnosticsNamespaceInvalid
	}
	homeserver, _ := root.bytesField(5, 32)
	folder, _ := root.bytesField(6, 32)
	namespaceID, _ := root.bytesField(7, 32)
	priorPublic := priorPrivate.Public().(ed25519.PublicKey)
	currentPublic := currentPrivate.Public().(ed25519.PublicKey)
	priorKeyID := diagnosticsKeyID(priorPublic)
	currentKeyID := diagnosticsKeyID(currentPublic)
	readmeDigest := sha256.Sum256([]byte(diagnosticsNamespaceReadme))
	value := diagnosticsCBORMapValue(
		diagnosticsCBORMapField(1, diagnosticsCBORTextValue(diagnosticsNamespaceCapabilityID)),
		diagnosticsCBORMapField(2, diagnosticsCBORUint(1)), diagnosticsCBORMapField(3, diagnosticsCBORUint(1)),
		diagnosticsCBORMapField(4, diagnosticsCBORUint(diagnosticsNamespaceHelperEpoch)),
		diagnosticsCBORMapField(5, diagnosticsCBORBstr(homeserver)), diagnosticsCBORMapField(6, diagnosticsCBORBstr(folder)),
		diagnosticsCBORMapField(7, diagnosticsCBORBstr(namespaceID)),
		diagnosticsCBORMapField(13, diagnosticsCBORBstr(currentPublic)), diagnosticsCBORMapField(14, diagnosticsCBORBstr(currentKeyID[:])),
		diagnosticsCBORMapField(15, diagnosticsCBORUint(currentEpoch)), diagnosticsCBORMapField(16, diagnosticsCBORBstr(priorPublic)),
		diagnosticsCBORMapField(17, diagnosticsCBORBstr(priorKeyID[:])), diagnosticsCBORMapField(18, diagnosticsCBORUint(priorEpoch)),
		diagnosticsCBORMapField(21, diagnosticsCBORBstr(rootDigest[:])), diagnosticsCBORMapField(22, diagnosticsCBORBstr(priorDigest[:])),
		diagnosticsCBORMapField(28, diagnosticsCBORUint(uint64(now.Unix()))), diagnosticsCBORMapField(29, diagnosticsCBORBstr(readmeDigest[:])),
	)
	return signDiagnosticsNamespaceHelperEpoch(value, priorPrivate, currentPrivate)
}

func diagnosticsRuntimeHelperManifestMatches(
	body []byte,
	root diagnosticsNamespaceMessage,
	rootDigest, priorDigest [32]byte,
	priorPublic ed25519.PublicKey,
	priorEpoch uint64,
	currentPublic ed25519.PublicKey,
	currentEpoch uint64,
) bool {
	message, err := decodeDiagnosticsNamespaceMessage(body)
	if err != nil || message.messageType != diagnosticsNamespaceHelperEpoch ||
		!diagnosticsNamespaceCommonBindingsEqual(root, message, 5, 6, 7) {
		return false
	}
	messagePrior, _ := message.bytesField(16, 32)
	messageCurrent, _ := message.bytesField(13, 32)
	messagePriorEpoch, _ := message.uintField(18)
	messageCurrentEpoch, _ := message.uintField(15)
	boundRoot, _ := message.bytesField(21, 32)
	boundPrior, _ := message.bytesField(22, 32)
	return bytes.Equal(messagePrior, priorPublic) && bytes.Equal(messageCurrent, currentPublic) &&
		messagePriorEpoch == priorEpoch && messageCurrentEpoch == currentEpoch &&
		bytes.Equal(boundRoot, rootDigest[:]) && bytes.Equal(boundPrior, priorDigest[:])
}
