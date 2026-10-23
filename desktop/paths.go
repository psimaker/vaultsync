package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	layoutpkg "github.com/psimaker/vaultsync/desktop/internal/layout"
)

// layout is everything VaultSync keeps on this computer — the shared
// package's, so the menu-bar app looks in the same places (internal/layout).
type layout = layoutpkg.Layout

func layoutFor(goos, home string, getenv func(string) string) (layout, error) {
	return layoutpkg.For(goos, home, getenv)
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
