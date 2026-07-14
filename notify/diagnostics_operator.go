package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	diagnosticsOperatorPairPath    = "/v1/pair"
	diagnosticsOperatorPendingPath = "/v1/namespace/pending"
	diagnosticsOperatorBodyBytes   = 1024
)

type diagnosticsOperatorRequest struct {
	FolderID string `json:"folder_id"`
}

type diagnosticsAdminCommand struct {
	action         string
	folderID       string
	appFingerprint string
	reason         uint64
}

func (runtime *diagnosticsRuntime) startOperatorServer() error {
	socketPath := filepath.Join(runtime.config.stateDirectory, "operator.sock")
	if info, err := os.Lstat(socketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 || diagnosticsOperatorSocketAlive(socketPath) {
			return errDiagnosticsPairingUnavailable
		}
		if err := os.Remove(socketPath); err != nil {
			return errDiagnosticsPairingUnavailable
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errDiagnosticsPairingUnavailable
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return errDiagnosticsPairingUnavailable
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(socketPath)
		return errDiagnosticsPairingUnavailable
	}
	runtime.operatorSocketPath = socketPath
	runtime.operatorListener = listener
	runtime.operatorServer = &http.Server{
		Handler:           http.HandlerFunc(runtime.serveOperatorHTTP),
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		IdleTimeout:       5 * time.Second,
		MaxHeaderBytes:    4 * 1024,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go func() {
		_ = runtime.operatorServer.Serve(listener)
	}()
	return nil
}

func diagnosticsOperatorSocketAlive(path string) bool {
	connection, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

func (runtime *diagnosticsRuntime) serveOperatorHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	mediaType, parameters, contentTypeErr := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if request.Method != http.MethodPost || request.URL.RawPath != "" || request.URL.RawQuery != "" || request.URL.ForceQuery ||
		contentTypeErr != nil || mediaType != "application/json" || len(parameters) != 0 ||
		request.ContentLength <= 0 || request.ContentLength > diagnosticsOperatorBodyBytes || len(request.TransferEncoding) != 0 ||
		request.Header.Get("Content-Encoding") != "" || request.Header.Get("Expect") != "" {
		diagnosticsWriteFixedStatus(writer, http.StatusBadRequest)
		return
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, diagnosticsOperatorBodyBytes+1))
	decoder.DisallowUnknownFields()
	var command diagnosticsOperatorRequest
	if err := decoder.Decode(&command); err != nil || command.FolderID == "" {
		diagnosticsWriteFixedStatus(writer, http.StatusBadRequest)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		diagnosticsWriteFixedStatus(writer, http.StatusBadRequest)
		return
	}
	if _, allowed := runtime.config.folder(command.FolderID); !allowed {
		diagnosticsWriteFixedStatus(writer, http.StatusNotFound)
		return
	}
	if err := withDiagnosticsRuntimeMutationLock(runtime.credentialStore, func() error {
		runtime.serveOperatorCommand(writer, request, command)
		return nil
	}); err != nil {
		diagnosticsWriteFixedStatus(writer, http.StatusNotFound)
	}
}

func (runtime *diagnosticsRuntime) serveOperatorCommand(
	writer http.ResponseWriter,
	request *http.Request,
	command diagnosticsOperatorRequest,
) {
	switch request.URL.Path {
	case diagnosticsOperatorPairPath:
		if runtime.preflightPairingFolder(request.Context(), command.FolderID) != nil {
			diagnosticsWriteFixedStatus(writer, http.StatusNotFound)
			return
		}
		digest, err := diagnosticsFolderIDDigest(command.FolderID)
		if err != nil {
			diagnosticsWriteFixedStatus(writer, http.StatusBadRequest)
			return
		}
		qr, err := runtime.pairing.beginInvitation(digest[:], runtime.config.AdvertisedHost, runtime.config.AdvertisedPort)
		if err != nil {
			diagnosticsWriteFixedStatus(writer, diagnosticsPairingHTTPStatus(err))
			return
		}
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.Header().Set("Content-Length", stringInt(len(qr)))
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, qr)
	case diagnosticsOperatorPendingPath:
		body, _, err := runtime.namespace.pendingForFolder(command.FolderID)
		if err != nil {
			diagnosticsWriteFixedStatus(writer, http.StatusNotFound)
			return
		}
		diagnosticsWriteCBOR(writer, http.StatusOK, body)
	default:
		diagnosticsWriteFixedStatus(writer, http.StatusNotFound)
	}
}

