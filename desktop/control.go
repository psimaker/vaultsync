package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
	// background: the engine belongs to the background service (run as the
	// service) rather than to a vaultsync run in a terminal; the remedies
	// status names differ. Fixed for the agent's lifetime.
	background bool
	// ctx is the agent's lifetime: a pairing runs on it, not on the request
	// that asked for it, so a caller that goes away mid-way does not abort
	// an accept.
	ctx context.Context
	srv *http.Server
}

// controlRequestLimit bounds a request body; the flags of a pairing are a
// few hundred bytes.
const controlRequestLimit = 64 << 10

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
	// The server's own diagnostics — an accept that failed, a request it
	// could not read — name the socket's path: they go to a private file in
	// VaultSync's folder (0600, started over past 1 MiB), never to the
	// service log.
	diag, err := openStderrLog(filepath.Join(c.a.lay.Base, "control-socket.log"))
	if err != nil {
		l.Close()
		cleanup()
		return "", nil, err
	}
	// A pairing takes minutes (discovery, the Hub, the wait for its share),
	// so the response may take that long; reading a request and holding an
	// idle connection may not.
	srv := &http.Server{
		Handler:           c.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      15 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          log.New(diag, "", log.LstdFlags),
	}
	c.srv = srv
	done := make(chan struct{})
	go func() {
		if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// The engine goes on; status and the menu-bar app lose their
			// socket until the next start. The words stay in the folder.
			c.logf("the control socket stopped serving — %s; vaultsync status reads the engine directly (details in control-socket.log)", serviceSummary(err))
			fmt.Fprintf(diag, "%s serve: %v\n", time.Now().Format(time.RFC3339), err)
		}
		close(done)
	}()
	stop = func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		_ = srv.Close()
		<-done
		cleanup()
		diag.Close()
	}
	return path, stop, nil
}

// listenControl listens at path — or, when VaultSync's folder lies too deep
// for a socket address, in a folder of our own under the temporary
// directory. cleanup removes what was made: the socket, and the folder
// only when nothing else is in it — never anything inside it (a vault
// placed there by hand would be a vault; pairing refuses the folder, see
// pairSession.reserved).
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
		_ = os.Remove(dir)
		return nil, "", nil, err // the first error names the real place
	}
	return l, at, func() {
		_ = os.Remove(at)
		_ = os.Remove(dir)
	}, nil
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
	err = client.Ping(pctx)
	cancel()
	switch {
	case err == nil:
	case errors.Is(err, syscall.ECONNREFUSED):
		// Nobody listens on the engine's port: it is on its way up, or the
		// supervisor is starting it again. Anything else — a refused probe,
		// no answer in time — says nothing about syncing and is reported.
		cs.statusReport = c.a.offlineReport()
		return cs, nil
	default:
		return cs, fmt.Errorf("the sync engine does not answer: %w", err)
	}
	rep, err := c.a.report(ctx, client, c.background)
	if err != nil {
		return cs, err
	}
	cs.Engine = "running"
	cs.statusReport = rep
	return cs, nil
}

