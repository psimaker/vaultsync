package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// layout is everything VaultSync keeps on this computer. Vaults are never in
// here: they stay where the user keeps them.
type layout struct {
	Base      string // one directory, 0700
	Bin       string // Base/bin
	Agent     string // Base/bin/vaultsync — the copy the background service runs
	Syncthing string // Base/bin/syncthing — the pinned engine
	Home      string // Base/syncthing — the engine's identity, config and database
	State     string // Base/agent.json
	Lock      string // Base/agent.lock — held by the one running engine owner
	Socket    string // Base/agent.sock — the running agent's control socket (control.go)
	Logs      string // where the service's own output goes (empty: the journal)
}

// layoutFor derives the layout from the user's home directory and the XDG
// variables, so a test can point it at a fresh directory.
func layoutFor(goos, home string, getenv func(string) string) (layout, error) {
	if home == "" || !filepath.IsAbs(home) {
		return layout{}, errors.New("no home directory to keep VaultSync's files in")
	}
	var l layout
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
		return layout{}, fmt.Errorf("VaultSync's desktop agent runs on macOS and Linux; %s comes later", goos)
	}
	return l.at(l.Base), nil
}

// at roots the layout at base. The background service is started with the
// base setup resolved (`run --state-dir`), so it never depends on the
// service manager's own environment (XDG_STATE_HOME may differ there).
func (l layout) at(base string) layout {
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

// agentState is agent.json: what the agent installed and where its engine
// listens. The engine's API key stays in the engine's own config.xml.
type agentState struct {
	Version   int `json:"version"`
	GUIPort   int `json:"guiPort"`
	Syncthing struct {
		Version      string `json:"version"`
		BinarySHA256 string `json:"binarySHA256"`
	} `json:"syncthing"`
	// ControlSocket is where the running agent listens (control.go) when
	// that is not the layout's usual place; empty otherwise.
	ControlSocket string `json:"controlSocket,omitempty"`
	// Env holds the shell's values of the variables the pairing guards
	// read (guardEnvVars), recorded by setup and by pair in a terminal: a
	// pairing run by the background service (the control socket) has the
	// service's environment, not the shell's, and must look where the
	// terminal looks — for the user's own Syncthing, Obsidian's registry
	// and the cloud clients' settings (#176). An empty record is a shell
	// with none of them set (its defaults apply); nil is no record yet.
	Env map[string]string `json:"env"`
}

// guardEnvVars are the variables the pairing guards read.
var guardEnvVars = []string{"XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR", "APPDATA"}

// recordGuardEnv is the shell's values of guardEnvVars, set ones only —
// never nil, so that a shell with none set is a record too.
func recordGuardEnv(getenv func(string) string) map[string]string {
	out := map[string]string{}
	for _, k := range guardEnvVars {
		if v := getenv(k); v != "" {
			out[k] = v
		}
	}
	return out
}

// updateState applies fn to agent.json as one step under its own lock —
// loaded, changed and written — so the writers (setup, run, the supervisor
// moving the engine's port, pair recording the shell) never write an older
// copy over each other's fields.
func updateState(path string, fn func(*agentState)) error {
	unlock, err := lockFileWait(filepath.Join(filepath.Dir(path), "state.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	st, err := loadState(path)
	if err != nil {
		return err
	}
	fn(&st)
	return saveState(path, st)
}

const agentStateVersion = 1

func loadState(path string) (agentState, error) {
	var st agentState
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		st.Version = agentStateVersion
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("%s is damaged: %w", path, err)
	}
	if st.Version != agentStateVersion {
		return st, fmt.Errorf("%s was written by another VaultSync version (%d)", path, st.Version)
	}
	return st, nil
}

// saveState writes agent.json atomically with owner-only permissions.
func saveState(path string, st agentState) error {
	st.Version = agentStateVersion
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'), 0o600)
}

// writeFileAtomic replaces path in one rename, so a reader sees the old or
// the new content and never a torn file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	// The rename itself is durable only once the directory is synced.
	return syncDir(dir)
}

// syncDir flushes a directory's entries to disk; a variable so a test can
// make it fail.
var syncDir = func(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
