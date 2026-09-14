package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/psimaker/vaultsync/hub/pake"
)

// Pairing protocol v1 — three requests over plain HTTP on the LAN:
//
//	POST /v1/pair/start      {pa}                → {session, pb}
//	POST /v1/pair/finish     {session, confirm}  → {confirm, box}
//	POST /v1/pair/provision  {session, box}      → {box}
//
// start/finish are the SPAKE2 exchange (pake package): the device's
// confirmation MAC arrives in finish, the Hub verifies it *before* releasing
// its own MAC or anything encrypted, and counts a failure against the pairing
// code on mismatch. Only after both confirmations are exchanged does either
// side use the session key — the RFC 9382 ordering.
//
// Every "box" is AES-256-GCM under the session key with the session ID and a
// direction label as associated data, so a box can be neither replayed into
// another session nor reflected back to its sender. provision is only accepted
// on a session whose finish succeeded and carries a strictly increasing
// sequence number.
//
// Nothing here relies on transport security: the LAN is assumed hostile
// (decision 036). Session keys live in memory only and die with the session.

const (
	pairingSessionTTL   = 5 * time.Minute
	pairingMaxSessions  = 32
	pairingStartsPerMin = 10
	pairingMaxBody      = 64 << 10
	protocolVersion     = 1
)

type startRequest struct {
	PA string `json:"pa"`
}

type startResponse struct {
	Session string `json:"session"`
	PB      string `json:"pb"`
}

type finishRequest struct {
	Session string `json:"session"`
	Confirm string `json:"confirm"`
}

type finishResponse struct {
	Confirm string `json:"confirm"`
	Box     string `json:"box"`
}

type provisionRequest struct {
	Session string `json:"session"`
	Box     string `json:"box"`
}

type boxResponse struct {
	Box string `json:"box"`
}

// hubPayload is the Hub's encrypted answer to finish and to every provision.
type hubPayload struct {
	Version     int         `json:"version"`
	HubDeviceID string      `json:"hubDeviceID"`
	HubName     string      `json:"hubName"`
	Vaults      []vaultInfo `json:"vaults"`
	Provisioned *vaultInfo  `json:"provisioned,omitempty"`
	Error       string      `json:"error,omitempty"`
}