// setPaused pauses or resumes every Hub this engine knows: the connections
// stop, nothing new arrives or leaves — the folders and their settings are
// untouched, and work the engine had already taken in (an index it holds,
// a change received before the pause) may still be applied, which status
// shows. The engine keeps a paused device paused across its restarts. The
// PATCH goes out even for a device that already reads as paused: the
// engine applies a change before it saves it, so a save that failed leaves
// the device paused in memory only — a retry must persist it. It takes the
// pairing lock: a pairing adds a Hub, and the two must not interleave — a
// pairing sees the pause, or the pause waits for the pairing.
func (c *controlServer) setPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	unlock, err := lockFile(filepath.Join(c.a.lay.Base, "pair.lock"))
	if errors.Is(err, ErrEngineRunning) {
		fail(w, http.StatusConflict, errors.New("a pairing is running on this computer — wait for it to finish, then try again"))
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	defer unlock()
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
// valid until the Hub expires it; only wrong codes lock it). The flow
// itself refuses while syncing is paused, for the terminal and the socket
// alike.
func (c *controlServer) pair(w http.ResponseWriter, r *http.Request) {
	var req pairRequest
	if err := readJSON(r.Body, &req); err != nil {
		fail(w, http.StatusBadRequest, fmt.Errorf("bad request: %w", err))
		return
	}
	client, err := c.client()
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	resp := pairResponse{}
	var out bytes.Buffer
	opts := pairOptions{code: req.Code, hub: req.Hub, vault: req.Vault, create: req.Create, path: req.Path, yes: req.Yes, name: req.Name}
	st, err := loadState(c.a.lay.State)
	if err != nil {
		fail(w, http.StatusInternalServerError, err)
		return
	}
	if st.Env == nil {
		// An agent.json from before the record: the guards would look only
		// where the service looks, which may not be where the user's own
		// Syncthing or cloud settings are — the terminal records that once.
		resp.Refusal = "VaultSync has not recorded where this computer keeps its settings yet. Run vaultsync pair in a terminal once, then try again here."
		writeJSON(w, http.StatusOK, resp)
		return
	}
	s, err := c.a.pairWith(c.ctx, &term{out: &out}, opts, client, pairOrigin{background: c.background, shellEnv: st.Env})
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

// readJSON reads one JSON document of at most controlRequestLimit bytes:
// anything longer, and anything after the document, is refused before a
// handler acts on it.
func readJSON(body io.Reader, v any) error {
	data, err := io.ReadAll(io.LimitReader(body, controlRequestLimit+1))
	if err != nil {
		return err
	}
	if len(data) > controlRequestLimit {
		return fmt.Errorf("the request is larger than %d bytes", controlRequestLimit)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("the request holds more than one JSON document")
	}
	return nil
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
		// No socket file, or nobody listening on it: no agent. Any other
		// failure — a socket this account may not open, no file descriptors
		// left, a connection made and then dropped — is reported as it is.
		var ne *net.OpError
		if errors.As(err, &ne) && ne.Op == "dial" && (errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)) {
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

// logSocketError says in the service's log that there is no control socket
// — without the error's words, which name paths (the log is read by
// others; the folder is not) — and keeps the words in VaultSync's folder.
func logSocketError(logf func(string, ...any), base string, err error) {
	logf("no control socket — %s; vaultsync status reads the engine directly (details in control-socket-error.txt)", serviceSummary(err))
	_ = writeFileAtomic(filepath.Join(base, "control-socket-error.txt"), []byte(err.Error()+"\n"), 0o600)
}

// noAgentForPause: no agent answers on the socket — which does not prove
// that the engine is stopped. An agent without a control socket (an older
// VaultSync, or a socket that could not be made) runs it all the same, so
// the engine is asked, and the refusal says what is known: it runs and was
// not paused; nobody listens on its port, so nothing syncs; or neither
// could be told, so syncing may continue.
func (a *app) noAgentForPause(ctx context.Context, st agentState, pause bool) error {
	if st.GUIPort == 0 {
		return refuse("VaultSync is not set up on this computer yet. Run vaultsync setup.")
	}
	client, err := a.engine().client(st)
	if errors.Is(err, fs.ErrNotExist) {
		// No config.xml: nothing was ever set up. Any other failure to
		// read it is not that, and is reported below.
		return refuse("VaultSync is not set up on this computer yet. Run vaultsync setup.")
	}
	if err == nil {
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = client.Ping(pctx)
		cancel()
	}
	switch {
	case err == nil:
		why := "The sync engine runs, but the VaultSync agent that controls it does not answer (an older VaultSync, or no control socket — see control-socket-error.txt in VaultSync's folder)"
		if pause {
			return refuse("%s: syncing could not be paused and continues. vaultsync stop stops the background service.", why)
		}
		return refuse("%s: VaultSync cannot tell whether syncing is paused, and could not resume it.", why)
	case errors.Is(err, syscall.ECONNREFUSED):
		// Nobody listens on the engine's port: the one positive sign that
		// nothing syncs.
		if pause {
			return refuse("%s — nothing syncs while it is not running, so there is nothing to pause. See vaultsync status.", errNoAgent)
		}
		return refuse("%s. vaultsync start resumes the background service.", errNoAgent)
	}
	// Refused, timed out, or the engine's config could not be read: not
	// known either way.
	return refuse("VaultSync could not confirm whether the sync engine runs (%s), and no agent answers on the control socket: syncing may continue, and nothing was changed. See vaultsync status; vaultsync stop stops the background service.", redactPaths(err.Error()))
}

// --- pause / resume from the terminal ----------------------------------------

func (a *app) pauseSync(ctx context.Context, pause bool) error {
	st, err := loadState(a.lay.State)
	if err != nil {
		return err
	}
	if st.GUIPort == 0 {
		return refuse("VaultSync is not set up on this computer yet. Run vaultsync setup.")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, err := a.controlClient(st).setPaused(ctx, pause)
	switch {
	case errors.Is(err, errNoAgent):
		return a.noAgentForPause(ctx, st, pause)
	case err != nil:
		return err
	case res.Hubs == 0:
		return refuse("No Hub is paired with this computer yet, so there is nothing to pause. Run vaultsync pair.")
	case pause:
		fmt.Fprintln(a.out, "✓ Syncing is paused on this computer: the connections to your Hub are off until you run vaultsync resume. Changes the engine had already received may still be applied for a moment; vaultsync status shows what is still in progress.")
	default:
		fmt.Fprintln(a.out, "✓ Syncing resumed on this computer.")
	}
	return nil
}
