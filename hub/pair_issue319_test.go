package main

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
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

// recordedHub is the real pairing server behind a listener that keeps the
// text of the last refusal it sent.
type recordedHub struct {
	fx   *pairingFixture
	addr string
	mu   sync.Mutex
	last string
}

func newRecordedHub(t *testing.T) *recordedHub {
	t.Helper()
	h := &recordedHub{fx: newPairingFixture(t)}
	handler := h.fx.server.handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK {
			var e errorResponse
			_ = json.Unmarshal(rec.Body.Bytes(), &e)
			h.mu.Lock()
			h.last = e.Error
			h.mu.Unlock()
		}
		maps.Copy(w.Header(), rec.Header())
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	}))
	t.Cleanup(srv.Close)
	h.addr = strings.TrimPrefix(srv.URL, "http://")
	return h
}

func (h *recordedHub) client() *pairing.Client { return pairing.NewClient(h.addr) }

func (h *recordedHub) said() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.last
}

// The client cleans and cuts an error answer's text. What the Hub words
// itself must come through that whole: a device shows it, and tells one
// refusal from another by its words. Every refusal the server can be brought
// to send is held against what the client hands out.
func TestIssue319_TheHubsOwnRefusalsArriveWhole(t *testing.T) {
	ctx := context.Background()
	const wrong = "TULIP-ANCHOR-00"
	handshake := func(h *recordedHub, code string) error {
		_, err := h.client().Handshake(ctx, code)
		return err
	}
	cases := []struct {
		name    string
		kind    pairing.Kind
		refused func(h *recordedHub) error
	}{
		{"an expired code", pairing.KindCodeExpired, func(h *recordedHub) error {
			h.fx.now = h.fx.now.Add(pairingCodeTTL + time.Minute)
			return handshake(h, h.fx.code)
		}},
		{"a locked code", pairing.KindCodeLocked, func(h *recordedHub) error {
			for range maxCodeFailures {
				_ = handshake(h, wrong)
			}
			return handshake(h, h.fx.code)
		}},
		{"no code", pairing.KindNoCode, func(h *recordedHub) error {
			if _, err := h.fx.store.update(func(st *hubState) error { st.Pairing = nil; return nil }); err != nil {
				t.Fatal(err)
			}
			return handshake(h, wrong)
		}},
		{"too many tries", pairing.KindRateLimited, func(h *recordedHub) error {
			for range pairingStartsPerMin {
				_ = handshake(h, wrong)
			}
			return handshake(h, wrong)
		}},
		{"a busy Hub", pairing.KindBusy, func(h *recordedHub) error {
			h.fx.server.mu.Lock()
			for i := range pairingMaxSessions {
				h.fx.server.sessions[strconv.Itoa(i)] = &pairingSession{id: strconv.Itoa(i), expires: h.fx.now.Add(time.Hour)}
			}
			h.fx.server.mu.Unlock()
			return handshake(h, h.fx.code)
		}},
		{"a session the Hub no longer knows", pairing.KindSessionExpired, func(h *recordedHub) error {
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
	}
	for _, c := range cases {
		h := newRecordedHub(t)
		err := c.refused(h)
		said := h.said()
		var apiErr *pairing.APIError
		if !errors.As(err, &apiErr) || said == "" || apiErr.Body != said {
			t.Errorf("%s: the Hub said %q, the client hands out: %v", c.name, said, err)
		}
		if k := pairing.FailureKind(err); k != c.kind {
			t.Errorf("%s: kind %q, want %q", c.name, k, c.kind)
		}
	}

	// A wrong code is told by the word "rejected" in the answer to finish.
	h := newRecordedHub(t)
	if err := handshake(h, wrong); !errors.Is(err, pairing.ErrCodeRejected) || h.said() != "pairing code rejected" {
		t.Errorf("a wrong code: the Hub said %q, the client hands out: %v", h.said(), err)
	}

	// Inside its encrypted answer the Hub passes on what its Syncthing said.
	h = newRecordedHub(t)
	c := h.client()
	if _, err := c.Handshake(ctx, h.fx.code); err != nil {
		t.Fatal(err)
	}
	h.fx.fake.mu.Lock()
	h.fx.fake.failDeviceAdd = true
	h.fx.fake.mu.Unlock()
	reply, err := c.Provision(ctx, 1, fakeDeviceID, "Laptop", "", false)
	if err != nil || !strings.HasPrefix(reply.Error, pairing.RegistrationRefusedPrefix+": ") || !strings.HasSuffix(reply.Error, "HTTP 500: config locked") {
		t.Errorf("a refused registration: %q, %v", reply.Error, err)
	}
}
