package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Obsidian records every vault it has opened in obsidian.json:
//
//	{"vaults": {"<16 hex>": {"path": "/Users/me/Notes", "ts": 1759730000000, "open": true}}}
//
// ts is when the vault was last opened (ms since the epoch). The agent offers
// these vaults newest-opened first; a bounded scan for .obsidian/ folders is
// the fallback when no registry lists any.

type localVault struct {
	Path     string
	Opened   time.Time // zero when found by the scan
	FromScan bool
}

// obsidianRegistries lists where Obsidian keeps obsidian.json on goos: the
// native install first, then the Linux sandboxes (Flatpak, Snap).
func obsidianRegistries(goos, home string, getenv func(string) string) []string {
	switch goos {
	case "darwin":
		return []string{filepath.Join(home, "Library", "Application Support", "obsidian", "obsidian.json")}
	case "linux":
		config := getenv("XDG_CONFIG_HOME")
		if config == "" || !filepath.IsAbs(config) {
			config = filepath.Join(home, ".config")
		}
		return []string{
			filepath.Join(config, "obsidian", "obsidian.json"),
			// Flatpak gives the app its own XDG_CONFIG_HOME under ~/.var/app.
			filepath.Join(home, ".var", "app", "md.obsidian.Obsidian", "config", "obsidian", "obsidian.json"),
			// Snap runs the app with HOME=~/snap/obsidian/<revision>.
			filepath.Join(home, "snap", "obsidian", "current", ".config", "obsidian", "obsidian.json"),
		}
	case "windows":
		appData := getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		return []string{filepath.Join(appData, "obsidian", "obsidian.json")}
	}
	return nil
}

// readObsidianRegistry parses one obsidian.json. A missing file is no error.
func readObsidianRegistry(path string) ([]localVault, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var reg struct {
		Vaults map[string]struct {
			Path string `json:"path"`
			TS   int64  `json:"ts"`
		} `json:"vaults"`
	}
	if err := json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&reg); err != nil {
		return nil, err
	}
	var out []localVault
	for _, v := range reg.Vaults {
		p := strings.TrimSpace(v.Path)
		if p == "" {
			continue
		}
		var opened time.Time
		if v.TS > 0 {
			opened = time.UnixMilli(v.TS)
		}
		out = append(out, localVault{Path: p, Opened: opened})
	}
	return out, nil
}

// knownVaults merges every registry: one entry per path (the newest opening
// wins), newest-opened first. Unreadable registries are reported, not fatal.
func knownVaults(registries []string) ([]localVault, []error) {
	byPath := map[string]localVault{}
	var errs []error
	for _, r := range registries {
		vaults, err := readObsidianRegistry(r)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, v := range vaults {
			if old, ok := byPath[v.Path]; !ok || v.Opened.After(old.Opened) {
				byPath[v.Path] = v
			}
		}
	}
	out := make([]localVault, 0, len(byPath))
	for _, v := range byPath {
		out = append(out, v)
	}
	sortVaults(out)
	return out, errs
}

func sortVaults(v []localVault) {
	sort.SliceStable(v, func(i, j int) bool {
		if !v[i].Opened.Equal(v[j].Opened) {
			return v[i].Opened.After(v[j].Opened)
		}
		return v[i].Path < v[j].Path
	})
}

// scanLimits bounds the fallback scan: it must answer in moments, also in a
// home directory with a million files.
type scanLimits struct {
	depth    int // levels below each root
	maxDirs  int
	deadline time.Time
}

// scanForVaults looks below roots for folders that hold an .obsidian folder.
// It never descends into hidden folders, into a vault, or into any path skip
// rejects (cloud folders: listing them can download every placeholder).
func scanForVaults(roots []string, skip func(string) bool, lim scanLimits) []localVault {
	seen := map[string]bool{}
	var out []localVault
	visited := 0
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if visited >= lim.maxDirs || (!lim.deadline.IsZero() && time.Now().After(lim.deadline)) {
			return
		}
		visited++
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.Name() == ".obsidian" && e.IsDir() {
				if !seen[dir] {
					seen[dir] = true
					out = append(out, localVault{Path: dir, FromScan: true})
				}
				return // a vault's own subfolders are notes, not vaults
			}
		}
		if depth >= lim.depth {
			return
		}
		for _, e := range entries {
			name := e.Name()
			if !e.IsDir() || strings.HasPrefix(name, ".") || skipScanName[name] {
				continue
			}
			child := filepath.Join(dir, name)
			if skip != nil && skip(child) {
				continue
			}
			walk(child, depth+1)
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}
	sortVaults(out)
	return out
}

// skipScanName are folders that never hold a vault but can hold a lot.
var skipScanName = map[string]bool{
	"Library":      true, // macOS: app data, caches, iCloud Drive's backing store
	"Applications": true,
	"node_modules": true,
	"snap":         true,
	"go":           true,
	"Pictures":     true,
	"Music":        true,
	"Movies":       true,
	"Videos":       true,
}
