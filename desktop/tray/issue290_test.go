package tray

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/psimaker/vaultsync/desktop/internal/control"
)

// #290, points 6 and 7: what the menu does while the agent is slow to
// answer its status, and what it says when there is nothing to start.

// slowStatus is how long the agent takes to answer each status in these
// tests: inside the menu's limit (StatusTimeout below), and long enough that
// a click held behind it is told from one that is not.
const slowStatus = 2 * time.Second

// heldNoLonger is how soon a click that waits for no status ask takes effect.
const heldNoLonger = 700 * time.Millisecond

// syncingAgent is an agent with a Hub and a vault that answers each status
// at once, until slow is called.
func syncingAgent(t *testing.T) *fakeAgent {
	t.Helper()
	agent := newFakeAgent(t)
	agent.set(func(f *fakeAgent) {
		f.hubs = 1
		f.status.Hubs = []Hub{{Name: "Office Hub", State: "connected", Kind: control.KindOK}}
		f.status.Vaults = []Vault{{Label: "Notes", Path: "/home/me/Notes", State: "up to date", Kind: control.KindOK}}
	})
	return agent
}

func upToDate(v View) bool { return v.Headline == "Up to date" }

// askInFlight makes every later status take slowStatus, opens the menu (a
// refresh) and returns once that ask has reached the agent.
func askInFlight(t *testing.T, agent *fakeAgent, ui *fakeUI) {
	t.Helper()
	var asks int
	agent.set(func(f *fakeAgent) { f.statusDelay, asks = slowStatus, f.statusAsks })
	ui.click(EventOpened, 0)
	agent.asked(asks + 1)
}

// An agent that answers each status slowly, inside the limit, held the menu's
// loop for every ask: a click on a vault, Open Log or Pause waited for the
// ask before it, and the five asks after a start held it for their sum (#290,
// point 6). A click that needs no fresh look takes effect at once.
func TestIssue290_ASlowStatusHoldsNoClick(t *testing.T) {
	for name, c := range map[string]struct {
		click  Event
		effect func(t *testing.T, agent *fakeAgent, opened chan string)
	}{
		"Open Log": {Event{Kind: EventOpenLog}, func(t *testing.T, _ *fakeAgent, opened chan string) {
			waitFor(t, opened, "the log was not opened")
		}},
		"a vault": {Event{Kind: EventOpenVault, Path: "/home/me/Notes"}, func(t *testing.T, _ *fakeAgent, opened chan string) {
			waitFor(t, opened, "the vault was not opened")
		}},
		"Pause": {Event{Kind: EventPause, Pause: true}, func(t *testing.T, agent *fakeAgent, _ chan string) {
			deadline := time.Now().Add(2 * slowStatus)
			for {
				var calls int
				agent.set(func(f *fakeAgent) { calls = len(f.calls) })
				if calls == 1 {
					return
				}
				if time.Now().After(deadline) {
					t.Fatal("the pause did not reach the agent")
				}
				time.Sleep(5 * time.Millisecond)
			}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			agent := syncingAgent(t)
			ui := newFakeUI(t)
			opened := make(chan string, 4)
			controllerFor(t, agent, ui, &Controller{
				Open: func(_ context.Context, p string) error { opened <- p; return nil },
			})
			ui.view(t, "up to date", upToDate)
			askInFlight(t, agent, ui)
			began := time.Now()
			ui.send(c.click)
			c.effect(t, agent, opened)
			if took := time.Since(began); took > heldNoLonger {
				t.Fatalf("the click took effect after %v: it waited for a status ask begun before it", took.Round(10*time.Millisecond))
			}
		})
	}
}

