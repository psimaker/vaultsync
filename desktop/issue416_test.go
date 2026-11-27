package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/psimaker/vaultsync/desktop/internal/control"
)

// #416: vaultsync start and status judged whether the background service
// runs this very build by the file at this program's path and the file at
// the service's, not by the programs that run. A file can be replaced while
// the program that was started from it runs on: in the instant between this
// process's start and its read of itself, and under a service whose run
// lives on after its program was replaced. Now this build is the mark
// compiled into it, and the build that runs is what its agent says on the
// control socket (decision 093).

// Every app a test builds — by a helper or by hand — and every fake agent
// is this build unless the test says otherwise, as in the program, where
// newApp gives each app the program's own mark. A test of another build
// sets the app's build, or the agent's; one of a build without a mark sets
// noBuildMark.
func init() { defaultBuildMark = "this build" }

// ofBuild answers as h does, as an agent of build says it on every answer.
func ofBuild(build string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if build != "" {
			w.Header().Set(control.BuildHeader, build)
		}
		h.ServeHTTP(w, r)
	})
}

// agentOfBuild serves a control socket, at the place agent.json records,
// whose agent says it runs build (noBuildMark: says none) and answers every
// request with the 404 an agent gives for a path it does not serve — the
// answer start waits for.
func agentOfBuild(t *testing.T, a *app, build string) {
	t.Helper()
	serveSocket(t, a, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if build == noBuildMark {
			w.Header().Del(control.BuildHeader)
		} else {
			w.Header().Set(control.BuildHeader, build)
		}
		http.NotFound(w, r)
	}))
}

// An older copy of VaultSync, started by its path beside a newer VaultSync
// for Mac whose run — in the instant between the copy's start and its read
// of itself — put its own build in the copy's place and recorded its newer
// engine. The newer service runs, syncs and says which build it is; start
// must not take it for this older copy and say that it does not sync (the
// person would delete in that belief, and the deletions reach every device).
func TestIssue416_AnOlderCopyDoesNotTakeTheNewerServiceForItself(t *testing.T) {
	run := &fakeRunner{}
	a := installedApp(t, "darwin", run)
	app := fakeMacApp(t, filepath.Join(a.home, "Applications"), appBundleName, shippedInfoPlist(t))
	appAgent := filepath.Join(app, "Contents", "MacOS", "vaultsync")
	writeFile(t, appAgent, "a newer VaultSync for Mac")
	data, err := a.svc.unitFile(appAgent)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, mustUnitPath(t, a.svc), string(data))
	// This program starts as an older copy of VaultSync…
	runsAs(t, a, a.lay.Agent, "an older copy of VaultSync")
	// …and before it reads itself, the newer app's run puts its own build
	// in the copy's place (keepCopyAtAppBuild) with its engine recorded.
	writeFile(t, a.lay.Agent, "a newer VaultSync for Mac")
	newerEngineHere(t, a.lay)
	agentOfBuild(t, a, "a newer VaultSync for Mac")
	err = a.startService()
	out := startText(a)
	if err != nil || strings.Contains(out, "does not sync") || strings.Contains(refusalText(err), "does not sync") {
		t.Fatalf("an older copy took the newer service for itself: %v\n%s", err, out)
	}
	if !strings.Contains(out, "runs another VaultSync build than this vaultsync") || !strings.Contains(out, "Note: this VaultSync is older") {
		t.Fatalf("start beside the newer service:\n%s", out)
	}
}

