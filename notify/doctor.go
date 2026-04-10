package main

import (
	"context"
	"errors"
	"fmt"
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
}

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

	fmt.Fprintln(os.Stdout, "vaultsync-notify doctor: running preflight checks")
	err := runPreflight(ctx, cfg, mode)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "vaultsync-notify doctor: all checks passed")
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
	return runPreflight(ctx, cfg, mode)
}

func runPreflight(ctx context.Context, cfg Config, mode preflightMode) error {
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
		})
	}

	failures := make([]string, 0)
	for _, check := range checks {
		err := runCheckWithRetry(ctx, mode.Retry, check.Name, check.Run)
		if err != nil {
			failures = append(failures, check.Name)
			logPreflightFailure(mode.Name, check, err)
			continue
		}
		if mode.PrintSuccess {
			fmt.Fprintf(os.Stdout, "OK   %s\n", check.Name)
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("%s failed checks: %s", mode.Name, strings.Join(failures, ", "))
	}
	return nil
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
