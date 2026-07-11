package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

type retryPolicy struct {
	Attempts       int
	AttemptTimeout time.Duration
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

type preflightCheck struct {
	Name        string
	Remediation string
	Run         func(context.Context) error
	// WarnOn downgrades a matching error to a visible warning instead of a
	// failure (#88): the check counts as passed, but the condition is printed
	// and logged rather than swallowed.
	WarnOn func(error) (string, bool)
}

// preflightOut is where the human-readable doctor output goes. A package var
// so tests can capture it (the same pattern as inactiveRecheckInterval).
var preflightOut io.Writer = os.Stdout

type preflightMode struct {
	Name           string
	IncludeTrigger bool
	PrintSuccess   bool
	Retry          retryPolicy
}

func runDoctor(ctx context.Context, cfg Config) error {
	mode := preflightMode{
		Name:           "doctor",
		IncludeTrigger: true,
		PrintSuccess:   true,
		Retry: retryPolicy{
			Attempts:       4,
			AttemptTimeout: 6 * time.Second,
			InitialBackoff: time.Second,
			MaxBackoff:     5 * time.Second,
		},
	}

	fmt.Fprintln(preflightOut, "vaultsync-notify doctor: running preflight checks")
	warnings, err := runPreflight(ctx, cfg, mode)
	if err != nil {
		return err
	}
	if warnings > 0 {
		fmt.Fprintf(preflightOut, "vaultsync-notify doctor: all checks passed, %d warning(s) — see above\n", warnings)
	} else {
		fmt.Fprintln(preflightOut, "vaultsync-notify doctor: all checks passed")
	}
	return nil
}

func runHealthcheck(ctx context.Context, cfg Config) error {
	mode := preflightMode{
		Name:           "healthcheck",
		IncludeTrigger: false,
		PrintSuccess:   false,
		Retry: retryPolicy{
			Attempts:       3,
			AttemptTimeout: 4 * time.Second,
			InitialBackoff: time.Second,
			MaxBackoff:     3 * time.Second,
		},
	}
	_, err := runPreflight(ctx, cfg, mode)
	return err
}

func runPreflight(ctx context.Context, cfg Config, mode preflightMode) (int, error) {
	st := NewSyncthingClient(cfg.SyncthingAPIURL, cfg.SyncthingAPIKey)
	relay := NewRelayClient(cfg.RelayURL, "")

	var deviceID string

	checks := []preflightCheck{
		{
			Name:        "Syncthing API reachable",
			Remediation: "Verify SYNCTHING_API_URL points to your Syncthing GUI/API and the service is running.",
			Run: func(ctx context.Context) error {
				return st.CheckAPIReachable(ctx)
			},
		},
		{
			Name:        "Syncthing API key valid",
			Remediation: "Open Syncthing Web UI -> Actions -> Settings -> GUI and update SYNCTHING_API_KEY with the current API key.",
			Run: func(ctx context.Context) error {
				return st.ValidateAPIKey(ctx)
			},
		},
		{
			Name:        "Syncthing Device ID readable",
			Remediation: "Ensure /rest/system/status returns a non-empty myID value for this Syncthing instance.",
			Run: func(ctx context.Context) error {
				id, err := st.GetDeviceID(ctx)
				if err == nil {
					deviceID = id
				}
				return err
			},
		},
		{
			Name:        "Relay health endpoint reachable",
			Remediation: "Check outbound HTTPS connectivity and confirm RELAY_URL points to the relay root URL.",
			Run: func(ctx context.Context) error {
				return relay.CheckHealth(ctx)
			},
		},
	}

	if mode.IncludeTrigger {
		checks = append(checks, preflightCheck{
			Name:        "Relay trigger endpoint response sanity",
			Remediation: "Check RELAY_URL, relay deployment health, and whether your relay accepts POST /api/v1/trigger requests.",
			Run: func(ctx context.Context) error {
				if deviceID == "" {
					return fmt.Errorf("device ID unavailable because prior checks failed")
				}
				triggerRelay := NewRelayClient(cfg.RelayURL, deviceID)
				return triggerRelay.ProbeTrigger(ctx)
			},
			// The endpoint answered, so connectivity — all this check gates —
			// is proven. But "subscribed and no wake-ups arrive" used to read
			// an all-passed doctor and be stuck; make the state visible (#88).
			WarnOn: func(err error) (string, bool) {
				if isSubscriptionInactive(err) {
					return "relay reports no active subscription for this device — wake-up delivery is off until it is active. Right after setup this is normal (subscribe in the VaultSync app, Relay tab). If you ARE subscribed, open VaultSync on the iPhone once so it re-provisions this device, and verify this server's Syncthing is paired with that iPhone.", true
				}
				return "", false
			},
		})
	}

	failures := make([]string, 0)
	warnings := 0
	for _, check := range checks {
		err := runCheckWithRetry(ctx, mode.Retry, check.Name, check.Run)
		if err != nil {
			if check.WarnOn != nil {
				if msg, ok := check.WarnOn(err); ok {
					warnings++
					fmt.Fprintf(preflightOut, "WARN %s\n  %s\n", check.Name, msg)
					slog.Warn("preflight check passed with warning",
						"classification", "recoverable",
						"component", "preflight",
						"mode", mode.Name,
						"check", check.Name,
						"warning", msg,
					)
					continue
				}
			}
			failures = append(failures, check.Name)
			logPreflightFailure(mode.Name, check, err)
			continue
		}
		if mode.PrintSuccess {
			fmt.Fprintf(preflightOut, "OK   %s\n", check.Name)
		}
	}

	if len(failures) > 0 {
		return warnings, fmt.Errorf("%s failed checks: %s", mode.Name, strings.Join(failures, ", "))
	}
	return warnings, nil
}

func runCheckWithRetry(ctx context.Context, policy retryPolicy, checkName string, run func(context.Context) error) error {
	if policy.Attempts < 1 {
		policy.Attempts = 1
	}
	if policy.AttemptTimeout <= 0 {
		policy.AttemptTimeout = 5 * time.Second
	}
	if policy.InitialBackoff <= 0 {
		policy.InitialBackoff = time.Second
	}
	if policy.MaxBackoff <= 0 {
		policy.MaxBackoff = 5 * time.Second
	}

	var lastErr error
	backoff := policy.InitialBackoff

	for attempt := 1; attempt <= policy.Attempts; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, policy.AttemptTimeout)
		err := run(attemptCtx)
		cancel()
		if err == nil {
			return nil
		}

		lastErr = err
		if attempt == policy.Attempts {
			break
		}
		// The subscription verdict is stable (#88): retrying the same request
		// within seconds cannot change it, so don't burn the retry budget.
		if isSubscriptionInactive(err) {
			break
		}

		if errors.Is(ctx.Err(), context.Canceled) {
			return ctx.Err()
		}

		slog.Warn("preflight check attempt failed; retrying",
			"classification", "recoverable",
			"component", "preflight",
			"check", checkName,
			"attempt", attempt,
			"error", err,
			"retry_in", backoff,
		)

		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		backoff = min(backoff*2, policy.MaxBackoff)
	}

	return lastErr
}

