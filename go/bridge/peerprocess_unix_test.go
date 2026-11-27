//go:build unix

package bridge

import (
	"fmt"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// peerReaperScript ends the peer's process group once its standard input
// reaches end of file. The test process holds the only write end of that
// pipe, so end of file means the test process is gone, however it went — a
// -timeout panic, a panic in another goroutine, a signal — and none of those
// runs a t.Cleanup. The reaper ignores the terminal's signals, so only end of
// file or SIGKILL end it, and it is a member of the group it kills: while it
// waits, no other process can be given the group's number.
const peerReaperScript = `trap '' HUP INT TERM; read -r _; kill -s KILL -- "-$1"`

// startPeerProcess starts cmd, the peer's engine, as the leader of a process
// group of its own and puts a reaper into that group (#334). It returns the
// function that ends the group: it kills both, waits for both, and returns
// what is still in the group after a few seconds — nothing, unless a process
// escaped the kill.
//
// The engine leads the group itself, so Syncthing's SetLowPriority on Linux,
// which moves a process that is not a group leader into a new group, leaves
// it where it is.
func startPeerProcess(cmd *exec.Cmd) (stop func() []string, err error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	pgid := cmd.Process.Pid
	reaper := exec.Command("/bin/sh", "-c", peerReaperScript, "vaultsync-peer-reaper", strconv.Itoa(pgid))
	reaper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: pgid}
	alive, err := reaper.StdinPipe()
	if err == nil {
		err = reaper.Start()
	}
	if err != nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_ = cmd.Wait()
		return nil, fmt.Errorf("start the peer's reaper: %w", err)
	}
	return func() []string {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_ = cmd.Wait()
		_ = reaper.Wait()
		_ = alive.Close()
		return processGroupLeft(pgid, 10*time.Second)
	}, nil
}

// processGroupLeft waits up to timeout for group pgid to be empty and returns
// its live members if it is not.
func processGroupLeft(pgid int, timeout time.Duration) []string {
	deadline := time.Now().Add(timeout)
	for {
		// ESRCH: nobody is left. EPERM: the number now belongs to
		// another user's group, so ours is gone too.
		if err := syscall.Kill(-pgid, 0); err != nil {
			return nil
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	procs, err := listProcesses()
	if err != nil {
		return []string{fmt.Sprintf("process group %d still has members, and listing them failed: %v", pgid, err)}
	}
	var left []string
	for _, p := range procs {
		if p.pgid == pgid && !p.zombie() {
			left = append(left, p.String())
		}
	}
	return left
}
