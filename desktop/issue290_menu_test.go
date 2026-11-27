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

	"github.com/psimaker/vaultsync/desktop/internal/control"
	"github.com/psimaker/vaultsync/desktop/tray"
)

// #290, points 5 and 7: the menu-bar app's Start, seen from the agent's side.

// menuStartChild is the program the menu-bar app on Linux runs for its Start
// — VaultSync's copy in the folder setup used, which the service and the menu
// (started with --state-dir) read — as the operating system starts it: with
// the menu's environment, whose XDG_STATE_HOME names another place (setup ran
// in a shell that set it, the desktop session does not).
func menuStartChild(t *testing.T) (service, child *app) {
	t.Helper()
	a, _ := linuxServiceOfThisBuild(t)
	quickAnswerWait(t)
	answeringAgent(t, a)
	if err := os.Chmod(a.lay.Agent, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := tray.AgentProgram("linux", a.home, a.lay)(); got != a.lay.Agent {
		t.Fatalf("the menu would run %q, not the copy %q", got, a.lay.Agent)
	}
	env := envOf(map[string]string{"XDG_STATE_HOME": filepath.Join(t.TempDir(), "session-state")})
	lay, err := layoutFor("linux", a.home, env)
	if err != nil {
		t.Fatal(err)
	}
	child = &app{goos: "linux", home: a.home, getenv: env, lay: lay, svc: a.svc, out: &bytes.Buffer{}, appEnv: a.appEnv}
	child.svc.lay, child.svc.getenv = lay, env
	return a, child
}

// The menu-bar app on Linux, started with --state-dir in a session whose
// XDG_STATE_HOME names another place, ran a bare `start`, which took its
// folder from the environment — and start dispatched no argument at all. It
// read another agent.json than the menu and the service: no copy there to
// judge the service by, its whole answer wait on a socket nobody serves, and
// "runs another build … does not answer yet" while the service's agent
// answered (9.9 s on a real systemd, against 0.4 s). The menu now names its
// folder, and start takes it (#290, point 5).
func TestIssue290_TheMenusStartReadsTheFolderTheMenuReads(t *testing.T) {
	a, child := menuStartChild(t)
	args := tray.StartArgs(a.lay.Base)
	if len(args) == 0 || args[0] != "start" {
		t.Fatalf("the menu runs %q", args)
	}
	if err := child.startCommand(args[1:]); err != nil {
		t.Fatalf("the menu's start: %v", err)
	}
	if out := startText(child); !strings.Contains(out, "✓ VaultSync is running again.") {
		t.Fatalf("the menu's start read %s, not the folder the menu and the service use (%s):\n%s", child.lay.Base, a.lay.Base, out)
	}
	if child.lay.Base != a.lay.Base || child.svc.lay.Base != a.lay.Base {
		t.Fatalf("start's folder %q, its service's %q, the menu's %q", child.lay.Base, child.svc.lay.Base, a.lay.Base)
	}
}

// An older menu-bar app (desktop-v0.3.0) beside this agent runs a bare
// `start`: it takes its folder from the environment, as it always did. (A
// menu of this build beside a desktop-v0.2.0 or v0.3.0 agent gives a flag
// those ignore, as they ignore every argument after start: also as before.)
func TestIssue290_AnOlderMenusBareStartIsAsBefore(t *testing.T) {
	_, child := menuStartChild(t)
	if err := child.startCommand(nil); err != nil {
		t.Fatalf("a bare start: %v", err)
	}
	if out := startText(child); !strings.Contains(out, "It runs another VaultSync build than this vaultsync") {
		t.Fatalf("a bare start in a session that names another folder:\n%s", out)
	}
	if !strings.HasSuffix(child.lay.Base, filepath.Join("session-state", "vaultsync")) {
		t.Fatalf("a bare start took its folder from %q, not the environment", child.lay.Base)
	}
}

// start refuses what it does not know, as every other command does — until
// #290 it ignored every argument — and a folder that is not absolute.
func TestIssue290_StartRefusesStrayArguments(t *testing.T) {
	for _, args := range [][]string{{"now"}, {"--yes"}, {"--state-dir", "relative/vaultsync"}, {"--state-dir"}} {
		_, child := menuStartChild(t)
		if err := child.startCommand(args); err == nil {
			t.Fatalf("start %q was taken:\n%s", args, startText(child))
		}
		if out := startText(child); out != "" {
			t.Fatalf("start %q said something before its refusal:\n%s", args, out)
		}
	}
}

// A removal of VaultSync's files that stops once it has taken VaultSync's copy
// — something in bin it cannot remove — keeps the record and has already
// removed the service and the command link that led to the copy (#276). The
// menu-bar app opened afterwards finds VaultSync set up, nobody answering and
// nothing to start, and said "Run vaultsync start in a terminal": the command
// is gone, and a vaultsync still there to run it says the service is not
// installed (#290, point 7).
func TestIssue290_AStoppedRemovalLeavesTheMenuNothingToStart(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes what a permission keeps")
	}
	a, link := uninstallable(t)
	stuck := filepath.Join(a.lay.Bin, "stuck")
	writeFile(t, filepath.Join(stuck, "x"), "x")
	if err := os.Chmod(stuck, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stuck, 0o700) })
	if err := a.uninstall(context.Background(), []string{"--remove-data", "--yes"}); err == nil {
		t.Fatal("the removal did not stop at bin")
	}
	if _, err := os.Lstat(a.lay.Agent); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the removal did not take VaultSync's copy before it stopped (%v), so this test does not test the case", err)
	}
	if _, err := os.Lstat(link); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the command link stayed: %v", err)
	}
	if _, err := control.SocketPath(a.lay); err != nil {
		t.Fatalf("the menu does not find VaultSync set up (%v), so this test does not test the case", err)
	}
	if prog := tray.AgentProgram("linux", a.home, a.lay)(); prog != "" {
		t.Fatalf("the menu found a program to start: %q", prog)
	}
	a.out = &bytes.Buffer{}
	if err := a.startService(); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("start with no service: %v", err)
	}
	v := tray.Render(tray.Status{}, tray.ErrNoAgent, tray.Options{})
	if strings.Contains(v.Detail, "vaultsync start") {
		t.Fatalf("nothing can start the service, and the menu names vaultsync start: %q", v.Detail)
	}
}
