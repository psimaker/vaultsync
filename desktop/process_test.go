//go:build unix

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// buildAgent compiles this package's binary for a test.
func buildAgent(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "vaultsync")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build vaultsync: %v\n%s", err, out)
	}
	return bin
}

// runService starts `vaultsync run` the way the background service does and
// returns what it wrote to stdout and stderr — the service log or journal.
func runService(t *testing.T, bin, home, stateDir string) string {
	t.Helper()
	cmd := exec.Command(bin, "run", "--state-dir", stateDir)
	cmd.Env = append(os.Environ(), "HOME="+home, "XDG_STATE_HOME=", "XDG_CONFIG_HOME=")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err == nil {
		t.Fatalf("the service was expected to fail here:\n%s", out.String())
	}
	return out.String()
}

// Codex review of #212, round 2: what the service writes to its log or the
// journal names neither the account's home folder nor a vault — also when it
// fails, and also when the engine writes to its stderr.
func TestIssue175_ServiceOutputStaysPrivate(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the vaultsync binary")
	}
	bin := buildAgent(t)

	t.Run("a damaged agent.json", func(t *testing.T) {
		home, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		lay, _ := layoutFor("linux", home, envOf(nil))
		writeFile(t, lay.State, "{")
		out := runService(t, bin, home, lay.Base)
		if !strings.Contains(out, "agent.json is damaged") || strings.Contains(out, home) {
			t.Fatalf("service output:\n%s", out)
		}
	})

	t.Run("the engine's stderr", func(t *testing.T) {
		home, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		lay, _ := layoutFor("linux", home, envOf(nil))
		writeFile(t, lay.Syncthing, "#!/bin/sh\necho 'panic: VAULT-CANARY "+filepath.Join(home, "Vaults", "Secret")+"' >&2\nexit 1\n")
		if err := os.Chmod(lay.Syncthing, 0o755); err != nil {
			t.Fatal(err)
		}
		sum, err := fileSHA256(lay.Syncthing)
		if err != nil {
			t.Fatal(err)
		}
		port, err := freeLoopbackPort()
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(lay.Home, "config.xml"), `<configuration><gui><address>127.0.0.1:`+strconv.Itoa(port)+`</address><apikey>k</apikey></gui></configuration>`)
		st := agentState{GUIPort: port}
		st.Syncthing.Version, st.Syncthing.BinarySHA256 = syncthingVersion, sum
		if err := saveState(lay.State, st); err != nil {
			t.Fatal(err)
		}
		out := runService(t, bin, home, lay.Base)
		if strings.Contains(out, "VAULT-CANARY") || strings.Contains(out, home) {
			t.Fatalf("the engine's stderr reached the service output:\n%s", out)
		}
		if !strings.Contains(out, "sync engine started") || !strings.Contains(out, "stopped") {
			t.Fatalf("service output:\n%s", out)
		}
		private, _ := os.ReadFile(filepath.Join(lay.Home, "syncthing-stderr.log"))
		if !strings.Contains(string(private), "VAULT-CANARY") {
			t.Fatalf("the engine's stderr was not kept in its private folder: %q", private)
		}
	})
}
