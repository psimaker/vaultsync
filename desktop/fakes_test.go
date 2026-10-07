package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psimaker/vaultsync/hub/pairing"
	"github.com/psimaker/vaultsync/hub/pake"
	"github.com/psimaker/vaultsync/hub/syncthing"
)

const (
	testMyID  = "BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB"
	testHubID = "AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA"
	otherID   = "CCCCCCC-CCCCCCC-CCCCCCC-CCCCCCC-CCCCCCC-CCCCCCC-CCCCCCC-CCCCCCC"
)

// fakeEngine is the REST subset of the agent's own Syncthing.
type fakeEngine struct {
	mu        sync.Mutex
	myID      string
	startTime string
	devices   []syncthing.DeviceConfig
	folders   []syncthing.FolderConfig
	pending   map[string]map[string]string // folder → device → label
	patches   []map[string]any
	client    *syncthing.Client
	// flap, when set, withdraws the pending offer right after the agent
	// first saw it, runs during, and offers again after back.
	flap *offerFlap
	// onNextFolders runs inside the next GET of the folder list.
	onNextFolders func()
}

type offerFlap struct {
	back   time.Duration
	during func()
}

func newFakeEngine(t *testing.T) *fakeEngine {
	t.Helper()
	e := &fakeEngine{
		myID:      testMyID,
		startTime: "2026-10-06T10:00:00Z",
		devices:   []syncthing.DeviceConfig{{DeviceID: testMyID, Name: "laptop"}},
		pending:   map[string]map[string]string{},
	}
	srv := httptest.NewServer(http.HandlerFunc(e.serve))
	t.Cleanup(srv.Close)
	e.client = syncthing.NewClient(srv.URL, "engine-key")
	return e
}

func (e *fakeEngine) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-API-Key") != "engine-key" {
		http.Error(w, "Not Authorized", http.StatusForbidden)
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	switch p := r.URL.Path; {
	case p == "/rest/system/ping":
		write(map[string]string{"ping": "pong"})
	case p == "/rest/system/status":
		write(map[string]any{"myID": e.myID, "startTime": e.startTime})
	case p == "/rest/system/connections":
		write(map[string]any{"connections": map[string]any{}})
	case p == "/rest/config/devices" && r.Method == http.MethodGet:
		write(e.devices)
	case p == "/rest/config/devices" && r.Method == http.MethodPost:
		var d syncthing.DeviceConfig
		_ = json.NewDecoder(r.Body).Decode(&d)
		e.devices = append(e.devices, d)
	case strings.HasPrefix(p, "/rest/config/devices/") && r.Method == http.MethodPatch:
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		e.patches = append(e.patches, m)
	case p == "/rest/config/folders" && r.Method == http.MethodGet:
		if hook := e.onNextFolders; hook != nil {
			e.onNextFolders = nil
			hook()
		}
		write(e.folders)
	case p == "/rest/config/folders" && r.Method == http.MethodPost:
		var f syncthing.FolderConfig
		_ = json.NewDecoder(r.Body).Decode(&f)
		e.folders = append(e.folders, f)
		delete(e.pending, f.ID)
	case p == "/rest/cluster/pending/folders":
		out := map[string]any{}
		for id, by := range e.pending {
			offered := map[string]any{}
			for dev, label := range by {
				offered[dev] = map[string]any{"label": label}
			}
			out[id] = map[string]any{"offeredBy": offered}
		}
		write(out)
		if e.flap != nil && len(e.pending) > 0 {
			// The offer was seen once; it goes away and comes back later,
			// so the next wait is a real one.
			flap := e.flap
			e.flap = nil
			saved := e.pending
			e.pending = map[string]map[string]string{}
			go flap.during()
			time.AfterFunc(flap.back, func() {
				e.mu.Lock()
				defer e.mu.Unlock()
				for id, by := range saved {
					e.pending[id] = by
				}
			})
		}
	default:
		http.NotFound(w, r)
	}
}

func (e *fakeEngine) offer(folderID, fromDevice, label string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pending[folderID] == nil {
		e.pending[folderID] = map[string]string{}
	}
	e.pending[folderID][fromDevice] = label
}

func (e *fakeEngine) folderCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.folders)
}

