package tray

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psimaker/vaultsync/desktop/internal/control"
	"github.com/psimaker/vaultsync/desktop/internal/layout"
)

// #248: with no agent answering, the menu offers to start the background
// service, as vaultsync start does. It decides nothing itself: what start
// says is what the menu shows.

func TestIssue248_TheStartItemIsThereOnlyWhenNoAgentAnswers(t *testing.T) {
	v := Render(Status{}, ErrNoAgent, Options{CanStart: true})
	if v.Start != (Action{"Start VaultSync", true}) || v.Headline != "VaultSync is not running" || v.Detail != "" {
		t.Fatalf("no agent, a vaultsync to run: %+v", v)
	}
	v = Render(Status{}, ErrNoAgent, Options{CanStart: true, Starting: true})
	if v.Start != (Action{"Starting VaultSync…", false}) {
		t.Fatalf("while it starts: %+v", v.Start)
	}
	// Nothing to run: the terminal, as before.
	v = Render(Status{}, ErrNoAgent, Options{})
	if v.Start != (Action{}) || v.Detail != "Run vaultsync start in a terminal" {
		t.Fatalf("no vaultsync to run: %+v", v)
	}
	// Not set up: never a start, which would point to setup; an agent that
	// may be running, or one that answers: nothing to start.
	for _, err := range []error{ErrNotSetUp, ErrNoSocket, ErrOldAgent, errors.New("boom"), nil} {
		st := Status{Engine: "running"}
		if v := Render(st, err, Options{CanStart: true}); v.Start != (Action{}) {
			t.Fatalf("%v: %+v", err, v.Start)
		}
	}
}

// fakeStart scripts what vaultsync start says, and counts its runs.
type fakeStart struct {
	mu       sync.Mutex
	programs []string
	hold     chan struct{}
	err      error
	said     string
	onRun    func()
}

func (f *fakeStart) start(_ context.Context, program string) (string, error) {
	f.mu.Lock()
	f.programs = append(f.programs, program)
	hold, err, said, onRun := f.hold, f.err, f.said, f.onRun
	f.mu.Unlock()
	if hold != nil {
		<-hold
	}
	if onRun != nil {
		onRun()
	}
	return said, err
}

func (f *fakeStart) runs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.programs...)
}

// settledRuns is runs after a wrongly started goroutine has had its time.
func (f *fakeStart) settledRuns() []string {
	time.Sleep(100 * time.Millisecond)
	return f.runs()
}

func notRunning(v View) bool { return v.Headline == "VaultSync is not running" }

func TestIssue248_StartRunsVaultsyncStartAndShowsWhatItSaid(t *testing.T) {
	agent := newFakeAgent(t)
	agent.stop()
	ui := newFakeUI(t)
	started := &fakeStart{hold: make(chan struct{}), said: "✓ VaultSync is running again."}
	notes := make(chan string, 4)
	controllerFor(t, agent, ui, &Controller{
		StartProgram: func() string { return "/Applications/VaultSync for Mac.app/Contents/MacOS/vaultsync" },
		Start:        started.start,
		Notify:       func(_ context.Context, s string) { notes <- s },
	})
	v := ui.view(t, "not running", notRunning)
	if !v.Start.Enabled {
		t.Fatalf("no Start to click: %+v", v)
	}
	ui.send(Event{Kind: EventStart})
	ui.view(t, "starting", func(v View) bool { return v.Start == Action{"Starting VaultSync…", false} })
	// A second click while it runs starts nothing more, and the menu goes
	// on answering.
	ui.send(Event{Kind: EventStart})
	ui.click(EventOpened, 0)
	ui.view(t, "still starting", func(v View) bool { return v.Start == Action{"Starting VaultSync…", false} })
	if got := started.settledRuns(); len(got) != 1 || got[0] != "/Applications/VaultSync for Mac.app/Contents/MacOS/vaultsync" {
		t.Fatalf("ran %v", got)
	}
	close(started.hold)
	// The service runs, its socket not yet: what start said stands, and
	// a click now would restart a service on its way up.
	v = ui.view(t, "what start said", func(v View) bool { return v.Notice != "" })
	if v.Notice != "✓ VaultSync is running again." || v.Start != (Action{"Starting VaultSync…", false}) {
		t.Fatalf("after the start: %+v", v)
	}
	ui.send(Event{Kind: EventStart})
	ui.click(EventOpened, 0)
	if got := started.settledRuns(); len(got) != 1 {
		t.Fatalf("a click after the start ran again: %v", got)
	}
	select {
	case note := <-notes:
		t.Fatalf("a start that worked was notified: %q", note)
	default:
	}
	// The agent answers, and speaks for itself — without a click: the menu
	// asks again in the seconds after a start.
	agent = restartAgent(t, agent)
	v = ui.view(t, "the agent answers", func(v View) bool { return v.Headline == "Up to date" })
	if v.Notice != "" || v.Start != (Action{}) {
		t.Fatalf("with the agent back: %+v", v)
	}
}

