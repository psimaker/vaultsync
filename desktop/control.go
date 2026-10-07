package main

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
	"path/filepath"
	"strings"
	"time"

	"github.com/psimaker/vaultsync/hub/syncthing"
)

// The control socket (#176, decision 049): the running agent answers
// status, pause, resume and pair over HTTP on a unix socket in VaultSync's
// folder — for vaultsync status, pause and resume in a terminal, and for
// the menu-bar app. Same-user only: the folder is 0700 and the socket 0600.
// The agent decides nothing here that it would not decide for the terminal:
// a pairing over the socket is the flag flow, consent included.

// errNoAgent: no running agent answers on the control socket.
var errNoAgent = errors.New("VaultSync is not running on this computer: neither the background service nor a vaultsync run answers")

// controlStatus is the agent's answer to GET /v1/status.
type controlStatus struct {
	Version   string `json:"version"`   // the agent's
	Syncthing string `json:"syncthing"` // the engine's, without the v
	// Engine is "running" when the engine answers, else "starting": the
	// agent is up and bringing it up.
	Engine string `json:"engine"`
	statusReport
}

// pauseResponse answers POST /v1/pause and /v1/resume.
type pauseResponse struct {
	Paused bool `json:"paused"`
	Hubs   int  `json:"hubs"` // Hubs this engine knows; 0 leaves nothing to pause
}

// pairRequest is POST /v1/pair: the flags of vaultsync pair.
type pairRequest struct {
	Code   string `json:"code"`
	Hub    string `json:"hub"`
	Vault  string `json:"vault"`
	Create bool   `json:"create"`
	Path   string `json:"path"`
	Yes    bool   `json:"yes"`
	Name   string `json:"name"`
}

// pairResponse: what vaultsync pair printed, its refusal when it needs a
// decision the flags did not make, and — once the Hub was reached — the
// menu the terminal would show, so the caller can choose and ask again.
type pairResponse struct {
	OK      bool      `json:"ok"`
	Output  string    `json:"output"`
	Refusal string    `json:"refusal,omitempty"`
	Menu    *menuJSON `json:"menu,omitempty"`
}

type menuJSON struct {
	Local       []menuEntryJSON `json:"local"`
	Hub         []menuEntryJSON `json:"hub"`
	Already     []menuEntryJSON `json:"already"`
	Unavailable []menuEntryJSON `json:"unavailable"`
	Note        string          `json:"note,omitempty"`
}

type menuEntryJSON struct {
	Label string `json:"label"`
	Path  string `json:"path,omitempty"`  // a folder on this computer
	Vault string `json:"vault,omitempty"` // a vault on the Hub, by its ID
	Note  string `json:"note,omitempty"`
}

func menuToJSON(m vaultMenu) *menuJSON {
	conv := func(in []menuEntry) []menuEntryJSON {
		out := make([]menuEntryJSON, 0, len(in))
		for _, e := range in {
			j := menuEntryJSON{Label: e.label, Path: e.path, Note: e.note}
			if e.vault != nil {
				j.Vault = e.vault.ID
			}
			out = append(out, j)
		}
		return out
	}
	return &menuJSON{Local: conv(m.local), Hub: conv(m.hub), Already: conv(m.already), Unavailable: conv(m.unavailable), Note: m.registryNote}
}

// --- the agent's side -------------------------------------------------------

type controlServer struct {
	a    *app
	eng  engine
	logf func(string, ...any)
	// ctx is the agent's lifetime: a pairing runs on it, not on the request
	// that asked for it, so a caller that goes away mid-way does not abort
	// an accept.
	ctx context.Context
}

func (c *controlServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", c.status)
	mux.HandleFunc("POST /v1/pause", func(w http.ResponseWriter, r *http.Request) { c.setPaused(w, r, true) })
	mux.HandleFunc("POST /v1/resume", func(w http.ResponseWriter, r *http.Request) { c.setPaused(w, r, false) })
	mux.HandleFunc("POST /v1/pair", c.pair)
	return mux
}

