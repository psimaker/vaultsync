package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/psimaker/vaultsync/hub/pairing"
	"github.com/psimaker/vaultsync/hub/syncthing"
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
// For a folder name without a character that hides, the name sent is the one
// shown in the brackets. One with such a character is shown with it and sent
// without it until the default is made from the cleaned name (decision 089
// point 8).
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

// issue320Earlier is what desktop-v0.3.0 and earlier sent the Hub for
// issue320Whole: the name cut to 64 bytes.
var issue320Earlier = "Notizen " + strings.Repeat("ü", 28)

// A vault an earlier VaultSync started from a long name has it on the Hub cut
// to 64 bytes. The same command, run again by this build, must find that
// vault — not end in a refusal for the folder it already syncs, and not
// start a second vault beside it.
func TestIssue320_ARunAgainFindsTheVaultAnEarlierVaultSyncMadeUnderTheCutName(t *testing.T) {
	ctx := context.Background()
	const id = "vs-aaaaaaaaaaaa"
	if pairing.SanitizeName(issue320Whole) != issue320Earlier {
		t.Fatalf("the cut form: %q", pairing.SanitizeName(issue320Whole))
	}
	has := "Your Hub already has this vault, under the name “" + issue320Earlier + "” an earlier VaultSync made from yours"
	session := func(t *testing.T, path string) (*fakeEngine, *fakeHub, *pairSession, *bytes.Buffer) {
		eng := newFakeEngine(t)
		hub := newFakeHub(t, eng, pairing.VaultInfo{ID: id, Label: issue320Earlier, Files: 3, SharedWith: []string{testMyID}})
		s, out := testSession(t, eng, hub, pairOptions{vault: issue320Whole, create: true, path: path})
		s.opts.code = hub.code
		if err := os.MkdirAll(filepath.Join(s.env.home, "Vaults"), 0o755); err != nil {
			t.Fatal(err)
		}
		return eng, hub, s, out
	}
	configure := func(t *testing.T, eng *fakeEngine, local string) {
		mkVault(t, local)
		eng.mu.Lock()
		eng.folders = append(eng.folders, syncthing.FolderConfig{ID: id, Label: issue320Earlier, Path: local})
		eng.mu.Unlock()
	}
	t.Run("set up at this folder: the run succeeds and names the Hub's name", func(t *testing.T) {
		eng, hub, s, out := session(t, "~/Vaults/N")
		configure(t, eng, filepath.Join(s.env.home, "Vaults", "N"))
		if err := s.run(ctx); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if !strings.Contains(out.String(), has+".\n") || !strings.Contains(out.String(), "✓ “"+issue320Earlier+"” is already set up to sync at ~/Vaults/N.") {
			t.Fatalf("output:\n%s", out)
		}
		if got := createsAsked(hub); len(got) != 0 || hub.provisionCount(id) != 0 || eng.folderCount() != 1 {
			t.Fatalf("the Hub was asked to start %q, or a folder was added", got)
		}
	})
	t.Run("not set up here: refused, with the Hub's name", func(t *testing.T) {
		eng, hub, s, out := session(t, "~/Vaults/New")
		err := s.run(ctx)
		if !strings.Contains(refusalText(err), has+". Download it as the vault on your Hub") {
			t.Fatalf("%v\n%s", err, out)
		}
		if got := createsAsked(hub); len(got) != 0 || eng.folderCount() != 0 {
			t.Fatalf("the Hub was asked to start %q, or a folder was added", got)
		}
	})
	t.Run("set up at another folder: refused, with where", func(t *testing.T) {
		eng, hub, s, out := session(t, "~/Vaults/New")
		configure(t, eng, filepath.Join(s.env.home, "Vaults", "Other"))
		err := s.run(ctx)
		if !strings.Contains(refusalText(err), has+", and it is set up to sync at ~/Vaults/Other on this computer.") {
			t.Fatalf("%v\n%s", err, out)
		}
		if got := createsAsked(hub); len(got) != 0 || eng.folderCount() != 1 {
			t.Fatalf("the Hub was asked to start %q, or a folder was added", got)
		}
	})
	t.Run("the terminal's question", func(t *testing.T) {
		eng := newFakeEngine(t)
		s, out := testSession(t, eng, nil, pairOptions{}, "", "Short")
		s.hello.Vaults = []pairing.VaultInfo{{ID: id, Label: issue320Earlier, Files: 3}}
		name, err := s.askHubName(ctx, issue320Whole, false)
		if err != nil || name != "Short" {
			t.Fatalf("got %q, %v\n%s", name, err, out)
		}
		if !strings.Contains(out.String(), "  "+has+". To download it, choose it under On your Hub") {
			t.Fatalf("output:\n%s", out)
		}
	})
}

// The reverse: where the Hub has the whole name — a vault this build
// started — that is the vault, also beside one under the cut name. (A
// released VaultSync finds the whole-name vault too: it searches the name
// as typed as well.)
func TestIssue320_AVaultUnderTheWholeNameComesBeforeTheCutOne(t *testing.T) {
	eng := newFakeEngine(t)
	hub := newFakeHub(t, eng,
		pairing.VaultInfo{ID: "vs-aaaaaaaaaaaa", Label: issue320Earlier, Files: 3},
		pairing.VaultInfo{ID: "vs-bbbbbbbbbbbb", Label: issue320Whole, Files: 2})
	s, out := testSession(t, eng, hub, pairOptions{vault: issue320Whole, create: true, path: "~/Vaults/New"})
	s.opts.code = hub.code
	if err := os.MkdirAll(filepath.Join(s.env.home, "Vaults"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := s.run(context.Background())
	if !strings.Contains(refusalText(err), "Your Hub already has a vault named “"+issue320Whole+"”. Download it") {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := createsAsked(hub); len(got) != 0 || eng.folderCount() != 0 {
		t.Fatalf("the Hub was asked to start %q, or a folder was added", got)
	}
}

// A share the Hub already sends is finished without a code, and its label
// becomes the label of the folder added here — which is kept, not cut.
func TestIssue320_AShareFinishedLaterKeepsTheVaultsWholeName(t *testing.T) {
	const id = "vs-aaaaaaaaaaaa"
	eng := newFakeEngine(t)
	s, out := testSession(t, eng, nil, pairOptions{}, "y", "~/Vaults/Long")
	eng.mu.Lock()
	eng.devices = append(eng.devices, syncthing.DeviceConfig{DeviceID: testHubID, Name: "Test Hub"})
	eng.pending[id] = map[string]string{testHubID: issue320Whole}
	eng.mu.Unlock()
	if err := s.run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	eng.mu.Lock()
	var labels []string
	for _, f := range eng.folders {
		labels = append(labels, f.Label)
	}
	eng.mu.Unlock()
	if len(labels) != 1 || labels[0] != issue320Whole {
		t.Fatalf("the sync engine's folder: %q, want %q\n%s", labels, issue320Whole, out)
	}
	if !strings.Contains(out.String(), "✓ “"+issue320Whole+"” is set up to sync at ~/Vaults/Long.") {
		t.Fatalf("output:\n%s", out)
	}
}
