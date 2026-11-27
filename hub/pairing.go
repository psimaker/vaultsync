package main

import (
	"bytes"
	"context"
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

	"github.com/psimaker/vaultsync/hub/pairing"
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
	pairingMaxBody      = pairing.MaxBody
	protocolVersion     = pairing.ProtocolVersion
)

// The wire types live in the pairing package, which the device side (the
// `pair` command and, through gomobile, the iOS app) shares with this server.
type (
	startRequest     = pairing.StartRequest
	startResponse    = pairing.StartResponse
	finishRequest    = pairing.FinishRequest
	finishResponse   = pairing.FinishResponse
	provisionRequest = pairing.ProvisionRequest
	boxResponse      = pairing.BoxResponse
	hubPayload       = pairing.HubPayload
	provisionPayload = pairing.ProvisionPayload
	errorResponse    = pairing.ErrorResponse
)

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
	code          []byte // the scalar of the code the session began with
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

	// countMu orders every attempt on a code: its check against the limit,
	// the verification and the count are one step (decision 097).
	countMu sync.Mutex
	// failures counts wrong attempts per code, keyed by the code's scalar,
	// for the life of this process; the state file holds the same count
	// whenever its write succeeds. Codes are opened by hand, so the map
	// holds a handful of entries at most.
	failures map[string]int
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
		failures: map[string]int{},
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
	if err == nil && s.lockedHere(code.Scalar) {
		err = errCodeLocked
	}
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
	sess, err := s.newSession(hub, hubConfirm, code.Scalar)
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
	// The limit on wrong codes is held at every attempt: the check, the
	// verification and the count are one step, whatever session the attempt
	// comes in on.
	s.countMu.Lock()
	if s.lockedLocked(sess.code) {
		s.countMu.Unlock()
		s.dropSession(sess.id)
		writeError(w, http.StatusForbidden, errCodeLocked.Error())
		return
	}
	key, err := sess.state.Verify(confirm)
	if err != nil {
		s.countFailureLocked(sess.code)
		s.countMu.Unlock()
		s.dropSession(sess.id)
		writeError(w, http.StatusForbidden, "pairing code rejected")
		return
	}
	s.countMu.Unlock()
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
	if strings.TrimSpace(p.Vault) != "" {
		provisioned, provErr = s.provision(ctx, sess, p)
	} else if err := s.prov.ensureDevice(ctx, sess.deviceID, sess.deviceName); err != nil {
		provErr = "could not register the device: " + err.Error()
	}
	reply := s.hubPayload(ctx, provisioned, provErr)
	box, err := sealBox(sess, "hub->device", reply)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encryption failed")
		return
	}
	writeJSON(w, http.StatusOK, boxResponse{Box: box})
}

