//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// #264: an agent never installs a Syncthing older than the one VaultSync
// set up on this computer — an older VaultSync (an old copy, an older
// VaultSync for Mac dragged into Applications, an old download) stops and
// says so, instead of putting its older engine over the state the newer one
// wrote. The guard has to sit in the older build, so it ships before the
// first app release.

// newerEngineHere records a newer Syncthing than this build pins, its
// binary in place, and returns what the binary holds.
func newerEngineHere(t *testing.T, lay layout) string {
	t.Helper()
	writeFile(t, lay.Syncthing, "the newer engine")
	sum, err := fileSHA256(lay.Syncthing)
	if err != nil {
		t.Fatal(err)
	}
	if err := updateState(lay.State, func(s *agentState) {
		s.Syncthing.Version, s.Syncthing.BinarySHA256 = "v9.0.0", sum
	}); err != nil {
		t.Fatal(err)
	}
	return "the newer engine"
}

func untouched(t *testing.T, lay layout, engine string) {
	t.Helper()
	if have, _ := os.ReadFile(lay.Syncthing); string(have) != engine {
		t.Fatalf("the newer engine was replaced: %q", have)
	}
	if st, err := loadState(lay.State); err != nil || st.Syncthing.Version != "v9.0.0" {
		t.Fatalf("agent.json's engine changed: %+v %v", st.Syncthing, err)
	}
}

