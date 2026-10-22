package tray

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psimaker/vaultsync/desktop/internal/control"
	"github.com/psimaker/vaultsync/desktop/internal/layout"
)

func TestIssue176_TrayShowsWhatTheAgentReports(t *testing.T) {
	home := "/home/me"
	base := Status{Engine: "running", Report: Report{Hubs: []Hub{{Name: "Office Hub", State: "connected", Kind: control.KindOK}}}}
	cases := []struct {
		name     string
		st       Status
		glyph    Glyph
		headline string
		detail   string
		pause    Action
		pair     Action
		quit     string
	}{
		{"up to date", withVaults(base, "up to date"), GlyphSynced, "Up to date", "", Action{"Pause Syncing", true}, Action{"Pair a Vault…", true}, "Quit (syncing continues)"},
		{"connected, a vault waiting", withVaults(base, "up to date", "waiting for your Hub to take it"), GlyphSyncing, "1 vault waiting for your Hub", "", Action{"Pause Syncing", true}, Action{"Pair a Vault…", true}, "Quit (syncing continues)"},
		{"connected, two vaults waiting", withVaults(base, "waiting for your Hub to take it", "waiting for your Hub"), GlyphSyncing, "2 vaults waiting for your Hub", "", Action{"Pause Syncing", true}, Action{"Pair a Vault…", true}, "Quit (syncing continues)"},
		{"syncing", withVaults(base, "syncing — 42 %", "up to date"), GlyphSyncing, "Syncing 1 vault", "", Action{"Pause Syncing", true}, Action{"Pair a Vault…", true}, "Quit (syncing continues)"},
		{"scanning and uploading", withVaults(base, "scanning the folder", "uploading to your Hub — 3 items left"), GlyphSyncing, "Syncing 2 vaults", "", Action{"Pause Syncing", true}, Action{"Pair a Vault…", true}, "Quit (syncing continues)"},
		{"an error outranks syncing", withVaults(base, "syncing", "error: permission denied — allow access to this folder (on a Mac: allow “vaultsync” in System Settings → Privacy & Security → Files & Folders), then run vaultsync stop and vaultsync start"), GlyphAttention, "Attention needed", "Vault 2: error: permission denied — allow access to this folder (on a Mac: allow “vaultsync” in System Settings → Privacy & Security → Files & Folders), then run vaultsync stop and vaultsync start", Action{"Pause Syncing", true}, Action{"Pair a Vault…", true}, "Quit (syncing continues)"},
		{"files that could not sync", withVaults(base, "3 files could not sync — see the engine's log"), GlyphAttention, "Attention needed", "Vault 1: 3 files could not sync — see the engine's log", Action{"Pause Syncing", true}, Action{"Pair a Vault…", true}, "Quit (syncing continues)"},
		{"a vault the engine gave no report on this time", withVaults(base, "up to date", "unknown"), GlyphSyncing, "1 vault without a status right now", "", Action{"Pause Syncing", true}, Action{"Pair a Vault…", true}, "Quit (syncing continues)"},
		{"no Hub paired", withVaults(Status{Engine: "running", Report: Report{Hubs: []Hub{}}}), GlyphOffline, "No Hub paired yet", "Pair a Vault… pairs this computer with your Hub", Action{"Pause Syncing", true}, Action{"Pair a Vault…", true}, "Quit"},
		{"Hub not connected", withVaults(Status{Engine: "running", Report: Report{Hubs: []Hub{{Name: "Office Hub", State: "not connected", Kind: control.KindWaiting}}}}, "waiting for your Hub"), GlyphOffline, "Waiting for your Hub", "Not connected right now; syncing resumes when it is", Action{"Pause Syncing", true}, Action{"Pair a Vault…", true}, "Quit"},
		{"connected through a relay", withVaults(Status{Engine: "running", Report: Report{Hubs: []Hub{{Name: "Office Hub", State: "connected through a relay (slower; a direct connection is not possible right now)", Kind: control.KindOK}}}}, "up to date"), GlyphSynced, "Up to date", "", Action{"Pause Syncing", true}, Action{"Pair a Vault…", true}, "Quit (syncing continues)"},
		{"connected, no vault", base, GlyphSynced, "Connected — no vault yet", "Pair a Vault… adds one", Action{"Pause Syncing", true}, Action{"Pair a Vault…", true}, "Quit (syncing continues)"},
		{"paused", paused(withVaults(base, "paused on this computer", "2 items left to sync")), GlyphPaused, "Paused on this computer", "", Action{"Resume Syncing", true}, Action{"Pair a Vault… (resume syncing first)", false}, "Quit"},
		{"paused with an error: the error shows", paused(withVaults(base, "error: boom", "paused on this computer")), GlyphAttention, "Attention needed", "Vault 1: error: boom", Action{"Resume Syncing", true}, Action{"Pair a Vault… (resume syncing first)", false}, "Quit"},
		{"one vault paused on its own", withVaults(base, "up to date", "paused"), GlyphPaused, "1 vault paused", "", Action{"Pause Syncing", true}, Action{"Pair a Vault…", true}, "Quit"},
		{"two vaults paused on the Hub's side", withVaults(base, "paused on your Hub", "paused on your Hub"), GlyphPaused, "2 vaults paused", "", Action{"Pause Syncing", true}, Action{"Pair a Vault…", true}, "Quit"},
		{"one of two Hubs paused, the other away", withVaults(Status{Engine: "running", Report: Report{Hubs: []Hub{{Name: "Office", State: "paused on this computer", Kind: control.KindPaused, Remedy: "vaultsync resume starts syncing again"}, {Name: "Home", State: "not connected", Kind: control.KindWaiting}}}}, "waiting for your Hub"), GlyphPaused, "1 of 2 Hubs paused", "vaultsync resume in a terminal resumes it", Action{"Pause Syncing", true}, Action{"Pair a Vault…", true}, "Quit"},
		{"engine starting", Status{Engine: "starting", Report: Report{Vaults: []Vault{{Label: "Notes", Path: "/home/me/Notes", State: "not syncing — the engine is stopped", Kind: control.KindOffline}}}}, GlyphSyncing, "Starting the sync engine…", "", Action{"Pause Syncing", false}, Action{"Pair a Vault…", false}, "Quit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := Render(c.st, nil, Options{Home: home, LogPath: "/home/me/Library/Logs/VaultSync/vaultsync.log", CanAsk: true})
			if v.Glyph != c.glyph || v.Headline != c.headline || v.Detail != c.detail {
				t.Fatalf("got %s %q / %q, want %s %q / %q", v.Glyph, v.Headline, v.Detail, c.glyph, c.headline, c.detail)
			}
			if v.Pause != c.pause || v.Pair != c.pair {
				t.Fatalf("actions: %+v %+v, want %+v %+v", v.Pause, v.Pair, c.pause, c.pair)
			}
			if v.Log != (Action{"Open Log", true}) || v.Quit != (Action{c.quit, true}) {
				t.Fatalf("log/quit: %+v %+v", v.Log, v.Quit)
			}
			if len(v.Vaults) != len(c.st.Vaults) || len(v.Hubs) != len(c.st.Hubs) {
				t.Fatalf("lines: %d vaults, %d hubs", len(v.Vaults), len(v.Hubs))
			}
		})
	}

	// The lines: the agent's words, with a terminal remedy cut off the
	// line and kept in the tooltip; the path a click opens is the agent's.
	st := paused(withVaults(base, "paused on this computer", "syncing — 42 %"))
	st.Hubs[0].State, st.Hubs[0].Kind, st.Hubs[0].Remedy = "paused on this computer", control.KindPaused, "vaultsync resume starts syncing again"
	st.Waiting = []Waiting{{Label: "Journal", Hub: "Office Hub"}}
	st.Attempts = []Attempt{{Vault: "Old", What: "your Hub is sharing it; run vaultsync pair to finish setup", At: "Oct 7 15:04"}}
	v := Render(st, nil, Options{Home: home, CanAsk: true})
	if got := v.Hubs[0]; got.Text != "Office Hub — paused on this computer" || got.Tooltip != "paused on this computer — vaultsync resume starts syncing again" {
		t.Fatalf("hub line: %+v", got)
	}
	if got := v.Vaults[0]; got.Text != "Vault 1 — paused on this computer" || got.Path != "/home/me/Vault 1" || got.Tooltip != "~/Vault 1" {
		t.Fatalf("vault line: %+v", got)
	}
	if got := v.Vaults[1]; got.Text != "Vault 2 — syncing — 42 %" {
		t.Fatalf("a percentage is not a remedy: %+v", got)
	}
	if v.Waiting[0].Text != "Journal — shared by Office Hub; Pair a Vault… sets it up" {
		t.Fatalf("waiting: %+v", v.Waiting)
	}
	if v.Attempts[0].Text != "Old — unfinished pairing (Oct 7 15:04)" || v.Attempts[0].Tooltip != st.Attempts[0].What {
		t.Fatalf("attempts: %+v", v.Attempts)
	}
	if v.Log.Title != "" {
		t.Fatalf("no log file, no Log item: %+v", v.Log)
	}
	if v = Render(withVaults(base, "up to date"), nil, Options{CanAsk: false}); v.Pair.Enabled || !strings.Contains(v.Pair.Title, "vaultsync pair in a terminal") {
		t.Fatalf("without dialogs the Pair item says what to do: %+v", v.Pair)
	}
	if v = Render(withVaults(base, "up to date"), nil, Options{CanAsk: true, Pairing: true, Notice: "said"}); v.Pair.Enabled || v.Pair.Title != "Pairing…" || v.Notice != "said" {
		t.Fatalf("while pairing: %+v notice %q", v.Pair, v.Notice)
	}

	// Without a report: why there is none, and nothing to click but the
	// log and Quit — and no "No vault yet" under a headline that says the
	// agent did not answer.
	for _, c := range []struct {
		err      error
		glyph    Glyph
		headline string
	}{
		{ErrNotSetUp, GlyphOffline, "VaultSync is not set up on this computer yet"},
		{ErrNoAgent, GlyphOffline, "VaultSync is not running"},
		{ErrNoSocket, GlyphAttention, "VaultSync does not answer"},
		{ErrOldAgent, GlyphAttention, "VaultSync needs an update on this computer"},
		{errors.New("the sync engine does not answer: boom"), GlyphAttention, "VaultSync could not report"},
	} {
		v := Render(Status{}, c.err, Options{CanAsk: true, LogPath: "/l/vaultsync.log"})
		// An agent that answered (too old) runs on without this app; one
		// that did not answer may or may not — plain Quit there.
		quit := "Quit"
		if errors.Is(c.err, ErrOldAgent) {
			quit = "Quit (syncing continues)"
		}
		if v.Glyph != c.glyph || v.Headline != c.headline || v.Detail == "" || v.Pause.Enabled || v.Pair.Enabled || v.Quit != (Action{quit, true}) || !v.Log.Enabled || v.NoVaultLine != "" {
			t.Fatalf("%v: %+v", c.err, v)
		}
	}
	if v := Render(base, nil, Options{CanAsk: true}); v.NoVaultLine != "No vault yet" {
		t.Fatalf("no vault: %+v", v)
	}
	if v := Render(withVaults(base, "up to date"), nil, Options{CanAsk: true}); v.NoVaultLine != "" {
		t.Fatalf("a vault: %+v", v)
	}
}

