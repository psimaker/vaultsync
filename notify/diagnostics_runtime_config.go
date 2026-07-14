package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	diagnosticsRuntimeConfigEnvironment = "VAULTSYNC_DIAGNOSTICS_CONFIG"
	diagnosticsRuntimeStateEnvironment  = "VAULTSYNC_DIAGNOSTICS_STATE"
	diagnosticsRuntimeConfigMaxBytes    = 64 * 1024
	diagnosticsRuntimeConfigFormat      = uint64(1)
	diagnosticsRuntimeMaximumFolders    = 8
)

var diagnosticsCGNATPrefix = netip.MustParsePrefix("100.64.0.0/10")

// diagnosticsRuntimeConfig is operator-authored and mounted read-only. It
// contains no host filesystem path: each mount alias resolves only to the
// fixed /diagnostics/<alias> path inside the container. The writable state
// directory is supplied separately so config cannot become a credential store.
type diagnosticsRuntimeConfig struct {
	FormatVersion  uint64                           `json:"format_version"`
	ListenAddress  string                           `json:"listen_address"`
	AdvertisedHost string                           `json:"advertised_host"`
	AdvertisedPort uint64                           `json:"advertised_port"`
	Folders        []diagnosticsRuntimeFolderConfig `json:"folders"`
	stateDirectory string
	configPath     string
	mountBindings  map[string][32]byte
	// mountPathOverrides is test-only in-process state. It is not serialized,
	// cannot be supplied by an operator or network request, and lets unit tests
	// model Docker's exact bind without writing under /diagnostics.
	mountPathOverrides map[string]string
}

type diagnosticsRuntimeFolderConfig struct {
	FolderID   string `json:"folder_id"`
	MountAlias string `json:"mount_alias"`
}