// A service whose run lives on after its program was replaced — VaultSync's
// own copy replaced outside setup's own restart (by hand, or a setup that
// stopped before it), on Linux or on a Mac without the app, which no watch
// ends; or VaultSync for Mac replaced in the seconds before its run's watch
// sees it. start leaves the living run alone (decision 085), and the run is
// the build it was started with, which says so: start must not call it this
// build, "already running", because the file at its path is this build now.
// It says that another build runs, and nothing more of how to replace it.
func TestIssue416_StartDoesNotTakeTheOldRunOfAReplacedProgramForThisBuild(t *testing.T) {
	for name, arrange := range map[string]func(t *testing.T) (*app, *fakeRunner){
		"linux, VaultSync's own copy replaced": func(t *testing.T) (*app, *fakeRunner) {
			a, run := linuxServiceOfThisBuild(t)
			run.unitActive = true
			return a, run
		},
		"a Mac, VaultSync's own copy replaced": darwinServiceOfThisBuild,
		"a Mac, VaultSync for Mac replaced": func(t *testing.T) (*app, *fakeRunner) {
			run := &fakeRunner{}
			a := installedApp(t, "darwin", run)
			app := fakeMacApp(t, filepath.Join(a.home, "Applications"), appBundleName, shippedInfoPlist(t))
			appAgent := filepath.Join(app, "Contents", "MacOS", "vaultsync")
			data, err := a.svc.unitFile(appAgent)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, mustUnitPath(t, a.svc), string(data))
			// The vaultsync command leads into the app, which is this
			// build now; launchd loaded its job from there before.
			runsAs(t, a, appAgent, "this build")
			run.loaded, run.loadedProgram = true, appAgent
			return a, run
		},
	} {
		t.Run(name, func(t *testing.T) {
			a, run := arrange(t)
			holdEngineLock(t, a)
			agentOfBuild(t, a, "the build the run was started with")
			if err := a.startService(); err != nil {
				t.Fatalf("start: %v", err)
			}
			out := startText(a)
			if strings.Contains(out, "already running") || strings.Contains(out, "running again") || !strings.Contains(out, "✓ The background service is running. It runs another VaultSync build than this vaultsync — vaultsync status says whether it syncs.") {
				t.Fatalf("start took the old run of a replaced program for this build:\n%s", out)
			}
			if strings.Contains(out, "vaultsync stop") {
				t.Fatalf("start names a restart for a run it leaves alone:\n%s", out)
			}
			// Left alone, as decision 085 has it: not loaded afresh.
			calls := strings.Join(run.calls, "\n")
			if strings.Contains(calls, "launchctl bootout") || strings.Contains(calls, "launchctl bootstrap") || strings.Contains(calls, "restart") {
				t.Fatalf("start restarted a living run:\n%s", calls)
			}
		})
	}
}

// status names vaultsync stop, then vaultsync start, as what installs this
// build's pin only where the background service runs this build (#267,
// decision 078). In the instant between this program's start and its read
// of itself, a setup of another build put that build in the copy's place
// and started the service with it: status must not take that service for
// this build, whose pin its next start would then be said to try.
func TestIssue416_StatusDoesNotTakeAnotherBuildsServiceForThisBuild(t *testing.T) {
	a := keptEngineApp(t)
	runsAs(t, a, a.lay.Agent, "this very build")
	writeFile(t, a.lay.Agent, "the build whose setup replaced it")
	agentOfBuild(t, a, "the build whose setup replaced it")
	out := statusText(t, a)
	if strings.Contains(out, "tries to install at its next start") || !strings.Contains(out, "which vaultsync setup installs") {
		t.Fatalf("status took another build's service for this build:\n%s", out)
	}
}

// keptEngineApp is a Linux computer whose service runs, with an older engine
// recorded than this build pins, the very one installed (#267).
func keptEngineApp(t *testing.T) *app {
	t.Helper()
	a := installedApp(t, "linux", &fakeRunner{answers: map[string]string{"systemctl --user is-active vaultsync.service": "active"}})
	sum, err := fileSHA256(a.lay.Syncthing)
	if err != nil {
		t.Fatal(err)
	}
	if err := updateState(a.lay.State, func(s *agentState) { s.Syncthing.Version, s.Syncthing.BinarySHA256 = "v2.1.6", sum }); err != nil {
		t.Fatal(err)
	}
	return a
}

// Without an answer, status cannot tell which build runs, and names what
// installs the pin in every case: setup with this vaultsync — never stop,
// then start, on the strength of the file at the service's path.
func TestIssue416_StatusWithoutAnAnswerNamesSetup(t *testing.T) {
	a := keptEngineApp(t)
	runsAs(t, a, a.lay.Agent, "this very build")
	out := statusText(t, a)
	if strings.Contains(out, "tries to install at its next start") || !strings.Contains(out, "which vaultsync setup installs") {
		t.Fatalf("status without an answer:\n%s", out)
	}
}

