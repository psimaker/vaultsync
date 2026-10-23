//go:build unix

package main

import (
	"context"
	"strings"
	"testing"

	"github.com/psimaker/vaultsync/desktop/internal/control"
	"github.com/psimaker/vaultsync/desktop/tray"
	"github.com/psimaker/vaultsync/hub/syncthing"
)

// The menu-bar app reads the agent's answers through the shared types
// (internal/control) and finds the agent where the shared layout puts it;
// what is left to keep in step is the socket's place and the socket itself.

// The icon and the headline are read off the words the agent's status
// uses; this holds the tray's reading to the agent's actual words, as
// describeFolder and report make them.
func TestIssue176_TheTrayReadsTheAgentsWords(t *testing.T) {
	folder := syncthingFolder("vs-aaaaaaaaaaaa", "Notes", "/home/me/Notes")
	folder.Devices = []syncthing.FolderDevice{folderDeviceOf(testMyID), folderDeviceOf(testHubID)}
	connected := connectionsView{}
	connected.Connections = map[string]struct {
		Connected bool   `json:"connected"`
		Type      string `json:"type"`
	}{testHubID: {Connected: true, Type: "tcp-client"}}
	complete := map[string]remoteCompletion{testHubID: {Completion: 100, RemoteState: "valid"}}
	hub := []tray.Hub{{ID: testHubID, Name: "Test Hub", State: "connected", Kind: control.KindOK}}
	pausedFolder := folder
	pausedFolder.Paused = true
	pausedOnHub := map[string]remoteCompletion{testHubID: {RemoteState: "paused"}}
	for _, c := range []struct {
		name   string
		folder syncthing.FolderConfig
		sum    folderSummary
		conns  connectionsView
		remote map[string]remoteCompletion
		glyph  tray.Glyph
	}{
		{"an error", folder, folderSummary{State: "error", Error: "permission denied"}, connected, complete, tray.GlyphAttention},
		{"files that could not sync", folder, folderSummary{State: "idle", Errors: 3}, connected, complete, tray.GlyphAttention},
		{"syncing", folder, folderSummary{State: "syncing", GlobalBytes: 100, InSyncBytes: 42}, connected, complete, tray.GlyphSyncing},
		{"scanning", folder, folderSummary{State: "scanning"}, connected, complete, tray.GlyphSyncing},
		{"items left", folder, folderSummary{State: "idle", NeedTotal: 2}, connected, complete, tray.GlyphSyncing},
		{"up to date", folder, folderSummary{State: "idle"}, connected, complete, tray.GlyphSynced},
		{"waiting for the Hub", folder, folderSummary{State: "idle"}, connectionsView{}, complete, tray.GlyphOffline},
		{"the folder paused", pausedFolder, folderSummary{State: "idle"}, connected, complete, tray.GlyphPaused},
		{"paused on the Hub", folder, folderSummary{State: "idle"}, connected, pausedOnHub, tray.GlyphPaused},
	} {
		state, kind := describeFolder(c.folder, c.sum, c.conns, c.remote, testMyID, false)
		hubs := hub
		if c.conns.Connections == nil {
			hubs = []tray.Hub{{ID: testHubID, Name: "Test Hub", State: "not connected", Kind: control.KindWaiting}}
		}
		v := tray.Render(tray.Status{Engine: "running", Report: tray.Report{Hubs: hubs, Vaults: []tray.Vault{{Label: "Notes", Path: "/home/me/Notes", State: state, Kind: kind}}}}, nil, tray.Options{Home: "/home/me", CanAsk: true})
		if v.Glyph != c.glyph {
			t.Fatalf("%s: the agent says %q (%s), the tray shows %s (want %s)", c.name, state, kind, v.Glyph, c.glyph)
		}
	}
	if state, kind := describeFolder(folder, folderSummary{State: "idle"}, connected, complete, testMyID, false); state != "up to date" || kind != control.KindOK {
		t.Fatalf("up to date reads %q %s", state, kind)
	}

	// The Hub's words, from a real report: paused, with the terminal's
	// remedy, which the menu cuts off because it has the item for it.
	eng := newFakeEngine(t)
	hubDevice(eng)
	eng.mu.Lock()
	for i := range eng.devices {
		if eng.devices[i].DeviceID == testHubID {
			eng.devices[i].Paused = true
		}
	}
	eng.mu.Unlock()
	a := agentApp(t, eng)
	rep, err := a.report(context.Background(), eng.client, false)
	if err != nil {
		t.Fatal(err)
	}
	v := tray.Render(tray.Status{Engine: "running", Report: rep}, nil, tray.Options{Home: a.home, CanAsk: true})
	if !rep.Paused || rep.Hubs[0].Kind != control.KindPaused || rep.Hubs[0].Remedy == "" || !strings.Contains(rep.Text, "Test Hub               paused on this computer — "+rep.Hubs[0].Remedy) || v.Glyph != tray.GlyphPaused || len(v.Hubs) != 1 || v.Hubs[0].Text != "Test Hub — paused on this computer" || v.Hubs[0].Tooltip != rep.Hubs[0].State+" — "+rep.Hubs[0].Remedy {
		t.Fatalf("paused, as the agent says it: %+v / %+v", rep.Hubs, v)
	}

	// A folder the engine gives no report on this time ("unknown"): a
	// passing state, not a problem to flip the icon over.
	eng.mu.Lock()
	for i := range eng.devices {
		eng.devices[i].Paused = false
	}
	eng.folderState = map[string]map[string]any{"vs-aaaaaaaaaaaa": nil}
	eng.mu.Unlock()
	rep, err = a.report(context.Background(), eng.client, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Vaults) != 1 || rep.Vaults[0].State != "unknown" || rep.Vaults[0].Kind != control.KindUnknown {
		t.Fatalf("no report this time: %+v", rep.Vaults)
	}
	// With the Hub connected (the fake engine's is not), that vault is a
	// passing state on the ring, never attention.
	v = tray.Render(tray.Status{Engine: "running", Report: tray.Report{Hubs: hub, Vaults: rep.Vaults}}, nil, tray.Options{Home: a.home, CanAsk: true})
	if v.Glyph != tray.GlyphSyncing || v.Headline != "1 vault without a status right now" {
		t.Fatalf("no report this time, as the tray shows it: %s %q", v.Glyph, v.Headline)
	}
}

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
		got, err := control.SocketPath(lay)
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
	// A vault on this computer carries the name the terminal would propose
	// for it on the Hub.
	m := menuToJSON(vaultMenu{local: []menuEntry{{label: "Work: 2026", path: "/home/me/Work: 2026", hubName: safeVaultName("Work: 2026")}}})
	if len(m.Local) != 1 || m.Local[0].Name != "Work 2026" {
		t.Fatalf("the proposed name: %+v", m.Local)
	}
}