func loadDiagnosticsRuntimeConfig() (*diagnosticsRuntimeConfig, error) {
	configPath := strings.TrimSpace(os.Getenv(diagnosticsRuntimeConfigEnvironment))
	stateDirectory := strings.TrimSpace(os.Getenv(diagnosticsRuntimeStateEnvironment))
	if configPath == "" && stateDirectory == "" {
		return nil, nil
	}
	if configPath == "" || stateDirectory == "" {
		return nil, newConfigurationError(
			"missing",
			"set_required_configuration",
			fmt.Errorf("%s and %s must be set together", diagnosticsRuntimeConfigEnvironment, diagnosticsRuntimeStateEnvironment),
			diagnosticsRuntimeConfigEnvironment,
			diagnosticsRuntimeStateEnvironment,
		)
	}
	if !filepath.IsAbs(configPath) || filepath.Clean(configPath) != configPath ||
		!filepath.IsAbs(stateDirectory) || filepath.Clean(stateDirectory) != stateDirectory {
		return nil, diagnosticsRuntimeConfigurationError("paths must be absolute and canonical")
	}
	resolvedConfigPath, configResolveErr := filepath.EvalSymlinks(configPath)
	resolvedStateDirectory, stateResolveErr := filepath.EvalSymlinks(stateDirectory)
	if configResolveErr != nil || stateResolveErr != nil ||
		!filepath.IsAbs(resolvedConfigPath) || !filepath.IsAbs(resolvedStateDirectory) {
		return nil, diagnosticsRuntimeConfigurationError("configuration paths are unavailable")
	}
	configPath = filepath.Clean(resolvedConfigPath)
	stateDirectory = filepath.Clean(resolvedStateDirectory)

	info, err := os.Lstat(configPath)
	if err != nil || !info.Mode().IsRegular() || checkDiagnosticsReadOnlyConfigFile(configPath, info) != nil ||
		info.Size() <= 0 || info.Size() > diagnosticsRuntimeConfigMaxBytes {
		return nil, diagnosticsRuntimeConfigurationError("configuration file is unavailable")
	}
	file, err := os.Open(configPath)
	if err != nil {
		return nil, diagnosticsRuntimeConfigurationError("configuration file is unavailable")
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) ||
		checkDiagnosticsReadOnlyConfigFile(configPath, openedInfo) != nil ||
		openedInfo.Size() <= 0 || openedInfo.Size() > diagnosticsRuntimeConfigMaxBytes {
		return nil, diagnosticsRuntimeConfigurationError("configuration file identity changed")
	}

	limited := &io.LimitedReader{R: file, N: diagnosticsRuntimeConfigMaxBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	var config diagnosticsRuntimeConfig
	if err := decoder.Decode(&config); err != nil {
		return nil, diagnosticsRuntimeConfigurationError("configuration JSON is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) || limited.N == 0 {
		return nil, diagnosticsRuntimeConfigurationError("configuration has trailing data")
	}
	config.configPath = configPath
	config.stateDirectory = stateDirectory
	if err := config.validate(); err != nil {
		return nil, diagnosticsRuntimeConfigurationError(err.Error())
	}
	config.mountBindings = make(map[string][32]byte)
	aliases := make(map[string]struct{})
	for _, folder := range config.Folders {
		if folder.MountAlias == "" {
			continue
		}
		aliases[folder.MountAlias] = struct{}{}
		slot := strings.TrimPrefix(folder.MountAlias, "namespace-")
		binding, ok := parseDiagnosticsRuntimeMountBinding(strings.TrimSpace(os.Getenv(
			diagnosticsRuntimeMountBindingEnvironmentPrefix + slot,
		)))
		if !ok {
			return nil, diagnosticsRuntimeConfigurationError("runtime mount binding is unavailable")
		}
		config.mountBindings[folder.MountAlias] = binding
	}
	for slot := 1; slot <= diagnosticsRuntimeMaximumFolders; slot++ {
		value := strings.TrimSpace(os.Getenv(diagnosticsRuntimeMountBindingEnvironmentPrefix + strconv.Itoa(slot)))
		_, used := aliases["namespace-"+strconv.Itoa(slot)]
		if value != "" && !used {
			return nil, diagnosticsRuntimeConfigurationError("unused runtime mount binding is configured")
		}
	}
	return &config, nil
}

func (config diagnosticsRuntimeConfig) validate() error {
	if config.FormatVersion != diagnosticsRuntimeConfigFormat {
		return errors.New("unsupported diagnostics configuration version")
	}
	listenHost, listenPortText, err := net.SplitHostPort(config.ListenAddress)
	if err != nil || listenHost == "" || listenPortText == "" {
		return errors.New("listen_address must be an explicit IP and port")
	}
	listenAddress, err := netip.ParseAddr(listenHost)
	if err != nil || listenAddress.IsUnspecified() || listenAddress.IsMulticast() ||
		!diagnosticsPrivateListenAddress(listenAddress.Unmap()) {
		return errors.New("listen_address must be loopback, link-local, private, or CGNAT/VPN")
	}
	listenPort, err := strconv.ParseUint(listenPortText, 10, 16)
	if err != nil || listenPort == 0 || listenPort != config.AdvertisedPort {
		return errors.New("listen and advertised ports must be the same nonzero port")
	}
	if !validDiagnosticsEndpointHost(config.AdvertisedHost) {
		return errors.New("advertised_host is invalid")
	}
	if !filepath.IsAbs(config.stateDirectory) || filepath.Clean(config.stateDirectory) != config.stateDirectory {
		return errors.New("state directory is invalid")
	}
	if config.configPath != "" {
		relative, err := filepath.Rel(config.stateDirectory, config.configPath)
		if err != nil || relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
			return errors.New("configuration must be outside the writable state directory")
		}
	}
	if len(config.Folders) > diagnosticsRuntimeMaximumFolders {
		return errors.New("too many diagnostics folder mappings")
	}
	seenFolders := make(map[string]struct{}, len(config.Folders))
	seenAliases := make(map[string]struct{}, len(config.Folders))
	for _, folder := range config.Folders {
		if folder.FolderID == "" || len(folder.FolderID) > 255 || strings.TrimSpace(folder.FolderID) != folder.FolderID ||
			strings.ContainsAny(folder.FolderID, "\x00\r\n") {
			return errors.New("folder_id is invalid")
		}
		if folder.MountAlias != "" && !validDiagnosticsMountAlias(folder.MountAlias) {
			return errors.New("mount_alias is invalid")
		}
		if _, exists := seenFolders[folder.FolderID]; exists {
			return errors.New("folder_id is duplicated")
		}
		if folder.MountAlias != "" {
			if _, exists := seenAliases[folder.MountAlias]; exists {
				return errors.New("mount_alias is duplicated")
			}
			seenAliases[folder.MountAlias] = struct{}{}
		}
		seenFolders[folder.FolderID] = struct{}{}
	}
	return nil
}

func diagnosticsPrivateListenAddress(address netip.Addr) bool {
	return address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsPrivate() || diagnosticsCGNATPrefix.Contains(address)
}

func validDiagnosticsMountAlias(alias string) bool {
	if !strings.HasPrefix(alias, "namespace-") {
		return false
	}
	slot, err := strconv.Atoi(strings.TrimPrefix(alias, "namespace-"))
	return err == nil && slot >= 1 && slot <= diagnosticsRuntimeMaximumFolders && alias == "namespace-"+strconv.Itoa(slot)
}

func (config diagnosticsRuntimeConfig) folder(folderID string) (diagnosticsRuntimeFolderConfig, bool) {
	for _, folder := range config.Folders {
		if folder.FolderID == folderID {
			return folder, true
		}
	}
	return diagnosticsRuntimeFolderConfig{}, false
}

func (config diagnosticsRuntimeConfig) runtimeMountBindingsValid() bool {
	for _, folder := range config.Folders {
		if folder.MountAlias == "" || config.mountPathOverrides[folder.MountAlias] != "" {
			continue
		}
		binding, ok := config.mountBindings[folder.MountAlias]
		if !ok || !nonzeroDiagnosticsBytes(binding[:]) {
			return false
		}
	}
	return true
}

func (config diagnosticsRuntimeConfig) mountPath(alias string) (string, error) {
	if !validDiagnosticsMountAlias(alias) {
		return "", errors.New("invalid diagnostics mount alias")
	}
	if override := config.mountPathOverrides[alias]; override != "" {
		if !filepath.IsAbs(override) || filepath.Clean(override) != override {
			return "", errors.New("invalid diagnostics test mount")
		}
		return override, nil
	}
	return filepath.Join("/diagnostics", alias), nil
}

func diagnosticsRuntimeConfigurationError(message string) error {
	return newConfigurationError(
		"invalid_value",
		"correct_configuration_value",
		errors.New(message),
		diagnosticsRuntimeConfigEnvironment,
		diagnosticsRuntimeStateEnvironment,
	)
}
