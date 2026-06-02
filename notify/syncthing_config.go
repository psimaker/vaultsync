package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// syncthingConfig is the minimal subset of Syncthing's config.xml we read: the
// <gui> block, which holds the REST API key and the address/TLS the API is
// served on.
//
// We parse with encoding/xml rather than a line grep on purpose: the element
// path <configuration><gui><apikey|address> is resolved precisely, so we never
// mistake a <device><address>dynamic</address> (which appears before <gui> in
// document order) for the GUI address. A naive "first <address> in the file"
// scan does exactly that.
//
// Syncthing's schema has exactly one <gui> element; if a hand-edited file ever
// contained several, encoding/xml binds this single (non-slice) field to one of
// them deterministically — we deliberately read only the gui apikey/address/tls
// and nothing else (no <password>, no folder <encryptionPassword>).
type syncthingConfig struct {
	XMLName xml.Name `xml:"configuration"`
	GUI     struct {
		TLS     string `xml:"tls,attr"`
		Address string `xml:"address"`
		APIKey  string `xml:"apikey"`
	} `xml:"gui"`
}

// detectedSyncthing carries the values resolved from a Syncthing config.xml.
type detectedSyncthing struct {
	APIKey string
	APIURL string
	Source string // the config.xml path the values came from
}

// syncthingConfigCandidatesFn returns the config.xml paths to probe, in
// priority order. It is a package var so tests can make detection deterministic
// (the same pattern used by inactiveRecheckInterval).
var syncthingConfigCandidatesFn = platformSyncthingConfigCandidates

// platformSyncthingConfigCandidates lists where to look for Syncthing's
// config.xml, most specific first:
//   - $SYNCTHING_CONFIG (explicit override) wins outright.
//   - the per-OS user default location.
//   - container / system-service locations, included on every OS so a Linux
//     helper running in Docker finds a volume-mounted config even when HOME is
//     unset or points somewhere odd.
func platformSyncthingConfigCandidates() []string {
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
	default: // linux and other unixes
		// Syncthing 1.27+ moved the default config dir to $XDG_STATE_HOME/syncthing
		// (~/.local/state/syncthing); older installs still use $XDG_CONFIG_HOME/
		// syncthing (~/.config/syncthing) and are NOT auto-migrated. Probe the
		// current default first, then the legacy one.
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

	// Container images and system-service layouts. Loopback-only inside the box,
	// so reading these is purely local. The official syncthing/syncthing image
	// sets STHOMEDIR=/var/syncthing/config, so its config lives one level below
	// the /var/syncthing volume; linuxserver/syncthing uses /config.
	paths = append(paths,
		"/var/syncthing/config/config.xml", // official syncthing/syncthing image (STHOMEDIR)
		"/config/config.xml",               // linuxserver/syncthing image (Unraid CA default)
		"/var/syncthing/config.xml",        // older official image tags / custom HOME layout
		"/var/lib/syncthing/config.xml",    // some systemd system services
		"/etc/syncthing/config.xml",        // some distro packages
	)

	return paths
}

// detectSyncthingFromConfig auto-detects the Syncthing API key and URL from the
// first readable config.xml among the candidate paths. Reading is purely local
// (a file on disk) — no network, nothing leaves the server.
func detectSyncthingFromConfig() (detectedSyncthing, error) {
	return detectSyncthingFromCandidates(syncthingConfigCandidatesFn())
}