// serve listens on the control socket and serves until stop is called. The
// engine lock must be held: a socket file found at the place belongs to an
// agent that is gone. It returns the path listened on.
func (c *controlServer) serve(ctx context.Context) (path string, stop func(), err error) {
	l, path, cleanup, err := listenControl(c.a.lay.Socket)
	if err != nil {
		return "", nil, err
	}
	c.ctx = ctx
	srv := &http.Server{Handler: c.handler(), ReadHeaderTimeout: 10 * time.Second}
	done := make(chan struct{})
	go func() {
		_ = srv.Serve(l)
		close(done)
	}()
	stop = func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		_ = srv.Close()
		<-done
		cleanup()
	}
	return path, stop, nil
}

// listenControl listens at path — or, when VaultSync's folder lies too deep
// for a socket address, in a folder of our own under the temporary
// directory. cleanup removes what was made.
func listenControl(path string) (l net.Listener, at string, cleanup func(), err error) {
	l, err = listenUnix(path)
	if err == nil {
		return l, path, func() { _ = os.Remove(path) }, nil
	}
	dir, derr := os.MkdirTemp("", "vaultsync-")
	if derr != nil {
		return nil, "", nil, err
	}
	at = filepath.Join(dir, "agent.sock")
	l, lerr := listenUnix(at)
	if lerr != nil {
		_ = os.RemoveAll(dir)
		return nil, "", nil, err // the first error names the real place
	}
	return l, at, func() { _ = os.RemoveAll(dir) }, nil
}

// listenUnix listens on a fresh socket at path, owner-only.
func listenUnix(path string) (net.Listener, error) {
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

func (c *controlServer) client() (*syncthing.Client, error) {
	st, err := loadState(c.a.lay.State)
	if err != nil {
		return nil, err
	}
	return c.eng.client(st)
}

func (c *controlServer) status(w http.ResponseWriter, r *http.Request) {
	cs, err := c.currentStatus(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

func (c *controlServer) currentStatus(ctx context.Context) (controlStatus, error) {
	cs := controlStatus{Version: version, Syncthing: strings.TrimPrefix(syncthingVersion, "v"), Engine: "starting", statusReport: emptyReport()}
	client, err := c.client()
	if err != nil {
		return cs, err
	}
	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	up := client.Ping(pctx) == nil
	cancel()
	if !up {
		return cs, nil
	}
	rep, err := c.a.report(ctx, client, serviceMode)
	if err != nil {
		return cs, err
	}
	cs.Engine = "running"
	cs.statusReport = rep
	return cs, nil
}

// setPaused pauses or resumes every Hub this engine knows. A paused device
// stays paused across restarts, like a vaultsync stop; the folders and
// their settings are untouched.
func (c *controlServer) setPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	client, err := c.client()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	ctx := r.Context()
	myID, err := client.MyID(ctx)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, fmt.Errorf("the sync engine does not answer: %w", err))
		return
	}
	devices, err := client.Devices(ctx)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, fmt.Errorf("the sync engine does not answer: %w", err))
		return
	}
	res := pauseResponse{Paused: paused}
	for _, d := range devices {
		if d.DeviceID == myID {
			continue
		}
		res.Hubs++
		if d.Paused == paused {
			continue
		}
		if err := client.PatchDevice(ctx, d.DeviceID, map[string]any{"paused": paused}); err != nil {
			fail(w, http.StatusServiceUnavailable, fmt.Errorf("the sync engine did not take the change: %w", err))
			return
		}
	}
	writeJSON(w, http.StatusOK, res)
}