func logPreflightFailure(mode string, check preflightCheck, err error) {
	hint := hintForCheckFailure(err)

	if mode == "doctor" {
		fmt.Fprintf(os.Stderr, "FAIL %s\n", check.Name)
		fmt.Fprintf(os.Stderr, "  Reason: %s\n", hint)
		fmt.Fprintf(os.Stderr, "  Fix: %s\n", check.Remediation)
	}

	slog.Error("preflight check failed",
		"classification", "fatal",
		"component", "preflight",
		"mode", mode,
		"check", check.Name,
		"error", err,
		"hint", check.Remediation,
	)
}

func hintForCheckFailure(err error) string {
	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) {
		if statusErr.Component == "syncthing" {
			switch statusErr.StatusCode {
			case http.StatusUnauthorized, http.StatusForbidden:
				return "Syncthing rejected the API key (HTTP 401/403)."
			case http.StatusNotFound:
				return "Syncthing endpoint was not found (HTTP 404)."
			default:
				return fmt.Sprintf("Syncthing returned HTTP %d.", statusErr.StatusCode)
			}
		}
		if statusErr.Component == "relay" {
			switch statusErr.StatusCode {
			case http.StatusNotFound:
				return "Relay health endpoint was not found (HTTP 404)."
			default:
				return fmt.Sprintf("Relay returned HTTP %d.", statusErr.StatusCode)
			}
		}
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return "Request timed out."
	}
	if errors.Is(err, context.Canceled) {
		return "Request was canceled."
	}

	return err.Error()
}