// fakeHub speaks pairing protocol v1 — the Hub half of SPAKE2 from
// hub/pake, the boxes from hub/pairing — with a vault catalogue the test
// controls. A share shows up in the engine as a pending offer, like the
// Hub's Syncthing offering the folder once connected.
type fakeHub struct {
	t        *testing.T
	code     string
	id, name string
	engine   *fakeEngine
	addr     string

	mu         sync.Mutex
	vaults     []pairing.VaultInfo
	sessions   map[string]*fakeHubSession
	starts     int
	failures   int
	provisions []pairing.ProvisionPayload
	// provisionStatus answers a provision with this HTTP status instead
	// (after acting on it, like the real Hub's 500 when sealing fails).
	provisionStatus int
	// tamper changes a reply before it is sealed.
	tamper func(*pairing.HubPayload)
	// offerDelay > 0 holds the engine's pending offer back; < 0 never sends it.
	offerDelay time.Duration
	// catalogueUnreadable makes every answer carry no vault list (null), as
	// the real Hub does when it cannot read its own vaults.
	catalogueUnreadable bool
	// onProvision runs inside the n-th provision request (1-based), before
	// it is answered.
	onProvision func(n int, p pairing.ProvisionPayload)
	// beforeOffer runs right before the engine gets the pending offer, after
	// the provision reply was composed, without h.mu held: what it changes —
	// on the Hub or on this computer — is what the agent meets after it
	// decided on that reply, however long the handshake took.
	beforeOffer func()
}

type fakeHubSession struct {
	state      *pake.State
	hubConfirm []byte
	key        []byte
	lastSeq    uint64
}

