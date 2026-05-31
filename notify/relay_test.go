package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// triggerStub is a relay test double that returns a fixed status for
// POST /api/v1/trigger and counts how many times it was hit.
type triggerStub struct {
	server *httptest.Server
	calls  atomic.Int32
}

func newTriggerStub(t *testing.T, status int, retryAfter, body string) *triggerStub {
	t.Helper()
	stub := &triggerStub{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/trigger":
			stub.calls.Add(1)
			if retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
			w.WriteHeader(status)
			if body != "" {
				_, _ = w.Write([]byte(body))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *triggerStub) client() *RelayClient { return NewRelayClient(s.server.URL, "DEVICE-TEST") }

func (s *triggerStub) triggerURL() string { return s.server.URL + "/api/v1/trigger" }

// doTrigger is the single source of truth for status-code classification, so we
// assert each branch directly — no retry/backoff timing involved.
func TestDoTriggerClassifiesStatusCodes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		status int
		assert func(t *testing.T, err error)
	}{
		{"202 accepted", http.StatusAccepted, func(t *testing.T, err error) {
			if err != nil {
				t.Fatalf("202 should succeed, got %v", err)
			}
		}},
		{"400 bad request -> subscription inactive", http.StatusBadRequest, assertSubscriptionInactive},
		{"401 unauthorized -> subscription inactive", http.StatusUnauthorized, assertSubscriptionInactive},
		{"402 payment required -> subscription inactive", http.StatusPaymentRequired, assertSubscriptionInactive},
		{"403 forbidden -> subscription inactive", http.StatusForbidden, assertSubscriptionInactive},
		{"405 method not allowed -> transient", http.StatusMethodNotAllowed, assertTransient},
		{"409 conflict -> transient", http.StatusConflict, assertTransient},
		{"404 not found -> fatal", http.StatusNotFound, func(t *testing.T, err error) {
			if !isFatal(err) {
				t.Fatalf("404 should be fatal (wrong RELAY_URL), got %T: %v", err, err)
			}
			if isSubscriptionInactive(err) {
				t.Fatal("404 must not be classified as a subscription state")
			}
		}},
		{"429 too many requests -> rate limit", http.StatusTooManyRequests, func(t *testing.T, err error) {
			if isFatal(err) || isSubscriptionInactive(err) {
				t.Fatalf("429 should be a transient rate limit, got %T", err)
			}
			if _, ok := retryAfter(err); !ok {
				t.Fatalf("429 should carry a retry-after duration, got %T: %v", err, err)
			}
		}},
		{"500 server error -> transient", http.StatusInternalServerError, assertTransient},
		{"503 unavailable -> transient", http.StatusServiceUnavailable, assertTransient},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stub := newTriggerStub(t, tc.status, "", "")
			err := stub.client().doTrigger(context.Background(), stub.triggerURL(), []byte(`{"device_id":"DEVICE-TEST"}`))
			tc.assert(t, err)
		})
	}
}

func assertSubscriptionInactive(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a subscription-inactive error, got nil")
	}
	if !isSubscriptionInactive(err) {
		t.Fatalf("expected subscription-inactive classification, got %T: %v", err, err)
	}
	if isFatal(err) {
		t.Fatal("a subscription-inactive response must never be fatal")
	}
}

func assertTransient(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a transient error, got nil")
	}
	if isFatal(err) || isSubscriptionInactive(err) {
		t.Fatalf("5xx should be transient (retryable), got %T: %v", err, err)
	}
}

// A subscription-inactive verdict is stable: Trigger must surface it after a
// single request instead of burning the full retry/backoff budget.
func TestTriggerDoesNotRetrySubscriptionInactive(t *testing.T) {
	t.Parallel()

	stub := newTriggerStub(t, http.StatusBadRequest, "", `{"error":"subscription expired"}`)
	err := stub.client().Trigger(context.Background())

	if !isSubscriptionInactive(err) {
		t.Fatalf("expected subscription-inactive error, got %T: %v", err, err)
	}
	if got := stub.calls.Load(); got != 1 {
		t.Fatalf("Trigger made %d requests for a stable subscription verdict, want 1", got)
	}
}

func TestTrigger404ReturnsFatalImmediately(t *testing.T) {
	t.Parallel()

	stub := newTriggerStub(t, http.StatusNotFound, "", "")
	err := stub.client().Trigger(context.Background())

	if !isFatal(err) {
		t.Fatalf("expected fatal error for 404, got %T: %v", err, err)
	}
	if got := stub.calls.Load(); got != 1 {
		t.Fatalf("Trigger made %d requests for a fatal 404, want 1", got)
	}
}

// ProbeTrigger backs the doctor's connectivity diagnostic. A subscription-state
// response proves the endpoint is reachable, so the probe must pass — otherwise
// the doctor would falsely report a config failure for an unprovisioned device.
func TestProbeTriggerTreatsSubscriptionInactiveAsReachable(t *testing.T) {
	t.Parallel()

	stub := newTriggerStub(t, http.StatusBadRequest, "", "")
	if err := stub.client().ProbeTrigger(context.Background()); err != nil {
		t.Fatalf("ProbeTrigger should treat an inactive subscription as reachable, got %v", err)
	}
}

func TestProbeTrigger404Fails(t *testing.T) {
	t.Parallel()

	stub := newTriggerStub(t, http.StatusNotFound, "", "")
	err := stub.client().ProbeTrigger(context.Background())
	if err == nil || !isFatal(err) {
		t.Fatalf("ProbeTrigger should fail fatally for 404, got %T: %v", err, err)
	}
}

// fireTrigger is the run loop's decision point: it must never return
// outcomeFatal for a subscription-state response.
func TestFireTriggerClassifiesOutcomes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		status int
		want   triggerOutcome
	}{
		{"accepted", http.StatusAccepted, outcomeDelivered},
		{"bad request", http.StatusBadRequest, outcomeSubscriptionInactive},
		{"forbidden", http.StatusForbidden, outcomeSubscriptionInactive},
		{"payment required", http.StatusPaymentRequired, outcomeSubscriptionInactive},
		{"not found", http.StatusNotFound, outcomeFatal},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stub := newTriggerStub(t, tc.status, "", "")
			if got := fireTrigger(context.Background(), stub.client(), "test"); got != tc.want {
				t.Fatalf("fireTrigger for %d = %d, want %d", tc.status, got, tc.want)
			}
		})
	}
}

// A transient relay failure (5xx) must classify as outcomeRetry, not fatal — a
// relay outage must never bring the sidecar down. A short deadline keeps the
// test fast instead of paying the real retry backoff.
func TestFireTriggerTransientReturnsRetry(t *testing.T) {
	t.Parallel()

	stub := newTriggerStub(t, http.StatusInternalServerError, "", "")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if got := fireTrigger(ctx, stub.client(), "test"); got != outcomeRetry {
		t.Fatalf("fireTrigger for a 5xx outage = %d, want outcomeRetry (%d)", got, outcomeRetry)
	}
}

// Trigger must actually retry a transient failure before giving up — the retry
// loop, not just the short-circuit branches, has to work.
func TestTriggerRetriesTransientThenSucceeds(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"accepted","devices_notified":1}`))
	}))
	defer server.Close()

	if err := NewRelayClient(server.URL, "DEVICE-TEST").Trigger(context.Background()); err != nil {
		t.Fatalf("expected success after one transient failure, got %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("Trigger made %d attempts, want 2 (one transient failure then success)", got)
	}
}
