package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strconv"
)

const (
	diagnosticsRuntimeMountBindingEnvironmentPrefix = "VAULTSYNC_DIAGNOSTICS_MOUNT_BINDING_"
	diagnosticsRuntimeMountBindingDomain            = "eu.vaultsync.runtime/v1/mount-binding\x00"
)

// diagnosticsRuntimeMountBinding is ephemeral deployment configuration. It
// binds the exact Syncthing path observed by the helper to the exact namespace
// inode mounted by the operator. Only the digest enters the container
// environment; the path is never persisted in helper state or logged.
func diagnosticsRuntimeMountBinding(
	folderID, folderPath, alias string,
	identity diagnosticsNamespaceFileIdentity,
) [32]byte {
	hash := sha256.New()
	_, _ = hash.Write([]byte(diagnosticsRuntimeMountBindingDomain))
	for _, field := range []string{
		folderID,
		folderPath,
		alias,
		strconv.FormatUint(identity.Device, 10),
		strconv.FormatUint(identity.Inode, 10),
	} {
		_, _ = hash.Write([]byte(field))
		_, _ = hash.Write([]byte{0})
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

func parseDiagnosticsRuntimeMountBinding(value string) ([32]byte, bool) {
	var binding [32]byte
	if len(value) != hex.EncodedLen(len(binding)) {
		return binding, false
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(binding) || !nonzeroDiagnosticsBytes(decoded) || hex.EncodeToString(decoded) != value {
		return binding, false
	}
	copy(binding[:], decoded)
	return binding, true
}

func (config *diagnosticsRuntimeConfig) mountBindingMatches(
	folderID, folderPath, alias string,
	identity diagnosticsNamespaceFileIdentity,
) bool {
	if config == nil || identity.Device == 0 || identity.Inode == 0 {
		return false
	}
	actual, ok := config.mountBindings[alias]
	if !ok {
		return false
	}
	expected := diagnosticsRuntimeMountBinding(folderID, folderPath, alias, identity)
	return subtle.ConstantTimeCompare(actual[:], expected[:]) == 1
}
