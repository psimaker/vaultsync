package main

import (
	"os/exec"
	"syscall"
)

// setChildAttrs makes the kernel stop the engine if the agent dies without
// stopping it (systemd's cgroup catches it too; this covers a manual run).
func setChildAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
}