// The running agent says its build on every answer — the 404 start asks
// for, the status, and an error — so start and status learn it without an
// ask of their own; a build without a mark says none (decision 093).
func TestIssue416_TheAgentSaysItsBuildOnEveryAnswer(t *testing.T) {
	eng := newFakeEngine(t)
	a := agentApp(t, eng)
	a.build = "0.9.9"
	client := serveControl(t, a)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	build, err := client.Ask(ctx, http.MethodGet, startAskPath, nil, nil)
	if build != "0.9.9" || !errors.Is(err, errNoAgent) {
		t.Fatalf("the 404 for start's ask: %q %v", build, err)
	}
	var cs controlStatus
	if build, err = client.Ask(ctx, http.MethodGet, "/v1/status", nil, &cs); build != "0.9.9" || err != nil {
		t.Fatalf("the status: %q %v", build, err)
	}
	var said *control.AnswerError
	if build, err = client.Ask(ctx, http.MethodGet, "/v1/pause", nil, nil); build != "0.9.9" || !errors.As(err, &said) {
		t.Fatalf("an error answer: %q %v", build, err)
	}

	b := agentApp(t, newFakeEngine(t))
	b.build = noBuildMark
	if build, err := serveControl(t, b).Ask(ctx, http.MethodGet, startAskPath, nil, nil); build != "" || !errors.Is(err, errNoAgent) {
		t.Fatalf("a build without a mark: %q %v", build, err)
	}
}

// start against the control server's own handler: the agent of this very
// build is this build; one of another build is another build.
func TestIssue416_StartHearsTheRealAgentsBuild(t *testing.T) {
	quickAnswerWait(t)
	for _, c := range []struct {
		service, said string
	}{
		{"this build", "✓ VaultSync is running again."},
		{"another build", "✓ The background service is running. It runs another VaultSync build than this vaultsync — vaultsync status says whether it syncs."},
	} {
		a, _ := linuxServiceOfThisBuild(t)
		agent := *a
		agent.build = c.service
		agent.out = &bytes.Buffer{}
		serveSocket(t, a, (&controlServer{a: &agent, eng: agent.engine(), logf: t.Logf}).handler())
		if err := a.startService(); err != nil || !strings.Contains(startText(a), c.said) {
			t.Fatalf("start beside an agent of %s: %v\n%s", c.service, err, startText(a))
		}
	}
}

// An agent from before builds said their mark answers without one: beside
// this build, which has one, that is another build — never "running again"
// on the strength of the file at the service's path, which is this build.
func TestIssue416_AnAnswerWithoutAMarkIsAnotherBuild(t *testing.T) {
	a, _ := linuxServiceOfThisBuild(t)
	agent := newFakeAgent(t)
	agent.build = noBuildMark
	agent.recordedFor(t, a)
	if err := a.startService(); err != nil {
		t.Fatal(err)
	}
	if out := startText(a); !strings.Contains(out, "runs another VaultSync build than this vaultsync") || strings.Contains(out, "running again") {
		t.Fatalf("start beside an agent without a mark:\n%s", out)
	}
}

// A build without a mark (a development build from a changed checkout, a
// test binary) knows no build to compare: beside an agent without one start
// says the service runs and names no build; beside one with a mark, that is
// another build.
func TestIssue416_ABuildWithoutAMarkNamesNoBuild(t *testing.T) {
	a, _ := linuxServiceOfThisBuild(t)
	a.build = noBuildMark
	agent := newFakeAgent(t)
	agent.build = noBuildMark
	agent.recordedFor(t, a)
	if err := a.startService(); err != nil {
		t.Fatal(err)
	}
	if out := startText(a); !strings.Contains(out, "✓ The background service is running. vaultsync status says whether it syncs.") || strings.Contains(out, "another VaultSync build") || strings.Contains(out, "running again") {
		t.Fatalf("start of a build without a mark:\n%s", out)
	}

	a, _ = linuxServiceOfThisBuild(t)
	a.build = noBuildMark
	agentOfBuild(t, a, "0.9.9")
	if err := a.startService(); err != nil {
		t.Fatal(err)
	}
	if out := startText(a); !strings.Contains(out, "runs another VaultSync build than this vaultsync") {
		t.Fatalf("start of a build without a mark beside one with a mark:\n%s", out)
	}
}

// recordedFor records the fake agent's socket in a's agent.json.
func (f *fakeAgent) recordedFor(t *testing.T, a *app) {
	t.Helper()
	recordSocket(t, a, f.path)
}

