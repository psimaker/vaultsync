package bridge

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/protocol"
)

// A vault removed here while a device still shared it was offered again only
// once a new connection to that device was set up, in practice after the app
// was closed and opened again (#341, #316). The engine writes a pending entry
// when a device's cluster config arrives, and a device sends one when a
// connection is set up or when it starts or stops sharing a folder with us.
// A removal is neither. Fork patch 009 has the engine offer the folder again
// from the device's last cluster config on the connection that is up, with
// the index start info that cluster config gave, so that nothing has to be
// closed and an accept syncs on that same connection.
//
// The tests run the bridge against a real peer. VAULTSYNC_BRIDGE_PEER_BIN
// runs them against another Syncthing binary, such as a stock 1.x a Hub may
// still run.

const issue341Note = "# Note\n\nwritten on the peer before it shared the vault\n"

func issue341ID(vault string) string { return "issue341-" + vault }

// issue341Events keeps the events the bridge's engine logs about index
// exchange, pulls and cluster configs, for a test to read afterwards.
type issue341Events struct {
	mu   sync.Mutex
	seen []events.Event
}

const issue341EventMask = events.RemoteIndexUpdated | events.RemoteDownloadProgress | events.ItemStarted | events.ClusterConfigReceived

func watchIssue341Events(t *testing.T) *issue341Events {
	t.Helper()
	mu.Lock()
	logger := stEvLogger
	mu.Unlock()
	w := &issue341Events{}
	sub := logger.Subscribe(issue341EventMask)
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case e, ok := <-sub.C():
				if !ok {
					return
				}
				w.mu.Lock()
				w.seen = append(w.seen, e)
				w.mu.Unlock()
			case <-stop:
				return
			}
		}
	}()
	t.Cleanup(func() {
		close(stop)
		sub.Unsubscribe()
	})
	return w
}

// count returns how many events have been seen so far: a mark to read from.
func (w *issue341Events) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.seen)
}

// about returns the events since mark of the given type whose data names
// the folder (and the device, when one is given).
func (w *issue341Events) about(mark int, typ events.EventType, folder, device string) []events.Event {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []events.Event
	for _, e := range w.seen[mark:] {
		if e.Type != typ {
			continue
		}
		f, d := issue341EventFolderAndDevice(e.Data)
		if (folder == "" || f == folder) && (device == "" || d == device) {
			out = append(out, e)
		}
	}
	return out
}

func issue341EventFolderAndDevice(data any) (folder, device string) {
	switch d := data.(type) {
	case map[string]interface{}:
		folder, _ = d["folder"].(string)
		device, _ = d["device"].(string)
	case map[string]string:
		folder, device = d["folder"], d["device"]
	default:
		// ClusterConfigReceivedEventData and others: read through JSON.
		raw, _ := json.Marshal(data)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		folder, _ = m["folder"].(string)
		device, _ = m["device"].(string)
	}
	return folder, device
}

// issue341Rig is a real peer that shares some vaults and the bridge that has
// accepted each of them under one selected folder.
type issue341Rig struct {
	peer      *peerInstance
	bridgeID  string
	configDir string
	selected  string
	ev        *issue341Events
}

