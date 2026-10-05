// Hub pairing (#174): the gomobile face of hub/pairing — the client, SPAKE2
// (hub/pake) and the code word list the vaultsync-hub CLI uses, linked from
// this repository's own hub module instead of reimplemented in Swift
// (decision 045).
//
// One pairing flow at a time, addressed by an ID from HubPairingBegin: the
// Hub keeps an authenticated session for five minutes while the user picks a
// vault, so the client lives here between the handshake and the provision.
// A newer flow ends the older one, every call checks that its flow is still
// the current one before it changes anything, and a flow runs one call at a
// time — so a late answer or a stale sheet can never act on a newer pairing.
//
// Every address that reaches a socket is a local-network IP literal: links
// and discovery answers pass pairing.LocalHubAddress, and the client
// re-checks the peer at dial time (pairing.NewLocalClient) — the handshake
// never leaves the LAN (decision 036).
//
// Nothing here accepts a share. Provisioning makes the Hub share a vault with
// this device; the share then arrives as a pending folder and goes through
// the app's accept flow with all of its guards (decisions 001, 007, 008).
//
// Results are JSON envelopes: {"v":1,"ok":true,"data":{…}} or
// {"v":1,"ok":false,"error":{"kind":"…","message":"…"}}. The message is
// diagnostic text and may carry addresses — log it as private.
//
// Lock order: the engine's mu before hubMu. Nothing here holds hubMu while it
// takes mu, and no network call runs under either lock.
package bridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/psimaker/vaultsync/hub/pairing"
	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/protocol"
)

const (
	hubEnvelopeVersion  = 1
	hubDiscoveryMaxWait = 10 * time.Second
	hubHandshakeTimeout = 25 * time.Second
	hubProvisionTimeout = 40 * time.Second
	// hubSessionDeadline is how long after a handshake this side still
	// provisions. The Hub forgets a session five minutes after it started;
	// stopping a minute earlier means a request is never sent into a session
	// that may already be gone (pairingSessionTTL in the vaultsync-hub
	// command).
	hubSessionDeadline = 4 * time.Minute
)

// Failure kinds besides pairing.Kind.
const (
	hubKindBadCode        = "badCode"        // not a pairing code at all
	hubKindBadAddress     = "badAddress"     // a Hub address that is not host[:port]
	hubKindNoNetwork      = "noNetwork"      // no interface took the discovery probe
	hubKindNoSession      = "noSession"      // provision without a handshake in this flow
	hubKindStaleFlow      = "staleFlow"      // the flow ended or a newer one began
	hubKindInProgress     = "inProgress"     // the flow is already running a call
	hubKindCancelled      = "cancelled"      // the flow ended during the call
	hubKindEngine         = "engine"         // the local engine could not add the Hub
	hubKindUnknownVault   = "unknownVault"   // not exactly one vault of the catalog
	hubKindHubRefused     = "hubRefused"     // the Hub answered, but did not share
	hubKindOutcomeUnknown = "outcomeUnknown" // the request may have reached the Hub
	hubKindProtocol       = "protocol"       // the Hub's answer is unusable
)

type hubFlow struct {
	id       string
	client   *pairing.Client // authenticated session; nil before a handshake
	hubID    protocol.DeviceID
	hubName  string
	catalog  []pairing.VaultInfo // nil when the Hub could not list its vaults
	pairedAt time.Time
	seq      uint64
	busy     bool
	abort    context.CancelFunc // the call in flight, if any
}

var (
	hubMu      sync.Mutex
	hubCurrent *hubFlow
	hubNow     = time.Now // replaced by tests
)

type hubEnvelope struct {
	V     int       `json:"v"`
	OK    bool      `json:"ok"`
	Data  any       `json:"data,omitempty"`
	Error *hubError `json:"error,omitempty"`
}

type hubError struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

type hubVaultJSON struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Files   int64  `json:"files"`
	Devices int    `json:"devices"` // devices other than the Hub that share it
}

type hubFoundJSON struct {
	Address string `json:"address"`
	Name    string `json:"name"`
}

func hubOK(data any) string {
	return hubEncode(hubEnvelope{V: hubEnvelopeVersion, OK: true, Data: data})
}

func hubFail(kind string, err error) string {
	return hubEncode(hubEnvelope{V: hubEnvelopeVersion, Error: &hubError{Kind: kind, Message: err.Error()}})
}

func hubEncode(e hubEnvelope) string {
	data, err := json.Marshal(e)
	if err != nil {
		return `{"v":1,"ok":false,"error":{"kind":"protocol","message":"encode result"}}`
	}
	return string(data)
}