// provision shares an existing vault, or a new one it creates, with the
// device. The record of the pairing comes first (decision 097): a pairing
// whose record cannot be written is refused before the device is registered
// or a vault created, so it leaves nothing behind. A later step that fails
// says in the reply and the log what exists by then. Errors are reported
// inside the encrypted reply so the device can show them; they never abort
// the session.
func (s *pairingServer) provision(ctx context.Context, sess *pairingSession, p provisionPayload) (*vaultInfo, string) {
	folders, err := s.prov.client.Folders(ctx)
	if err != nil {
		return nil, "could not read vaults: " + err.Error()
	}
	target := findVaultByLabel(folders, p.Vault)
	label := strings.TrimSpace(p.Vault)
	if target == nil {
		if !p.Create {
			return nil, fmt.Sprintf("no vault named %q on this hub", p.Vault)
		}
		if _, err := slugify(p.Vault); err != nil {
			return nil, "could not create the vault: " + err.Error()
		}
	} else {
		label = target.Label
	}
	if _, err := s.store.update(func(st *hubState) error {
		st.recordDevice(pairedDevice{
			DeviceID: sess.deviceID,
			Name:     sess.deviceName,
			PairedAt: s.now(),
			Vaults:   []string{label},
		})
		return nil
	}); err != nil {
		s.logf("pairing: could not record the pairing, nothing was changed: %v", err)
		return nil, "could not record the pairing in the Hub's state: " + err.Error() + " (nothing was changed)"
	}
	if err := s.prov.ensureDevice(ctx, sess.deviceID, sess.deviceName); err != nil {
		s.logf("pairing: could not register a device whose pairing is recorded: %v", err)
		return nil, pairing.RegistrationRefusedPrefix + ": " + err.Error() + " (the Hub recorded this pairing; nothing else was changed)"
	}
	if target == nil {
		// Pairing may create only fresh, empty directories; adopting existing
		// content is an operator decision made on the Hub's own shell.
		created, err := s.prov.createVault(ctx, p.Vault, false)
		if err != nil {
			s.logf("pairing: could not create a vault for a registered device: %v", err)
			return nil, "could not create the vault: " + err.Error() + " (this device is registered with the Hub; nothing was shared)"
		}
		target = &created
		s.logf("pairing: created vault %s for a paired device", target.ID)
	}
	if err := s.prov.shareVault(ctx, target.ID, sess.deviceID); err != nil {
		s.logf("pairing: could not share vault %s with a registered device: %v", target.ID, err)
		return nil, "could not share the vault: " + err.Error() + fmt.Sprintf(" (this device is registered with the Hub and the vault %q exists; nothing was shared)", target.Label)
	}
	s.logf("pairing: shared vault %s with a paired device", target.ID)
	// The device decides whether it may accept into a non-empty directory
	// from this count; unknown (-1) makes it fail closed, and 0 is proven
	// (vaultFiles, #214).
	return &vaultInfo{ID: target.ID, Label: target.Label, Files: s.prov.vaultFiles(ctx, *target)}, ""
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

func (s *pairingServer) newSession(hub *pake.State, hubConfirm, code []byte) (*pairingSession, error) {
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
	sess := &pairingSession{id: b64e(id[:]), state: hub, hubConfirm: hubConfirm, expires: s.now().Add(pairingSessionTTL), code: code}
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

// countFailureLocked counts one wrong attempt on the code whose scalar is
// code, with countMu held: the new count is the higher of this process's and
// the state file's, plus one. This process keeps it, and the file is written
// with it, so each attempt is one step also when the write fails, and a
// failed write is still counted here. An attempt on a session from an older
// code counts against that code only.
func (s *pairingServer) countFailureLocked(code []byte) {
	next := s.failures[string(code)] + 1
	_, err := s.store.update(func(st *hubState) error {
		if st.Pairing == nil || !bytes.Equal(st.Pairing.Scalar, code) {
			return nil
		}
		if st.Pairing.Failures+1 > next {
			next = st.Pairing.Failures + 1
		}
		st.Pairing.Failures = next
		if next >= maxCodeFailures {
			s.logf("pairing: code locked after %d wrong attempts — issue a new one with `vaultsync-hub code`", next)
		}
		return nil
	})
	s.failures[string(code)] = next
	if err != nil {
		s.logf("pairing: could not write the failed attempt to the state file, counted here: %v", err)
	}
}

// lockedLocked reports, with countMu held, whether the code whose scalar is
// code has reached the limit: in this process's count or in the state file's.
func (s *pairingServer) lockedLocked(code []byte) bool {
	if s.failures[string(code)] >= maxCodeFailures {
		return true
	}
	st, err := s.store.load()
	return err == nil && st.Pairing != nil && bytes.Equal(st.Pairing.Scalar, code) && st.Pairing.Failures >= maxCodeFailures
}

// lockedHere reports whether this process counted the limit for the code
// whose scalar is code, and then brings the state file up to that count
// where its write had failed.
func (s *pairingServer) lockedHere(code []byte) bool {
	s.countMu.Lock()
	defer s.countMu.Unlock()
	counted := s.failures[string(code)]
	if counted < maxCodeFailures {
		return false
	}
	_, err := s.store.update(func(st *hubState) error {
		if st.Pairing != nil && bytes.Equal(st.Pairing.Scalar, code) && st.Pairing.Failures < counted {
			st.Pairing.Failures = counted
		}
		return nil
	})
	if err != nil {
		s.logf("pairing: could not write the failed attempts to the state file: %v", err)
	}
	return true
}

// --- boxes ------------------------------------------------------------------

// Boxes are AES-256-GCM under the session key with "<session>|<direction>" as
// associated data (pairing.SealBox / pairing.OpenBox).

func sealBox(sess *pairingSession, direction string, v any) (string, error) {
	return pairing.SealBox(sess.id, sess.key, direction, v)
}

func openBox(sess *pairingSession, direction string, box string, v any) error {
	return pairing.OpenBox(sess.id, sess.key, direction, box, v)
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
