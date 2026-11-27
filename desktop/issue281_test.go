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
// computer (decision 054) that #264 left out — pair, pause, resume and
// uninstall, the copy, the menu-bar app's read of agent.json, and one line
// of status (decision 091). The issue's setup.lock race is not here.

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
	if !errors.Is(err, control.ErrOlderApp) || strings.Contains(err.Error(), "damaged") || !strings.Contains(err.Error(), "older") {
		t.Fatalf("an older menu-bar app beside a newer agent.json: %v", err)
	}
	// The menu-bar app ships for Mac and Linux: the shared words name no
	// system (the menu says what it is, tray.Render).
	if strings.Contains(err.Error(), "for Mac") {
		t.Fatalf("the shared words name a system: %v", err)
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

// planService writes the background service's file as setup does, naming
// prog, and records prog as the program the last setup started it with.
func planService(t *testing.T, a *app, prog string) {
	t.Helper()
	data, err := a.svc.unitFile(prog)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, mustUnitPath(t, a.svc), string(data))
	sum, err := fileSHA256(prog)
	if err != nil {
		t.Fatal(err)
	}
	if err := updateState(a.lay.State, func(s *agentState) { s.Started.Service = sum }); err != nil {
		t.Fatal(err)
	}
}

// The way on names the program that holds the VaultSync set up here, with
// the command and this run's arguments — the one agent.json records the
// last setup started the service with, else VaultSync's own copy where it
// is another build — and the setup link only where none is found. Where the
// background service runs this very build, it parks: nothing syncs, and
// pair, pause and resume go on only once the setup link brought a current
// build; uninstall goes through the other program all the same. stop is
// named only where a background service is installed, and by this
// program's own path where no other program is found.
func TestIssue281_TheWayOnNamesTheVaultSyncSetUpHere(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	refused := func(t *testing.T, err error, want ...string) {
		t.Helper()
		got := refusalText(err)
		if !strings.Contains(got, "older than the one set up on this computer") || !strings.Contains(got, "Nothing was changed — ") {
			t.Fatalf("not the older refusal: %v", err)
		}
		for _, w := range want {
			if strings.HasPrefix(w, "!") {
				if strings.Contains(got, w[1:]) {
					t.Errorf("says %q:\n%s", w[1:], got)
				}
			} else if !strings.Contains(got, w) {
				t.Errorf("does not say %q:\n%s", w, got)
			}
		}
	}
	// anOlderDownload makes this program an older download outside
	// VaultSync's places, and returns how a shell runs it.
	anOlderDownload := func(t *testing.T, a *app) string {
		t.Helper()
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		self := filepath.Join(dir, "vaultsync")
		runsAs(t, a, self, "an older download")
		return shellQuote(self)
	}

	t.Run("an older VaultSync for Mac that the service runs", func(t *testing.T) {
		a, _, app := macSetupFixture(t)
		writeFile(t, a.lay.Agent, "the newer build's copy")
		planService(t, a, a.lay.Agent)
		newerEngineHere(t, a.lay)
		// The service's file names the app, which now holds this older
		// build: what the last setup started is the copy's build.
		data, err := a.svc.unitFile(filepath.Join(app, "Contents", "MacOS", "vaultsync"))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, mustUnitPath(t, a.svc), string(data))
		copyCmd := shellQuote(a.lay.Agent)
		refused(t, a.uninstall(context.Background(), []string{"--remove-data", "--yes"}),
			"to remove VaultSync, run "+copyCmd+" uninstall --remove-data --yes, the VaultSync set up here.",
			"Nothing syncs here meanwhile: the background service runs this VaultSync too.",
			"!setup link", "!stop")
		refused(t, a.pair(context.Background(), nil),
			"run the setup link to get the current VaultSync, then vaultsync pair.",
			"Nothing syncs here meanwhile: the background service runs this VaultSync too.")
	})

	t.Run("an older download beside the app the service runs", func(t *testing.T) {
		a, _, app := macSetupFixture(t)
		agent := filepath.Join(app, "Contents", "MacOS", "vaultsync")
		writeFile(t, a.lay.Agent, "a copy of yet another build")
		planService(t, a, agent)
		newerEngineHere(t, a.lay)
		anOlderDownload(t, a)
		appCmd := shellQuote(agent)
		// What the person passed comes along, one shell word each.
		refused(t, a.pair(context.Background(), []string{"--name", "Anna's Mac; rm -rf ~", "--code", "TULIP-ANCHOR-42"}),
			"run "+appCmd+` pair --name 'Anna'\''s Mac; rm -rf ~' --code TULIP-ANCHOR-42, the VaultSync set up here.`,
			"!Nothing syncs here", "!setup link")
		refused(t, a.uninstall(context.Background(), []string{"--yes"}),
			"to remove VaultSync, run "+appCmd+" uninstall --yes, the VaultSync set up here.",
			appCmd+" stop stops syncing now.")
	})

	t.Run("an older download beside a newer agent.json format on Linux", func(t *testing.T) {
		a := installedApp(t, "linux", &fakeRunner{})
		writeFile(t, a.lay.Agent, "the newer copy")
		writeFile(t, a.lay.State, `{"version": 3, "guiPort": 8384}`+"\n")
		anOlderDownload(t, a)
		copyCmd := shellQuote(a.lay.Agent)
		refused(t, a.pauseSync(context.Background(), true),
			"run "+copyCmd+" pause, the VaultSync set up here.", copyCmd+" stop stops syncing now.")
		refused(t, a.pauseSync(context.Background(), false),
			"run "+copyCmd+" resume, the VaultSync set up here.", "!stop")
	})

	t.Run("no other program of VaultSync's", func(t *testing.T) {
		a := installedApp(t, "linux", &fakeRunner{})
		newerEngineHere(t, a.lay)
		own := anOlderDownload(t, a)
		refused(t, a.pair(context.Background(), nil),
			"run the setup link to get the current VaultSync, then vaultsync pair.", "!Nothing syncs here")
		// stop by this program's own path: it is not refused, and a bare
		// vaultsync may be nowhere on the PATH.
		refused(t, a.uninstall(context.Background(), []string{"--remove-data"}),
			"run the setup link to get the current VaultSync, then vaultsync uninstall --remove-data.",
			" "+own+" stop stops syncing now.", "!. vaultsync stop")
	})

	t.Run("no background service installed", func(t *testing.T) {
		a := installedApp(t, "linux", &fakeRunner{})
		writeFile(t, a.lay.Agent, "the newer copy")
		newerEngineHere(t, a.lay)
		anOlderDownload(t, a)
		if err := os.Remove(mustUnitPath(t, a.svc)); err != nil {
			t.Fatal(err)
		}
		// setup --no-service: stop refuses there, and syncing goes on.
		for _, err := range []error{
			a.uninstall(context.Background(), []string{"--yes"}),
			a.pauseSync(context.Background(), true),
		} {
			refused(t, err, "!stop stops syncing now",
				"No background service is installed here, so stop stops nothing: a vaultsync run started by hand, in a terminal or at login, syncs until it is stopped there.")
		}
	})

	t.Run("an older copy that the service runs on Linux", func(t *testing.T) {
		a := installedApp(t, "linux", &fakeRunner{})
		newerEngineHere(t, a.lay)
		runsAs(t, a, a.lay.Agent, "the older copy")
		refused(t, a.pauseSync(context.Background(), false),
			"run the setup link to get the current VaultSync, then vaultsync resume.",
			"Nothing syncs here meanwhile: the background service runs this VaultSync too.")
	})
}

