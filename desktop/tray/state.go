package tray

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/psimaker/vaultsync/desktop/internal/layout"
)

// Where the agent keeps its files is the shared layout's word
// (internal/layout); what the menu-bar app adds is reading agent.json for
// the socket's place.

// ErrNotSetUp: no agent.json — vaultsync setup never ran on this computer.
var ErrNotSetUp = errors.New("VaultSync is not set up on this computer yet — run vaultsync setup in a terminal")

// ErrNoSocket: nobody answers on the socket, and the agent has recorded
// that it could not make one — so VaultSync may well be running, only
// without the socket; the terminal's status can tell, the menu cannot.
var ErrNoSocket = errors.New("VaultSync does not answer on its control socket: it is stopped, or it runs without the socket (control-socket-error.txt is in VaultSync's folder) — vaultsync status in a terminal tells which")

// SocketErrorRecorded: the agent wrote control-socket-error.txt — its last
// start could not serve the socket (a start that serves one removes the
// file), so nobody answering may well be an agent running without it.
func SocketErrorRecorded(lay layout.Layout) bool {
	_, err := os.Stat(filepath.Join(lay.Base, "control-socket-error.txt"))
	return err == nil
}

// agentState is the part of agent.json the menu-bar app reads.
type agentState struct {
	Version       int    `json:"version"`
	GUIPort       int    `json:"guiPort"`
	ControlSocket string `json:"controlSocket"`
}

// SocketPath is where the running agent listens: the place agent.json
// records when the agent had to fall back elsewhere, else the layout's
// agent.sock. Without an agent.json VaultSync is not set up (ErrNotSetUp);
// one that cannot be read is reported as it is.
func SocketPath(lay layout.Layout) (string, error) {
	data, err := os.ReadFile(lay.State)
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
	return lay.Socket, nil
}
