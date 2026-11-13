package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psimaker/vaultsync/hub/pairing"
	"github.com/psimaker/vaultsync/hub/syncthing"
)

// #292: a Hub's name comes from anyone on the network and is shown beside the
// address a person chooses the Hub by. Decision 061.

// The terminal's list printed the name first and the address last. A name
// can hold anything that looks like an address, and one Hebrew or Arabic
// letter in it laid the row out from the right in a terminal that follows a
// text's direction, the address before the name. The row now begins with the
// address, the one field an answer cannot write, and a left-to-right mark
// before the name gives it its direction (decisions 056 and 061).
func TestIssue292_TheTerminalListsEachHubByItsAddressFirst(t *testing.T) {
	eng := newFakeEngine(t)
	// Nothing that is no number of the list chooses a Hub; the third answer does.
	s, out := testSession(t, eng, nil, pairOptions{}, "", "4", "2")
	s.env.discover = func(context.Context) ([]pairing.DiscoveredHub, error) {
		return []pairing.DiscoveredHub{
			{Address: "192.168.1.66:8390", Name: "Home                 192.168.1.20"},
			{Address: "192.168.1.20:8390", Name: "\u05d1\u05d9\u05ea 192.168.1.66"},
			{Address: "192.168.1.21:8390"},
		}, nil
	}
	if err := s.findHub(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if s.hub.Address != "192.168.1.20:8390" {
		t.Fatalf("the Hub chosen: %+v", s.hub)
	}
	const ask = "  Which Hub printed your code? Enter a number: "
	want := "  Looking for your Hub on this network… 3 Hubs answered:\n" +
		"    1) 192.168.1.66 — \u200eHome                 192.168.1.20\n" +
		"    2) 192.168.1.20 — \u200e\u05d1\u05d9\u05ea 192.168.1.66\n" +
		"    3) 192.168.1.21 — \u200eyour Hub\n" +
		ask + ask + ask
	if out.String() != want {
		t.Fatalf("the list:\n%q\nwant:\n%q", out.String(), want)
	}
}

// One Hub answered: the code goes to it without a question, and this line is
// all a person sees of where. It named the Hub and then its address: a name
// in a right-to-left script drew the address before itself, inside its
// quotes, and a name with a quote and a bracket in it wrote an address of
// its own before the real one. The address now comes first here too.
func TestIssue292_TheOneHubFoundIsShownByItsAddressFirst(t *testing.T) {
	eng := newFakeEngine(t)
	s, out := testSession(t, eng, nil, pairOptions{})
	s.env.discover = func(context.Context) ([]pairing.DiscoveredHub, error) {
		return []pairing.DiscoveredHub{{Address: "192.168.1.20:8390", Name: "\u05d1\u05d9\u05ea 2"}}, nil
	}
	if err := s.findHub(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := "  Looking for your Hub on this network… found 192.168.1.20 — \u200e“\u05d1\u05d9\u05ea 2”\n"
	if out.String() != want {
		t.Fatalf("the line:\n%q\nwant:\n%q", out.String(), want)
	}
}

// The name a Hub gives itself in the handshake is shown, recorded and given
// to the sync engine without what hides (#292).
func TestIssue292_TheHubsOwnNameIsKeptWithoutWhatHides(t *testing.T) {
	eng := newFakeEngine(t)
	hub := newFakeHub(t, eng, pairing.VaultInfo{ID: "vs-aaaaaaaaaaaa", Label: "Recipes", Files: 12})
	hub.name = "\u202eHome\u2028 Hub\u0085\u200f"
	s, out := testSession(t, eng, hub, pairOptions{}, hub.code, "1", "")
	if err := s.run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "✓ Connected to “Home Hub”.\n") {
		t.Errorf("the line that names the Hub:\n%q", out)
	}
	if strings.ContainsFunc(out.String(), func(r rune) bool { return r == 0x202e || r == 0x2028 || r == 0x85 || r == 0x200f }) {
		t.Errorf("the output carries what the name brought:\n%q", out)
	}
	named := false
	for _, d := range eng.devices {
		if d.DeviceID == testHubID {
			named = d.Name == "Home Hub"
		}
	}
	if !named {
		t.Errorf("the Hub as the sync engine knows it: %+v", eng.devices)
	}
	if att, _ := loadAttempts(s.env.lay); len(att) != 1 || att[0].Hub != "Home Hub" {
		t.Errorf("the attempt's record: %+v", att)
	}
}

// --create asks for a new vault, and the name the Hub is asked for is the
// cleaned one. The catalogue was searched for the name as typed only: with a
// character in it that cleaning drops — a control character, and now a
// pasted zero-width space, a soft hyphen, a direction mark — or with more
// than the 64 bytes cleaning keeps, an existing vault of the cleaned name
// was not found here, the Hub found it, and an empty folder received a vault
// nobody chose. The search goes by the cleaned name too (#292).
func TestIssue292_CreateFindsAnExistingVaultByTheCleanedName(t *testing.T) {
	ctx := context.Background()
	const id = "vs-aaaaaaaaaaaa"
	long := strings.Repeat("я", 32) // 64 bytes: all that is kept of a longer name
	for _, typed := range []string{"Notes\u200b", "No\u00adtes", "\ufeffNotes", "Notes\u200e", "notes\u2028", "Notes\x01", long + "2", long + " 2027"} {
		label := "Notes"
		if strings.HasPrefix(typed, long) {
			label = long
		}
		eng := newFakeEngine(t)
		hub := newFakeHub(t, eng, pairing.VaultInfo{ID: id, Label: label, Files: 3})
		s, out := testSession(t, eng, hub, pairOptions{vault: typed, create: true, path: "~/Vaults/New"})
		s.opts.code = hub.code
		// The folder above exists: nothing but the name stands in the way.
		if err := os.MkdirAll(filepath.Join(s.env.home, "Vaults"), 0o755); err != nil {
			t.Fatal(err)
		}
		err := s.run(ctx)
		if !strings.Contains(refusalText(err), "Your Hub already has a vault named “"+label+"”.") {
			t.Errorf("--vault %+q --create: %v\n%s", typed, err, out)
		}
		if n := hub.provisionCount(label); n != 0 || eng.folderCount() != 0 {
			t.Errorf("--vault %+q --create: the Hub was asked for the existing vault's name %d times, and %d folders were added", typed, n, eng.folderCount())
		}
	}

	// The same flags again, after they set the vault up here: it is found,
	// as for a name that needs no cleaning (#228).
	eng := newFakeEngine(t)
	hub := newFakeHub(t, eng, pairing.VaultInfo{ID: id, Label: "Notes", Files: 3, SharedWith: []string{testMyID}})
	s, out := testSession(t, eng, hub, pairOptions{vault: "Notes\u200b", create: true, path: "~/Vaults/Notes"})
	s.opts.code = hub.code
	local := filepath.Join(s.env.home, "Vaults", "Notes")
	mkVault(t, local)
	eng.mu.Lock()
	eng.folders = append(eng.folders, syncthing.FolderConfig{ID: id, Label: "Notes", Path: local})
	eng.mu.Unlock()
	if err := s.run(ctx); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "✓ “Notes” is already set up to sync at ~/Vaults/Notes.") || strings.Contains(out.String(), "overlaps") {
		t.Fatalf("the same flags again:\n%s", out)
	}
	if hub.provisionCount(id) != 0 || eng.folderCount() != 1 {
		t.Fatal("the Hub was asked, or a folder was added")
	}

	// Without --create nothing is cleaned: a vault is chosen by its ID or by
	// its label as the Hub has it.
	eng = newFakeEngine(t)
	hub = newFakeHub(t, eng, pairing.VaultInfo{ID: id, Label: "Notes", Files: 3})
	s, _ = testSession(t, eng, hub, pairOptions{vault: "Notes\u200b", path: "~/Vaults/New"})
	s.opts.code = hub.code
	if err := s.run(ctx); !strings.Contains(refusalText(err), "Your Hub has no vault named") {
		t.Fatalf("without --create: %v", err)
	}
}