func newIssue341Rig(t *testing.T, vaults ...string) *issue341Rig {
	t.Helper()
	peer := startPeer(t, peerBinary(t), "issue341-peer")
	r := &issue341Rig{peer: peer}
	for _, vault := range vaults {
		dir := r.peerDir(vault)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte(issue341Note), 0o600); err != nil {
			t.Fatal(err)
		}
		peer.mustDo(t, http.MethodPost, "/rest/config/folders", map[string]any{
			"id":               issue341ID(vault),
			"label":            vault,
			"path":             dir,
			"type":             "sendreceive",
			"rescanIntervalS":  3600,
			"fsWatcherEnabled": false,
			"devices":          []map[string]any{},
		}, nil)
		peer.scanAndSettle(t, issue341ID(vault))
	}

	r.configDir = testConfigDir(t)
	r.selected = filepath.Join(r.configDir, "selected")
	if errMsg := StartSyncthing(r.configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	captureBridgeLog(t, filepath.Join(r.configDir, "bridge-engine.log"))
	r.ev = watchIssue341Events(t)
	bridgeListen := "tcp://127.0.0.1:" + strconv.Itoa(freePort(t))
	mu.Lock()
	err := commitConfigLocked(func(cfg *config.Configuration) {
		cfg.Options.RawListenAddresses = []string{bridgeListen}
		cfg.Options.GlobalAnnEnabled = false
		cfg.Options.LocalAnnEnabled = false
		cfg.Options.RelaysEnabled = false
		cfg.Options.NATEnabled = false
	})
	mu.Unlock()
	if err != nil {
		t.Fatalf("pin bridge to loopback: %v", err)
	}
	r.bridgeID = DeviceID()
	if errMsg := AddDevice(peer.id, "Peer"); errMsg != "" {
		t.Fatalf("AddDevice(peer) = %q", errMsg)
	}
	mu.Lock()
	err = commitConfigLocked(func(cfg *config.Configuration) {
		for i := range cfg.Devices {
			if cfg.Devices[i].DeviceID.String() == peer.id {
				cfg.Devices[i].Addresses = []string{peer.listen}
			}
		}
	})
	mu.Unlock()
	if err != nil {
		t.Fatalf("set peer address: %v", err)
	}
	peer.mustDo(t, http.MethodPost, "/rest/config/devices", map[string]any{
		"deviceID":  r.bridgeID,
		"name":      "Bridge",
		"addresses": []string{bridgeListen},
	}, nil)
	for _, vault := range vaults {
		r.setPeerSharing(t, vault, true)
	}
	waitFor(t, "bridge connected to peer", 60*time.Second, func() bool {
		return bridgeConnected(peer.id)
	})
	for _, vault := range vaults {
		waitFor(t, "the first offer of "+vault, 60*time.Second, func() bool {
			return r.offered(vault)
		})
		if errMsg := acceptAsDirectChild(issue341ID(vault), vault, r.bridgeDir(vault), false); errMsg != "" {
			t.Fatalf("accept %s: %q", vault, errMsg)
		}
	}
	for _, vault := range vaults {
		r.waitBridgeHolds(t, vault, r.bridgeDir(vault), "note.md", issue341Note)
	}
	return r
}

func (r *issue341Rig) peerDir(vault string) string   { return filepath.Join(r.peer.home, vault) }
func (r *issue341Rig) bridgeDir(vault string) string { return filepath.Join(r.selected, vault) }

// setPeerSharing shares the peer's vault with the bridge, or stops sharing
// it, as a person does on the peer.
func (r *issue341Rig) setPeerSharing(t *testing.T, vault string, shared bool) {
	t.Helper()
	var folderCfg map[string]any
	r.peer.mustDo(t, http.MethodGet, "/rest/config/folders/"+issue341ID(vault), nil, &folderCfg)
	devices := []map[string]any{{"deviceID": r.peer.id}}
	if shared {
		devices = append(devices, map[string]any{"deviceID": r.bridgeID})
	}
	folderCfg["devices"] = devices
	r.peer.mustDo(t, http.MethodPut, "/rest/config/folders/"+issue341ID(vault), folderCfg, nil)
}

// setPeerPaused pauses or resumes the peer's vault.
func (r *issue341Rig) setPeerPaused(t *testing.T, vault string, paused bool) {
	t.Helper()
	r.peer.mustDo(t, http.MethodPatch, "/rest/config/folders/"+issue341ID(vault), map[string]any{"paused": paused}, nil)
}

// offered reports whether the vault's share is among the pending offers, as
// one the peer made.
func (r *issue341Rig) offered(vault string) bool {
	var pending []PendingFolderInfo
	if err := json.Unmarshal([]byte(GetPendingFoldersJSON()), &pending); err != nil {
		return false
	}
	for _, p := range pending {
		if p.ID != issue341ID(vault) {
			continue
		}
		for _, device := range p.OfferedBy {
			if device.DeviceID == r.peer.id {
				return true
			}
		}
	}
	return false
}

