package pairing

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A link-supplied address is checked again at the socket: a Hub address
// outside the local network never sees a single packet (#174).
func TestIssue174_LocalClientRefusesNonLocalPeersAtDial(t *testing.T) {
	start := time.Now()
	_, err := NewLocalClient("192.0.2.1:8390").Handshake(context.Background(), "TULIP-ANCHOR-42")
	if !errors.Is(err, ErrNotLocal) || FailureKind(err) != KindNotLocal {
		t.Fatalf("non-local dial: %v (kind %q)", err, FailureKind(err))
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("refusal waited for the network instead of failing at the socket")
	}
}

// The Hub never redirects; a client that followed one would carry the
// handshake to wherever an answer points.
func TestIssue174_ClientRefusesRedirects(t *testing.T) {
	var elsewhere atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	defer target.Close()
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer hub.Close()
	for _, c := range []*Client{
		NewClient(strings.TrimPrefix(hub.URL, "http://")),
		NewLocalClient(strings.TrimPrefix(hub.URL, "http://")),
	} {
		_, err := c.Handshake(context.Background(), "TULIP-ANCHOR-42")
		if err == nil || !strings.Contains(err.Error(), "redirect") {
			t.Fatalf("redirect followed or wrong error: %v", err)
		}
	}
	if elsewhere.Load() != 0 {
		t.Fatal("a redirect reached its target")
	}
}

func TestFailureKindOfUnknownAndNil(t *testing.T) {
	if FailureKind(nil) != KindNone {
		t.Fatal("nil error has a kind")
	}
	if FailureKind(errors.New("boom")) != KindOther {
		t.Fatal("plain error is not other")
	}
	if FailureKind(&APIError{Status: http.StatusTooManyRequests, Body: "too many pairing attempts from this address — wait a minute"}) != KindRateLimited {
		t.Fatal("429 is not rate limited")
	}
	if FailureKind(&APIError{Status: http.StatusInternalServerError, Body: "hub state unavailable"}) != KindOther {
		t.Fatal("500 is not other")
	}
}
