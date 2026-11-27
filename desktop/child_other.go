//go:build !linux

package main

import "os/exec"

// setChildAttrs: on macOS the engine stays in the agent's process group, so
// launchd stops it together with the background service's agent. A run in
// a terminal has no such end: ended by a kill or a crash, it leaves its
// engine running until that engine stops by itself, and the next run waits
// for it (#419, decision 086).
func setChildAttrs(*exec.Cmd) {}
