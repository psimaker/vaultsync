//go:build !unix

package bridge

import "os/exec"

// startPeerProcess starts the peer's engine and returns the function that
// ends it. Without process groups only the engine itself is killed; the
// bridge suite runs on Linux and macOS, where peerprocess_unix_test.go also
// ends the engine when the test binary dies (#334).
func startPeerProcess(cmd *exec.Cmd) (stop func() []string, err error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return func() []string {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil
	}, nil
}