// HubPairingBegin starts a pairing flow and returns its ID. An older flow
// ends: its call in flight is aborted and its session forgotten.
func HubPairingBegin() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	hubMu.Lock()
	defer hubMu.Unlock()
	hubEndLocked()
	hubCurrent = &hubFlow{id: hex.EncodeToString(b[:])}
	return hubCurrent.id
}

// HubPairingEnd ends the flow — aborting its call in flight and forgetting
// its session — if it is still the current one. Ending a flow cannot undo
// what the Hub already did (a registration, a share).
func HubPairingEnd(flow string) {
	hubMu.Lock()
	defer hubMu.Unlock()
	if hubCurrent != nil && hubCurrent.id == flow {
		hubEndLocked()
	}
}

func hubEndLocked() {
	if hubCurrent != nil && hubCurrent.abort != nil {
		hubCurrent.abort()
	}
	hubCurrent = nil
}

// HubPairingNormalizeCode returns the canonical form of a typed pairing code
// (TULIP-ANCHOR-42), or "" when the input cannot be a code — the CLI's rules:
// case, spaces, dots, commas, underscores and a missing leading zero are
// forgiven; both words must be on the word list.
func HubPairingNormalizeCode(raw string) string {
	code, err := pairing.NormalizeCode(raw)
	if err != nil {
		return ""
	}
	return code
}

// HubPairingCheckAddress validates a Hub address from a QR code or an opened
// link: data {"address":"192.168.1.20:8390"}, or a failure of kind
// "notLocal" (not a private, loopback or link-local IP literal) or
// "badAddress".
func HubPairingCheckAddress(raw string) string {
	addr, err := pairing.LocalHubAddress(raw)
	if err != nil {
		return hubFail(hubAddressKind(err), err)
	}
	return hubOK(map[string]string{"address": addr})
}

// HubPairingDiscover broadcasts the discovery probe on the pairing port and
// collects answers for waitMillis (at most ten seconds): data
// {"hubs":[{"address","name"}]} — possibly empty — or a failure of kind
// "noNetwork" when no interface took the probe (no network, or local-network
// access denied).
func HubPairingDiscover(flow string, waitMillis int) string {
	wait := time.Duration(waitMillis) * time.Millisecond
	if wait <= 0 || wait > hubDiscoveryMaxWait {
		wait = hubDiscoveryMaxWait
	}
	f, ctx, done, fail := hubAcquire(flow, wait+time.Second)
	if fail != "" {
		return fail
	}
	defer done()
	found, err := pairing.DiscoverHubs(ctx, pairing.DefaultPort, wait)
	if !hubStillCurrent(f) {
		return hubFail(hubKindCancelled, errHubFlowEnded)
	}
	if errors.Is(err, pairing.ErrNoProbeSent) {
		return hubFail(hubKindNoNetwork, err)
	}
	if err != nil {
		return hubFail(string(pairing.KindOther), err)
	}
	return hubOK(map[string]any{"hubs": hubLocalAnswers(found)})
}

// hubLocalAnswers keeps the discovery answers a device may dial: answers are
// hints from anyone on the network, so only local-network senders are
// offered — the same rule as a link's address.
func hubLocalAnswers(found []pairing.DiscoveredHub) []hubFoundJSON {
	hubs := []hubFoundJSON{}
	for _, h := range found {
		if addr, err := pairing.LocalHubAddress(h.Address); err == nil {
			hubs = append(hubs, hubFoundJSON{Address: addr, Name: h.Name})
		}
	}
	return hubs
}

