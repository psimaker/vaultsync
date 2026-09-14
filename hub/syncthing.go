package main

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
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// The Hub talks to exactly one Syncthing instance — its own — over the REST
// API on loopback. Everything here is deliberately minimal: typed structs for
// the handful of fields the Hub reads or writes, and PATCH for every change so
// fields the Hub does not know about are never rewritten.

const maxResponseBytes = 8 << 20

// SyncthingClient is a thin REST client bound to one API URL and key.
type SyncthingClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewSyncthingClient builds a client. Redirects are refused: the API key must
// never follow a redirect to another host.
func NewSyncthingClient(apiURL, apiKey string) *SyncthingClient {
	c, _ := newSyncthingClientTLS(apiURL, apiKey, "")
	return c
}

// newSyncthingClientTLS builds a client that, when certPath is set, trusts
// exactly that PEM certificate (Syncthing's self-signed https-cert.pem next to
// config.xml) instead of the system roots. Used only for auto-detected TLS
// GUIs; an explicit SYNCTHING_API_URL keeps normal verification.
func newSyncthingClientTLS(apiURL, apiKey, certPath string) (*SyncthingClient, error) {
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
	return &SyncthingClient{
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

// --- config.xml discovery ---------------------------------------------------

// errNoSyncthingConfig marks "no readable config.xml yet" so a first-boot
// waiter can keep polling instead of failing on a race with Syncthing's own
// startup.
var errNoSyncthingConfig = errors.New("no Syncthing config.xml found")

type syncthingConfigXML struct {
	XMLName xml.Name `xml:"configuration"`
	GUI     struct {
		TLS     string `xml:"tls,attr"`
		Address string `xml:"address"`
		APIKey  string `xml:"apikey"`
	} `xml:"gui"`
}

// detectedSyncthing carries the values resolved from a config.xml.
type detectedSyncthing struct {
	APIKey string
	APIURL string
	Source string
	// CertPath is Syncthing's own GUI certificate when the detected GUI uses
	// TLS and no URL override is in effect; empty otherwise.
	CertPath string
}

// syncthingConfigCandidates lists where to look for config.xml, most specific
// first. Mirrors notify's probe order for the paths that matter to a Hub or a
// desktop with a stock Syncthing.
func syncthingConfigCandidates() []string {
	var paths []string
	if c := strings.TrimSpace(os.Getenv("SYNCTHING_CONFIG")); c != "" {
		paths = append(paths, c)
	}
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		if home != "" {
			paths = append(paths, filepath.Join(home, "Library", "Application Support", "Syncthing", "config.xml"))
		}
	case "windows":
		if la := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); la != "" {
			paths = append(paths, filepath.Join(la, "Syncthing", "config.xml"))
		}
	default:
		if xdg := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); xdg != "" {
			paths = append(paths, filepath.Join(xdg, "syncthing", "config.xml"))
		}
		if home != "" {
			paths = append(paths, filepath.Join(home, ".local", "state", "syncthing", "config.xml"))
		}
		if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
			paths = append(paths, filepath.Join(xdg, "syncthing", "config.xml"))
		}
		if home != "" {
			paths = append(paths, filepath.Join(home, ".config", "syncthing", "config.xml"))
		}
	}
	paths = append(paths,
		"/var/syncthing/config/config.xml", // official syncthing/syncthing image (STHOMEDIR)
		"/config/config.xml",               // linuxserver/syncthing image
		"/var/syncthing/config.xml",
		"/var/lib/syncthing/config.xml",
	)
	return paths
}

