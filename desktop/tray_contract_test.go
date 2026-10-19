package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/psimaker/vaultsync/desktop/tray"
)

// The menu-bar app reads the agent's answers through its own types (tray/
// wire.go) and finds the agent where the agent's layout puts it. These
// tests keep the two sides the same: a field renamed on one side fails
// here, not on a person's desktop.

func TestIssue176_TheTrayReadsTheAgentsJSON(t *testing.T) {
	cs := controlStatus{Version: "0.2.0", Syncthing: "2.1.6", Engine: "running", statusReport: statusReport{
		Paused:   true,
		Hubs:     []hubStatus{{ID: "h1", Name: "Office Hub", State: "connected"}},
		Vaults:   []vaultStatus{{ID: "v1", Label: "Notes", Path: "/home/me/Notes", State: "up to date"}},
		Waiting:  []waitingStatus{{Label: "Journal", Hub: "Office Hub"}},
		Attempts: []attemptStatus{{Vault: "Old", What: "your Hub is sharing it", At: "Oct 7 15:04"}},
		Text:     "\nHub\n",
	}}
	var ts tray.Status
	roundTrip(t, cs, &ts)
	want := tray.Status{Version: "0.2.0", Syncthing: "2.1.6", Engine: "running", Paused: true,
		Hubs:     []tray.Hub{{ID: "h1", Name: "Office Hub", State: "connected"}},
		Vaults:   []tray.Vault{{ID: "v1", Label: "Notes", Path: "/home/me/Notes", State: "up to date"}},
		Waiting:  []tray.Waiting{{Label: "Journal", Hub: "Office Hub"}},
		Attempts: []tray.Attempt{{Vault: "Old", What: "your Hub is sharing it", At: "Oct 7 15:04"}},
		Text:     "\nHub\n"}
	if !reflect.DeepEqual(ts, want) {
		t.Fatalf("status:\n%+v\nwant\n%+v", ts, want)
	}

	var pr tray.PauseResult
	roundTrip(t, pauseResponse{Paused: true, Hubs: 2}, &pr)
	if pr != (tray.PauseResult{Paused: true, Hubs: 2}) {
		t.Fatalf("pause: %+v", pr)
	}

	resp := pairResponse{OK: false, Output: "✓ Connected\n", Refusal: "Pass --vault", Needs: "vault", Menu: &menuJSON{
		Local:       []menuEntryJSON{{Label: "Work", Path: "/home/me/Work", Note: "~/Work"}},
		Hub:         []menuEntryJSON{{Label: "Notes", Vault: "vs-1", Note: "1 file on your Hub", Default: "/home/me/Vaults/Notes"}},
		Already:     []menuEntryJSON{{Label: "Journal", Note: "~/Journal"}},
		Unavailable: []menuEntryJSON{},
		Note:        "a note",
	}}
	var tr tray.PairResult
	roundTrip(t, resp, &tr)
	wantPair := tray.PairResult{Output: "✓ Connected\n", Refusal: "Pass --vault", Needs: "vault", Menu: &tray.Menu{
		Local:       []tray.MenuEntry{{Label: "Work", Path: "/home/me/Work", Note: "~/Work"}},
		Hub:         []tray.MenuEntry{{Label: "Notes", Vault: "vs-1", Note: "1 file on your Hub", Default: "/home/me/Vaults/Notes"}},
		Already:     []tray.MenuEntry{{Label: "Journal", Note: "~/Journal"}},
		Unavailable: []tray.MenuEntry{},
		Note:        "a note",
	}}
	if !reflect.DeepEqual(tr, wantPair) {
		t.Fatalf("pair:\n%+v\nwant\n%+v", tr, wantPair)
	}

	// The other direction: what the menu-bar app sends is what the agent
	// reads, flag for flag.
	var req pairRequest
	roundTrip(t, tray.PairRequest{Code: "TULIP-ANCHOR-42", Hub: "192.168.1.20", Vault: "Work", Create: true, Path: "/home/me/Work", Yes: true, Name: "Laptop"}, &req)
	if req != (pairRequest{Code: "TULIP-ANCHOR-42", Hub: "192.168.1.20", Vault: "Work", Create: true, Path: "/home/me/Work", Yes: true, Name: "Laptop"}) {
		t.Fatalf("request: %+v", req)
	}
}

