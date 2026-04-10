package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoadConfigRequiresBootstrapEnvironment(t *testing.T) {
	t.Setenv("SYNCTHING_API_URL", "")
	t.Setenv("SYNCTHING_API_KEY", "")
	t.Setenv("RELAY_URL", "")
	t.Setenv("DEBOUNCE_SECONDS", "")
	t.Setenv("WATCHED_FOLDERS", "")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected configuration error when required env vars are missing")
	}

	msg := err.Error()
	for _, required := range []string{"SYNCTHING_API_URL", "SYNCTHING_API_KEY", "RELAY_URL"} {
		if !strings.Contains(msg, required) {
			t.Fatalf("error %q should mention missing %s", msg, required)
		}
	}
}

func TestLoadConfigParsesBootstrapValues(t *testing.T) {
	t.Setenv("SYNCTHING_API_URL", "http://localhost:8384")
	t.Setenv("SYNCTHING_API_KEY", "test-key")
	t.Setenv("RELAY_URL", "https://relay.example.com")
	t.Setenv("DEBOUNCE_SECONDS", "7")
	t.Setenv("WATCHED_FOLDERS", " vault-b, vault-a ,,vault-a ")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig returned unexpected error: %v", err)
	}

	if cfg.DebounceSeconds != 7 {
		t.Fatalf("DebounceSeconds = %d, want 7", cfg.DebounceSeconds)
	}
	if cfg.WatchedFolders == nil {
		t.Fatal("WatchedFolders should not be nil when WATCHED_FOLDERS is set")
	}
	if !cfg.WatchedFolders["vault-a"] || !cfg.WatchedFolders["vault-b"] {
		t.Fatalf("watched folders parsed incorrectly: %+v", cfg.WatchedFolders)
	}
	if len(cfg.WatchedFolders) != 2 {
		t.Fatalf("expected duplicate/empty watched IDs to be normalized, got %+v", cfg.WatchedFolders)
	}
}

func TestLoadConfigRejectsInvalidDebounceSeconds(t *testing.T) {
	t.Setenv("SYNCTHING_API_URL", "http://localhost:8384")
	t.Setenv("SYNCTHING_API_KEY", "test-key")
	t.Setenv("RELAY_URL", "https://relay.example.com")
	t.Setenv("DEBOUNCE_SECONDS", "0")
	t.Setenv("WATCHED_FOLDERS", "")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected DEBOUNCE_SECONDS validation error")
	}
	if !strings.Contains(err.Error(), "DEBOUNCE_SECONDS") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestRunHealthcheckSkipsTriggerProbe(t *testing.T) {
	var relayTriggerCalls atomic.Int32

	syncthing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/system/status" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"myID":"DEVICE-123"}`))
	}))
	defer syncthing.Close()

	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/trigger":
			relayTriggerCalls.Add(1)
			http.Error(w, "trigger should not run in healthcheck mode", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer relay.Close()

	cfg := Config{
		SyncthingAPIURL: syncthing.URL,
		SyncthingAPIKey: "test-key",
		RelayURL:        relay.URL,
		DebounceSeconds: 5,
	}

	if err := runHealthcheck(context.Background(), cfg); err != nil {
		t.Fatalf("runHealthcheck returned unexpected error: %v", err)
	}
	if got := relayTriggerCalls.Load(); got != 0 {
		t.Fatalf("relay trigger was called %d times in healthcheck mode, want 0", got)
	}
}

func TestRunPreflightDoctorModeIncludesTriggerProbe(t *testing.T) {
	var relayTriggerCalls atomic.Int32

	syncthing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/system/status" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"myID":"DEVICE-456"}`))
	}))
	defer syncthing.Close()

	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/trigger":
			relayTriggerCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"status":"accepted","devices_notified":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer relay.Close()

	cfg := Config{
		SyncthingAPIURL: syncthing.URL,
		SyncthingAPIKey: "test-key",
		RelayURL:        relay.URL,
		DebounceSeconds: 5,
	}
	mode := preflightMode{
		Name:           "doctor",
		IncludeTrigger: true,
		PrintSuccess:   false,
		Retry: retryPolicy{
			Attempts:       1,
			AttemptTimeout: 500 * time.Millisecond,
			InitialBackoff: time.Millisecond,
			MaxBackoff:     2 * time.Millisecond,
		},
	}

	if err := runPreflight(context.Background(), cfg, mode); err != nil {
		t.Fatalf("runPreflight doctor mode returned unexpected error: %v", err)
	}
	if got := relayTriggerCalls.Load(); got != 1 {
		t.Fatalf("relay trigger probe calls = %d, want 1", got)
	}
}
