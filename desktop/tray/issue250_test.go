package tray

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// #250: when several Hubs answer, the menu offers them by name and sends the
// chosen one's address; beside an agent older than the list it asks for an
// address, as before. Decision 056.

var twoHubs = []HubChoice{{Name: "Home", Address: "192.168.1.20:8390"}, {Address: "192.168.1.21:8390"}}

const severalWords = "2 Hubs answered (Home at 192.168.1.20:8390, 192.168.1.21:8390) — choose yours with --hub; a code is sent only to the Hub you choose. VaultSync sends a code only to the Hub you choose."

// severalHubsAgent asks for a Hub until it gets one, with hubs in its answer.
func severalHubsAgent(t *testing.T, hubs []HubChoice) *fakeAgent {
	t.Helper()
	agent := newFakeAgent(t)
	agent.set(func(f *fakeAgent) {
		inner := pairingScript(t, "TULIP-ANCHOR-42")
		f.pair = func(req PairRequest) (PairResult, int) {
			if req.Hub == "" {
				return PairResult{Refusal: severalWords, Needs: "hub", Hubs: hubs}, 200
			}
			return inner(req)
		}
	})
	return agent
}

func TestIssue250_TheMenuOffersTheHubsAndSendsTheChosenAddress(t *testing.T) {
	agent := severalHubsAgent(t, twoHubs)
	ask := &fakeAsker{code: "TULIP-ANCHOR-42", hub: "192.168.1.21:8390", choose: func(m Menu) (Choice, bool) { return Choice{Entry: m.Hub[0]}, true }}
	line, err := Pair(context.Background(), NewClient(agent.socket), ask)
	if err != nil || !strings.Contains(line, "is set up") {
		t.Fatalf("%q %v", line, err)
	}
	if got := ask.sequence(); len(got) < 2 || got[1] != "hubs:2" {
		t.Fatalf("asked: %v", got)
	}
	agent.set(func(f *fakeAgent) {
		if f.pairs[0].Hub != "" || len(f.pairs) < 2 {
			t.Fatalf("requests: %+v", f.pairs)
		}
		for _, r := range f.pairs[1:] {
			if r.Hub != "192.168.1.21:8390" {
				t.Fatalf("the Hub was not carried along: %+v", f.pairs)
			}
		}
	})
}

// A cancel sends nothing more.
func TestIssue250_NoHubChosenSendsNothing(t *testing.T) {
	agent := severalHubsAgent(t, twoHubs)
	ask := &fakeAsker{code: "TULIP-ANCHOR-42", cancel: "hub"}
	if line, err := Pair(context.Background(), NewClient(agent.socket), ask); err != nil || line != "" {
		t.Fatalf("%q %v", line, err)
	}
	agent.set(func(f *fakeAgent) {
		if len(f.pairs) != 1 {
			t.Fatalf("requests after the cancel: %+v", f.pairs)
		}
	})
}

// Beside an agent older than the list there is no list: the words and the
// address prompt, as before.
func TestIssue250_AnOlderAgentIsAskedForAnAddress(t *testing.T) {
	agent := severalHubsAgent(t, nil)
	ask := &fakeAsker{code: "TULIP-ANCHOR-42", hub: "192.168.1.20", choose: func(m Menu) (Choice, bool) { return Choice{Entry: m.Hub[0]}, true }}
	line, err := Pair(context.Background(), NewClient(agent.socket), ask)
	if err != nil || !strings.Contains(line, "is set up") {
		t.Fatalf("%q %v", line, err)
	}
	if got := ask.sequence(); len(got) < 2 || got[1] != "hub:"+severalWords {
		t.Fatalf("asked: %v", got)
	}
	agent.set(func(f *fakeAgent) {
		if f.pairs[len(f.pairs)-1].Hub != "192.168.1.20" {
			t.Fatalf("requests: %+v", f.pairs)
		}
	})
}

