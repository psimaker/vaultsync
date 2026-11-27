//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/psimaker/vaultsync/desktop/internal/control"
)

// #281: the commands of a VaultSync older than the one set up on this
// computer (decision 054) that #264 left out — pair and uninstall, the
// menu-bar app's read of agent.json, and one line of status — and a race
// between the setup link's setup and the service launchd starts beside it.

// laterField is a field of agent.json this build does not know, as a
// later build may keep one: a write in this build's shape drops it.
const laterField = "laterField"

// withALaterField adds laterField to agent.json as it is.
func withALaterField(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	m[laterField] = "kept by the newer build"
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(out)+"\n")
}

// olderHere makes this build the older one in either of 054's two ways —
// a newer engine recorded, or agent.json in a newer format — and returns
// agent.json as it then is.
func olderHere(t *testing.T, a *app, how string) []byte {
	t.Helper()
	switch how {
	case "a newer engine recorded":
		newerEngineHere(t, a.lay)
		withALaterField(t, a.lay.State)
	case "a newer agent.json format":
		st, err := loadState(a.lay.State)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, a.lay.State, `{"version": 3, "guiPort": `+jsonInt(st.GUIPort)+`, "syncthing": {"version": "v9.0.0"}, "env": {}}`+"\n")
	default:
		t.Fatalf("unknown way: %s", how)
	}
	if !a.olderBuild() {
		t.Fatalf("this build is not older after %s", how)
	}
	before, err := os.ReadFile(a.lay.State)
	if err != nil {
		t.Fatal(err)
	}
	return before
}