// The asks after a start are the loop's too: with an agent slow to answer
// each, a click during the wait took effect only once the ask in flight was
// answered (#290, point 6).
func TestIssue290_TheAsksAfterAStartHoldNoClick(t *testing.T) {
	agent := newFakeAgent(t)
	agent.stop()
	ui := newFakeUI(t)
	started := &fakeStart{hold: make(chan struct{}), said: "✓ VaultSync is running again."}
	opened := make(chan string, 4)
	controllerFor(t, agent, ui, &Controller{
		StartProgram: func() string { return "/x/vaultsync" },
		Start:        started.start,
		StartWaits:   []time.Duration{10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond, 10 * time.Millisecond},
		Open:         func(_ context.Context, p string) error { opened <- p; return nil },
	})
	ui.view(t, "not running", notRunning)
	ui.send(Event{Kind: EventStart})
	ui.view(t, "starting", func(v View) bool { return v.Start == Action{"Starting VaultSync…", false} })
	// The service runs and its agent answers, slowly: its engine has just
	// started (up to 8 s on a macOS VM with two cores, #290).
	agent.set(func(f *fakeAgent) { f.statusDelay = slowStatus })
	agent.serveAgain()
	close(started.hold)
	agent.asked(1)
	began := time.Now()
	ui.send(Event{Kind: EventOpenLog})
	waitFor(t, opened, "the log was not opened")
	if took := time.Since(began); took > heldNoLonger {
		t.Fatalf("Open Log during the asks after a start took %v", took.Round(10*time.Millisecond))
	}
}

// A click on Pair asks the agent first (one gone since the last poll is said
// before the person types a code for nobody), and that look was the loop's: a
// click behind it waited for it (#290, point 6). Pair's own look stays.
func TestIssue290_PairsLookHoldsNoOtherClick(t *testing.T) {
	agent := syncingAgent(t)
	ui := newFakeUI(t)
	opened := make(chan string, 4)
	asker := &fakeAsker{cancel: "code"}
	controllerFor(t, agent, ui, &Controller{
		Asker: asker,
		Open:  func(_ context.Context, p string) error { opened <- p; return nil },
	})
	ui.view(t, "up to date", upToDate)
	var asks int
	agent.set(func(f *fakeAgent) { f.statusDelay, asks = slowStatus, f.statusAsks })
	ui.send(Event{Kind: EventPair})
	agent.asked(asks + 1)
	began := time.Now()
	ui.send(Event{Kind: EventOpenLog})
	waitFor(t, opened, "the log was not opened")
	if took := time.Since(began); took > heldNoLonger {
		t.Fatalf("Open Log behind Pair's look took %v", took.Round(10*time.Millisecond))
	}
}

func waitFor(t *testing.T, ch chan string, why string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * slowStatus):
		t.Fatal(why)
	}
}

// With nothing VaultSync could start — the program its LaunchAgent names gone,
// and its own copy too — the menu said "Run vaultsync start in a terminal". A
// removal of VaultSync's files that stopped after the copy (#276) leaves that:
// its record, no copy, no service, and the vaultsync command gone with the
// copy. start, where a vaultsync is still there to run it, says that the
// service is not installed (#290, point 7).
func TestIssue290_NothingToStartNamesNoCommandThatIsGone(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			agent := newFakeAgent(t) // agent.json with its port: set up
			agent.stop()
			home := t.TempDir()
			ui := newFakeUI(t)
			controllerFor(t, agent, ui, &Controller{
				StartProgram: AgentProgram(goos, home, agent.layout),
				Start:        (&fakeStart{}).start,
			})
			v := ui.view(t, "not running", notRunning)
			if v.Start.Enabled {
				t.Fatalf("a Start with nothing to run: %+v", v)
			}
			if strings.Contains(v.Detail, "vaultsync start") {
				t.Fatalf("no program of VaultSync's is there to start it (%s), and the menu names vaultsync start: %q", filepath.Base(agent.layout.Agent), v.Detail)
			}
		})
	}
}

// drain takes every view rendered so far.
func drain(ui *fakeUI) {
	for {
		select {
		case <-ui.views:
		default:
			return
		}
	}
}

