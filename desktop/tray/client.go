package tray

import (
	"context"
	"errors"
	"net/http"

	"github.com/psimaker/vaultsync/desktop/internal/control"
)

// Client reaches the running agent on its control socket — the only thing
// the menu-bar app ever talks to (decision 048): it never touches the sync
// engine, and decides nothing the agent would not decide for the terminal.
// The dialing, the owner check and the reading of "no agent" are the
// shared client's (internal/control), the terminal's too.

// ErrNoAgent: no running agent answers on the control socket.
var ErrNoAgent = control.ErrNoAgent

// ErrForeignSocket: the socket is served by another account.
var ErrForeignSocket = control.ErrForeignSocket

// ErrOldAgent: something answers on the socket, but not this API — an
// agent older than this app. The terminal falls back to the engine in
// that case; the menu-bar app never touches the engine, so it says so.
var ErrOldAgent = errors.New("the VaultSync agent on this computer is older than this menu-bar app and does not answer its requests: update VaultSync here")

type Client struct {
	*control.Client
}

// NewClient talks to the agent on the socket at path.
func NewClient(path string) *Client {
	return &Client{control.New(path, ErrOldAgent)}
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	var s Status
	err := c.Do(ctx, http.MethodGet, "/v1/status", nil, &s)
	return s, err
}

func (c *Client) SetPaused(ctx context.Context, paused bool) (PauseResult, error) {
	var r PauseResult
	path := "/v1/resume"
	if paused {
		path = "/v1/pause"
	}
	err := c.Do(ctx, http.MethodPost, path, nil, &r)
	return r, err
}

func (c *Client) Pair(ctx context.Context, req PairRequest) (PairResult, error) {
	var r PairResult
	err := c.Do(ctx, http.MethodPost, "/v1/pair", req, &r)
	return r, err
}
