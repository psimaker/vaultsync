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

func statusText(t *testing.T, a *app) string {
	t.Helper()
	a.out = &bytes.Buffer{}
	if err := a.status(context.Background()); err != nil {
		t.Fatal(err)
	}
	return a.out.(*bytes.Buffer).String()
}

// A service file without a session to run in — a Mac reached over SSH before
// anyone logged in at the screen, a Linux account whose user manager is not
// running — is not "stopped": status says it waits for the login, names
// lingering on Linux, and does not promise a start on its own (a service
// stopped with vaultsync stop stays disabled across the login). A stopped
// service in a live session still points at start; a running one is running.
func TestIssue232_StatusSaysTheServiceWaitsForLogin(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			run := &fakeRunner{noSession: true}
			a := installedApp(t, goos, run)
			out := statusText(t, a)
			if !strings.Contains(out, "Background service: waits for your login") || strings.Contains(out, "stopped —") || strings.Contains(out, "on its own") {
				t.Fatalf("status without a session:\n%s", out)
			}
			if !strings.Contains(out, "Stopped it with vaultsync stop? Then run vaultsync start once you are logged in.") {
				t.Fatalf("status must not promise a start on its own:\n%s", out)
			}
			if strings.Contains(out, "loginctl enable-linger") != (goos == "linux") {
				t.Fatalf("lingering is a Linux remedy:\n%s", out)
			}
			if !strings.Contains(out, "Sync engine: not running") {
				t.Fatalf("the engine is not running without the service:\n%s", out)
			}
			if goos == "darwin" && !contains(run.calls, "launchctl print gui/501") {
				t.Fatalf("the probe asks launchd for the domain itself: %v", run.calls)
			}

			run.noSession = false
			if out := statusText(t, a); !strings.Contains(out, "Background service: stopped — vaultsync start resumes it") {
				t.Fatalf("status with a session and a stopped service:\n%s", out)
			}

			run.calls = nil
			if _, err := a.svc.install(a.svc.lay.Agent, true); err != nil {
				t.Fatal(err)
			}
			run.answers = map[string]string{"systemctl --user is-active vaultsync.service": "active"}
			run.calls = nil
			if out := statusText(t, a); !strings.Contains(out, "Background service: running") {
				t.Fatalf("status with a running service:\n%s", out)
			}
			if contains(run.calls, "launchctl print gui/501") {
				t.Fatalf("a running service needs no session probe: %v", run.calls)
			}
		})
	}
}

// start without a session says the same — after a vaultsync stop it must be
// run again once logged in, so the message asks for that instead of the
// service manager's error. With a session, a failing service manager keeps
// its own words.
func TestIssue232_StartSaysTheServiceWaitsForLogin(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			run := &fakeRunner{}
			a := installedApp(t, goos, run)
			// Stopped in a session, then the session ends: the service stays
			// disabled, so a later login does not start it.
			if err := a.svc.stop(); err != nil {
				t.Fatal(err)
			}
			run.noSession = true
			err := a.startService()
			var r *refusal
			if !errors.As(err, &r) || !strings.Contains(r.msg, "then run vaultsync start again") || strings.Contains(r.msg, "on its own") {
				t.Fatalf("start without a session: %v", err)
			}
			if strings.Contains(r.msg, "launchctl") || strings.Contains(r.msg, "systemctl") || strings.Contains(r.msg, "Could not find domain") || strings.Contains(r.msg, "connect to bus") {
				t.Fatalf("the service manager's error is not the message: %v", err)
			}
			if strings.Contains(r.msg, "loginctl enable-linger") != (goos == "linux") {
				t.Fatalf("lingering is a Linux remedy: %v", err)
			}

			run.noSession = false
			run.fail = map[string]error{"launchctl enable": errors.New("exit status 1"), "systemctl --user enable": errors.New("exit status 1")}
			err = a.startService()
			if err == nil || errors.As(err, &r) || !strings.Contains(err.Error(), "enable") || !strings.Contains(err.Error(), "boom") {
				t.Fatalf("start with a session and a failing service manager keeps its words: %v", err)
			}
		})
	}
}

// A probe that fails for another reason — a shell without the session's
// environment, a permission — is no evidence of a logout: status says the
// manager could not be asked and keeps its words, start keeps its error.
func TestIssue232_ProbeFailureIsNotALogout(t *testing.T) {
	for goos, words := range map[string]string{
		"darwin": "Could not print domain: 1: Operation not permitted",
		"linux":  "Failed to connect to bus: $DBUS_SESSION_BUS_ADDRESS and $XDG_RUNTIME_DIR not defined",
	} {
		t.Run(goos, func(t *testing.T) {
			run := &fakeRunner{probeFails: words}
			a := installedApp(t, goos, run)
			out := statusText(t, a)
			if !strings.Contains(out, "Background service: unknown — the service manager could not be asked from here ("+words+")") {
				t.Fatalf("status with a failing probe:\n%s", out)
			}
			if strings.Contains(out, "waits for your login") || strings.Contains(out, "stopped —") {
				t.Fatalf("a failing probe is neither a logout nor a stop:\n%s", out)
			}
			run.fail = map[string]error{"launchctl enable": errors.New("exit status 1"), "systemctl --user enable": errors.New("exit status 1")}
			err := a.startService()
			var r *refusal
			if err == nil || errors.As(err, &r) || !strings.Contains(err.Error(), "enable") || !strings.Contains(err.Error(), "boom") {
				t.Fatalf("start with a failing probe keeps the manager's error: %v", err)
			}
		})
	}
}

// The probe asks about this user, not a fixed one: launchd's domain by uid,
// the systemd user manager by its runtime directory.
func TestIssue232_ProbeAsksAboutThisUser(t *testing.T) {
	run := &fakeRunner{noSession: true}
	a := installedApp(t, "darwin", run)
	a.svc.uid = 502
	if out := statusText(t, a); !strings.Contains(out, "waits for your login") || !contains(run.calls, "launchctl print gui/502") {
		t.Fatalf("darwin probe for uid 502: %v\n%s", run.calls, out)
	}

	run = &fakeRunner{noSession: true}
	a = installedApp(t, "linux", run)
	a.svc.uid = 502
	var asked []string
	a.svc.exists = func(p string) bool { asked = append(asked, p); return false }
	if out := statusText(t, a); !strings.Contains(out, "waits for your login") || !contains(asked, filepath.Join("/run", "user", "502")) {
		t.Fatalf("linux probe for uid 502: asked %v\n%s", asked, out)
	}
	// The directory there while systemctl cannot connect: the shell lacks
	// the environment, the manager may well be running.
	a.svc.exists = func(string) bool { return true }
	if out := statusText(t, a); !strings.Contains(out, "Background service: unknown") {
		t.Fatalf("runtime directory present, bus unreachable:\n%s", out)
	}
}
