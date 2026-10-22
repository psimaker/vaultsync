package tray

import (
	"errors"

	"github.com/psimaker/vaultsync/desktop/internal/control"
)

// Where the agent keeps its files is the shared layout's word
// (internal/layout); where it listens, read from agent.json, is the shared
// client's (control.SocketPath).

// ErrNotSetUp: vaultsync setup never ran on this computer.
var ErrNotSetUp = control.ErrNotSetUp

// ErrAlreadyRunning: another menu-bar app of this user holds the lock —
// or an uninstall, which takes it while it removes VaultSync's folder.
var ErrAlreadyRunning = errors.New("the VaultSync menu-bar app is already running (or VaultSync is being removed right now)")

// ErrNoSocket: nobody answers on the socket, and the agent has recorded
// that it could not make or keep one — so VaultSync may well be running,
// only without the socket; the terminal's status can tell, the menu cannot.
var ErrNoSocket = errors.New("VaultSync does not answer on its control socket: it is stopped, or it runs without the socket (control-socket-error.txt is in VaultSync's folder) — vaultsync status in a terminal tells which")
