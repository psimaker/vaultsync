//go:build unix

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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

// What the service writes to its log or the
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
		if strings.Contains(out, home) || strings.Contains(out, "agent.json") || !strings.Contains(out, "could not run the sync engine") {
			t.Fatalf("service output:\n%s", out)
		}
		details, _ := os.ReadFile(filepath.Join(lay.Base, "last-error.txt"))
		if !strings.Contains(string(details), "agent.json is damaged") {
			t.Fatalf("the details were not kept in VaultSync's folder: %q", details)
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

// The service may be told a folder outside
// the home folder (run --state-dir); its errors name neither.
func TestIssue175_ServiceOutputHidesACustomStateDir(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the vaultsync binary")
	}
	bin := buildAgent(t)
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	elsewhere, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(elsewhere, "PrivateClient", "vaultsync")
	writeFile(t, filepath.Join(state, "agent.json"), "{")
	out := runService(t, bin, home, state)
	if strings.Contains(out, elsewhere) || strings.Contains(out, "PrivateClient") || !strings.Contains(out, "could not run the sync engine") {
		t.Fatalf("service output:\n%s", out)
	}
}

// A path above a custom state folder
// (a file where a folder should be, with spaces in its name) is part of the
// error — the service prints only the kind of failure.
func TestIssue175_ServiceOutputHidesEveryPath(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the vaultsync binary")
	}
	bin := buildAgent(t)
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	elsewhere, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(elsewhere, "Clients PrivateClient")
	writeFile(t, blocker, "a file, not a folder")
	out := runService(t, bin, home, filepath.Join(blocker, "vaultsync"))
	for _, secret := range []string{"PrivateClient", "Clients", elsewhere} {
		if strings.Contains(out, secret) {
			t.Fatalf("the service output names %q:\n%s", secret, out)
		}
	}
	if !strings.Contains(out, "a file operation failed (mkdir: not a directory)") {
		t.Fatalf("service output:\n%s", out)
	}
}

func TestIssue175_ServiceSummaryCarriesNoFreeText(t *testing.T) {
	secret := "/srv/Clients PrivateClient/Notes"
	for _, err := range []error{
		fmt.Errorf("%s is damaged: %w", secret, errors.New("unexpected end of JSON input")),
		refuse("%s overlaps the vault %s", secret, secret),
		&fs.PathError{Op: "open", Path: secret, Err: syscall.EACCES},
		fmt.Errorf("download %s: %w", secret, ErrChecksumMismatch),
	} {
		if got := serviceSummary(err); strings.Contains(got, "PrivateClient") || strings.Contains(got, "Notes") {
			t.Errorf("serviceSummary(%v) = %q", err, got)
		}
	}
}