// pendingSnapshot is what is pending, without the times: folder, label and
// the devices that offer it, in a fixed order.
func issue341PendingSnapshot(t *testing.T) string {
	t.Helper()
	var pending []PendingFolderInfo
	if err := json.Unmarshal([]byte(GetPendingFoldersJSON()), &pending); err != nil {
		t.Fatalf("GetPendingFoldersJSON unmarshal: %v", err)
	}
	var rows []string
	for _, p := range pending {
		var devices []string
		for _, d := range p.OfferedBy {
			devices = append(devices, d.DeviceID)
		}
		slices.Sort(devices)
		rows = append(rows, fmt.Sprintf("%s %q by %s", p.ID, p.Label, strings.Join(devices, ",")))
	}
	slices.Sort(rows)
	return strings.Join(rows, "; ")
}

func (r *issue341Rig) configured(t *testing.T, vault string) bool {
	t.Helper()
	return configuredFolderIDs(t)[issue341ID(vault)]
}

// remoteState is the bridge's view of the peer for a vault it has
// configured: "valid" once the peer's cluster config on the current
// connection shares it, "notSharing" or "paused" when it says so.
func (r *issue341Rig) remoteState(vault string) string {
	mu.Lock()
	app := stApp
	mu.Unlock()
	if app == nil || app.Internals == nil {
		return ""
	}
	device, err := protocol.DeviceIDFromString(r.peer.id)
	if err != nil {
		return ""
	}
	comp, err := app.Internals.Completion(device, issue341ID(vault))
	if err != nil {
		return ""
	}
	return comp.RemoteState.String()
}

// waitClusterConfig waits for a cluster config from the peer after mark.
func (r *issue341Rig) waitClusterConfig(t *testing.T, mark int, what string) {
	t.Helper()
	waitFor(t, what, 30*time.Second, func() bool {
		return len(r.ev.about(mark, events.ClusterConfigReceived, "", r.peer.id)) > 0
	})
}

// pendingAfterAFreshConnection stops and starts the bridge's engine on the
// same home, waits until the peer's cluster config on the new connection has
// been handled, and returns what is pending then: what a fresh connection
// shows. kept is a vault the bridge has configured and the peer shares.
func (r *issue341Rig) pendingAfterAFreshConnection(t *testing.T, kept string) string {
	t.Helper()
	if errMsg := StopSyncthing(); errMsg != "" {
		t.Fatalf("StopSyncthing() = %q", errMsg)
	}
	if errMsg := StartSyncthing(r.configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() again = %q", errMsg)
	}
	r.ev = watchIssue341Events(t)
	waitFor(t, "the peer's cluster config on a new connection", 60*time.Second, func() bool {
		return r.remoteState(kept) == "valid"
	})
	return issue341PendingSnapshot(t)
}

// connStartedAt is when the peer's connection to the bridge was set up, as
// the peer reports it: a reconnect changes it.
func (r *issue341Rig) connStartedAt(t *testing.T) string {
	t.Helper()
	var out struct {
		Connections map[string]struct {
			Connected bool   `json:"connected"`
			StartedAt string `json:"startedAt"`
			Primary   *struct {
				StartedAt string `json:"startedAt"`
			} `json:"primary"`
		} `json:"connections"`
	}
	r.peer.mustDo(t, http.MethodGet, "/rest/system/connections", nil, &out)
	c := out.Connections[r.bridgeID]
	switch {
	case !c.Connected:
		return "not connected"
	case c.Primary != nil && c.Primary.StartedAt != "":
		return c.Primary.StartedAt
	default:
		return c.StartedAt
	}
}

func (r *issue341Rig) sameConnection(t *testing.T, since, when string) {
	t.Helper()
	if now := r.connStartedAt(t); now != since {
		t.Fatalf("%s: the peer's connection to the bridge changed (set up at %s, now %s)", when, since, now)
	}
	if !bridgeConnected(r.peer.id) {
		t.Fatalf("%s: the bridge is not connected to the peer", when)
	}
	if n := r.peer.connectionCount(t, r.bridgeID); n != 1 {
		t.Fatalf("%s: the peer holds %d connections to the bridge, want 1", when, n)
	}
}