// HubPairingHandshake runs the code exchange with the Hub at address and
// keeps the session in the flow: data {"hubName","hubDeviceID",
// "catalogAvailable","vaults":[{"id","label","files","devices"}]}. Failure
// kinds: "codeRejected" (wrong code — the Hub counted it),
// "authenticationFailed" (the other side does not know the code),
// "codeExpired", "codeLocked", "noCode", "sessionExpired", "rateLimited",
// "busy", "notLocal", "unreachable", "incompatible", "badCode",
// "badAddress", "staleFlow", "inProgress", "cancelled", "protocol", "other".
// The code is tried against this one address only: a code must never be
// spent on a Hub the user did not pick.
func HubPairingHandshake(flow, address, code string) string {
	addr, err := pairing.LocalHubAddress(address)
	if err != nil {
		return hubFail(hubAddressKind(err), err)
	}
	canonical, err := pairing.NormalizeCode(code)
	if err != nil {
		return hubFail(hubKindBadCode, err)
	}
	f, ctx, done, fail := hubAcquire(flow, hubHandshakeTimeout)
	if fail != "" {
		return fail
	}
	defer done()
	client := pairing.NewLocalClient(addr)
	hello, err := client.Handshake(ctx, canonical)

	hubMu.Lock()
	defer hubMu.Unlock()
	if hubCurrent != f {
		return hubFail(hubKindCancelled, errHubFlowEnded)
	}
	if err != nil {
		return hubFail(string(pairing.FailureKind(err)), err)
	}
	id, err := protocol.DeviceIDFromString(hello.HubDeviceID)
	if err != nil || id == protocol.EmptyDeviceID {
		return hubFail(hubKindProtocol, errors.New("the hub sent no valid device ID"))
	}
	f.client, f.hubID, f.hubName = client, id, pairing.SanitizeName(hello.HubName)
	f.catalog, f.pairedAt, f.seq = hello.Vaults, hubNow(), 0
	return hubOK(map[string]any{
		"hubName":     f.hubName,
		"hubDeviceID": id.String(),
		// The Hub sends no list at all (null, not []) when it could not read
		// its vaults — that is not an empty Hub.
		"catalogAvailable": hello.Vaults != nil,
		"vaults":           hubVaults(hello.Vaults),
	})
}

// HubPairingProvision adds the flow's Hub as a device here (unless it already
// is one), then asks the Hub to register this device under deviceName and
// share the vault vaultID from the handshake's catalog: data
// {"provisioned":{"id","label","files","devices"}}. An empty vaultID only
// registers this device — for a Hub whose vaults are all on this iPhone
// already: data {"registered":true}. A share then arrives as a pending
// folder; this call never accepts it. Failure kinds: "noSession",
// "sessionExpired", "unknownVault", "engine", "hubRefused" (the Hub's reason
// in the message), "outcomeUnknown" (the request may have reached the Hub —
// never retried automatically), "staleFlow", "inProgress", "cancelled",
// "unreachable", "notLocal", "protocol", "other".
func HubPairingProvision(flow, vaultID, deviceName string) string {
	f, ctx, done, fail := hubAcquire(flow, hubProvisionTimeout)
	if fail != "" {
		return fail
	}
	defer done()

	hubMu.Lock()
	client, hubID, hubName, catalog, pairedAt := f.client, f.hubID, f.hubName, f.catalog, f.pairedAt
	hubMu.Unlock()
	if client == nil {
		return hubFail(hubKindNoSession, errors.New("no pairing session — enter the code again"))
	}
	if hubNow().Sub(pairedAt) > hubSessionDeadline {
		return hubFail(string(pairing.KindSessionExpired), errors.New("the pairing session is about to expire — enter the code again"))
	}
	registerOnly := vaultID == ""
	if !registerOnly {
		if err := hubCheckVault(catalog, vaultID); err != nil {
			return hubFail(hubKindUnknownVault, err)
		}
	}

	myID, err := ensureHubDevice(f, hubID, hubName)
	if errors.Is(err, errHubFlowEnded) {
		return hubFail(hubKindCancelled, err)
	}
	if err != nil {
		return hubFail(hubKindEngine, err)
	}

	hubMu.Lock()
	if hubCurrent != f {
		hubMu.Unlock()
		return hubFail(hubKindCancelled, errHubFlowEnded)
	}
	f.seq++
	seq := f.seq
	hubMu.Unlock()

	reply, err := client.Provision(ctx, seq, myID, deviceName, vaultID, false)
	if err != nil {
		kind := hubProvisionFailureKind(err)
		if !hubStillCurrent(f) && kind != hubKindOutcomeUnknown {
			return hubFail(hubKindCancelled, errHubFlowEnded)
		}
		return hubFail(kind, err)
	}
	// The Hub has acted by now; report what it did even if the flow ended.
	if registerOnly {
		// Only a failed registration is a refusal; an error the Hub reports
		// after registering (reading its own state for the reply) is not.
		if strings.HasPrefix(reply.Error, pairing.RegistrationRefusedPrefix) {
			return hubFail(hubKindHubRefused, errors.New(reply.Error))
		}
		return hubOK(map[string]any{"registered": true})
	}
	if reply.Provisioned == nil {
		if reply.Error != "" {
			return hubFail(hubKindHubRefused, errors.New(reply.Error))
		}
		return hubFail(hubKindHubRefused, errors.New("the hub did not share a vault"))
	}
	if reply.Provisioned.ID != vaultID {
		return hubFail(hubKindProtocol, errors.New("the hub shared a different vault than the one chosen"))
	}
	// A shared vault is the answer, even when the Hub also reports an error:
	// it fills Error when reading its own state for the reply fails after
	// the share went through (its device ID, its vault list).
	return hubOK(map[string]any{"provisioned": hubVault(*reply.Provisioned)})
}

