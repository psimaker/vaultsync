package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/psimaker/vaultsync/hub/pairing"
)

// #320 (f): the name typed for a new vault went through SanitizeName, which
// cuts at 64 bytes, and the length check behind it counted the characters of
// what was left — so it could never refuse. A name longer than 64
// characters, or one whose letters take more than one byte each, became a
// shorter vault name on the Hub that nobody was shown. The Hub counts 64
// characters, and the agent's own message says "1 to 64 characters".

// whole is 38 characters and 68 bytes: within the Hub's 64 characters, past
// the 64 bytes SanitizeName keeps.
var issue320Whole = "Notizen " + strings.Repeat("ü", 30)

// tooLong is one character more than the Hub takes.
var issue320TooLong = strings.Repeat("n", 65)

// createsAsked lists the names the Hub was asked to start a vault under.
func createsAsked(h *fakeHub) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var names []string
	for _, p := range h.provisions {
		if p.Create {
			names = append(names, p.Vault)
		}
	}
	return names
}

func TestIssue320_ANewVaultsNameWithinTheHubsLimitIsSentWhole(t *testing.T) {
	if utf8.RuneCountInString(issue320Whole) != 38 || len(issue320Whole) != 68 {
		t.Fatalf("the test's name: %d characters, %d bytes", utf8.RuneCountInString(issue320Whole), len(issue320Whole))
	}
	eng := newFakeEngine(t)
	hub := newFakeHub(t, eng)
	s, out := testSession(t, eng, hub, pairOptions{vault: issue320Whole, create: true, path: "~/Vaults/New"})
	s.opts.code = hub.code
	if err := os.MkdirAll(filepath.Join(s.env.home, "Vaults"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := createsAsked(hub); len(got) != 1 || got[0] != issue320Whole {
		t.Errorf("the Hub was asked to start %q, want %q", got, issue320Whole)
	}
	eng.mu.Lock()
	var labels []string
	for _, f := range eng.folders {
		labels = append(labels, f.Label)
	}
	eng.mu.Unlock()
	if len(labels) != 1 || labels[0] != issue320Whole {
		t.Errorf("the sync engine's folder: %q, want %q", labels, issue320Whole)
	}
}

// The menu-bar app starts a vault from this computer with the name the agent
// proposed for it, through the same flags and without a terminal: the name
// it showed is the name the Hub is asked for.
func TestIssue320_TheMenuBarAppsRequestSendsTheNameWhole(t *testing.T) {
	eng := newFakeEngine(t)
	hub := newFakeHub(t, eng)
	a := agentApp(t, eng)
	fresh := filepath.Join(a.home, "Vaults", "New")
	if err := os.MkdirAll(filepath.Dir(fresh), 0o755); err != nil {
		t.Fatal(err)
	}
	opts := pairOptions{code: hub.code, hub: hub.addr, vault: issue320Whole, create: true, path: fresh}
	if _, err := a.pairWith(context.Background(), &term{out: io.Discard}, opts, eng.client, pairOrigin{background: true, service: true}); err != nil {
		t.Fatal(err)
	}
	if got := createsAsked(hub); len(got) != 1 || got[0] != issue320Whole {
		t.Errorf("the Hub was asked to start %q, want %q", got, issue320Whole)
	}
}

func TestIssue320_ANewVaultsNamePastTheHubsLimitIsRefusedNotCut(t *testing.T) {
	eng := newFakeEngine(t)
	hub := newFakeHub(t, eng)
	s, out := testSession(t, eng, hub, pairOptions{vault: issue320TooLong, create: true, path: "~/Vaults/New"})
	s.opts.code = hub.code
	if err := os.MkdirAll(filepath.Join(s.env.home, "Vaults"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := s.run(context.Background())
	if refusalText(err) != "Use a vault name of 1 to 64 characters." {
		t.Errorf("--vault <65 characters> --create: %v\n%s", err, out)
	}
	if got := createsAsked(hub); len(got) != 0 || eng.folderCount() != 0 {
		t.Errorf("the Hub was asked to start %q, and %d folders were added", got, eng.folderCount())
	}
}

// The terminal offers the folder's own name as the default; Enter takes it.
// The name sent must be the one shown in the brackets.
func TestIssue320_TheDefaultNameShownIsTheNameUsed(t *testing.T) {
	eng := newFakeEngine(t)
	s, out := testSession(t, eng, nil, pairOptions{}, "")
	name, err := s.askHubName(context.Background(), issue320Whole, false)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "["+issue320Whole+"]") {
		t.Fatalf("the default shown:\n%q", out)
	}
	if name != issue320Whole {
		t.Errorf("Enter on the default %q gave the name %q", issue320Whole, name)
	}
}

// A typed name past 64 characters is asked again, never cut.
func TestIssue320_ATypedNamePastTheHubsLimitIsAskedAgain(t *testing.T) {
	eng := newFakeEngine(t)
	s, out := testSession(t, eng, nil, pairOptions{}, issue320TooLong, "Short")
	name, err := s.askHubName(context.Background(), "Notes", false)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "  Use a name of 1 to 64 characters.\n") || name != "Short" {
		t.Errorf("a name of 65 characters gave %q\n%s", name, out)
	}
	if pairing.SanitizeName(issue320TooLong) == issue320TooLong {
		t.Fatal("the test's name is not past what SanitizeName keeps")
	}
}
