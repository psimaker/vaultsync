package main

import (
	"context"
	"strings"
	"testing"

	"github.com/psimaker/vaultsync/hub/pairing"
)

// #292: a Hub's name comes from anyone on the network and is shown beside the
// address a person chooses the Hub by. Decision 061.

// The terminal's list printed the name first and the address last. A name
// can hold anything that looks like an address, and one Hebrew or Arabic
// letter in it laid the row out from the right in a terminal that follows a
// text's direction, the address before the name. The row now begins with the
// address, the one field an answer cannot write, and a left-to-right mark
// gives it its direction, as the menu's rows do (decision 056).
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
		"    1) 192.168.1.66 \u200e— Home                 192.168.1.20\n" +
		"    2) 192.168.1.20 \u200e— \u05d1\u05d9\u05ea 192.168.1.66\n" +
		"    3) 192.168.1.21 \u200e— your Hub\n" +
		ask + ask + ask
	if out.String() != want {
		t.Fatalf("the list:\n%q\nwant:\n%q", out.String(), want)
	}
	// The mark is the first character of a row that says a direction: before
	// it stand only the number, the address and spaces.
	for _, row := range strings.Split(out.String(), "\n")[1:4] {
		before, rest, found := strings.Cut(row, "\u200e")
		if !found || strings.Contains(rest, "\u200e") || !strings.HasSuffix(before, " ") || strings.ContainsFunc(before, func(r rune) bool { return r > 0x7f }) {
			t.Errorf("the row %q", row)
		}
	}
}

// One Hub answered: the line names it and then its address. A name in a
// right-to-left script drew the address before itself, inside its quotes; a
// left-to-right mark after the name keeps the address where it is written.
func TestIssue292_TheOneHubFoundKeepsItsAddressAfterItsName(t *testing.T) {
	eng := newFakeEngine(t)
	s, out := testSession(t, eng, nil, pairOptions{})
	s.env.discover = func(context.Context) ([]pairing.DiscoveredHub, error) {
		return []pairing.DiscoveredHub{{Address: "192.168.1.20:8390", Name: "\u05d1\u05d9\u05ea 2"}}, nil
	}
	if err := s.findHub(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := "  Looking for your Hub on this network… found “\u05d1\u05d9\u05ea 2”\u200e (192.168.1.20)\n"
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
