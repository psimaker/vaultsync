package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/psimaker/vaultsync/desktop/internal/layout"
)

// Where the running agent listens, as a client finds it from agent.json —
// the terminal's commands and the menu-bar app read it alike.

// ErrNotSetUp: no agent.json with a port — vaultsync setup never ran on
// this computer.
var ErrNotSetUp = errors.New("VaultSync is not set up on this computer yet — run vaultsync setup in a terminal")

// socketState is the part of agent.json a client reads.
type socketState struct {
	Version       int    `json:"version"`
	GUIPort       int    `json:"guiPort"`
	ControlSocket string `json:"controlSocket"`
}

// SocketPath is where the running agent listens: the place agent.json
// records when the agent had to fall back elsewhere, else the layout's
// agent.sock. Without an agent.json, or one without the engine's port,
// VaultSync is not set up (ErrNotSetUp); one that cannot be read is
// reported as it is.
func SocketPath(lay layout.Layout) (string, error) {
	data, err := os.ReadFile(lay.State)
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrNotSetUp
	}
	if err != nil {
		return "", fmt.Errorf("VaultSync's settings could not be read: %w", err)
	}
	var st socketState
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

// SocketErrorRecorded: the agent wrote its socket-error file — it runs,
// or ran, without a socket it could not make or keep (a start that serves
// one, and a clean stop, remove the file), so nobody answering may well
// be an agent running without it.
func SocketErrorRecorded(lay layout.Layout) bool {
	_, err := os.Stat(lay.SocketErrorFile())
	return err == nil
}
