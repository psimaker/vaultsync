package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/psimaker/vaultsync/hub/pairing"
)

// #319: the text of an error answer comes from whatever answered at the
// address that was dialled, and `vaultsync-hub pair` prints it. Decision 062.

// hidden reports a character a terminal does not show as written: a control
// character, a format character, a line or paragraph separator.
func hidden(r rune) bool {
	return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
}

// `vaultsync-hub pair` ended with the answer's text as it came: a terminal
// sequence, a mark that reverses what follows and line breaks reached the
// terminal, and the text could be as long as an answer may be. The line now
// holds one clean, short line of it.
func TestIssue319_PairPrintsAnErrorAnswerAsOneCleanShortLine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(errorResponse{Error: "\x1b[2Jhub state\u2028unavailable\u202e\r\n$ " + strings.Repeat("я", 30000)})
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	_, _, err := handshakeChosenHub(context.Background(), []discoveredHub{{Address: addr}}, "TULIP-ANCHOR-42", nil)
	if err == nil {
		t.Fatal("an error answer paired")
	}
	line, lead := err.Error(), "hub at "+addr+" did not answer properly: hub pairing: HTTP 500: "
	if !strings.HasPrefix(line, lead+"[2Jhub state unavailable $ яя") || !strings.HasSuffix(line, "я…") {
		t.Errorf("the line: %+q", line)
	}
	if len(line) > len(lead)+300 || strings.ContainsFunc(line, hidden) {
		t.Errorf("the line is %d bytes behind its own words, or holds what a terminal does not show as written: %+q", len(line)-len(lead), line)
	}
}

// The client cleans and cuts an error answer's text. What the Hub words
// itself must come through that whole where a device goes by it: the
// refusals that are told apart by their words. Each of them is provoked on
// the real server and held against what the client hands out, so one that
// is reworded past the bound, or with something in it that cleaning drops,
// fails here. The server's other refusals are shown and nothing else; they
// are not held.
func TestIssue319_TheHubsOwnRefusalsArriveWhole(t *testing.T) {
	ctx := context.Background()
	const wrong = "TULIP-ANCHOR-00"
	handshake := func(h *countingHub, code string) error {
		_, err := h.client().Handshake(ctx, code)
		return err
	}
	cases := []struct {
		name    string
		kind    pairing.Kind
		refused func(h *countingHub) error
	}{
		{"an expired code", pairing.KindCodeExpired, func(h *countingHub) error {
			h.fx.now = h.fx.now.Add(pairingCodeTTL + time.Minute)
			return handshake(h, h.fx.code)
		}},
		{"a locked code", pairing.KindCodeLocked, func(h *countingHub) error {
			for range maxCodeFailures {
				_ = handshake(h, wrong)
			}
			return handshake(h, h.fx.code)
		}},
		{"no code", pairing.KindNoCode, func(h *countingHub) error {
			if _, err := h.fx.store.update(func(st *hubState) error { st.Pairing = nil; return nil }); err != nil {
				t.Fatal(err)
			}
			return handshake(h, wrong)
		}},
		{"too many tries", pairing.KindRateLimited, func(h *countingHub) error {
			for range pairingStartsPerMin {
				_ = handshake(h, wrong)
			}
			return handshake(h, wrong)
		}},
		{"a busy Hub", pairing.KindBusy, func(h *countingHub) error {
			h.fx.server.mu.Lock()
			for i := range pairingMaxSessions {
				h.fx.server.sessions[strconv.Itoa(i)] = &pairingSession{id: strconv.Itoa(i), expires: h.fx.now.Add(time.Hour)}
			}
			h.fx.server.mu.Unlock()
			return handshake(h, h.fx.code)
		}},
		{"a session the Hub no longer knows", pairing.KindSessionExpired, func(h *countingHub) error {
			c := h.client()
			if _, err := c.Handshake(ctx, h.fx.code); err != nil {
				t.Fatal(err)
			}
			h.fx.server.mu.Lock()
			clear(h.fx.server.sessions)
			h.fx.server.mu.Unlock()
			_, err := c.Provision(ctx, 1, fakeDeviceID, "Laptop", "", false)
			return err
		}},
		{"a request from outside the local network", pairing.KindNotLocal, func(h *countingHub) error {
			h.askedFrom("203.0.113.9:4444")
			return handshake(h, h.fx.code)
		}},
	}
	for _, c := range cases {
		h := newCountingHub(t)
		err := c.refused(h)
		var apiErr *pairing.APIError
		if !errors.As(err, &apiErr) || apiErr.Body != h.said() {
			t.Errorf("%s: the Hub said %q, the client hands out: %v", c.name, h.said(), err)
		}
		if k := pairing.FailureKind(err); k != c.kind {
			t.Errorf("%s: kind %q, want %q", c.name, k, c.kind)
		}
	}

	// A wrong code is told by the word "rejected" in the answer to finish.
	h := newCountingHub(t)
	if err := handshake(h, wrong); !errors.Is(err, pairing.ErrCodeRejected) || h.said() != "pairing code rejected" {
		t.Errorf("a wrong code: the Hub said %q, the client hands out: %v", h.said(), err)
	}
}

