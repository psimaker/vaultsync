package tray

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	if got := started.runs(); len(got) != 1 || got[0] != "/Applications/VaultSync for Mac.app/Contents/MacOS/vaultsync" {
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
	if got := started.runs(); len(got) != 1 {
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
	v := ui.view(t, "no answer", func(v View) bool { return v.Notice == noStartAnswer })
	if v.Start != (Action{"Start VaultSync", true}) || !notRunning(v) {
		t.Fatalf("after the wait: %+v", v)
	}
	select {
	case note := <-notes:
		if note != noStartAnswer {
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

// An answer in time ends the wait: nothing about "does not answer" follows
// when the waits run out.
func TestIssue248_AnAnswerInTimeEndsTheWait(t *testing.T) {
	agent := newFakeAgent(t)
	agent.stop()
	ui := newFakeUI(t)
	started := &fakeStart{said: "✓ VaultSync is running again."}
	notes := make(chan string, 4)
	controllerFor(t, agent, ui, &Controller{
		StartProgram: func() string { return "/x/vaultsync" },
		Start:        started.start,
		StartWaits:   []time.Duration{50 * time.Millisecond, 50 * time.Millisecond},
		Notify:       func(_ context.Context, s string) { notes <- s },
	})
	ui.view(t, "not running", notRunning)
	started.onRun = func() { restartAgent(t, agent) }
	ui.send(Event{Kind: EventStart})
	ui.view(t, "the agent answers", func(v View) bool { return v.Headline == "Up to date" })
	time.Sleep(150 * time.Millisecond)
	select {
	case note := <-notes:
		t.Fatalf("notified %q", note)
	default:
	}
	if v := ui.view(t, "still the agent", func(v View) bool { return v.Headline == "Up to date" }); v.Notice != "" {
		t.Fatalf("after the waits: %+v", v)
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
	if got := started.runs(); len(got) != 0 {
		t.Fatalf("started beside a running agent: %v", got)
	}
}

// VaultSync's own copy, else the agent beside this program in VaultSync for
// Mac; never one found on PATH. A bundle kept elsewhere (in Downloads, a
// "Keep Both" copy) does not come ahead of the copy.
func TestIssue248_WhichVaultsyncStartRuns(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
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
	macos := filepath.Join(tmp, "VaultSync for Mac.app", "Contents", "MacOS")
	tray, agent := filepath.Join(macos, "vaultsync-tray"), filepath.Join(macos, "vaultsync")
	copyOf := filepath.Join(tmp, "VaultSync", "bin", "vaultsync")
	lay := layout.Layout{Agent: copyOf}
	exec(tray)
	onPath := filepath.Join(tmp, "path", "vaultsync")
	exec(onPath)
	t.Setenv("PATH", filepath.Dir(onPath))

	if got := AgentProgram(tray, lay)(); got != "" {
		t.Fatalf("neither there, one on PATH: %q", got)
	}
	exec(copyOf)
	if got := AgentProgram(tray, lay)(); got != copyOf {
		t.Fatalf("only the copy: %q", got)
	}
	exec(agent)
	if got := AgentProgram(tray, lay)(); got != copyOf {
		t.Fatalf("a bundle beside the copy: %q", got)
	}
	if err := os.Remove(copyOf); err != nil {
		t.Fatal(err)
	}
	if got := AgentProgram(tray, lay)(); got != agent {
		t.Fatalf("beside it in the app, no copy: %q", got)
	}
	// Reached through a link, it still looks beside the real program.
	link := filepath.Join(tmp, "vaultsync-tray-link")
	if err := os.Symlink(tray, link); err != nil {
		t.Fatal(err)
	}
	if got := AgentProgram(link, lay)(); got != agent {
		t.Fatalf("through a link: %q", got)
	}
	exec(copyOf)
	// A tray anywhere else (a Linux download) takes the copy, even beside
	// a file of that name.
	elsewhere := filepath.Join(tmp, "Downloads", "vaultsync-tray")
	exec(elsewhere)
	exec(filepath.Join(tmp, "Downloads", "vaultsync"))
	if got := AgentProgram(elsewhere, lay)(); got != copyOf {
		t.Fatalf("a download beside a file of that name: %q", got)
	}
	// What cannot be run does not count.
	if err := os.Chmod(copyOf, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := AgentProgram(tray, lay)(); got != agent {
		t.Fatalf("not executable: %q", got)
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