// One ask is out at a time: whatever asks for a look while one is out — the
// menu opened again and again, a poll — is one more ask once it is answered,
// not one each (#290).
func TestIssue290_OneAskAtATime(t *testing.T) {
	agent := syncingAgent(t)
	ui := newFakeUI(t)
	controllerFor(t, agent, ui, &Controller{})
	ui.view(t, "up to date", upToDate)
	askInFlight(t, agent, ui)
	for i := 0; i < 5; i++ {
		ui.click(EventOpened, 0)
	}
	// The one out, then the one the five were folded into.
	agent.asked(3)
	time.Sleep(slowStatus + slowStatus/2)
	agent.set(func(f *fakeAgent) {
		if f.statusAsks != 3 {
			t.Fatalf("asked %d times: the menu opened five times while an ask was out", f.statusAsks)
		}
	})
}

// An answer to an ask begun before a pause's request returned may tell the
// state from before it: it is not shown, or the menu would offer to pause
// again for as long as the next ask takes. The next answer is (#290).
func TestIssue290_APauseShowsNoAnswerFromBeforeIt(t *testing.T) {
	agent := syncingAgent(t)
	agent.set(func(f *fakeAgent) { f.early = true })
	ui := newFakeUI(t)
	controllerFor(t, agent, ui, &Controller{})
	ui.view(t, "up to date", upToDate)
	askInFlight(t, agent, ui)
	drain(ui)
	ui.send(Event{Kind: EventPause, Pause: true})
	select {
	case v := <-ui.views:
		if !v.Paused || v.Pause.Title != "Resume Syncing" {
			t.Fatalf("the first view after the pause is from before it: %+v", v)
		}
	case <-time.After(4 * slowStatus):
		t.Fatal("no view after the pause")
	}
}

// A click on Pair acts on the answer to an ask begun after it, not on one
// already out: that one may tell the state before something changed — here a
// pause from a terminal, under which pairing is refused — and the menu would
// ask the person for a code for a pairing the agent then refuses (#290).
func TestIssue290_PairActsOnALookBegunAfterTheClick(t *testing.T) {
	agent := syncingAgent(t)
	agent.set(func(f *fakeAgent) { f.early = true })
	ui := newFakeUI(t)
	asker := &fakeAsker{cancel: "code"}
	controllerFor(t, agent, ui, &Controller{Asker: asker})
	if v := ui.view(t, "up to date", upToDate); !v.Pair.Enabled {
		t.Fatalf("no Pair to click: %+v", v)
	}
	askInFlight(t, agent, ui) // answers "not paused"
	agent.set(func(f *fakeAgent) { f.status.Paused = true })
	ui.send(Event{Kind: EventPair})
	ui.view(t, "paused", func(v View) bool { return v.Paused })
	time.Sleep(100 * time.Millisecond)
	if asked := asker.sequence(); len(asked) != 0 {
		t.Fatalf("a pairing began on an answer from before the click: %v", asked)
	}
}

// The menu's Start runs in the menu's own VaultSync folder: the one its
// --state-dir names, which the program it runs would otherwise take from the
// environment (#290, point 5).
func TestIssue290_TheMenusStartNamesItsFolder(t *testing.T) {
	if got := StartArgs("/home/me/state/vaultsync"); strings.Join(got, " ") != "start --state-dir /home/me/state/vaultsync" {
		t.Fatalf("the command line: %q", got)
	}
	if got := StartArgs(""); strings.Join(got, " ") != "start" {
		t.Fatalf("without a folder: %q", got)
	}
	agent := newFakeAgent(t)
	agent.stop()
	ui := newFakeUI(t)
	started := &fakeStart{said: "✓ VaultSync is running again."}
	controllerFor(t, agent, ui, &Controller{
		StartProgram: func() string { return "/x/vaultsync" },
		Start:        started.start,
		StartWaits:   []time.Duration{time.Millisecond},
	})
	ui.view(t, "not running", notRunning)
	ui.send(Event{Kind: EventStart})
	for deadline := time.Now().Add(10 * time.Second); len(started.runs()) == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("nothing was started")
		}
	}
	started.mu.Lock()
	defer started.mu.Unlock()
	if len(started.dirs) != 1 || started.dirs[0] != agent.layout.Base {
		t.Fatalf("started in %q, the menu's folder is %q", started.dirs, agent.layout.Base)
	}
}
