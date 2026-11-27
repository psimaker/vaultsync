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

// The menu-bar app on Linux, started with --state-dir naming the folder setup
// used, in a session whose XDG_STATE_HOME names another place (setup ran in a
// shell that set it, the desktop session does not): its Start runs VaultSync's
// copy in that folder with a bare `start`, which takes its folder from the
// environment, and dispatches no flag to start at all (#290, point 5). start
// then reads another agent.json than the menu and the service: it finds no
// copy there to judge the service by, waits its whole answer wait on a socket
// nobody serves, and says the service runs another build and does not answer,
// while the service's agent answers in the folder the menu reads.
func TestIssue290_TheMenusStartReadsTheFolderTheMenuReads(t *testing.T) {
	a, _ := linuxServiceOfThisBuild(t) // the folder setup used: the service's, and the menu's
	quickAnswerWait(t)
	answeringAgent(t, a)
	// The child the menu starts: VaultSync's copy in that folder (what
	// tray.AgentProgram names on Linux), with the menu's environment.
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
	child := &app{goos: "linux", home: a.home, getenv: env, lay: lay, svc: a.svc, out: &bytes.Buffer{}, appEnv: a.appEnv}
	child.svc.lay, child.svc.getenv = lay, env
	if err := child.startService(); err != nil {
		t.Fatalf("the menu's start: %v", err)
	}
	if out := startText(child); !strings.Contains(out, "✓ VaultSync is running again.") {
		t.Fatalf("the menu's start read %s, not the folder the menu and the service use (%s):\n%s", lay.Base, a.lay.Base, out)
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
