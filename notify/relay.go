package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
		http:     &http.Client{Timeout: 15 * time.Second},
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &HTTPStatusError{
			Component:  "relay",
			URL:        url,
			StatusCode: resp.StatusCode,
			Body:       string(body),
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
// exponential backoff (max 5 attempts). Returns fatal errors for invalid
// request/endpoint configuration.
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

		if isFatal(err) {
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
			"error", err,
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
			slog.Warn("decode trigger response failed", "error", err)
			return nil // push was accepted, decode failure is non-critical
		}
		slog.Info("relay trigger accepted", "devices_notified", tr.DevicesNotified)
		return nil

	case http.StatusBadRequest:
		return &fatalError{msg: "relay rejected request (400 Bad Request): check device ID"}

	case http.StatusNotFound:
		return &fatalError{msg: "relay endpoint not found (404): check RELAY_URL"}

	case http.StatusTooManyRequests:
		ra := parseRetryAfter(resp.Header.Get("Retry-After"))
		return &rateLimitError{retryAfter: ra}

	case http.StatusUnauthorized, http.StatusForbidden:
		return &fatalError{msg: fmt.Sprintf("relay rejected request (%d): check RELAY_URL or relay auth policy", resp.StatusCode)}

	default:
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			return &fatalError{msg: fmt.Sprintf("relay rejected request (%d): %s", resp.StatusCode, respBody)}
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &transientError{err: fmt.Errorf("unexpected status %d: %s", resp.StatusCode, respBody)}
	}
}

type fatalError struct{ msg string }

func (e *fatalError) Error() string { return e.msg }

type transientError struct{ err error }

func (e *transientError) Error() string { return e.err.Error() }

type rateLimitError struct{ retryAfter time.Duration }

func (e *rateLimitError) Error() string {
	return fmt.Sprintf("rate limited (retry after %s)", e.retryAfter)
}

func isFatal(err error) bool {
	_, ok := err.(*fatalError)
	return ok
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
