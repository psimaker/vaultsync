package tray

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/psimaker/vaultsync/desktop/internal/control"
)

// #291: a long notice was one menu item, which macOS cut in its middle — where
// the remedy often stands — and for which it widened the whole menu. It is
// shown on at most MenuLinesMost lines of MenuLineWidth characters now, its
// end kept where something has to go.

// The three longest texts the menu really shows, as start, the agent and a
// dialog program word them.
var (
	startNoAnswerYet    = "VaultSync's background service runs, but it does not answer yet — it may still be installing its sync engine. vaultsync status says what it does. If it stays so, run vaultsync stop, then vaultsync start; where that changes nothing, run the setup link, which brings the current VaultSync to the service (its log: ~/Library/Logs/VaultSync/vaultsync.log)."
	startOlderRefusal   = "The background service runs, but it does not answer: this VaultSync is older than the one set up on this computer: its sync engine is Syncthing 2.1.8, this vaultsync brings 2.1.7. A background service of this build does not sync — run the setup link to get the current VaultSync; vaultsync status says whether the service syncs."
	dialogFailureAtMost = "the code dialog could not be shown: kdialog exited with status -1: " + said("qt.qpa.plugin: Could not find the Qt platform plugin \"bogus\" in \"\"\nThis application failed to start because no Qt platform plugin could be initialized. Reinstalling the application may fix this problem.\n\nAvailable platform plugins are: eglfs, linuxfb, minimal, minimalegl, offscreen, vkkhrdisplay, vnc, wayland-brcm, wayland-egl, wayland, xcb.\n")
)

func texts(lines []Line) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Text
	}
	return out
}

// Where a text needs more lines than the menu gives it, the first lines and
// the longest end that fits are kept, and the middle goes: a text that says
// its cause and then its remedy keeps the remedy. Every line's tooltip holds
// the whole text.
func TestIssue291_ALongNoticeKeepsItsEnd(t *testing.T) {
	for name, c := range map[string]struct {
		text string
		want []string
	}{
		"start, no answer yet (352 characters)": {startNoAnswerYet, []string{
			"VaultSync's background service runs, but it does not answer…",
			"where that changes nothing, run the setup link, which brings",
			"the current VaultSync to the service (its log:",
			"~/Library/Logs/VaultSync/vaultsync.log).",
		}},
		"start's refusal for an older build (328)": {startOlderRefusal, []string{
			"The background service runs, but it does not answer: this…",
			"A background service of this build does not sync — run the",
			"setup link to get the current VaultSync; vaultsync status",
			"says whether the service syncs.",
		}},
		"a dialog that failed, at its bound (267)": {dialogFailureAtMost, []string{
			"the code dialog could not be shown: kdialog exited with",
			"status -1: qt.qpa.plugin: Could not find the Qt platform",
			"plugin \"bogus\" in \"\" This application failed to start…",
			"Reinstalling the application may fix this probl…",
		}},
		"a note after a start that worked": {"✓ VaultSync is running again.\nNote: this VaultSync is older than the one set up on this computer: its sync engine is Syncthing 2.1.8, this vaultsync brings 2.1.7. Run the setup link to get the current VaultSync.", []string{
			"✓ VaultSync is running again.",
			"Note: this VaultSync is older than the one set up on this…",
			"its sync engine is Syncthing 2.1.8, this vaultsync brings",
			"2.1.7. Run the setup link to get the current VaultSync.",
		}},
	} {
		got := MenuLines(c.text)
		if !reflect.DeepEqual(texts(got), c.want) {
			t.Errorf("%s:\n%q", name, texts(got))
		}
		for _, l := range got {
			if l.Tooltip != strings.Join(paragraphs(c.text), "\n") {
				t.Errorf("%s: tooltip %q", name, l.Tooltip)
			}
		}
	}
}

