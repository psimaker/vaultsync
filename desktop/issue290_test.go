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
	run.onCall = func(call string) {
		if !strings.Contains(call, "bootstrap") {
			return
		}
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
	out := a.out.(*bytes.Buffer).String()
	if strings.Contains(refusalText(err), "does not sync") || strings.Contains(out, "does not sync") {
		t.Fatalf("a stale copy judged the newer service by the copy that service had just replaced: %v\n%s", err, out)
	}
	if !strings.Contains(out, "runs another VaultSync build than this vaultsync") {
		t.Fatalf("start beside a newer app: %v\n%s", err, out)
	}
}
