//go:build unix

package tray

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup puts cmd in a process group of its own. When a login job
// ends, launchd signals what is left in its process group: without this,
// a quit would take a vaultsync start and its launchctl with it.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