// detectSyncthing resolves API key and URL from the first readable config.xml.
// apiURLOverride wins over the address in the file (a containerised Hub reaches
// Syncthing on a published port, not the container-internal bind address).
func detectSyncthing(apiURLOverride string) (detectedSyncthing, error) {
	var lastErr error
	for _, p := range syncthingConfigCandidates() {
		f, err := os.Open(p)
		if err != nil {
			if !os.IsNotExist(err) {
				lastErr = fmt.Errorf("%s: %w", p, err)
			}
			continue
		}
		var cfg syncthingConfigXML
		err = xml.NewDecoder(io.LimitReader(f, 4<<20)).Decode(&cfg)
		f.Close()
		if err != nil {
			return detectedSyncthing{}, fmt.Errorf("%s: parse config.xml: %w", p, err)
		}
		if strings.TrimSpace(cfg.GUI.APIKey) == "" {
			// Syncthing writes config.xml before the GUI section is complete on
			// first boot; treat as not-yet-there so the caller retries.
			lastErr = fmt.Errorf("%s: %w (no apikey yet)", p, errNoSyncthingConfig)
			continue
		}
		d := detectedSyncthing{APIKey: strings.TrimSpace(cfg.GUI.APIKey), Source: p}
		if apiURLOverride != "" {
			d.APIURL = apiURLOverride
		} else {
			d.APIURL = guiURL(cfg.GUI.Address, cfg.GUI.TLS)
			if strings.EqualFold(cfg.GUI.TLS, "true") {
				d.CertPath = filepath.Join(filepath.Dir(p), "https-cert.pem")
			}
		}
		return d, nil
	}
	if lastErr != nil {
		return detectedSyncthing{}, lastErr
	}
	return detectedSyncthing{}, errNoSyncthingConfig
}