func diagnosticsOperatorRequestBody(folderID string) ([]byte, error) {
	if folderID == "" {
		return nil, errDiagnosticsPairingUnavailable
	}
	return json.Marshal(diagnosticsOperatorRequest{FolderID: folderID})
}

func requestDiagnosticsOperator(
	ctx context.Context,
	config *diagnosticsRuntimeConfig,
	path string,
) (*http.Client, string, error) {
	if config == nil {
		return nil, "", errDiagnosticsPairingUnavailable
	}
	socketPath := filepath.Join(config.stateDirectory, "operator.sock")
	transport := &http.Transport{
		DisableCompression: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
	}
	client := &http.Client{
		Transport:     transport,
		Timeout:       10 * time.Second,
		CheckRedirect: rejectHelperHTTPRedirect,
	}
	return client, "http://unix" + path, nil
}

func requestDiagnosticsPairingInvitation(ctx context.Context, config *diagnosticsRuntimeConfig, folderID string) (string, error) {
	body, err := diagnosticsOperatorRequestBody(folderID)
	if err != nil {
		return "", err
	}
	client, endpoint, err := requestDiagnosticsOperator(ctx, config, diagnosticsOperatorPairPath)
	if err != nil {
		return "", err
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return "", errDiagnosticsPairingUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength <= 0 || response.ContentLength > diagnosticsMaximumMessageBytes*2 {
		return "", errDiagnosticsPairingUnavailable
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, diagnosticsMaximumMessageBytes*2+1))
	if err != nil || int64(len(encoded)) != response.ContentLength {
		return "", errDiagnosticsPairingUnavailable
	}
	if _, err := decodeDiagnosticsPairingQR(string(encoded)); err != nil {
		return "", errDiagnosticsPairingUnavailable
	}
	return string(encoded), nil
}

func requestDiagnosticsPendingEnablement(ctx context.Context, config *diagnosticsRuntimeConfig, folderID string) ([]byte, error) {
	body, err := diagnosticsOperatorRequestBody(folderID)
	if err != nil {
		return nil, err
	}
	client, endpoint, err := requestDiagnosticsOperator(ctx, config, diagnosticsOperatorPendingPath)
	if err != nil {
		return nil, err
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, errDiagnosticsPairingUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength <= 0 || response.ContentLength > diagnosticsHTTPMaximumBodyBytes {
		return nil, errDiagnosticsPairingUnavailable
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, diagnosticsHTTPMaximumBodyBytes+1))
	if err != nil || int64(len(encoded)) != response.ContentLength {
		return nil, errDiagnosticsPairingUnavailable
	}
	message, err := decodeDiagnosticsNamespaceMessage(encoded)
	if err != nil || message.messageType != diagnosticsNamespaceEnablement {
		return nil, errDiagnosticsPairingUnavailable
	}
	return encoded, nil
}

func runDiagnosticsPairOperator(ctx context.Context, config Config, folderID string) (string, error) {
	if config.diagnosticsRuntime == nil {
		return "", errDiagnosticsPairingUnavailable
	}
	return requestDiagnosticsPairingInvitation(ctx, config.diagnosticsRuntime, folderID)
}

