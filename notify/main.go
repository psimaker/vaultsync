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
	// StartupAnnounce sends one wake-up right after a successful startup health
	// check (B1) so a freshly (re)started helper proves liveness to the iPhone
	// immediately — the moment that flips the app's reactivation card / "active"
	// state — without waiting for the next vault change. loadConfig enables it by
	// default (env STARTUP_ANNOUNCE=false to opt out). NOTE: the zero value is
	// false, so Config literals that don't set it (e.g. focused runService tests)
	// keep announce off; only loadConfig turns it on for the real service.
	StartupAnnounce bool
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

	cfg, err := loadConfigAwaitingSyncthing(context.Background())
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
		runCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
		code := runService(runCtx, cfg)
		stop()
		os.Exit(code)
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

	// Startup announce (B1) defaults ON; opt out with a falsey STARTUP_ANNOUNCE.
	startupAnnounce := true
	if v := strings.TrimSpace(strings.ToLower(os.Getenv("STARTUP_ANNOUNCE"))); v != "" {
		switch v {
		case "0", "false", "no", "off":
			startupAnnounce = false
		case "1", "true", "yes", "on":
			startupAnnounce = true
		default:
			return Config{}, fmt.Errorf("STARTUP_ANNOUNCE must be a boolean (got %q)", v)
		}
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

	apiURL := required("SYNCTHING_API_URL")
	apiKey := required("SYNCTHING_API_KEY")

	// Auto-detect the Syncthing API key/URL from config.xml when either is not
	// set explicitly. This removes the manual "open the Syncthing web UI, copy
	// the API key, paste it into a command" step — the documented Phase-3
	// onboarding bottleneck — for the common case where the helper runs next to
	// Syncthing or shares its config volume. Explicit env always wins per field;
	// detection only fills the gaps. Reading config.xml is purely local: no
	// network, nothing leaves the server.
	var detectErr error
	if apiURL == "" || apiKey == "" {
		detected, err := detectSyncthingFromConfig()
		if err != nil {
			detectErr = err
		} else {
			filled := make([]string, 0, 2)
			if apiURL == "" {
				apiURL = detected.APIURL
				filled = append(filled, "SYNCTHING_API_URL")
			}
			if apiKey == "" {
				apiKey = detected.APIKey
				filled = append(filled, "SYNCTHING_API_KEY")
			}
			if len(filled) > 0 {
				// The API key itself is deliberately never logged.
				slog.Info("auto-detected Syncthing configuration from config.xml",
					"component", "config",
					"source", detected.Source,
					"filled", strings.Join(filled, ", "),
					"syncthing_url", apiURL,
				)
			}
		}
	}

	// RELAY_URL stays required (no production default). A default-to-prod would
	// turn "forgot an env var" into unsanctioned production traffic — the
	// startup announce fires a real trigger to whatever RELAY_URL points at — so
	// the relay endpoint must be a conscious choice. The docker-compose file and
	// the app's setup command both supply it explicitly, so this costs the
	// operator nothing in practice; only the Syncthing key/URL are auto-detected.
	cfg := Config{
		SyncthingAPIURL: apiURL,
		SyncthingAPIKey: apiKey,
		RelayURL:        required("RELAY_URL"),
		DebounceSeconds: debounce,
		WatchedFolders:  watched,
		StartupAnnounce: startupAnnounce,
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
		// A permission denial has a different fix (match the uid that owns
		// config.xml) than "set the env vars / SYNCTHING_CONFIG", so surface it
		// directly instead of burying it in the generic message.
		if detectErr != nil && errors.Is(detectErr, os.ErrPermission) {
			return Config{}, detectErr
		}
		if detectErr != nil {
			return Config{}, fmt.Errorf("missing %s; Syncthing auto-detection also found nothing: %w", strings.Join(missing, ", "), detectErr)
		}
		return Config{}, fmt.Errorf("required configuration not set: %s. Set them explicitly; the Syncthing key/URL can also be auto-detected by pointing SYNCTHING_CONFIG at your config.xml", strings.Join(missing, ", "))
	}
	return cfg, nil
}

// configWaitPollInterval is how often loadConfigAwaitingSyncthing re-checks for a
// not-yet-written config.xml. A package var so tests can shrink it.
var configWaitPollInterval = 2 * time.Second

// loadConfigAwaitingSyncthing wraps loadConfig with a bounded wait for the common
// first-boot race where the helper starts before Syncthing has written config.xml
// (e.g. a fresh `docker compose up`). It retries ONLY while the sole problem is a
// not-yet-present config.xml AND RELAY_URL is set; a missing RELAY_URL, a
// permission denial, or a malformed config still fails immediately. The wait
// budget is SYNCTHING_CONFIG_WAIT_SECONDS (0/unset = no wait — the unchanged
// fail-fast for a bare invocation; docker-compose sets it to 60).
func loadConfigAwaitingSyncthing(ctx context.Context) (Config, error) {
	cfg, err := loadConfig()
	if err == nil {
		return cfg, nil
	}

	wait := configWaitDuration()
	if wait <= 0 || !errors.Is(err, errNoSyncthingConfig) || strings.TrimSpace(os.Getenv("RELAY_URL")) == "" {
		return Config{}, err
	}

	slog.Warn("Syncthing config.xml not found yet; waiting for it to appear",
		"classification", "recoverable",
		"component", "config",
		"wait", wait,
		"action", "retry",
	)

	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	ticker := time.NewTicker(configWaitPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return Config{}, err
		case <-deadline.C:
			return Config{}, err
		case <-ticker.C:
			cfg, retryErr := loadConfig()
			if retryErr == nil {
				slog.Info("Syncthing config.xml appeared; continuing startup", "component", "config")
				return cfg, nil
			}
			// A different failure (permission, malformed, missing RELAY_URL) will
			// not fix itself by waiting — surface it now instead of burning the
			// whole budget.
			if !errors.Is(retryErr, errNoSyncthingConfig) {
				return Config{}, retryErr
			}
			err = retryErr
		}
	}
}

