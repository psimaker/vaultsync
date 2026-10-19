package main

import (
	"context"
	"testing"

	"github.com/psimaker/vaultsync/desktop/tray"
)

// The menu-bar app reads the agent's answers through the shared types
// (internal/control) and finds the agent where the shared layout puts it;
// what is left to keep in step is the socket's place and the socket itself.

func TestIssue176_TheTrayLooksWhereTheAgentIs(t *testing.T) {
	// The socket: the usual place, or the one agent.json records — the
	// same the terminal's own client dials. (Where VaultSync's folder and
	// the log are is the shared layout's word, internal/layout.)
	lay, err := layoutFor("linux", t.TempDir(), envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	a := &app{goos: "linux", lay: lay}
	for _, recorded := range []string{"", "/run/user/1000/vaultsync-abc/agent.sock"} {
		st := agentState{GUIPort: 8384, ControlSocket: recorded}
		if err := saveState(lay.State, st); err != nil {
			t.Fatal(err)
		}
		got, err := tray.SocketPath(lay)
		if err != nil || got != a.controlClient(st).Path() {
			t.Fatalf("recorded %q: %q vs %q (%v)", recorded, got, a.controlClient(st).Path(), err)
		}
	}

	// And through a real agent's socket: the menu-bar app's client reads
	// the report the terminal prints, and its pause lands in the engine.
	eng := newFakeEngine(t)
	hubDevice(eng)
	agent := agentApp(t, eng)
	serveControl(t, agent)
	ctx := context.Background()
	c := tray.NewClient(agent.lay.Socket)
	ts, err := c.Status(ctx)
	if err != nil || ts.Engine != "running" || len(ts.Hubs) != 1 || ts.Hubs[0].Name != "Test Hub" || len(ts.Vaults) != 1 || ts.Vaults[0].Label != "Notes" || ts.Vaults[0].Path != "/home/me/Notes" {
		t.Fatalf("status through the real socket: %+v %v", ts, err)
	}
	if rep, err := agent.report(ctx, eng.client, false); err != nil || rep.Text != ts.Text {
		t.Fatalf("text differs:\n%q\n%q (%v)", rep.Text, ts.Text, err)
	}
	if res, err := c.SetPaused(ctx, true); err != nil || !res.Paused || res.Hubs != 1 {
		t.Fatalf("pause: %+v %v", res, err)
	}
	if paused, hubs := pausedDevices(eng); paused != 1 || hubs != 1 {
		t.Fatalf("the engine: %d of %d paused", paused, hubs)
	}
	if ts, err := c.Status(ctx); err != nil || !ts.Paused || ts.Vaults[0].State != "paused on this computer" {
		t.Fatalf("status while paused: %+v %v", ts, err)
	}
	if res, err := c.SetPaused(ctx, false); err != nil || res.Paused {
		t.Fatalf("resume: %+v %v", res, err)
	}
	// Hub named, so no search of the network: the first thing the flow
	// asks for is the code.
	if pr, err := c.Pair(ctx, tray.PairRequest{Hub: "127.0.0.1:1"}); err != nil || pr.OK || pr.Needs != "code" {
		t.Fatalf("pair without a code through the real socket: %+v %v", pr, err)
	}
}
