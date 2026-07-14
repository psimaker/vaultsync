package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sync"
	"time"
)

const (
	diagnosticsNamespaceEnablementPath    = "/api/v1/diagnostics/namespace/enablement"
	diagnosticsNamespaceAuthorizationPath = "/api/v1/diagnostics/namespace/authorization"
	diagnosticsCapabilityPath             = "/api/v1/diagnostics/capability"
	diagnosticsAttestationPath            = "/api/v1/diagnostics/attestation"
	diagnosticsAuthorizeResponsePath      = "/api/v1/diagnostics/authorize-response"
	diagnosticsCleanupPath                = "/api/v1/diagnostics/cleanup"
	diagnosticsHTTPMaximumBodyBytes       = 16 * 1024
	diagnosticsRuntimeMutationLockFile    = ".runtime-mutation.lock"
)

var (
	errDiagnosticsOperatorUnavailable = errors.New("diagnostics operator socket unavailable")
	errDiagnosticsListenerUnavailable = errors.New("diagnostics TLS listener unavailable")
)

type diagnosticsRuntime struct {
	config                         *diagnosticsRuntimeConfig
	credentialStore                *diagnosticsCredentialStore
	namespaceStore                 *diagnosticsNamespaceStateStore
	pairing                        *diagnosticsPairingManager
	syncthing                      *SyncthingClient
	sessions                       *diagnosticsRuntimeSessions
	namespace                      *diagnosticsNamespaceRuntime
	tlsConfig                      *tls.Config
	server                         *http.Server
	listener                       net.Listener
	operatorServer                 *http.Server
	operatorListener               net.Listener
	operatorSocketPath             string
	errors                         chan error
	closeOnce                      sync.Once
	rateMutex                      sync.Mutex
	operationRequests              []time.Time
	lifecycleReconciliationPending bool
}

func newDiagnosticsRuntime(config *diagnosticsRuntimeConfig, deviceID string, syncthingClients ...*SyncthingClient) (*diagnosticsRuntime, error) {
	if goruntime.GOOS != "linux" || config == nil || config.validate() != nil || !config.runtimeMountBindingsValid() {
		return nil, errDiagnosticsPairingUnavailable
	}
	stateInfo, err := os.Lstat(config.stateDirectory)
	if err != nil || !stateInfo.IsDir() || checkDiagnosticsPrivateDirectory(config.stateDirectory, stateInfo) != nil {
		return nil, errDiagnosticsCredentialStateInvalid
	}
	rawDeviceID, err := parseDiagnosticsDeviceID(deviceID)
	if err != nil {
		return nil, err
	}
	deviceDigest, err := diagnosticsDeviceIDDigest(rawDeviceID[:])
	if err != nil {
		return nil, err
	}
	credentialDirectory := filepath.Join(config.stateDirectory, "credentials")
	namespaceDirectory := filepath.Join(config.stateDirectory, "namespace")
	credentialStore, err := openDiagnosticsCredentialStore(credentialDirectory, deviceDigest[:], rand.Reader)
	if err != nil {
		return nil, err
	}
	namespaceStore, err := openDiagnosticsNamespaceStateStore(namespaceDirectory)
	if err != nil {
		return nil, err
	}
	pairing, err := newDiagnosticsPairingManager(credentialStore, rand.Reader, time.Now)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := newDiagnosticsServerTLSConfig(credentialStore, time.Now)
	if err != nil {
		return nil, err
	}
	runtime := &diagnosticsRuntime{
		config: config, credentialStore: credentialStore, namespaceStore: namespaceStore,
		pairing: pairing, tlsConfig: tlsConfig, errors: make(chan error, 1),
	}
	if len(syncthingClients) > 0 {
		runtime.syncthing = syncthingClients[0]
	}
	runtime.sessions = newDiagnosticsRuntimeSessions(config, credentialStore, namespaceStore)
	runtime.namespace = newDiagnosticsNamespaceRuntime(config, credentialStore, namespaceStore, runtime.sessions)
	runtime.namespace.preflight = runtime.preflightNamespaceHandle
	runtime.server = &http.Server{
		Handler:           runtime,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8 * 1024,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	return runtime, nil
}

func (runtime *diagnosticsRuntime) start() error {
	if runtime == nil || runtime.server == nil || runtime.tlsConfig == nil {
		return errDiagnosticsPairingUnavailable
	}
	if err := withDiagnosticsRuntimeMutationLock(runtime.credentialStore, func() error {
		return runtime.pairing.reconcileConfirmedLifecycle(runtime.prepareLifecycleCommit)
	}); err != nil {
		// A confirmed helper-key transition can be temporarily unable to append
		// every namespace manifest (for example while one exact bind is offline).
		// Keep Trigger v1 and the fixed pairing/recovery endpoint available. All
		// operation sessions remain fail-closed because the transition is still
		// present; an exact retry or explicit redeploy completes forward.
		runtime.lifecycleReconciliationPending = true
	}
	if err := runtime.startOperatorServer(); err != nil {
		return errDiagnosticsOperatorUnavailable
	}
	listener, err := net.Listen("tcp", runtime.config.ListenAddress)
	if err != nil {
		_ = runtime.operatorServer.Close()
		_ = runtime.operatorListener.Close()
		_ = os.Remove(runtime.operatorSocketPath)
		return errDiagnosticsListenerUnavailable
	}
	runtime.listener = listener
	go func() {
		err := runtime.server.Serve(tls.NewListener(listener, runtime.tlsConfig))
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			err = nil
		}
		runtime.errors <- err
		close(runtime.errors)
	}()
	return nil
}

