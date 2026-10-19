package tray

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Where the agent keeps its files, as the menu-bar app needs it: VaultSync's
// folder (for agent.json and the socket's usual place) and the service's
// log. It mirrors the agent's layout (paths.go); the agent's own test keeps
// the two the same.

// ErrNotSetUp: no agent.json — vaultsync setup never ran on this computer.
var ErrNotSetUp = errors.New("VaultSync is not set up on this computer yet — run vaultsync setup in a terminal")

// StateDir is VaultSync's folder for this user.
func StateDir(goos, home string, getenv func(string) string) (string, error) {
	if home == "" || !filepath.IsAbs(home) {
		return "", errors.New("no home directory to find VaultSync's files in")
	}
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "VaultSync"), nil
	case "linux":
		state := getenv("XDG_STATE_HOME")
		if state == "" || !filepath.IsAbs(state) {
			state = filepath.Join(home, ".local", "state")
		}
		return filepath.Join(state, "vaultsync"), nil
	}
	return "", fmt.Errorf("the menu-bar app runs on macOS and Linux; %s comes later", goos)
}

// LogPath is the background service's own log file on a Mac; empty on
// Linux, where the service logs to the journal.
func LogPath(goos, home string) string {
	if goos == "darwin" {
		return filepath.Join(home, "Library", "Logs", "VaultSync", "vaultsync.log")
	}
	return ""
}

// ErrNoSocket: nobody answers on the socket, and the agent has recorded
// that it could not make one — so VaultSync may well be running, only
// without the socket; the terminal's status can tell, the menu cannot.
var ErrNoSocket = errors.New("VaultSync does not answer on its control socket: it is stopped, or it runs without the socket (control-socket-error.txt is in VaultSync's folder) — vaultsync status in a terminal tells which")

// SocketErrorRecorded: the agent wrote control-socket-error.txt (it could
// not serve the socket at some start; the file stays until the folder is
// removed, so it says "maybe", never "now").
func SocketErrorRecorded(stateDir string) bool {
	_, err := os.Stat(filepath.Join(stateDir, "control-socket-error.txt"))
	return err == nil
}

// agentState is the part of agent.json the menu-bar app reads.
type agentState struct {
	Version       int    `json:"version"`
	GUIPort       int    `json:"guiPort"`
	ControlSocket string `json:"controlSocket"`
}

// SocketPath is where the running agent listens: the place agent.json
// records when the agent had to fall back elsewhere, else agent.sock in
// VaultSync's folder. Without an agent.json VaultSync is not set up
// (ErrNotSetUp); one that cannot be read is reported as it is.
func SocketPath(stateDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, "agent.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrNotSetUp
	}
	if err != nil {
		return "", fmt.Errorf("VaultSync's settings could not be read: %w", err)
	}
	var st agentState
	if err := json.Unmarshal(data, &st); err != nil {
		return "", fmt.Errorf("VaultSync's settings could not be read: agent.json is damaged (%v)", err)
	}
	if st.GUIPort == 0 {
		return "", ErrNotSetUp
	}
	if st.ControlSocket != "" {
		return st.ControlSocket, nil
	}
	return filepath.Join(stateDir, "agent.sock"), nil
}
