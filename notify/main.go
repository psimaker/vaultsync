package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Config struct {
	SyncthingAPIURL string
	SyncthingAPIKey string
	RelayURL        string
	DebounceSeconds int
	WatchedFolders  map[string]bool // nil = watch all
}

type appMode int

const (
	modeRun appMode = iota
	modeDoctor
	modeHealthcheck
)

func main() {
	mode, err := parseMode()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("invalid runtime configuration",
			"classification", "fatal",
			"component", "config",
			"error", err,
			"action", "exit",
		)
		os.Exit(1)
	}

	ctx := context.Background()

	switch mode {
	case modeDoctor:
		if err := runDoctor(ctx, cfg); err != nil {
			slog.Error("doctor checks failed",
				"classification", "fatal",
				"component", "doctor",
				"error", err,
				"action", "fix_configuration",
			)
			os.Exit(1)
		}
		return
	case modeHealthcheck:
		if err := runHealthcheck(ctx, cfg); err != nil {
			slog.Error("healthcheck failed",
				"classification", "fatal",
				"component", "healthcheck",
				"error", err,
			)
			os.Exit(1)
		}
		return
	default:
		os.Exit(runService(cfg))
	}
}

func parseMode() (appMode, error) {
	doctor := flag.Bool("doctor", false, "run connectivity checks and exit")
	healthcheck := flag.Bool("healthcheck", false, "run readiness checks and exit")
	flag.Parse()

	if *doctor && *healthcheck {
		return modeRun, fmt.Errorf("--doctor and --healthcheck cannot be used together")
	}

	if *doctor {
		return modeDoctor, nil
	}
	if *healthcheck {
		return modeHealthcheck, nil
	}
	return modeRun, nil
}

func loadConfig() (Config, error) {
	required := func(key string) string {
		v := strings.TrimSpace(os.Getenv(key))
		if v == "" {
			return ""
		}
		return v
	}

	debounce := 5
	if v := os.Getenv("DEBOUNCE_SECONDS"); v != "" {
		d, err := strconv.Atoi(v)
		if err != nil || d < 1 {
			return Config{}, fmt.Errorf("DEBOUNCE_SECONDS must be a positive integer (got %q)", v)
		}
		debounce = d
	}

	var watched map[string]bool
	if v := os.Getenv("WATCHED_FOLDERS"); v != "" {
		watched = make(map[string]bool)
		for _, f := range strings.Split(v, ",") {
			f = strings.TrimSpace(f)
			if f != "" {
				watched[f] = true
			}
		}
	}

	cfg := Config{
		SyncthingAPIURL: required("SYNCTHING_API_URL"),
		SyncthingAPIKey: required("SYNCTHING_API_KEY"),
		RelayURL:        required("RELAY_URL"),
		DebounceSeconds: debounce,
		WatchedFolders:  watched,
	}

	missing := make([]string, 0, 3)
	if cfg.SyncthingAPIURL == "" {
		missing = append(missing, "SYNCTHING_API_URL")
	}
	if cfg.SyncthingAPIKey == "" {
		missing = append(missing, "SYNCTHING_API_KEY")
	}
	if cfg.RelayURL == "" {
		missing = append(missing, "RELAY_URL")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("required environment variables not set: %s", strings.Join(missing, ", "))
	}

	return cfg, nil
}

// Accept only events that can indicate real sync work for remote peers.
// StateChanged is intentionally excluded because every idle→scanning→syncing
// transition would otherwise emit multiple pushes per logical change.
//
// LocalIndexUpdated covers direct edits on the homeserver itself.
// FolderCompletion with outstanding need{Items,Bytes} covers the common case
// where a remote peer is known to be behind and needs a wake-up to reconnect.
var relevantEventTypes = map[string]bool{
	"LocalIndexUpdated": true,
	"FolderCompletion":  true,
}

