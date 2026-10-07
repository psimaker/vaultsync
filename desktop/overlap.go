package main

import (
	"encoding/xml"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// The agent's own overlap check, in front of the shared guard (decision 001,
// defense in depth): join.AcceptShare compares path strings, which cannot see
// that ~/notes and ~/Notes are one folder on macOS, or that a symlink points
// into a synced folder. Here the file system decides: symlinks are resolved,
// case is folded where the file system folds it, and existing directories are
// compared by identity.

// onDiskOverlap returns the first of folders that is the same directory as
// candidate, contains it, or lies inside it.
func onDiskOverlap(candidate string, folders []string, goos string) (string, bool) {
	cands := resolvedForms(candidate)
	for _, f := range folders {
		for _, fp := range resolvedForms(f) {
			for _, c := range cands {
				if overlapsFold(c, fp, foldCase(goos)) {
					return f, true
				}
			}
		}
		if sameOrNested(candidate, f) || sameOrNested(f, candidate) {
			return f, true
		}
	}
	return "", false
}

// sameOrNested reports whether inner, or an existing directory above it, is
// the very directory outer names (os.SameFile: device and inode). It catches
// spellings no string comparison can, such as Unicode normalization or a
// second mount of the same file system.
func sameOrNested(inner, outer string) bool {
	outerInfo, err := os.Stat(outer)
	if err != nil || !outerInfo.IsDir() {
		return false
	}
	p := filepath.Clean(inner)
	for {
		if info, err := os.Stat(p); err == nil && os.SameFile(info, outerInfo) {
			return true
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false
		}
		p = parent
	}
}

// userSyncthing is what the agent may learn, read-only, about a Syncthing the
// user runs on this computer: that it exists (so the agent stays off its
// default ports) and which folders it syncs (so no folder syncs twice). The
// agent never writes to it, starts it or stops it.
type userSyncthing struct {
	ConfigPath string
	Folders    []string
	// Unreadable is set when the config exists but cannot be parsed: the
	// agent then cannot rule out an overlap and refuses (fail closed).
	Unreadable error
}

func userSyncthingConfigs(goos, home string, getenv func(string) string) []string {
	switch goos {
	case "darwin":
		return []string{filepath.Join(home, "Library", "Application Support", "Syncthing", "config.xml")}
	case "linux":
		var out []string
		if s := getenv("XDG_STATE_HOME"); s != "" && filepath.IsAbs(s) {
			out = append(out, filepath.Join(s, "syncthing", "config.xml"))
		}
		out = append(out, filepath.Join(home, ".local", "state", "syncthing", "config.xml"))
		if c := getenv("XDG_CONFIG_HOME"); c != "" && filepath.IsAbs(c) {
			out = append(out, filepath.Join(c, "syncthing", "config.xml"))
		}
		return append(out, filepath.Join(home, ".config", "syncthing", "config.xml"))
	}
	return nil
}

// findUserSyncthing reads the first existing config.xml of the user's own
// Syncthing. A config it cannot read or parse still counts as "Syncthing is
// here", with Unreadable set.
func findUserSyncthing(goos, home string, getenv func(string) string) (userSyncthing, bool) {
	return findUserSyncthingIn(goos, home, getenv)
}

// findUserSyncthingIn is findUserSyncthing looking through the places every
// one of the environments names — the shell's as setup recorded it, and the
// process's own. Every config that exists is read and its folders count: a
// stale one in one place must not hide the one in use in another. One that
// cannot be read keeps Unreadable set (fail closed); ConfigPath names the
// first existing one, or the unreadable one.
func findUserSyncthingIn(goos, home string, envs ...func(string) string) (userSyncthing, bool) {
	var candidates []string
	for _, env := range envs {
		candidates = append(candidates, userSyncthingConfigs(goos, home, env)...)
	}
	var us userSyncthing
	found := false
	for _, p := range uniqStrings(candidates) {
		_, err := os.Stat(p)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			continue
		}
		if !found {
			us.ConfigPath = p
			found = true
		}
		if err == nil {
			var f *os.File
			if f, err = os.Open(p); err == nil {
				var cfg struct {
					Folders []struct {
						Path string `xml:"path,attr"`
					} `xml:"folder"`
				}
				err = xml.NewDecoder(io.LimitReader(f, 16<<20)).Decode(&cfg)
				f.Close()
				if err == nil {
					for _, fo := range cfg.Folders {
						path := strings.TrimSpace(fo.Path)
						if path == "~" || strings.HasPrefix(path, "~/") {
							path = filepath.Join(home, strings.TrimPrefix(path, "~"))
						}
						if path != "" {
							us.Folders = append(us.Folders, path)
						}
					}
				}
			}
		}
		if err != nil && us.Unreadable == nil {
			// Not "absent" — unreachable (permissions, I/O) or not parsable:
			// fail closed.
			us.Unreadable = err
			us.ConfigPath = p
		}
	}
	return us, found
}

// uniqStrings keeps the first of each value, in order.
func uniqStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