func (runtime *diagnosticsRuntime) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if request.TLS == nil || request.TLS.Version != tls.VersionTLS13 || request.Method != http.MethodPost ||
		request.URL.RawPath != "" || request.URL.RawQuery != "" || request.URL.ForceQuery {
		diagnosticsWriteFixedStatus(writer, http.StatusBadRequest)
		return
	}
	// Count every syntactically eligible request, including malformed pairing,
	// namespace, and unknown-path traffic, before body decoding. Per-protocol
	// limits remain narrower; this is the helper-wide transport ceiling.
	if !runtime.consumeOperationRequest(time.Now()) {
		diagnosticsWriteFixedStatus(writer, http.StatusTooManyRequests)
		return
	}
	body, ok := diagnosticsReadHTTPBody(writer, request)
	if !ok {
		return
	}
	if err := withDiagnosticsRuntimeMutationLock(runtime.credentialStore, func() error {
		runtime.serveDiagnosticsBody(request.Context(), writer, request.URL.Path, body)
		return nil
	}); err != nil {
		diagnosticsWriteFixedStatus(writer, http.StatusNotFound)
	}
}

func (runtime *diagnosticsRuntime) serveDiagnosticsBody(
	ctx context.Context,
	writer http.ResponseWriter,
	path string,
	body []byte,
) {
	switch path {
	case diagnosticsPairingPath:
		runtime.handlePairing(writer, body)
	case diagnosticsNamespaceEnablementPath:
		runtime.handleNamespaceEnablement(writer, body)
	case diagnosticsNamespaceAuthorizationPath:
		runtime.handleNamespaceAuthorization(ctx, writer, body)
	case diagnosticsCapabilityPath:
		runtime.handleCapability(ctx, writer, body)
	case diagnosticsAttestationPath:
		runtime.handleAttestation(ctx, writer, body)
	case diagnosticsAuthorizeResponsePath:
		runtime.handleResponseAuthorization(ctx, writer, body)
	case diagnosticsCleanupPath:
		runtime.handleCleanup(ctx, writer, body)
	default:
		diagnosticsWriteFixedStatus(writer, http.StatusNotFound)
	}
}

func withDiagnosticsRuntimeMutationLock(store *diagnosticsCredentialStore, action func() error) error {
	if store == nil || action == nil {
		return errDiagnosticsPairingUnavailable
	}
	return withDiagnosticsCredentialFileLock(filepath.Join(store.directory, diagnosticsRuntimeMutationLockFile), action)
}

func diagnosticsReadHTTPBody(writer http.ResponseWriter, request *http.Request) ([]byte, bool) {
	mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/cbor" || len(parameters) != 0 ||
		request.ContentLength <= 0 || request.ContentLength > diagnosticsHTTPMaximumBodyBytes ||
		len(request.TransferEncoding) != 0 || request.Header.Get("Content-Encoding") != "" ||
		request.Header.Get("Expect") != "" {
		diagnosticsWriteFixedStatus(writer, http.StatusBadRequest)
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, diagnosticsHTTPMaximumBodyBytes+1))
	if err != nil || int64(len(body)) != request.ContentLength || len(body) > diagnosticsHTTPMaximumBodyBytes {
		diagnosticsWriteFixedStatus(writer, http.StatusBadRequest)
		return nil, false
	}
	return body, true
}

