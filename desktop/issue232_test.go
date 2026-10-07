package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// installedApp is an app whose engine is prepared (config.xml with the API
// address and key, agent.json) and whose service file is written, with the
// engine not running: nobody listens on its API port.
func installedApp(t *testing.T, goos string, run *fakeRunner) *app {
	t.Helper()
	svc, _ := testService(t, goos, run)
	port, err := freeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(svc.lay.Home, "config.xml"), "<configuration><gui><address>127.0.0.1:"+strconv.Itoa(port)+"</address><apikey>k</apikey></gui></configuration>\n")
	if err := saveState(svc.lay.State, agentState{GUIPort: port}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, mustUnitPath(t, svc), "installed\n")
	return &app{goos: goos, home: svc.home, getenv: envOf(nil), lay: svc.lay, svc: svc, out: &bytes.Buffer{}}
}

// A service file without a session to run in — a Mac reached over SSH before
// anyone logged in at the screen, Linux after logging out without lingering —
// is not "stopped": status says it waits for the login, and names lingering
// on Linux. A stopped service in a live session still points at start.
func TestIssue232_StatusSaysTheServiceWaitsForLogin(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			run := &fakeRunner{noSession: true}
			a := installedApp(t, goos, run)
			if err := a.status(context.Background()); err != nil {
				t.Fatal(err)
			}
			out := a.out.(*bytes.Buffer).String()
			if !strings.Contains(out, "Background service: waits for your login") || strings.Contains(out, "vaultsync start") {
				t.Fatalf("status without a session:\n%s", out)
			}
			if strings.Contains(out, "loginctl enable-linger") != (goos == "linux") {
				t.Fatalf("lingering is a Linux remedy:\n%s", out)
			}
			if !strings.Contains(out, "Sync engine: not running") {
				t.Fatalf("the engine is not running without the service:\n%s", out)
			}

			run.noSession = false
			a.out = &bytes.Buffer{}
			if err := a.status(context.Background()); err != nil {
				t.Fatal(err)
			}
			if out := a.out.(*bytes.Buffer).String(); !strings.Contains(out, "Background service: stopped — vaultsync start resumes it") {
				t.Fatalf("status with a session and a stopped service:\n%s", out)
			}
		})
	}
}

// start without a session says the same instead of the service manager's
// error; with a session, a failing service manager keeps its own error.
func TestIssue232_StartSaysTheServiceWaitsForLogin(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			run := &fakeRunner{noSession: true}
			a := installedApp(t, goos, run)
			err := a.startService()
			var r *refusal
			if !errors.As(err, &r) || !strings.Contains(strings.ToLower(r.msg), "log in") || strings.Contains(r.msg, "launchctl") || strings.Contains(r.msg, "systemctl") {
				t.Fatalf("start without a session: %v", err)
			}
			if strings.Contains(r.msg, "loginctl enable-linger") != (goos == "linux") {
				t.Fatalf("lingering is a Linux remedy: %v", err)
			}

			run.noSession = false
			run.fail = map[string]error{"launchctl enable": errors.New("exit status 1"), "systemctl --user enable": errors.New("exit status 1")}
			err = a.startService()
			if err == nil || errors.As(err, &r) || !strings.Contains(err.Error(), "enable") {
				t.Fatalf("start with a session and a failing service manager: %v", err)
			}
		})
	}
}
