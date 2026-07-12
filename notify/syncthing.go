package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
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
						"error_kind", operationalErrorKind(err),
						"action", "exit",
					)
					return
				}
				slog.Warn("syncthing event poll failed", "error_kind", operationalErrorKind(err), "retry_in", backoff)
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
		return systemStatus{}, resp.StatusCode, &HTTPStatusError{
			Component:  "syncthing",
			StatusCode: resp.StatusCode,
		}
	}

	var status systemStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return systemStatus{}, resp.StatusCode, fmt.Errorf("decode status: %w", err)
	}

	return status, resp.StatusCode, nil
}

// remoteDeviceConfig is the subset of /rest/config/devices needed to decide
// which peers to include in the stale-peer sweep.
type remoteDeviceConfig struct {
	DeviceID string `json:"deviceID"`
	Paused   bool   `json:"paused"`
}

// deviceConnection is the subset of a /rest/system/connections entry the
// doctor's peer-state checks need.
type deviceConnection struct {
	Connected bool `json:"connected"`
}

// folderConfig is the subset of /rest/config/folders needed to decide whether
// a folder is shared with a given device. Devices always includes the local
// device itself.
type folderConfig struct {
	Devices []struct {
		DeviceID string `json:"deviceID"`
	} `json:"devices"`
}

// Connections returns the connection state of every configured device, keyed
// by device ID. The map includes the local device itself (always as not
// connected) — callers must exclude it.
func (c *SyncthingClient) Connections(ctx context.Context) (map[string]deviceConnection, error) {
	var out struct {
		Connections map[string]deviceConnection `json:"connections"`
	}
	if err := c.getJSON(ctx, c.apiURL+"/rest/system/connections", &out); err != nil {
		return nil, err
	}
	return out.Connections, nil
}

// ListFolders returns all configured folders with their shared-device lists.
func (c *SyncthingClient) ListFolders(ctx context.Context) ([]folderConfig, error) {
	var folders []folderConfig
	if err := c.getJSON(ctx, c.apiURL+"/rest/config/folders", &folders); err != nil {
		return nil, err
	}
	return folders, nil
}

// DeviceCompletion is the subset of /rest/db/completion used to decide whether
// a peer still needs data from this instance.
type DeviceCompletion struct {
	NeedBytes   int64 `json:"needBytes"`
	NeedItems   int   `json:"needItems"`
	NeedDeletes int   `json:"needDeletes"`
}

// ListDevices returns all configured devices, including this one.
func (c *SyncthingClient) ListDevices(ctx context.Context) ([]remoteDeviceConfig, error) {
	var devices []remoteDeviceConfig
	if err := c.getJSON(ctx, c.apiURL+"/rest/config/devices", &devices); err != nil {
		return nil, err
	}
	return devices, nil
}

// Completion reads sync completion for a device. An empty folder aggregates
// across all folders shared with that device.
func (c *SyncthingClient) Completion(ctx context.Context, deviceID, folder string) (DeviceCompletion, error) {
	query := url.Values{"device": {deviceID}}
	if folder != "" {
		query.Set("folder", folder)
	}

	var completion DeviceCompletion
	if err := c.getJSON(ctx, c.apiURL+"/rest/db/completion?"+query.Encode(), &completion); err != nil {
		return DeviceCompletion{}, err
	}
	return completion, nil
}

func (c *SyncthingClient) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("X-API-Key", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return &HTTPStatusError{
			Component:  "syncthing",
			StatusCode: resp.StatusCode,
		}
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
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
		return nil, &HTTPStatusError{
			Component:  "syncthing",
			StatusCode: resp.StatusCode,
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