// restartAgent brings the fake agent back on its socket, as a service that
// has started does.
func restartAgent(t *testing.T, was *fakeAgent) *fakeAgent {
	t.Helper()
	was.set(func(f *fakeAgent) {
		f.hubs = 1
		f.status.Hubs = []Hub{{Name: "Office Hub", State: "connected", Kind: control.KindOK}}
		f.status.Vaults = []Vault{{Label: "Notes", Path: "/home/me/Notes", State: "up to date", Kind: control.KindOK}}
	})
	was.serveAgain()
	return was
}

// What start refuses with is the menu's notice, in start's words — and it
// is notified. The Start item stays for another try.
func TestIssue248_ARefusedStartIsSaidInItsWords(t *testing.T) {
	agent := newFakeAgent(t)
	agent.stop()
	ui := newFakeUI(t)
	refusal := "The background service runs, but it does not sync: this VaultSync is older than the one set up on this computer."
	started := &fakeStart{err: errors.New(refusal)}
	notes := make(chan string, 4)
	controllerFor(t, agent, ui, &Controller{
		StartProgram: func() string { return "/x/vaultsync" },
		Start:        started.start,
		Notify:       func(_ context.Context, s string) { notes <- s },
	})
	ui.view(t, "not running", notRunning)
	ui.send(Event{Kind: EventStart})
	v := ui.view(t, "the refusal", func(v View) bool { return v.Notice != "" })
	if v.Notice != "Could not start VaultSync: "+refusal || v.Start != (Action{"Start VaultSync", true}) {
		t.Fatalf("after the refusal: %+v", v)
	}
	select {
	case note := <-notes:
		if note != v.Notice {
			t.Fatalf("notified %q", note)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the refusal was not notified")
	}
}

// With no vaultsync to run there is no Start item; one that goes before the
// click runs nothing.
func TestIssue248_NothingToRunNoStart(t *testing.T) {
	agent := newFakeAgent(t)
	agent.stop()
	ui := newFakeUI(t)
	started := &fakeStart{}
	var mu sync.Mutex
	program := "/x/vaultsync"
	controllerFor(t, agent, ui, &Controller{
		StartProgram: func() string { mu.Lock(); defer mu.Unlock(); return program },
		Start:        started.start,
	})
	ui.view(t, "with a program", func(v View) bool { return v.Start.Enabled })
	mu.Lock()
	program = ""
	mu.Unlock()
	ui.send(Event{Kind: EventStart})
	ui.click(EventOpened, 0)
	v := ui.view(t, "without one", func(v View) bool { return v.Start == Action{} })
	if v.Detail != "Run vaultsync start in a terminal" {
		t.Fatalf("detail: %q", v.Detail)
	}
	if got := started.runs(); len(got) != 0 {
		t.Fatalf("ran %v", got)
	}
}

// A service that started and never answers (a run that keeps ending before
// its socket counts as running for a moment each time) does not leave "running
// again" standing: the menu says it does not answer, notifies, and Start is
// there again. An agent that answers later still speaks for itself.
func TestIssue248_ARunningAgainThatNeverAnswersDoesNotStand(t *testing.T) {
	agent := newFakeAgent(t)
	agent.stop()
	ui := newFakeUI(t)
	started := &fakeStart{said: "✓ VaultSync is running again."}
	notes := make(chan string, 4)
	controllerFor(t, agent, ui, &Controller{
		StartProgram: func() string { return "/x/vaultsync" },
		Start:        started.start,
		StartWaits:   []time.Duration{10 * time.Millisecond, 10 * time.Millisecond},
		Notify:       func(_ context.Context, s string) { notes <- s },
	})
	ui.view(t, "not running", notRunning)
	ui.send(Event{Kind: EventStart})
	said := noStartAnswer(agent.layout, "/home/me")
	if !strings.Contains(said, "last-error.txt") {
		t.Fatalf("no word of where the reason is: %q", said)
	}
	v := ui.view(t, "no answer", func(v View) bool { return v.Notice == said })
	if v.Start != (Action{"Start VaultSync", true}) || !notRunning(v) {
		t.Fatalf("after the wait: %+v", v)
	}
	select {
	case note := <-notes:
		if note != said {
			t.Fatalf("notified %q", note)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("it was not notified")
	}
	restartAgent(t, agent)
	ui.click(EventOpened, 0)
	v = ui.view(t, "the agent answers", func(v View) bool { return v.Headline == "Up to date" })
	if v.Notice != "" {
		t.Fatalf("with the agent back: %+v", v)
	}
}

// An answer on the last ask is in time: nothing about "does not answer".
func TestIssue248_AnAnswerOnTheLastAskIsInTime(t *testing.T) {
	agent := newFakeAgent(t)
	agent.stop()
	ui := newFakeUI(t)
	started := &fakeStart{said: "✓ VaultSync is running again."}
	notes := make(chan string, 4)
	controllerFor(t, agent, ui, &Controller{
		StartProgram: func() string { return "/x/vaultsync" },
		Start:        started.start,
		StartWaits:   []time.Duration{10 * time.Millisecond, 300 * time.Millisecond},
		Notify:       func(_ context.Context, s string) { notes <- s },
	})
	ui.view(t, "not running", notRunning)
	ui.send(Event{Kind: EventStart})
	ui.view(t, "what start said", func(v View) bool { return v.Notice == "✓ VaultSync is running again." })
	restartAgent(t, agent)
	v := ui.view(t, "the agent answers", func(v View) bool { return v.Headline == "Up to date" })
	if v.Notice != "" {
		t.Fatalf("with the agent back: %+v", v)
	}
	select {
	case note := <-notes:
		t.Fatalf("notified %q", note)
	case <-time.After(100 * time.Millisecond):
	}
}

// Whatever answers speaks for itself, however badly: the wait ends with nothing
// about "does not answer" under it.
func TestIssue248_ABadAnswerEndsTheWaitToo(t *testing.T) {
	for name, set := range map[string]func(*fakeAgent){
		"an error":       func(f *fakeAgent) { f.statusErr = "the sync engine does not answer" },
		"an older agent": func(f *fakeAgent) { f.old = true },
	} {
		t.Run(name, func(t *testing.T) {
			agent := newFakeAgent(t)
			agent.stop()
			ui := newFakeUI(t)
			notes := make(chan string, 4)
			started := &fakeStart{hold: make(chan struct{}), said: "✓ VaultSync is running again."}
			controllerFor(t, agent, ui, &Controller{
				StartProgram: func() string { return "/x/vaultsync" },
				Start:        started.start,
				StartWaits:   []time.Duration{20 * time.Millisecond, 20 * time.Millisecond, 20 * time.Millisecond},
				Notify:       func(_ context.Context, s string) { notes <- s },
			})
			ui.view(t, "not running", notRunning)
			ui.send(Event{Kind: EventStart})
			ui.view(t, "starting", func(v View) bool { return v.Start == Action{"Starting VaultSync…", false} })
			agent.set(set)
			agent.serveAgain()
			close(started.hold)
			v := ui.view(t, "the bad answer", func(v View) bool { return !notRunning(v) && v.Notice == "" })
			if v.Start != (Action{}) {
				t.Fatalf("the view: %+v", v)
			}
			if got := started.runs(); len(got) != 1 {
				t.Fatalf("ran %v", got)
			}
			select {
			case note := <-notes:
				t.Fatalf("notified %q", note)
			case <-time.After(150 * time.Millisecond):
			}
			// One that answers badly gets the poll: a stalled socket would
			// hold the loop through every ask.
			agent.set(func(f *fakeAgent) {
				if f.statusAsks > 2 {
					t.Errorf("asked %d times after a bad answer", f.statusAsks)
				}
			})
		})
	}
}

// A socket error on record is no answer: the run that recorded it may be gone,
// and the wait goes on to its end.
func TestIssue248_ASocketErrorOnRecordIsNoAnswer(t *testing.T) {
	agent := newFakeAgent(t)
	agent.stop()
	ui := newFakeUI(t)
	started := &fakeStart{hold: make(chan struct{}), said: "✓ VaultSync is running again."}
	notes := make(chan string, 4)
	controllerFor(t, agent, ui, &Controller{
		StartProgram: func() string { return "/x/vaultsync" },
		Start:        started.start,
		StartWaits:   []time.Duration{10 * time.Millisecond, 10 * time.Millisecond},
		Notify:       func(_ context.Context, s string) { notes <- s },
	})
	ui.view(t, "not running", notRunning)
	ui.send(Event{Kind: EventStart})
	ui.view(t, "starting", func(v View) bool { return v.Start == Action{"Starting VaultSync…", false} })
	if err := os.WriteFile(agent.layout.SocketErrorFile(), []byte("no socket\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	close(started.hold)
	select {
	case note := <-notes:
		if note != noStartAnswer(agent.layout, "/home/me") {
			t.Fatalf("notified %q", note)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wait ended without a word")
	}
}

// The socket comes before the engine: the asks go on past an agent whose engine
// is still starting, so the menu moves on without waiting for a poll.
func TestIssue248_TheAsksGoOnPastAnEngineStillStarting(t *testing.T) {
	agent := newFakeAgent(t)
	agent.stop()
	ui := newFakeUI(t)
	started := &fakeStart{hold: make(chan struct{}), said: "✓ VaultSync is running again."}
	controllerFor(t, agent, ui, &Controller{
		StartProgram: func() string { return "/x/vaultsync" },
		Start:        started.start,
		StartWaits:   []time.Duration{10 * time.Millisecond, 500 * time.Millisecond},
	})
	ui.view(t, "not running", notRunning)
	ui.send(Event{Kind: EventStart})
	ui.view(t, "starting", func(v View) bool { return v.Start == Action{"Starting VaultSync…", false} })
	agent.set(func(f *fakeAgent) { f.status.Engine = "starting" })
	agent.serveAgain()
	close(started.hold)
	ui.view(t, "the engine is starting", func(v View) bool { return strings.Contains(v.Headline, "Starting") })
	// Only after the ask that heard "starting" has been answered does the
	// engine run: the next ask is the one that has to come.
	deadline := time.Now().Add(5 * time.Second)
	for asks := 0; asks < 2; {
		agent.set(func(f *fakeAgent) { asks = f.statusAsks })
		if time.Now().After(deadline) {
			t.Fatalf("asked %d times", asks)
		}
		time.Sleep(5 * time.Millisecond)
	}
	agent.set(func(f *fakeAgent) { f.status.Engine = "running" })
	ui.view(t, "the engine runs", func(v View) bool { return !strings.Contains(v.Headline, "Starting") && !notRunning(v) })
}

// What an open said during the wait stays: only start's own words are replaced.
func TestIssue248_AnothersNoticeStaysThroughTheWait(t *testing.T) {
	agent := newFakeAgent(t)
	agent.stop()
	ui := newFakeUI(t)
	started := &fakeStart{said: "✓ VaultSync is running again."}
	notes := make(chan string, 4)
	controllerFor(t, agent, ui, &Controller{
		StartProgram: func() string { return "/x/vaultsync" },
		Start:        started.start,
		StartWaits:   []time.Duration{300 * time.Millisecond},
		Open:         func(context.Context, string) error { return errors.New("no way to open it") },
		Notify:       func(_ context.Context, s string) { notes <- s },
	})
	ui.view(t, "not running", notRunning)
	ui.send(Event{Kind: EventStart})
	ui.view(t, "what start said", func(v View) bool { return v.Notice == "✓ VaultSync is running again." })
	ui.send(Event{Kind: EventOpenLog})
	opened := ui.view(t, "the open's notice", func(v View) bool { return strings.Contains(v.Notice, "no way to open it") })
	v := ui.view(t, "the wait is over", func(v View) bool { return v.Start.Enabled })
	if v.Notice != opened.Notice {
		t.Fatalf("the open's notice went: %q", v.Notice)
	}
	said := noStartAnswer(agent.layout, "/home/me")
	for want := 2; want > 0; want-- {
		select {
		case note := <-notes:
			if note == said {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no notification that it does not answer")
		}
	}
	t.Fatal("no notification that it does not answer")
}

// A second start after the notice is followed like the first.
func TestIssue248_ASecondStartIsFollowedToo(t *testing.T) {
	agent := newFakeAgent(t)
	agent.stop()
	ui := newFakeUI(t)
	started := &fakeStart{said: "✓ VaultSync is running again."}
	notes := make(chan string, 4)
	controllerFor(t, agent, ui, &Controller{
		StartProgram: func() string { return "/x/vaultsync" },
		Start:        started.start,
		StartWaits:   []time.Duration{10 * time.Millisecond},
		Notify:       func(_ context.Context, s string) { notes <- s },
	})
	said := noStartAnswer(agent.layout, "/home/me")
	ui.view(t, "not running", notRunning)
	for round := 1; round <= 2; round++ {
		ui.send(Event{Kind: EventStart})
		ui.view(t, "no answer", func(v View) bool { return v.Notice == said && v.Start.Enabled })
		select {
		case <-notes:
		case <-time.After(5 * time.Second):
			t.Fatalf("round %d was not notified", round)
		}
		if got := started.runs(); len(got) != round {
			t.Fatalf("round %d ran %v", round, got)
		}
	}
}

func TestIssue248_TheAsksTakeAboutFifteenSeconds(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	if got := (&Controller{}).startWaits(); !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
}

// A second instance found by the refresh a Start click makes ends the app as a
// poll's refresh would, and starts nothing: a start would outlive this app.
func TestIssue248_AStartClickFindsASecondInstance(t *testing.T) {
	agent := newFakeAgent(t)
	agent.stop()
	ui := newFakeUI(t)
	started := &fakeStart{}
	var mu sync.Mutex
	held := &fakeLock{valid: true}
	other := false
	c := &Controller{Home: "/home/me", Layout: agent.layout, UI: ui, Poll: time.Hour, StatusTimeout: 5 * time.Second,
		StartProgram: func() string { return "/x/vaultsync" }, Start: started.start,
		Lock: func(layout.Layout) (LockHandle, error) {
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
	ui.view(t, "not running", notRunning)
	mu.Lock()
	held.mu.Lock()
	held.valid = false
	held.mu.Unlock()
	other = true
	mu.Unlock()
	ui.send(Event{Kind: EventStart})
	select {
	case err := <-done:
		if !errors.Is(err, ErrAlreadyRunning) {
			t.Fatalf("ended with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the app did not end")
	}
	if got := started.settledRuns(); len(got) != 0 {
		t.Fatalf("started beside another instance: %v", got)
	}
}

// A click on a menu older than the agent asks first: a start would restart
// an agent that has come up since the last poll.
func TestIssue248_AClickOnAStaleMenuAsksFirst(t *testing.T) {
	agent := newFakeAgent(t)
	agent.stop()
	ui := newFakeUI(t)
	started := &fakeStart{}
	controllerFor(t, agent, ui, &Controller{
		StartProgram: func() string { return "/x/vaultsync" },
		Start:        started.start,
	})
	ui.view(t, "not running", notRunning)
	restartAgent(t, agent)
	ui.send(Event{Kind: EventStart})
	ui.view(t, "the agent answers", func(v View) bool { return v.Headline == "Up to date" })
	if got := started.settledRuns(); len(got) != 0 {
		t.Fatalf("started beside a running agent: %v", got)
	}
}

// The program the service runs: the one its LaunchAgent names on a Mac, the copy
// on Linux. Without one named or there, the copy; never one found on PATH, and
// never one beside this app, wherever this app is.
func TestIssue248_WhichVaultsyncStartRuns(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	exec := func(p string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bundle := filepath.Join(home, "Applications", "VaultSync for Mac.app", "Contents", "MacOS", "vaultsync")
	copyOf := filepath.Join(home, "VaultSync", "bin", "vaultsync")
	lay := layout.Layout{Agent: copyOf}
	unit := layout.LaunchAgentPlist(home)
	names := func(program string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
			t.Fatal(err)
		}
		plist := `<?xml version="1.0"?><plist version="1.0"><dict><key>Label</key><string>` + layout.ServiceLabel + `</string><key>ProgramArguments</key><array><string>` + program + `</string><string>run</string></array></dict></plist>`
		if err := os.WriteFile(unit, []byte(plist), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mac := AgentProgram("darwin", home, lay)
	onPath := filepath.Join(home, "path", "vaultsync")
	exec(onPath)
	t.Setenv("PATH", filepath.Dir(onPath))

	if got := mac(); got != "" {
		t.Fatalf("nothing there, one on PATH: %q", got)
	}
	exec(copyOf)
	if got := mac(); got != copyOf {
		t.Fatalf("no LaunchAgent, a copy: %q", got)
	}
	// The app's agent names itself ahead of the copy: it is what runs.
	exec(bundle)
	names(bundle)
	if got := mac(); got != bundle {
		t.Fatalf("the program it names: %q", got)
	}
	// And the copy when that is what it names, with an app beside it.
	names(copyOf)
	if got := mac(); got != copyOf {
		t.Fatalf("it names the copy: %q", got)
	}
	// What it names is gone, or cannot be run, or says nothing certain: the
	// copy, whose start says what is wrong.
	gone := filepath.Join(home, "Trash", "vaultsync")
	names(gone)
	if got := mac(); got != copyOf {
		t.Fatalf("it names one that is gone: %q", got)
	}
	names(bundle)
	if err := os.Chmod(bundle, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := mac(); got != copyOf {
		t.Fatalf("it names one that cannot run: %q", got)
	}
	if err := os.Chmod(bundle, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Dir(copyOf))
	names("vaultsync")
	if got := mac(); got != copyOf {
		t.Fatalf("it names no path: %q", got)
	}
	if err := os.WriteFile(unit, []byte("not a plist"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := mac(); got != copyOf {
		t.Fatalf("a LaunchAgent that cannot be read: %q", got)
	}
	// Linux has no LaunchAgent to read, whatever lies there.
	names(bundle)
	if got := AgentProgram("linux", home, lay)(); got != copyOf {
		t.Fatalf("Linux: %q", got)
	}
	if err := os.Chmod(copyOf, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := AgentProgram("linux", home, lay)(); got != "" {
		t.Fatalf("Linux, a copy that cannot run: %q", got)
	}
}

func TestIssue248_WhatStartSaysComesFromItsOutput(t *testing.T) {
	type outcome struct {
		stdout, stderr string
		status         int
		err            error
	}
	for name, c := range map[string]struct {
		run  outcome
		said string
		why  string
	}{
		"it worked":   {run: outcome{stdout: "✓ VaultSync is running again.\n"}, said: "✓ VaultSync is running again."},
		"a refusal":   {run: outcome{stderr: "\nThe background service cannot start: it runs …\n", status: 1}, why: "The background service cannot start: it runs …"},
		"an error":    {run: outcome{stderr: "error: launchctl enable: boom\n", status: 1}, why: "launchctl enable: boom"},
		"no words":    {run: outcome{status: 3}, why: "vaultsync start exited with status 3"},
		"it can't":    {run: outcome{err: errors.New("no such file")}, why: "no such file"},
		"out of time": {run: outcome{err: context.DeadlineExceeded}, why: errStartWaited.Error()},
	} {
		var args []string
		var ctxErr error
		run := func(ctx context.Context, name string, a []string, _ string) (string, string, int, error) {
			args, ctxErr = a, ctx.Err()
			return c.run.stdout, c.run.stderr, c.run.status, c.run.err
		}
		// Whatever the caller's context: a quit must not end a start.
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		said, err := startWith(run)(cancelled, "/x/vaultsync")
		if strings.Join(args, " ") != "start" || ctxErr != nil {
			t.Errorf("%s: ran %v, its context %v", name, args, ctxErr)
		}
		if said != c.said || (err == nil) != (c.why == "") || (err != nil && err.Error() != c.why) {
			t.Errorf("%s: said %q, %v", name, said, err)
		}
	}
}
