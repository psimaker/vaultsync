//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
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
	writeFile(t, a.lay.State, `{"version": 2, "syncthing": {"version": "v3.0.0"}}`)
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
	if have, _ := os.ReadFile(a.lay.State); !strings.Contains(string(have), `"version": 2`) {
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

// start of an older build says that the service runs but does not sync.
func TestIssue264_StartOfAnOlderBuildSaysItDoesNotSync(t *testing.T) {
	run := &fakeRunner{}
	a := installedApp(t, "darwin", run)
	newerEngineHere(t, a.lay)
	if err := a.startService(); !strings.Contains(refusalText(err), "runs, but it does not sync: this VaultSync is older") {
		t.Fatalf("start of an older build: %v", err)
	}
}
