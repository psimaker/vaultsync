package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Cloud folders are a hard block, not a warning (#175): iCloud Drive,
// OneDrive, Dropbox, Google Drive and Nextcloud replace files they have not
// downloaded with placeholders, and Syncthing reads a placeholder that goes
// away as a deletion — which it then sends to every device. A vault inside
// such a folder, or one that contains such a folder, is never synced.

type cloudRoot struct {
	Path     string
	Provider string
}

// cloudEnv is where to look; tests point every part of it into a fresh
// directory.
type cloudEnv struct {
	goos       string
	home       string
	getenv     func(string) string
	volumes    string // macOS: /Volumes (legacy Google Drive File Stream)
	runtimeDir string // Linux: $XDG_RUNTIME_DIR, where GNOME's online accounts mount (gvfs)
}

func liveCloudEnv(goos, home string) cloudEnv {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" && goos == "linux" {
		runtimeDir = "/run/user/" + strconv.Itoa(os.Getuid())
	}
	return cloudEnv{goos: goos, home: home, getenv: os.Getenv, volumes: "/Volumes", runtimeDir: runtimeDir}
}

// homePrefixes are folder names a cloud client creates in the home folder
// (also as "Dropbox (Personal)", "OneDrive - Contoso", "Nextcloud2" …).
var homePrefixes = []struct{ prefix, provider string }{
	{"Dropbox", "Dropbox"},
	{"OneDrive", "OneDrive"},
	{"Google Drive", "Google Drive"},
	{"GoogleDrive", "Google Drive"},
	{"Nextcloud", "Nextcloud"},
	{"iCloud Drive", "iCloud Drive"},
	{"iCloudDrive", "iCloud Drive"},
}

// cloudRoots lists the folders cloud clients manage on this computer: their
// default places plus every folder their own settings name.
func cloudRoots(env cloudEnv) []cloudRoot {
	var roots []cloudRoot
	add := func(path, provider string) {
		if path != "" {
			roots = append(roots, cloudRoot{Path: filepath.Clean(path), Provider: provider})
		}
	}
	home := env.home
	if entries, err := os.ReadDir(home); err == nil {
		for _, e := range entries {
			for _, p := range homePrefixes {
				if strings.HasPrefix(e.Name(), p.prefix) {
					add(filepath.Join(home, e.Name()), p.provider)
				}
			}
		}
	}
	for _, p := range dropboxPaths(filepath.Join(home, ".dropbox", "info.json")) {
		add(p, "Dropbox")
	}
	config := env.getenv("XDG_CONFIG_HOME")
	if config == "" || !filepath.IsAbs(config) {
		config = filepath.Join(home, ".config")
	}
	switch env.goos {
	case "darwin":
		mobile := filepath.Join(home, "Library", "Mobile Documents")
		add(mobile, "iCloud Drive")
		// "Desktop & Documents Folders" in iCloud: iCloud Drive then holds a
		// Documents entry for the home folder's Documents, and Desktop moves
		// with it (one setting covers both).
		if _, err := os.Lstat(filepath.Join(mobile, "com~apple~CloudDocs", "Documents")); err == nil {
			add(filepath.Join(home, "Documents"), "iCloud Drive")
			add(filepath.Join(home, "Desktop"), "iCloud Drive")
		}
		// Every File Provider cloud (OneDrive, Dropbox, Google Drive, Box,
		// Nextcloud with virtual files …) mounts below ~/Library/CloudStorage.
		storage := filepath.Join(home, "Library", "CloudStorage")
		add(storage, "a cloud drive")
		if entries, err := os.ReadDir(storage); err == nil {
			for _, e := range entries {
				add(filepath.Join(storage, e.Name()), fileProviderName(e.Name()))
			}
		}
		if entries, err := os.ReadDir(env.volumes); err == nil {
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "GoogleDrive") {
					add(filepath.Join(env.volumes, e.Name()), "Google Drive")
				}
			}
		}
		for _, cfg := range []string{
			filepath.Join(home, "Library", "Preferences", "Nextcloud", "nextcloud.cfg"),
			filepath.Join(home, "Library", "Containers", "com.nextcloud.desktopclient", "Data", "Library", "Preferences", "Nextcloud", "nextcloud.cfg"),
		} {
			for _, p := range nextcloudPaths(cfg) {
				add(p, "Nextcloud")
			}
		}
	case "linux":
		for _, cfg := range []string{
			filepath.Join(config, "Nextcloud", "nextcloud.cfg"),
			filepath.Join(home, ".var", "app", "com.nextcloud.desktopclient.nextcloud", "config", "Nextcloud", "nextcloud.cfg"),
		} {
			for _, p := range nextcloudPaths(cfg) {
				add(p, "Nextcloud")
			}
		}
		// The usual OneDrive client for Linux (abraunegg/onedrive).
		if p := oneDriveLinuxSyncDir(filepath.Join(config, "onedrive", "config"), home); p != "" {
			add(p, "OneDrive")
		}
		// GNOME Online Accounts mount Google Drive (and other online
		// storage) through gvfs.
		if env.runtimeDir != "" {
			add(filepath.Join(env.runtimeDir, "gvfs"), "Google Drive or another online account")
		}
	case "windows":
		for _, v := range []string{"OneDrive", "OneDriveConsumer", "OneDriveCommercial"} {
			add(env.getenv(v), "OneDrive")
		}
		for _, base := range []string{env.getenv("LOCALAPPDATA"), env.getenv("APPDATA")} {
			if base != "" {
				for _, p := range dropboxPaths(filepath.Join(base, "Dropbox", "info.json")) {
					add(p, "Dropbox")
				}
			}
		}
		if appData := env.getenv("APPDATA"); appData != "" {
			for _, p := range nextcloudPaths(filepath.Join(appData, "Nextcloud", "nextcloud.cfg")) {
				add(p, "Nextcloud")
			}
		}
	}
	return roots
}