func TestIssue264_AnOlderSetupDoesNotPutAnOlderEngineBack(t *testing.T) {
	home := t.TempDir()
	lay, err := layoutFor("linux", home, envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	engine := newerEngineHere(t, lay)
	orig := ttyPath
	ttyPath = filepath.Join(t.TempDir(), "no-tty")
	defer func() { ttyPath = orig }()
	out := &bytes.Buffer{}
	term, closeTerm := openTerm(out)
	defer closeTerm()
	a := &app{goos: "linux", home: home, lay: lay, out: out, getenv: envOf(nil)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // whatever happens, nothing is downloaded
	_, _, err = a.ensureEngine(ctx, term)
	if !strings.Contains(refusalText(err), "older than the one set up on this computer") {
		t.Fatalf("an older setup with a newer engine here: %v\n%s", err, out)
	}
	if strings.Contains(out.String(), "Downloading") {
		t.Fatalf("an older setup set out to download its engine:\n%s", out)
	}
	untouched(t, lay, engine)
}

// The service of an older build parks — launchd would only start an
// exiting one again every 10 s — and touches nothing; run in a terminal it
// says so and ends.
func TestIssue264_AnOlderServiceDoesNotPutAnOlderEngineBack(t *testing.T) {
	a, _ := runFixture(t)
	engine := newerEngineHere(t, a.lay)
	origMode, origTerm := serviceMode, stderrIsTerminal
	defer func() { serviceMode, stderrIsTerminal = origMode, origTerm }()
	serviceMode = true
	ended := make(chan error, 1)
	stop := inBackground(t, func(ctx context.Context) error {
		err := a.run(ctx, nil)
		ended <- err
		return err
	})
	select {
	case err := <-ended:
		t.Fatalf("an older service ended instead of parking: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	if err := stop(); err != nil {
		t.Fatalf("a parked older service ended with %v when stopped", err)
	}
	untouched(t, a.lay, engine)

	serviceMode = false
	stderrIsTerminal = func() bool { return true }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := a.run(ctx, nil)
	if !errors.Is(err, errOlderAgent) {
		t.Fatalf("an older run in a terminal: %v", err)
	}
	if got := serviceSummary(err); got != errOlderAgent.Error() {
		t.Fatalf("the service log says %q", got)
	}
	untouched(t, a.lay, engine)
}

// status of an older build says it is older than the VaultSync set up here.
func TestIssue264_StatusSaysThisVaultSyncIsOlder(t *testing.T) {
	a := installedApp(t, "linux", &fakeRunner{})
	newerEngineHere(t, a.lay)
	if out := statusText(t, a); !strings.Contains(out, "Note: this VaultSync is older than the one set up on this computer: its sync engine is Syncthing 9.0.0") {
		t.Fatalf("status of an older build:\n%s", out)
	}
	// Without the engine's config too it says the same, not "run vaultsync
	// setup": this build's setup refuses.
	if err := os.Remove(filepath.Join(a.lay.Home, "config.xml")); err != nil {
		t.Fatal(err)
	}
	out := statusText(t, a)
	if !strings.Contains(out, "Note: this VaultSync is older than the one set up on this computer") || strings.Contains(out, "Run vaultsync setup") {
		t.Fatalf("status of an older build without the engine's config:\n%s", out)
	}
	// Nothing set up at all is still "not set up".
	if err := os.Remove(a.lay.State); err != nil {
		t.Fatal(err)
	}
	if out := statusText(t, a); !strings.Contains(out, "not set up on this computer yet. Run vaultsync setup.") {
		t.Fatalf("status with nothing set up:\n%s", out)
	}
}

func TestIssue264_EngineVersionsCompare(t *testing.T) {
	for _, c := range []struct {
		a, b  string
		newer bool
	}{
		{"v2.1.7", "v2.1.6", true}, {"v2.2.0", "v2.1.9", true}, {"v3.0.0", "v2.9.9", true},
		{"v2.1.6", "v2.1.6", false}, {"v2.1.5", "v2.1.6", false}, {"v2.1.6-rc.1", "v2.1.6", false},
		{"", "v2.1.6", false}, {"garbage", "v2.1.6", false}, {"v2.10.0", "v2.9.0", true},
	} {
		if got := engineNewer(c.a, c.b); got != c.newer {
			t.Errorf("engineNewer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

// agent.json in a newer format than this build knows is a newer VaultSync
// set up here: setup refuses, the service parks, status says so — never the
// bare "another version" error, nor a restart every 10 s. (desktop-v0.1.0
// refuses such an agent.json too: the one guard it has.)
func TestIssue264_ANewerAgentJSONMeansThisBuildIsOlder(t *testing.T) {
	a, _ := runFixture(t)
	writeFile(t, a.lay.State, `{"version": 3, "syncthing": {"version": "v3.0.0"}}`)
	orig := ttyPath
	ttyPath = filepath.Join(t.TempDir(), "no-tty")
	defer func() { ttyPath = orig }()
	out := &bytes.Buffer{}
	term, closeTerm := openTerm(out)
	defer closeTerm()
	if _, _, err := a.ensureEngine(context.Background(), term); !strings.Contains(refusalText(err), "older than the one set up on this computer") {
		t.Fatalf("setup beside a newer agent.json: %v", err)
	}
	origMode := serviceMode
	defer func() { serviceMode = origMode }()
	serviceMode = true
	ended := make(chan error, 1)
	stop := inBackground(t, func(ctx context.Context) error {
		err := a.run(ctx, nil)
		ended <- err
		return err
	})
	select {
	case err := <-ended:
		t.Fatalf("the service beside a newer agent.json ended instead of parking: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	if err := stop(); err != nil {
		t.Fatalf("it ended with %v when stopped", err)
	}
	if have, _ := os.ReadFile(a.lay.State); !strings.Contains(string(have), `"version": 3`) {
		t.Fatalf("the newer agent.json was rewritten: %s", have)
	}
}

// A recorded engine version this build cannot read is not taken for an
// older one: the guard fails closed.
func TestIssue264_AnUnreadableEngineVersionIsNotTakenForOlder(t *testing.T) {
	for _, v := range []string{"v2.2", "v2.2.0+build", "next"} {
		if !recordedEngineNewer(v) {
			t.Errorf("%q counted as not newer", v)
		}
	}
	if recordedEngineNewer("") || recordedEngineNewer(syncthingVersion) {
		t.Fatal("nothing recorded, or this pin, counted as newer")
	}
}

// The service of an older VaultSync for Mac parks until its program is
// replaced — the setup link putting the current app in place, or one
// dragged over it: then it ends, and launchd starts the current build.
func TestIssue264_AParkedOlderAppEndsWhenItsProgramIsReplaced(t *testing.T) {
	a, _ := runFixture(t)
	newerEngineHere(t, a.lay)
	app := fakeMacApp(t, filepath.Join(a.home, "Applications"), appBundleName, shippedInfoPlist(t))
	agent := filepath.Join(app, "Contents", "MacOS", "vaultsync")
	a.appEnv = macAppEnv(a.home, filepath.Join(t.TempDir(), "none"), agent)
	every, grace := appCheckEvery, appGoneGrace
	appCheckEvery, appGoneGrace = 20*time.Millisecond, 20*time.Millisecond
	defer func() { appCheckEvery, appGoneGrace = every, grace }()
	origMode := serviceMode
	defer func() { serviceMode = origMode }()
	serviceMode = true
	ended := make(chan error, 1)
	stop := inBackground(t, func(ctx context.Context) error {
		err := a.run(ctx, nil)
		ended <- err
		return err
	})
	defer func() { _ = stop() }()
	select {
	case err := <-ended:
		t.Fatalf("the older app's service ended instead of parking: %v", err)
	case <-time.After(10 * appCheckEvery):
	}
	newer := agent + ".new"
	writeFile(t, newer, "#!/bin/sh\necho the current build\n")
	if err := os.Chmod(newer, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(newer, agent); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-ended:
		if err != nil {
			t.Fatalf("the replaced older service ended with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the older app's service stayed parked after its program was replaced")
	}
}

// runsAs makes this program the file at self (with what), as os.Executable
// would say it.
func runsAs(t *testing.T, a *app, self, what string) {
	t.Helper()
	writeFile(t, self, what)
	a.appEnv = appEnv{goos: a.goos, home: a.home,
		executable: func() (string, error) { return self, nil },
		resolve:    filepath.EvalSymlinks, readFile: os.ReadFile, lstat: os.Lstat, owned: ownedByMe}
}

// start judges the background service only when it runs this very build:
// then what this program knows of itself holds (older: it does not sync;
// current: running again). For another build this program's pin says
// nothing — a newer service syncs, an older one parks — so start claims
// neither and leaves it to status, which asks the running agent. An older
// download's start must not say "does not sync" beside a newer service that
// syncs (the person would delete in that belief, and the deletions reach
// every device), nor the current build's start "running again" beside an
// older one that parks.
func TestIssue264_StartJudgesTheServiceByWhatItRuns(t *testing.T) {
	// systemd says the started unit runs.
	active := func() *fakeRunner {
		return &fakeRunner{answers: map[string]string{"systemctl --user is-active vaultsync.service": "active"}}
	}
	text := func(a *app) string { return a.out.(*bytes.Buffer).String() }

	// This very build, older than what is set up: it does not sync.
	a := installedApp(t, "linux", active())
	newerEngineHere(t, a.lay)
	runsAs(t, a, a.lay.Agent, "this build")
	if err := a.startService(); !strings.Contains(refusalText(err), "runs, but it does not sync: this VaultSync is older") {
		t.Fatalf("start of an older build that the service runs: %v", err)
	}
	// This very build, current: running again.
	a = installedApp(t, "linux", active())
	runsAs(t, a, a.lay.Agent, "this build")
	if err := a.startService(); err != nil || !strings.Contains(text(a), "running again") {
		t.Fatalf("start of the build the service runs: %v\n%s", err, text(a))
	}

	// An older download beside a newer service: no "does not sync".
	a = installedApp(t, "linux", active())
	writeFile(t, a.lay.Agent, "the newer build the service runs")
	newerEngineHere(t, a.lay)
	runsAs(t, a, filepath.Join(t.TempDir(), "vaultsync"), "an older download")
	if err := a.startService(); err != nil {
		t.Fatalf("start beside a newer service: %v", err)
	}
	if out := text(a); !strings.Contains(out, "runs another VaultSync build than this vaultsync — vaultsync status says whether it syncs") || !strings.Contains(out, "Note: this VaultSync is older") || strings.Contains(out, "does not sync") {
		t.Fatalf("start beside a newer service:\n%s", out)
	}

	// The current build beside an older service, which parks: no
	// "running again".
	a = installedApp(t, "linux", active())
	writeFile(t, a.lay.Agent, "an older build the service runs")
	runsAs(t, a, filepath.Join(t.TempDir(), "vaultsync"), "the current build")
	if err := a.startService(); err != nil {
		t.Fatalf("start beside an older service: %v", err)
	}
	if out := text(a); strings.Contains(out, "running again") || !strings.Contains(out, "vaultsync status says whether it syncs") {
		t.Fatalf("start beside an older service:\n%s", out)
	}
}

// agent.json takes format 2 where a build from desktop-v0.2.0 on takes
// over, and desktop-v0.1.0 refuses every format but 1: a kept v0.1.0
// download changes nothing there — it knows neither the app nor an older
// engine's hazard. Format 1 is read, and the takeover writes 2.
func TestIssue264_DesktopV010RefusesWhatThisBuildWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	writeFile(t, path, `{"version": 1, "guiPort": 41234, "syncthing": {"version": "v2.1.6"}}`)
	st, err := loadState(path)
	if err != nil || st.GUIPort != 41234 || st.Syncthing.Version != "v2.1.6" {
		t.Fatalf("format 1 not read: %+v %v", st, err)
	}
	if err := updateState(path, raiseStateFormat); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var format struct {
		Version int `json:"version"`
	}
	// desktop-v0.1.0's loadState: anything but version 1 is "another
	// VaultSync version", and it stops before it changes anything.
	if err := json.Unmarshal(data, &format); err != nil || format.Version == 1 || format.Version != agentStateVersion {
		t.Fatalf("the takeover wrote format %d (%v); desktop-v0.1.0 reads format 1", format.Version, err)
	}
	if back, err := loadState(path); err != nil || back.GUIPort != 41234 {
		t.Fatalf("format 2 not read back: %+v %v", back, err)
	}
}

// A newer format is newer whatever shape its fields take — never
// "damaged", which would skip the guard (and a person told the file is
// damaged may delete it). This format with a field of the wrong shape is.
func TestIssue264_ANewerFormatIsNewerWhateverItsFieldsLookLike(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.json")
	writeFile(t, path, `{"version": 3, "guiPort": "8384", "syncthing": ["v3.0.0"], "started": "now"}`)
	if st, err := loadState(path); !errors.Is(err, errStateNewer) || !olderThanSetUp(st, err) {
		t.Fatalf("a newer format with reshaped fields: %v", err)
	}
	writeFile(t, path, `{"version": 2, "guiPort": "8384"}`)
	if _, err := loadState(path); err == nil || errors.Is(err, errStateNewer) || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("this format with a field of the wrong shape: %v", err)
	}
}

// An older copy beside an installed VaultSync for Mac parks and stays
// parked — with a newer engine recorded or a newer agent.json format — and
// writes nothing: a park note in its older shape would drop what the newer
// build keeps, and a run ended at the first look would be started again by
// launchd every 10 s.
func TestIssue264_AnOlderCopyBesideTheAppParksAndWritesNothing(t *testing.T) {
	for name, older := range map[string]func(*testing.T, *app){
		"a newer engine recorded": func(t *testing.T, a *app) { newerEngineHere(t, a.lay) },
		"a newer agent.json format": func(t *testing.T, a *app) {
			writeFile(t, a.lay.State, `{"version": 3, "syncthing": {"version": "v3.0.0"}}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			a, _ := copyBesideApp(t, true)
			older(t, a)
			before, err := os.ReadFile(a.lay.State)
			if err != nil {
				t.Fatal(err)
			}
			ended := make(chan error, 1)
			stop := inBackground(t, func(ctx context.Context) error {
				err := a.run(ctx, nil)
				ended <- err
				return err
			})
			select {
			case err := <-ended:
				t.Fatalf("the older copy ended instead of parking: %v", err)
			case <-time.After(20 * appCheckEvery):
			}
			if err := stop(); err != nil {
				t.Fatalf("the parked copy ended with %v when stopped", err)
			}
			if after, _ := os.ReadFile(a.lay.State); !bytes.Equal(before, after) {
				t.Fatalf("the older copy rewrote agent.json:\n%s", after)
			}
		})
	}
}

// status of a build older than agent.json's format says so — and does not
// end on the raw format error after that note.
func TestIssue264_StatusOfAnOlderBuildBesideANewerFormat(t *testing.T) {
	a := installedApp(t, "linux", &fakeRunner{})
	writeFile(t, a.lay.State, `{"version": 3, "syncthing": {"version": "v3.0.0"}}`)
	if out := statusText(t, a); !strings.Contains(out, "Note: this VaultSync is older than the one set up on this computer") {
		t.Fatalf("status beside a newer agent.json format:\n%s", out)
	}
}

// An older copy whose engine is parked, and VaultSync for Mac arrives: it
// ends (launchd starts it again, and it parks beside the app) — and notes
// nothing in agent.json, whose newer build's fields it would drop.
func TestIssue264_AnOlderCopyNotesNothingWhenTheAppComes(t *testing.T) {
	a, _ := copyBesideApp(t, false)
	newerEngineHere(t, a.lay)
	before, err := os.ReadFile(a.lay.State)
	if err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 1)
	stop := inBackground(t, func(ctx context.Context) error {
		err := a.run(ctx, nil)
		ended <- err
		return err
	})
	defer func() { _ = stop() }()
	select {
	case err := <-ended:
		t.Fatalf("the older copy ended before the app came: %v", err)
	case <-time.After(10 * appCheckEvery):
	}
	fakeMacApp(t, filepath.Join(a.home, "Applications"), appBundleName, shippedInfoPlist(t))
	select {
	case err := <-ended:
		if err != nil {
			t.Fatalf("the older copy ended with %v when the app came", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the older copy went on beside the app")
	}
	if after, _ := os.ReadFile(a.lay.State); !bytes.Equal(before, after) {
		t.Fatalf("the older copy rewrote agent.json:\n%s", after)
	}
}

// A run in a terminal from VaultSync for Mac ends when the app is replaced
// by another build (the setup link's update, one dragged over it) — and
// says so: a `vaultsync run` kept in a terminal must not stop syncing
// without a word. The service ends without an error instead, and launchd
// starts what is there now.
func TestIssue264_ATerminalRunSaysWhenItsProgramIsReplaced(t *testing.T) {
	a, _ := runFixture(t)
	app := fakeMacApp(t, filepath.Join(a.home, "Applications"), appBundleName, shippedInfoPlist(t))
	agent := filepath.Join(app, "Contents", "MacOS", "vaultsync")
	a.appEnv = macAppEnv(a.home, filepath.Join(t.TempDir(), "none"), agent)
	fastAppWatch(t)
	origMode, origTerm := serviceMode, stderrIsTerminal
	defer func() { serviceMode, stderrIsTerminal = origMode, origTerm }()
	serviceMode = false
	stderrIsTerminal = func() bool { return true }
	ended := make(chan error, 1)
	stop := inBackground(t, func(ctx context.Context) error {
		err := a.run(ctx, nil)
		ended <- err
		return err
	})
	defer func() { _ = stop() }()
	select {
	case err := <-ended:
		t.Fatalf("the run ended before its program was replaced: %v", err)
	case <-time.After(10 * appCheckEvery):
	}
	newer := agent + ".new"
	writeFile(t, newer, "#!/bin/sh\necho the current build\n")
	if err := os.Chmod(newer, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(newer, agent); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-ended:
		if !errors.Is(err, errProgramReplaced) {
			t.Fatalf("the replaced run ended with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the run went on after its program was replaced")
	}
}

// serveEngineLocally makes the engine download come from a local server,
// its checksum pinned to the archive served there.
func serveEngineLocally(t *testing.T, goos string, binary []byte) {
	t.Helper()
	key := goos + "/" + runtime.GOARCH
	pin, ok := syncthingPins[key]
	if !ok {
		t.Skipf("no pin for %s", key)
	}
	top, _, _ := strings.Cut(pin.binary, "/")
	archive := realTarGz(t, top, binary)
	if strings.HasSuffix(pin.archive, ".zip") {
		archive = realZip(t, top, binary)
	}
	origPin, origBase := pin, syncthingDownloadBase
	syncthingPins[key] = syncthingPin{archive: pin.archive, sha256: sum(archive), binary: pin.binary}
	syncthingDownloadBase = serveArchive(t, pin.archive, archive).URL + "/"
	t.Cleanup(func() { syncthingPins[key], syncthingDownloadBase = origPin, origBase })
}

// untilTheRunServes waits for a's run to have its engine going: its socket
// recorded, or the record that it has to do without one.
func untilTheRunServes(t *testing.T, a *app) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if st, err := loadState(a.lay.State); err == nil && st.ControlSocket != "" {
			return
		}
		if _, err := os.Stat(a.lay.SocketErrorFile()); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the run did not get its engine going")
		}
	}
}

// stateFormat is the format agent.json at path is in.
func stateFormat(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return f.Version
}

// asV010 puts agent.json at path in format 1, as desktop-v0.1.0 wrote it.
func asV010(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	m["version"] = 1
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, string(out)+"\n")
}

// A later build's pair, setup notes or run beside a desktop-v0.1.0 that is
// still installed keep agent.json in format 1: v0.1.0 reads that format
// alone, and its service would otherwise stop at its next start — again
// every 10 s, with nothing synced. Only a takeover moves it to format 2.
func TestIssue264_AFormatOneSetupStaysReadableUntilSetupTakesOver(t *testing.T) {
	a, _ := runFixture(t)
	asV010(t, a.lay.State)
	if err := updateState(a.lay.State, func(s *agentState) { s.Env = map[string]string{"PATH": "/usr/bin"} }); err != nil {
		t.Fatal(err)
	}
	if err := updateStateIfSetUp(a.lay.State, func(s *agentState) { s.Started.Service = "a build" }); err != nil {
		t.Fatal(err)
	}
	if f := stateFormat(t, a.lay.State); f != 1 {
		t.Fatalf("a note beside desktop-v0.1.0 wrote format %d", f)
	}
	// A run: its engine's port, its socket's place, and that place cleared
	// when it ends.
	stop := inBackground(t, func(ctx context.Context) error { return a.run(ctx, nil) })
	untilTheRunServes(t, a)
	if err := stop(); err != nil {
		t.Fatalf("the run ended with %v", err)
	}
	if f := stateFormat(t, a.lay.State); f != 1 {
		t.Fatalf("a run beside desktop-v0.1.0 wrote format %d", f)
	}

	// setup takes over: its copy is in place, and agent.json takes this
	// build's format, which desktop-v0.1.0 refuses from then on.
	b, _, _ := macSetupFixture(t)
	asV010(t, b.lay.State)
	_, _ = setupFrom(t, b) // its pairing with nobody fails after the takeover
	if f := stateFormat(t, b.lay.State); f != agentStateVersion {
		t.Fatalf("setup left agent.json in format %d", f)
	}
}

// The other takeovers move agent.json from format 1 too: an engine this
// build installs (run, and setup's ensureEngine) — an older VaultSync must
// not then put its own Syncthing over it — and run keeping VaultSync's copy
// at VaultSync for Mac's build, after which a restart of the old service
// runs this build.
func TestIssue264_EveryTakeoverMovesTheFormat(t *testing.T) {
	t.Run("run installs its engine", func(t *testing.T) {
		a, _ := runFixture(t)
		binary, err := os.ReadFile(a.lay.Syncthing)
		if err != nil {
			t.Fatal(err)
		}
		asV010(t, a.lay.State)
		if err := os.Remove(a.lay.Syncthing); err != nil {
			t.Fatal(err)
		}
		serveEngineLocally(t, a.goos, binary)
		stop := inBackground(t, func(ctx context.Context) error { return a.run(ctx, nil) })
		untilTheRunServes(t, a)
		if err := stop(); err != nil {
			t.Fatalf("the run ended with %v", err)
		}
		if f := stateFormat(t, a.lay.State); f != agentStateVersion {
			t.Fatalf("a run that installed its engine left agent.json in format %d", f)
		}
	})

	t.Run("setup installs its engine", func(t *testing.T) {
		a, _ := runFixture(t)
		binary, err := os.ReadFile(a.lay.Syncthing)
		if err != nil {
			t.Fatal(err)
		}
		asV010(t, a.lay.State)
		if err := os.Remove(a.lay.Syncthing); err != nil {
			t.Fatal(err)
		}
		serveEngineLocally(t, a.goos, binary)
		orig := ttyPath
		ttyPath = filepath.Join(t.TempDir(), "no-tty")
		defer func() { ttyPath = orig }()
		out := &bytes.Buffer{}
		term, closeTerm := openTerm(out)
		defer closeTerm()
		if _, changed, err := a.ensureEngine(context.Background(), term); err != nil || !changed {
			t.Fatalf("setup's engine install: changed=%v %v\n%s", changed, err, out)
		}
		if f := stateFormat(t, a.lay.State); f != agentStateVersion {
			t.Fatalf("setup's engine install left agent.json in format %d", f)
		}
	})

	t.Run("run keeps the copy at the app's build", func(t *testing.T) {
		a, _ := runFixture(t)
		app := fakeMacApp(t, filepath.Join(a.home, "Applications"), appBundleName, shippedInfoPlist(t))
		a.appEnv = macAppEnv(a.home, filepath.Join(t.TempDir(), "none"), filepath.Join(app, "Contents", "MacOS", "vaultsync"))
		asV010(t, a.lay.State)
		if err := a.keepCopyAtAppBuild(); err != nil {
			t.Fatal(err)
		}
		if f := stateFormat(t, a.lay.State); f != agentStateVersion {
			t.Fatalf("the copy at the app's build left agent.json in format %d", f)
		}
		// An agent.json that cannot be written is not the copy's failure:
		// uninstall would say the copy could not follow, and keep the
		// command on the app.
		writeFile(t, a.lay.State, "not json")
		if err := a.keepCopyAtAppBuild(); err != nil {
			t.Fatalf("a damaged agent.json reported as the copy's failure: %v", err)
		}
	})
}

// A run refused because another holds the engine lock has taken over
// nothing: it installs no engine and writes nothing. Beside a desktop-v0.1.0
// service holding that lock, a format 2 written here would stop that service
// at its next start, and a newer engine put in place would run under it.
func TestIssue264_ARunRefusedAtTheEngineLockTouchesNothing(t *testing.T) {
	a, _ := runFixture(t)
	binary, err := os.ReadFile(a.lay.Syncthing)
	if err != nil {
		t.Fatal(err)
	}
	asV010(t, a.lay.State)
	before, err := os.ReadFile(a.lay.State)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(a.lay.Syncthing); err != nil {
		t.Fatal(err)
	}
	serveEngineLocally(t, a.goos, binary)
	unlock, err := lockFile(a.lay.Lock)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	origMode, origTerm := serviceMode, stderrIsTerminal
	defer func() { serviceMode, stderrIsTerminal = origMode, origTerm }()
	serviceMode = false
	stderrIsTerminal = func() bool { return true }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := a.run(ctx, nil); !strings.Contains(refusalText(err), "already running") {
		t.Fatalf("a run beside the lock's holder: %v", err)
	}
	if _, err := os.Stat(a.lay.Syncthing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused run installed its engine: %v", err)
	}
	if after, _ := os.ReadFile(a.lay.State); !bytes.Equal(before, after) {
		t.Fatalf("a refused run wrote agent.json:\n%s", after)
	}
}
