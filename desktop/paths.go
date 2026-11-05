package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

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
	// Started records, by SHA-256, the programs setup last started the
	// background service and the menu-bar app's start at login with —
	// written once they run (decision 053). A setup that stopped before
	// that (a download that failed after the setup link put a new
	// VaultSync for Mac in place) leaves the record behind the programs on
	// disk, and the next setup restarts them. No record restarts them
	// once, nothing more.
	Started struct {
		Service string `json:"service,omitempty"`
		Tray    string `json:"tray,omitempty"`
	} `json:"started"`
	// ParkedBesideApp is the VaultSync for Mac a run of any other program —
	// VaultSync's own copy — found installed: on a Mac only the app's agent
	// runs the engine while the app is there (decision 053 (8)), and such a
	// run stays parked also once the app is gone again, until setup sets
	// the service up anew and clears it. Empty: not parked.
	ParkedBesideApp string `json:"parkedBesideApp,omitempty"`
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

// updateStateIfSetUp is updateState for a note that only adds to a setup
// that is there — what setup started the jobs with: it makes neither
// VaultSync's folder nor agent.json, so an uninstall --remove-data that
// finished meanwhile stays finished.
func updateStateIfSetUp(path string, fn func(*agentState)) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	// The lock, and the write, without making the folder: one an uninstall
	// took apart meanwhile stays gone.
	h, err := waitForLock(context.Background(), filepath.Join(filepath.Dir(path), "state.lock"), 10*time.Second, lockFileNoCreate)
	if err != nil {
		return err
	}
	defer h.Release()
	if _, err := os.Stat(path); err != nil {
		return err
	}
	st, err := loadState(path)
	if err != nil {
		return err
	}
	fn(&st)
	st.Version = agentStateVersion
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomicIn(path, append(data, '\n'), 0o600)
}

// agentStateVersion is the agent.json format this build writes. Format 2,
// from desktop-v0.2.0 on, has format 1's fields: it is read, and written
// back as 2. desktop-v0.1.0 refuses every format but its own, so a kept
// v0.1.0 changes nothing where a later VaultSync is set up — it knows
// neither VaultSync for Mac (decision 053) nor that an older engine must
// not go over a newer one's state (decision 054).
const agentStateVersion = 2

// oldestStateVersion is the oldest agent.json format this build reads.
const oldestStateVersion = 1

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
	// The format is read on its own first: a newer one may give a field
	// this build knows another shape, and is newer, not damaged.
	var format struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &format); err != nil {
		return st, fmt.Errorf("%s is damaged: %w", path, err)
	}
	if format.Version > agentStateVersion {
		return st, fmt.Errorf("%s was written by a newer VaultSync (format %d): %w", path, format.Version, errStateNewer)
	}
	if format.Version < oldestStateVersion {
		return st, fmt.Errorf("%s was written by another VaultSync version (%d)", path, format.Version)
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("%s is damaged: %w", path, err)
	}
	return st, nil
}

// errStateNewer: agent.json is in a newer format than this build knows — a
// newer VaultSync is set up here, and this build is older (decision 054).
var errStateNewer = errors.New("agent.json was written by a newer VaultSync")

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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeFileAtomicIn(path, data, perm)
}

// writeFileAtomicIn is writeFileAtomic into a folder that must already be
// there: it never makes one.
func writeFileAtomicIn(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
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