func withVaults(st Status, states ...string) Status {
	st.Vaults = []Vault{}
	for i, s := range states {
		label := "Vault " + string(rune('1'+i))
		st.Vaults = append(st.Vaults, Vault{ID: "id" + label, Label: label, Path: "/home/me/" + label, State: s, Kind: kindOf(s)})
	}
	return st
}

// kindOf gives a test's state words the kind the agent would give them
// (the agent's own test holds the two together on the agent's words).
func kindOf(state string) string {
	switch {
	case state == "unknown":
		return control.KindUnknown
	case strings.HasPrefix(state, "error"), strings.Contains(state, "could not sync"):
		return control.KindProblem
	case strings.HasPrefix(state, "syncing"), strings.HasPrefix(state, "scanning"), strings.HasPrefix(state, "uploading"), strings.Contains(state, "left to sync"):
		return control.KindBusy
	case strings.HasPrefix(state, "paused"):
		return control.KindPaused
	case strings.HasPrefix(state, "waiting"):
		return control.KindWaiting
	case strings.HasPrefix(state, "not syncing"):
		return control.KindOffline
	}
	return control.KindOK
}

func paused(st Status) Status {
	st.Paused = true
	return st
}

func TestIssue176_TrayFindsTheAgentWhereTheAgentSaysItIs(t *testing.T) {
	dir := t.TempDir()
	lay := layout.Layout{}.At(dir)
	if _, err := control.SocketPath(lay); !errors.Is(err, ErrNotSetUp) {
		t.Fatalf("no agent.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.json"), []byte(`{"version":1,"guiPort":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := control.SocketPath(lay); !errors.Is(err, ErrNotSetUp) {
		t.Fatalf("no port yet: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.json"), []byte(`{"version":1,"guiPort":8384}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, err := control.SocketPath(lay); err != nil || p != filepath.Join(dir, "agent.sock") {
		t.Fatalf("the usual place: %q %v", p, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.json"), []byte(`{"version":1,"guiPort":8384,"controlSocket":"/run/user/1000/vaultsync-x/agent.sock"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, err := control.SocketPath(lay); err != nil || p != "/run/user/1000/vaultsync-x/agent.sock" {
		t.Fatalf("the recorded place: %q %v", p, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.json"), []byte(`{"version":1,`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := control.SocketPath(lay); err == nil || errors.Is(err, ErrNotSetUp) || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("a damaged agent.json is reported, not read as unset: %v", err)
	}
}

func TestIssue176_TrayClientTalksToTheAgentAlone(t *testing.T) {
	agent := newFakeAgent(t)
	agent.set(func(f *fakeAgent) {
		f.status.Hubs = []Hub{{ID: "h", Name: "Office Hub", State: "connected", Kind: control.KindOK}}
		f.status.Vaults = []Vault{{ID: "v", Label: "Notes", Path: "/home/me/Notes", State: "up to date", Kind: control.KindOK}}
		f.status.Text = "\nHub\n  Office Hub             connected\n"
	})
	ctx := context.Background()
	c := NewClient(agent.socket)
	st, err := c.Status(ctx)
	if err != nil || st.Engine != "running" || len(st.Hubs) != 1 || st.Vaults[0].Path != "/home/me/Notes" || !strings.Contains(st.Text, "Office Hub") {
		t.Fatalf("status: %+v %v", st, err)
	}
	// An agent that answers and fails is the news.
	agent.set(func(f *fakeAgent) { f.statusErr = "the sync engine does not answer: boom" })
	if _, err := c.Status(ctx); err == nil || errors.Is(err, ErrNoAgent) || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("a failing agent: %v", err)
	}
	agent.set(func(f *fakeAgent) { f.statusErr = "" })
	// A connection made and dropped is not "no agent" either.
	dropping := filepath.Join(shortDir(t), "agent.sock")
	l, err := net.Listen("unix", dropping)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	t.Cleanup(func() { l.Close() })
	if _, err := NewClient(dropping).Status(ctx); err == nil || errors.Is(err, ErrNoAgent) {
		t.Fatalf("a dropped connection: %v", err)
	}
	// No socket, nobody listening, an agent without this API, an address
	// too long to be served: no agent.
	if _, err := NewClient(filepath.Join(shortDir(t), "agent.sock")).Status(ctx); !errors.Is(err, ErrNoAgent) {
		t.Fatalf("no socket file: %v", err)
	}
	if _, err := NewClient(strings.Repeat("/x", 60) + "/agent.sock").Status(ctx); !errors.Is(err, ErrNoAgent) {
		t.Fatalf("an overlong address: %v", err)
	}
	// Something answers, but not this API: an older agent, which is not
	// "not running" — the menu cannot ask the engine instead.
	agent.set(func(f *fakeAgent) { f.old = true })
	if _, err := c.Status(ctx); !errors.Is(err, ErrOldAgent) || errors.Is(err, ErrNoAgent) {
		t.Fatalf("an older agent: %v", err)
	}
	agent.set(func(f *fakeAgent) { f.old = false })
	agent.stop()
	if _, err := c.Status(ctx); !errors.Is(err, ErrNoAgent) {
		t.Fatalf("nobody listening: %v", err)
	}
}

func TestIssue176_TrayNeverTalksToAnotherAccountsSocket(t *testing.T) {
	agent := newFakeAgent(t)
	foreign := control.PeerFunc(func(*net.UnixConn) (int, error) { return os.Getuid() + 1, nil })
	control.PeerUIDHook.Store(&foreign)
	defer control.PeerUIDHook.Store(nil)
	_, err := NewClient(agent.socket).Status(context.Background())
	if !errors.Is(err, ErrForeignSocket) {
		t.Fatalf("a foreign socket: %v", err)
	}
	unknown := control.PeerFunc(func(*net.UnixConn) (int, error) { return -1, errors.New("no credentials") })
	control.PeerUIDHook.Store(&unknown)
	if _, err := NewClient(agent.socket).Status(context.Background()); err == nil || errors.Is(err, ErrNoAgent) || !strings.Contains(err.Error(), "could not tell whose") {
		t.Fatalf("unknown owner: %v", err)
	}
}

// controllerFor runs a controller against the fake agent until the test ends.
func controllerFor(t *testing.T, agent *fakeAgent, ui *fakeUI, c *Controller) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c.Home = "/home/me"
	if c.Layout.Base == "" {
		// Like a Mac: a service log to open. A test that wants none (the
		// journal) sets Layout itself.
		c.Layout = agent.layout
		c.Layout.Logs = agent.logs
	}
	c.UI = ui
	c.Poll = time.Hour
	c.StatusTimeout = 5 * time.Second
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the controller did not stop")
		}
	})
}