// hubAcquire claims the flow for one call: it must be the current flow and
// idle. done releases it and cancels the call's context.
func hubAcquire(flow string, timeout time.Duration) (*hubFlow, context.Context, func(), string) {
	hubMu.Lock()
	defer hubMu.Unlock()
	f := hubCurrent
	if f == nil || flow == "" || f.id != flow {
		return nil, nil, nil, hubFail(hubKindStaleFlow, errors.New("this pairing has ended — start again"))
	}
	if f.busy {
		return nil, nil, nil, hubFail(hubKindInProgress, errors.New("a pairing step is still running"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	f.busy, f.abort = true, cancel
	done := func() {
		cancel()
		hubMu.Lock()
		f.busy, f.abort = false, nil
		hubMu.Unlock()
	}
	return f, ctx, done, ""
}

func hubStillCurrent(f *hubFlow) bool {
	hubMu.Lock()
	defer hubMu.Unlock()
	return hubCurrent == f
}

func hubAddressKind(err error) string {
	if errors.Is(err, pairing.ErrNotLocal) {
		return string(pairing.KindNotLocal)
	}
	return hubKindBadAddress
}

// hubCheckVault requires vaultID to name exactly one vault of the catalog.
// The Hub resolves a selector by ID or, case-insensitively, by label, so an ID
// that is also another vault's label would be ambiguous.
func hubCheckVault(catalog []pairing.VaultInfo, vaultID string) error {
	if vaultID == "" {
		return errors.New("no vault chosen")
	}
	matches := 0
	for _, v := range catalog {
		if v.ID == vaultID || strings.EqualFold(v.Label, vaultID) {
			matches++
		}
	}
	switch {
	case matches == 0:
		return errors.New("the vault is not on this hub")
	case matches > 1:
		return errors.New("the vault cannot be told apart from another vault on this hub")
	}
	return nil
}

// hubProvisionFailureKind sorts a failed provision: a request the Hub refused
// before acting (4xx), or a connection that never opened, is a definite
// failure; anything that broke after the request may have left — a server
// error (the Hub answers 500 when sealing its reply fails, after it shared),
// a timeout, a reset connection, an answer that does not decrypt — leaves
// the outcome unknown.
func hubProvisionFailureKind(err error) string {
	var apiErr *pairing.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Status >= 500 {
			return hubKindOutcomeUnknown
		}
		return string(pairing.FailureKind(err))
	}
	if errors.Is(err, pairing.ErrNotLocal) {
		return string(pairing.KindNotLocal)
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return string(pairing.KindUnreachable)
	}
	return hubKindOutcomeUnknown
}

var errHubFlowEnded = errors.New("pairing ended")

// ensureHubDevice makes the Hub a configured device so its share can arrive,
// and returns this device's ID. A Hub that is already configured stays as it
// is — name, addresses, paused state: the user may have changed them. The
// flow is checked under mu, right before the change: a pairing that ended
// while this call waited for the engine changes nothing.
func ensureHubDevice(f *hubFlow, id protocol.DeviceID, name string) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	if !hubStillCurrent(f) {
		return "", errHubFlowEnded
	}
	if !engineUpLocked() || stCfg == nil {
		return "", errors.New("syncthing not running")
	}
	if id == stMyID {
		return "", errors.New("the hub announced this device's own ID")
	}
	for _, dev := range stCfg.Devices() {
		if dev.DeviceID == id {
			return stMyID.String(), nil
		}
	}
	if err := commitConfigLocked(func(cfg *config.Configuration) {
		cfg.Devices = append(cfg.Devices, peerDeviceConfig(id, name))
	}); err != nil {
		return "", fmt.Errorf("modify config: %v", err)
	}
	return stMyID.String(), nil
}

func hubVault(v pairing.VaultInfo) hubVaultJSON {
	return hubVaultJSON{ID: v.ID, Label: v.Label, Files: v.Files, Devices: len(v.SharedWith)}
}

func hubVaults(in []pairing.VaultInfo) []hubVaultJSON {
	out := make([]hubVaultJSON, 0, len(in))
	for _, v := range in {
		out = append(out, hubVault(v))
	}
	return out
}