func runService(cfg Config) int {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	st := NewSyncthingClient(cfg.SyncthingAPIURL, cfg.SyncthingAPIKey)
	deviceID, err := waitForDeviceID(ctx, st)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return 0
		}
		slog.Error("failed to initialize Syncthing Device ID",
			"classification", "fatal",
			"component", "syncthing",
			"error", err,
			"action", "exit",
		)
		return 1
	}

	relay := NewRelayClient(cfg.RelayURL, deviceID)
	if err := warmupRelay(ctx, relay); err != nil {
		slog.Error("relay startup check failed with fatal configuration error",
			"classification", "fatal",
			"component", "relay",
			"error", err,
			"action", "exit",
		)
		return 1
	}

	slog.Info("vaultsync-notify starting",
		"device_id", deviceID,
		"syncthing_url", cfg.SyncthingAPIURL,
		"relay_url", cfg.RelayURL,
		"debounce_seconds", cfg.DebounceSeconds,
		"watched_folders", formatWatched(cfg.WatchedFolders),
	)

	events := st.Subscribe(ctx)
	debounceDur := time.Duration(cfg.DebounceSeconds) * time.Second

	var debounceTimer *time.Timer
	var debounceCh <-chan time.Time
	pending := make(map[string]string)
	lastTriggered := make(map[string]string)

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				if len(pending) > 0 {
					flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
					delivered, fatal := fireTrigger(flushCtx, relay)
					if delivered {
						markTriggered(lastTriggered, pending)
					}
					if fatal {
						flushCancel()
						return 1
					}
					flushCancel()
				}
				if ctx.Err() == nil {
					slog.Error("syncthing event stream closed unexpectedly",
						"classification", "fatal",
						"component", "syncthing",
						"action", "exit",
					)
					return 1
				}
				slog.Info("event stream closed, shutting down")
				return 0
			}

			candidate, ok := triggerCandidateForEvent(ev, cfg.WatchedFolders)
			if !ok {
				continue
			}

			if lastTriggered[candidate.Folder] == candidate.Marker {
				slog.Debug("skipping duplicate trigger candidate",
					"type", ev.Type,
					"folder", candidate.Folder,
					"marker", candidate.Marker,
					"id", ev.ID,
				)
				continue
			}

			slog.Debug("queued trigger candidate",
				"type", ev.Type,
				"folder", candidate.Folder,
				"marker", candidate.Marker,
				"id", ev.ID,
			)
			pending[candidate.Folder] = candidate.Marker

			if debounceTimer == nil {
				debounceTimer = time.NewTimer(debounceDur)
				debounceCh = debounceTimer.C
			} else {
				if !debounceTimer.Stop() {
					select {
					case <-debounceTimer.C:
					default:
					}
				}
				debounceTimer.Reset(debounceDur)
			}
		case <-debounceCh:
			debounceTimer = nil
			debounceCh = nil
			if len(pending) == 0 {
				continue
			}
			delivered, fatal := fireTrigger(ctx, relay)
			if delivered {
				markTriggered(lastTriggered, pending)
				clear(pending)
				continue
			}
			if fatal {
				return 1
			}
			debounceTimer = time.NewTimer(debounceDur)
			debounceCh = debounceTimer.C

		case <-ctx.Done():
			slog.Info("shutdown signal received")
			if len(pending) > 0 {
				shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
				if delivered, _ := fireTrigger(shutdownCtx, relay); delivered {
					markTriggered(lastTriggered, pending)
				}
				shutdownCancel()
			}
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			slog.Info("vaultsync-notify stopped")
			return 0
		}
	}
}

func waitForDeviceID(ctx context.Context, st *SyncthingClient) (string, error) {
	backoff := time.Second
	for attempt := 1; ; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		deviceID, err := st.GetDeviceID(attemptCtx)
		cancel()
		if err == nil {
			return deviceID, nil
		}

		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		if isFatalSyncthingError(err) {
			return "", err
		}

		slog.Warn("failed to read Syncthing Device ID; retrying",
			"classification", "recoverable",
			"component", "syncthing",
			"attempt", attempt,
			"error", err,
			"retry_in", backoff,
		)

		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return "", ctx.Err()
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func warmupRelay(ctx context.Context, relay *RelayClient) error {
	backoff := time.Second
	const attempts = 3

	for attempt := 1; attempt <= attempts; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
		err := relay.CheckHealth(attemptCtx)
		cancel()
		if err == nil {
			return nil
		}

		if isFatalRelayConfigError(err) {
			return err
		}

		if attempt == attempts {
			slog.Warn("relay health check failed at startup; continuing with runtime retries",
				"classification", "recoverable",
				"component", "relay",
				"error", err,
				"action", "continue",
			)
			return nil
		}

		slog.Warn("relay health check failed at startup; retrying",
			"classification", "recoverable",
			"component", "relay",
			"attempt", attempt,
			"error", err,
			"retry_in", backoff,
		)

		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		backoff = min(backoff*2, 10*time.Second)
	}

	return nil
}