// The tray's one helper that mirrors the agent's — the ~ shortening — is
// the agent's own, path for path. (Where a Hub vault goes is the agent's
// word in the menu, menuEntryJSON.Default, not a copy of safeVaultName.)
func TestIssue176_TheTrayShortensPathsLikeTheAgent(t *testing.T) {
	for _, p := range []string{"/home/me/Notes", "/home/me", "/home/me/", "/other/Notes", "", "/home/meister/Notes"} {
		if got, want := tray.TildePath("/home/me", p), tildePath("/home/me", p); got != want {
			t.Fatalf("%q: tray %q, agent %q", p, got, want)
		}
	}
}

// Every field the tray reads exists on the agent's side, and every field
// the tray sends is one the agent reads — by JSON key, so a field added
// on one side alone fails here.
func TestIssue176_TheTrayAndTheAgentShareEveryField(t *testing.T) {
	keys := func(v any) map[string]bool {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("%s: %v", data, err)
		}
		out := map[string]bool{}
		for k := range m {
			out[k] = true
		}
		return out
	}
	same := func(what string, a, b any) {
		t.Helper()
		if ka, kb := keys(a), keys(b); !reflect.DeepEqual(ka, kb) {
			t.Fatalf("%s: agent %v, tray %v", what, ka, kb)
		}
	}
	same("status", controlStatus{statusReport: emptyReport()}, tray.Status{})
	same("hub", hubStatus{}, tray.Hub{})
	same("vault", vaultStatus{}, tray.Vault{})
	same("waiting", waitingStatus{}, tray.Waiting{})
	same("attempt", attemptStatus{}, tray.Attempt{})
	same("pause", pauseResponse{}, tray.PauseResult{})
	same("pair request", pairRequest{}, tray.PairRequest{})
	// The agent omits empty optional fields; compare with them filled.
	same("pair result", pairResponse{Refusal: "r", Needs: "n", Menu: &menuJSON{}}, tray.PairResult{Refusal: "r", Needs: "n", Menu: &tray.Menu{}})
	same("menu", menuJSON{Note: "n"}, tray.Menu{Note: "n"})
	same("menu entry", menuEntryJSON{Path: "p", Vault: "v", Note: "n", Default: "d"}, tray.MenuEntry{Path: "p", Vault: "v", Note: "n", Default: "d"})
}

func roundTrip(t *testing.T, from, to any) {
	t.Helper()
	data, err := json.Marshal(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, to); err != nil {
		t.Fatalf("%s: %v", data, err)
	}
}

func TestIssue176_TheTrayLooksWhereTheAgentIs(t *testing.T) {
	for _, c := range []struct{ goos, home, xdg string }{
		{"darwin", "/Users/me", ""},
		{"linux", "/home/me", ""},
		{"linux", "/home/me", "/var/lib/me-state"},
	} {
		env := envOf(map[string]string{"XDG_STATE_HOME": c.xdg})
		lay, err := layoutFor(c.goos, c.home, env)
		if err != nil {
			t.Fatal(err)
		}
		dir, err := tray.StateDir(c.goos, c.home, env)
		if err != nil || dir != lay.Base {
			t.Fatalf("%s: %q vs %q (%v)", c.goos, dir, lay.Base, err)
		}
		if c.goos == "darwin" {
			if got := tray.LogPath(c.goos, c.home); got != filepath.Join(lay.Logs, "vaultsync.log") {
				t.Fatalf("log: %q", got)
			}
		}
	}

	// The socket: the usual place, or the one agent.json records — the
	// same the terminal's own client dials.
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
		got, err := tray.SocketPath(lay.Base)
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