// peerEventsSince returns the peer's events of the given types after since,
// from its REST event API (the same in 1.x and 2.x), and the last ID seen.
func (r *issue341Rig) peerEventsSince(t *testing.T, since int, types string) ([]issue341PeerEvent, int) {
	t.Helper()
	var out []issue341PeerEvent
	r.peer.mustDo(t, http.MethodGet, "/rest/events?since="+strconv.Itoa(since)+"&timeout=0&events="+url.QueryEscape(types), nil, &out)
	last := since
	for _, e := range out {
		if e.ID > last {
			last = e.ID
		}
	}
	return out, last
}

type issue341PeerEvent struct {
	ID   int            `json:"id"`
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

const issue341PeerEventTypes = "RemoteIndexUpdated,RemoteDownloadProgress,ItemStarted"

func (r *issue341Rig) waitBridgeHolds(t *testing.T, vault, dir, name, content string) {
	t.Helper()
	waitFor(t, name+" in the bridge's "+vault, 60*time.Second, func() bool {
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(got) != content {
			return false
		}
		var st FolderStatus
		if err := json.Unmarshal([]byte(GetFolderStatusJSON(issue341ID(vault))), &st); err != nil {
			return false
		}
		return st.State == "idle" && st.NeedFiles == 0
	})
}

// syncsBothWays writes a note on the peer and one on the bridge and waits for
// each to arrive on the other side.
func (r *issue341Rig) syncsBothWays(t *testing.T, vault, bridgeDir, tag string) {
	t.Helper()
	fromPeer, fromBridge := "from-peer-"+tag+".md", "from-bridge-"+tag+".md"
	content := "# " + tag + "\n"
	if err := os.WriteFile(filepath.Join(r.peerDir(vault), fromPeer), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	r.peer.mustDo(t, http.MethodPost, "/rest/db/scan?folder="+issue341ID(vault), nil, nil)
	r.waitBridgeHolds(t, vault, bridgeDir, fromPeer, content)
	if err := os.WriteFile(filepath.Join(bridgeDir, fromBridge), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if errMsg := RescanFolder(issue341ID(vault)); errMsg != "" {
		t.Fatalf("RescanFolder(%s) = %q", vault, errMsg)
	}
	waitFor(t, fromBridge+" in the peer's "+vault, 60*time.Second, func() bool {
		got, err := os.ReadFile(filepath.Join(r.peerDir(vault), fromBridge))
		return err == nil && string(got) == content
	})
}

// removeAndExpectTheOffer removes the vault and requires its share to be
// pending when RemoveFolder returns: the offer is written inside the commit
// the bridge waits for, so a later poll would only hide a stall.
func (r *issue341Rig) removeAndExpectTheOffer(t *testing.T, vault string) {
	t.Helper()
	start := time.Now()
	if errMsg := RemoveFolder(issue341ID(vault)); errMsg != "" {
		t.Fatalf("RemoveFolder(%s) = %q", vault, errMsg)
	}
	if !r.offered(vault) {
		t.Fatalf("the share of %s is not pending when its removal returned (after %s)", vault, time.Since(start).Round(time.Millisecond))
	}
	if r.configured(t, vault) {
		t.Fatalf("%s is configured again without an accept", vault)
	}
	t.Logf("%s offered again when its removal returned, after %s", vault, time.Since(start).Round(time.Millisecond))
}

// The reported case: a vault is removed while its peer stays connected and
// goes on sharing it. Its share is pending when the removal returns, as the
// peer's offer, and waits there. The connection is the same throughout: the
// peer's other vault syncs both ways after the removal, and the vault
// accepted again syncs on that connection too.
func TestIssue341_ARemovedVaultsShareIsOfferedAgain(t *testing.T) {
	if testing.Short() {
		t.Skip("real peer process; skipped in -short")
	}
	r := newIssue341Rig(t, "removed", "kept")
	connected := r.connStartedAt(t)

	r.removeAndExpectTheOffer(t, "removed")
	var pending []PendingFolderInfo
	if err := json.Unmarshal([]byte(GetPendingFoldersJSON()), &pending); err != nil {
		t.Fatal(err)
	}
	for _, p := range pending {
		if p.ID == issue341ID("removed") && p.Label != "removed" {
			t.Fatalf("the offer carries the label %q, want the peer's %q", p.Label, "removed")
		}
	}
	// What the vault held stays on disk as it was.
	if got, err := os.ReadFile(filepath.Join(r.bridgeDir("removed"), "note.md")); err != nil || string(got) != issue341Note {
		t.Fatalf("the removed vault's note: %q, %v", got, err)
	}
	r.syncsBothWays(t, "kept", r.bridgeDir("kept"), "after-the-removal")
	r.sameConnection(t, connected, "after the removal")

	again := filepath.Join(r.selected, "removed again")
	if errMsg := acceptAsDirectChild(issue341ID("removed"), "removed", again, false); errMsg != "" {
		t.Fatalf("accept the removed vault again: %q", errMsg)
	}
	r.waitBridgeHolds(t, "removed", again, "note.md", issue341Note)
	r.syncsBothWays(t, "removed", again, "after-the-accept")
	r.syncsBothWays(t, "kept", r.bridgeDir("kept"), "after-the-accept")
	r.sameConnection(t, connected, "after the accept")
}

// A removal is the person's word that this iPhone stops syncing the vault.
// Until they accept it again, nothing of it may cross to or from any device:
// no index either way, no file pulled. The offer that is back only waits.
//
// Every such exchange is seen where it lands: an index received is a
// RemoteIndexUpdated on the side that received it, a pull starts with an
// ItemStarted on the side that pulls, download progress is a
// RemoteDownloadProgress. The bridge's engine logs its own, the peer's are
// read from its event API; and an index for a folder the receiving side has
// no handler for would close the connection, which the peer's record of the
// connection shows. The peer changes the vault in the meantime, so it has
// something to send. After the tap, the same events show the exchange, so
// their absence before it is not an accident of where the test looked.
func TestIssue341_NothingOfTheVaultIsExchangedUntilTheTap(t *testing.T) {
	if testing.Short() {
		t.Skip("real peer process; skipped in -short")
	}
	r := newIssue341Rig(t, "removed", "kept")
	removedID := issue341ID("removed")
	connected := r.connStartedAt(t)
	// The peer's event API keeps a subscription per set of types from its
	// first request on: open it before the window.
	_, peerMark := r.peerEventsSince(t, 0, issue341PeerEventTypes)
	bridgeMark := r.ev.count()

	r.removeAndExpectTheOffer(t, "removed")

	// The peer changes the removed vault, and the kept one syncs both ways
	// over the same connection.
	changed := "changed-on-the-peer-after-the-removal.md"
	if err := os.WriteFile(filepath.Join(r.peerDir("removed"), changed), []byte("# changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.peer.scanAndSettle(t, removedID)
	r.syncsBothWays(t, "kept", r.bridgeDir("kept"), "in-the-window")
	time.Sleep(2 * time.Second)

	for _, typ := range []events.EventType{events.RemoteIndexUpdated, events.RemoteDownloadProgress, events.ItemStarted} {
		if got := r.ev.about(bridgeMark, typ, removedID, ""); len(got) > 0 {
			t.Errorf("the bridge's engine logged %s for the removed vault before the tap: %+v", typ, got)
		}
	}
	peerEvents, _ := r.peerEventsSince(t, peerMark, issue341PeerEventTypes)
	for _, e := range peerEvents {
		if folder, _ := e.Data["folder"].(string); folder == removedID {
			t.Errorf("the peer logged %s for the removed vault before the tap: %+v", e.Type, e.Data)
		}
	}
	var completion struct {
		RemoteState string `json:"remoteState"`
	}
	r.peer.mustDo(t, http.MethodGet, "/rest/db/completion?folder="+removedID+"&device="+r.bridgeID, nil, &completion)
	if completion.RemoteState != "" && completion.RemoteState != "notSharing" {
		t.Errorf("the peer sees the bridge's state of the removed vault as %q before the tap, want notSharing", completion.RemoteState)
	}
	if !r.offered("removed") || r.configured(t, "removed") {
		t.Fatal("the offer did not wait for the tap")
	}
	r.sameConnection(t, connected, "before the tap")
	if t.Failed() {
		return
	}

	// The tap. The same events now show the exchange.
	bridgeMark = r.ev.count()
	again := filepath.Join(r.selected, "removed again")
	if errMsg := acceptAsDirectChild(removedID, "removed", again, false); errMsg != "" {
		t.Fatalf("accept the removed vault again: %q", errMsg)
	}
	r.waitBridgeHolds(t, "removed", again, changed, "# changed\n")
	r.syncsBothWays(t, "removed", again, "after-the-tap")
	if len(r.ev.about(bridgeMark, events.RemoteIndexUpdated, removedID, r.peer.id)) == 0 {
		t.Error("no index from the peer for the vault after the tap: the events this test reads do not show the exchange")
	}
	if len(r.ev.about(bridgeMark, events.ItemStarted, removedID, "")) == 0 {
		t.Error("no pull of the vault after the tap: the events this test reads do not show the exchange")
	}
	sawPeerIndex := false
	peerEvents, _ = r.peerEventsSince(t, peerMark, issue341PeerEventTypes)
	for _, e := range peerEvents {
		folder, _ := e.Data["folder"].(string)
		device, _ := e.Data["device"].(string)
		if e.Type == "RemoteIndexUpdated" && folder == removedID && device == r.bridgeID {
			sawPeerIndex = true
		}
	}
	if !sawPeerIndex {
		t.Error("the peer logged no index from the bridge for the vault after the tap: the events this test reads do not show the exchange")
	}
	r.sameConnection(t, connected, "after the tap")
}

// The offer that comes back is the device's own current one: a device that
// stopped sharing the vault with us, or paused it, sends a cluster config
// that says so, and what is pending afterwards is what a fresh connection
// to it shows — before the removal and after the offer is back alike.
func TestIssue341_WhatIsPendingIsWhatAFreshConnectionShows(t *testing.T) {
	if testing.Short() {
		t.Skip("real peer process; skipped in -short")
	}
	cases := []struct {
		name string
		// change is what the peer does; before tells whether it does it
		// before the removal or once the offer is back.
		change  func(t *testing.T, r *issue341Rig)
		before  bool
		offered bool
	}{
		{"the peer stops sharing before the removal", func(t *testing.T, r *issue341Rig) { r.setPeerSharing(t, "removed", false) }, true, false},
		{"the peer stops sharing after the offer is back", func(t *testing.T, r *issue341Rig) { r.setPeerSharing(t, "removed", false) }, false, false},
		{"the peer pauses before the removal", func(t *testing.T, r *issue341Rig) { r.setPeerPaused(t, "removed", true) }, true, true},
		{"the peer pauses after the offer is back", func(t *testing.T, r *issue341Rig) { r.setPeerPaused(t, "removed", true) }, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newIssue341Rig(t, "removed", "kept")
			if c.before {
				mark := r.ev.count()
				c.change(t, r)
				r.waitClusterConfig(t, mark, "the peer's cluster config after its change")
				if errMsg := RemoveFolder(issue341ID("removed")); errMsg != "" {
					t.Fatalf("RemoveFolder() = %q", errMsg)
				}
			} else {
				r.removeAndExpectTheOffer(t, "removed")
				mark := r.ev.count()
				c.change(t, r)
				r.waitClusterConfig(t, mark, "the peer's cluster config after its change")
			}
			if got := r.offered("removed"); got != c.offered {
				t.Fatalf("offered = %v after the peer's change, want %v", got, c.offered)
			}
			now := issue341PendingSnapshot(t)
			fresh := r.pendingAfterAFreshConnection(t, "kept")
			if now != fresh {
				t.Fatalf("pending differs from what a fresh connection shows:\n  now:   %s\n  fresh: %s", now, fresh)
			}
			if !c.offered {
				return
			}
			// Resumed, the vault accepted again syncs on the connection that
			// is up.
			connected := r.connStartedAt(t)
			mark := r.ev.count()
			r.setPeerPaused(t, "removed", false)
			r.waitClusterConfig(t, mark, "the peer's cluster config after it resumed")
			again := filepath.Join(r.selected, "removed again")
			if errMsg := acceptAsDirectChild(issue341ID("removed"), "removed", again, false); errMsg != "" {
				t.Fatalf("accept the removed vault again: %q", errMsg)
			}
			r.waitBridgeHolds(t, "removed", again, "note.md", issue341Note)
			r.syncsBothWays(t, "removed", again, "after-the-resume")
			r.sameConnection(t, connected, "after the accept")
		})
	}
}

// Vaults removed one after another — what the notices for vaults Obsidian
// cannot show ask for (#270) — are each offered again when their removal
// returns: nothing is redialed, so no limit on redials is met.
func TestIssue341_VaultsRemovedOneAfterAnotherAreEachOfferedAgain(t *testing.T) {
	if testing.Short() {
		t.Skip("real peer process; skipped in -short")
	}
	removed := []string{"first", "second", "third", "fourth", "fifth"}
	r := newIssue341Rig(t, append([]string{"kept"}, removed...)...)
	connected := r.connStartedAt(t)
	for _, vault := range removed {
		r.removeAndExpectTheOffer(t, vault)
	}
	for _, vault := range removed {
		if !r.offered(vault) {
			t.Fatalf("the share of %s is no longer pending after the later removals", vault)
		}
	}
	r.syncsBothWays(t, "kept", r.bridgeDir("kept"), "after-the-removals")
	r.sameConnection(t, connected, "after the removals")
}

// The offer is written inside the commit that removes the folder, however
// late that commit is (#343). A removal applied before the bridge's wait
// ends is offered again even while the rest of its commit is held; a
// removal still queued when the wait ends is not offered while the folder
// is configured, and is offered once it lands.
func TestIssue341_ARemovalThatOutlastsTheWaitIsOfferedOnceItLands(t *testing.T) {
	if testing.Short() {
		t.Skip("real peer process; skipped in -short")
	}
	t.Run("applied, its commit held", func(t *testing.T) {
		r := newIssue341Rig(t, "removed", "kept")
		held := newHeldCommitter(removesFolder(issue341ID("removed")))
		subscribeHeld(t, held)
		defer overrideLifecycleTimeouts(t)()
		lifecycleTimeouts.configCommit = 300 * time.Millisecond

		if answer := RemoveFolder(issue341ID("removed")); answer != "" {
			t.Fatalf("RemoveFolder() = %q, want success for a removal already applied", answer)
		}
		waitFor(t, "the offer while the removal's commit is held", 10*time.Second, func() bool {
			return r.offered("removed")
		})
		held.let()
	})
	t.Run("queued behind another change", func(t *testing.T) {
		r := newIssue341Rig(t, "removed", "kept")
		const marker = 34134
		held := newHeldCommitter(func(from, to config.Configuration) bool {
			return to.Options.MaxRecvKbps == marker && from.Options.MaxRecvKbps != marker
		})
		subscribeHeld(t, held)
		mu.Lock()
		cfg := stCfg
		mu.Unlock()
		blocker := make(chan error, 1)
		go func() {
			waiter, err := cfg.Modify(func(c *config.Configuration) { c.Options.MaxRecvKbps = marker })
			if err == nil {
				waiter.Wait()
			}
			blocker <- err
		}()
		select {
		case <-held.entered:
		case <-time.After(10 * time.Second):
			t.Fatal("the unrelated change never reached its commit")
		}
		defer overrideLifecycleTimeouts(t)()
		lifecycleTimeouts.configCommit = 300 * time.Millisecond

		if answer := RemoveFolder(issue341ID("removed")); answer != FolderRemovalNotConfirmedError() {
			t.Fatalf("RemoveFolder() = %q, want %q", answer, FolderRemovalNotConfirmedError())
		}
		if r.offered("removed") {
			t.Fatal("offered while the folder is still configured")
		}
		held.let()
		if err := <-blocker; err != nil {
			t.Fatalf("the unrelated change failed: %v", err)
		}
		waitFor(t, "the offer once the queued removal landed", 10*time.Second, func() bool {
			return r.offered("removed")
		})
		if r.configured(t, "removed") {
			t.Fatal("the removal did not land")
		}
	})
}
