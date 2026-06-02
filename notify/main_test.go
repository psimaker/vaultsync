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
	t.Setenv("SYNCTHING_CONFIG", "")

	// Disable config.xml auto-detection so the test is hermetic (it must not pick
	// up a real Syncthing config on the developer's machine).
	restore := syncthingConfigCandidatesFn
	syncthingConfigCandidatesFn = func() []string { return nil }
	t.Cleanup(func() { syncthingConfigCandidatesFn = restore })

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected configuration error when nothing is set and config is not auto-detectable")
	}

	// RELAY_URL stays required (no production default), alongside the Syncthing
	// values when auto-detection finds nothing.
	msg := err.Error()
	for _, required := range []string{"SYNCTHING_API_URL", "SYNCTHING_API_KEY", "RELAY_URL"} {
		if !strings.Contains(msg, required) {
			t.Fatalf("error %q should mention missing %s", msg, required)
		}
	}
}

func TestLoadConfigRelayURLIsRequired(t *testing.T) {
	// Syncthing values are set explicitly (so auto-detection never runs), but
	// RELAY_URL is omitted — loadConfig must refuse rather than default to prod.
	t.Setenv("SYNCTHING_API_URL", "http://localhost:8384")
	t.Setenv("SYNCTHING_API_KEY", "test-key")
	t.Setenv("RELAY_URL", "")
	t.Setenv("DEBOUNCE_SECONDS", "")
	t.Setenv("WATCHED_FOLDERS", "")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected an error: RELAY_URL must be required (no production default)")
	}
	if !strings.Contains(err.Error(), "RELAY_URL") {
		t.Fatalf("error %q should mention the missing RELAY_URL", err)
	}
}

func TestLoadConfigAutoDetectsSyncthingFromConfigXML(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.xml")
	if err := os.WriteFile(cfgPath, []byte(deviceBeforeGUIConfigXML), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	// Only RELAY_URL is set explicitly; the Syncthing values must come from the
	// config.xml, with no manual key paste.
	t.Setenv("SYNCTHING_API_URL", "")
	t.Setenv("SYNCTHING_API_KEY", "")
	t.Setenv("RELAY_URL", "https://relay.example.com")
	t.Setenv("DEBOUNCE_SECONDS", "")
	t.Setenv("WATCHED_FOLDERS", "")

	restore := syncthingConfigCandidatesFn
	syncthingConfigCandidatesFn = func() []string { return []string{cfgPath} }
	t.Cleanup(func() { syncthingConfigCandidatesFn = restore })

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig returned unexpected error: %v", err)
	}
	if cfg.SyncthingAPIKey != "fixture-api-key-12345" {
		t.Fatalf("SyncthingAPIKey = %q, want the key from config.xml", cfg.SyncthingAPIKey)
	}
	if cfg.SyncthingAPIURL != "http://127.0.0.1:8384" {
		t.Fatalf("SyncthingAPIURL = %q, want http://127.0.0.1:8384 (from <gui><address>, not a device address)", cfg.SyncthingAPIURL)
	}
}

func TestLoadConfigExplicitEnvWinsOverConfigXML(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.xml")
	if err := os.WriteFile(cfgPath, []byte(deviceBeforeGUIConfigXML), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	// A separate-container deployment sets the URL by service name explicitly;
	// only the key should be auto-detected from the shared config volume.
	t.Setenv("SYNCTHING_API_URL", "http://syncthing:8384")
	t.Setenv("SYNCTHING_API_KEY", "")
	t.Setenv("RELAY_URL", "https://relay.example.com")
	t.Setenv("DEBOUNCE_SECONDS", "")
	t.Setenv("WATCHED_FOLDERS", "")

	restore := syncthingConfigCandidatesFn
	syncthingConfigCandidatesFn = func() []string { return []string{cfgPath} }
	t.Cleanup(func() { syncthingConfigCandidatesFn = restore })

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig returned unexpected error: %v", err)
	}
	if cfg.SyncthingAPIURL != "http://syncthing:8384" {
		t.Fatalf("explicit SYNCTHING_API_URL must win, got %q", cfg.SyncthingAPIURL)
	}
	if cfg.SyncthingAPIKey != "fixture-api-key-12345" {
		t.Fatalf("SyncthingAPIKey = %q, want the auto-detected key", cfg.SyncthingAPIKey)
	}
}