// pair runs vaultsync pair's flag flow in the agent: no terminal, so every
// decision the flags did not make is refused with the flag to pass — and
// with the menu, so the caller can choose and ask again (the code stays
// valid until the Hub expires it; only wrong codes lock it).
func (c *controlServer) pair(w http.ResponseWriter, r *http.Request) {
	var req pairRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, fmt.Errorf("bad request: %w", err))
		return
	}
	client, err := c.client()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	paused, err := pausedHere(c.ctx, client)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, fmt.Errorf("the sync engine does not answer: %w", err))
		return
	}
	resp := pairResponse{}
	if paused {
		resp.Refusal = "Syncing is paused on this computer — run vaultsync resume first."
		writeJSON(w, http.StatusOK, resp)
		return
	}
	var out bytes.Buffer
	opts := pairOptions{code: req.Code, hub: req.Hub, vault: req.Vault, create: req.Create, path: req.Path, yes: req.Yes, name: req.Name}
	s, err := c.a.pairWith(c.ctx, &term{out: &out}, opts, client)
	resp.Output = out.String()
	if s != nil && s.menu != nil {
		resp.Menu = menuToJSON(*s.menu)
	}
	var ref *refusal
	switch {
	case err == nil:
		resp.OK = true
	case errors.Is(err, errPairingBusy):
		fail(w, http.StatusConflict, err)
		return
	case errors.As(err, &ref):
		resp.Refusal = redactPaths(ref.msg)
	default:
		fail(w, http.StatusInternalServerError, errors.New(redactPaths(err.Error())))
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// pausedHere: every Hub this engine knows is paused (and there is one).
func pausedHere(ctx context.Context, c *syncthing.Client) (bool, error) {
	myID, err := c.MyID(ctx)
	if err != nil {
		return false, err
	}
	devices, err := c.Devices(ctx)
	if err != nil {
		return false, err
	}
	hubs, paused := 0, 0
	for _, d := range devices {
		if d.DeviceID == myID {
			continue
		}
		hubs++
		if d.Paused {
			paused++
		}
	}
	return hubs > 0 && paused == hubs, nil
}

func fail(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// --- the terminal's side ----------------------------------------------------

type controlClient struct {
	http *http.Client
}

func newControlClient(path string) *controlClient {
	return &controlClient{http: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}}}
}

// controlClient reaches the running agent: on the socket agent.json
// records, else at the socket's usual place.
func (a *app) controlClient(st agentState) *controlClient {
	path := st.ControlSocket
	if path == "" {
		path = a.lay.Socket
	}
	return newControlClient(path)
}

func (c *controlClient) status(ctx context.Context) (controlStatus, error) {
	var cs controlStatus
	err := c.do(ctx, http.MethodGet, "/v1/status", nil, &cs)
	return cs, err
}

func (c *controlClient) setPaused(ctx context.Context, paused bool) (pauseResponse, error) {
	var res pauseResponse
	path := "/v1/resume"
	if paused {
		path = "/v1/pause"
	}
	err := c.do(ctx, http.MethodPost, path, nil, &res)
	return res, err
}

func (c *controlClient) pair(ctx context.Context, req pairRequest) (pairResponse, error) {
	var res pairResponse
	err := c.do(ctx, http.MethodPost, "/v1/pair", req, &res)
	return res, err
}

func (c *controlClient) do(ctx context.Context, method, path string, in, out any) error {
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
		// No socket, or nobody listening on it: no agent.
		var ne *net.OpError
		if errors.As(err, &ne) {
			return errNoAgent
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errNoAgent // an agent without this API
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

// --- pause / resume from the terminal ----------------------------------------

func (a *app) pauseSync(ctx context.Context, pause bool) error {
	if !a.engine().prepared() {
		return refuse("VaultSync is not set up on this computer yet. Run vaultsync setup.")
	}
	st, err := loadState(a.lay.State)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, err := a.controlClient(st).setPaused(ctx, pause)
	switch {
	case errors.Is(err, errNoAgent) && pause:
		return refuse("%s — nothing syncs while it is not running, so there is nothing to pause. See vaultsync status.", errNoAgent)
	case errors.Is(err, errNoAgent):
		return refuse("%s. vaultsync start resumes the background service.", errNoAgent)
	case err != nil:
		return err
	case res.Hubs == 0:
		return refuse("No Hub is paired with this computer yet, so there is nothing to pause. Run vaultsync pair.")
	case pause:
		fmt.Fprintln(a.out, "✓ Syncing is paused on this computer. Nothing syncs until you run vaultsync resume.")
	default:
		fmt.Fprintln(a.out, "✓ Syncing resumed on this computer.")
	}
	return nil
}
