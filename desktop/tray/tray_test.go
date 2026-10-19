package tray

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psimaker/vaultsync/desktop/internal/control"
)

func TestIssue176_TrayShowsWhatTheAgentReports(t *testing.T) {
	home := "/home/me"
	base := Status{Engine: "running", Hubs: []Hub{{Name: "Office Hub", State: "connected"}}}
	cases := []struct {
		name     string
		st       Status
		glyph    Glyph
		headline string
		detail   string
		pause    Action
		pair     Action
	}{
		{"up to date", withVaults(base, "up to date"), GlyphSynced, "Up to date", "", Action{"Pause Syncing", true}, Action{"Pair a Vault…", true}},
		{"connected, a vault waiting", withVaults(base, "up to date", "waiting for your Hub to take it"), GlyphSynced, "Connected", "", Action{"Pause Syncing", true}, Action{"Pair a Vault…", true}},
		{"syncing", withVaults(base, "syncing — 42 %", "up to date"), GlyphSyncing, "Syncing 1 vault", "", Action{"Pause Syncing", true}, Action{"Pair a Vault…", true}},
		{"scanning and uploading", withVaults(base, "scanning the folder", "uploading to your Hub — 3 items left"), GlyphSyncing, "Syncing 2 vaults", "", Action{"Pause Syncing", true}, Action{"Pair a Vault…", true}},
		{"an error outranks syncing", withVaults(base, "syncing", "error: permission denied — allow access to this folder (on a Mac: allow “vaultsync” in System Settings → Privacy & Security → Files & Folders), then run vaultsync stop and vaultsync start"), GlyphAttention, "Attention needed", "Vault 2: error: permission denied — allow access to this folder (on a Mac: allow “vaultsync” in System Settings → Privacy & Security → Files & Folders), then run vaultsync stop and vaultsync start", Action{"Pause Syncing", true}, Action{"Pair a Vault…", true}},
		{"files that could not sync", withVaults(base, "3 files could not sync — see the engine's log"), GlyphAttention, "Attention needed", "Vault 1: 3 files could not sync — see the engine's log", Action{"Pause Syncing", true}, Action{"Pair a Vault…", true}},
		{"no Hub paired", withVaults(Status{Engine: "running", Hubs: []Hub{}}), GlyphOffline, "No Hub paired yet", "Pair a Vault… pairs this computer with your Hub", Action{"Pause Syncing", true}, Action{"Pair a Vault…", true}},
		{"Hub not connected", withVaults(Status{Engine: "running", Hubs: []Hub{{Name: "Office Hub", State: "not connected"}}}, "waiting for your Hub"), GlyphOffline, "Waiting for your Hub", "Not connected right now; syncing resumes when it is", Action{"Pause Syncing", true}, Action{"Pair a Vault…", true}},
		{"connected through a relay", withVaults(Status{Engine: "running", Hubs: []Hub{{Name: "Office Hub", State: "connected through a relay (slower; a direct connection is not possible right now)"}}}, "up to date"), GlyphSynced, "Up to date", "", Action{"Pause Syncing", true}, Action{"Pair a Vault…", true}},
		{"connected, no vault", base, GlyphSynced, "Connected — no vault yet", "Pair a Vault… adds one", Action{"Pause Syncing", true}, Action{"Pair a Vault…", true}},
		{"paused", paused(withVaults(base, "paused on this computer", "2 items left to sync")), GlyphPaused, "Paused on this computer", "", Action{"Resume Syncing", true}, Action{"Pair a Vault… (resume syncing first)", false}},
		{"engine starting", Status{Engine: "starting", Vaults: []Vault{{Label: "Notes", Path: "/home/me/Notes", State: "not syncing — the engine is stopped"}}}, GlyphSyncing, "Starting the sync engine…", "", Action{"Pause Syncing", false}, Action{"Pair a Vault…", false}},
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
			if v.Log != (Action{"Open Log", true}) || !v.Quit.Enabled {
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
	st.Hubs[0].State = "paused on this computer — vaultsync resume starts syncing again"
	st.Waiting = []Waiting{{Label: "Journal", Hub: "Office Hub"}}
	st.Attempts = []Attempt{{Vault: "Old", What: "your Hub is sharing it; run vaultsync pair to finish setup", At: "Oct 7 15:04"}}
	v := Render(st, nil, Options{Home: home, CanAsk: true})
	if got := v.Hubs[0]; got.Text != "Office Hub — paused on this computer" || got.Tooltip != st.Hubs[0].State {
		t.Fatalf("hub line: %+v", got)
	}
	if got := v.Vaults[0]; got.Text != "Vault 1 — paused on this computer" || got.Path != "/home/me/Vault 1" || got.Tooltip != "~/Vault 1" {
		t.Fatalf("vault line: %+v", got)
	}
	if got := v.Vaults[1]; got.Text != "Vault 2 — syncing — 42 %" {
		t.Fatalf("a percentage is not a remedy: %+v", got)
	}
	// A remedy the menu has no item for stays on the line: the tooltip is
	// not shown on every desktop.
	if got := shortened("Notes — ", "error: permission denied — allow access to this folder, then run vaultsync stop and vaultsync start"); got.Text != "Notes — error: permission denied — allow access to this folder, then run vaultsync stop and vaultsync start" || got.Tooltip != "" {
		t.Fatalf("a remedy without an item: %+v", got)
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
		{ErrNoSocket, GlyphOffline, "VaultSync does not answer"},
		{ErrOldAgent, GlyphAttention, "VaultSync needs an update on this computer"},
		{errors.New("the sync engine does not answer: boom"), GlyphAttention, "VaultSync could not report"},
	} {
		v := Render(Status{}, c.err, Options{CanAsk: true, LogPath: "/l/vaultsync.log"})
		if v.Glyph != c.glyph || v.Headline != c.headline || v.Detail == "" || v.Pause.Enabled || v.Pair.Enabled || !v.Quit.Enabled || !v.Log.Enabled || v.NoVaultLine != "" {
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
		st.Vaults = append(st.Vaults, Vault{ID: "id" + label, Label: label, Path: "/home/me/" + label, State: s})
	}
	return st
}

func paused(st Status) Status {
	st.Paused = true
	return st
}

func TestIssue176_TrayFindsTheAgentWhereTheAgentSaysItIs(t *testing.T) {
	dir := t.TempDir()
	if _, err := SocketPath(dir); !errors.Is(err, ErrNotSetUp) {
		t.Fatalf("no agent.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.json"), []byte(`{"version":1,"guiPort":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SocketPath(dir); !errors.Is(err, ErrNotSetUp) {
		t.Fatalf("no port yet: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.json"), []byte(`{"version":1,"guiPort":8384}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, err := SocketPath(dir); err != nil || p != filepath.Join(dir, "agent.sock") {
		t.Fatalf("the usual place: %q %v", p, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.json"), []byte(`{"version":1,"guiPort":8384,"controlSocket":"/run/user/1000/vaultsync-x/agent.sock"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, err := SocketPath(dir); err != nil || p != "/run/user/1000/vaultsync-x/agent.sock" {
		t.Fatalf("the recorded place: %q %v", p, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.json"), []byte(`{"version":1,`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SocketPath(dir); err == nil || errors.Is(err, ErrNotSetUp) || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("a damaged agent.json is reported, not read as unset: %v", err)
	}

	for _, c := range []struct{ goos, home, xdg, want string }{
		{"darwin", "/Users/me", "", "/Users/me/Library/Application Support/VaultSync"},
		{"linux", "/home/me", "", "/home/me/.local/state/vaultsync"},
		{"linux", "/home/me", "/var/state", "/var/state/vaultsync"},
		{"linux", "/home/me", "relative", "/home/me/.local/state/vaultsync"},
	} {
		got, err := StateDir(c.goos, c.home, func(k string) string {
			if k == "XDG_STATE_HOME" {
				return c.xdg
			}
			return ""
		})
		if err != nil || got != c.want {
			t.Fatalf("%s %s %q: %q %v", c.goos, c.home, c.xdg, got, err)
		}
	}
	if _, err := StateDir("windows", `C:\Users\me`, func(string) string { return "" }); err == nil {
		t.Fatal("windows comes later")
	}
	if LogPath("darwin", "/Users/me") != "/Users/me/Library/Logs/VaultSync/vaultsync.log" || LogPath("linux", "/home/me") != "" {
		t.Fatal("log paths")
	}
}

func TestIssue176_TrayClientTalksToTheAgentAlone(t *testing.T) {
	agent := newFakeAgent(t)
	agent.set(func(f *fakeAgent) {
		f.status.Hubs = []Hub{{ID: "h", Name: "Office Hub", State: "connected"}}
		f.status.Vaults = []Vault{{ID: "v", Label: "Notes", Path: "/home/me/Notes", State: "up to date"}}
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
	if c.GOOS == "" {
		c.GOOS = "darwin"
	}
	c.Home = "/home/me"
	c.StateDir = agent.stateDir
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
		f.status.Hubs = []Hub{{Name: "Office Hub", State: "connected"}}
		f.status.Vaults = []Vault{{Label: "Notes", Path: "/home/me/Notes", State: "up to date"}}
	})
	ui := newFakeUI()
	var notes []string
	var mu sync.Mutex
	controllerFor(t, agent, ui, &Controller{Notify: func(_ context.Context, s string) { mu.Lock(); notes = append(notes, s); mu.Unlock() }})
	ui.view(t, "up to date", func(v View) bool { return v.Headline == "Up to date" })

	ui.click(EventPause, 0)
	v := ui.view(t, "paused", func(v View) bool { return v.Glyph == GlyphPaused })
	if v.Pause.Title != "Resume Syncing" || v.Notice != "" {
		t.Fatalf("after pause: %+v", v)
	}
	ui.click(EventPause, 0)
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
	ui.click(EventPause, 0)
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
	ui.click(EventPause, 0)
	ui.click(EventOpened, 0)
	ui.view(t, "still not running", func(v View) bool { return v.Headline == "VaultSync is not running" })
	agent.set(func(f *fakeAgent) {
		if len(f.calls) != 3 {
			t.Fatalf("calls after the agent stopped: %v", f.calls)
		}
	})

	// The agent recorded that it could not make its socket: nobody
	// answering then does not mean nobody runs, and the menu says so.
	if err := os.WriteFile(filepath.Join(agent.stateDir, "control-socket-error.txt"), []byte("too deep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ui.click(EventOpened, 0)
	v = ui.view(t, "no socket", func(v View) bool { return v.Headline == "VaultSync does not answer" })
	if !strings.Contains(v.Detail, "control-socket-error.txt") || v.Glyph != GlyphOffline {
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
	ui := newFakeUI()
	var mu sync.Mutex
	var tries int
	var handles []*fakeLock
	folderThere := false
	lock := func(dir string) (LockHandle, error) {
		mu.Lock()
		defer mu.Unlock()
		tries++
		if dir != agent.stateDir {
			t.Errorf("locking %q", dir)
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

	// A second instance found running ends the loop with its error.
	ui2 := newFakeUI()
	c2 := &Controller{GOOS: "darwin", Home: "/home/me", StateDir: agent.stateDir, UI: ui2, Poll: time.Hour, Lock: func(string) (LockHandle, error) { return nil, ErrAlreadyRunning }}
	if err := c2.Run(context.Background()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("a second instance: %v", err)
	}
	// One already held at start is never asked for again while it is
	// valid, and released on the way out.
	start := &fakeLock{valid: true}
	c3 := &Controller{GOOS: "darwin", Home: "/home/me", StateDir: agent.stateDir, UI: newFakeUI(), Poll: time.Hour, Held: start, Lock: func(string) (LockHandle, error) { t.Error("asked again"); return nil, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c3.Run(ctx); !errors.Is(err, context.Canceled) || !start.released {
		t.Fatalf("held at start: %v released %v", err, start.released)
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
	ui := newFakeUI()
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
		f.status.Hubs = []Hub{{Name: "Office Hub", State: "connected"}}
		f.status.Vaults = []Vault{{Label: "Notes", Path: "/home/me/Notes", State: "up to date"}, {Label: "Odd", Path: "relative/path", State: "up to date"}}
	})
	ui := newFakeUI()
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
	ui.click(EventOpenVault, 0)
	ui.click(EventOpenLog, 0)
	ui.click(EventOpenVault, 7) // no such line
	ui.click(EventOpenVault, 1) // the opener refuses it
	v := ui.view(t, "the refusal", func(v View) bool { return strings.HasPrefix(v.Notice, "Could not open it") })
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"/home/me/Notes", "/home/me/Library/Logs/VaultSync/vaultsync.log", "relative/path"}; !reflect.DeepEqual(opened, want) {
		t.Fatalf("opened %v, want %v", opened, want)
	}
	if !strings.Contains(v.Notice, "not an absolute path") {
		t.Fatalf("notice: %q", v.Notice)
	}

	for _, c := range []struct{ goos, path, name string }{
		{"darwin", "/home/me/Notes", "open"},
		{"linux", "/home/me/Notes", "xdg-open"},
		{"darwin", "Notes", ""},
		{"darwin", "", ""},
		{"windows", `C:\Notes`, ""},
	} {
		name, args, err := openCommand(c.goos, c.path)
		if c.name == "" {
			if err == nil {
				t.Fatalf("%s %q opened", c.goos, c.path)
			}
			continue
		}
		if err != nil || name != c.name || !reflect.DeepEqual(args, []string{c.path}) {
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
			return PairResult{Refusal: "~/Work already contains files. VaultSync would sync it with your Hub as the new vault “Work”, and edits and deletions on connected devices would also change this folder. Pass --yes to agree.", Needs: "yes"}, 200
		}
		return PairResult{OK: true, Output: "✓ Connected to “Office Hub”\n\n✓ “" + req.Vault + "” is set up to sync at " + req.Path + ". First sync is starting; check vaultsync status.\n"}, 200
	}
}

func TestIssue176_TrayPairingAsksOnlyWhatTheAgentNeeds(t *testing.T) {
	ctx := context.Background()
	home := "/home/me"
	chooseLocal := func(m Menu) (Choice, bool) { return Choice{Local: true, Entry: m.Local[0]}, true }
	chooseHub := func(m Menu) (Choice, bool) { return Choice{Entry: m.Hub[0]}, true }

	// A vault from this computer, with files: code → menu → consent → done.
	agent := newFakeAgent(t)
	agent.set(func(f *fakeAgent) { f.pair = pairingScript(t, "TULIP-ANCHOR-42") })
	ask := &fakeAsker{code: "TULIP-ANCHOR-42", choose: chooseLocal, agree: true}
	line, err := Pair(ctx, NewClient(agent.socket), ask, home)
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
			{Code: "TULIP-ANCHOR-42", Vault: "Work", Create: true, Path: "/home/me/Work", Yes: true},
		}
		if !reflect.DeepEqual(f.pairs, want) {
			t.Fatalf("requests:\n%+v\nwant\n%+v", f.pairs, want)
		}
	})
	if len(ask.texts) != 1 || !strings.Contains(ask.texts[0], "~/Work already contains files") || strings.Contains(ask.texts[0], "--yes") || !strings.Contains(ask.texts[0], "Obsidian Sync") {
		t.Fatalf("the consent text: %q", ask.texts)
	}
	if len(ask.menus) != 1 || ask.menus[0].Hub[0].Vault != "vs-aaaaaaaaaaaa" || len(ask.menus[0].Unavailable) != 1 {
		t.Fatalf("the menu shown: %+v", ask.menus)
	}
	if ask.told[0] != line {
		t.Fatalf("told: %v", ask.told)
	}

	// Consent refused: yes is never sent, nothing changed.
	agent = newFakeAgent(t)
	agent.set(func(f *fakeAgent) { f.pair = pairingScript(t, "TULIP-ANCHOR-42") })
	ask = &fakeAsker{code: "TULIP-ANCHOR-42", choose: chooseLocal, agree: false}
	if line, err := Pair(ctx, NewClient(agent.socket), ask, home); err != nil || line != "" {
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
	if line, err := Pair(ctx, NewClient(agent.socket), ask, home); err != nil || !strings.Contains(line, "“vs-aaaaaaaaaaaa” is set up to sync at /home/me/Vaults/Notes") {
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
	if line, err := Pair(ctx, NewClient(agent.socket), ask, home); err != nil || !strings.Contains(line, "did not say where to put Notes") || !reflect.DeepEqual(ask.sequence(), []string{"code", "vault", "inform"}) {
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
	line, err = Pair(ctx, NewClient(agent.socket), ask, home)
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
	if line, err := Pair(ctx, NewClient(agent.socket), ask, home); err != nil || line != "" {
		t.Fatalf("cancelled: %q %v", line, err)
	}
	ask = &fakeAsker{code: "  ", cancel: ""}
	if line, err := Pair(ctx, NewClient(agent.socket), ask, home); err != nil || !strings.HasPrefix(line, "Enter the code your Hub printed") || len(ask.told) != 1 {
		t.Fatalf("an empty code is said, not swallowed: %q %v %v", line, err, ask.told)
	}
	ask = &fakeAsker{code: "TULIP-ANCHOR-42", cancel: "vault"}
	if line, err := Pair(ctx, NewClient(agent.socket), ask, home); err != nil || line != "" {
		t.Fatalf("cancelled at the menu: %q %v", line, err)
	}
	agent.set(func(f *fakeAgent) {
		if len(f.pairs) != 1 {
			t.Fatalf("requests: %+v", f.pairs)
		}
	})

	// A wrong code: the agent's refusal, shown, and the conversation ends.
	ask = &fakeAsker{code: "WRONG-CODE-00", choose: chooseLocal, agree: true}
	line, err = Pair(ctx, NewClient(agent.socket), ask, home)
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
	if line, err := Pair(ctx, NewClient(agent.socket), ask, home); err != nil || !strings.Contains(line, "is set up") {
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

	// The agent asking for the same flag twice ends the conversation
	// instead of asking the person again and again.
	agent = newFakeAgent(t)
	agent.set(func(f *fakeAgent) {
		f.pair = func(PairRequest) (PairResult, int) {
			return PairResult{Refusal: "Pass --vault NAME and --path FOLDER — there is no terminal to ask on.", Needs: "vault", Menu: &Menu{Local: []MenuEntry{{Label: "Work", Path: "/home/me/Work"}}}}, 200
		}
	})
	ask = &fakeAsker{code: "TULIP-ANCHOR-42", choose: chooseLocal}
	if line, err := Pair(ctx, NewClient(agent.socket), ask, home); err != nil || !strings.HasPrefix(line, "VaultSync asked for the same answer again; run vaultsync pair in a terminal. It said: Pass --vault") || !reflect.DeepEqual(ask.sequence(), []string{"code", "vault", "inform"}) {
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
	if line, err := Pair(ctx, NewClient(agent.socket), ask, home); err != nil || !strings.Contains(line, "Add --create") || ask.told[0] != line {
		t.Fatalf("create: %q %v", line, err)
	}
	agent.set(func(f *fakeAgent) {
		f.pair = func(PairRequest) (PairResult, int) {
			return PairResult{Refusal: "a pairing is running on this computer — wait for it to finish, then try again"}, 409
		}
	})
	ask = &fakeAsker{code: "TULIP-ANCHOR-42"}
	if line, err := Pair(ctx, NewClient(agent.socket), ask, home); err == nil || line != "" || !strings.Contains(err.Error(), "wait for it to finish") || ask.told[0] != err.Error() {
		t.Fatalf("busy: %q %v %v", line, err, ask.told)
	}
	agent.stop()
	ask = &fakeAsker{code: "TULIP-ANCHOR-42"}
	if _, err := Pair(ctx, NewClient(agent.socket), ask, home); !errors.Is(err, ErrNoAgent) || len(ask.told) != 1 {
		t.Fatalf("no agent: %v %v", err, ask.told)
	}

	// Out of time: no dialog is opened on a context that is done, and the
	// menu's notice says what happened in its own words.
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	ask = &fakeAsker{code: "TULIP-ANCHOR-42"}
	if _, err := Pair(expired, NewClient(agent.socket), ask, home); !errors.Is(err, context.Canceled) || len(ask.told) != 0 {
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
		f.status.Hubs = []Hub{{Name: "Office Hub", State: "connected"}}
		f.pair = pairingScript(t, "TULIP-ANCHOR-42")
	})
	ui := newFakeUI()
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
		f.status.Vaults = []Vault{{Label: "Work", Path: "/home/me/Work", State: "syncing"}}
	})
	close(ask.release)
	v = ui.view(t, "done", func(v View) bool { return v.Pair.Enabled && v.Notice != "" })
	if !strings.HasPrefix(v.Notice, "✓ “Work” is set up") || v.Headline != "Syncing 1 vault" {
		t.Fatalf("after the pairing: %+v", v)
	}
	if got := ask.sequence(); !reflect.DeepEqual(got, []string{"code", "vault", "consent", "inform"}) {
		t.Fatalf("asked: %v", got)
	}

	// Without dialogs there is no pairing from the menu, and a click does
	// not reach the agent.
	ui2 := newFakeUI()
	agent2 := newFakeAgent(t)
	agent2.set(func(f *fakeAgent) {
		f.hubs = 1
		f.status.Hubs = []Hub{{Name: "Office Hub", State: "connected"}}
		f.pair = pairingScript(t, "x")
	})
	controllerFor(t, agent2, ui2, &Controller{GOOS: "linux"})
	v = ui2.view(t, "no dialogs", func(v View) bool { return v.Headline == "Connected — no vault yet" })
	if v.Pair.Enabled || !strings.Contains(v.Pair.Title, "terminal") || v.Log.Title != "" {
		t.Fatalf("linux without zenity: %+v", v)
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
	m := Menu{Local: []MenuEntry{{Label: "Work", Path: "/w", Note: "~/w"}, {Label: "Work", Path: "/w2"}}, Hub: []MenuEntry{{Label: "Notes", Vault: "vs-1", Note: "1 file on your Hub"}}}
	items, choices := menuItems(m)
	if !reflect.DeepEqual(items, []string{"1) Work — on this computer, ~/w", "2) Work — on this computer", "3) Notes — on your Hub, 1 file on your Hub"}) {
		t.Fatalf("items: %q", items)
	}
	if c, ok := chosen("2) Work — on this computer", choices); !ok || !c.Local || c.Entry.Path != "/w2" {
		t.Fatalf("chosen 2: %+v %v", c, ok)
	}
	if c, ok := chosen("3) Notes — on your Hub, 1 file on your Hub", choices); !ok || c.Local || c.Entry.Vault != "vs-1" {
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
	a = &scriptAsker{run: script("", "", 0)}
	if ok, err := a.Confirm(ctx, "Folder X already contains files."); !ok || err != nil {
		t.Fatalf("consent: %v %v", ok, err)
	}
	if c := calls[len(calls)-1]; !strings.Contains(c.stdin, `default button "Cancel"`) || !strings.Contains(c.stdin, `if button returned of r is not "Start syncing" then error number -128`) || !strings.Contains(c.stdin, appleScriptString("Folder X already contains files.")) {
		t.Fatalf("the consent script: %s", c.stdin)
	}
	a = &scriptAsker{run: script("3) Notes — on your Hub, 1 file on your Hub\n", "", 0)}
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
	z = &zenityAsker{run: script("3) Notes — on your Hub, 1 file on your Hub\n", "", 0)}
	if c, ok, err := z.ChooseVault(ctx, m); err != nil || !ok || c.Entry.Vault != "vs-1" {
		t.Fatalf("zenity choose: %+v %v %v", c, ok, err)
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
	if _, ok := NewAsker("darwin", has()).(*scriptAsker); !ok {
		t.Fatal("a Mac asks through osascript")
	}
	if _, ok := NewAsker("linux", has("zenity")).(*zenityAsker); !ok {
		t.Fatal("Linux with zenity asks through it")
	}
	if NewAsker("linux", has()) != nil || NewAsker("windows", has("zenity")) != nil {
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

func TestIssue176_TrayOneInstancePerUser(t *testing.T) {
	dir := t.TempDir()
	l, err := TakeLock(dir)
	if err != nil || l == nil || !l.Valid() {
		t.Fatal(err, l)
	}
	if _, err := TakeLock(dir); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("a second one: %v", err)
	}
	l.Release()
	l, err = TakeLock(dir)
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
	l2, err := TakeLock(dir)
	if err != nil || l2 == nil || !l2.Valid() {
		t.Fatalf("a fresh lock after the file was taken away: %v", err)
	}
	l.Release()
	l2.Release()
	missing := filepath.Join(dir, "nothing-set-up")
	if l, err := TakeLock(missing); err != nil || l != nil {
		t.Fatalf("no folder yet: %v %v", err, l)
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
	if err := os.Chmod(parent, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	if _, err := TakeLock(filepath.Join(parent, "VaultSync")); err == nil || errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("an unreadable folder: %v", err)
	}
}

func TestIssue176_TrayTextHelpers(t *testing.T) {
	if got := lastLine("a\n\n  b  \n\n"); got != "b" {
		t.Fatalf("lastLine: %q", got)
	}
	if got := consentText("X already contains files. Pass --yes to agree."); !strings.HasPrefix(got, "X already contains files.\n\nIf Obsidian Sync") {
		t.Fatalf("consentText: %q", got)
	}
}
