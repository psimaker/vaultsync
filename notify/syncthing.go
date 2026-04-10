package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Event represents a single Syncthing REST API event.
type Event struct {
	ID   int             `json:"id"`
	Type string          `json:"type"`
	Time time.Time       `json:"time"`
	Data json.RawMessage `json:"data"`
}

// EventData contains the folder field common to relevant events.
type EventData struct {
	Folder string `json:"folder"`
}

type SyncthingClient struct {
	apiURL string
	apiKey string
	http   *http.Client
}

func NewSyncthingClient(apiURL, apiKey string) *SyncthingClient {
	return &SyncthingClient{
		apiURL: apiURL,
		apiKey: apiKey,
		http: &http.Client{
			Timeout: 90 * time.Second, // long-poll can block up to 60s server-side
		},
	}
}

// Subscribe polls /rest/events in a loop, sending events to the returned channel.
// It blocks until ctx is cancelled. Errors are logged and retried with backoff.
func (c *SyncthingClient) Subscribe(ctx context.Context) <-chan Event {
	ch := make(chan Event, 64)

	go func() {
		defer close(ch)

		var lastID int
		backoff := time.Second

		for {
			if ctx.Err() != nil {
				return
			}

			events, err := c.poll(ctx, lastID)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				if isFatalSyncthingError(err) {
					slog.Error("syncthing event poll failed with fatal configuration error",
						"classification", "fatal",
						"component", "syncthing",
						"error", err,
						"action", "exit",
					)
					return
				}
				slog.Warn("syncthing event poll failed", "error", err, "retry_in", backoff)
				select {
				case <-time.After(backoff):
				case <-ctx.Done():
					return
				}
				backoff = min(backoff*2, 30*time.Second)
				continue
			}

			backoff = time.Second

			for _, ev := range events {
				if ev.ID > lastID {
					lastID = ev.ID
				}
				select {
				case ch <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return ch
}

// systemStatus is the subset of /rest/system/status we need.
type systemStatus struct {
	MyID string `json:"myID"`
}

// CheckAPIReachable validates that the Syncthing API endpoint can be contacted.
// Any HTTP response status counts as reachable; transport errors do not.
func (c *SyncthingClient) CheckAPIReachable(ctx context.Context) error {
	_, _, err := c.getSystemStatus(ctx, false)
	if err == nil {
		return nil
	}

	var statusErr *HTTPStatusError
	if errors.As(err, &statusErr) {
		return nil
	}

	return err
}

// ValidateAPIKey verifies that the configured API key is accepted.
func (c *SyncthingClient) ValidateAPIKey(ctx context.Context) error {
	_, _, err := c.getSystemStatus(ctx, true)
	return err
}

// GetDeviceID reads this Syncthing instance's Device ID from /rest/system/status.
func (c *SyncthingClient) GetDeviceID(ctx context.Context) (string, error) {
	status, _, err := c.getSystemStatus(ctx, true)
	if err != nil {
		return "", err
	}

	if status.MyID == "" {
		return "", fmt.Errorf("empty device ID in system status")
	}

	return status.MyID, nil
}

func (c *SyncthingClient) getSystemStatus(ctx context.Context, withAPIKey bool) (systemStatus, int, error) {
	url := c.apiURL + "/rest/system/status"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return systemStatus{}, 0, fmt.Errorf("create request: %w", err)
	}
	if withAPIKey {
		req.Header.Set("X-API-Key", c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return systemStatus{}, 0, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return systemStatus{}, resp.StatusCode, &HTTPStatusError{
			Component:  "syncthing",
			URL:        url,
			StatusCode: resp.StatusCode,
			Body:       string(body),
		}
	}

	var status systemStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return systemStatus{}, resp.StatusCode, fmt.Errorf("decode status: %w", err)
	}

	return status, resp.StatusCode, nil
}

func (c *SyncthingClient) poll(ctx context.Context, since int) ([]Event, error) {
	url := fmt.Sprintf("%s/rest/events?since=%d&limit=100", c.apiURL, since)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("X-API-Key", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, &HTTPStatusError{
			Component:  "syncthing",
			URL:        url,
			StatusCode: resp.StatusCode,
			Body:       string(body),
		}
	}

	var events []Event
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return nil, fmt.Errorf("decode events: %w", err)
	}

	return events, nil
}

func isFatalSyncthingError(err error) bool {
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		return false
	}

	if statusErr.Component != "syncthing" {
		return false
	}

	return statusErr.StatusCode >= 400 &&
		statusErr.StatusCode < 500 &&
		statusErr.StatusCode != http.StatusTooManyRequests
}