func runDiagnosticsNamespaceOperator(
	ctx context.Context,
	config Config,
	folderID, sourcePath, mountedParent string,
	sourceDevice, sourceInode uint64,
	confirmed bool,
) (string, error) {
	if config.diagnosticsRuntime == nil || !confirmed {
		return "", errDiagnosticsNamespaceUnsupported
	}
	// A live signed pending request drives normal creation. If the process died
	// after the root and protected root record became durable but before the
	// local mount alias was written, an explicit rerun may instead resume that
	// exact registered root. The installer path below performs every state,
	// signature, Device/folder/ignore, and inode check and never creates in this
	// recovery mode.
	enablement, _ := requestDiagnosticsPendingEnablement(ctx, config.diagnosticsRuntime, folderID)
	syncthing := NewSyncthingClient(config.SyncthingAPIURL, config.SyncthingAPIKey)
	deviceID, err := syncthing.GetDeviceID(ctx)
	if err != nil {
		return "", errDiagnosticsNamespaceUnsupported
	}
	runtime, err := newDiagnosticsRuntime(config.diagnosticsRuntime, deviceID, syncthing)
	if err != nil {
		return "", err
	}
	defer runtime.close()
	var record diagnosticsNamespaceRootRecord
	err = withDiagnosticsRuntimeMutationLock(runtime.credentialStore, func() error {
		var prepareErr error
		record, prepareErr = prepareDiagnosticsNamespaceForOperator(
			ctx, config.diagnosticsRuntime, runtime.credentialStore, runtime.namespaceStore, syncthing,
			folderID, sourcePath, mountedParent, sourceDevice, sourceInode, enablement, true,
		)
		return prepareErr
	})
	if err != nil {
		return "", err
	}
	if !validDiagnosticsMountAlias(record.MountAlias) {
		return "", fmt.Errorf("operator result: %w", errDiagnosticsNamespaceStateInvalid)
	}
	return record.MountAlias, nil
}

func runDiagnosticsAdminOperator(ctx context.Context, config Config, command diagnosticsAdminCommand) (string, error) {
	if config.diagnosticsRuntime == nil || command.folderID == "" {
		return "", errDiagnosticsPairingUnavailable
	}
	syncthing := NewSyncthingClient(config.SyncthingAPIURL, config.SyncthingAPIKey)
	deviceID, err := syncthing.GetDeviceID(ctx)
	if err != nil {
		return "", errDiagnosticsPairingUnavailable
	}
	runtime, err := newDiagnosticsRuntime(config.diagnosticsRuntime, deviceID, syncthing)
	if err != nil {
		return "", err
	}
	defer runtime.close()
	var result string
	err = withDiagnosticsRuntimeMutationLock(runtime.credentialStore, func() error {
		var actionErr error
		result, actionErr = runDiagnosticsAdminOperatorLocked(runtime, command)
		return actionErr
	})
	return result, err
}

func runDiagnosticsAdminOperatorLocked(runtime *diagnosticsRuntime, command diagnosticsAdminCommand) (string, error) {
	state, err := runtime.credentialStore.snapshot()
	if err != nil {
		return "", err
	}
	authorizations, err := diagnosticsAdminAuthorizationsForFolder(runtime.config, state, command.folderID)
	if err != nil {
		return "", err
	}
	if command.action == "list" {
		lines := make([]string, 0, len(authorizations))
		for _, authorization := range authorizations {
			namespaceState := "no"
			if len(authorization.NamespaceInitialAppKeyID) == 32 {
				namespaceState = "yes"
			}
			lines = append(lines, fmt.Sprintf(
				"%s state=%s namespace=%s\n",
				diagnosticsAdminAppFingerprint(authorization.AppKeyID), authorization.State, namespaceState,
			))
		}
		sort.Strings(lines)
		return strings.Join(lines, ""), nil
	}
	authorization, err := diagnosticsAdminSelectAuthorization(authorizations, command.appFingerprint)
	if err != nil || authorization.State != "active" {
		return "", errDiagnosticsPairingUnavailable
	}
	encode := base64.RawURLEncoding.EncodeToString
	switch command.action {
	case "rotate-helper":
		if !diagnosticsAdminRotationReady(runtime, authorization, state.Identity) {
			return "", errDiagnosticsPairingUnavailable
		}
		proposal, proof, err := runtime.pairing.beginHelperKeyRotation(authorization.RecordID)
		if err != nil {
			return "", err
		}
		return "proposal=" + encode(proposal) + "\nproof=" + encode(proof) + "\n", nil
	case "rotate-tls":
		if !diagnosticsAdminRotationReady(runtime, authorization, state.Identity) {
			return "", errDiagnosticsPairingUnavailable
		}
		proposal, err := runtime.pairing.beginTLSPinRotation(authorization.RecordID)
		if err != nil {
			return "", err
		}
		return "proposal=" + encode(proposal) + "\n", nil
	case "revoke":
		record, err := runtime.pairing.revokeLocally(authorization.RecordID, command.reason)
		if err != nil || len(record) == 0 {
			return "", errDiagnosticsPairingUnavailable
		}
		return "revocation=" + encode(record) + "\n", nil
	default:
		return "", errDiagnosticsPairingUnavailable
	}
}

