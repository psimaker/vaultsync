// Package syncthing is the minimal REST client VaultSync's Go tools use to
// drive a Syncthing instance on loopback: the Hub drives its own Syncthing,
// a device drives the Syncthing it pairs (the vaultsync-hub `pair` command,
// the vaultsync desktop agent). Typed structs cover only the fields these
// tools read or write.
//
// Folders are never PATCHed — see FolderRaw for why.
package syncthing

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const maxResponseBytes = 8 << 20

// Client is a thin REST client bound to one API URL and key.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewClient builds a client. Redirects are refused: the API key must never
// follow a redirect to another host.
func NewClient(apiURL, apiKey string) *Client {
	c, _ := NewClientTLS(apiURL, apiKey, "")
	return c
}

// NewClientTLS builds a client that, when certPath is set, trusts exactly
// that PEM certificate (Syncthing's self-signed https-cert.pem next to
// config.xml) instead of the system roots. Used only for auto-detected TLS
// GUIs; an explicit SYNCTHING_API_URL keeps normal verification.
func NewClientTLS(apiURL, apiKey, certPath string) (*Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if certPath != "" {
		pem, err := os.ReadFile(certPath)
		if err != nil {
			return nil, fmt.Errorf("read Syncthing GUI certificate: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s holds no usable certificate", certPath)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &Client{
		baseURL: strings.TrimRight(apiURL, "/"),
		apiKey:  apiKey,
		http: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("redirects are not followed")
			},
		},
	}, nil
}

// --- config.xml -------------------------------------------------------------

// GUIConfig is the <gui> element of a Syncthing config.xml: where the REST API
// listens and the key it requires.
type GUIConfig struct {
	TLS     string `xml:"tls,attr"`
	Address string `xml:"address"`
	APIKey  string `xml:"apikey"`
}

type configXML struct {
	XMLName xml.Name  `xml:"configuration"`
	GUI     GUIConfig `xml:"gui"`
}

// ParseGUIConfig reads the <gui> element of a config.xml (at most 4 MiB).
func ParseGUIConfig(r io.Reader) (GUIConfig, error) {
	var cfg configXML
	if err := xml.NewDecoder(io.LimitReader(r, 4<<20)).Decode(&cfg); err != nil {
		return GUIConfig{}, err
	}
	return cfg.GUI, nil
}

// --- typed API subset -------------------------------------------------------

// DeviceConfig mirrors the fields of a Syncthing device entry VaultSync touches.
type DeviceConfig struct {
	DeviceID          string   `json:"deviceID"`
	Name              string   `json:"name"`
	Addresses         []string `json:"addresses"`
	Compression       string   `json:"compression"`
	Introducer        bool     `json:"introducer"`
	Paused            bool     `json:"paused"`
	AutoAcceptFolders bool     `json:"autoAcceptFolders"`
}

type FolderDevice struct {
	DeviceID string `json:"deviceID"`
}

type VersioningConfig struct {
	Type             string            `json:"type"`
	Params           map[string]string `json:"params"`
	CleanupIntervalS int               `json:"cleanupIntervalS"`
	FSPath           string            `json:"fsPath"`
	FSType           string            `json:"fsType"`
}

// FolderConfig mirrors the fields of a Syncthing folder entry VaultSync touches.
type FolderConfig struct {
	ID               string           `json:"id"`
	Label            string           `json:"label"`
	Path             string           `json:"path"`
	Type             string           `json:"type"`
	Devices          []FolderDevice   `json:"devices"`
	RescanIntervalS  int              `json:"rescanIntervalS"`
	FSWatcherEnabled bool             `json:"fsWatcherEnabled"`
	FSWatcherDelayS  int              `json:"fsWatcherDelayS"`
	IgnorePerms      bool             `json:"ignorePerms"`
	AutoNormalize    bool             `json:"autoNormalize"`
	MaxConflicts     int              `json:"maxConflicts"`
	Paused           bool             `json:"paused"`
	Versioning       VersioningConfig `json:"versioning"`
}

type systemStatus struct {
	MyID string `json:"myID"`
}

// DBStatus carries the per-folder database counters VaultSync reads.
type DBStatus struct {
	LocalFiles        int64  `json:"localFiles"`
	LocalDirectories  int64  `json:"localDirectories"`
	LocalSymlinks     int64  `json:"localSymlinks"`
	LocalBytes        int64  `json:"localBytes"`
	GlobalFiles       int64  `json:"globalFiles"`
	GlobalDirectories int64  `json:"globalDirectories"`
	GlobalSymlinks    int64  `json:"globalSymlinks"`
	State             string `json:"state"`
}

type connectionsResponse struct {
	Connections map[string]struct {
		Connected bool   `json:"connected"`
		Address   string `json:"address"`
	} `json:"connections"`
}

// Ping checks that the API answers with this key.
func (c *Client) Ping(ctx context.Context) error {
	var out struct {
		Ping string `json:"ping"`
	}
	return c.do(ctx, http.MethodGet, "/rest/system/ping", nil, &out)
}

// MyID returns the local device ID.
func (c *Client) MyID(ctx context.Context) (string, error) {
	var st systemStatus
	if err := c.do(ctx, http.MethodGet, "/rest/system/status", nil, &st); err != nil {
		return "", err
	}
	if st.MyID == "" {
		return "", errors.New("syncthing reported an empty device ID")
	}
	return st.MyID, nil
}