func TestIssue176_TrayPauseAndResumeAskOnlyTheAgent(t *testing.T) {
	agent := newFakeAgent(t)
	agent.set(func(f *fakeAgent) {
		f.hubs = 1
		f.status.Hubs = []Hub{{Name: "Office Hub", State: "connected", Kind: control.KindOK}}
		f.status.Vaults = []Vault{{Label: "Notes", Path: "/home/me/Notes", State: "up to date", Kind: control.KindOK}}
	})
	ui := newFakeUI(t)
	var notes []string
	var mu sync.Mutex
	controllerFor(t, agent, ui, &Controller{Notify: func(_ context.Context, s string) { mu.Lock(); notes = append(notes, s); mu.Unlock() }})
	ui.view(t, "up to date", func(v View) bool { return v.Headline == "Up to date" })

	ui.send(Event{Kind: EventPause, Pause: true})
	v := ui.view(t, "paused", func(v View) bool { return v.Glyph == GlyphPaused })
	if v.Pause.Title != "Resume Syncing" || v.Notice != "" {
		t.Fatalf("after pause: %+v", v)
	}
	// A click queued behind the first (still asking to pause) must not
	// undo it: asking for the state already in place does nothing.
	ui.send(Event{Kind: EventPause, Pause: true})
	ui.click(EventOpened, 0)
	ui.view(t, "still paused", func(v View) bool { return v.Glyph == GlyphPaused })
	ui.send(Event{Kind: EventPause, Pause: false})
	ui.view(t, "resumed", func(v View) bool { return v.Glyph == GlyphSynced && v.Pause.Title == "Pause Syncing" })
	agent.set(func(f *fakeAgent) {
		if !reflect.DeepEqual(f.calls, []string{"pause", "resume"}) {
			t.Fatalf("calls: %v", f.calls)
		}
	})

	// Nothing to pause: the agent's count, the terminal's words.
	agent.set(func(f *fakeAgent) { f.hubs = 0; f.status.Hubs = []Hub{} })
	ui.click(EventOpened, 0)
	ui.view(t, "no Hub", func(v View) bool { return v.Headline == "No Hub paired yet" })
	ui.send(Event{Kind: EventPause, Pause: true})
	v = ui.view(t, "the notice", func(v View) bool { return v.Notice != "" })
	if v.Notice != "No Hub is paired with this computer yet, so there is nothing to pause." {
		t.Fatalf("notice: %q", v.Notice)
	}
	mu.Lock()
	if len(notes) != 1 || notes[0] != v.Notice {
		t.Fatalf("notified: %v", notes)
	}
	mu.Unlock()

	// No agent: the item is off, a click does nothing, the agent is not asked.
	agent.stop()
	ui.click(EventOpened, 0)
	v = ui.view(t, "not running", func(v View) bool { return v.Headline == "VaultSync is not running" })
	if v.Pause.Enabled || v.Pair.Enabled {
		t.Fatalf("actions without an agent: %+v", v)
	}
	ui.send(Event{Kind: EventPause, Pause: true})
	ui.click(EventOpened, 0)
	ui.view(t, "still not running", func(v View) bool { return v.Headline == "VaultSync is not running" })
	agent.set(func(f *fakeAgent) {
		if len(f.calls) != 3 {
			t.Fatalf("calls after the agent stopped: %v", f.calls)
		}
	})

	// A report without kinds is an older agent's: nothing is read off its
	// words, the menu says to update.
	agent2 := newFakeAgent(t)
	agent2.set(func(f *fakeAgent) {
		f.status.Hubs = []Hub{{Name: "Office Hub", State: "connected"}}
		f.status.Vaults = []Vault{{Label: "Notes", Path: "/home/me/Notes", State: "error: boom"}}
	})
	ui2 := newFakeUI(t)
	controllerFor(t, agent2, ui2, &Controller{})
	if v := ui2.view(t, "needs an update", func(v View) bool { return v.Headline == "VaultSync needs an update on this computer" }); v.Glyph != GlyphAttention {
		t.Fatalf("a report without kinds: %+v", v)
	}

	// The agent recorded that it could not make its socket: nobody
	// answering then does not mean nobody runs, and the menu says so.
	if err := os.WriteFile(filepath.Join(agent.stateDir, "control-socket-error.txt"), []byte("too deep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ui.click(EventOpened, 0)
	v = ui.view(t, "no socket", func(v View) bool { return v.Headline == "VaultSync does not answer" })
	if !strings.Contains(v.Detail, "control-socket-error.txt") || v.Glyph != GlyphAttention {
		t.Fatalf("without a socket: %+v", v)
	}
}

// fakeLock is a held one-instance lock a test can invalidate.
type fakeLock struct {
	mu       sync.Mutex
	valid    bool
	released bool
}

func (l *fakeLock) Valid() bool { l.mu.Lock(); defer l.mu.Unlock(); return l.valid }
func (l *fakeLock) Release()    { l.mu.Lock(); defer l.mu.Unlock(); l.released = true }

func TestIssue176_TrayTakesTheLockOnceTheFolderIsThere(t *testing.T) {
	agent := newFakeAgent(t)
	ui := newFakeUI(t)
	var mu sync.Mutex
	var tries int
	var handles []*fakeLock
	folderThere := false
	lock := func(lay layout.Layout) (LockHandle, error) {
		mu.Lock()
		defer mu.Unlock()
		tries++
		if lay.Base != agent.stateDir {
			t.Errorf("locking %q", lay.Base)
		}
		if !folderThere {
			return nil, nil
		}
		l := &fakeLock{valid: true}
		handles = append(handles, l)
		return l, nil
	}
	c := &Controller{Lock: lock}
	controllerFor(t, agent, ui, c)
	ui.view(t, "first", func(v View) bool { return v.Headline != "" })
	ui.click(EventOpened, 0)
	ui.view(t, "second", func(v View) bool { return v.Headline != "" })
	mu.Lock()
	if tries != 2 || c.Held != nil {
		t.Fatalf("not held yet: tries %d held %v", tries, c.Held)
	}
	folderThere = true
	mu.Unlock()
	ui.click(EventOpened, 0)
	ui.view(t, "third", func(v View) bool { return v.Headline != "" })
	ui.click(EventOpened, 0)
	ui.view(t, "fourth", func(v View) bool { return v.Headline != "" })
	mu.Lock()
	if tries != 3 || c.Held == nil || len(handles) != 1 {
		t.Fatalf("once held, no more tries: tries %d held %v", tries, c.Held)
	}
	// The file taken away (uninstall --remove-data): the lock guards an
	// orphan — released, and taken again at the next refresh.
	handles[0].valid = false
	mu.Unlock()
	ui.click(EventOpened, 0)
	ui.view(t, "fifth", func(v View) bool { return v.Headline != "" })
	mu.Lock()
	if tries != 4 || len(handles) != 2 || !handles[0].released || handles[0] == c.Held {
		t.Fatalf("an invalid lock is released and taken again: tries %d handles %d released %v", tries, len(handles), handles[0].released)
	}
	mu.Unlock()

	// Any other failure to take the lock is said in the menu and tried
	// again — the app stays.
	ui4 := newFakeUI(t)
	var fails int
	c4 := &Controller{Lock: func(layout.Layout) (LockHandle, error) {
		mu.Lock()
		defer mu.Unlock()
		fails++
		if fails == 1 {
			return nil, errors.New("open tray.lock: permission denied")
		}
		return &fakeLock{valid: true}, nil
	}}
	controllerFor(t, agent, ui4, c4)
	v := ui4.view(t, "the lock notice", func(v View) bool { return strings.Contains(v.Notice, "could not be locked") })
	if v.Headline == "" {
		t.Fatalf("the menu goes on: %+v", v)
	}
	ui4.click(EventOpened, 0)
	ui4.view(t, "held after all, the notice gone", func(v View) bool { return c4.Held != nil && v.Notice == "" })
	// While the lock fails, what an action said is not overwritten by it:
	// both stand in the menu.
	ui5 := newFakeUI(t)
	c5 := &Controller{Lock: func(layout.Layout) (LockHandle, error) {
		return nil, errors.New("open tray.lock: read-only file system")
	}, Open: func(context.Context, string) error { return errors.New("no such file") }}
	controllerFor(t, agent, ui5, c5)
	ui5.view(t, "the lock notice alone", func(v View) bool { return strings.Contains(v.Notice, "could not be locked") })
	ui5.click(EventOpenLog, 0)
	ui5.view(t, "both", func(v View) bool {
		return strings.Contains(v.Notice, "Could not open it") && strings.Contains(v.Notice, "could not be locked")
	})

	// A second instance found running ends the loop with its error.
	ui2 := newFakeUI(t)
	c2 := &Controller{Home: "/home/me", Layout: agent.layout, UI: ui2, Poll: time.Hour, Lock: func(layout.Layout) (LockHandle, error) { return nil, ErrAlreadyRunning }}
	if err := c2.Run(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("a second instance: %v", err)
	}
	// One already held at start is never asked for again while it is
	// valid, and released on the way out.
	start := &fakeLock{valid: true}
	c3 := &Controller{Home: "/home/me", Layout: agent.layout, UI: newFakeUI(t), Poll: time.Hour, Held: start, Lock: func(layout.Layout) (LockHandle, error) { t.Error("asked again"); return nil, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c3.Run(ctx); !errors.Is(err, context.Canceled) || !start.released {
		t.Fatalf("held at start: %v released %v", err, start.released)
	}
}

func TestIssue176_TrayShowsNothingOnTheWayOut(t *testing.T) {
	// Quit while a status request is in flight: the cut-short request is
	// not rendered as "could not report" — the loop ends with the context.
	agent := newFakeAgent(t)
	hold := make(chan struct{})
	agent.set(func(f *fakeAgent) { f.statusHold = hold })
	ui := newFakeUI(t)
	ctx, cancel := context.WithCancel(context.Background())
	c := &Controller{Home: "/home/me", Layout: agent.layout, UI: ui, Poll: time.Hour, StatusTimeout: 20 * time.Second}
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	time.Sleep(200 * time.Millisecond) // the request is on its way
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run ended with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not end")
	}
	close(hold)
	select {
	case v := <-ui.views:
		t.Fatalf("a view on the way out: %+v", v)
	default:
	}
}

func TestIssue176_TrayShowsTheLastOutcomeAfterAnAction(t *testing.T) {
	// Not set up (no agent.json): a failed Open Log must not turn the
	// menu into "Starting the sync engine…" — the last refresh's outcome
	// stays until the next refresh.
	agent := newFakeAgent(t)
	if err := os.Remove(filepath.Join(agent.stateDir, "agent.json")); err != nil {
		t.Fatal(err)
	}
	ui := newFakeUI(t)
	controllerFor(t, agent, ui, &Controller{Open: func(context.Context, string) error { return errors.New("no such file") }})
	ui.view(t, "not set up", func(v View) bool { return v.Headline == "VaultSync is not set up on this computer yet" })
	ui.click(EventOpenLog, 0)
	v := ui.view(t, "the notice", func(v View) bool { return v.Notice != "" })
	if v.Headline != "VaultSync is not set up on this computer yet" || v.Glyph != GlyphOffline || !strings.Contains(v.Notice, "no such file") {
		t.Fatalf("after a failed open: %+v", v)
	}
}

func TestIssue176_TrayOpensOnlyWhatTheAgentListed(t *testing.T) {
	agent := newFakeAgent(t)
	agent.set(func(f *fakeAgent) {
		f.hubs = 1
		f.status.Hubs = []Hub{{Name: "Office Hub", State: "connected", Kind: control.KindOK}}
		f.status.Vaults = []Vault{{Label: "Notes", Path: "/home/me/Notes", State: "up to date", Kind: control.KindOK}, {Label: "Odd", Path: "relative/path", State: "up to date", Kind: control.KindOK}}
	})
	ui := newFakeUI(t)
	var mu sync.Mutex
	var opened []string
	open := func(_ context.Context, p string) error {
		mu.Lock()
		defer mu.Unlock()
		opened = append(opened, p)
		if !filepath.IsAbs(p) {
			return errors.New("not an absolute path")
		}
		return nil
	}
	controllerFor(t, agent, ui, &Controller{Open: open})
	ui.view(t, "up to date", func(v View) bool { return v.Headline == "Up to date" })
	ui.send(Event{Kind: EventOpenVault, Path: "/home/me/Notes"})
	ui.click(EventOpenLog, 0)
	ui.send(Event{Kind: EventOpenVault, Path: "/home/me/Elsewhere"}) // no vault's folder: not opened
	ui.send(Event{Kind: EventOpenVault, Path: "relative/path"})      // the opener refuses it
	v := ui.view(t, "the refusal", func(v View) bool { return strings.HasPrefix(v.Notice, "Could not open it") })
	// The opens run off the loop: all three have been handed to the
	// opener once the last one's refusal is in the menu, but not
	// necessarily in that order, nor before this line.
	want := []string{filepath.Join(agent.logs, "vaultsync.log"), "/home/me/Notes", "relative/path"}
	sort.Strings(want)
	var got []string
	for deadline := time.Now().Add(10 * time.Second); ; {
		mu.Lock()
		got = append([]string(nil), opened...)
		mu.Unlock()
		sort.Strings(got)
		if reflect.DeepEqual(got, want) || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("opened %v, want %v", got, want)
	}
	if !strings.Contains(v.Notice, "not an absolute path") {
		t.Fatalf("notice: %q", v.Notice)
	}

	// An open that takes its time (a file manager that keeps the program
	// waiting) holds neither the menu nor Quit: the loop goes on while it
	// runs.
	release := make(chan struct{})
	slow := newFakeUI(t)
	controllerFor(t, agent, slow, &Controller{Open: func(context.Context, string) error { <-release; return nil }})
	slow.view(t, "up to date", func(v View) bool { return v.Headline == "Up to date" })
	slow.send(Event{Kind: EventOpenVault, Path: "/home/me/Notes"})
	slow.click(EventOpened, 0)
	slow.view(t, "a refresh while the open runs", func(v View) bool { return v.Headline == "Up to date" })
	close(release)

	dirs := func(path string) bool { return !strings.HasSuffix(path, ".log") }
	for _, c := range []struct {
		goos, path, name string
		args             []string
	}{
		{"darwin", "/home/me/Notes", "open", []string{"/home/me/Notes"}},
		{"linux", "/home/me/Notes", "xdg-open", []string{"/home/me/Notes"}},
		// A folder named like a bundle would be launched by open; it is
		// revealed in its parent instead. A file is a file.
		{"darwin", "/home/me/Journal.app", "open", []string{"-R", "/home/me/Journal.app"}},
		{"darwin", "/home/me/notes.v2", "open", []string{"-R", "/home/me/notes.v2"}},
		{"darwin", "/home/me/.hidden", "open", []string{"/home/me/.hidden"}},
		{"darwin", "/home/me/Library/Logs/VaultSync/vaultsync.log", "open", []string{"/home/me/Library/Logs/VaultSync/vaultsync.log"}},
		{"darwin", "Notes", "", nil},
		{"darwin", "", "", nil},
		{"windows", `C:\Notes`, "", nil},
	} {
		name, args, err := openCommand(c.goos, c.path, dirs)
		if c.name == "" {
			if err == nil {
				t.Fatalf("%s %q opened", c.goos, c.path)
			}
			continue
		}
		if err != nil || name != c.name || !reflect.DeepEqual(args, c.args) {
			t.Fatalf("%s %q: %s %v %v", c.goos, c.path, name, args, err)
		}
	}
}

// pairingScript is the agent's side of one conversation: the vault menu
// first, then the consent for a folder with files, then success.
func pairingScript(t *testing.T, code string) func(PairRequest) (PairResult, int) {
	menu := &Menu{
		Local:       []MenuEntry{{Label: "Work", Path: "/home/me/Work", Note: "~/Work  opened today"}},
		Hub:         []MenuEntry{{Label: "Notes", Vault: "vs-aaaaaaaaaaaa", Note: "12 files on your Hub", Default: "/home/me/Vaults/Notes"}},
		Already:     []MenuEntry{{Label: "Journal", Note: "~/Journal"}},
		Unavailable: []MenuEntry{{Label: "Cloudy", Note: "~/iCloud/Cloudy — iCloud Drive — make a local copy first (A shows how)"}},
	}
	return func(req PairRequest) (PairResult, int) {
		switch {
		case req.Code == "":
			return PairResult{Refusal: "Pass the code your Hub printed: --code WORD-WORD-NN — there is no terminal to ask on.", Needs: "code"}, 200
		case req.Code != code:
			return PairResult{Output: "✗ Your Hub did not accept the code.\n", Refusal: "Your Hub did not accept this code. 4 tries are left before it is locked."}, 200
		case req.Vault == "" || req.Path == "":
			return PairResult{Output: "✓ Connected to “Office Hub”\n", Refusal: "Pass --vault NAME and --path FOLDER (with --create for a new vault on your Hub) — there is no terminal to ask on.", Needs: "vault", Menu: menu}, 200
		case req.Create && !req.Yes:
			return PairResult{Folder: "1:2", Refusal: "~/Work already contains files. VaultSync would sync it with your Hub as the new vault “Work”, and edits and deletions on connected devices would also change this folder. Pass --yes to agree.", Needs: "yes", Consent: "Remotely Save is enabled in “Work”; two services syncing one folder work against each other.\n“Work” at ~/Work already contains files. VaultSync will sync this folder with “Office Hub”.\nEdits and deletions on connected devices will also change this folder.\nIf Obsidian Sync or another service also syncs this folder, turn that off for it first."}, 200
		}
		return PairResult{OK: true, Output: "✓ Connected to “Office Hub”\n\n✓ “" + req.Vault + "” is set up to sync at " + req.Path + ". First sync is starting; check vaultsync status.\n"}, 200
	}
}

func TestIssue176_TrayPairingAsksOnlyWhatTheAgentNeeds(t *testing.T) {
	ctx := context.Background()
	chooseLocal := func(m Menu) (Choice, bool) { return Choice{Local: true, Entry: m.Local[0]}, true }
	chooseHub := func(m Menu) (Choice, bool) { return Choice{Entry: m.Hub[0]}, true }

	// A vault from this computer, with files: code → menu → consent → done.
	agent := newFakeAgent(t)
	agent.set(func(f *fakeAgent) { f.pair = pairingScript(t, "TULIP-ANCHOR-42") })
	ask := &fakeAsker{code: "TULIP-ANCHOR-42", choose: chooseLocal, agree: true}
	line, err := Pair(ctx, NewClient(agent.socket), ask)
	if err != nil || !strings.HasPrefix(line, "✓ “Work” is set up to sync at /home/me/Work") {
		t.Fatalf("pairing: %q %v", line, err)
	}
	if got := ask.sequence(); !reflect.DeepEqual(got, []string{"code", "vault", "consent", "inform"}) {
		t.Fatalf("asked: %v", got)
	}
	agent.set(func(f *fakeAgent) {
		want := []PairRequest{
			{Code: "TULIP-ANCHOR-42"},
			{Code: "TULIP-ANCHOR-42", Vault: "Work", Create: true, Path: "/home/me/Work"},
			{Code: "TULIP-ANCHOR-42", Vault: "Work", Create: true, Path: "/home/me/Work", Yes: true, Folder: "1:2"},
		}
		if !reflect.DeepEqual(f.pairs, want) {
			t.Fatalf("requests:\n%+v\nwant\n%+v", f.pairs, want)
		}
	})
	// The dialog shows the agent's consent — the terminal's lines, the
	// plugin warning first — never the refusal with its flag.
	if len(ask.texts) != 1 || !strings.HasPrefix(ask.texts[0], "Remotely Save is enabled") || !strings.Contains(ask.texts[0], "“Work” at ~/Work already contains files") || strings.Contains(ask.texts[0], "--yes") || !strings.Contains(ask.texts[0], "Obsidian Sync") {
		t.Fatalf("the consent text: %q", ask.texts)
	}
	if len(ask.menus) != 1 || ask.menus[0].Hub[0].Vault != "vs-aaaaaaaaaaaa" || len(ask.menus[0].Unavailable) != 1 {
		t.Fatalf("the menu shown: %+v", ask.menus)
	}
	// needs yes without the question to agree to, or without the folder it
	// is about: the refusal is shown, no consent dialog is made of a
	// sentence that names a flag, and no yes goes out for a folder not
	// named.
	for _, strip := range []string{"consent", "folder"} {
		agent = newFakeAgent(t)
		agent.set(func(f *fakeAgent) {
			inner := pairingScript(t, "TULIP-ANCHOR-42")
			f.pair = func(req PairRequest) (PairResult, int) {
				res, code := inner(req)
				if strip == "consent" {
					res.Consent = ""
				} else {
					res.Folder = ""
				}
				return res, code
			}
		})
		ask = &fakeAsker{code: "TULIP-ANCHOR-42", choose: chooseLocal, agree: true}
		line, err = Pair(ctx, NewClient(agent.socket), ask)
		if err != nil || !strings.Contains(line, "Pass --yes to agree.") || !reflect.DeepEqual(ask.sequence(), []string{"code", "vault", "inform"}) || ask.told[0] != line {
			t.Fatalf("needs yes without %s: %q %v %v %v", strip, line, err, ask.sequence(), ask.told)
		}
		agent.set(func(f *fakeAgent) {
			for _, r := range f.pairs {
				if r.Yes {
					t.Fatalf("a yes went out without %s: %+v", strip, f.pairs)
				}
			}
		})
	}

	// Consent refused: yes is never sent, nothing changed.
	agent = newFakeAgent(t)
	agent.set(func(f *fakeAgent) { f.pair = pairingScript(t, "TULIP-ANCHOR-42") })
	ask = &fakeAsker{code: "TULIP-ANCHOR-42", choose: chooseLocal, agree: false}
	if line, err := Pair(ctx, NewClient(agent.socket), ask); err != nil || line != "" {
		t.Fatalf("declined consent: %q %v", line, err)
	}
	agent.set(func(f *fakeAgent) {
		if len(f.pairs) != 2 || f.pairs[1].Yes {
			t.Fatalf("requests after a declined consent: %+v", f.pairs)
		}
	})
	if got := ask.sequence(); !reflect.DeepEqual(got, []string{"code", "vault", "consent", "inform"}) || ask.told[0] != "Nothing was changed." {
		t.Fatalf("asked: %v told %v", got, ask.told)
	}

	// A vault from the Hub goes where the agent says the terminal would
	// put it; an agent that names no place gets no request for it.
	agent = newFakeAgent(t)
	agent.set(func(f *fakeAgent) { f.pair = pairingScript(t, "TULIP-ANCHOR-42") })
	ask = &fakeAsker{code: "TULIP-ANCHOR-42", choose: chooseHub}
	if line, err := Pair(ctx, NewClient(agent.socket), ask); err != nil || !strings.Contains(line, "“vs-aaaaaaaaaaaa” is set up to sync at /home/me/Vaults/Notes") {
		t.Fatalf("a Hub vault: %q %v", line, err)
	}
	agent.set(func(f *fakeAgent) {
		if len(f.pairs) != 2 || f.pairs[1] != (PairRequest{Code: "TULIP-ANCHOR-42", Vault: "vs-aaaaaaaaaaaa", Path: "/home/me/Vaults/Notes"}) {
			t.Fatalf("requests: %+v", f.pairs)
		}
	})
	agent = newFakeAgent(t)
	agent.set(func(f *fakeAgent) {
		inner := pairingScript(t, "TULIP-ANCHOR-42")
		f.pair = func(req PairRequest) (PairResult, int) {
			res, code := inner(req)
			if res.Menu != nil {
				res.Menu.Hub[0].Default = ""
			}
			return res, code
		}
	})
	ask = &fakeAsker{code: "TULIP-ANCHOR-42", choose: chooseHub}
	if line, err := Pair(ctx, NewClient(agent.socket), ask); err != nil || !strings.Contains(line, "did not say where to put Notes") || !reflect.DeepEqual(ask.sequence(), []string{"code", "vault", "inform"}) {
		t.Fatalf("no default folder: %q %v %v", line, err, ask.sequence())
	}
	agent.set(func(f *fakeAgent) {
		if len(f.pairs) != 1 {
			t.Fatalf("requests without a default: %+v", f.pairs)
		}
	})

	// A vault on this computer whose name the Hub already has goes out as
	// --create like any other; the agent refuses it (never a download in
	// its place), and the refusal is shown as it is — no consent asked.
	agent = newFakeAgent(t)
	agent.set(func(f *fakeAgent) {
		inner := pairingScript(t, "TULIP-ANCHOR-42")
		f.pair = func(req PairRequest) (PairResult, int) {
			if req.Create && strings.EqualFold(req.Vault, "notes") {
				return PairResult{Refusal: "Your Hub already has a vault named “Notes”. Leave out --create to download it, or choose another name for a new vault."}, 200
			}
			res, code := inner(req)
			if res.Menu != nil {
				res.Menu.Local = append(res.Menu.Local, MenuEntry{Label: "notes", Path: "/home/me/notes"})
			}
			return res, code
		}
	})
	ask = &fakeAsker{code: "TULIP-ANCHOR-42", choose: func(m Menu) (Choice, bool) { return Choice{Local: true, Entry: m.Local[1]}, true }, agree: true}
	line, err = Pair(ctx, NewClient(agent.socket), ask)
	if err != nil || !strings.HasPrefix(line, "Your Hub already has a vault named “Notes”.") || !reflect.DeepEqual(ask.sequence(), []string{"code", "vault", "inform"}) {
		t.Fatalf("a taken name: %q %v %v", line, err, ask.sequence())
	}
	agent.set(func(f *fakeAgent) {
		if len(f.pairs) != 2 || f.pairs[1] != (PairRequest{Code: "TULIP-ANCHOR-42", Vault: "notes", Create: true, Path: "/home/me/notes"}) {
			t.Fatalf("requests for a taken name: %+v", f.pairs)
		}
	})

	// Cancelled at the code: the agent is never asked. Cancelled at the
	// menu: asked once, nothing more.
	agent = newFakeAgent(t)
	agent.set(func(f *fakeAgent) { f.pair = pairingScript(t, "TULIP-ANCHOR-42") })
	ask = &fakeAsker{code: "TULIP-ANCHOR-42", cancel: "code"}
	if line, err := Pair(ctx, NewClient(agent.socket), ask); err != nil || line != "" {
		t.Fatalf("cancelled: %q %v", line, err)
	}
	ask = &fakeAsker{code: "  ", cancel: ""}
	if line, err := Pair(ctx, NewClient(agent.socket), ask); err != nil || !strings.HasPrefix(line, "Enter the code your Hub printed") || len(ask.told) != 1 {
		t.Fatalf("an empty code is said, not swallowed: %q %v %v", line, err, ask.told)
	}
	ask = &fakeAsker{code: "TULIP-ANCHOR-42", cancel: "vault"}
	if line, err := Pair(ctx, NewClient(agent.socket), ask); err != nil || line != "" {
		t.Fatalf("cancelled at the menu: %q %v", line, err)
	}
	agent.set(func(f *fakeAgent) {
		if len(f.pairs) != 1 {
			t.Fatalf("requests: %+v", f.pairs)
		}
	})

	// A wrong code: the agent's refusal, shown, and the conversation ends.
	ask = &fakeAsker{code: "WRONG-CODE-00", choose: chooseLocal, agree: true}
	line, err = Pair(ctx, NewClient(agent.socket), ask)
	if err != nil || !strings.Contains(line, "did not accept this code") || !reflect.DeepEqual(ask.sequence(), []string{"code", "inform"}) {
		t.Fatalf("a wrong code: %q %v %v", line, err, ask.sequence())
	}

	// Several Hubs answered: the person names one, and it is sent along.
	agent = newFakeAgent(t)
	agent.set(func(f *fakeAgent) {
		inner := pairingScript(t, "TULIP-ANCHOR-42")
		f.pair = func(req PairRequest) (PairResult, int) {
			if req.Hub == "" {
				return PairResult{Refusal: "2 Hubs answered: Office Hub at 192.168.1.20:7700, Attic at 192.168.1.21:7700. VaultSync sends a code only to the Hub you choose.", Needs: "hub"}, 200
			}
			return inner(req)
		}
	})
	ask = &fakeAsker{code: "TULIP-ANCHOR-42", hub: "192.168.1.20", choose: chooseHub}
	if line, err := Pair(ctx, NewClient(agent.socket), ask); err != nil || !strings.Contains(line, "is set up") {
		t.Fatalf("several Hubs: %q %v", line, err)
	}
	if got := ask.sequence(); len(got) != 4 || !strings.HasPrefix(got[1], "hub:2 Hubs answered") {
		t.Fatalf("asked: %v", got)
	}
	agent.set(func(f *fakeAgent) {
		for _, r := range f.pairs[1:] {
			if r.Hub != "192.168.1.20" {
				t.Fatalf("the Hub was not carried along: %+v", f.pairs)
			}
		}
	})
	// An empty address is said, not sent as a Hub.
	ask = &fakeAsker{code: "TULIP-ANCHOR-42", hub: "  ", choose: chooseHub}
	if line, err := Pair(ctx, NewClient(agent.socket), ask); err != nil || !strings.HasPrefix(line, "Enter your Hub's address") || !reflect.DeepEqual(ask.sequence()[2:], []string{"inform"}) {
		t.Fatalf("an empty address: %q %v %v", line, err, ask.sequence())
	}

	// The agent asking for the same flag twice ends the conversation
	// instead of asking the person again and again.
	agent = newFakeAgent(t)
	agent.set(func(f *fakeAgent) {
		f.pair = func(PairRequest) (PairResult, int) {
			return PairResult{Refusal: "Pass --vault NAME and --path FOLDER — there is no terminal to ask on.", Needs: "vault", Menu: &Menu{Local: []MenuEntry{{Label: "Work", Path: "/home/me/Work"}}}}, 200
		}
	})
	ask = &fakeAsker{code: "TULIP-ANCHOR-42", choose: chooseLocal}
	if line, err := Pair(ctx, NewClient(agent.socket), ask); err != nil || !strings.HasPrefix(line, "VaultSync asked for the same answer again; run vaultsync pair in a terminal. It said: Pass --vault") || !reflect.DeepEqual(ask.sequence(), []string{"code", "vault", "inform"}) {
		t.Fatalf("asked twice: %q %v %v", line, err, ask.sequence())
	}
	agent.set(func(f *fakeAgent) {
		if len(f.pairs) != 2 {
			t.Fatalf("requests: %+v", f.pairs)
		}
	})

	// A refusal the app has no dialog for (create, or one it does not
	// know) is shown as it is; the agent's own failure likewise, and it is
	// the error.
	agent = newFakeAgent(t)
	agent.set(func(f *fakeAgent) {
		f.pair = func(PairRequest) (PairResult, int) {
			return PairResult{Refusal: "Your Hub has no vault named “Work”. Add --create to start it as a new vault.", Needs: "create"}, 200
		}
	})
	ask = &fakeAsker{code: "TULIP-ANCHOR-42"}
	if line, err := Pair(ctx, NewClient(agent.socket), ask); err != nil || !strings.HasPrefix(line, "Your Hub no longer has the vault you chose.") || strings.Contains(line, "--create") || ask.told[0] != line {
		t.Fatalf("create: %q %v", line, err)
	}
	agent.set(func(f *fakeAgent) {
		f.pair = func(PairRequest) (PairResult, int) {
			return PairResult{Refusal: "a pairing is running on this computer — wait for it to finish, then try again"}, 409
		}
	})
	ask = &fakeAsker{code: "TULIP-ANCHOR-42"}
	if line, err := Pair(ctx, NewClient(agent.socket), ask); err == nil || line != "" || !strings.Contains(err.Error(), "wait for it to finish") || ask.told[0] != err.Error() {
		t.Fatalf("busy: %q %v %v", line, err, ask.told)
	}
	agent.stop()
	ask = &fakeAsker{code: "TULIP-ANCHOR-42"}
	if _, err := Pair(ctx, NewClient(agent.socket), ask); !errors.Is(err, ErrNoAgent) || len(ask.told) != 1 {
		t.Fatalf("no agent: %v %v", err, ask.told)
	}

	// Out of time: no dialog is opened on a context that is done, and the
	// menu's notice says what happened in its own words.
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	ask = &fakeAsker{code: "TULIP-ANCHOR-42"}
	if _, err := Pair(expired, NewClient(agent.socket), ask); !errors.Is(err, context.Canceled) || len(ask.told) != 0 {
		t.Fatalf("a context that is done: %v told %v", err, ask.told)
	}
	if got := pairingOutcome("", context.DeadlineExceeded); got != pairingTimeoutNotice {
		t.Fatalf("the time limit: %q", got)
	}
	if got := pairingOutcome("", context.Canceled); got != "" {
		t.Fatalf("a quit: %q", got)
	}
	if got := pairingOutcome("", errors.New("boom")); got != "boom" {
		t.Fatalf("a failure: %q", got)
	}
	if got := pairingOutcome("✓ done", nil); got != "✓ done" {
		t.Fatalf("success: %q", got)
	}
}

func TestIssue176_TrayRunsAPairingWhileTheMenuGoesOn(t *testing.T) {
	agent := newFakeAgent(t)
	agent.set(func(f *fakeAgent) {
		f.hubs = 1
		f.status.Hubs = []Hub{{Name: "Office Hub", State: "connected", Kind: control.KindOK}}
		f.pair = pairingScript(t, "TULIP-ANCHOR-42")
	})
	ui := newFakeUI(t)
	ask := &fakeAsker{code: "TULIP-ANCHOR-42", agree: true, release: make(chan struct{}), choose: func(m Menu) (Choice, bool) { return Choice{Local: true, Entry: m.Local[0]}, true }}
	var mu sync.Mutex
	var notes []string
	controllerFor(t, agent, ui, &Controller{Asker: ask, Notify: func(_ context.Context, s string) { mu.Lock(); notes = append(notes, s); mu.Unlock() }})
	ui.view(t, "connected", func(v View) bool { return v.Headline == "Connected — no vault yet" })
	ui.click(EventPair, 0)
	v := ui.view(t, "pairing", func(v View) bool { return v.Pair.Title == "Pairing…" })
	if v.Pair.Enabled || !v.Pause.Enabled {
		t.Fatalf("while pairing: %+v", v)
	}
	// The menu keeps working meanwhile, and a second click starts nothing.
	ui.click(EventPair, 0)
	ui.click(EventOpened, 0)
	ui.view(t, "still pairing", func(v View) bool { return v.Pair.Title == "Pairing…" })
	agent.set(func(f *fakeAgent) {
		f.status.Vaults = []Vault{{Label: "Work", Path: "/home/me/Work", State: "syncing", Kind: control.KindBusy}}
	})
	close(ask.release)
	v = ui.view(t, "done", func(v View) bool { return v.Pair.Enabled && v.Notice != "" })
	if !strings.HasPrefix(v.Notice, "✓ “Work” is set up") || v.Headline != "Syncing 1 vault" {
		t.Fatalf("after the pairing: %+v", v)
	}
	if got := ask.sequence(); !reflect.DeepEqual(got, []string{"code", "vault", "consent", "inform"}) {
		t.Fatalf("asked: %v", got)
	}

	// An agent gone since the last poll is found before the code is asked
	// for: no dialog opens for nobody.
	gone := newFakeAgent(t)
	gone.set(func(f *fakeAgent) {
		f.hubs = 1
		f.status.Hubs = []Hub{{Name: "Office Hub", State: "connected", Kind: control.KindOK}}
	})
	uiGone := newFakeUI(t)
	askGone := &fakeAsker{code: "TULIP-ANCHOR-42"}
	controllerFor(t, gone, uiGone, &Controller{Asker: askGone})
	uiGone.view(t, "connected", func(v View) bool { return v.Pair.Enabled })
	gone.stop()
	uiGone.click(EventPair, 0)
	uiGone.view(t, "not running", func(v View) bool { return v.Headline == "VaultSync is not running" })
	if got := askGone.sequence(); len(got) != 0 {
		t.Fatalf("a dialog for an agent that is gone: %v", got)
	}

	// Without dialogs there is no pairing from the menu, and a click does
	// not reach the agent.
	ui2 := newFakeUI(t)
	agent2 := newFakeAgent(t)
	agent2.set(func(f *fakeAgent) {
		f.hubs = 1
		f.status.Hubs = []Hub{{Name: "Office Hub", State: "connected", Kind: control.KindOK}}
		f.pair = pairingScript(t, "x")
	})
	controllerFor(t, agent2, ui2, &Controller{Layout: agent2.layout}) // the journal: no log to open
	v = ui2.view(t, "no dialogs", func(v View) bool { return v.Headline == "Connected — no vault yet" })
	if v.Pair.Enabled || !strings.Contains(v.Pair.Title, "terminal") || v.Log.Title != "" {
		t.Fatalf("linux without zenity: %+v", v)
	}
	// A log folder without the file (an agent run from a terminal, the
	// service never installed): nothing to open either.
	ui3 := newFakeUI(t)
	noFile := agent2.layout
	noFile.Logs = t.TempDir()
	controllerFor(t, agent2, ui3, &Controller{Layout: noFile})
	if v := ui3.view(t, "no log file", func(v View) bool { return v.Headline == "Connected — no vault yet" }); v.Log.Title != "" {
		t.Fatalf("a log item without a file: %+v", v)
	}
	ui2.click(EventPair, 0)
	ui2.click(EventOpened, 0)
	ui2.view(t, "unchanged", func(v View) bool { return v.Headline == "Connected — no vault yet" })
	agent2.set(func(f *fakeAgent) {
		if len(f.pairs) != 0 {
			t.Fatalf("a pairing without dialogs: %+v", f.pairs)
		}
	})
}

func TestIssue176_TrayDialogsCarryTextAsData(t *testing.T) {
	// AppleScript: every character a script could read as code is escaped.
	if got := appleScriptString("say \"hi\" \\ there\nnext\ttab"); got != `"say \"hi\" \\ there\nnext\ttab"` {
		t.Fatalf("appleScriptString: %s", got)
	}
	if got := appleScriptList([]string{"a", `b"c`}); got != `{"a", "b\"c"}` {
		t.Fatalf("appleScriptList: %s", got)
	}
	// zenity: the markup it would interpret is escaped.
	if got := pangoText("R&D <notes>"); got != "R&amp;D &lt;notes&gt;" {
		t.Fatalf("pangoText: %s", got)
	}
	// The menu's lines are numbered so a choice is found by number alone.
	m := Menu{Local: []MenuEntry{{Label: "Work", Path: "/w", Note: "~/w"}, {Label: "Work", Path: "/w2"}}, Hub: []MenuEntry{{Label: "Notes", Vault: "vs-1", Note: "1 file on your Hub", Default: "/home/me/Vaults/Notes"}}}
	items, choices := menuItems(m, "/home/me")
	if !reflect.DeepEqual(items, []string{"1) Work — on this computer, ~/w", "2) Work — on this computer", "3) Notes — on your Hub → ~/Vaults/Notes, 1 file on your Hub"}) {
		t.Fatalf("items: %q", items)
	}
	if c, ok := chosen("2) Work — on this computer", choices); !ok || !c.Local || c.Entry.Path != "/w2" {
		t.Fatalf("chosen 2: %+v %v", c, ok)
	}
	if c, ok := chosen("3) Notes — on your Hub → ~/Vaults/Notes, 1 file on your Hub", choices); !ok || c.Local || c.Entry.Vault != "vs-1" {
		t.Fatalf("chosen 3: %+v %v", c, ok)
	}
	for _, bad := range []string{"", "4) x", "x) y", "false"} {
		if _, ok := chosen(bad, choices); ok {
			t.Fatalf("%q chosen", bad)
		}
	}
	if p := menuPrompt(Menu{Already: []MenuEntry{{Label: "Journal", Note: "~/Journal"}}, Unavailable: []MenuEntry{{Label: "Cloudy", Note: "iCloud"}}, Note: "Obsidian’s list could not be read"}); !strings.Contains(p, "Already syncing: Journal (~/Journal)") || !strings.Contains(p, "Unavailable: Cloudy (iCloud)") || !strings.Contains(p, "Obsidian’s list") {
		t.Fatalf("prompt: %s", p)
	}

	// osascript: a cancel (-128) is no error; any other failure is; the
	// consent is yes only for its own button.
	type call struct {
		name, stdin string
		args        []string
	}
	var calls []call
	script := func(stdout, stderr string, status int) runner {
		return func(_ context.Context, name string, args []string, stdin string) (string, string, int, error) {
			calls = append(calls, call{name, stdin, args})
			return stdout, stderr, status, nil
		}
	}
	ctx := context.Background()
	a := &scriptAsker{run: script("TULIP-ANCHOR-42\n", "", 0)}
	if code, ok, err := a.AskCode(ctx); err != nil || !ok || code != "TULIP-ANCHOR-42" {
		t.Fatalf("code: %q %v %v", code, ok, err)
	}
	if c := calls[len(calls)-1]; c.name != "osascript" || len(c.args) != 0 || !strings.Contains(c.stdin, appleScriptString(codePrompt)) || !strings.Contains(c.stdin, "return text returned of r") {
		t.Fatalf("the code script: %+v", c)
	}
	// Activated first, so the dialog comes to the front — without an Apple
	// event to another app, which would need the Automation consent.
	if c := calls[len(calls)-1]; !strings.HasPrefix(c.stdin, "activate\n") || strings.Contains(c.stdin, "System Events") {
		t.Fatalf("the dialog is not brought to the front on its own:\n%s", c.stdin)
	}
	a = &scriptAsker{run: script("", "execution error: User canceled. (-128)\n", 1)}
	if _, ok, err := a.AskCode(ctx); ok || err != nil {
		t.Fatalf("cancel: %v %v", ok, err)
	}
	if ok, err := a.Confirm(ctx, "x"); ok || err != nil {
		t.Fatalf("cancel at the consent: %v %v", ok, err)
	}
	a = &scriptAsker{run: script("", "syntax error", 1)}
	if _, _, err := a.AskCode(ctx); err == nil || !strings.Contains(err.Error(), "code dialog could not be shown") {
		t.Fatalf("a failing script: %v", err)
	}
	// A number that merely contains -128 is not a cancel.
	a = &scriptAsker{run: script("", "execution error: System Events got an error: item -1280 not found", 1)}
	if _, ok, err := a.AskCode(ctx); ok || err == nil {
		t.Fatalf("a failure with -1280 in it read as a cancel: %v %v", ok, err)
	}
	a = &scriptAsker{run: script("", "", 0)}
	if ok, err := a.Confirm(ctx, "Folder X already contains files."); !ok || err != nil {
		t.Fatalf("consent: %v %v", ok, err)
	}
	if c := calls[len(calls)-1]; !strings.Contains(c.stdin, `default button "Cancel"`) || !strings.Contains(c.stdin, `if button returned of r is not "Start syncing" then error number -128`) || !strings.Contains(c.stdin, appleScriptString("Folder X already contains files.")) {
		t.Fatalf("the consent script: %s", c.stdin)
	}
	a = &scriptAsker{run: script("3) Notes — on your Hub → ~/Vaults/Notes, 1 file on your Hub\n", "", 0), home: "/home/me"}
	if c, ok, err := a.ChooseVault(ctx, m); err != nil || !ok || c.Entry.Vault != "vs-1" {
		t.Fatalf("choose: %+v %v %v", c, ok, err)
	}
	if c := calls[len(calls)-1]; !strings.Contains(c.stdin, appleScriptList(items)) || !strings.Contains(c.stdin, "if c is false then error number -128") {
		t.Fatalf("the list script: %s", c.stdin)
	}
	a = &scriptAsker{run: script("", "", 0)}
	if c, ok, err := a.ChooseVault(ctx, Menu{}); ok || err != nil || c != (Choice{}) {
		t.Fatalf("an empty menu: %+v %v %v", c, ok, err)
	}
	if c := calls[len(calls)-1]; !strings.Contains(c.stdin, "Nothing to choose") {
		t.Fatalf("an empty menu is told: %s", c.stdin)
	}
	a = &scriptAsker{run: script("9) nothing\n", "", 0)}
	if _, _, err := a.ChooseVault(ctx, m); err == nil {
		t.Fatal("a line not in the list")
	}

	// zenity: one argument per text, status 1 is a cancel, anything else
	// but 0 a failure.
	z := &zenityAsker{run: script("TULIP-ANCHOR-42\n", "", 0)}
	if code, ok, err := z.AskCode(ctx); err != nil || !ok || code != "TULIP-ANCHOR-42" {
		t.Fatalf("zenity code: %q %v %v", code, ok, err)
	}
	if c := calls[len(calls)-1]; c.name != "zenity" || !reflect.DeepEqual(c.args, []string{"--title=VaultSync", "--entry", "--text=" + codePrompt}) || c.stdin != "" {
		t.Fatalf("zenity args: %+v", c)
	}
	z = &zenityAsker{run: script("", "", 0)}
	if ok, err := z.Confirm(ctx, "R&D already contains files."); !ok || err != nil {
		t.Fatalf("zenity consent: %v %v", ok, err)
	}
	if c := calls[len(calls)-1]; !reflect.DeepEqual(c.args, []string{"--title=VaultSync", "--question", "--no-markup", "--default-cancel", "--ok-label=Start syncing", "--cancel-label=Cancel", "--width=480", "--text=R&D already contains files."}) {
		t.Fatalf("zenity consent args: %q", c.args)
	}
	z = &zenityAsker{run: script("", "", 1)}
	if ok, err := z.Confirm(ctx, "x"); ok || err != nil {
		t.Fatalf("zenity cancel: %v %v", ok, err)
	}
	z = &zenityAsker{run: script("", "", 5)}
	if _, err := z.Confirm(ctx, "x"); err == nil {
		t.Fatal("zenity timeout is a failure")
	}
	z = &zenityAsker{run: script("3) Notes — on your Hub → ~/Vaults/Notes, 1 file on your Hub\n", "", 0), home: "/home/me"}
	if c, ok, err := z.ChooseVault(ctx, m); err != nil || !ok || c.Entry.Vault != "vs-1" {
		t.Fatalf("zenity choose: %+v %v %v", c, ok, err)
	}
	// Continue without a row: nothing chosen, as a cancel — not a dialog
	// that could not be shown.
	z = &zenityAsker{run: script("\n", "", 0), home: "/home/me"}
	if c, ok, err := z.ChooseVault(ctx, m); err != nil || ok || c != (Choice{}) {
		t.Fatalf("zenity without a row: %+v %v %v", c, ok, err)
	}
	if c := calls[len(calls)-1]; !reflect.DeepEqual(c.args[len(c.args)-3:], items) || !strings.Contains(strings.Join(c.args, " "), "--column=Vault") {
		t.Fatalf("zenity list args: %q", c.args)
	}

	// Which dialogs a computer has.
	has := func(names ...string) func(string) (string, error) {
		return func(n string) (string, error) {
			for _, h := range names {
				if h == n {
					return "/usr/bin/" + n, nil
				}
			}
			return "", errors.New("not found")
		}
	}
	if _, ok := NewAsker("darwin", "/home/me", has()).(*scriptAsker); !ok {
		t.Fatal("a Mac asks through osascript")
	}
	if _, ok := NewAsker("linux", "/home/me", has("zenity")).(*zenityAsker); !ok {
		t.Fatal("Linux with zenity asks through it")
	}
	if NewAsker("linux", "/home/me", has()) != nil || NewAsker("windows", "/home/me", has("zenity")) != nil {
		t.Fatal("no dialogs elsewhere")
	}
	// Notifications: a Mac through osascript, Linux through notify-send
	// when it is there, else none.
	if name, _, stdin, ok := notifyCommand("darwin", has(), "Paused \"now\""); !ok || name != "osascript" || !strings.Contains(stdin, `display notification "Paused \"now\"" with title "VaultSync"`) {
		t.Fatalf("mac notification: %s %q %v", name, stdin, ok)
	}
	if name, args, _, ok := notifyCommand("linux", has("notify-send"), "-paused"); !ok || name != "notify-send" || !reflect.DeepEqual(args, []string{"--", "VaultSync", "-paused"}) {
		t.Fatalf("linux notification: %s %q %v", name, args, ok)
	}
	if _, _, _, ok := notifyCommand("linux", has(), "Paused"); ok {
		t.Fatal("no notify-send, no notification")
	}

	// A program the context kills reports the context, not an exit status
	// the kill left; a program that fails without a word is named with
	// its status.
	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if _, _, _, err := execRunner(short, "sleep", []string{"5"}, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a killed program: %v", err)
	}
	inTime, cancelInTime := context.WithTimeout(ctx, 5*time.Second)
	defer cancelInTime()
	if _, _, status, err := execRunner(inTime, "true", nil, ""); err != nil || status != 0 {
		t.Fatalf("a program that finished in time: %v %d", err, status)
	}
	// A program that is done but left a child holding its pipes (xdg-open
	// and the file manager it started) is what it did, not a failure.
	if _, _, status, err := execRunner(ctx, "sh", []string{"-c", "sleep 3 & exit 0"}, ""); err != nil || status != 0 {
		t.Fatalf("a program whose child keeps the pipes: %v %d", err, status)
	}
	if _, _, status, err := execRunner(ctx, "sh", []string{"-c", "sleep 3 & exit 4"}, ""); err != nil || status != 4 {
		t.Fatalf("its exit status still counts: %v %d", err, status)
	}
	// A program that failed on its own is its failure, also with a limit
	// that is near — and even when the limit passed as it ended: only a
	// kill (exit code -1) is the context's doing.
	near, cancelNear := context.WithTimeout(ctx, 5*time.Second)
	defer cancelNear()
	if _, _, status, err := execRunner(near, "sh", []string{"-c", "exit 3"}, ""); err != nil || status != 3 {
		t.Fatalf("a program that failed on its own: %v %d", err, status)
	}
	exited3 := exec.Command("sh", "-c", "exit 3").Run()
	if status, err := outcome(exited3, context.DeadlineExceeded, 3); err != nil || status != 3 {
		t.Fatalf("failed on its own as the limit passed: %v %d", err, status)
	}
	if _, err := outcome(errors.New("signal: killed"), context.DeadlineExceeded, -1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("killed by the limit: %v", err)
	}
	if status, err := outcome(exec.ErrWaitDelay, nil, 0); err != nil || status != 0 {
		t.Fatalf("pipes left to a child: %v %d", err, status)
	}
	if _, err := outcome(errors.New("fork: no such file"), nil, -1); err == nil {
		t.Fatal("a program that could not run")
	}
	silent := func(_ context.Context, name string, args []string, stdin string) (string, string, int, error) {
		return "", "", 3, nil
	}
	if err := openWith("linux", silent)(ctx, "/home/me/Notes"); err == nil || err.Error() != "xdg-open exited with status 3" {
		t.Fatalf("a silent failure: %v", err)
	}
	loud := func(_ context.Context, name string, args []string, stdin string) (string, string, int, error) {
		return "", "no application to open it\n", 4, nil
	}
	if err := openWith("linux", loud)(ctx, "/home/me/Notes"); err == nil || err.Error() != "no application to open it" {
		t.Fatalf("a failure with words: %v", err)
	}
}

func TestIssue176_TrayIconsAreFiveDistinctShapes(t *testing.T) {
	glyphs := []Glyph{GlyphOffline, GlyphSynced, GlyphSyncing, GlyphPaused, GlyphAttention}
	seen := map[string]Glyph{}
	for _, g := range glyphs {
		for _, template := range []bool{true, false} {
			data := Icon(g, template)
			img, err := png.Decode(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("%s: %v", g, err)
			}
			if b := img.Bounds(); b.Dx() != iconSize || b.Dy() != iconSize {
				t.Fatalf("%s: %v", g, b)
			}
			inked, outside := 0, 0
			for y := 0; y < iconSize; y++ {
				for x := 0; x < iconSize; x++ {
					r, g2, b2, a := img.At(x, y).RGBA()
					if a == 0 {
						outside++
						continue
					}
					inked++
					if template && (r != 0 || g2 != 0 || b2 != 0) {
						t.Fatalf("%s: a template icon is black, got %d %d %d", g, r, g2, b2)
					}
					if !template && a == 0xffff && (r>>8 != 0x8e || g2>>8 != 0x8e) {
						t.Fatalf("%s: the plain icon is grey, got %d %d %d", g, r>>8, g2>>8, b2>>8)
					}
				}
			}
			if inked < 100 || outside < 100 {
				t.Fatalf("%s: %d inked, %d clear", g, inked, outside)
			}
			if other, dup := seen[string(data)]; dup {
				t.Fatalf("%s (template %v) looks like %s", g, template, other)
			}
			seen[string(data)] = g
			if !bytes.Equal(Icon(g, template), data) {
				t.Fatalf("%s: drawn differently twice", g)
			}
		}
	}
}

func TestIssue176_TrayPairClickFindsASecondInstance(t *testing.T) {
	// A second instance found by the refresh a Pair click makes ends the
	// app as a poll's refresh would — not at the next tick, and with no
	// dialog opened beside the other instance.
	agent := newFakeAgent(t)
	agent.set(func(f *fakeAgent) {
		f.hubs = 1
		f.status.Hubs = []Hub{{Name: "Office Hub", State: "connected", Kind: control.KindOK}}
	})
	ui := newFakeUI(t)
	ask := &fakeAsker{code: "TULIP-ANCHOR-42"}
	var mu sync.Mutex
	held := &fakeLock{valid: true}
	other := false
	c := &Controller{Home: "/home/me", Layout: agent.layout, UI: ui, Asker: ask, Poll: time.Hour, StatusTimeout: 5 * time.Second, Lock: func(layout.Layout) (LockHandle, error) {
		mu.Lock()
		defer mu.Unlock()
		if other {
			return nil, ErrAlreadyRunning
		}
		return held, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	ui.view(t, "connected", func(v View) bool { return v.Pair.Enabled })
	// The lock's file replaced under it, the other instance holding the
	// new one: the next look lets go and finds it held.
	mu.Lock()
	held.mu.Lock()
	held.valid = false
	held.mu.Unlock()
	other = true
	mu.Unlock()
	ui.click(EventPair, 0)
	select {
	case err := <-done:
		if !errors.Is(err, ErrAlreadyRunning) {
			t.Fatalf("run ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the app went on beside the other instance")
	}
	if got := ask.sequence(); len(got) != 0 {
		t.Fatalf("a dialog opened beside the other instance: %v", got)
	}
}

func TestIssue176_TrayOneInstancePerUser(t *testing.T) {
	dir := t.TempDir()
	lay := layout.Layout{}.At(dir)
	// Not set up (no agent.json): nothing to lock, nothing made.
	if l, err := TakeLock(lay); err != nil || l != nil {
		t.Fatalf("before setup: %v %v", err, l)
	}
	if _, err := os.Stat(filepath.Join(dir, "tray.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a lock file before setup")
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.json"), []byte(`{"version":1,"guiPort":8384}`), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := TakeLock(lay)
	if err != nil || l == nil || !l.Valid() {
		t.Fatal(err, l)
	}
	if _, err := TakeLock(lay); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("a second one: %v", err)
	}
	l.Release()
	l, err = TakeLock(lay)
	if err != nil || l == nil {
		t.Fatalf("after release: %v %v", err, l)
	}
	// The file taken away while held: the lock no longer guards the place,
	// and a fresh one can be taken next to it.
	if err := os.Remove(filepath.Join(dir, "tray.lock")); err != nil {
		t.Fatal(err)
	}
	if l.Valid() {
		t.Fatal("a lock on a file that is gone reads as valid")
	}
	l2, err := TakeLock(lay)
	if err != nil || l2 == nil || !l2.Valid() {
		t.Fatalf("a fresh lock after the file was taken away: %v", err)
	}
	l.Release()
	l2.Release()
	missing := filepath.Join(dir, "nothing-set-up")
	if l, err := TakeLock(layout.Layout{}.At(missing)); err != nil || l != nil {
		t.Fatalf("no folder yet: %v %v", err, l)
	}
	// Being taken apart (agent.json gone, the folder still there): nothing
	// is made in it any more.
	if err := os.Remove(filepath.Join(dir, "agent.json")); err != nil {
		t.Fatal(err)
	}
	if l, err := TakeLock(lay); err != nil || l != nil {
		t.Fatalf("during an uninstall: %v %v", err, l)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the lock made VaultSync's folder")
	}
	// A folder that cannot be looked at is an error — not a lock skipped,
	// which would let a second instance run unnoticed.
	if os.Getuid() == 0 {
		t.Skip("root can look at anything")
	}
	parent := filepath.Join(dir, "closed")
	if err := os.MkdirAll(filepath.Join(parent, "VaultSync"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "VaultSync", "agent.json"), []byte(`{"version":1,"guiPort":8384}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	if _, err := TakeLock(layout.Layout{}.At(filepath.Join(parent, "VaultSync"))); err == nil || errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("an unreadable folder: %v", err)
	}
}

func TestIssue176_TrayTextHelpers(t *testing.T) {
	if got := resultLine("✓ “Notes” is set up to sync at ~/Vaults/Notes.\n  Open it in Obsidian after the first sync: Open folder as vault → ~/Vaults/Notes\n"); got != "✓ “Notes” is set up to sync at ~/Vaults/Notes." {
		t.Fatalf("the ✓ line, not the hint after it: %q", got)
	}
	// A vault on this computer goes out under the name the agent proposed
	// for it, its folder's name only when none was proposed.
	if v, create, path := requestFor(Choice{Local: true, Entry: MenuEntry{Label: "Work: 2026", Path: "/w", Name: "Work 2026"}}); v != "Work 2026" || !create || path != "/w" {
		t.Fatalf("a proposed name: %q %v %q", v, create, path)
	}
	if v, _, _ := requestFor(Choice{Local: true, Entry: MenuEntry{Label: "Work", Path: "/w"}}); v != "Work" {
		t.Fatalf("no proposed name: %q", v)
	}
	if got := resultLine("a\n\n  b  \n\n"); got != "b" {
		t.Fatalf("lastLine: %q", got)
	}
}