func jsonInt(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

var olderWays = []string{"a newer engine recorded", "a newer agent.json format"}

// pair of an older build changes nothing: no search for a Hub, no lock, no
// write of agent.json (whose newer fields its own shape would drop), no
// change to the newer engine's configuration in this build's shape — and it
// says so, with the way on.
func TestIssue281_AnOlderPairChangesNothing(t *testing.T) {
	for _, how := range olderWays {
		t.Run(how, func(t *testing.T) {
			a, _, searched := issue380App(t, false)
			// A shell whose guard variables differ from the record: a
			// pairing in a terminal records them in agent.json.
			a.getenv = envOf(map[string]string{"XDG_CONFIG_HOME": filepath.Join(a.home, "elsewhere")})
			before := olderHere(t, a, how)
			noTerminal(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			err := a.pair(ctx, nil)
			if got := refusalText(err); !strings.Contains(got, "older than the one set up on this computer") || !strings.Contains(got, "Nothing was changed") {
				t.Errorf("pair of an older build: %v\n%s", err, a.out)
			}
			if n := searched.Load(); n != 0 {
				t.Errorf("pair of an older build searched for a Hub %d time(s)", n)
			}
			if after, _ := os.ReadFile(a.lay.State); !bytes.Equal(before, after) {
				t.Errorf("pair of an older build rewrote agent.json:\n%s", after)
			}
			if _, err := os.Lstat(filepath.Join(a.lay.Base, "pair.lock")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("pair of an older build took the pairing lock: %v", err)
			}
		})
	}
}

// uninstall run from an older VaultSync for Mac changes nothing — not the
// background service, not the menu-bar icon's start at login, not
// VaultSync's own copy (the newer build's), not agent.json — before it asks
// anything, with or without --remove-data, and says how to go on.
func TestIssue281_AnOlderUninstallChangesNothing(t *testing.T) {
	for _, how := range olderWays {
		for _, args := range [][]string{{"--yes"}, {"--remove-data", "--yes"}} {
			t.Run(how+" "+strings.Join(args, " "), func(t *testing.T) {
				a, run, _ := macSetupFixture(t)
				run.loaded, run.trayLoaded = true, true
				writeFile(t, mustUnitPath(t, a.svc), "installed\n")
				writeFile(t, a.svc.trayPlistPath(), "installed\n")
				writeFile(t, a.lay.Agent, "the newer build's copy")
				before := olderHere(t, a, how)
				err := a.uninstall(context.Background(), args)
				if got := refusalText(err); !strings.Contains(got, "older than the one set up on this computer") || !strings.Contains(got, "Nothing was changed") {
					t.Errorf("uninstall of an older build: %v\n%s", err, a.out)
				}
				if serviceTouched(run.calls) {
					t.Errorf("uninstall of an older build asked the service manager: %v", run.calls)
				}
				for _, p := range []string{mustUnitPath(t, a.svc), a.svc.trayPlistPath()} {
					if _, err := os.Stat(p); err != nil {
						t.Errorf("uninstall of an older build removed %s: %v", p, err)
					}
				}
				if have, _ := os.ReadFile(a.lay.Agent); string(have) != "the newer build's copy" {
					t.Errorf("uninstall of an older build replaced VaultSync's copy (%d bytes)", len(have))
				}
				if after, _ := os.ReadFile(a.lay.State); !bytes.Equal(before, after) {
					t.Errorf("uninstall of an older build rewrote agent.json:\n%s", after)
				}
			})
		}
	}
}

// keepCopyAtAppBuild of an older VaultSync for Mac puts nothing over the
// newer build's copy and writes no agent.json: an older build writes
// nothing (054), and its write at the same format drops what the newer
// build keeps. Its run reaches it only past the older-build park; this is
// the second layer, for every caller.
func TestIssue281_AnOlderAppPutsNoCopyOverTheNewerOne(t *testing.T) {
	a, _, _ := macSetupFixture(t)
	writeFile(t, a.lay.Agent, "the newer build's copy")
	before := olderHere(t, a, "a newer engine recorded")
	if err := a.keepCopyAtAppBuild(); err != nil {
		t.Fatalf("keepCopyAtAppBuild of an older build: %v", err)
	}
	if have, _ := os.ReadFile(a.lay.Agent); string(have) != "the newer build's copy" {
		t.Errorf("an older build put its copy over the newer one (%d bytes)", len(have))
	}
	if after, _ := os.ReadFile(a.lay.State); !bytes.Equal(before, after) {
		t.Errorf("an older build rewrote agent.json:\n%s", after)
	}
}

// The menu-bar app of an older VaultSync for Mac reads agent.json's format
// first, as the agent does: a newer format whose fields it cannot read is
// a newer VaultSync, not damage — a person told the file is damaged may
// delete it.
func TestIssue281_AnOlderMenuBarAppSaysItIsOlder(t *testing.T) {
	lay, err := layoutFor("darwin", t.TempDir(), envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, lay.State, `{"version": 3, "guiPort": {"api": 8384}, "controlSocket": ["a", "b"]}`+"\n")
	_, err = control.SocketPath(lay)
	if err == nil || strings.Contains(err.Error(), "damaged") || !strings.Contains(err.Error(), "older") {
		t.Fatalf("an older menu-bar app beside a newer agent.json: %v", err)
	}
	// This format with a field of the wrong shape is damage, as before.
	writeFile(t, lay.State, `{"version": 2, "guiPort": "8384"}`+"\n")
	if _, err := control.SocketPath(lay); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("this format with a field of the wrong shape: %v", err)
	}
}

// status of an older copy that the background service runs beside
// VaultSync for Mac: the service parks (054) and writes no park note, so
// the copy note — "moving the app to the Trash will not stop it", which
// says it syncs — is wrong there. The older note above it says what is.
func TestIssue281_StatusOfAnOlderCopyBesideTheAppSaysNoCopyNote(t *testing.T) {
	run := &fakeRunner{loaded: true}
	a := installedApp(t, "darwin", run)
	fakeMacApp(t, filepath.Join(a.home, "Applications"), appBundleName, shippedInfoPlist(t))
	// This program is the copy the service runs.
	runsAs(t, a, a.lay.Agent, "the older copy")
	data, err := a.svc.unitFile(a.lay.Agent)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, mustUnitPath(t, a.svc), string(data))
	newerEngineHere(t, a.lay)
	out := statusText(t, a)
	if !strings.Contains(out, "Note: this VaultSync is older than the one set up on this computer") {
		t.Fatalf("status of an older copy:\n%s", out)
	}
	if strings.Contains(out, "moving the app to the Trash will not stop it") {
		t.Errorf("status of an older copy beside the app says it syncs on:\n%s", out)
	}
}
