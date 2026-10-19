package tray

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"syscall"

	"github.com/psimaker/vaultsync/desktop/internal/peer"
)

// Client reaches the running agent on its control socket — the only thing
// the menu-bar app ever talks to (decision 048): it never touches the sync
// engine, and decides nothing the agent would not decide for the terminal.

// ErrNoAgent: no running agent answers on the control socket.
var ErrNoAgent = errors.New("VaultSync is not running on this computer: neither the background service nor a vaultsync run answers")

// ErrForeignSocket: the socket is served by another account.
var ErrForeignSocket = errors.New("the control socket is served by another account; VaultSync does not use it")

// ErrOldAgent: something answers on the socket, but not this API — an
// agent older than this app. The terminal falls back to the engine in
// that case; the menu-bar app never touches the engine, so it says so.
var ErrOldAgent = errors.New("the VaultSync agent on this computer is older than this menu-bar app and does not answer its requests: update VaultSync here")

// peerUID is the kernel's word on whose a connection is; a variable so a
// test can pretend another account.
var peerUID = peerUIDDefault

func peerUIDDefault(conn *net.UnixConn) (int, error) { return peer.UID(conn) }

type Client struct {
	path string
	http *http.Client
}

// NewClient talks to the agent on the socket at path.
func NewClient(path string) *Client {
	return &Client{path: path, http: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			conn, err := d.DialContext(ctx, "unix", path)
			if err != nil {
				return nil, err
			}
			// Nothing is sent before the kernel has said whose socket this
			// is: a place another account could have taken over must not
			// see a request, a pairing code least of all.
			uc, ok := conn.(*net.UnixConn)
			if !ok {
				conn.Close()
				return nil, ErrForeignSocket
			}
			uid, err := peerUID(uc)
			if err != nil {
				conn.Close()
				return nil, fmt.Errorf("could not tell whose the control socket is: %w", err)
			}
			if uid != os.Getuid() {
				conn.Close()
				return nil, fmt.Errorf("%w (uid %d)", ErrForeignSocket, uid)
			}
			return conn, nil
		},
	}}}
}

// Path is the socket the client talks to.
func (c *Client) Path() string { return c.path }

func (c *Client) Status(ctx context.Context) (Status, error) {
	var s Status
	err := c.do(ctx, http.MethodGet, "/v1/status", nil, &s)
	return s, err
}

func (c *Client) SetPaused(ctx context.Context, paused bool) (PauseResult, error) {
	var r PauseResult
	path := "/v1/resume"
	if paused {
		path = "/v1/pause"
	}
	err := c.do(ctx, http.MethodPost, path, nil, &r)
	return r, err
}

func (c *Client) Pair(ctx context.Context, req PairRequest) (PairResult, error) {
	var r PairResult
	err := c.do(ctx, http.MethodPost, "/v1/pair", req, &r)
	return r, err
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	// A socket address too long for this system cannot be served by anyone
	// — the agent fell back elsewhere, or runs without a socket — so it is
	// no agent, not an error of its own.
	if len(c.path) >= peer.SunPathMax {
		return ErrNoAgent
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://vaultsync"+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// No socket file, or nobody listening on it: no agent. Any other
		// failure — a socket this account may not open, one served by
		// another account, a connection made and then dropped — is
		// reported as it is.
		var ne *net.OpError
		if errors.As(err, &ne) && ne.Op == "dial" && (errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)) {
			return ErrNoAgent
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrOldAgent
	}
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return errors.New(e.Error)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}
