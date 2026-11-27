package tray

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/psimaker/vaultsync/desktop/internal/control"
)

// #291, the menu's side: a pairing that ends where no dialog showed it is
// notified, a pairing's words go with the agent that said them, and what a
// start notes after it worked is notified.

// pairingMenu runs a pairing from the menu with ask, against an agent that asks
// for a vault with menu, and returns the view it ended on, what was notified
// meanwhile, and the agent and the menu for what the test does next.
func pairingMenu(t *testing.T, ask Asker, menu Menu) (View, <-chan string, *fakeAgent, *fakeUI) {
	t.Helper()
	agent := newFakeAgent(t)
	agent.set(func(f *fakeAgent) {
		f.hubs = 1
		f.status.Hubs = []Hub{{Name: "Office Hub", State: "connected", Kind: control.KindOK}}
		f.pair = func(PairRequest) (PairResult, int) {
			return PairResult{Refusal: "Pass --vault NAME and --path FOLDER (with --create for a new vault on your Hub) — there is no terminal to ask on.", Needs: "vault", Menu: &menu}, 200
		}
	})
	ui := newFakeUI(t)
	notes := make(chan string, 8)
	controllerFor(t, agent, ui, &Controller{Asker: ask, Notify: func(_ context.Context, s string) { notes <- s }})
	ui.view(t, "connected", func(v View) bool { return v.Pair.Enabled })
	ui.click(EventPair, 0)
	ui.view(t, "pairing", func(v View) bool { return v.Pair.Title == "Pairing…" })
	v := ui.view(t, "the pairing's end", func(v View) bool { return v.Pair.Enabled })
	return v, notes, agent, ui
}

// noted waits up to wait for a notification; "" when none came.
func noted(notes <-chan string, wait time.Duration) string {
	select {
	case s := <-notes:
		return s
	case <-time.After(wait):
		return ""
	}
}

var hubVaultSyncs = Menu{Already: []MenuEntry{{Label: "Notes", Vault: "vs-1", Note: "~/Vaults/Notes"}}}

// A person who clicked Pair a Vault… and saw no dialog learns of its end only
// by opening the menu again: a pairing that ends on a dialog that could not be
// shown, or on a message that could not be shown, is notified. One whose end a
// dialog showed is not, nor a cancel.
func TestIssue291_APairingThatEndsUnseenIsNotified(t *testing.T) {
	// No display: the code dialog is never shown.
	v, notes, _, _ := pairingMenu(t, &zenityAsker{run: (&recorder{status: 1, stderr: zenityNoDisplay["zenity 4.2.2"]}).run}, hubVaultSyncs)
	if !strings.Contains(v.Notice, "zenity found no display") {
		t.Fatalf("notice: %q", v.Notice)
	}
	if got := noted(notes, 5*time.Second); got != v.Notice {
		t.Fatalf("notified %q, the notice is %q", got, v.Notice)
	}

	// The code was asked, and the message the pairing ends on could not be
	// shown (zenity, a name outside ASCII, no UTF-8 locale).
	v, notes, _, _ = pairingMenu(t, &zenityAsker{run: (&recorder{stdouts: []string{"TULIP-ANCHOR-42\n", ""}, failFrom: 2}).run}, hubVaultSyncs)
	if v.Notice != everyVaultSyncs {
		t.Fatalf("notice: %q", v.Notice)
	}
	if got := noted(notes, 5*time.Second); got != everyVaultSyncs {
		t.Fatalf("notified %q", got)
	}

	// The message was shown: nothing more to say.
	v, notes, _, _ = pairingMenu(t, &zenityAsker{run: (&recorder{stdouts: []string{"TULIP-ANCHOR-42\n", ""}}).run}, hubVaultSyncs)
	if v.Notice != everyVaultSyncs {
		t.Fatalf("notice: %q", v.Notice)
	}
	if got := noted(notes, 300*time.Millisecond); got != "" {
		t.Fatalf("a message that was shown was notified too: %q", got)
	}

	// A cancel: nothing to say.
	v, notes, _, _ = pairingMenu(t, &zenityAsker{run: (&recorder{status: 1}).run}, hubVaultSyncs)
	if v.Notice != "" {
		t.Fatalf("notice after a cancel: %q", v.Notice)
	}
	if got := noted(notes, 300*time.Millisecond); got != "" {
		t.Fatalf("a cancel was notified: %q", got)
	}
}

// After vaultsync stop, a pairing's last words stood under "VaultSync is not
// running" until something else was clicked: they were the agent's, and the
// agent is gone.
func TestIssue291_APairingsWordsGoWithTheAgent(t *testing.T) {
	v, _, agent, ui := pairingMenu(t, &zenityAsker{run: (&recorder{stdouts: []string{"TULIP-ANCHOR-42\n", ""}}).run}, hubVaultSyncs)
	if v.Notice != everyVaultSyncs {
		t.Fatalf("notice: %q", v.Notice)
	}
	agent.stop()
	ui.click(EventOpened, 0)
	v = ui.view(t, "not running", notRunning)
	if v.Notice != "" {
		t.Fatalf("under %q: %q", v.Headline, v.Notice)
	}
}

// What vaultsync start notes after it worked — a note on a VaultSync newer
// than the build that runs, with its remedy — is notified, as a refusal is.
// A start that only says it runs is not (#248).
func TestIssue291_AStartsNoteIsNotified(t *testing.T) {
	agent := newFakeAgent(t)
	agent.stop()
	ui := newFakeUI(t)
	said := "✓ VaultSync is running again.\nNote: VaultSync 9.9.9 is set up on this computer, newer than this one. Run the setup link to get the current VaultSync."
	started := &fakeStart{said: said}
	notes := make(chan string, 4)
	controllerFor(t, agent, ui, &Controller{
		StartProgram: func() string { return "/usr/local/bin/vaultsync" },
		Start:        started.start,
		Notify:       func(_ context.Context, s string) { notes <- s },
	})
	ui.view(t, "not running", notRunning)
	ui.send(Event{Kind: EventStart})
	ui.view(t, "what start said", func(v View) bool { return v.Notice != "" })
	if got := noted(notes, 5*time.Second); !strings.Contains(got, "Note: VaultSync 9.9.9 is set up on this computer") {
		t.Fatalf("notified %q", got)
	}
}