func guiURL(address, tls string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		address = "127.0.0.1:8384"
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		host, port = address, "8384"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	scheme := "http"
	if strings.EqualFold(tls, "true") {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

// --- typed API subset -------------------------------------------------------

// deviceConfig mirrors the fields of a Syncthing device entry the Hub touches.
type deviceConfig struct {
	DeviceID          string   `json:"deviceID"`
	Name              string   `json:"name"`
	Addresses         []string `json:"addresses"`
	Compression       string   `json:"compression"`
	Introducer        bool     `json:"introducer"`
	Paused            bool     `json:"paused"`
	AutoAcceptFolders bool     `json:"autoAcceptFolders"`
}

type folderDevice struct {
	DeviceID string `json:"deviceID"`
}

type versioningConfig struct {
	Type             string            `json:"type"`
	Params           map[string]string `json:"params"`
	CleanupIntervalS int               `json:"cleanupIntervalS"`
	FSPath           string            `json:"fsPath"`
	FSType           string            `json:"fsType"`
}

// folderConfig mirrors the fields of a Syncthing folder entry the Hub touches.
type folderConfig struct {
	ID               string           `json:"id"`
	Label            string           `json:"label"`
	Path             string           `json:"path"`
	Type             string           `json:"type"`
	Devices          []folderDevice   `json:"devices"`
	RescanIntervalS  int              `json:"rescanIntervalS"`
	FSWatcherEnabled bool             `json:"fsWatcherEnabled"`
	FSWatcherDelayS  int              `json:"fsWatcherDelayS"`
	IgnorePerms      bool             `json:"ignorePerms"`
	AutoNormalize    bool             `json:"autoNormalize"`
	MaxConflicts     int              `json:"maxConflicts"`
	Paused           bool             `json:"paused"`
	Versioning       versioningConfig `json:"versioning"`
}

type systemStatus struct {
	MyID string `json:"myID"`
}

type dbStatus struct {
	LocalFiles  int64  `json:"localFiles"`
	LocalBytes  int64  `json:"localBytes"`
	GlobalFiles int64  `json:"globalFiles"`
	State       string `json:"state"`
}

type connectionsResponse struct {
	Connections map[string]struct {
		Connected bool   `json:"connected"`
		Address   string `json:"address"`
	} `json:"connections"`
}

// Ping checks that the API answers with this key.
func (c *SyncthingClient) Ping(ctx context.Context) error {
	var out struct {
		Ping string `json:"ping"`
	}
	return c.do(ctx, http.MethodGet, "/rest/system/ping", nil, &out)
}

// MyID returns the local device ID.
func (c *SyncthingClient) MyID(ctx context.Context) (string, error) {
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
func (c *SyncthingClient) Devices(ctx context.Context) ([]deviceConfig, error) {
	var out []deviceConfig
	err := c.do(ctx, http.MethodGet, "/rest/config/devices", nil, &out)
	return out, err
}

// AddDevice creates a device entry. Syncthing rejects duplicates, so callers
// check Devices first for idempotent behaviour.
func (c *SyncthingClient) AddDevice(ctx context.Context, d deviceConfig) error {
	return c.do(ctx, http.MethodPost, "/rest/config/devices", d, nil)
}

// PatchDevice applies a partial update to one device entry.
func (c *SyncthingClient) PatchDevice(ctx context.Context, deviceID string, patch map[string]any) error {
	return c.do(ctx, http.MethodPatch, "/rest/config/devices/"+url.PathEscape(deviceID), patch, nil)
}

// Folders lists configured folders.
func (c *SyncthingClient) Folders(ctx context.Context) ([]folderConfig, error) {
	var out []folderConfig
	err := c.do(ctx, http.MethodGet, "/rest/config/folders", nil, &out)
	return out, err
}

// AddFolder creates a folder entry.
func (c *SyncthingClient) AddFolder(ctx context.Context, f folderConfig) error {
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
func (c *SyncthingClient) FolderRaw(ctx context.Context, folderID string) (map[string]any, error) {
	out := map[string]any{}
	err := c.do(ctx, http.MethodGet, "/rest/config/folders/"+url.PathEscape(folderID), nil, &out)
	return out, err
}

// PutFolderRaw replaces one folder entry with the full object.
func (c *SyncthingClient) PutFolderRaw(ctx context.Context, folderID string, folder map[string]any) error {
	return c.do(ctx, http.MethodPut, "/rest/config/folders/"+url.PathEscape(folderID), folder, nil)
}

// PatchOptions applies a partial update to the global options.
func (c *SyncthingClient) PatchOptions(ctx context.Context, patch map[string]any) error {
	return c.do(ctx, http.MethodPatch, "/rest/config/options", patch, nil)
}

// Options returns the global options as a generic map (the Hub inspects only a
// few keys and must not depend on the full schema).
func (c *SyncthingClient) Options(ctx context.Context) (map[string]any, error) {
	out := map[string]any{}
	err := c.do(ctx, http.MethodGet, "/rest/config/options", nil, &out)
	return out, err
}

// PatchFolderDefaults applies a partial update to the folder defaults template.
func (c *SyncthingClient) PatchFolderDefaults(ctx context.Context, patch map[string]any) error {
	return c.do(ctx, http.MethodPatch, "/rest/config/defaults/folder", patch, nil)
}

// DBStatus returns per-folder database counters.
func (c *SyncthingClient) DBStatus(ctx context.Context, folderID string) (dbStatus, error) {
	var out dbStatus
	err := c.do(ctx, http.MethodGet, "/rest/db/status?folder="+url.QueryEscape(folderID), nil, &out)
	return out, err
}

// ConnectedDevices returns the IDs of currently connected devices.
func (c *SyncthingClient) ConnectedDevices(ctx context.Context) (map[string]bool, error) {
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

// apiError carries the status code so callers can distinguish "not found"
// from a transport failure without string matching.
type apiError struct {
	Status int
	Body   string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("syncthing API: HTTP %d: %s", e.Status, e.Body)
}

func (c *SyncthingClient) do(ctx context.Context, method, path string, in, out any) error {
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
		return &apiError{Status: resp.StatusCode, Body: msg}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode %s: %w", path, err)
		}
	}
	return nil
}

// waitForSyncthing polls until config.xml is readable and the API answers, or
// the deadline passes. On a fresh Hub the Syncthing container writes its config
// a few seconds after start; the Hub must ride that out instead of crashing.
func waitForSyncthing(ctx context.Context, apiURLOverride string, timeout time.Duration, log func(string)) (*SyncthingClient, detectedSyncthing, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	announced := false
	for {
		det, err := detectSyncthing(apiURLOverride)
		if err == nil {
			var client *SyncthingClient
			client, err = newSyncthingClientTLS(det.APIURL, det.APIKey, det.CertPath)
			if err == nil {
				if perr := client.Ping(ctx); perr == nil {
					return client, det, nil
				} else {
					err = perr
				}
			}
		}
		lastErr = err
		if !announced && log != nil {
			log("waiting for Syncthing to come up…")
			announced = true
		}
		if time.Now().After(deadline) {
			return nil, detectedSyncthing{}, fmt.Errorf("syncthing not reachable after %s: %w", timeout, lastErr)
		}
		select {
		case <-ctx.Done():
			return nil, detectedSyncthing{}, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// PendingFolders returns folder ID → device IDs that offered it.
func (c *SyncthingClient) PendingFolders(ctx context.Context) (map[string][]string, error) {
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