func TestLoadConfigAwaitingSyncthingWaitsForConfigXML(t *testing.T) {
	// First-boot race: the helper starts before Syncthing has written config.xml.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.xml") // does NOT exist yet

	t.Setenv("SYNCTHING_API_URL", "")
	t.Setenv("SYNCTHING_API_KEY", "")
	t.Setenv("RELAY_URL", "https://relay.example.com")
	t.Setenv("DEBOUNCE_SECONDS", "")
	t.Setenv("WATCHED_FOLDERS", "")
	t.Setenv("SYNCTHING_CONFIG_WAIT_SECONDS", "5")

	restore := syncthingConfigCandidatesFn
	syncthingConfigCandidatesFn = func() []string { return []string{cfgPath} }
	t.Cleanup(func() { syncthingConfigCandidatesFn = restore })

	restorePoll := configWaitPollInterval
	configWaitPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { configWaitPollInterval = restorePoll })

	// Syncthing writes config.xml shortly after the helper starts.
	go func() {
		time.Sleep(60 * time.Millisecond)
		_ = os.WriteFile(cfgPath, []byte(deviceBeforeGUIConfigXML), 0o600)
	}()

	cfg, err := loadConfigAwaitingSyncthing(context.Background())
	if err != nil {
		t.Fatalf("loadConfigAwaitingSyncthing should ride out the first-boot race, got: %v", err)
	}
	if cfg.SyncthingAPIKey != "fixture-api-key-12345" {
		t.Fatalf("SyncthingAPIKey = %q, want the key from the config.xml that appeared", cfg.SyncthingAPIKey)
	}
}

