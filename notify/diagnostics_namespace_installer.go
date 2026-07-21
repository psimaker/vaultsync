package main

import (
	"bytes"
	"crypto/sha256"
)

var diagnosticsNamespaceRequiredIgnorePatterns = []string{
	"VaultSync Diagnostics",
	"VaultSync Diagnostics/README.txt",
	"VaultSync Diagnostics/root-manifest.cbor",
	"VaultSync Diagnostics/manifest-epochs",
	"VaultSync Diagnostics/manifest-epochs/*.helper-manifest.cbor",
	"VaultSync Diagnostics/installations",
	"VaultSync Diagnostics/installations/*",
	"VaultSync Diagnostics/installations/*/authorization.cbor",
	"VaultSync Diagnostics/installations/*/authorization-epochs",
	"VaultSync Diagnostics/installations/*/authorization-epochs/*.authorization.cbor",
	"VaultSync Diagnostics/installations/*/operations",
	"VaultSync Diagnostics/installations/*/operations/*.request.cbor",
	"VaultSync Diagnostics/installations/*/operations/*.attestation.cbor",
	"VaultSync Diagnostics/installations/*/operations/*.response.cbor",
}

type diagnosticsNamespaceIgnoreVerdict struct {
	Evaluated         bool
	IncludesSupported bool
	AnyMatched        bool
	Fingerprint       [32]byte
}

type diagnosticsNamespaceInstallerHooks struct {
	afterRootCreate func() error
}

type diagnosticsNamespacePreparationRequest struct {
	parentPath        string
	parentDevice      uint64
	parentInode       uint64
	operatorConfirmed bool
	recoveryOnly      bool
	homeserverBinding []byte
	folderBinding     []byte
	helperPublicKey   []byte
	helperEpoch       uint64
	enablement        []byte
	rootManifest      []byte
	ignore            diagnosticsNamespaceIgnoreVerdict
	stateStore        *diagnosticsNamespaceStateStore
	hooks             diagnosticsNamespaceInstallerHooks
}

func diagnosticsNamespaceIgnoreFingerprint() [32]byte {
	hash := sha256.New()
	_, _ = hash.Write([]byte("eu.vaultsync.namespace/v1/ignore-preflight\x00"))
	for _, pattern := range diagnosticsNamespaceRequiredIgnorePatterns {
		_, _ = hash.Write([]byte(pattern))
		_, _ = hash.Write([]byte{0})
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

func diagnosticsNamespaceRootForFolder(state diagnosticsNamespaceState, folderBinding []byte) (diagnosticsNamespaceRootRecord, bool) {
	for _, root := range state.Roots {
		if bytes.Equal(root.FolderBinding, folderBinding) {
			return cloneDiagnosticsNamespaceRootRecord(root), true
		}
	}
	return diagnosticsNamespaceRootRecord{}, false
}

func (verdict diagnosticsNamespaceIgnoreVerdict) valid() bool {
	return verdict.Evaluated && verdict.IncludesSupported && !verdict.AnyMatched &&
		verdict.Fingerprint == diagnosticsNamespaceIgnoreFingerprint()
}
