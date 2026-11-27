package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A copy of VaultSync started by its path beside a newer VaultSync for Mac
// — whose run, the moment the service starts, puts its own build in the
// copy's place and installs a newer engine — is judged before the start:
// the service runs another build than this one, and start says so, not
// "does not sync", which the newer service belies (#290).
func TestIssue290_StartJudgesTheBuildBeforeTheServiceRuns(t *testing.T) {
	run := &fakeRunner{}
	a := installedApp(t, "darwin", run)
	app := fakeMacApp(t, filepath.Join(a.home, "Applications"), appBundleName, shippedInfoPlist(t))
	appAgent := filepath.Join(app, "Contents", "MacOS", "vaultsync")
	data, err := a.svc.unitFile(appAgent)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, mustUnitPath(t, a.svc), string(data))
	runsAs(t, a, a.lay.Agent, "an older copy of VaultSync")
	replaced := false
	run.onCall = func(call string) {
		if !strings.Contains(call, "bootstrap") {
			return
		}
		replaced = true
		// The newer app's run as it comes up: its build in the copy's
		// place (keepCopyAtAppBuild), and its engine recorded.
		build, err := os.ReadFile(appAgent)
		if err != nil {
			t.Error(err)
			return
		}
		writeFile(t, a.lay.Agent, string(build))
		newerEngineHere(t, a.lay)
	}
	err = a.startService()
	if !replaced {
		t.Fatal("start loaded no job: the newer run never came up, so this test tests nothing")
	}
	out := a.out.(*bytes.Buffer).String()
	if err != nil || strings.Contains(out, "does not sync") {
		t.Fatalf("a stale copy judged the newer service by the copy that service had just replaced: %v\n%s", err, out)
	}
	if !strings.Contains(out, "runs another VaultSync build than this vaultsync") || strings.Contains(out, "Nothing was changed") {
		t.Fatalf("start beside a newer app: %v\n%s", err, out)
	}
}

// VaultSync for Mac replaced while start loads its job — dragged over in
// the Finder, or updated by the setup link — runs the newer build launchd
// finds at the path then: start, whose look before the start saw this very
// build, says only that another build runs, not "does not sync" (#290).
func TestIssue290_StartTakesThisBuildOnlyWhereBothLooksAgree(t *testing.T) {
	run := &fakeRunner{}
	a := installedApp(t, "darwin", run)
	app := fakeMacApp(t, filepath.Join(a.home, "Applications"), appBundleName, shippedInfoPlist(t))
	appAgent := filepath.Join(app, "Contents", "MacOS", "vaultsync")
	data, err := a.svc.unitFile(appAgent)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, mustUnitPath(t, a.svc), string(data))
	build, err := os.ReadFile(appAgent)
	if err != nil {
		t.Fatal(err)
	}
	runsAs(t, a, a.lay.Agent, string(build)) // the copy at the app's build
	replaced := false
	run.onCall = func(call string) {
		if !strings.Contains(call, "bootstrap") {
			return
		}
		replaced = true
		writeFile(t, appAgent, "a newer VaultSync for Mac")
		newerEngineHere(t, a.lay)
	}
	err = a.startService()
	if !replaced {
		t.Fatal("start loaded no job: the app was never replaced, so this test tests nothing")
	}
	out := a.out.(*bytes.Buffer).String()
	if err != nil || strings.Contains(out, "does not sync") || !strings.Contains(out, "runs another VaultSync build than this vaultsync") {
		t.Fatalf("start while the app was replaced: %v\n%s", err, out)
	}
}

// VaultSync for Mac replaced while start loads its job, and its run putting
// its own build in the copy's place as it comes up: the look after the start
// compares the service with this build as it was when start began, not with
// the file at its path, which is the newer build by then (#290).
func TestIssue290_StartJudgesBothLooksAgainstThisBuildAsItWas(t *testing.T) {
	run := &fakeRunner{}
	a := installedApp(t, "darwin", run)
	app := fakeMacApp(t, filepath.Join(a.home, "Applications"), appBundleName, shippedInfoPlist(t))
	appAgent := filepath.Join(app, "Contents", "MacOS", "vaultsync")
	data, err := a.svc.unitFile(appAgent)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, mustUnitPath(t, a.svc), string(data))
	build, err := os.ReadFile(appAgent)
	if err != nil {
		t.Fatal(err)
	}
	runsAs(t, a, a.lay.Agent, string(build)) // the copy at the app's build
	replaced := false
	run.onCall = func(call string) {
		if !strings.Contains(call, "bootstrap") {
			return
		}
		replaced = true
		writeFile(t, appAgent, "a newer VaultSync for Mac")
		writeFile(t, a.lay.Agent, "a newer VaultSync for Mac")
		newerEngineHere(t, a.lay)
	}
	err = a.startService()
	if !replaced {
		t.Fatal("start loaded no job: nothing was replaced, so this test tests nothing")
	}
	out := a.out.(*bytes.Buffer).String()
	if err != nil || strings.Contains(out, "does not sync") || !strings.Contains(out, "runs another VaultSync build than this vaultsync") {
		t.Fatalf("start while the app and the copy were replaced: %v\n%s", err, out)
	}
}

// A service whose program becomes this very build only while start loads its
// job — VaultSync for Mac replaced by this build meanwhile — is not taken
// for this build either: the looks before and after the start differ, and
// start says only that another build runs (#290).
func TestIssue290_StartSaysAnotherBuildWhereTheLooksDiffer(t *testing.T) {
	run := &fakeRunner{}
	a := installedApp(t, "darwin", run)
	app := fakeMacApp(t, filepath.Join(a.home, "Applications"), appBundleName, shippedInfoPlist(t))
	appAgent := filepath.Join(app, "Contents", "MacOS", "vaultsync")
	data, err := a.svc.unitFile(appAgent)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, mustUnitPath(t, a.svc), string(data))
	runsAs(t, a, a.lay.Agent, "this very build")
	replaced := false
	run.onCall = func(call string) {
		if !strings.Contains(call, "bootstrap") {
			return
		}
		replaced = true
		writeFile(t, appAgent, "this very build")
		newerEngineHere(t, a.lay)
	}
	err = a.startService()
	if !replaced {
		t.Fatal("start loaded no job: the app was never replaced, so this test tests nothing")
	}
	out := a.out.(*bytes.Buffer).String()
	if err != nil || strings.Contains(out, "does not sync") || !strings.Contains(out, "runs another VaultSync build than this vaultsync") {
		t.Fatalf("start where the looks differ: %v\n%s", err, out)
	}
}