func TestLoadConfigAwaitingSyncthingFailsFastWithoutRelayURL(t *testing.T) {
	// A missing RELAY_URL is a real misconfiguration, not a first-boot race: it
	// must fail immediately even with a wait budget set, never burn the budget,
	// and never silently default to production.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.xml") // never created

	t.Setenv("SYNCTHING_API_URL", "")
	t.Setenv("SYNCTHING_API_KEY", "")
	t.Setenv("RELAY_URL", "")
	t.Setenv("DEBOUNCE_SECONDS", "")
	t.Setenv("WATCHED_FOLDERS", "")
	t.Setenv("SYNCTHING_CONFIG_WAIT_SECONDS", "10")

	restore := syncthingConfigCandidatesFn
	syncthingConfigCandidatesFn = func() []string { return []string{cfgPath} }
	t.Cleanup(func() { syncthingConfigCandidatesFn = restore })

	start := time.Now()
	_, err := loadConfigAwaitingSyncthing(context.Background())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected an immediate error when RELAY_URL is missing")
	}
	if elapsed > time.Second {
		t.Fatalf("must fail fast without RELAY_URL, but waited %v", elapsed)
	}
	if !strings.Contains(err.Error(), "RELAY_URL") {
		t.Fatalf("error %q should mention the missing RELAY_URL", err)
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

// newSyncthingStub returns a Syncthing test server that reports a fixed Device
// ID and emits exactly one relevant change event (LocalIndexUpdated, folder
// vault-a) on the first poll, then long-polls with no further events — letting
// the run loop's own timers, not a stream of events, drive the test.
func newSyncthingStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/system/status":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"myID":"DEVICE-INT"}`))
		case "/rest/events":
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("since") == "0" {
				_, _ = w.Write([]byte(`[{"id":1,"type":"LocalIndexUpdated","data":{"folder":"vault-a","sequence":42}}]`))
				return
			}
			// Emulate Syncthing's long-poll: hold the connection until the
			// client disconnects (ctx cancel), then return no new events.
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestRunServiceSurvivesInactiveSubscription is the regression guard for the
// crash-restart loop: when the relay declines triggers with HTTP 400 (an
// expired/cancelled/not-yet-provisioned subscription), runService must keep
// running and shut down cleanly (exit 0) on signal — never exit 1, which under
// `restart: unless-stopped` would loop forever. It also asserts the inactive
// verdict does not re-hammer the relay on the fast debounce cadence.
func TestRunServiceSurvivesInactiveSubscription(t *testing.T) {
	// Push the recheck far beyond the test window so any extra trigger would
	// have to come from the (wrong) fast debounce cadence, not the recheck.
	restore := inactiveRecheckInterval
	inactiveRecheckInterval = time.Hour
	t.Cleanup(func() { inactiveRecheckInterval = restore })

	syncthing := newSyncthingStub(t)

	var triggerCalls atomic.Int32
	firstTrigger := make(chan struct{}, 1)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/trigger":
			triggerCalls.Add(1)
			select {
			case firstTrigger <- struct{}{}:
			default:
			}
			http.Error(w, `{"error":"subscription expired"}`, http.StatusBadRequest)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(relay.Close)

	cfg := Config{
		SyncthingAPIURL: syncthing.URL,
		SyncthingAPIKey: "test-key",
		RelayURL:        relay.URL,
		DebounceSeconds: 1,
		// Announce disabled: these tests isolate change-detection / inactive /
		// fatal-on-change behavior; the startup announce has its own tests.
		StartupAnnounce: false,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	codeCh := make(chan int, 1)
	go func() { codeCh <- runService(ctx, cfg) }()

	// Wait for the relay to decline the first trigger with 400.
	select {
	case <-firstTrigger:
	case code := <-codeCh:
		t.Fatalf("runService exited early with code %d before processing a trigger", code)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the relay trigger")
	}

	// Across several debounce periods with no new events, the 400 must neither
	// bring the process down nor cause a fast-cadence re-hammer of the relay.
	select {
	case code := <-codeCh:
		t.Fatalf("runService exited with code %d on a 400 trigger; expected it to keep running", code)
	case <-time.After(2500 * time.Millisecond):
	}
	if got := triggerCalls.Load(); got != 1 {
		t.Fatalf("relay was triggered %d times while inactive; want 1 (no fast-cadence re-hammer)", got)
	}

	// Graceful shutdown must report success, not a fatal exit.
	cancel()
	select {
	case code := <-codeCh:
		if code != 0 {
			t.Fatalf("runService exit code = %d after graceful shutdown, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runService did not shut down after context cancel")
	}
}

// TestRunServiceResumesAfterSubscriptionActivates proves the second half of the
// requirement: once the subscription is active again, delivery resumes
// automatically — here even with NO further Syncthing change, driven solely by
// the slow recheck timer.
func TestRunServiceResumesAfterSubscriptionActivates(t *testing.T) {
	restore := inactiveRecheckInterval
	inactiveRecheckInterval = 150 * time.Millisecond
	t.Cleanup(func() { inactiveRecheckInterval = restore })

	syncthing := newSyncthingStub(t)

	var active atomic.Bool
	declined := make(chan struct{}, 1)
	accepted := make(chan struct{}, 1)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/trigger":
			if active.Load() {
				select {
				case accepted <- struct{}{}:
				default:
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(`{"status":"accepted","devices_notified":1}`))
				return
			}
			select {
			case declined <- struct{}{}:
			default:
			}
			http.Error(w, `{"error":"subscription expired"}`, http.StatusBadRequest)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(relay.Close)

	cfg := Config{
		SyncthingAPIURL: syncthing.URL,
		SyncthingAPIKey: "test-key",
		RelayURL:        relay.URL,
		DebounceSeconds: 1,
		// Announce disabled: these tests isolate change-detection / inactive /
		// fatal-on-change behavior; the startup announce has its own tests.
		StartupAnnounce: false,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	codeCh := make(chan int, 1)
	go func() { codeCh <- runService(ctx, cfg) }()

	// The relay first declines the trigger (inactive subscription).
	select {
	case <-declined:
	case code := <-codeCh:
		t.Fatalf("runService exited with code %d before the relay declined", code)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the first (declined) trigger")
	}

	// Subscription is reactivated. With no further Syncthing events, the slow
	// recheck must resume delivery on its own.
	active.Store(true)
	select {
	case <-accepted:
	case code := <-codeCh:
		t.Fatalf("runService exited with code %d instead of resuming delivery", code)
	case <-time.After(10 * time.Second):
		t.Fatal("delivery did not resume automatically after the subscription became active")
	}

	cancel()
	select {
	case code := <-codeCh:
		if code != 0 {
			t.Fatalf("runService exit code = %d after graceful shutdown, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runService did not shut down after context cancel")
	}
}

// TestRunServiceExitsOnFatalTrigger asserts the other half stays fatal: a 404
// (wrong RELAY_URL / missing endpoint) at the trigger stage must exit 1, even
// though the startup health check passed.
func TestRunServiceExitsOnFatalTrigger(t *testing.T) {
	syncthing := newSyncthingStub(t)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			// Trigger (and everything else) is 404 -> fatal misconfiguration.
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(relay.Close)

	cfg := Config{
		SyncthingAPIURL: syncthing.URL,
		SyncthingAPIKey: "test-key",
		RelayURL:        relay.URL,
		DebounceSeconds: 1,
		// Announce disabled: these tests isolate change-detection / inactive /
		// fatal-on-change behavior; the startup announce has its own tests.
		StartupAnnounce: false,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	codeCh := make(chan int, 1)
	go func() { codeCh <- runService(ctx, cfg) }()

	select {
	case code := <-codeCh:
		if code != 1 {
			t.Fatalf("runService exit code = %d for a fatal 404 trigger, want 1", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runService did not exit on a fatal trigger")
	}
}

// newQuietSyncthingStub reports a fixed Device ID and NEVER emits a change
// event — every /rest/events poll long-polls until the client disconnects and
// returns []. This isolates the startup announce: the only relay trigger a test
// sees is the announce itself, not a change-driven one.
func newQuietSyncthingStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/system/status":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"myID":"DEVICE-QUIET"}`))
		case "/rest/events":
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestRunServiceStartupAnnounceDelivers proves B1: with no Syncthing change at
// all, a successful startup fires exactly one wake-up (the announce) so the
// helper proves liveness to the iPhone immediately.
func TestRunServiceStartupAnnounceDelivers(t *testing.T) {
	syncthing := newQuietSyncthingStub(t)

	var triggerCalls atomic.Int32
	announced := make(chan struct{}, 1)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/trigger":
			triggerCalls.Add(1)
			select {
			case announced <- struct{}{}:
			default:
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"status":"accepted","devices_notified":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(relay.Close)

	cfg := Config{
		SyncthingAPIURL: syncthing.URL,
		SyncthingAPIKey: "test-key",
		RelayURL:        relay.URL,
		DebounceSeconds: 1,
		StartupAnnounce: true,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	codeCh := make(chan int, 1)
	go func() { codeCh <- runService(ctx, cfg) }()

	select {
	case <-announced:
	case code := <-codeCh:
		t.Fatalf("runService exited with code %d before the startup announce fired", code)
	case <-time.After(10 * time.Second):
		t.Fatal("startup announce never reached the relay")
	}

	// No Syncthing changes are emitted, so the announce must be the only trigger.
	time.Sleep(1500 * time.Millisecond)
	if got := triggerCalls.Load(); got != 1 {
		t.Fatalf("relay triggered %d times; want exactly 1 (a single startup announce)", got)
	}

	cancel()
	select {
	case code := <-codeCh:
		if code != 0 {
			t.Fatalf("runService exit code = %d after graceful shutdown, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runService did not shut down after context cancel")
	}
}

// TestRunServiceStartupAnnounceDisabled proves the opt-out: with
// StartupAnnounce=false and no change events, the relay is never triggered.
func TestRunServiceStartupAnnounceDisabled(t *testing.T) {
	syncthing := newQuietSyncthingStub(t)

	var triggerCalls atomic.Int32
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/trigger":
			triggerCalls.Add(1)
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"status":"accepted","devices_notified":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(relay.Close)

	cfg := Config{
		SyncthingAPIURL: syncthing.URL,
		SyncthingAPIKey: "test-key",
		RelayURL:        relay.URL,
		DebounceSeconds: 1,
		StartupAnnounce: false,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	codeCh := make(chan int, 1)
	go func() { codeCh <- runService(ctx, cfg) }()

	time.Sleep(1500 * time.Millisecond)
	if got := triggerCalls.Load(); got != 0 {
		t.Fatalf("relay triggered %d times with announce disabled and no changes; want 0", got)
	}

	cancel()
	select {
	case code := <-codeCh:
		if code != 0 {
			t.Fatalf("runService exit code = %d after graceful shutdown, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runService did not shut down after context cancel")
	}
}

// TestRunServiceStartupAnnounceInactiveKeepsRunning proves the announce is
// best-effort: an inactive-subscription verdict (HTTP 400) at startup is logged
// and ignored — the service keeps running and shuts down cleanly, never exit 1.
func TestRunServiceStartupAnnounceInactiveKeepsRunning(t *testing.T) {
	syncthing := newQuietSyncthingStub(t)

	declined := make(chan struct{}, 1)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/api/v1/trigger":
			select {
			case declined <- struct{}{}:
			default:
			}
			http.Error(w, `{"error":"subscription expired"}`, http.StatusBadRequest)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(relay.Close)

	cfg := Config{
		SyncthingAPIURL: syncthing.URL,
		SyncthingAPIKey: "test-key",
		RelayURL:        relay.URL,
		DebounceSeconds: 1,
		StartupAnnounce: true,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	codeCh := make(chan int, 1)
	go func() { codeCh <- runService(ctx, cfg) }()

	select {
	case <-declined:
	case code := <-codeCh:
		t.Fatalf("runService exited with code %d before the announce was declined", code)
	case <-time.After(10 * time.Second):
		t.Fatal("startup announce never reached the relay")
	}

	// A declined announce must not bring the service down.
	select {
	case code := <-codeCh:
		t.Fatalf("runService exited with code %d on a declined startup announce; expected it to keep running", code)
	case <-time.After(1500 * time.Millisecond):
	}

	cancel()
	select {
	case code := <-codeCh:
		if code != 0 {
			t.Fatalf("runService exit code = %d after graceful shutdown, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runService did not shut down after context cancel")
	}
}

// TestRunServiceStartupAnnounceFatalExits proves a genuine misconfiguration at
// the announce stage (404 trigger while health is 200) exits 1, mirroring the
// change-driven fatal path.
func TestRunServiceStartupAnnounceFatalExits(t *testing.T) {
	syncthing := newQuietSyncthingStub(t)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			http.NotFound(w, r) // trigger 404 -> fatal misconfiguration
		}
	}))
	t.Cleanup(relay.Close)

	cfg := Config{
		SyncthingAPIURL: syncthing.URL,
		SyncthingAPIKey: "test-key",
		RelayURL:        relay.URL,
		DebounceSeconds: 1,
		StartupAnnounce: true,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	codeCh := make(chan int, 1)
	go func() { codeCh <- runService(ctx, cfg) }()

	select {
	case code := <-codeCh:
		if code != 1 {
			t.Fatalf("runService exit code = %d for a fatal 404 startup announce, want 1", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runService did not exit on a fatal startup announce")
	}
}

func TestLoadConfigStartupAnnounceDefaultAndOverride(t *testing.T) {
	set := func(announce string) (Config, error) {
		t.Setenv("SYNCTHING_API_URL", "http://localhost:8384")
		t.Setenv("SYNCTHING_API_KEY", "test-key")
		t.Setenv("RELAY_URL", "https://relay.example.com")
		t.Setenv("DEBOUNCE_SECONDS", "")
		t.Setenv("WATCHED_FOLDERS", "")
		t.Setenv("STARTUP_ANNOUNCE", announce)
		return loadConfig()
	}

	cfg, err := set("")
	if err != nil {
		t.Fatalf("default loadConfig error: %v", err)
	}
	if !cfg.StartupAnnounce {
		t.Fatal("StartupAnnounce should default to true when STARTUP_ANNOUNCE is unset")
	}

	for _, falsey := range []string{"false", "0", "no", "off", "FALSE"} {
		cfg, err := set(falsey)
		if err != nil {
			t.Fatalf("loadConfig(%q) error: %v", falsey, err)
		}
		if cfg.StartupAnnounce {
			t.Fatalf("STARTUP_ANNOUNCE=%q should disable the announce", falsey)
		}
	}

	cfg, err = set("true")
	if err != nil || !cfg.StartupAnnounce {
		t.Fatalf("STARTUP_ANNOUNCE=true should enable announce (cfg=%v err=%v)", cfg.StartupAnnounce, err)
	}

	if _, err := set("maybe"); err == nil {
		t.Fatal("STARTUP_ANNOUNCE=maybe should be rejected")
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