// The address first, the one field an answer cannot write; a name is only a
// hint from the network, and what it can do to a row is taken out of it.
func TestIssue250_TheRowsNameEachHub(t *testing.T) {
	got := hubItems([]HubChoice{
		{Name: "Home", Address: "192.168.1.20:8390"},
		{Address: "192.168.1.21:8390"},
		{Name: "Home", Address: "[fe80::1]:8390"},
		{Name: "No port", Address: "192.168.1.22"},
		{Name: "Home Hub — 192.168.1.20\u2028\u2028\u2028.", Address: "192.168.1.67:8390"},
		{Name: "\u202eHub evil\u200b", Address: "192.168.1.68:8390"},
		{Name: " \u2029\u202e ", Address: "192.168.1.69:8390"},
		{Name: `R&D <b>x</b> a_b`, Address: "192.168.1.70:8390"},
		{Name: "Attic\u2029Hub", Address: "192.168.1.71:8390"},
	})
	want := []string{
		"192.168.1.20 — Home",
		"192.168.1.21 — your Hub",
		"fe80::1 — Home",
		"192.168.1.22 — No port",
		"192.168.1.67 — Home Hub — 192.168.1.20.",
		"192.168.1.68 — Hub evil",
		"192.168.1.69 — your Hub",
		"192.168.1.70 — R&D <b>x</b> a_b",
		"192.168.1.71 — AtticHub",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
}

// Each program: the list, the row read back by its tag or number (two Hubs may
// have one name), the row that chooses nothing, and a cancel.
func TestIssue250_EveryProgramOffersTheHubs(t *testing.T) {
	ctx := context.Background()
	sameName := []HubChoice{{Name: "Home", Address: "192.168.1.20:8390"}, {Name: "Home", Address: "192.168.1.21:8390"}}
	items := hubItems(sameName)
	linux := map[string]func(*recorder) Asker{
		"zenity":  func(r *recorder) Asker { return &zenityAsker{run: r.run} },
		"kdialog": func(r *recorder) Asker { return &kdialogAsker{run: r.run} },
	}
	for name, a := range linux {
		r := &recorder{stdout: "2)\n"}
		addr, ok, err := a(r).ChooseHub(ctx, sameName)
		if err != nil || !ok || addr != "192.168.1.21:8390" {
			t.Fatalf("%s: %q %v %v", name, addr, ok, err)
		}
		if got := r.args[len(r.args)-6:]; !reflect.DeepEqual(got, []string{noChoiceTag, noHubRow, "1)", items[0], "2)", items[1]}) {
			t.Fatalf("%s rows: %q", name, got)
		}
		if !strings.Contains(strings.Join(r.args, "\x00"), "Which Hub printed your code?") {
			t.Fatalf("%s prompt: %q", name, r.args)
		}
		// Return alone, then a choice: asked again, and only the chosen hears.
		r = &recorder{stdouts: []string{noChoiceTag + "\n", "1)\n"}}
		addr, ok, err = a(r).ChooseHub(ctx, sameName)
		if err != nil || !ok || addr != "192.168.1.20:8390" || r.calls != 2 {
			t.Fatalf("%s, Return first: %q %v %v, %d times", name, addr, ok, err, r.calls)
		}
		// Never a choice: none, not the first.
		r = &recorder{stdout: noChoiceTag + "\n"}
		if addr, ok, err := a(r).ChooseHub(ctx, sameName); addr != "" || ok || err != nil {
			t.Fatalf("%s, never chosen: %q %v %v", name, addr, ok, err)
		}
		r = &recorder{status: 1}
		if addr, ok, err := a(r).ChooseHub(ctx, sameName); addr != "" || ok || err != nil {
			t.Fatalf("%s, Cancel: %q %v %v", name, addr, ok, err)
		}
		r = &recorder{stdout: "9)\n"}
		if _, _, err := a(r).ChooseHub(ctx, sameName); err == nil {
			t.Errorf("%s: a row not in the list", name)
		}
		// A dialog that cannot be shown is a failure, not a cancel.
		r = &recorder{err: errors.New("no display")}
		if _, _, err := a(r).ChooseHub(ctx, sameName); err == nil {
			t.Errorf("%s: a failure", name)
		}
	}

	// osascript: its list opens with nothing selected, and needs no such row.
	r := &recorder{stdout: "2) " + items[1] + "\n"}
	addr, ok, err := (&scriptAsker{run: r.run}).ChooseHub(ctx, sameName)
	if err != nil || !ok || addr != "192.168.1.21:8390" {
		t.Fatalf("osascript: %q %v %v", addr, ok, err)
	}
	if !strings.Contains(r.stdin, "choose from list "+appleScriptList([]string{"1) " + items[0], "2) " + items[1]})) || !strings.Contains(r.stdin, appleScriptString(hubListPrompt)) {
		t.Fatalf("osascript script: %s", r.stdin)
	}
	r = &recorder{stderr: "execution error: User canceled. (-128)", status: 1}
	if addr, ok, err := (&scriptAsker{run: r.run}).ChooseHub(ctx, sameName); addr != "" || ok || err != nil {
		t.Fatalf("osascript cancel: %q %v %v", addr, ok, err)
	}
	r = &recorder{stdout: "9) Elsewhere\n"}
	if _, _, err := (&scriptAsker{run: r.run}).ChooseHub(ctx, sameName); err == nil {
		t.Error("osascript: a row not in the list")
	}
}