func (runtime *diagnosticsRuntime) handlePairing(writer http.ResponseWriter, body []byte) {
	message, err := decodeDiagnosticsPairingMessage(body)
	if err != nil {
		diagnosticsWriteFixedStatus(writer, http.StatusBadRequest)
		return
	}
	var response []byte
	switch message.messageType {
	case diagnosticsPairingAppRequest:
		response, err = runtime.pairing.acceptAppRequest(body)
	case diagnosticsPairingFinalize, diagnosticsPairingReceipt, diagnosticsPairingActivate, diagnosticsPairingAbort:
		response, err = runtime.pairing.handleBootstrapTransition(body)
	case diagnosticsPairingAppKeyRotationRequest, diagnosticsPairingAppKeyRotationNewProof,
		diagnosticsPairingHelperKeyRotationConfirm, diagnosticsPairingTLSPinRotationConfirm,
		diagnosticsPairingRevocationRequest, diagnosticsPairingLifecycleFinalize, diagnosticsPairingLifecycleAbort:
		response, err = runtime.pairing.handleLifecycleMessage(body)
	default:
		err = errDiagnosticsPairingInvalid
	}
	if err != nil {
		diagnosticsWriteFixedStatus(writer, diagnosticsPairingHTTPStatus(err))
		return
	}
	if len(response) == 0 {
		// D022 request/proof/confirmation messages whose next signed message is
		// sent by the app have no helper CBOR body. HTTP is transport diagnostics
		// only, but an accepted empty transition must not be reported as missing.
		diagnosticsWriteFixedStatus(writer, http.StatusAccepted)
		return
	}
	diagnosticsWriteCBOR(writer, http.StatusOK, response)
}

func (runtime *diagnosticsRuntime) handleCapability(ctx context.Context, writer http.ResponseWriter, body []byte) {
	runtime.handleCapabilityAt(ctx, writer, body, time.Now())
}

func (runtime *diagnosticsRuntime) handleCapabilityAt(
	ctx context.Context,
	writer http.ResponseWriter,
	body []byte,
	now time.Time,
) {
	session, err := runtime.sessions.sessionForCapabilityMessage(body, now)
	if err != nil {
		diagnosticsWriteFixedStatus(writer, diagnosticsRuntimeHTTPStatus(err))
		return
	}
	if session.release != nil {
		defer session.release()
	}
	appKeyID := diagnosticsKeyID(session.binding.appPublicKey)
	if runtime.sessions.coordinator == nil || !runtime.sessions.coordinator.allowRequest(appKeyID, now) {
		diagnosticsWriteFixedStatus(writer, http.StatusTooManyRequests)
		return
	}
	if runtime.preflightBinding(ctx, session.binding) != nil {
		diagnosticsWriteFixedStatus(writer, http.StatusNotFound)
		return
	}
	context := diagnosticsUploadVerificationContext{
		appPublicKey:    session.binding.appPublicKey,
		helperPublicKey: session.binding.helperPrivateKey.Public().(ed25519.PublicKey),
	}
	query, err := decodeDiagnosticsCapabilityMessage(body, context)
	if err != nil || validateDiagnosticsCapabilityClock(query, now) != nil ||
		!diagnosticsCapabilityMatchesBinding(query, session.binding) {
		diagnosticsWriteFixedStatus(writer, http.StatusBadRequest)
		return
	}
	response, err := buildDiagnosticsCapabilityResponse(query, session.binding, now)
	if err != nil {
		diagnosticsWriteFixedStatus(writer, http.StatusNotFound)
		return
	}
	if session.confirmation != nil {
		committed, err := runtime.pairing.observeLifecycleCapability(
			session.confirmation.recordID,
			session.confirmation.transitionDigest,
			runtime.prepareLifecycleCommit,
		)
		if err != nil || !committed {
			// A helper signing-key or TLS-pin transition is global even though
			// every app/folder authorization confirms it independently. Do not
			// return a valid proposed-state capability response until the exact
			// query has made the durable global commit possible. Earlier peers
			// receive only transport-unavailable and recover with an exact fresh
			// query after the remaining confirmations arrive.
			diagnosticsWriteFixedStatus(writer, http.StatusNotFound)
			return
		}
	}
	diagnosticsWriteCBOR(writer, http.StatusOK, response)
}