// What a build's mark is: the version it was stamped with; for one stamped
// with none, the commit of a clean checkout; nothing otherwise. A release's
// build information alone names no release (decision 093, measured on the
// desktop-v0.3.0 assets), so the stamp decides.
func TestIssue416_WhatABuildsMarkIs(t *testing.T) {
	vcs := func(rev, modified string) *debug.BuildInfo {
		info := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "-trimpath", Value: "true"}, {Key: "vcs", Value: "git"}}}
		if rev != "" {
			info.Settings = append(info.Settings, debug.BuildSetting{Key: "vcs.revision", Value: rev})
		}
		if modified != "" {
			info.Settings = append(info.Settings, debug.BuildSetting{Key: "vcs.modified", Value: modified})
		}
		return info
	}
	release := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "-trimpath", Value: "true"}, {Key: "CGO_ENABLED", Value: "0"}}}
	for _, c := range []struct {
		version string
		info    *debug.BuildInfo
		want    string
	}{
		{"0.3.0", release, "0.3.0"},
		{"0.3.0-rc.1", release, "0.3.0-rc.1"},
		{"dev-eed0faf", release, "dev-eed0faf"},
		{"dev", vcs("eed0fafc7c34414f1a607d4da394a455c89196b3", "false"), "dev+eed0fafc7c34414f1a607d4da394a455c89196b3"},
		{"dev", vcs("eed0fafc7c34414f1a607d4da394a455c89196b3", "true"), ""},
		{"dev", vcs("eed0fafc7c34414f1a607d4da394a455c89196b3", ""), ""},
		{"dev", release, ""},
		{"dev", nil, ""},
		{"", release, ""},
	} {
		if got := buildMarkOf(c.version, c.info); got != c.want {
			t.Errorf("buildMarkOf(%q) = %q, want %q", c.version, got, c.want)
		}
	}
	for mark, want := range map[string]string{"0.3.0": "0.3.0", "dev+abc": "dev+abc", "0.3 .0": "", "0.3.0\n": "", "": ""} {
		if got := headerSafe(mark); got != want {
			t.Errorf("headerSafe(%q) = %q, want %q", mark, got, want)
		}
	}
	// This test binary is stamped with nothing and built without vcs: no
	// mark, so it is one build with nothing.
	if got := buildMark(); got != "" {
		t.Errorf("a test binary's mark: %q", got)
	}
}

// The default every test relies on: an app nothing marked is this build, and
// so is a fake agent and the control server of such an app — start from one
// such app beside the other's agent is "running again", as a test written
// before marks existed expects (decision 093).
func TestIssue416_AnAppNothingMarkedIsThisBuild(t *testing.T) {
	quickAnswerWait(t)
	byHand := &app{}
	if byHand.mark() != defaultBuildMark || defaultBuildMark == "" {
		t.Fatalf("an app built by hand carries %q", byHand.mark())
	}
	none := &app{build: noBuildMark}
	if none.mark() != "" {
		t.Fatalf("an app of a build without a mark carries %q", none.mark())
	}
	a := installedApp(t, "linux", &fakeRunner{systemd: true})
	service := &app{goos: a.goos, home: a.home, getenv: a.getenv, lay: a.lay, svc: a.svc, out: &bytes.Buffer{}}
	serveSocket(t, a, (&controlServer{a: service, eng: service.engine(), logf: t.Logf}).handler())
	if err := a.startService(); err != nil || !strings.Contains(startText(a), "✓ VaultSync is running again.") {
		t.Fatalf("start beside an agent of an app nothing marked: %v\n%s", err, startText(a))
	}
	a = installedApp(t, "linux", &fakeRunner{systemd: true})
	answeringAgent(t, a)
	if err := a.startService(); err != nil || !strings.Contains(startText(a), "✓ VaultSync is running again.") {
		t.Fatalf("start beside a fake agent: %v\n%s", err, startText(a))
	}
}

// An older copy refused at pair, whose file the newer build's setup
// replaced in the instant before it read itself, while the service runs
// that newer build and syncs: the refusal judged the service's build by the
// files and said that nothing syncs. A parked service never answers, so no
// look tells whether it runs this build: the refusal says what holds if it
// does, and claims nothing (decision 093, decision 091).
func TestIssue416_AnOlderRefusalClaimsNothingOfTheServicesBuild(t *testing.T) {
	a := installedApp(t, "linux", &fakeRunner{})
	newerEngineHere(t, a.lay)
	runsAs(t, a, a.lay.Agent, "the older copy")
	writeFile(t, a.lay.Agent, "the newer build its setup put in place")
	got := refusalText(a.pair(context.Background(), nil))
	if !strings.Contains(got, "this VaultSync is older") || strings.Contains(got, "Nothing syncs here meanwhile") {
		t.Fatalf("an older refusal judged the service's build by files:\n%s", got)
	}
	if !strings.Contains(got, "Where the background service runs this VaultSync too, nothing syncs until the setup link brings the current one to it.") {
		t.Fatalf("an older refusal does not say what holds if the service runs this build:\n%s", got)
	}
}

