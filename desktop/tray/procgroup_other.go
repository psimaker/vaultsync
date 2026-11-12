//go:build !unix

package tray

import "os/exec"

// ownProcessGroup: the signal it guards against is launchd's.
func ownProcessGroup(*exec.Cmd) {}