func (runtime *diagnosticsRuntime) handleAttestation(ctx context.Context, writer http.ResponseWriter, body []byte) {
	session, err := runtime.sessions.sessionForMessage(body)
	if err != nil || runtime.preflightBinding(ctx, session.binding) != nil {
		diagnosticsWriteFixedStatus(writer, diagnosticsRuntimeHTTPStatus(err))
		return
	}
	result := session.attestor.attest(body)
	switch result.disposition {
	case diagnosticsUploadAccepted:
		diagnosticsWriteCBOR(writer, http.StatusOK, result.attestation)
	case diagnosticsUploadPending:
		diagnosticsWriteFixedStatus(writer, http.StatusAccepted)
	case diagnosticsUploadRejected:
		diagnosticsWriteFixedStatus(writer, http.StatusBadRequest)
	case diagnosticsUploadUnavailable:
		diagnosticsWriteFixedStatus(writer, http.StatusNotFound)
	case diagnosticsUploadConflict:
		diagnosticsWriteFixedStatus(writer, http.StatusConflict)
	case diagnosticsUploadLimited:
		diagnosticsWriteFixedStatus(writer, http.StatusTooManyRequests)
	default:
		diagnosticsWriteFixedStatus(writer, http.StatusNotFound)
	}
}

func (runtime *diagnosticsRuntime) handleResponseAuthorization(ctx context.Context, writer http.ResponseWriter, body []byte) {
	session, err := runtime.sessions.sessionForMessage(body)
	if err != nil || runtime.preflightBinding(ctx, session.binding) != nil {
		diagnosticsWriteFixedStatus(writer, diagnosticsRuntimeHTTPStatus(err))
		return
	}
	result := session.response.authorizeResponse(body)
	diagnosticsWriteFixedStatus(writer, diagnosticsResponseHTTPStatus(result, true))
}

func (runtime *diagnosticsRuntime) handleCleanup(ctx context.Context, writer http.ResponseWriter, body []byte) {
	session, err := runtime.sessions.sessionForMessage(body)
	if err != nil || runtime.preflightBinding(ctx, session.binding) != nil {
		diagnosticsWriteFixedStatus(writer, diagnosticsRuntimeHTTPStatus(err))
		return
	}
	result := session.response.cleanup(body)
	if result.disposition == diagnosticsResponseAccepted && len(result.acknowledgment) > 0 {
		diagnosticsWriteCBOR(writer, http.StatusOK, result.acknowledgment)
		return
	}
	diagnosticsWriteFixedStatus(writer, diagnosticsResponseHTTPStatus(result, false))
}

func (runtime *diagnosticsRuntime) handleNamespaceEnablement(writer http.ResponseWriter, body []byte) {
	if err := runtime.namespace.acceptEnablement(body); err != nil {
		diagnosticsWriteFixedStatus(writer, diagnosticsNamespaceHTTPStatus(err))
		return
	}
	diagnosticsWriteFixedStatus(writer, http.StatusAccepted)
}

func (runtime *diagnosticsRuntime) handleNamespaceAuthorization(ctx context.Context, writer http.ResponseWriter, body []byte) {
	if err := runtime.namespace.authorize(ctx, body); err != nil {
		diagnosticsWriteFixedStatus(writer, diagnosticsNamespaceHTTPStatus(err))
		return
	}
	diagnosticsWriteFixedStatus(writer, http.StatusAccepted)
}

func (runtime *diagnosticsRuntime) preflightBinding(ctx context.Context, binding diagnosticsUploadBinding) error {
	return runtime.preflightNamespaceHandle(ctx, binding.folderBinding, binding.namespaceHandle)
}

func (runtime *diagnosticsRuntime) preflightNamespaceHandle(
	ctx context.Context,
	folderBinding []byte,
	handle *diagnosticsNamespaceRootHandle,
) error {
	if runtime.syncthing == nil || len(folderBinding) != 32 || handle == nil {
		return errDiagnosticsNamespaceUnsupported
	}
	folderID, err := runtime.namespace.folderIDForBinding(folderBinding)
	if err != nil {
		return err
	}
	configured, ok := runtime.config.folder(folderID)
	if !ok || configured.MountAlias == "" {
		return errDiagnosticsNamespaceUnsupported
	}
	folder, err := runtime.preflightSyncthingFolder(ctx, folderID)
	if err != nil {
		return errDiagnosticsNamespaceUnsupported
	}
	if !runtime.config.mountBindingMatches(folderID, folder.Path, configured.MountAlias, handle.Identity()) {
		return errDiagnosticsNamespaceUnsupported
	}
	return nil
}

