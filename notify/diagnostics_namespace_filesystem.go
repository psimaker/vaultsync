package main

import (
	"crypto/sha256"
	"errors"
	"strconv"
	"strings"
)

const (
	diagnosticsNamespaceMaximumArtifactBytes  = 16 * 1024
	diagnosticsNamespaceMaximumRootEntries    = 128
	diagnosticsNamespaceMaximumInstallEntries = 32
	diagnosticsNamespaceMaximumCleanupFiles   = 3
)

var (
	errDiagnosticsNamespaceUnsupported = errors.New("diagnostics namespace deployment unsupported")
	errDiagnosticsNamespaceCollision   = errors.New("diagnostics namespace collision")
	errDiagnosticsNamespaceConflict    = errors.New("diagnostics namespace conflict")
	errDiagnosticsNamespaceLimit       = errors.New("diagnostics namespace limit exceeded")
)

type diagnosticsNamespaceFileIdentity struct {
	Device  uint64
	Inode   uint64
	MountID uint64
}

type diagnosticsNamespacePath struct {
	components []string
	persistent bool
}

type diagnosticsNamespaceOwnedArtifact struct {
	path     diagnosticsNamespacePath
	identity diagnosticsNamespaceFileIdentity
	digest   [32]byte
}

type diagnosticsNamespaceCleanupResult struct {
	Removed   int
	Missing   int
	Conflicts int
}

type diagnosticsNamespaceRootHandle struct {
	platform any
	identity diagnosticsNamespaceFileIdentity
}

func diagnosticsNamespaceReadmePath() diagnosticsNamespacePath {
	return diagnosticsNamespacePath{components: []string{"README.txt"}, persistent: true}
}

func diagnosticsNamespaceRootManifestPath() diagnosticsNamespacePath {
	return diagnosticsNamespacePath{components: []string{diagnosticsNamespaceRootManifestName}, persistent: true}
}

func diagnosticsNamespaceHelperEpochPath(epoch uint64) (diagnosticsNamespacePath, error) {
	name, err := diagnosticsNamespaceEpochFilename(epoch, false)
	if err != nil {
		return diagnosticsNamespacePath{}, err
	}
	return diagnosticsNamespacePath{components: []string{diagnosticsNamespaceManifestEpochsName, name}, persistent: true}, nil
}

func diagnosticsNamespaceAuthorizationPaths(installationBinding []byte) ([3]diagnosticsNamespacePath, error) {
	component, err := diagnosticsNamespaceComponent(installationBinding)
	if err != nil {
		return [3]diagnosticsNamespacePath{}, err
	}
	base := []string{diagnosticsNamespaceInstallationsName, component}
	return [3]diagnosticsNamespacePath{
		{components: append(append([]string(nil), base...), diagnosticsNamespaceAuthorizationName), persistent: true},
		{components: append(append([]string(nil), base...), diagnosticsNamespaceAuthorizationEpochsName), persistent: true},
		{components: append(append([]string(nil), base...), diagnosticsNamespaceOperationsName), persistent: true},
	}, nil
}

func diagnosticsNamespaceAuthorizationEpochPath(installationBinding []byte, epoch uint64) (diagnosticsNamespacePath, error) {
	paths, err := diagnosticsNamespaceAuthorizationPaths(installationBinding)
	if err != nil {
		return diagnosticsNamespacePath{}, err
	}
	name, err := diagnosticsNamespaceEpochFilename(epoch, true)
	if err != nil {
		return diagnosticsNamespacePath{}, err
	}
	return diagnosticsNamespacePath{
		components: append(append([]string(nil), paths[1].components...), name),
		persistent: true,
	}, nil
}

func diagnosticsNamespaceOperationPath(installationBinding, operationID []byte, kind uint64) (diagnosticsNamespacePath, error) {
	paths, err := diagnosticsNamespaceAuthorizationPaths(installationBinding)
	if err != nil {
		return diagnosticsNamespacePath{}, err
	}
	filenames, err := diagnosticsNamespaceOperationFilenames(operationID)
	if err != nil || kind < 1 || kind > 3 {
		return diagnosticsNamespacePath{}, errDiagnosticsNamespaceInvalid
	}
	return diagnosticsNamespacePath{
		components: append(append([]string(nil), paths[2].components...), filenames[kind-1]),
	}, nil
}

func (path diagnosticsNamespacePath) valid() bool {
	if len(path.components) == 0 || len(path.components) > 4 {
		return false
	}
	for _, component := range path.components {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return diagnosticsNamespaceFixedPathShape(path.components)
}

func diagnosticsNamespaceFixedPathShape(components []string) bool {
	switch len(components) {
	case 1:
		switch components[0] {
		case "README.txt", diagnosticsNamespaceRootManifestName, diagnosticsNamespaceManifestEpochsName, diagnosticsNamespaceInstallationsName:
			return true
		}
	case 2:
		if components[0] == diagnosticsNamespaceManifestEpochsName {
			return diagnosticsNamespaceFixedEpochName(components[1], false)
		}
		return components[0] == diagnosticsNamespaceInstallationsName && diagnosticsNamespaceFixedComponent(components[1])
	case 3:
		if components[0] != diagnosticsNamespaceInstallationsName || !diagnosticsNamespaceFixedComponent(components[1]) {
			return false
		}
		switch components[2] {
		case diagnosticsNamespaceAuthorizationName, diagnosticsNamespaceAuthorizationEpochsName, diagnosticsNamespaceOperationsName:
			return true
		}
	case 4:
		if components[0] != diagnosticsNamespaceInstallationsName || !diagnosticsNamespaceFixedComponent(components[1]) {
			return false
		}
		if components[2] == diagnosticsNamespaceAuthorizationEpochsName {
			return diagnosticsNamespaceFixedEpochName(components[3], true)
		}
		if components[2] == diagnosticsNamespaceOperationsName {
			return diagnosticsNamespaceFixedOperationName(components[3])
		}
	}
	return false
}

func diagnosticsNamespaceFixedComponent(component string) bool {
	_, err := parseDiagnosticsNamespaceComponent(component)
	return err == nil
}

func diagnosticsNamespaceFixedEpochName(name string, authorization bool) bool {
	suffix := ".helper-manifest.cbor"
	if authorization {
		suffix = ".authorization.cbor"
	}
	value, found := strings.CutSuffix(name, suffix)
	if !found || value == "" || (len(value) > 1 && value[0] == '0') {
		return false
	}
	epoch, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return false
	}
	expected, err := diagnosticsNamespaceEpochFilename(epoch, authorization)
	return err == nil && expected == name
}

func diagnosticsNamespaceFixedOperationName(name string) bool {
	for _, suffix := range []string{".request.cbor", ".attestation.cbor", ".response.cbor"} {
		if component, found := strings.CutSuffix(name, suffix); found {
			return diagnosticsNamespaceFixedComponent(component)
		}
	}
	return false
}

func (artifact diagnosticsNamespaceOwnedArtifact) matches(body []byte, identity diagnosticsNamespaceFileIdentity) bool {
	return artifact.identity == identity && artifact.digest == sha256.Sum256(body)
}

func (handle *diagnosticsNamespaceRootHandle) Identity() diagnosticsNamespaceFileIdentity {
	if handle == nil {
		return diagnosticsNamespaceFileIdentity{}
	}
	return handle.identity
}