// Devices lists configured devices (including the local one).
func (c *Client) Devices(ctx context.Context) ([]DeviceConfig, error) {
	var out []DeviceConfig
	err := c.do(ctx, http.MethodGet, "/rest/config/devices", nil, &out)
	return out, err
}

// AddDevice creates a device entry. Syncthing rejects duplicates, so callers
// check Devices first for idempotent behaviour.
func (c *Client) AddDevice(ctx context.Context, d DeviceConfig) error {
	return c.do(ctx, http.MethodPost, "/rest/config/devices", d, nil)
}

// PatchDevice applies a partial update to one device entry.
func (c *Client) PatchDevice(ctx context.Context, deviceID string, patch map[string]any) error {
	return c.do(ctx, http.MethodPatch, "/rest/config/devices/"+url.PathEscape(deviceID), patch, nil)
}

// Folders lists configured folders.
func (c *Client) Folders(ctx context.Context) ([]FolderConfig, error) {
	var out []FolderConfig
	err := c.do(ctx, http.MethodGet, "/rest/config/folders", nil, &out)
	return out, err
}

// AddFolder creates a folder entry.
func (c *Client) AddFolder(ctx context.Context, f FolderConfig) error {
	return c.do(ctx, http.MethodPost, "/rest/config/folders", f, nil)
}

// FolderRaw returns one folder entry as the untyped JSON Syncthing serves.
//
// Folders are never PATCHed: Syncthing's FolderConfiguration.UnmarshalJSON
// resets every field with a default tag before decoding, so a partial update
// silently turns maxConflicts -1 back into 10 (verified against the pinned
// source and a live instance). Changes are read-modify-write on the raw
// object followed by PutFolderRaw, which preserves fields this client does not
// model.
func (c *Client) FolderRaw(ctx context.Context, folderID string) (map[string]any, error) {
	out := map[string]any{}
	err := c.do(ctx, http.MethodGet, "/rest/config/folders/"+url.PathEscape(folderID), nil, &out)
	return out, err
}

// PutFolderRaw replaces one folder entry with the full object.
func (c *Client) PutFolderRaw(ctx context.Context, folderID string, folder map[string]any) error {
	return c.do(ctx, http.MethodPut, "/rest/config/folders/"+url.PathEscape(folderID), folder, nil)
}

// PatchOptions applies a partial update to the global options.
func (c *Client) PatchOptions(ctx context.Context, patch map[string]any) error {
	return c.do(ctx, http.MethodPatch, "/rest/config/options", patch, nil)
}

// Options returns the global options as a generic map (callers inspect only a
// few keys and must not depend on the full schema).
func (c *Client) Options(ctx context.Context) (map[string]any, error) {
	out := map[string]any{}
	err := c.do(ctx, http.MethodGet, "/rest/config/options", nil, &out)
	return out, err
}

// PatchFolderDefaults applies a partial update to the folder defaults template.
func (c *Client) PatchFolderDefaults(ctx context.Context, patch map[string]any) error {
	return c.do(ctx, http.MethodPatch, "/rest/config/defaults/folder", patch, nil)
}

// DBStatus returns per-folder database counters.
func (c *Client) DBStatus(ctx context.Context, folderID string) (DBStatus, error) {
	var out DBStatus
	err := c.do(ctx, http.MethodGet, "/rest/db/status?folder="+url.QueryEscape(folderID), nil, &out)
	return out, err
}

// ConnectedDevices returns the IDs of currently connected devices.
func (c *Client) ConnectedDevices(ctx context.Context) (map[string]bool, error) {
	var out connectionsResponse
	if err := c.do(ctx, http.MethodGet, "/rest/system/connections", nil, &out); err != nil {
		return nil, err
	}
	res := map[string]bool{}
	for id, conn := range out.Connections {
		res[id] = conn.Connected
	}
	return res, nil
}

// PendingFolders returns folder ID → device IDs that offered it.
func (c *Client) PendingFolders(ctx context.Context) (map[string][]string, error) {
	var out map[string]struct {
		OfferedBy map[string]json.RawMessage `json:"offeredBy"`
	}
	if err := c.do(ctx, http.MethodGet, "/rest/cluster/pending/folders", nil, &out); err != nil {
		return nil, err
	}
	res := map[string][]string{}
	for id, entry := range out {
		for dev := range entry.OfferedBy {
			res[id] = append(res[id], dev)
		}
	}
	return res, nil
}

// PendingOffer is one device's offer of a folder this Syncthing has not
// accepted.
type PendingOffer struct {
	Label string `json:"label"`
}

// PendingOffers returns folder ID → offering device ID → offer, with the
// label each device gave the folder.
func (c *Client) PendingOffers(ctx context.Context) (map[string]map[string]PendingOffer, error) {
	var out map[string]struct {
		OfferedBy map[string]PendingOffer `json:"offeredBy"`
	}
	if err := c.do(ctx, http.MethodGet, "/rest/cluster/pending/folders", nil, &out); err != nil {
		return nil, err
	}
	res := map[string]map[string]PendingOffer{}
	for id, entry := range out {
		res[id] = entry.OfferedBy
	}
	return res, nil
}

// Get decodes the JSON answer of any GET endpoint into out — for the few
// read-only views (version, completion) the typed subset does not cover.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

// APIError carries the status code so callers can distinguish "not found"
// from a transport failure without string matching.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("syncthing API: HTTP %d: %s", e.Status, e.Body)
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		return &APIError{Status: resp.StatusCode, Body: msg}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode %s: %w", path, err)
		}
	}
	return nil
}
