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

// Trigger only on ItemFinished. StateChanged fires on every folder transition
// (idle→scanning→syncing→idle), which can produce 4+ pushes per actual file
// change. iOS silent-push budget is consumed aggressively by such bursts.
// ItemFinished alone indicates a real file sync completion and paired with
// the debounce window covers the wake-up semantics we need.
var relevantEventTypes = map[string]bool{
	"ItemFinished": true,
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
	pending := false

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				if pending {
					flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
					if fireTrigger(flushCtx, relay) {
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

			if !isRelevant(ev, cfg.WatchedFolders) {
				continue
			}

			folder := extractFolder(ev)
			slog.Debug("relevant event", "type", ev.Type, "folder", folder, "id", ev.ID)

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
			pending = true

		case <-debounceCh:
			pending = false
			debounceTimer = nil
			debounceCh = nil
			if fireTrigger(ctx, relay) {
				return 1
			}

		case <-ctx.Done():
			slog.Info("shutdown signal received")
			if pending {
				shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
				fireTrigger(shutdownCtx, relay)
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

func isRelevant(ev Event, watchedFolders map[string]bool) bool {
	if !relevantEventTypes[ev.Type] {
		return false
	}
	if watchedFolders == nil {
		return true
	}
	folder := extractFolder(ev)
	return folder != "" && watchedFolders[folder]
}

func extractFolder(ev Event) string {
	var data EventData
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		return ""
	}
	return data.Folder
}

// fireTrigger sends a relay trigger. Returns true if the error is fatal and
// the process should exit.
func fireTrigger(ctx context.Context, relay *RelayClient) bool {
	slog.Info("sending relay trigger")
	if err := relay.Trigger(ctx); err != nil {
		if isFatal(err) {
			slog.Error("relay trigger failed with fatal configuration error",
				"classification", "fatal",
				"component", "relay",
				"error", err,
				"action", "exit",
			)
			return true
		}
		slog.Error("relay trigger failed",
			"classification", "recoverable",
			"component", "relay",
			"error", err,
			"action", "continue",
		)
	}
	return false
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
