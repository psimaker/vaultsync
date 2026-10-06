package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/psimaker/vaultsync/hub/join"
)

// countingHub is a real pairing server behind a request counter: every
// request the device sends to it is visible, and its state holds the wrong
// codes it counted.
type countingHub struct {
	fx       *pairingFixture
	requests atomic.Int64
	addr     string
}

func newCountingHub(t *testing.T) *countingHub {
	t.Helper()
	h := &countingHub{fx: newPairingFixture(t)}
	handler := h.fx.server.handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.requests.Add(1)
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	h.addr = strings.TrimPrefix(srv.URL, "http://")
	return h
}

func (h *countingHub) failures(t *testing.T) int {
	t.Helper()
	st, err := h.fx.store.load()
	if err != nil {
		t.Fatal(err)
	}
	return st.Pairing.Failures
}

// `vaultsync-hub pair` used to try the code on every Hub that answered
// discovery, so a second Hub on the network counted a wrong code — five lock
// it — for a code that was never meant for it (#204). The code now goes to
// exactly one Hub: the only answer, or the one the user picks.
func TestIssue204_CodeGoesOnlyToTheChosenHub(t *testing.T) {
	ctx := context.Background()

	t.Run("several Hubs and nobody to ask: no Hub hears the code", func(t *testing.T) {
		a, b := newCountingHub(t), newCountingHub(t)
		candidates := []discoveredHub{{Address: a.addr, Name: "Hub A"}, {Address: b.addr, Name: "Hub B"}}
		_, _, err := handshakeChosenHub(ctx, candidates, b.fx.code, nil)
		var several *join.SeveralHubsError
		if !errors.As(err, &several) || !strings.Contains(err.Error(), "--hub") {
			t.Fatalf("expected a refusal naming --hub, got %v", err)
		}
		if a.requests.Load() != 0 || b.requests.Load() != 0 {
			t.Fatalf("a Hub heard the code without being chosen: A=%d B=%d requests", a.requests.Load(), b.requests.Load())
		}
	})

	t.Run("a chooser without a terminal never picks", func(t *testing.T) {
		a, b := newCountingHub(t), newCountingHub(t)
		candidates := []discoveredHub{{Address: a.addr}, {Address: b.addr}}
		noTerminal := func(hubs []discoveredHub) (int, error) { return 0, &join.SeveralHubsError{Hubs: hubs} }
		if _, _, err := handshakeChosenHub(ctx, candidates, a.fx.code, noTerminal); err == nil {
			t.Fatal("expected a refusal")
		}
		if a.requests.Load() != 0 || b.requests.Load() != 0 {
			t.Fatal("a Hub heard the code without being chosen")
		}
	})

	t.Run("the chosen Hub pairs; the other hears nothing", func(t *testing.T) {
		a, b := newCountingHub(t), newCountingHub(t)
		candidates := []discoveredHub{{Address: a.addr}, {Address: b.addr}}
		chooseB := func([]discoveredHub) (int, error) { return 1, nil }
		client, hello, err := handshakeChosenHub(ctx, candidates, b.fx.code, chooseB)
		if err != nil || client == nil || hello.HubDeviceID != fakeHubID {
			t.Fatalf("pairing with the chosen Hub: %v %+v", err, hello)
		}
		if a.requests.Load() != 0 {
			t.Fatalf("the Hub that was not chosen got %d requests", a.requests.Load())
		}
	})

	t.Run("a wrong pick costs one try on that Hub and never reaches the other", func(t *testing.T) {
		a, b := newCountingHub(t), newCountingHub(t)
		candidates := []discoveredHub{{Address: a.addr}, {Address: b.addr}}
		chooseA := func([]discoveredHub) (int, error) { return 0, nil }
		_, _, err := handshakeChosenHub(ctx, candidates, b.fx.code, chooseA)
		if err == nil || !strings.Contains(err.Error(), "did not accept this code") {
			t.Fatalf("expected the chosen Hub to reject the code, got %v", err)
		}
		if a.failures(t) != 1 {
			t.Fatalf("the chosen Hub counted %d wrong codes, want 1", a.failures(t))
		}
		if b.requests.Load() != 0 || b.failures(t) != 0 {
			t.Fatalf("the code was tried on the other Hub too: %d requests, %d failures", b.requests.Load(), b.failures(t))
		}
	})
}

// A vault accepted on a device gets the settings the Hub gives its own
// folders; join keeps its own copy of the values for the device side.
func TestDeviceFolderSettingsMatchHubDefaults(t *testing.T) {
	if join.FolderRescanIntervalS != hubRescanIntervalS || join.FolderFSWatcherDelayS != hubFSWatcherDelayS || join.FolderMaxConflicts != hubMaxConflicts {
		t.Fatalf("join folder settings (%d, %d, %d) drifted from the Hub defaults (%d, %d, %d)",
			join.FolderRescanIntervalS, join.FolderFSWatcherDelayS, join.FolderMaxConflicts,
			hubRescanIntervalS, hubFSWatcherDelayS, hubMaxConflicts)
	}
}
