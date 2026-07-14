package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RelayClient sends trigger requests to the central relay.
type RelayClient struct {
	relayURL string
	deviceID string
	http     *http.Client
}

func NewRelayClient(relayURL, deviceID string) *RelayClient {
	return &RelayClient{
		relayURL: relayURL,
		deviceID: deviceID,
		http:     &http.Client{Timeout: 15 * time.Second, CheckRedirect: rejectHelperHTTPRedirect},
	}
}

type triggerRequest struct {
	DeviceID string `json:"device_id"`
}

type triggerResponse struct {
	Status          string `json:"status"`
	DevicesNotified int    `json:"devices_notified"`
}

type relayHealthResponse struct {
	Status string `json:"status"`
}

// CheckHealth verifies the relay health endpoint.
func (c *RelayClient) CheckHealth(ctx context.Context) error {
	url := c.relayURL + "/api/v1/health"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &HTTPStatusError{
			Component:  "relay",
			StatusCode: resp.StatusCode,
		}
	}

	var health relayHealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return fmt.Errorf("decode relay health: %w", err)
	}

	if !strings.EqualFold(health.Status, "ok") {
		return fmt.Errorf("unexpected relay health status %q", health.Status)
	}

	return nil
}

// Trigger sends a wake-up signal to the relay. Retries transient errors with
// exponential backoff (max 5 attempts).
//
// Two non-transient outcomes short-circuit the retry loop and are surfaced to
// the caller unchanged:
//   - *fatalError for a genuine misconfiguration (endpoint missing / wrong
//     RELAY_URL), which a runtime retry cannot fix.
//   - *subscriptionInactiveError when the relay declines the trigger because
//     the device has no active subscription (expired, cancelled, or not yet
//     provisioned). This is a normal, self-resolving runtime state: the caller
//     keeps the process running and resumes delivery once the subscription is
//     active again — it must never bring the sidecar down.
func (c *RelayClient) Trigger(ctx context.Context) error {
	body, err := json.Marshal(triggerRequest{DeviceID: c.deviceID})
	if err != nil {
		return fmt.Errorf("marshal trigger: %w", err)
	}

	url := c.relayURL + "/api/v1/trigger"
	backoff := time.Second
	const maxRetries = 5

	for attempt := range maxRetries {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		err := c.doTrigger(ctx, url, body)
		if err == nil {
			return nil
		}

		// A misconfiguration or an inactive-subscription verdict is stable;
		// retrying the same request within seconds cannot change it, so return
		// immediately and let the caller decide how to react.
		if isFatal(err) || isSubscriptionInactive(err) {
			return err
		}

		wait := backoff
		if ra, ok := retryAfter(err); ok {
			wait = ra
		}

		slog.Warn("relay trigger failed; retrying",
			"classification", "recoverable",
			"component", "relay",
			"attempt", attempt+1,
			"error_kind", operationalErrorKind(err),
			"retry_in", wait,
		)

		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
		backoff = min(backoff*2, 30*time.Second)
	}

	return fmt.Errorf("relay trigger failed after %d attempts", maxRetries)
}

// ProbeTrigger sends a single trigger request without retries.
//
// A rate-limit verdict proves the trigger endpoint is reachable and behaving —
// which is all the probe checks — so it maps to success. The subscription
// verdict also proves reachability, but is returned unchanged (#88): the
// doctor downgrades it to a WARN instead of silence, because "subscribed but
// no wake-ups arrive" is exactly the case an all-passed doctor used to mask.
// It still never fails the check — subscription state is managed in the iOS
// app, not by the operator, and setup legitimately precedes subscribing.
func (c *RelayClient) ProbeTrigger(ctx context.Context) error {
	body, err := json.Marshal(triggerRequest{DeviceID: c.deviceID})
	if err != nil {
		return fmt.Errorf("marshal trigger: %w", err)
	}

	url := c.relayURL + "/api/v1/trigger"
	err = c.doTrigger(ctx, url, body)
	if err != nil {
		var rateLimited *rateLimitError
		if errors.As(err, &rateLimited) {
			return nil
		}
	}
	return err
}

func (c *RelayClient) doTrigger(ctx context.Context, url string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return &transientError{err: fmt.Errorf("request: %w", err)}
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusAccepted:
		var tr triggerResponse
		if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
			slog.Warn("decode trigger response failed", "error_kind", "decode")
			return nil // push was accepted, decode failure is non-critical
		}
		slog.Info("relay trigger accepted", "devices_notified", tr.DevicesNotified)
		return nil

	case http.StatusNotFound:
		// The endpoint is missing: wrong RELAY_URL or a broken relay deployment.
		// A runtime retry cannot fix this, so it stays fatal. (A wrong RELAY_URL
		// is normally caught earlier by the startup health check.)
		return &fatalError{msg: "relay endpoint not found (404): check RELAY_URL"}

	case http.StatusTooManyRequests:
		ra := parseRetryAfter(resp.Header.Get("Retry-After"))
		return &rateLimitError{retryAfter: ra}

	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden:
		// The relay reached us but declined this device. The trigger endpoint
		// has no auth and the device ID is read straight from Syncthing (always
		// well-formed), so these codes mean the subscription is expired,
		// cancelled, or not yet provisioned — the relay gates pushes on the
		// verified StoreKit expiry. That is a self-resolving runtime state, not
		// a misconfiguration — never fatal.
		return &subscriptionInactiveError{statusCode: resp.StatusCode}

	default:
		// Any other status (5xx, or an undocumented 4xx such as a proxy-level
		// 405/422) is treated as transient: keep retrying and stay alive rather
		// than crash. Preserve only the status code; dependency response bodies
		// are untrusted and must not reach errors or logs.
		return &transientError{err: fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)}
	}
}

type fatalError struct{ msg string }

func (e *fatalError) Error() string { return e.msg }

type transientError struct{ err error }

func (e *transientError) Error() string { return "transient dependency failure" }
func (e *transientError) Unwrap() error { return e.err }

type rateLimitError struct{ retryAfter time.Duration }

func (e *rateLimitError) Error() string {
	return fmt.Sprintf("rate limited (retry after %s)", e.retryAfter)
}

// subscriptionInactiveError indicates the relay declined a trigger because the
// device's subscription is not active: expired, cancelled, or not yet
// provisioned. It is a normal runtime condition, not a misconfiguration, so the
// notify sidecar keeps running and resumes delivery automatically once the
// subscription is active again.
type subscriptionInactiveError struct {
	statusCode int
}

func (e *subscriptionInactiveError) Error() string {
	return fmt.Sprintf("relay declined trigger (HTTP %d): no active subscription for this device (expired, cancelled, or not yet provisioned)", e.statusCode)
}

func isFatal(err error) bool {
	_, ok := err.(*fatalError)
	return ok
}

func isSubscriptionInactive(err error) bool {
	var e *subscriptionInactiveError
	return errors.As(err, &e)
}

func retryAfter(err error) (time.Duration, bool) {
	if e, ok := err.(*rateLimitError); ok && e.retryAfter > 0 {
		return e.retryAfter, true
	}
	return 0, false
}

func parseRetryAfter(header string) time.Duration {
	if header == "" {
		return 30 * time.Second
	}
	if secs, err := strconv.Atoi(header); err == nil {
		return time.Duration(secs) * time.Second
	}
	return 30 * time.Second
}
