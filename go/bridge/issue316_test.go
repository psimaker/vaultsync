package bridge

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/protocol"
)

// A vault removed here while its peer stays connected and goes on sharing it
// was not offered again until the app restarted (#316, seen on a device). The
// engine notes an offer when a peer's cluster configuration arrives, and a
// peer sends one when a connection is set up or when its own configuration
// changes. A removal here changes nothing on the peer, so nothing arrived.
//
// The tests run the bridge against a real loopback peer: whether the peer
// announces the share again, and what that does to the vaults that stay, only
// shows there.

const issue316Note = "# Note\n\nwritten on the peer before it shared the vault\n"

func issue316FolderID(vault string) string { return "issue316-" + vault }

// issue316Rig is a real peer that shares some vaults and the bridge that has
// accepted each of them under one selected folder, settled.
type issue316Rig struct {
	peer      *peerInstance
	bridgeID  string
	configDir string
	// selected is the selected folder: every vault syncs in a direct
	// subfolder of it.
	selected string
}

// newIssue316Rig starts the peer and the bridge. reconnectIntervalS is how
// long the bridge waits before it dials an address again by itself; 0 keeps
// the engine's default of a minute.
func newIssue316Rig(t *testing.T, reconnectIntervalS int, vaults ...string) *issue316Rig {
	t.Helper()
	peer := startPeer(t, peerBinary(t), "issue316-peer")
	rig := &issue316Rig{peer: peer}
	for _, vault := range vaults {
		dir := rig.peerDir(vault)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte(issue316Note), 0o600); err != nil {
			t.Fatal(err)
		}
		peer.mustDo(t, http.MethodPost, "/rest/config/folders", map[string]any{
			"id":               issue316FolderID(vault),
			"label":            vault,
			"path":             dir,
			"type":             "sendreceive",
			"rescanIntervalS":  3600,
			"fsWatcherEnabled": false,
			"devices":          []map[string]any{},
		}, nil)
		peer.scanAndSettle(t, issue316FolderID(vault))
	}

	rig.configDir = testConfigDir(t)
	rig.selected = filepath.Join(rig.configDir, "selected")
	if errMsg := StartSyncthing(rig.configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	captureBridgeLog(t, filepath.Join(rig.configDir, "bridge-engine.log"))
	bridgeListen := "tcp://127.0.0.1:" + strconv.Itoa(freePort(t))
	mu.Lock()
	err := commitConfigLocked(func(cfg *config.Configuration) {
		cfg.Options.RawListenAddresses = []string{bridgeListen}
		cfg.Options.GlobalAnnEnabled = false
		cfg.Options.LocalAnnEnabled = false
		cfg.Options.RelaysEnabled = false
		cfg.Options.NATEnabled = false
		if reconnectIntervalS > 0 {
			cfg.Options.ReconnectIntervalS = reconnectIntervalS
		}
	})
	mu.Unlock()
	if err != nil {
		t.Fatalf("pin bridge to loopback: %v", err)
	}
	issue316WaitForTheFirstListenerToClose()
	rig.bridgeID = DeviceID()
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
		"deviceID":  rig.bridgeID,
		"name":      "Bridge",
		"addresses": []string{bridgeListen},
	}, nil)
	for _, vault := range vaults {
		var folderCfg map[string]any
		peer.mustDo(t, http.MethodGet, "/rest/config/folders/"+issue316FolderID(vault), nil, &folderCfg)
		folderCfg["devices"] = []map[string]any{{"deviceID": peer.id}, {"deviceID": rig.bridgeID}}
		peer.mustDo(t, http.MethodPut, "/rest/config/folders/"+issue316FolderID(vault), folderCfg, nil)
	}

	waitFor(t, "bridge connected to peer", 60*time.Second, func() bool {
		return bridgeConnected(peer.id)
	})
	for _, vault := range vaults {
		waitFor(t, "the first offer of "+vault, 60*time.Second, func() bool {
			return rig.offered(vault)
		})
		if errMsg := acceptAsDirectChild(issue316FolderID(vault), vault, rig.bridgeDir(vault), false); errMsg != "" {
			t.Fatalf("accept %s: %q", vault, errMsg)
		}
	}
	for _, vault := range vaults {
		rig.waitBridgeHolds(t, vault, rig.bridgeDir(vault), "note.md", issue316Note)
	}
	return rig
}