// Inside its encrypted answer the Hub passes on what its Syncthing said,
// behind its own words and cut at 200 bytes. The client's bound is chosen so
// that such a text arrives whole, however long the Syncthing's answer was: a
// longer cut on the Hub fails here.
func TestIssue319_ASyncthingAnswerBehindTheHubsWordsArrivesWhole(t *testing.T) {
	ctx := context.Background()
	for _, said := range []string{"config locked", strings.Repeat("x", 5000)} {
		h := newCountingHub(t)
		c := h.client()
		if _, err := c.Handshake(ctx, h.fx.code); err != nil {
			t.Fatal(err)
		}
		h.fx.fake.mu.Lock()
		h.fx.fake.failDeviceAdd, h.fx.fake.deviceAddError = true, said
		h.fx.fake.mu.Unlock()
		want := said
		if len(want) > 200 {
			want = want[:200] + "…"
		}
		reply, err := c.Provision(ctx, 1, fakeDeviceID, "Laptop", "", false)
		if err != nil || !strings.HasPrefix(reply.Error, pairing.RegistrationRefusedPrefix+": ") || !strings.HasSuffix(reply.Error, "HTTP 500: "+want) {
			t.Errorf("a refused registration, %d bytes: %q, %v", len(reply.Error), reply.Error, err)
		}
	}
}

// What the Hub quotes in a message of its own — the name it was asked for, a
// vault's label — arrives as the Hub wrote it. The first cleaning made every
// run of white space one space: asked for "My  Notes" on a Hub that has "My
// Notes", a device read that no vault is named "My Notes". Only white space
// that breaks a line becomes a space; the rest stays as it was sent.
func TestIssue319_WhatTheHubQuotesArrivesAsWritten(t *testing.T) {
	ctx := context.Background()
	h := newCountingHub(t)
	c := h.client()
	if _, err := c.Handshake(ctx, h.fx.code); err != nil {
		t.Fatal(err)
	}
	if reply, err := c.Provision(ctx, 1, fakeDeviceID, "Laptop", "My Notes", true); err != nil || reply.Error != "" {
		t.Fatalf("a new vault: %+v, %v", reply, err)
	}
	reply, err := c.Provision(ctx, 2, fakeDeviceID, "Laptop", "My  Notes", false)
	if err != nil || reply.Error != `no vault named "My  Notes" on this hub` {
		t.Errorf("a name with two spaces: %+q, %v", reply.Error, err)
	}

	// A label written with an ideographic space and a no-break space, on a
	// vault whose folder holds the one a new vault would get.
	const label = "Journal\u30002026\u00a0Notes"
	h.fx.fake.mu.Lock()
	h.fx.fake.folders = append(h.fx.fake.folders, folderConfig{ID: "vs-cccccccccccc", Label: label, Path: "/var/syncthing/vaults"})
	h.fx.fake.mu.Unlock()
	reply, err = c.Provision(ctx, 3, fakeDeviceID, "Laptop", "Recipes", true)
	if err != nil || reply.Error != "could not create the vault: path overlaps an existing vault ("+label+")" {
		t.Errorf("a label with wide spaces: %+q, %v", reply.Error, err)
	}
}