// A shell reads each argument back as the one word it was: plain where it
// holds nothing a shell reads specially, else in single quotes.
func TestIssue281_ArgumentsComeAlongAsOneWordEach(t *testing.T) {
	for in, want := range map[string]string{
		"--remove-data":     "--remove-data",
		"TULIP-ANCHOR-42":   "TULIP-ANCHOR-42",
		"~/Vaults/Notes":    "'~/Vaults/Notes'",
		"My Notes":          "'My Notes'",
		"a;b":               "'a;b'",
		"it's":              `'it'\''s'`,
		"":                  "''",
		"$HOME":             "'$HOME'",
		"/Users/me/x.y,z@1": "/Users/me/x.y,z@1",
	} {
		if got := shellWord(in); got != want {
			t.Errorf("shellWord(%q) = %s, want %s", in, got, want)
		}
	}
	if got := shellArgs([]string{"--vault", "My Notes"}); got != " --vault 'My Notes'" {
		t.Errorf("shellArgs: %q", got)
	}
}

// An older pause or resume that reaches a running agent goes through, as on
// main: the agent does the pausing, and this program writes nothing —
// beside a newer engine, and beside a newer format whose socket's place
// reads in this build's shape.
func TestIssue281_AnOlderPauseOrResumeGoesThroughARunningAgent(t *testing.T) {
	for _, how := range []string{"a newer engine recorded", "a newer agent.json format that records the socket"} {
		t.Run(how, func(t *testing.T) {
			f := newFakeAgent(t)
			f.hubs = 1
			a := terminalApp(t, f.path)
			var before []byte
			if how == "a newer engine recorded" {
				before = olderHere(t, a, how)
			} else {
				st, err := loadState(a.lay.State)
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, a.lay.State, `{"version": 3, "guiPort": `+jsonInt(st.GUIPort)+`, "controlSocket": `+jsonString(f.path)+`, "laterField": {"x": 1}}`+"\n")
				before = mustRead(t, a.lay.State)
			}
			if !a.olderBuild() {
				t.Fatal("this build is not the older one")
			}
			if err := a.pauseSync(context.Background(), true); err != nil {
				t.Fatalf("an older pause beside a running agent: %v", err)
			}
			if err := a.pauseSync(context.Background(), false); err != nil {
				t.Fatalf("an older resume beside a running agent: %v", err)
			}
			if strings.Join(f.calls, ",") != "pause,resume" {
				t.Errorf("the agent was asked %v", f.calls)
			}
			if out := a.out.(*bytes.Buffer).String(); !strings.Contains(out, "✓ Syncing is paused on this computer") || !strings.Contains(out, "✓ Syncing resumed on this computer.") {
				t.Errorf("what pause and resume said:\n%s", out)
			}
			if after := mustRead(t, a.lay.State); !bytes.Equal(before, after) {
				t.Errorf("an older build rewrote agent.json:\n%s", after)
			}
		})
	}
}

