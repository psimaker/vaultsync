//go:build unix

package main

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/psimaker/vaultsync/desktop/internal/control"
	"github.com/psimaker/vaultsync/hub/pairing"
)

// #250: the socket's answer names the Hubs that answered, beside the words and
// the needs it always had. Decision 056.

// v020PairResult is the answer as a menu-bar app from desktop-v0.2.0 reads it.
// Frozen: it still runs beside a newer agent after a Finder drag.
type v020PairResult struct {
	OK      bool   `json:"ok"`
	Output  string `json:"output"`
	Refusal string `json:"refusal,omitempty"`
	Needs   string `json:"needs,omitempty"`
	Consent string `json:"consent,omitempty"`
	Folder  string `json:"folder,omitempty"`
}

func TestIssue250_TheSocketNamesTheHubsBesideItsWords(t *testing.T) {
	eng := newFakeEngine(t)
	notes := pairing.VaultInfo{ID: "vs-aaaaaaaaaaaa", Label: "Notes"}
	a, b := newFakeHub(t, eng, notes), newFakeHub(t, eng, notes)
	agent := agentApp(t, eng)
	agent.discover = func(context.Context) ([]pairing.DiscoveredHub, error) {
		return []pairing.DiscoveredHub{{Address: a.addr, Name: "Home"}, {Address: b.addr}, {Address: "203.0.113.5:8390", Name: "Elsewhere"}}, nil
	}
	client := serveControl(t, agent)
	ctx := context.Background()

	res, err := client.Pair(ctx, pairRequest{Code: b.code})
	if err != nil {
		t.Fatal(err)
	}
	want := []control.HubChoice{{Name: "Home", Address: a.addr}, {Address: b.addr}}
	if res.OK || res.Needs != "hub" || !reflect.DeepEqual(res.Hubs, want) {
		t.Fatalf("the answer: %+v", res)
	}
	// Nobody has heard the code: decision 045 does not move.
	if a.starts+b.starts != 0 {
		t.Fatalf("a Hub heard the code: %d, %d", a.starts, b.starts)
	}

	// A v0.2.0 menu reads the words it always read, and asks for an address
	// under them. They are spelled out: it is the older app that shows them.
	var old v020PairResult
	if err := client.Do(ctx, http.MethodPost, "/v1/pair", pairRequest{Code: b.code}, &old); err != nil {
		t.Fatalf("the answer through v0.2.0's fields: %v", err)
	}
	words := "2 Hubs answered (Home at " + a.addr + ", " + b.addr + ") — choose yours with --hub; a code is sent only to the Hub you choose. VaultSync sends a code only to the Hub you choose."
	if old.OK || old.Needs != "hub" || old.Refusal != words {
		t.Fatalf("what v0.2.0 reads: %+v", old)
	}

	// Nothing else carries the list: every other answer goes as it did.
	raw := map[string]any{}
	if err := client.Do(ctx, http.MethodPost, "/v1/pair", pairRequest{Hub: b.addr}, &raw); err != nil {
		t.Fatal(err)
	}
	if _, there := raw["hubs"]; there || raw["needs"] != "code" {
		t.Fatalf("an answer that asks for no Hub: %v", raw)
	}

	// The address chosen goes out as --hub does, to that Hub alone.
	res, err = client.Pair(ctx, pairRequest{Code: b.code, Hub: res.Hubs[1].Address})
	if err != nil || res.OK || res.Needs != "vault" || len(res.Hubs) != 0 {
		t.Fatalf("with the Hub chosen: %+v %v", res, err)
	}
	if a.starts != 0 || b.starts == 0 {
		t.Fatalf("the code went to: %d, %d", a.starts, b.starts)
	}
	// The agent still checks what it is sent, whatever a list offered.
	res, err = client.Pair(ctx, pairRequest{Code: b.code, Hub: "203.0.113.5:8390"})
	if err != nil || !strings.Contains(res.Refusal, "not on your local network") {
		t.Fatalf("a Hub off this network: %+v %v", res, err)
	}
}