func detectSyncthingFromCandidates(candidates []string) (detectedSyncthing, error) {
	var probed []string
	// A candidate that exists but is unreadable (config.xml is mode 0600, owned
	// by Syncthing's user — PUID, default 1000 in the Docker images — and lives
	// in a 0700 dir). Remember the denial but keep probing: another candidate
	// may be readable. Surfaced only if nothing else works, so its uid-specific
	// fix doesn't mask a config we could actually read. Wraps os.ErrPermission
	// so loadConfig can detect and surface it directly.
	var permErr error
	for _, p := range candidates {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		probed = append(probed, p)

		info, err := os.Stat(p)
		if err != nil {
			if os.IsPermission(err) {
				permErr = permissionDeniedError(p, err)
			}
			continue // not present / not accessible here; try the next candidate
		}
		if info.IsDir() {
			continue
		}

		cfg, err := parseSyncthingConfig(p)
		if err != nil {
			if os.IsPermission(err) {
				permErr = permissionDeniedError(p, err)
				continue
			}
			// The file exists but we can't make sense of it. Surface this rather
			// than silently skipping — a corrupt config is worth telling the
			// operator about, with the path so they know which file to fix.
			return detectedSyncthing{}, fmt.Errorf("found Syncthing config at %s but could not read it: %w", p, err)
		}

		key := strings.TrimSpace(cfg.GUI.APIKey)
		if key == "" {
			return detectedSyncthing{}, fmt.Errorf("Syncthing config at %s has no GUI API key (enable it in Syncthing: Actions → Settings → GUI), or set SYNCTHING_API_KEY explicitly", p)
		}

		return detectedSyncthing{
			APIKey: key,
			APIURL: inferSyncthingURL(cfg.GUI.Address, strings.EqualFold(strings.TrimSpace(cfg.GUI.TLS), "true")),
			Source: p,
		}, nil
	}

	if permErr != nil {
		return detectedSyncthing{}, permErr
	}
	if len(probed) == 0 {
		return detectedSyncthing{}, fmt.Errorf("no Syncthing config.xml location to probe; set SYNCTHING_CONFIG to its path, or set SYNCTHING_API_KEY and SYNCTHING_API_URL explicitly")
	}
	return detectedSyncthing{}, fmt.Errorf("no Syncthing config.xml found (looked in: %s); set SYNCTHING_CONFIG to its path, or set SYNCTHING_API_KEY and SYNCTHING_API_URL explicitly", strings.Join(probed, ", "))
}

// permissionDeniedError wraps os.ErrPermission (so errors.Is works upstream) and
// spells out the uid-match fix specific to Syncthing's 0600 config.xml.
func permissionDeniedError(path string, err error) error {
	return fmt.Errorf("found Syncthing config at %s but cannot read it: %w — run the helper as the user that owns config.xml (the Docker images default to uid 1000), or set SYNCTHING_API_KEY/SYNCTHING_API_URL explicitly", path, err)
}

func parseSyncthingConfig(path string) (syncthingConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return syncthingConfig{}, err
	}
	defer f.Close()

	// config.xml is normally a few KB. Cap the read so a misconfigured
	// SYNCTHING_CONFIG pointing at a huge file can't exhaust memory.
	const maxConfigBytes = 16 << 20 // 16 MiB
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes))
	if err != nil {
		return syncthingConfig{}, err
	}

	var cfg syncthingConfig
	if err := xml.Unmarshal(data, &cfg); err != nil {
		return syncthingConfig{}, fmt.Errorf("parse XML: %w", err)
	}
	return cfg, nil
}

// inferSyncthingURL turns a Syncthing GUI <address> + tls flag into a reachable
// REST API base URL. Wildcard/bare-port listen addresses are normalized to
// loopback (correct on the same host or with `--network host`; a separate
// container reaches Syncthing by service name and should set SYNCTHING_API_URL
// explicitly).
func inferSyncthingURL(address string, tls bool) string {
	scheme := "http"
	if tls {
		scheme = "https"
	}

	address = strings.TrimSpace(address)
	if address == "" {
		return scheme + "://127.0.0.1:8384"
	}
	if strings.HasPrefix(address, "http://") || strings.HasPrefix(address, "https://") {
		return address
	}

	host, port, err := net.SplitHostPort(address)
	if err != nil {
		// Not a host:port (e.g. a unix socket path or an unexpected value).
		// Fall back to the loopback default; the operator can override.
		return scheme + "://127.0.0.1:8384"
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	if port == "" {
		port = "8384"
	}
	// Reject an out-of-range port rather than build a malformed URL (Syncthing
	// validates this itself, but a hand-edited config shouldn't slip through).
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return scheme + "://127.0.0.1:8384"
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}
