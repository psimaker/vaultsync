package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunCheckWithRetrySucceedsAfterTransientFailures(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	err := runCheckWithRetry(context.Background(), retryPolicy{
		Attempts:       4,
		AttemptTimeout: 200 * time.Millisecond,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     2 * time.Millisecond,
	}, "transient-check", func(ctx context.Context) error {
		if attempts.Add(1) < 3 {
			return errors.New("temporary network error")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected success after retries, got error: %v", err)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("expected 3 attempts, got %d", got)
	}
}

func TestValidateAPIKeyClassifiesForbiddenAsFatal(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/system/status" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer server.Close()

	client := NewSyncthingClient(server.URL, "bad-key")
	err := client.ValidateAPIKey(context.Background())
	if err == nil {
		t.Fatal("expected API key validation error")
	}

	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("expected HTTPStatusError, got %T", err)
	}
	if statusErr.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", statusErr.StatusCode)
	}
	if !isFatalSyncthingError(err) {
		t.Fatal("expected forbidden error to be treated as fatal syncthing config error")
	}
}

func TestRelayHealthAndTriggerProbe(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/trigger":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method for trigger: %s", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"status":"accepted","devices_notified":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	relay := NewRelayClient(server.URL, "DEVICE123")
	if err := relay.CheckHealth(context.Background()); err != nil {
		t.Fatalf("expected relay health check to pass, got: %v", err)
	}
	if err := relay.ProbeTrigger(context.Background()); err != nil {
		t.Fatalf("expected trigger probe to pass, got: %v", err)
	}
}

func TestRunCheckWithRetryReturnsLastError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("still failing")
	err := runCheckWithRetry(context.Background(), retryPolicy{
		Attempts:       2,
		AttemptTimeout: 200 * time.Millisecond,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     2 * time.Millisecond,
	}, "always-fails", func(ctx context.Context) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected last error %v, got %v", wantErr, err)
	}
}
