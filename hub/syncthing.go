package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/psimaker/vaultsync/hub/syncthing"
)

// The Hub talks to exactly one Syncthing instance — its own — over the REST
// API on loopback. The client lives in the syncthing package, which the
// device side (the `pair` command, the vaultsync desktop agent) shares; this
// file keeps how the Hub finds its own instance.

type (
	SyncthingClient  = syncthing.Client
	deviceConfig     = syncthing.DeviceConfig
	folderDevice     = syncthing.FolderDevice
	versioningConfig = syncthing.VersioningConfig
	folderConfig     = syncthing.FolderConfig
	apiError         = syncthing.APIError
)

// NewSyncthingClient builds a client (syncthing.NewClient): redirects are
// refused so the API key never follows one to another host.
func NewSyncthingClient(apiURL, apiKey string) *SyncthingClient {
	return syncthing.NewClient(apiURL, apiKey)
}

// newSyncthingClientTLS trusts exactly Syncthing's own GUI certificate when
// certPath is set (syncthing.NewClientTLS).
func newSyncthingClientTLS(apiURL, apiKey, certPath string) (*SyncthingClient, error) {
	return syncthing.NewClientTLS(apiURL, apiKey, certPath)
}

// --- config.xml discovery ---------------------------------------------------

// errNoSyncthingConfig marks "no readable config.xml yet" so a first-boot
// waiter can keep polling instead of failing on a race with Syncthing's own
// startup.
var errNoSyncthingConfig = errors.New("no Syncthing config.xml found")

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
		gui, err := syncthing.ParseGUIConfig(f)
		f.Close()
		if err != nil {
			return detectedSyncthing{}, fmt.Errorf("%s: parse config.xml: %w", p, err)
		}
		if strings.TrimSpace(gui.APIKey) == "" {
			// Syncthing writes config.xml before the GUI section is complete on
			// first boot; treat as not-yet-there so the caller retries.
			lastErr = fmt.Errorf("%s: %w (no apikey yet)", p, errNoSyncthingConfig)
			continue
		}
		d := detectedSyncthing{APIKey: strings.TrimSpace(gui.APIKey), Source: p}
		if apiURLOverride != "" {
			d.APIURL = apiURLOverride
		} else {
			d.APIURL = guiURL(gui.Address, gui.TLS)
			if strings.EqualFold(gui.TLS, "true") {
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