func configWaitDuration() time.Duration {
	v := strings.TrimSpace(os.Getenv("SYNCTHING_CONFIG_WAIT_SECONDS"))
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
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

// inactiveRecheckInterval is how long the run loop waits before re-attempting a
// trigger the relay declined for an inactive subscription. It is deliberately
// much slower than the debounce cadence: it resumes delivery on its own soon
// after the subscription is reactivated — even if no new Syncthing change
// arrives — without hammering the relay (which allows ~10 triggers/min/device)
// while the subscription stays inactive. Overridable in tests.
var inactiveRecheckInterval = 60 * time.Second

func runService(ctx context.Context, cfg Config) int {
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
		"startup_announce", cfg.StartupAnnounce,
	)

	// B1 — startup announce. Fire one wake-up now so a freshly (re)started helper
	// proves liveness to the iPhone immediately (this is the real delivery that
	// flips the app's reactivation card / "active" state), instead of waiting for
	// the next vault change. Best-effort: an inactive subscription or a transient
	// failure is logged by fireTrigger and ignored — the run loop still delivers
	// on the next change / inactive-recheck; only a genuine misconfiguration
	// (fatal) brings the service down, exactly as a change-driven trigger would.
	//
	// B3 / anti-spam (K6): this runs at most once per process start, and the
	// relay's per-device 30s debounce absorbs rapid container restarts (a
	// too-soon announce is debounced to 429 -> retried within the bounded window
	// below -> no duplicate push, just a logged retry). The bounded context keeps
	// startup responsive instead of blocking on a long 429 backoff.
	if cfg.StartupAnnounce {
		announceCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		outcome := fireTrigger(announceCtx, relay, "startup-announce")
		cancel()
		if outcome == outcomeFatal {
			return 1
		}
	}

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
					outcome := fireTrigger(flushCtx, relay, "stream-flush")
					flushCancel()
					if outcome == outcomeDelivered {
						markTriggered(lastTriggered, pending)
					}
					if outcome == outcomeFatal {
						return 1
					}
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
			switch fireTrigger(ctx, relay, "change-detected") {
			case outcomeDelivered:
				markTriggered(lastTriggered, pending)
				clear(pending)
			case outcomeFatal:
				return 1
			case outcomeRetry:
				// Transient failure: keep the pending work and retry promptly on
				// the debounce cadence.
				debounceTimer = time.NewTimer(debounceDur)
				debounceCh = debounceTimer.C
			case outcomeSubscriptionInactive:
				// No active subscription: keep the pending work and re-check on a
				// slow cadence (not the fast debounce cadence) so delivery
				// resumes automatically once the subscription is active again —
				// even with no further changes — without hammering the relay
				// while it stays inactive. A new change still re-arms the faster
				// debounce timer in the event branch.
				debounceTimer = time.NewTimer(inactiveRecheckInterval)
				debounceCh = debounceTimer.C
			}

		case <-ctx.Done():
			slog.Info("shutdown signal received")
			if len(pending) > 0 {
				shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
				if fireTrigger(shutdownCtx, relay, "shutdown-flush") == outcomeDelivered {
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

// triggerOutcome classifies the result of a relay trigger so the run loop can
// react without collapsing the process on recoverable conditions.
type triggerOutcome int

const (
	// outcomeDelivered: the relay accepted the wake-up signal.
	outcomeDelivered triggerOutcome = iota
	// outcomeRetry: a transient failure (network blip, 5xx, relay rate limit).
	// Keep the pending work and retry promptly on the debounce cadence.
	outcomeRetry
	// outcomeSubscriptionInactive: the relay declined because the device has no
	// active subscription (expired, cancelled, or not yet provisioned). Keep the
	// process alive; delivery resumes automatically once the subscription is
	// active again. This must never bring the sidecar down.
	outcomeSubscriptionInactive
	// outcomeFatal: a genuine misconfiguration (wrong RELAY_URL / missing
	// endpoint) that a runtime retry cannot fix.
	outcomeFatal
)

// fireTrigger sends a relay trigger and classifies the result so the caller can
// retain pending work on transient failures, keep running through inactive
// subscriptions, and exit only on a real misconfiguration.
func fireTrigger(ctx context.Context, relay *RelayClient, reason string) triggerOutcome {
	slog.Info("sending relay trigger", "reason", reason)
	err := relay.Trigger(ctx)
	if err == nil {
		return outcomeDelivered
	}

	switch {
	case isFatal(err):
		slog.Error("relay trigger failed with fatal configuration error",
			"classification", "fatal",
			"component", "relay",
			"reason", reason,
			"error", err,
			"action", "exit",
		)
		return outcomeFatal
	case isSubscriptionInactive(err):
		slog.Warn("relay reports no active subscription for this device; keeping notify running",
			"classification", "recoverable",
			"component", "relay",
			"reason", reason,
			"error", err,
			"action", "await_active_subscription",
		)
		return outcomeSubscriptionInactive
	default:
		slog.Error("relay trigger failed",
			"classification", "recoverable",
			"component", "relay",
			"reason", reason,
			"error", err,
			"action", "continue",
		)
		return outcomeRetry
	}
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
