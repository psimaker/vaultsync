package main

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// #416: vaultsync start and status judged whether the background service
// runs this very build by the file at this program's path and the file at
// the service's, not by the programs that run. A file can be replaced while
// the program that was started from it runs on: in the instant between this
// process's start and its read of itself, and under a service whose run
// lives on after its program was replaced.

// buildHeader is the header a running agent says its build in on every
// answer of its control socket.
const buildHeader = "VaultSync-Build"

// agentOfBuild serves a control socket, at the place agent.json records,
// whose agent says it runs build and answers every request with the 404 an
// agent gives for a path it does not serve — the answer start waits for.
func agentOfBuild(t *testing.T, a *app, build string) {
	t.Helper()
	serveSocket(t, a, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(buildHeader, build)
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
	if !strings.Contains(out, "runs another VaultSync build than this vaultsync") {
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
func TestIssue416_StartDoesNotTakeTheOldRunOfAReplacedProgramForThisBuild(t *testing.T) {
	for name, arrange := range map[string]func(t *testing.T) *app{
		"linux, VaultSync's own copy replaced": func(t *testing.T) *app {
			a, run := linuxServiceOfThisBuild(t)
			run.unitActive = true
			return a
		},
		"a Mac, VaultSync's own copy replaced": func(t *testing.T) *app {
			a, _ := darwinServiceOfThisBuild(t)
			return a
		},
		"a Mac, VaultSync for Mac replaced": func(t *testing.T) *app {
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
			return a
		},
	} {
		t.Run(name, func(t *testing.T) {
			a := arrange(t)
			holdEngineLock(t, a)
			agentOfBuild(t, a, "the build the run was started with")
			if err := a.startService(); err != nil {
				t.Fatalf("start: %v", err)
			}
			out := startText(a)
			if strings.Contains(out, "already running") || strings.Contains(out, "running again") || !strings.Contains(out, "runs another VaultSync build than this vaultsync") {
				t.Fatalf("start took the old run of a replaced program for this build:\n%s", out)
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
	a := installedApp(t, "linux", &fakeRunner{answers: map[string]string{"systemctl --user is-active vaultsync.service": "active"}})
	sum, err := fileSHA256(a.lay.Syncthing)
	if err != nil {
		t.Fatal(err)
	}
	if err := updateState(a.lay.State, func(s *agentState) { s.Syncthing.Version, s.Syncthing.BinarySHA256 = "v2.1.6", sum }); err != nil {
		t.Fatal(err)
	}
	runsAs(t, a, a.lay.Agent, "this very build")
	writeFile(t, a.lay.Agent, "the build whose setup replaced it")
	agentOfBuild(t, a, "the build whose setup replaced it")
	out := statusText(t, a)
	if strings.Contains(out, "tries to install at its next start") || !strings.Contains(out, "which vaultsync setup installs") {
		t.Fatalf("status took another build's service for this build:\n%s", out)
	}
}