func (runtime *diagnosticsRuntime) preflightSyncthingFolder(ctx context.Context, folderID string) (folderConfig, error) {
	if runtime == nil || runtime.syncthing == nil || folderID == "" {
		return folderConfig{}, errDiagnosticsNamespaceUnsupported
	}
	preflightContext, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	state, stateErr := runtime.credentialStore.snapshot()
	deviceDigest, deviceErr := diagnosticsSyncthingDeviceIDDigest(preflightContext, runtime.syncthing)
	if deviceErr != nil || stateErr != nil ||
		!bytesEqual(deviceDigest[:], state.Identity.DeviceIDDigest) {
		return folderConfig{}, errDiagnosticsNamespaceUnsupported
	}
	folder, err := runtime.syncthing.Folder(preflightContext, folderID)
	if err != nil || folder.ID != folderID || folder.Paused || folder.Type != "sendreceive" ||
		!filepath.IsAbs(folder.Path) || filepath.Clean(folder.Path) != folder.Path {
		return folderConfig{}, errDiagnosticsNamespaceUnsupported
	}
	ignores, err := runtime.syncthing.FolderIgnores(preflightContext, folderID)
	if err != nil || ignores.Error != "" || !diagnosticsNamespaceIgnoreVerdictFromExpanded(ignores.Expanded).valid() {
		return folderConfig{}, errDiagnosticsNamespaceUnsupported
	}
	confirmedDigest, err := diagnosticsSyncthingDeviceIDDigest(preflightContext, runtime.syncthing)
	if err != nil || !bytesEqual(confirmedDigest[:], deviceDigest[:]) ||
		!bytesEqual(confirmedDigest[:], state.Identity.DeviceIDDigest) {
		return folderConfig{}, errDiagnosticsNamespaceUnsupported
	}
	return folder, nil
}

func diagnosticsSyncthingDeviceIDDigest(ctx context.Context, syncthing *SyncthingClient) ([32]byte, error) {
	if syncthing == nil {
		return [32]byte{}, errDiagnosticsNamespaceUnsupported
	}
	deviceID, err := syncthing.GetDeviceID(ctx)
	if err != nil {
		return [32]byte{}, errDiagnosticsNamespaceUnsupported
	}
	rawDeviceID, err := parseDiagnosticsDeviceID(deviceID)
	if err != nil {
		return [32]byte{}, errDiagnosticsNamespaceUnsupported
	}
	digest, err := diagnosticsDeviceIDDigest(rawDeviceID[:])
	if err != nil {
		return [32]byte{}, errDiagnosticsNamespaceUnsupported
	}
	return digest, nil
}

func (runtime *diagnosticsRuntime) preflightPairingFolder(ctx context.Context, folderID string) error {
	configured, ok := runtime.config.folder(folderID)
	if !ok {
		return errDiagnosticsNamespaceUnsupported
	}
	folder, err := runtime.preflightSyncthingFolder(ctx, folderID)
	if err != nil {
		return err
	}
	if configured.MountAlias == "" {
		return nil
	}
	digest, err := diagnosticsFolderIDDigest(folderID)
	if err != nil {
		return errDiagnosticsNamespaceUnsupported
	}
	state, err := runtime.credentialStore.snapshot()
	if err != nil {
		return errDiagnosticsNamespaceUnsupported
	}
	var folderBinding []byte
	for _, candidate := range state.Folders {
		if bytesEqual(candidate.FolderIDDigest, digest[:]) {
			folderBinding = candidate.FolderBinding
			break
		}
	}
	root, mapped, err := runtime.sessions.rootAndFolder(folderBinding)
	if err != nil || mapped.MountAlias != configured.MountAlias {
		return errDiagnosticsNamespaceUnsupported
	}
	mountPath, err := runtime.config.mountPath(configured.MountAlias)
	if err != nil {
		return errDiagnosticsNamespaceUnsupported
	}
	handle, err := openDiagnosticsNamespaceRoot(mountPath, nil)
	if err != nil {
		return errDiagnosticsNamespaceUnsupported
	}
	defer handle.Close()
	if handle.ValidateRootRecord(root) != nil || handle.ScanFixedLayout() != nil ||
		!runtime.config.mountBindingMatches(folderID, folder.Path, configured.MountAlias, handle.Identity()) {
		return errDiagnosticsNamespaceUnsupported
	}
	return nil
}

