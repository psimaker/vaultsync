package tray

import (
	"errors"

	"github.com/psimaker/vaultsync/desktop/internal/control"
)

// Client reaches the running agent on its control socket — the only thing
// the menu-bar app ever talks to (decision 048): it never touches the sync
// engine, and decides nothing the agent would not decide for the terminal.
// It is the shared client (internal/control), the terminal's too; what
// differs is what a 404 means.
type Client = control.Client

// ErrNoAgent: no running agent answers on the control socket.
var ErrNoAgent = control.ErrNoAgent

// ErrForeignSocket: the socket is served by another account.
var ErrForeignSocket = control.ErrForeignSocket

// ErrOldAgent: something answers on the socket, but not this API — an
// agent older than this app. The terminal falls back to the engine in
// that case; the menu-bar app never touches the engine, so it says so.
var ErrOldAgent = errors.New("the VaultSync agent on this computer is older than this menu-bar app and does not answer its requests: update VaultSync here")

// NewClient talks to the agent on the socket at path.
func NewClient(path string) *Client {
	return control.New(path, ErrOldAgent)
}