// The current build's start beside an older service that parks and never
// answers: the advice must end somewhere. stop, then start, runs the same
// older program again; the setup link brings the current build to the
// service (054). It still says the service may be installing its engine,
// which a slow first start does.
func TestIssue416_BesideAParkedOlderServiceStartNamesAWayThatEnds(t *testing.T) {
	a, _ := linuxServiceOfThisBuild(t)
	quickAnswerWait(t)
	writeFile(t, a.lay.Agent, "an older build the service runs, which parks")
	runsAs(t, a, filepath.Join(t.TempDir(), "vaultsync"), "the current build")
	if err := a.startService(); err != nil {
		t.Fatal(err)
	}
	want := "VaultSync's background service runs, but it does not answer yet — it may still be installing its sync engine. vaultsync status says what it does. If it stays so, run vaultsync stop, then vaultsync start; where that changes nothing, run the setup link, which brings the current VaultSync to the service (its log: journalctl --user -u vaultsync).\n"
	if out := startText(a); out != want {
		t.Fatalf("start beside a parked older service:\n%s", out)
	}
}

// A run of this very build that answers while what is set up has become
// newer passed its own check when it started, so it runs: start says so,
// and the note names the way out, the setup link — a run of this build
// parks at its next start (054). Before #416 this was the refusal "runs,
// but it does not sync", the riskier claim of a run that syncs.
func TestIssue416_ThisBuildAnsweringWhileOlderRunsAndNamesTheWayOut(t *testing.T) {
	a, _ := linuxServiceOfThisBuild(t)
	answeringAgent(t, a)
	newerEngineHere(t, a.lay)
	if err := a.startService(); err != nil {
		t.Fatalf("start: %v", err)
	}
	out := startText(a)
	if !strings.HasPrefix(out, "✓ VaultSync is running again.\nNote: this VaultSync is older than the one set up on this computer") || !strings.HasSuffix(out, " Run the setup link to get the current VaultSync.\n") || strings.Contains(out, "does not sync") {
		t.Fatalf("start beside a run of this build that is older than what is set up:\n%s", out)
	}
}

// A newer run records its engine as it comes up, during start's wait: start
// reads what is set up after the wait, so the older note is there beside
// the newer service's answer.
func TestIssue416_StartReadsWhatIsSetUpAfterTheWait(t *testing.T) {
	a, _ := linuxServiceOfThisBuild(t)
	sum, err := fileSHA256(a.lay.Syncthing)
	if err != nil {
		t.Fatal(err)
	}
	serveSocket(t, a, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := updateState(a.lay.State, func(s *agentState) { s.Syncthing.Version, s.Syncthing.BinarySHA256 = "v9.0.0", sum }); err != nil {
			t.Error(err)
		}
		w.Header().Set(control.BuildHeader, "a newer build")
		http.NotFound(w, r)
	}))
	if err := a.startService(); err != nil {
		t.Fatal(err)
	}
	if out := startText(a); !strings.Contains(out, "runs another VaultSync build than this vaultsync") || !strings.Contains(out, "Note: this VaultSync is older") {
		t.Fatalf("start read what is set up before the newer run recorded it:\n%s", out)
	}
}

// status's one ask for the service's build ends with status's own context —
// Ctrl-C, or a caller's deadline — not only after its 3 s.
func TestIssue416_StatusAsksTheBuildWithinItsContext(t *testing.T) {
	a, _ := linuxServiceOfThisBuild(t)
	path := filepath.Join(shortDir(t), "agent.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	held := make(chan net.Conn, 8) // takes the request, never answers
	t.Cleanup(func() {
		l.Close()
		for {
			select {
			case c := <-held:
				c.Close()
			default:
				return
			}
		}
	})
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			held <- c
		}
	}()
	recordSocket(t, a, path)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	began := time.Now()
	if a.serviceSaysThisBuild(ctx) {
		t.Fatal("a silent socket said this build")
	}
	if took := time.Since(began); took > time.Second {
		t.Fatalf("the ask outlived its context by %v", took)
	}
}