// provisionPayload identifies the device and asks the Hub to share (or create
// and share) one vault with it. An empty Vault only registers the device.
type provisionPayload struct {
	Version  int    `json:"version"`
	Seq      uint64 `json:"seq"`
	DeviceID string `json:"deviceID"`
	Name     string `json:"name"`
	Vault    string `json:"vault"`
	Create   bool   `json:"create"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// --- sessions ---------------------------------------------------------------

type pairingSession struct {
	id            string
	state         *pake.State
	hubConfirm    []byte // cB, released to the device only after its cA verified
	key           []byte
	deviceID      string
	deviceName    string
	authenticated bool
	lastSeq       uint64
	expires       time.Time
}

type pairingServer struct {
	store   *stateStore
	prov    *provisioner
	hubName string
	version string
	now     func() time.Time
	logf    func(string, ...any)

	mu       sync.Mutex
	sessions map[string]*pairingSession
	starts   map[string][]time.Time // remote IP → recent start timestamps
}

func newPairingServer(store *stateStore, prov *provisioner, hubName, version string) *pairingServer {
	return &pairingServer{
		store:    store,
		prov:     prov,
		hubName:  hubName,
		version:  version,
		now:      time.Now,
		logf:     log.Printf,
		sessions: map[string]*pairingSession{},
		starts:   map[string][]time.Time{},
	}
}

func (s *pairingServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/hub", s.handleInfo)
	mux.HandleFunc("POST /v1/pair/start", s.handleStart)
	mux.HandleFunc("POST /v1/pair/finish", s.handleFinish)
	mux.HandleFunc("POST /v1/pair/provision", s.handleProvision)
	return localNetworkOnly(mux)
}

// localNetworkOnly refuses every request whose source address is not a
// private, loopback or link-local address. The listener binds all interfaces
// (host networking), so this is the defence for an operator who forwards the
// port against the documentation: an Internet client gets a 403 before any
// pairing state is touched. It is not a substitute for not forwarding — a
// router that rewrites source addresses would still defeat it.
func localNetworkOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLocalNetworkAddress(remoteIP(r)) {
			writeError(w, http.StatusForbidden, "pairing is only available from the local network")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isLocalNetworkAddress(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

func (s *pairingServer) handleInfo(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name":     s.hubName,
		"version":  s.version,
		"protocol": protocolVersion,
	})
}

func (s *pairingServer) handleStart(w http.ResponseWriter, r *http.Request) {
	var req startRequest
	if !readJSON(w, r, &req) {
		return
	}
	pa, err := b64d(req.PA)
	if err != nil || len(pa) != pake.PointSize {
		writeError(w, http.StatusBadRequest, "malformed request")
		return
	}
	if !s.allowStart(remoteIP(r)) {
		writeError(w, http.StatusTooManyRequests, "too many pairing attempts from this address — wait a minute")
		return
	}
	st, err := s.store.load()
	if err != nil {
		s.logf("pairing: state unreadable: %v", err)
		writeError(w, http.StatusInternalServerError, "hub state unavailable")
		return
	}
	code, err := st.activePairing(s.now())
	if err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	hub, err := pake.NewState(pake.RoleHub, code.Scalar)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "pairing unavailable")
		return
	}
	pb, err := hub.Start()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "pairing unavailable")
		return
	}
	// The Hub finishes its half now; the confirmation it sends back is only
	// released after the device proves knowledge of the code (finish).
	hubConfirm, err := hub.Finish(pa)
	if err != nil {
		writeError(w, http.StatusBadRequest, "malformed key exchange")
		return
	}
	sess, err := s.newSession(hub, hubConfirm)
	if err != nil {
		writeError(w, http.StatusTooManyRequests, "the hub is busy pairing — try again shortly")
		return
	}
	writeJSON(w, http.StatusOK, startResponse{Session: sess.id, PB: b64e(pb)})
}

func (s *pairingServer) handleFinish(w http.ResponseWriter, r *http.Request) {
	var req finishRequest
	if !readJSON(w, r, &req) {
		return
	}
	confirm, err := b64d(req.Confirm)
	if err != nil {
		writeError(w, http.StatusBadRequest, "malformed request")
		return
	}
	sess := s.session(req.Session, false)
	if sess == nil {
		writeError(w, http.StatusForbidden, "unknown or expired pairing session")
		return
	}
	key, err := sess.state.Verify(confirm)
	if err != nil {
		s.dropSession(sess.id)
		s.countFailure()
		writeError(w, http.StatusForbidden, "pairing code rejected")
		return
	}
	sess.key = key
	sess.authenticated = true

	reply := s.hubPayload(r.Context(), nil, "")
	box, err := sealBox(sess, "hub->device", reply)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encryption failed")
		return
	}
	writeJSON(w, http.StatusOK, finishResponse{Confirm: b64e(sess.hubConfirm), Box: box})
}

func (s *pairingServer) handleProvision(w http.ResponseWriter, r *http.Request) {
	var req provisionRequest
	if !readJSON(w, r, &req) {
		return
	}
	sess := s.session(req.Session, true)
	if sess == nil {
		writeError(w, http.StatusForbidden, "unknown or expired pairing session")
		return
	}
	var p provisionPayload
	if err := openBox(sess, "device->hub", req.Box, &p); err != nil {
		writeError(w, http.StatusBadRequest, "undecryptable payload")
		return
	}
	if p.Version != protocolVersion || !looksLikeDeviceID(p.DeviceID) {
		writeError(w, http.StatusBadRequest, "unsupported device payload")
		return
	}
	if p.Seq <= sess.lastSeq {
		writeError(w, http.StatusBadRequest, "replayed request")
		return
	}
	sess.lastSeq = p.Seq
	sess.deviceID = p.DeviceID
	sess.deviceName = sanitizeName(p.Name)

	ctx := r.Context()
	var provisioned *vaultInfo
	var provErr string
	if err := s.prov.ensureDevice(ctx, sess.deviceID, sess.deviceName); err != nil {
		provErr = "could not register the device: " + err.Error()
	} else if strings.TrimSpace(p.Vault) != "" {
		provisioned, provErr = s.provision(ctx, sess, p)
	}
	reply := s.hubPayload(ctx, provisioned, provErr)
	box, err := sealBox(sess, "hub->device", reply)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encryption failed")
		return
	}
	writeJSON(w, http.StatusOK, boxResponse{Box: box})
}

// provision shares an existing vault or creates a new one, then records the
// device in the Hub state. Errors are reported inside the encrypted reply so
// the device can show them; they never abort the session.
func (s *pairingServer) provision(ctx context.Context, sess *pairingSession, p provisionPayload) (*vaultInfo, string) {
	folders, err := s.prov.client.Folders(ctx)
	if err != nil {
		return nil, "could not read vaults: " + err.Error()
	}
	target := findVaultByLabel(folders, p.Vault)
	if target == nil {
		if !p.Create {
			return nil, fmt.Sprintf("no vault named %q on this hub", p.Vault)
		}
		// Pairing may create only fresh, empty directories; adopting existing
		// content is an operator decision made on the Hub's own shell.
		created, err := s.prov.createVault(ctx, p.Vault, false)
		if err != nil {
			return nil, "could not create the vault: " + err.Error()
		}
		target = &created
	}
	if err := s.prov.shareVault(ctx, target.ID, sess.deviceID); err != nil {
		return nil, "could not share the vault: " + err.Error()
	}
	if _, err := s.store.update(func(st *hubState) error {
		st.recordDevice(pairedDevice{
			DeviceID: sess.deviceID,
			Name:     sess.deviceName,
			PairedAt: s.now(),
			Vaults:   []string{target.Label},
		})
		return nil
	}); err != nil {
		s.logf("pairing: could not record device: %v", err)
	}
	s.logf("pairing: shared vault %s with a paired device", target.ID)
	// The device decides whether it may accept into a non-empty directory
	// from this count; unknown (-1) makes it fail closed.
	files := int64(-1)
	if st, err := s.prov.client.DBStatus(ctx, target.ID); err == nil {
		files = st.LocalFiles
	}
	return &vaultInfo{ID: target.ID, Label: target.Label, Files: files}, ""
}

func (s *pairingServer) hubPayload(ctx context.Context, provisioned *vaultInfo, errMsg string) hubPayload {
	out := hubPayload{Version: protocolVersion, HubName: s.hubName, Provisioned: provisioned, Error: errMsg}
	if id, err := s.prov.client.MyID(ctx); err == nil {
		out.HubDeviceID = id
	} else if out.Error == "" {
		out.Error = "hub could not read its own device ID: " + err.Error()
	}
	if vaults, err := s.prov.listVaults(ctx); err == nil {
		out.Vaults = vaults
	}
	return out
}

// --- session bookkeeping ----------------------------------------------------

func (s *pairingServer) newSession(hub *pake.State, hubConfirm []byte) (*pairingSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	if len(s.sessions) >= pairingMaxSessions {
		return nil, errors.New("too many sessions")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	sess := &pairingSession{id: b64e(id[:]), state: hub, hubConfirm: hubConfirm, expires: s.now().Add(pairingSessionTTL)}
	s.sessions[sess.id] = sess
	return sess, nil
}

func (s *pairingServer) session(id string, needAuth bool) *pairingSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	sess, ok := s.sessions[id]
	if !ok || (needAuth && !sess.authenticated) || (!needAuth && sess.authenticated) {
		return nil
	}
	return sess
}

func (s *pairingServer) dropSession(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, id)
}

func (s *pairingServer) expireLocked() {
	now := s.now()
	for id, sess := range s.sessions {
		if now.After(sess.expires) {
			delete(s.sessions, id)
		}
	}
}

func (s *pairingServer) allowStart(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	recent := s.starts[ip][:0]
	for _, t := range s.starts[ip] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= pairingStartsPerMin {
		s.starts[ip] = recent
		return false
	}
	s.starts[ip] = append(recent, now)
	return true
}

func (s *pairingServer) countFailure() {
	_, err := s.store.update(func(st *hubState) error {
		if st.Pairing != nil {
			st.Pairing.Failures++
			if st.Pairing.Failures >= maxCodeFailures {
				s.logf("pairing: code locked after %d wrong attempts — issue a new one with `vaultsync-hub code`", st.Pairing.Failures)
			}
		}
		return nil
	})
	if err != nil {
		s.logf("pairing: could not record failed attempt: %v", err)
	}
}

// --- boxes ------------------------------------------------------------------

func aead(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func sealBox(sess *pairingSession, direction string, v any) (string, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	g, err := aead(sess.key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ad := []byte(sess.id + "|" + direction)
	return b64e(append(nonce, g.Seal(nil, nonce, plain, ad)...)), nil
}

func openBox(sess *pairingSession, direction string, box string, v any) error {
	raw, err := b64d(box)
	if err != nil {
		return err
	}
	g, err := aead(sess.key)
	if err != nil {
		return err
	}
	if len(raw) < g.NonceSize() {
		return errors.New("box too short")
	}
	ad := []byte(sess.id + "|" + direction)
	plain, err := g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], ad)
	if err != nil {
		return err
	}
	return json.Unmarshal(plain, v)
}

// --- helpers ----------------------------------------------------------------

func b64e(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func b64d(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, pairingMaxBody+1))
	if err != nil || len(body) > pairingMaxBody {
		writeError(w, http.StatusRequestEntityTooLarge, "request too large")
		return false
	}
	if err := json.Unmarshal(body, v); err != nil {
		writeError(w, http.StatusBadRequest, "malformed JSON")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// looksLikeDeviceID accepts the canonical Syncthing device ID form: 56
// base32 characters in 8 dash-separated groups of 7 (check digits included).
func looksLikeDeviceID(id string) bool {
	groups := strings.Split(id, "-")
	if len(groups) != 8 {
		return false
	}
	for _, g := range groups {
		if len(g) != 7 {
			return false
		}
		for _, r := range g {
			if !(r >= 'A' && r <= 'Z') && !(r >= '2' && r <= '7') {
				return false
			}
		}
	}
	return true
}
