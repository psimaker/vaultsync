package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	t.Setenv("POKE_INTERVAL_MINUTES", "15")
	t.Setenv("WATCHED_FOLDERS", " vault-b, vault-a ,,vault-a ")
	t.Setenv("UPLOAD_LISTEN_ADDR", ":8081")
	t.Setenv("UPLOAD_ROOT_DIR", "/tmp/vaultsync-upload")
	t.Setenv("UPLOAD_AUTH_TOKEN", "secret")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig returned unexpected error: %v", err)
	}

	if cfg.DebounceSeconds != 7 {
		t.Fatalf("DebounceSeconds = %d, want 7", cfg.DebounceSeconds)
	}
	if cfg.PokeIntervalMin != 15 {
		t.Fatalf("PokeIntervalMin = %d, want 15", cfg.PokeIntervalMin)
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
	if cfg.UploadListenAddr != ":8081" || cfg.UploadRootDir != "/tmp/vaultsync-upload" || cfg.UploadAuthToken != "secret" {
		t.Fatalf("upload config parsed incorrectly: %+v", cfg)
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

func TestLoadConfigRejectsInvalidPokeIntervalMinutes(t *testing.T) {
	t.Setenv("SYNCTHING_API_URL", "http://localhost:8384")
	t.Setenv("SYNCTHING_API_KEY", "test-key")
	t.Setenv("RELAY_URL", "https://relay.example.com")
	t.Setenv("DEBOUNCE_SECONDS", "5")
	t.Setenv("POKE_INTERVAL_MINUTES", "-1")
	t.Setenv("WATCHED_FOLDERS", "")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected POKE_INTERVAL_MINUTES validation error")
	}
	if !strings.Contains(err.Error(), "POKE_INTERVAL_MINUTES") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestLoadConfigRequiresCompleteUploadEndpointConfiguration(t *testing.T) {
	t.Setenv("SYNCTHING_API_URL", "http://localhost:8384")
	t.Setenv("SYNCTHING_API_KEY", "test-key")
	t.Setenv("RELAY_URL", "https://relay.example.com")
	t.Setenv("UPLOAD_LISTEN_ADDR", ":8090")
	t.Setenv("UPLOAD_ROOT_DIR", "")
	t.Setenv("UPLOAD_AUTH_TOKEN", "")

	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "UPLOAD_LISTEN_ADDR") {
		t.Fatalf("expected upload endpoint config error, got %v", err)
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

func TestTriggerCandidateForEventLocalIndexUpdated(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(map[string]any{
		"folder":   "vault-a",
		"sequence": 42,
	})
	if err != nil {
		t.Fatalf("marshal local index payload: %v", err)
	}

	candidate, ok := triggerCandidateForEvent(Event{
		Type: "LocalIndexUpdated",
		Data: raw,
	}, nil)
	if !ok {
		t.Fatal("expected LocalIndexUpdated to produce a trigger candidate")
	}
	if candidate.Folder != "vault-a" {
		t.Fatalf("folder = %q, want vault-a", candidate.Folder)
	}
	if candidate.Marker != "local-index:42" {
		t.Fatalf("marker = %q, want local-index:42", candidate.Marker)
	}
}

func TestTriggerCandidateForEventFolderCompletionNeedsOutstandingWork(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(map[string]any{
		"folder":    "vault-a",
		"device":    "PEER-123",
		"sequence":  77,
		"needItems": 1,
		"needBytes": 73,
	})
	if err != nil {
		t.Fatalf("marshal folder completion payload: %v", err)
	}

	candidate, ok := triggerCandidateForEvent(Event{
		Type: "FolderCompletion",
		Data: raw,
	}, nil)
	if !ok {
		t.Fatal("expected FolderCompletion with outstanding work to trigger")
	}
	want := "folder-completion:PEER-123:77:1:73"
	if candidate.Marker != want {
		t.Fatalf("marker = %q, want %q", candidate.Marker, want)
	}
}

func TestTriggerCandidateForEventIgnoresSettledFolderCompletion(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(map[string]any{
		"folder":    "vault-a",
		"device":    "PEER-123",
		"sequence":  77,
		"needItems": 0,
		"needBytes": 0,
	})
	if err != nil {
		t.Fatalf("marshal folder completion payload: %v", err)
	}

	if _, ok := triggerCandidateForEvent(Event{
		Type: "FolderCompletion",
		Data: raw,
	}, nil); ok {
		t.Fatal("expected settled FolderCompletion to be ignored")
	}
}

func TestHandleUploadStoresMarkdownUnderRoot(t *testing.T) {
	root := t.TempDir()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/upload?path=brain/notes/test.md", strings.NewReader("# hello"))
	req.Header.Set("Authorization", "Bearer token-123")
	req.Header.Set("X-VaultSync-Device-ID", "DEVICE-123")
	rec := httptest.NewRecorder()

	handleUpload(rec, req, root, "token-123")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	content, err := os.ReadFile(filepath.Join(root, "brain", "notes", "test.md"))
	if err != nil {
		t.Fatalf("read uploaded file: %v", err)
	}
	if string(content) != "# hello" {
		t.Fatalf("stored content = %q", string(content))
	}
}

func TestHandleUploadRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/upload?path=../../evil.md", strings.NewReader("bad"))
	req.Header.Set("Authorization", "Bearer token-123")
	rec := httptest.NewRecorder()

	handleUpload(rec, req, root, "token-123")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleUploadRequiresAuthorization(t *testing.T) {
	root := t.TempDir()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/upload?path=brain/test.md", strings.NewReader("hello"))
	rec := httptest.NewRecorder()

	handleUpload(rec, req, root, "token-123")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestTriggerCandidateForEventHonorsWatchedFolders(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(map[string]any{
		"folder":   "vault-b",
		"sequence": 42,
	})
	if err != nil {
		t.Fatalf("marshal local index payload: %v", err)
	}

	if _, ok := triggerCandidateForEvent(Event{
		Type: "LocalIndexUpdated",
		Data: raw,
	}, map[string]bool{"vault-a": true}); ok {
		t.Fatal("expected unwatched folder to be ignored")
	}
}

func TestMarkTriggeredCopiesPendingMarkers(t *testing.T) {
	t.Parallel()

	lastTriggered := map[string]string{
		"vault-a": "local-index:1",
	}
	pending := map[string]string{
		"vault-a": "local-index:2",
		"vault-b": "folder-completion:PEER:3:1:7",
	}

	markTriggered(lastTriggered, pending)

	if got := lastTriggered["vault-a"]; got != "local-index:2" {
		t.Fatalf("vault-a marker = %q, want local-index:2", got)
	}
	if got := lastTriggered["vault-b"]; got != "folder-completion:PEER:3:1:7" {
		t.Fatalf("vault-b marker = %q, want updated marker", got)
	}
}

func TestShouldSendPeriodicPoke(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 12, 12, 0, 0, 0, time.UTC)
	interval := 15 * time.Minute

	if !shouldSendPeriodicPoke(now, interval, time.Time{}, 0) {
		t.Fatal("expected zero last-delivery time to allow periodic poke")
	}
	if shouldSendPeriodicPoke(now, interval, now.Add(-5*time.Minute), 0) {
		t.Fatal("expected recent successful trigger to suppress periodic poke")
	}
	if shouldSendPeriodicPoke(now, interval, now.Add(-20*time.Minute), 1) {
		t.Fatal("expected pending change-trigger work to suppress periodic poke")
	}
	if !shouldSendPeriodicPoke(now, interval, now.Add(-20*time.Minute), 0) {
		t.Fatal("expected stale last trigger with no pending work to allow periodic poke")
	}
}