func diagnosticsAdminRotationReady(
	runtime *diagnosticsRuntime,
	authorization diagnosticsPairingAuthorization,
	identity diagnosticsHelperCredentialIdentity,
) bool {
	if runtime == nil || runtime.sessions == nil || runtime.namespaceStore == nil {
		return false
	}
	namespaceState, err := runtime.namespaceStore.snapshot()
	if err != nil {
		return false
	}
	_, hasRegisteredRoot := diagnosticsNamespaceRootForFolder(namespaceState, authorization.FolderBinding)
	if len(authorization.NamespaceInitialAppKeyID) == 0 {
		return !hasRegisteredRoot && authorization.NamespaceAuthorizationEpoch == 0
	}
	if !hasRegisteredRoot {
		return false
	}
	_, err = runtime.sessions.sessionForAuthorization(authorization, identity)
	return err == nil
}

func diagnosticsAdminAuthorizationsForFolder(
	config *diagnosticsRuntimeConfig,
	state diagnosticsCredentialState,
	folderID string,
) ([]diagnosticsPairingAuthorization, error) {
	if config == nil {
		return nil, errDiagnosticsPairingUnavailable
	}
	if _, ok := config.folder(folderID); !ok {
		return nil, errDiagnosticsPairingUnavailable
	}
	digest, err := diagnosticsFolderIDDigest(folderID)
	if err != nil {
		return nil, errDiagnosticsPairingUnavailable
	}
	var folderBinding []byte
	for _, folder := range state.Folders {
		if bytes.Equal(folder.FolderIDDigest, digest[:]) {
			folderBinding = folder.FolderBinding
			break
		}
	}
	if len(folderBinding) != 32 {
		return nil, errDiagnosticsPairingUnavailable
	}
	result := make([]diagnosticsPairingAuthorization, 0)
	for _, authorization := range state.Authorizations {
		if bytes.Equal(authorization.FolderBinding, folderBinding) {
			result = append(result, authorization)
		}
	}
	return result, nil
}

func diagnosticsAdminSelectAuthorization(
	authorizations []diagnosticsPairingAuthorization,
	fingerprint string,
) (diagnosticsPairingAuthorization, error) {
	if len(fingerprint) != 12 || fingerprint != strings.ToUpper(fingerprint) {
		return diagnosticsPairingAuthorization{}, errDiagnosticsPairingUnavailable
	}
	if decoded, err := hex.DecodeString(fingerprint); err != nil || len(decoded) != 6 {
		return diagnosticsPairingAuthorization{}, errDiagnosticsPairingUnavailable
	}
	var selected *diagnosticsPairingAuthorization
	for index := range authorizations {
		if diagnosticsAdminAppFingerprint(authorizations[index].AppKeyID) != fingerprint {
			continue
		}
		if selected != nil {
			return diagnosticsPairingAuthorization{}, errDiagnosticsPairingUnavailable
		}
		candidate := authorizations[index]
		selected = &candidate
	}
	if selected == nil {
		return diagnosticsPairingAuthorization{}, errDiagnosticsPairingUnavailable
	}
	return *selected, nil
}

func diagnosticsAdminAppFingerprint(appKeyID []byte) string {
	if len(appKeyID) != 32 {
		return ""
	}
	return strings.ToUpper(hex.EncodeToString(appKeyID[:6]))
}