type triggerCandidate struct {
	Folder string
	Marker string
}

type localIndexUpdatedData struct {
	Folder   string `json:"folder"`
	Sequence int    `json:"sequence"`
}

type folderCompletionData struct {
	Folder    string `json:"folder"`
	Device    string `json:"device"`
	NeedBytes int64  `json:"needBytes"`
	NeedItems int    `json:"needItems"`
	Sequence  int    `json:"sequence"`
}

func triggerCandidateForEvent(ev Event, watchedFolders map[string]bool) (triggerCandidate, bool) {
	if !relevantEventTypes[ev.Type] {
		return triggerCandidate{}, false
	}

	switch ev.Type {
	case "LocalIndexUpdated":
		var data localIndexUpdatedData
		if err := json.Unmarshal(ev.Data, &data); err != nil {
			return triggerCandidate{}, false
		}
		if !isWatchedFolder(data.Folder, watchedFolders) {
			return triggerCandidate{}, false
		}
		return triggerCandidate{
			Folder: data.Folder,
			Marker: fmt.Sprintf("local-index:%d", data.Sequence),
		}, true

	case "FolderCompletion":
		var data folderCompletionData
		if err := json.Unmarshal(ev.Data, &data); err != nil {
			return triggerCandidate{}, false
		}
		if !isWatchedFolder(data.Folder, watchedFolders) {
			return triggerCandidate{}, false
		}
		if data.NeedItems <= 0 && data.NeedBytes <= 0 {
			return triggerCandidate{}, false
		}
		return triggerCandidate{
			Folder: data.Folder,
			Marker: fmt.Sprintf("folder-completion:%s:%d:%d:%d", data.Device, data.Sequence, data.NeedItems, data.NeedBytes),
		}, true

	default:
		return triggerCandidate{}, false
	}
}

func isWatchedFolder(folder string, watchedFolders map[string]bool) bool {
	if folder == "" {
		return false
	}
	if watchedFolders == nil {
		return true
	}
	return watchedFolders[folder]
}

func markTriggered(lastTriggered, pending map[string]string) {
	for folder, marker := range pending {
		lastTriggered[folder] = marker
	}
}

// fireTrigger sends a relay trigger.
// Returns `(delivered, fatal)` so the caller can retain pending work on
// transient relay failures without collapsing the process on recoverable errors.
func fireTrigger(ctx context.Context, relay *RelayClient) (bool, bool) {
	slog.Info("sending relay trigger")
	if err := relay.Trigger(ctx); err != nil {
		if isFatal(err) {
			slog.Error("relay trigger failed with fatal configuration error",
				"classification", "fatal",
				"component", "relay",
				"error", err,
				"action", "exit",
			)
			return false, true
		}
		slog.Error("relay trigger failed",
			"classification", "recoverable",
			"component", "relay",
			"error", err,
			"action", "continue",
		)
		return false, false
	}
	return true, false
}

func formatWatched(folders map[string]bool) string {
	if folders == nil {
		return "all"
	}
	ids := make([]string, 0, len(folders))
	for id := range folders {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return strings.Join(ids, ", ")
}

func isFatalRelayConfigError(err error) bool {
	if isFatal(err) {
		return true
	}

	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		return false
	}

	if statusErr.Component != "relay" {
		return false
	}

	return statusErr.StatusCode >= 400 &&
		statusErr.StatusCode < 500 &&
		statusErr.StatusCode != http.StatusTooManyRequests
}
