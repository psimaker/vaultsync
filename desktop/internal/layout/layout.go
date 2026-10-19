// Package layout is everything VaultSync keeps on this computer, for the
// agent that keeps it and the menu-bar app that reads it: one answer to
// where VaultSync's folder, the control socket and the service's log are.
// Vaults are never in here: they stay where the user keeps them.
package layout

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

type Layout struct {
	Base      string // one directory, 0700
	Bin       string // Base/bin
	Agent     string // Base/bin/vaultsync — the copy the background service runs
	Syncthing string // Base/bin/syncthing — the pinned engine
	Home      string // Base/syncthing — the engine's identity, config and database
	State     string // Base/agent.json
	Lock      string // Base/agent.lock — held by the one running engine owner
	Socket    string // Base/agent.sock — the running agent's control socket
	Logs      string // where the service's own output goes (empty: the journal)
}

// For derives the layout from the user's home directory and the XDG
// variables, so a test can point it at a fresh directory.
func For(goos, home string, getenv func(string) string) (Layout, error) {
	if home == "" || !filepath.IsAbs(home) {
		return Layout{}, errors.New("no home directory to keep VaultSync's files in")
	}
	var l Layout
	switch goos {
	case "darwin":
		l.Base = filepath.Join(home, "Library", "Application Support", "VaultSync")
		l.Logs = filepath.Join(home, "Library", "Logs", "VaultSync")
	case "linux":
		state := getenv("XDG_STATE_HOME")
		if state == "" || !filepath.IsAbs(state) { // the spec ignores relative values
			state = filepath.Join(home, ".local", "state")
		}
		l.Base = filepath.Join(state, "vaultsync")
	default:
		return Layout{}, fmt.Errorf("VaultSync's desktop agent runs on macOS and Linux; %s comes later", goos)
	}
	return l.At(l.Base), nil
}

// At roots the layout at base. The background service is started with the
// base setup resolved (`run --state-dir`), so it never depends on the
// service manager's own environment (XDG_STATE_HOME may differ there).
func (l Layout) At(base string) Layout {
	l.Base = base
	l.Bin = filepath.Join(l.Base, "bin")
	l.Agent = filepath.Join(l.Bin, "vaultsync")
	l.Syncthing = filepath.Join(l.Bin, "syncthing")
	l.Home = filepath.Join(l.Base, "syncthing")
	l.State = filepath.Join(l.Base, "agent.json")
	l.Lock = filepath.Join(l.Base, "agent.lock")
	l.Socket = filepath.Join(l.Base, "agent.sock")
	return l
}

// SocketErrorFile is the agent's record that it could not make or keep
// its control socket (control.go), in VaultSync's folder.
func (l Layout) SocketErrorFile() string {
	return filepath.Join(l.Base, "control-socket-error.txt")
}

// LogFile is the background service's own log on a Mac; "" where the
// service logs to the journal.
func (l Layout) LogFile() string {
	if l.Logs == "" {
		return ""
	}
	return filepath.Join(l.Logs, "vaultsync.log")
}

// AtStateDir is the layout rooted at a --state-dir flag's value: the folder
// setup used, as the service names it (run --state-dir) and the menu-bar
// app may be told it. "" keeps the layout; anything else must be absolute.
func (l Layout) AtStateDir(dir string) (Layout, error) {
	if dir == "" {
		return l, nil
	}
	if !filepath.IsAbs(dir) {
		return l, errors.New("--state-dir must be an absolute path")
	}
	return l.At(filepath.Clean(dir)), nil
}

// TrayLock is the menu-bar app's one-instance lock, in VaultSync's folder.
func (l Layout) TrayLock() string {
	return filepath.Join(l.Base, "tray.lock")
}

// Shorten takes VaultSync's folder and the home folder out of a message
// meant for a log others read: the folder becomes <VaultSync folder>, the
// home folder ~ — in the form given and in the resolved one (a folder
// reached through a link appears in both).
func Shorten(msg, folder, home string) string {
	replace := func(path, with string) {
		if path == "" || path == "/" {
			return
		}
		if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved != path {
			msg = strings.ReplaceAll(msg, resolved, with)
		}
		msg = strings.ReplaceAll(msg, path, with)
	}
	replace(folder, "<VaultSync folder>")
	replace(home, "~")
	return msg
}

// TildePath shows a path below the home folder as ~/…, the way people know
// it.
func TildePath(home, p string) string {
	if home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if rel, err := filepath.Rel(home, p); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		return "~" + string(filepath.Separator) + rel
	}
	return p
}