// A text that fits is shown whole, a paragraph on lines of its own, and needs
// no tooltip; no line is longer than MenuLineWidth.
func TestIssue291_ATextThatFitsIsShownWhole(t *testing.T) {
	for name, c := range map[string]struct {
		text string
		want []string
	}{
		"start, no answer": {noStartAnswer, []string{
			"VaultSync started but does not answer yet — vaultsync status",
			"in a terminal tells where it stands",
		}},
		"macOS refuses an open": {"Could not open it: " + errOpenRefused.Error(), []string{
			"Could not open it: macOS has not allowed “VaultSync for Mac”",
			"to open it — allow it in System Settings → Privacy &",
			"Security → Files & Folders (“VaultSync for Mac”), then click",
			"it again",
		}},
		"two paragraphs": {"✓ VaultSync is running again.\n\nNote: something.", []string{"✓ VaultSync is running again.", "Note: something."}},
		"short":          {"Nothing was changed.", []string{"Nothing was changed."}},
		"empty":          {" \n\t", nil},
	} {
		got := MenuLines(c.text)
		if !reflect.DeepEqual(texts(got), c.want) && !(len(got) == 0 && c.want == nil) {
			t.Errorf("%s: %q", name, texts(got))
		}
		for _, l := range got {
			if l.Tooltip != "" || utf8.RuneCountInString(l.Text) > MenuLineWidth {
				t.Errorf("%s: %+v", name, l)
			}
		}
	}
}

// A word longer than a line (a path, a device ID) is broken where it must,
// and never inside a character: a letter with its combining accent, an emoji
// with its skin tone, a flag's two regional indicators stay on one line.
func TestIssue291_ALineIsNeverBrokenInsideACharacter(t *testing.T) {
	for name, unit := range map[string]string{
		"a combining accent": "e\u0301",
		"a skin tone":        "\U0001F44D\U0001F3FD",
		"a flag":             "\U0001F1E9\U0001F1EA",
		"a plain letter":     "x",
	} {
		// One plain letter first: a break counted in runes then falls inside
		// a character of two runes.
		word := "y" + strings.Repeat(unit, 150)
		lines := MenuLines(word)
		if len(lines) == 0 || len(lines) > MenuLinesMost {
			t.Fatalf("%s: %d lines", name, len(lines))
		}
		for i, l := range lines {
			body := strings.TrimPrefix(strings.TrimSuffix(l.Text, cutMark), "y")
			if body != strings.Repeat(unit, strings.Count(body, unit)) {
				t.Errorf("%s: line %d broken inside a character: %q", name, i, l.Text)
			}
			if n := len(clusters(l.Text)); n > MenuLineWidth {
				t.Errorf("%s: line %d holds %d characters", name, i, n)
			}
		}
	}
	// A path in a sentence: the sentence's words stay whole, the path is
	// broken where it must.
	path := "/home/me/" + strings.Repeat("a-rather-long-folder-name/", 4) + "Notes"
	lines := texts(MenuLines("Could not open " + path + " here."))
	if len(lines) != 4 || lines[0] != "Could not open" || strings.Join(lines[1:3], "") != path || lines[3] != "here." {
		t.Errorf("a path: %q", lines)
	}
}

// A text from outside is cleaned as part 1's lines are: escape sequences
// whole, white space that breaks a line one space, what hides out.
func TestIssue291_MenuLinesAreCleaned(t *testing.T) {
	got := texts(MenuLines("bad\x1b[31m\tname\u202e here\r\nnext\x07 line"))
	if !reflect.DeepEqual(got, []string{"bad name here", "next line"}) {
		t.Fatalf("%q", got)
	}
}

// A notification leaves this app for the system's notification service: one
// line, cleaned, at most notifyMost characters. Every text the menu says fits
// whole.
func TestIssue291_ANotificationIsOneCleanBoundedLine(t *testing.T) {
	for _, s := range []string{startNoAnswerYet, startOlderRefusal, dialogFailureAtMost, noStartAnswer, pairingTimeoutNotice} {
		if got := notificationText(s); got != s {
			t.Errorf("not whole: %q", got)
		}
	}
	got := notificationText("✓ VaultSync is running again.\nNote: newer\x1b[0m one\u2028here.")
	if got != "✓ VaultSync is running again. Note: newer one here." {
		t.Errorf("%q", got)
	}
	long := strings.Repeat("word ", 200)
	got = notificationText(long)
	if n := len(clusters(got)); n > notifyMost || !strings.HasSuffix(got, cutMark) || strings.ContainsFunc(got, hides) {
		t.Errorf("%d characters: %q", n, got)
	}
}

// heldUI is the fake menu, whose Render can be held: the loop renders on its
// own goroutine, so while a Render is held the loop is.
type heldUI struct {
	*fakeUI
	mu      sync.Mutex
	hold    chan struct{}
	entered chan struct{}
}

func (u *heldUI) Render(v View) {
	u.mu.Lock()
	hold := u.hold
	u.hold = nil
	u.mu.Unlock()
	if hold != nil {
		u.entered <- struct{}{}
		<-hold
	}
	u.fakeUI.Render(v)
}