// issue316WaitForTheFirstListenerToClose waits until nothing answers on the
// engine's default port any more. The engine starts listening there and moves
// to the loopback port of the rig, and the old listener takes up to a second
// to close; a dial made in that second goes out from the old listener's port.
//
// The engine closes a connection by aborting it. When the bridge closed a
// connection it had dialed from that port, the peer was seen not to learn of
// it on a Mac whose firewall runs in stealth mode: with no listener left on
// the port the kernel answers nothing that arrives for it, the peer's end
// stayed established, and the peer refused every new connection as a second
// one (lsof, and sysctl net.inet.tcp.blackhole = 2, on 2026-11-14). An
// install keeps the listener it started with, so the tests do not dial in
// that second. The wait is bounded: another program may listen on the port.
func issue316WaitForTheFirstListenerToClose() {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(config.DefaultTCPPort), 200*time.Millisecond)
		if err != nil {
			return
		}
		conn.Close()
		time.Sleep(100 * time.Millisecond)
	}
}

func (r *issue316Rig) peerDir(vault string) string   { return filepath.Join(r.peer.home, vault) }
func (r *issue316Rig) bridgeDir(vault string) string { return filepath.Join(r.selected, vault) }

// offered reports whether the vault's share is among the pending offers, as
// one the peer made.
func (r *issue316Rig) offered(vault string) bool {
	var pending []PendingFolderInfo
	if err := json.Unmarshal([]byte(GetPendingFoldersJSON()), &pending); err != nil {
		return false
	}
	for _, p := range pending {
		if p.ID != issue316FolderID(vault) {
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

// folder returns the vault's folder as the bridge has it configured.
func (r *issue316Rig) folder(t *testing.T, vault string) (FolderInfo, bool) {
	t.Helper()
	var folders []FolderInfo
	if err := json.Unmarshal([]byte(GetFoldersJSON()), &folders); err != nil {
		t.Fatalf("GetFoldersJSON unmarshal: %v", err)
	}
	for _, folder := range folders {
		if folder.ID == issue316FolderID(vault) {
			return folder, true
		}
	}
	return FolderInfo{}, false
}

// peerPaused reports whether the bridge's configuration has the peer paused.
func (r *issue316Rig) peerPaused(t *testing.T) bool {
	t.Helper()
	var devices []DeviceInfo
	if err := json.Unmarshal([]byte(GetConnectionsJSON()), &devices); err != nil {
		t.Fatalf("GetConnectionsJSON unmarshal: %v", err)
	}
	for _, device := range devices {
		if device.DeviceID == r.peer.id {
			return device.Paused
		}
	}
	t.Fatal("the peer is no longer a device of the bridge")
	return false
}

// waitBridgeHolds waits until dir holds the file with its content and the
// vault's folder is idle with nothing left to fetch.
func (r *issue316Rig) waitBridgeHolds(t *testing.T, vault, dir, name, content string) {
	t.Helper()
	waitFor(t, name+" in the bridge's "+vault, 120*time.Second, func() bool {
		if got, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(got) != content {
			return false
		}
		var st FolderStatus
		if err := json.Unmarshal([]byte(GetFolderStatusJSON(issue316FolderID(vault))), &st); err != nil {
			return false
		}
		return st.State == "idle" && st.NeedFiles == 0
	})
}

// syncsBothWays writes a note on the peer and one on the bridge and waits for
// each to arrive on the other side.
func (r *issue316Rig) syncsBothWays(t *testing.T, vault, tag string) {
	t.Helper()
	fromPeer, fromBridge := "from-peer-"+tag+".md", "from-bridge-"+tag+".md"
	content := "# " + tag + "\n"
	if err := os.WriteFile(filepath.Join(r.peerDir(vault), fromPeer), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	r.peer.mustDo(t, http.MethodPost, "/rest/db/scan?folder="+issue316FolderID(vault), nil, nil)
	r.waitBridgeHolds(t, vault, r.bridgeDir(vault), fromPeer, content)

	if err := os.WriteFile(filepath.Join(r.bridgeDir(vault), fromBridge), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if errMsg := RescanFolder(issue316FolderID(vault)); errMsg != "" {
		t.Fatalf("RescanFolder(%s) = %q", vault, errMsg)
	}
	waitFor(t, fromBridge+" in the peer's "+vault, 120*time.Second, func() bool {
		got, err := os.ReadFile(filepath.Join(r.peerDir(vault), fromBridge))
		return err == nil && string(got) == content
	})
}

// holdsOneConnection checks for a few seconds that the bridge and the peer
// stay connected over exactly one connection: a reconnect that kept repeating
// would show here.
func (r *issue316Rig) holdsOneConnection(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if !bridgeConnected(r.peer.id) {
			t.Fatal("the connection to the peer dropped again")
		}
		if n := r.peer.connectionCount(t, r.bridgeID); n != 1 {
			t.Fatalf("the peer holds %d connections to the bridge, want exactly 1", n)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// The reported scenario: a vault is removed while its peer stays connected and
// goes on sharing it. The share has to be back among the pending offers
// without a restart, as an offer the peer made, waiting to be accepted — and
// the peer's other vault has to go on syncing.
func TestIssue316_ARemovedVaultsShareIsOfferedAgain(t *testing.T) {
	if testing.Short() {
		t.Skip("real peer process; skipped in -short")
	}
	rig := newIssue316Rig(t, 0, "removed", "kept")
	removedID := issue316FolderID("removed")

	mu.Lock()
	commitsBefore := stConfigCommits
	mu.Unlock()
	if errMsg := RemoveFolder(removedID); errMsg != "" {
		t.Fatalf("RemoveFolder() = %q", errMsg)
	}
	waitFor(t, "the removed vault's share among the pending offers, without a restart", 30*time.Second, func() bool {
		return rig.offered("removed")
	})

	// It waits there: nothing has accepted it again.
	if _, configured := rig.folder(t, "removed"); configured {
		t.Fatal("the removed vault is configured again without an accept")
	}
	// What the vault held stays on disk as it was.
	if got, err := os.ReadFile(filepath.Join(rig.bridgeDir("removed"), "note.md")); err != nil || string(got) != issue316Note {
		t.Fatalf("the removed vault's note: %q, %v", got, err)
	}
	// The removal is the only thing the bridge wrote to the configuration: no
	// pause and resume of the peer, which a kill in between would leave
	// paused.
	mu.Lock()
	commits := stConfigCommits - commitsBefore
	mu.Unlock()
	if commits != 1 {
		t.Fatalf("the removal made %d configuration changes, want 1", commits)
	}
	if rig.peerPaused(t) {
		t.Fatal("the peer is paused after the removal")
	}

	// The peer's other vault goes on syncing, in both directions, over one
	// connection that holds.
	rig.syncsBothWays(t, "kept", "after-the-removal")
	rig.holdsOneConnection(t)

	// The offer is the peer's own: accepted, the vault is shared with the
	// peer again and its notes arrive.
	again := filepath.Join(rig.selected, "removed again")
	if errMsg := acceptAsDirectChild(removedID, "removed", again, false); errMsg != "" {
		t.Fatalf("accept the removed vault again: %q", errMsg)
	}
	folder, configured := rig.folder(t, "removed")
	if !configured || len(folder.DeviceIDs) != 1 || folder.DeviceIDs[0] != rig.peer.id {
		t.Fatalf("the vault accepted again is shared with %v, want the peer", folder.DeviceIDs)
	}
	rig.waitBridgeHolds(t, "removed", again, "note.md", issue316Note)
}

// Vaults removed one after another, each once the one before is offered
// again: the notices for vaults Obsidian cannot show ask for exactly that
// (#270). The first one is paused when it is removed, as the path collision
// shield leaves a vault before it names removal as the way out (#45).
//
// The engine dials a device at once after a connection closed, but only a few
// times in a row; after that the next dial is the regular one, a minute after
// the last. The bridge dials every five seconds here instead, so the test
// covers that case without waiting a minute for each vault.
// VAULTSYNC_BRIDGE_ISSUE316_RECONNECT_S=60 measures it as an install has it.
func TestIssue316_VaultsRemovedOneAfterAnotherAreEachOfferedAgain(t *testing.T) {
	if testing.Short() {
		t.Skip("real peer process; skipped in -short")
	}
	reconnectS := envInt("VAULTSYNC_BRIDGE_ISSUE316_RECONNECT_S", 5)
	removed := []string{"first", "second", "third", "fourth", "fifth"}
	rig := newIssue316Rig(t, reconnectS, append([]string{"kept"}, removed...)...)

	if errMsg := SetFolderPaused(issue316FolderID("first"), true); errMsg != "" {
		t.Fatalf("SetFolderPaused() = %q", errMsg)
	}
	for _, vault := range removed {
		start := time.Now()
		if errMsg := RemoveFolder(issue316FolderID(vault)); errMsg != "" {
			t.Fatalf("RemoveFolder(%s) = %q", vault, errMsg)
		}
		waitFor(t, "the share of "+vault+" among the pending offers", time.Duration(2*reconnectS+30)*time.Second, func() bool {
			return rig.offered(vault)
		})
		t.Logf("%s is offered again after %s", vault, time.Since(start).Round(100*time.Millisecond))
	}

	// A later removal has not taken an earlier offer away again.
	for _, vault := range removed {
		if !rig.offered(vault) {
			t.Fatalf("the share of %s is no longer among the pending offers", vault)
		}
		if _, configured := rig.folder(t, vault); configured {
			t.Fatalf("%s is configured again without an accept", vault)
		}
	}
	rig.syncsBothWays(t, "kept", "after-the-removals")
	rig.holdsOneConnection(t)
}

// After the bridge has closed a connection, both sides dial at once, each
// from its listening port to the other's. TCP can join two such attempts
// into one connection on which both ends are TLS clients: the handshake
// fails on both, both try again after the same interval, and meet again the
// same way (seen against the real peer: three failed handshakes in a row at
// the same moments, and no connection for the rest of the test).
//
// The dial is therefore tried once more from a port of its own when the
// handshake fails. Here a listener stands in for the other side. It closes
// the first connection as soon as it is accepted, which fails the handshake:
// the second connection has to follow at once and from another port. That
// one it holds open without a word: the bridge has to give it up when the
// dial's deadline is over, so that a dial takes no longer than it could
// without the second try.
func TestIssue316_AFailedHandshakeIsTriedAgainFromAnotherPort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	type arrival struct {
		port int
		at   time.Time
		// over receives the moment the bridge closed a connection the
		// listener held open.
		over chan time.Time
	}
	arrivals := make(chan arrival, 64)
	go func() {
		for first := true; ; first = false {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			a := arrival{conn.RemoteAddr().(*net.TCPAddr).Port, time.Now(), make(chan time.Time, 1)}
			select {
			case arrivals <- a:
			default:
			}
			if first {
				conn.Close()
				continue
			}
			go func() {
				defer conn.Close()
				// The client's first handshake message, and then nothing
				// until the bridge closes the connection.
				buf := make([]byte, 4096)
				for {
					if _, err := conn.Read(buf); err != nil {
						a.over <- time.Now()
						return
					}
				}
			}()
		}
	}()

	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	bridgePort := freePort(t)
	mu.Lock()
	err = commitConfigLocked(func(cfg *config.Configuration) {
		cfg.Options.RawListenAddresses = []string{"tcp://127.0.0.1:" + strconv.Itoa(bridgePort)}
		cfg.Options.GlobalAnnEnabled = false
		cfg.Options.LocalAnnEnabled = false
		cfg.Options.RelaysEnabled = false
		cfg.Options.NATEnabled = false
	})
	mu.Unlock()
	if err != nil {
		t.Fatalf("pin bridge to loopback: %v", err)
	}
	issue316WaitForTheFirstListenerToClose()

	other := protocol.NewDeviceID([]byte("issue316: a device that never completes a handshake")).String()
	if errMsg := AddDevice(other, "Other"); errMsg != "" {
		t.Fatalf("AddDevice() = %q", errMsg)
	}
	mu.Lock()
	err = commitConfigLocked(func(cfg *config.Configuration) {
		for i := range cfg.Devices {
			if cfg.Devices[i].DeviceID.String() == other {
				cfg.Devices[i].Addresses = []string{"tcp://" + listener.Addr().String()}
			}
		}
	})
	mu.Unlock()
	if err != nil {
		t.Fatalf("set the device's address: %v", err)
	}

	// The first attempt comes from the bridge's listening port. The engine
	// dials a device again after a second at the earliest, so a connection
	// from another port within half a second is the second try of the same
	// dial and not the next one.
	var first, second arrival
	select {
	case first = <-arrivals:
	case <-time.After(30 * time.Second):
		t.Fatal("the bridge never dialed")
	}
	if first.port != bridgePort {
		t.Fatalf("the first dial came from port %d, want the bridge's listening port %d", first.port, bridgePort)
	}
	select {
	case second = <-arrivals:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("no second try from another port after the failed handshake")
	}
	if second.port == bridgePort {
		t.Fatalf("the next connection came from the listening port again, %s later", second.at.Sub(first.at))
	}

	// The dial's deadline is five seconds (fork patch 003), a handshake's
	// own limit is ten: the second try has to end with the first of them.
	select {
	case over := <-second.over:
		if took := over.Sub(first.at); took > 7*time.Second {
			t.Fatalf("the bridge held the second try for %s after the dial began", took)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the bridge never gave the second try up")
	}
}
