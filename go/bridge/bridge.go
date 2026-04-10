// Package bridge provides the gomobile-exported API for VaultSync.
// This is the thin layer between Swift (iOS) and syncthing/lib (Go).
//
// Exported functions use only primitive types + string + []byte.
// Complex data is serialized as JSON across the bridge.
package bridge

import (
	"runtime"

	"github.com/syncthing/syncthing/lib/build"
)

// Ping returns "pong" — used to verify the bridge is loaded and callable.
func Ping() string {
	return "pong"
}

// Version returns the Go runtime version used to build the bridge.
func Version() string {
	return runtime.Version()
}

// Arch returns the architecture the bridge was compiled for.
func Arch() string {
	return runtime.GOARCH
}

// SyncthingVersion returns the version of the embedded syncthing library.
func SyncthingVersion() string {
	return build.Version
}