// holdNext holds the loop in its next Render, until release.
func (u *heldUI) holdNext() (release func()) {
	hold := make(chan struct{})
	u.mu.Lock()
	u.hold = hold
	u.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(hold) }) }
}

// What a pairing says reaches the menu through the loop alone: a pairing that
// ends while the loop is held is notified once the loop is free again, never
// from the pairing's own goroutine meanwhile.
func TestIssue291_APairingsNotificationComesFromTheLoop(t *testing.T) {
	agent := newFakeAgent(t)
	agent.set(func(f *fakeAgent) {
		f.hubs = 1
		f.status.Hubs = []Hub{{Name: "Office Hub", State: "connected", Kind: control.KindOK}}
	})
	ui := &heldUI{fakeUI: newFakeUI(t), entered: make(chan struct{}, 1)}
	// The code dialog waits for the gate, then finds no display.
	gate := make(chan struct{})
	asked := make(chan struct{}, 1)
	run := func(context.Context, string, []string, string) (string, string, int, error) {
		asked <- struct{}{}
		<-gate
		return "", zenityNoDisplay["zenity 4.2.2"], 1, nil
	}
	notes := make(chan string, 4)
	ctx, cancel := context.WithCancel(context.Background())
	c := &Controller{Home: "/home/me", Layout: agent.layout, UI: ui, Poll: time.Hour, StatusTimeout: 5 * time.Second,
		Asker: &zenityAsker{run: run}, Notify: func(_ context.Context, s string) { notes <- s }}
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	t.Cleanup(func() {
		close(gate)
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the controller did not stop")
		}
	})
	ui.view(t, "connected", func(v View) bool { return v.Pair.Enabled })
	ui.click(EventPair, 0)
	<-asked
	// The loop is held in a Render, and the pairing ends meanwhile.
	release := ui.holdNext()
	defer release()
	ui.click(EventOpened, 0)
	<-ui.entered
	gate <- struct{}{}
	if got := noted(notes, 400*time.Millisecond); got != "" {
		t.Fatalf("notified while the loop was held: %q", got)
	}
	release()
	got := noted(notes, 5*time.Second)
	if !strings.Contains(got, "zenity found no display") {
		t.Fatalf("notified %q", got)
	}
	v := ui.view(t, "the notice", func(v View) bool { return v.Notice != "" })
	if notificationText(v.Notice) != got {
		t.Fatalf("notice %q, notified %q", v.Notice, got)
	}
}

// A pause's answer is the agent's too: it goes with the agent.
func TestIssue291_APausesAnswerGoesWithTheAgent(t *testing.T) {
	agent := newFakeAgent(t)
	agent.set(func(f *fakeAgent) {
		f.status.Hubs = []Hub{{Name: "Office Hub", State: "connected", Kind: control.KindOK}}
	})
	ui := newFakeUI(t)
	controllerFor(t, agent, ui, &Controller{})
	ui.view(t, "connected", func(v View) bool { return v.Pause.Enabled })
	ui.send(Event{Kind: EventPause, Pause: true})
	v := ui.view(t, "the answer", func(v View) bool { return v.Notice != "" })
	if !strings.HasPrefix(v.Notice, "No Hub is paired") {
		t.Fatalf("%q", v.Notice)
	}
	agent.stop()
	ui.click(EventOpened, 0)
	if v = ui.view(t, "not running", notRunning); v.Notice != "" {
		t.Fatalf("under %q: %q", v.Headline, v.Notice)
	}
}

// An open's failure is the desktop's, not the agent's: it stays when the
// agent goes, as a start's notice keeps its own rule (decision 057).
func TestIssue291_AnOpensFailureStaysWhenTheAgentGoes(t *testing.T) {
	agent := syncingAgent(t)
	ui := newFakeUI(t)
	controllerFor(t, agent, ui, &Controller{Open: func(context.Context, string) error { return errOpenRefused }})
	ui.view(t, "up to date", upToDate)
	ui.send(Event{Kind: EventOpenVault, Path: "/home/me/Notes"})
	v := ui.view(t, "the open's failure", func(v View) bool { return v.Notice != "" })
	agent.stop()
	ui.click(EventOpened, 0)
	if v2 := ui.view(t, "not running", notRunning); v2.Notice != v.Notice {
		t.Fatalf("%q became %q", v.Notice, v2.Notice)
	}
}