func newFakeHub(t *testing.T, engine *fakeEngine, vaults ...pairing.VaultInfo) *fakeHub {
	t.Helper()
	code, err := pairing.GenerateCode()
	if err != nil {
		t.Fatal(err)
	}
	h := &fakeHub{t: t, code: code, id: testHubID, name: "Test Hub", engine: engine, vaults: vaults, sessions: map[string]*fakeHubSession{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/pair/start", h.start)
	mux.HandleFunc("POST /v1/pair/finish", h.finish)
	mux.HandleFunc("POST /v1/pair/provision", h.provision)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	h.addr = strings.TrimPrefix(srv.URL, "http://")
	return h
}

func (h *fakeHub) fail(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(pairing.ErrorResponse{Error: msg})
}

func (h *fakeHub) start(w http.ResponseWriter, r *http.Request) {
	var req pairing.StartRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	pa, err := pairing.B64Decode(req.PA)
	if err != nil {
		h.fail(w, http.StatusBadRequest, "malformed request")
		return
	}
	st, _ := pake.NewState(pake.RoleHub, pake.PasswordScalar([]byte(h.code)))
	pb, _ := st.Start()
	confirm, err := st.Finish(pa)
	if err != nil {
		h.fail(w, http.StatusBadRequest, "malformed key exchange")
		return
	}
	var id [8]byte
	_, _ = rand.Read(id[:])
	h.mu.Lock()
	h.starts++
	h.sessions[hex.EncodeToString(id[:])] = &fakeHubSession{state: st, hubConfirm: confirm}
	h.mu.Unlock()
	_ = json.NewEncoder(w).Encode(pairing.StartResponse{Session: hex.EncodeToString(id[:]), PB: pairing.B64Encode(pb)})
}

func (h *fakeHub) finish(w http.ResponseWriter, r *http.Request) {
	var req pairing.FinishRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	h.mu.Lock()
	defer h.mu.Unlock()
	sess := h.sessions[req.Session]
	if sess == nil {
		h.fail(w, http.StatusForbidden, "unknown or expired pairing session")
		return
	}
	confirm, _ := pairing.B64Decode(req.Confirm)
	key, err := sess.state.Verify(confirm)
	if err != nil {
		delete(h.sessions, req.Session)
		h.failures++
		h.fail(w, http.StatusForbidden, "pairing code rejected")
		return
	}
	sess.key = key
	box, _ := pairing.SealBox(req.Session, key, pairing.DirectionHubToDevice, h.payloadLocked(nil, ""))
	_ = json.NewEncoder(w).Encode(pairing.FinishResponse{Confirm: pairing.B64Encode(sess.hubConfirm), Box: box})
}

// payloadLocked answers like the real Hub: an empty list is [], and a list
// the Hub cannot read is left out (null).
func (h *fakeHub) payloadLocked(p *pairing.VaultInfo, errMsg string) pairing.HubPayload {
	out := pairing.HubPayload{Version: pairing.ProtocolVersion, HubDeviceID: h.id, HubName: h.name, Provisioned: p, Error: errMsg}
	if !h.catalogueUnreadable {
		out.Vaults = append([]pairing.VaultInfo{}, h.vaults...)
	}
	return out
}

func (h *fakeHub) provision(w http.ResponseWriter, r *http.Request) {
	var req pairing.ProvisionRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	h.mu.Lock()
	sess := h.sessions[req.Session]
	if sess == nil || sess.key == nil {
		h.mu.Unlock()
		h.fail(w, http.StatusForbidden, "unknown or expired pairing session")
		return
	}
	var p pairing.ProvisionPayload
	if err := pairing.OpenBox(req.Session, sess.key, pairing.DirectionDeviceToHub, req.Box, &p); err != nil || p.Seq <= sess.lastSeq {
		h.mu.Unlock()
		h.fail(w, http.StatusBadRequest, "bad provision")
		return
	}
	sess.lastSeq = p.Seq
	h.provisions = append(h.provisions, p)
	if h.onProvision != nil {
		h.onProvision(len(h.provisions), p)
	}
	var shared *pairing.VaultInfo
	errMsg := ""
	if strings.TrimSpace(p.Vault) != "" {
		idx := -1
		for i, v := range h.vaults {
			if v.ID == p.Vault || strings.EqualFold(v.Label, p.Vault) {
				idx = i
				break
			}
		}
		if idx < 0 && p.Create {
			var id [6]byte
			_, _ = rand.Read(id[:])
			h.vaults = append(h.vaults, pairing.VaultInfo{ID: "vs-" + hex.EncodeToString(id[:]), Label: p.Vault})
			idx = len(h.vaults) - 1
		}
		if idx < 0 {
			errMsg = "no vault named " + p.Vault + " on this hub"
		} else {
			v := &h.vaults[idx]
			if !contains(v.SharedWith, p.DeviceID) {
				v.SharedWith = append(v.SharedWith, p.DeviceID)
			}
			c := *v
			shared = &c
		}
	}
	reply := h.payloadLocked(shared, errMsg)
	if h.tamper != nil {
		h.tamper(&reply)
	}
	status := h.provisionStatus
	h.mu.Unlock()
	if shared != nil {
		// Outside the lock: beforeOffer may take it.
		h.sendOffer(*shared)
	}
	if status != 0 {
		h.fail(w, status, "encryption failed")
		return
	}
	box, _ := pairing.SealBox(req.Session, sess.key, pairing.DirectionHubToDevice, reply)
	_ = json.NewEncoder(w).Encode(pairing.BoxResponse{Box: box})
}

func (h *fakeHub) sendOffer(v pairing.VaultInfo) {
	if h.engine == nil || h.offerDelay < 0 {
		return
	}
	deliver := func() {
		if h.beforeOffer != nil {
			h.beforeOffer()
		}
		h.engine.offer(v.ID, h.id, v.Label)
	}
	if h.offerDelay == 0 {
		deliver()
		return
	}
	time.AfterFunc(h.offerDelay, deliver)
}

func (h *fakeHub) provisionCount(vault string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, p := range h.provisions {
		if p.Vault == vault {
			n++
		}
	}
	return n
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// testSession wires a pairing session to a fake engine and a fake Hub in a
// fresh home directory. answers, when set, is what the person types.
func testSession(t *testing.T, eng *fakeEngine, hub *fakeHub, opts pairOptions, answers ...string) (*pairSession, *bytes.Buffer) {
	t.Helper()
	// The resolved temporary directory: on macOS /var is a link to
	// /private/var, and the agent configures resolved paths.
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tmp, "home")
	lay, err := layoutFor("linux", home, envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	tm := &term{out: out}
	if answers != nil {
		tm.in = bufio.NewReader(strings.NewReader(strings.Join(answers, "\n") + "\n"))
	}
	if opts.hub == "" && hub != nil {
		opts.hub = hub.addr
	}
	s := &pairSession{
		t:    tm,
		opts: opts,
		env: pairEnv{
			goos: "linux", home: home, getenv: envOf(nil), lay: lay, engine: eng.client,
			discover:       func(context.Context) ([]pairing.DiscoveredHub, error) { return nil, nil },
			dial:           pairing.NewLocalClient,
			cloud:          func() []cloudRoot { return cloudRoots(cloudEnv{goos: "linux", home: home, getenv: envOf(nil)}) },
			userST:         func() (userSyncthing, bool) { return userSyncthing{}, false },
			unitDir:        filepath.Join(home, ".config", "systemd", "user"),
			pendingTimeout: 3 * time.Second,
			deviceName:     "Test Laptop",
			now:            time.Now,
		},
	}
	return s, out
}

func syncthingFolder(id, label, path string) syncthing.FolderConfig {
	return syncthing.FolderConfig{ID: id, Label: label, Path: path, Type: "sendreceive"}
}

func folderDeviceOf(id string) syncthing.FolderDevice { return syncthing.FolderDevice{DeviceID: id} }

// hookReader runs before once, then reads like strings.Reader: a person who
// answers only after something changed on disk.
type hookReader struct {
	before func()
	r      *strings.Reader
}

func (h *hookReader) Read(b []byte) (int, error) {
	if h.before != nil {
		h.before()
		h.before = nil
	}
	return h.r.Read(b)
}

// lineReader answers one line per Read, running before[i] ahead of line i, so
// a bufio.Reader on top of it hands out exactly one answer per question.
type lineReader struct {
	lines  []string
	before map[int]func()
	next   int
}

func (l *lineReader) Read(b []byte) (int, error) {
	if l.next >= len(l.lines) {
		return 0, io.EOF
	}
	if hook := l.before[l.next]; hook != nil {
		hook()
	}
	n := copy(b, l.lines[l.next]+"\n")
	l.next++
	return n, nil
}
