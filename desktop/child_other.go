//go:build !linux

package main

import "os/exec"

// setChildAttrs: on macOS the engine stays in the agent's process group, so
// launchd stops it together with the agent.
func setChildAttrs(*exec.Cmd) {}