func (runtime *diagnosticsRuntime) consumeOperationRequest(now time.Time) bool {
	runtime.rateMutex.Lock()
	defer runtime.rateMutex.Unlock()
	runtime.operationRequests = pruneDiagnosticsUploadWindow(runtime.operationRequests, now, time.Minute)
	if len(runtime.operationRequests) >= diagnosticsUploadMaximumAllRequestsMinute {
		return false
	}
	runtime.operationRequests = append(runtime.operationRequests, now)
	return true
}

func diagnosticsCapabilityMatchesBinding(message diagnosticsCapabilityMessage, binding diagnosticsUploadBinding) bool {
	homeserver, _ := diagnosticsUploadBytesField(message.value, 5, 32)
	folder, _ := diagnosticsUploadBytesField(message.value, 6, 32)
	appEpoch, _ := diagnosticsUploadUintField(message.value, 9)
	helperEpoch, _ := diagnosticsUploadUintField(message.value, 10)
	return bytesEqual(homeserver, binding.homeserverBinding) && bytesEqual(folder, binding.folderBinding) &&
		appEpoch == binding.appEpoch && helperEpoch == binding.helperEpoch
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}

func diagnosticsResponseHTTPStatus(result diagnosticsResponseResult, authorization bool) int {
	switch result.disposition {
	case diagnosticsResponseAccepted:
		if authorization {
			return http.StatusAccepted
		}
		return http.StatusOK
	case diagnosticsResponseRejected:
		return http.StatusBadRequest
	case diagnosticsResponseUnavailable:
		return http.StatusNotFound
	case diagnosticsResponseConflict:
		return http.StatusConflict
	case diagnosticsResponseLimited:
		return http.StatusTooManyRequests
	default:
		return http.StatusNotFound
	}
}

func diagnosticsPairingHTTPStatus(err error) int {
	switch {
	case errors.Is(err, errDiagnosticsPairingRateLimited):
		return http.StatusTooManyRequests
	case errors.Is(err, errDiagnosticsPairingUnavailable), errors.Is(err, errDiagnosticsPairingExpired):
		return http.StatusNotFound
	default:
		return http.StatusBadRequest
	}
}

func diagnosticsNamespaceHTTPStatus(err error) int {
	switch {
	case errors.Is(err, errDiagnosticsPairingRateLimited), errors.Is(err, errDiagnosticsNamespaceLimit):
		return http.StatusTooManyRequests
	case errors.Is(err, errDiagnosticsNamespaceConflict), errors.Is(err, errDiagnosticsNamespaceCollision):
		return http.StatusConflict
	case errors.Is(err, errDiagnosticsPairingUnavailable), errors.Is(err, errDiagnosticsNamespaceUnsupported):
		return http.StatusNotFound
	default:
		return http.StatusBadRequest
	}
}

func diagnosticsRuntimeHTTPStatus(err error) int {
	switch {
	case errors.Is(err, errDiagnosticsNamespaceConflict):
		return http.StatusConflict
	default:
		return http.StatusNotFound
	}
}

func diagnosticsWriteCBOR(writer http.ResponseWriter, status int, body []byte) {
	if len(body) == 0 || len(body) > diagnosticsHTTPMaximumBodyBytes {
		diagnosticsWriteFixedStatus(writer, http.StatusNotFound)
		return
	}
	writer.Header().Set("Content-Type", "application/cbor")
	writer.Header().Set("Content-Length", stringInt(len(body)))
	writer.WriteHeader(status)
	_, _ = writer.Write(body)
}

func diagnosticsWriteFixedStatus(writer http.ResponseWriter, status int) {
	writer.Header().Set("Content-Length", "0")
	writer.WriteHeader(status)
}

func stringInt(value int) string {
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	position := len(buffer)
	for value > 0 {
		position--
		buffer[position] = byte('0' + value%10)
		value /= 10
	}
	return string(buffer[position:])
}

func (runtime *diagnosticsRuntime) close() {
	if runtime == nil {
		return
	}
	runtime.closeOnce.Do(func() {
		if runtime.operatorServer != nil {
			_ = runtime.operatorServer.Close()
		}
		if runtime.operatorListener != nil {
			_ = runtime.operatorListener.Close()
		}
		if runtime.operatorSocketPath != "" {
			_ = os.Remove(runtime.operatorSocketPath)
		}
		if runtime.server != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = runtime.server.Shutdown(ctx)
			cancel()
		}
		if runtime.listener != nil {
			_ = runtime.listener.Close()
		}
		if runtime.sessions != nil {
			_ = runtime.sessions.close()
		}
	})
}