// An older pause or resume that reaches no agent refuses with the older
// words and asks nothing more — not the engine, not the raw format error —
// and so does one beside a newer format whose socket's place does not read
// in this build's shape: no other place is tried.
func TestIssue281_AnOlderPauseOrResumeWithNoAgentAsksNothing(t *testing.T) {
	cases := append(olderWays, "a newer format whose socket's place does not read")
	for _, how := range cases {
		t.Run(how, func(t *testing.T) {
			f := newFakeAgent(t)
			f.hubs = 1
			nobody := filepath.Join(shortDir(t), "agent.sock")
			socket := nobody
			if how == "a newer format whose socket's place does not read" {
				socket = f.path
			}
			a := terminalApp(t, socket)
			var before []byte
			if how == "a newer format whose socket's place does not read" {
				// The agent listens at the layout's own place too: a guess
				// there would reach it.
				a.lay.Socket = f.path
				writeFile(t, a.lay.State, `{"version": 3, "guiPort": 8384, "controlSocket": {"path": `+jsonString(f.path)+`}}`+"\n")
				before = mustRead(t, a.lay.State)
			} else {
				before = olderHere(t, a, how)
			}
			for _, pause := range []bool{true, false} {
				err := a.pauseSync(context.Background(), pause)
				got := refusalText(err)
				if !strings.Contains(got, "older than the one set up on this computer") || !strings.Contains(got, "Nothing was changed") {
					t.Errorf("pause %v of an older build with no agent: %v", pause, err)
				}
				if strings.Contains(got, "nothing to pause") || strings.Contains(got, "could not confirm") || strings.Contains(got, "sync engine runs") {
					t.Errorf("pause %v of an older build asked the engine itself: %v", pause, err)
				}
			}
			if len(f.calls) != 0 {
				t.Errorf("asked an agent at a place the record does not name in this build's shape: %v", f.calls)
			}
			if after := mustRead(t, a.lay.State); !bytes.Equal(before, after) {
				t.Errorf("an older build rewrote agent.json:\n%s", after)
			}
		})
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// The menu-bar app and the agent read agent.json by one format: what this
// build writes reads, and a newer format whose fields read as this build's
// is used — the menu-bar app only reads it.
func TestIssue281_TheMenuBarAppReadsByTheAgentsFormat(t *testing.T) {
	if agentStateVersion != control.StateFormat {
		t.Fatalf("the agent writes format %d, the menu-bar app reads up to %d", agentStateVersion, control.StateFormat)
	}
	lay, err := layoutFor("darwin", t.TempDir(), envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := saveState(lay.State, agentState{GUIPort: 8384, ControlSocket: "/elsewhere/agent.sock"}); err != nil {
		t.Fatal(err)
	}
	if p, err := control.SocketPath(lay); err != nil || p != "/elsewhere/agent.sock" {
		t.Fatalf("this build's agent.json: %q %v", p, err)
	}
	writeFile(t, lay.State, `{"version": 3, "guiPort": 8384, "controlSocket": "/elsewhere/agent.sock", "laterField": {}}`+"\n")
	if p, err := control.SocketPath(lay); err != nil || p != "/elsewhere/agent.sock" {
		t.Fatalf("a newer agent.json that reads: %q %v", p, err)
	}
	// A newer format without the port where this build reads it is newer,
	// not "not set up".
	writeFile(t, lay.State, `{"version": 3, "engine": {"guiPort": 8384}}`+"\n")
	if _, err := control.SocketPath(lay); !errors.Is(err, control.ErrOlderApp) {
		t.Fatalf("a newer agent.json without the port here: %v", err)
	}
}
