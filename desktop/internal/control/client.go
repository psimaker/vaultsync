// Package control is the client side of the agent's control socket
// (decision 049), shared by the terminal (vaultsync status, pause, resume)
// and the menu-bar app: one dialer that checks whose the socket is before a
// byte is sent, one reading of "no agent", one request shape. What a 404
// means differs — the terminal asks the engine instead, the menu-bar app
// says the agent is older — so each caller names it.
package control

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
	"sync/atomic"
	"syscall"

	"github.com/psimaker/vaultsync/desktop/internal/peer"
)

// ErrNoAgent: no running agent answers on the control socket.
var ErrNoAgent = errors.New("VaultSync is not running on this computer: neither the background service nor a vaultsync run answers")

// ErrForeignSocket: the socket is served by another account.
var ErrForeignSocket = errors.New("the control socket is served by another account; VaultSync does not use it")

// PeerFunc tells whose a unix socket connection is.
type PeerFunc func(*net.UnixConn) (int, error)

// PeerUIDHook lets a test pretend another account, on both ends (set from
// the test, read by a server's own goroutine — hence atomic).
var PeerUIDHook atomic.Pointer[PeerFunc]

// PeerUID is the kernel's word on whose a connection is, or the hook's.
func PeerUID(c *net.UnixConn) (int, error) {
	if f := PeerUIDHook.Load(); f != nil {
		return (*f)(c)
	}
	return peer.UID(c)
}

// Client reaches the agent on one socket.
type Client struct {
	http     *http.Client
	path     string
	notFound error
}

// New talks to the agent on the socket at path; notFound is what a 404 —
// something answering, but not this API — is to the caller.
func New(path string, notFound error) *Client {
	return &Client{path: path, notFound: notFound, http: &http.Client{Transport: &http.Transport{
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
			uid, err := PeerUID(uc)
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

// CloseIdleConnections drops the connection kept open between requests.
func (c *Client) CloseIdleConnections() { c.http.CloseIdleConnections() }

// Do sends one request and decodes the answer into out (nil: none
// expected). No socket file, or nobody listening on it, is ErrNoAgent; a
// 404 is notFound; an error the agent answered with is returned in its
// words; anything else — a socket this account may not open, one served
// by another account, a connection made and then dropped — as it is.
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
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
		var ne *net.OpError
		if errors.As(err, &ne) && ne.Op == "dial" && (errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)) {
			return ErrNoAgent
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return c.notFound
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