func fileProviderName(dir string) string {
	switch {
	case strings.HasPrefix(dir, "OneDrive"):
		return "OneDrive"
	case strings.HasPrefix(dir, "Dropbox"):
		return "Dropbox"
	case strings.HasPrefix(dir, "GoogleDrive"):
		return "Google Drive"
	case strings.HasPrefix(dir, "Nextcloud"):
		return "Nextcloud"
	case strings.HasPrefix(dir, "iCloud"):
		return "iCloud Drive"
	}
	return "a cloud drive"
}

// dropboxPaths reads the folders a Dropbox client syncs from its info.json
// ({"personal": {"path": …}, "business": {"path": …}}).
func dropboxPaths(infoJSON string) []string {
	data, err := readSmallFile(infoJSON)
	if err != nil {
		return nil
	}
	var info map[string]struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(data, &info) != nil {
		return nil
	}
	var out []string
	for _, account := range info {
		if account.Path != "" {
			out = append(out, account.Path)
		}
	}
	return out
}

// nextcloudPaths reads every synced folder from a Nextcloud client's
// nextcloud.cfg (Qt INI: "0\Folders\1\localPath=/Users/me/Nextcloud/").
func nextcloudPaths(cfg string) []string {
	data, err := readSmallFile(cfg)
	if err != nil {
		return nil
	}
	var out []string
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok || !(strings.HasSuffix(key, `\localPath`) || strings.HasSuffix(key, "/localPath")) {
			continue
		}
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

// oneDriveLinuxSyncDir reads sync_dir from the onedrive client's config; the
// client's default is ~/OneDrive (already covered by the home prefixes).
func oneDriveLinuxSyncDir(cfg, home string) string {
	data, err := readSmallFile(cfg)
	if err != nil {
		return ""
	}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "sync_dir" {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if value == "~" || strings.HasPrefix(value, "~/") {
			value = filepath.Join(home, strings.TrimPrefix(value, "~"))
		}
		return value
	}
	return ""
}

func readSmallFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 1<<20))
}

// cloudBlock reports the cloud folder path lies in, or that lies inside path
// — the most specific one when several match (OneDrive rather than "a cloud
// drive" for ~/Library/CloudStorage/OneDrive-Personal). Both the path as
// given and its resolved form are checked, so a symlink into a cloud folder
// is caught; macOS and Windows compare without regard to case, like their
// file systems.
func cloudBlock(path string, roots []cloudRoot, goos string) (cloudRoot, bool) {
	paths := resolvedForms(path)
	var best cloudRoot
	found := false
	for _, r := range roots {
		for _, rp := range resolvedForms(r.Path) {
			for _, p := range paths {
				if overlapsFold(p, rp, foldCase(goos)) && (!found || len(r.Path) > len(best.Path)) {
					best, found = r, true
				}
			}
		}
	}
	return best, found
}

// resolvedForms is path cleaned, plus — when it differs — the path with every
// symlink of its longest existing prefix resolved.
func resolvedForms(path string) []string {
	clean := filepath.Clean(path)
	out := []string{clean}
	if r := resolveExistingPrefix(clean); r != clean {
		out = append(out, r)
	}
	return out
}

// resolveExistingPrefix resolves symlinks in the longest prefix of path that
// exists and appends the rest unchanged — a folder that does not exist yet
// still lands where its parent really is.
func resolveExistingPrefix(path string) string {
	var rest []string
	p := path
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(append([]string{r}, rest...)...)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return path
		}
		rest = append([]string{filepath.Base(p)}, rest...)
		p = parent
	}
}

func foldCase(goos string) bool { return goos == "darwin" || goos == "windows" }

// overlapsFold is join.PathsOverlap, optionally without regard to case.
func overlapsFold(a, b string, fold bool) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if fold {
		a, b = strings.ToLower(a), strings.ToLower(b)
	}
	if a == b {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(a, strings.TrimSuffix(b, sep)+sep) || strings.HasPrefix(b, strings.TrimSuffix(a, sep)+sep)
}
