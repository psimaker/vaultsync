package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/syncthing/syncthing/lib/config"
)

// A server folder whose index carries a heavy deletion/rename history was
// reported to crash-loop the app about one second after connect — right at
// index exchange — while a fresh folder from the same server synced fine, and
// a server-side index rebuild made the crash go away (#135, carried as #185).
// This harness builds such an index on a real loopback peer (rounds of
// create → scan → delete → scan), then lets the bridge accept the share and
// exchange indexes. The engine must survive any index a peer sends: a panic
// aborts this test binary, a dropped connection or a folder that never
// settles fails the assertions.
//
// The peer is a syncthing binary built from the pinned, patched tree
// (VAULTSYNC_BRIDGE_PEER_BIN, or built on demand into the temp dir).
func TestIssue185_HeavyTombstoneIndexExchangeSurvives(t *testing.T) {
	if testing.Short() {
		t.Skip("real peer process; skipped in -short")
	}
	rounds := envInt("VAULTSYNC_BRIDGE_TOMBSTONE_ROUNDS", 4)
	filesPerRound := envInt("VAULTSYNC_BRIDGE_TOMBSTONE_FILES", 2500)

	bin := peerBinary(t)
	peer := startPeer(t, bin, "tombstone-peer")

	// --- Build the tombstone-heavy index on the peer, before the bridge
	// ever sees the folder: every round leaves filesPerRound deletion
	// tombstones behind, the last round's files stay live.
	const folderID = "tombstone-vault"
	folderPath := filepath.Join(peer.home, "vault")
	if err := os.MkdirAll(folderPath, 0o700); err != nil {
		t.Fatal(err)
	}
	peer.mustDo(t, http.MethodPost, "/rest/config/folders", map[string]any{
		"id":               folderID,
		"label":            "Tombstone Vault",
		"path":             folderPath,
		"type":             "sendreceive",
		"rescanIntervalS":  3600,
		"fsWatcherEnabled": false,
		"devices":          []map[string]any{},
	}, nil)

	for round := 0; round < rounds; round++ {
		if round > 0 {
			// Delete the previous round's files: each becomes a tombstone
			// with a bumped version on the next scan.
			removeRound(t, folderPath, round-1, filesPerRound)
			peer.scanAndSettle(t, folderID)
		}
		writeRound(t, folderPath, round, filesPerRound)
		peer.scanAndSettle(t, folderID)
	}
	status := peer.dbStatus(t, folderID)
	wantTombstones := (rounds - 1) * filesPerRound
	if status.LocalDeleted < wantTombstones {
		t.Fatalf("peer index holds %d tombstones, want at least %d", status.LocalDeleted, wantTombstones)
	}
	if status.LocalFiles != filesPerRound {
		t.Fatalf("peer index holds %d live files, want %d", status.LocalFiles, filesPerRound)
	}
	t.Logf("peer index ready: %d live files, %d tombstones", status.LocalFiles, status.LocalDeleted)

	// --- Bridge: start, pin to loopback, pair with the peer.
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	// The bridge discards engine logs by design (privacy); this harness
	// keeps them in a file and prints the tail on failure — the engine's own
	// view of the exchange is the evidence this issue lacked (#135).
	bridgeLog := captureBridgeLog(t, filepath.Join(configDir, "bridge-engine.log"))
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
	bridgeID := DeviceID()
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

	// The peer learns the bridge and shares the tombstone-heavy folder.
	peer.mustDo(t, http.MethodPost, "/rest/config/devices", map[string]any{
		"deviceID":  bridgeID,
		"name":      "Bridge",
		"addresses": []string{bridgeListen},
	}, nil)
	var folderCfg map[string]any
	peer.mustDo(t, http.MethodGet, "/rest/config/folders/"+folderID, nil, &folderCfg)
	folderCfg["devices"] = []map[string]any{{"deviceID": peer.id}, {"deviceID": bridgeID}}
	peer.mustDo(t, http.MethodPut, "/rest/config/folders/"+folderID, folderCfg, nil)

	// --- Connect and accept the share into an empty directory.
	waitFor(t, "bridge connected to peer", 60*time.Second, func() bool {
		return bridgeConnected(peer.id)
	})
	waitFor(t, "pending folder offer", 60*time.Second, func() bool {
		var pending []PendingFolderInfo
		json.Unmarshal([]byte(GetPendingFoldersJSON()), &pending)
		for _, p := range pending {
			if p.ID == folderID {
				return true
			}
		}
		return false
	})
	acceptPath := filepath.Join(configDir, "accepted-vault")
	if errMsg := AcceptPendingFolder(folderID, "Tombstone Vault", acceptPath, false); errMsg != "" {
		t.Fatalf("AcceptPendingFolder() = %q", errMsg)
	}

	// --- Index exchange: the connection must hold and the folder must
	// settle with exactly the live files, tombstones applied, nothing
	// needed. A panic in the engine aborts the test binary before this.
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)
	var last FolderStatus
	lastTrace := time.Now()
	waitFor(t, "accepted folder settled after index exchange", 120*time.Second, func() bool {
		if !bridgeConnected(peer.id) {
			// A drop right at index exchange is the reported symptom.
			t.Logf("connection to peer dropped during index exchange")
		}
		if err := json.Unmarshal([]byte(GetFolderStatusJSON(folderID)), &last); err != nil {
			return false
		}
		if time.Since(lastTrace) > 5*time.Second {
			lastTrace = time.Now()
			t.Logf("bridge folder: state=%s global=%d local=%d need=%d connected=%v",
				last.State, last.GlobalFiles, last.LocalFiles, last.NeedFiles, bridgeConnected(peer.id))
		}
		// The global count includes the surviving round directory.
		return last.State == "idle" && last.GlobalFiles >= filesPerRound && last.NeedFiles == 0 && last.LocalFiles == filesPerRound
	})
	_ = bridgeLog
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)
	t.Logf("bridge settled: state=%s global=%d local=%d need=%d; heap %d→%d MiB (sys %d MiB)",
		last.State, last.GlobalFiles, last.LocalFiles, last.NeedFiles,
		memBefore.HeapAlloc>>20, memAfter.HeapAlloc>>20, memAfter.Sys>>20)

	// The connection has to stay up past the ~1 s mark the report names —
	// hold it for a while and check it never dropped.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if !bridgeConnected(peer.id) {
			t.Fatal("connection to the peer dropped after index exchange")
		}
		time.Sleep(500 * time.Millisecond)
	}
	// One connection per peer (#185): the promotion race needs a second
	// connection to land on, and the bridge asks for exactly one — which
	// the peer honours through the hello negotiation.
	if n := peer.connectionCount(t, bridgeID); n != 1 {
		t.Fatalf("peer holds %d connections to the bridge, want exactly 1", n)
	}
	if errMsg := StopSyncthing(); errMsg != "" {
		t.Fatalf("StopSyncthing() = %q", errMsg)
	}
}

// captureBridgeLog routes the engine's slog output to a file for the rest of
// the test and prints its tail when the test fails.
func captureBridgeLog(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		configurePrivacySafeLogging()
		f.Close()
		if t.Failed() {
			data, _ := os.ReadFile(path)
			if len(data) > 12000 {
				data = data[len(data)-12000:]
			}
			t.Logf("--- bridge engine log (tail) ---\n%s", data)
		}
	})
	return f
}

// --- peer harness -----------------------------------------------------------

type peerInstance struct {
	home   string
	gui    string
	listen string
	apiKey string
	id     string
	client *http.Client
}

type peerDBStatus struct {
	State        string `json:"state"`
	LocalFiles   int    `json:"localFiles"`
	LocalDeleted int    `json:"localDeleted"`
	GlobalFiles  int    `json:"globalFiles"`
	NeedFiles    int    `json:"needFiles"`
}

// peerBinary returns a stock-like syncthing for the loopback peer:
// VAULTSYNC_BRIDGE_PEER_BIN when set (CI builds it once), otherwise a build
// through scripts/build-peer-syncthing.sh into the temp dir that later runs
// on this machine reuse. The peer carries only the build-enabling patches,
// never the behavioural ones — it has to behave like a real server.
func peerBinary(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("VAULTSYNC_BRIDGE_PEER_BIN"); bin != "" {
		return bin
	}
	dir := filepath.Join(os.TempDir(), "vaultsync-bridge-peer")
	bin := filepath.Join(dir, "syncthing")
	if info, err := os.Stat(bin); err == nil && info.Size() > 0 {
		return bin
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Logf("building peer syncthing into %s (first run on this machine)", bin)
	cmd := exec.Command(filepath.Join("..", "scripts", "build-peer-syncthing.sh"), bin)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build peer syncthing: %v\n%s", err, out)
	}
	return bin
}

func startPeer(t *testing.T, bin, name string) *peerInstance {
	t.Helper()
	home := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	p := &peerInstance{
		home:   home,
		gui:    "127.0.0.1:" + strconv.Itoa(freePort(t)),
		listen: "tcp://127.0.0.1:" + strconv.Itoa(freePort(t)),
		apiKey: "bridge-test-" + name,
		client: &http.Client{Timeout: 30 * time.Second},
	}
	gen := exec.Command(bin, "generate", "--home="+home, "--no-port-probing")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("syncthing generate: %v\n%s", err, out)
	}
	// Pin the peer to loopback before it ever starts: no default listener,
	// no discovery, no relay, no NAT — the test must not touch a network.
	cfgPath := filepath.Join(home, "config.xml")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(raw)
	for old, repl := range map[string]string{
		"<listenAddress>default</listenAddress>":              "<listenAddress>" + p.listen + "</listenAddress>",
		"<globalAnnounceEnabled>true</globalAnnounceEnabled>": "<globalAnnounceEnabled>false</globalAnnounceEnabled>",
		"<localAnnounceEnabled>true</localAnnounceEnabled>":   "<localAnnounceEnabled>false</localAnnounceEnabled>",
		"<relaysEnabled>true</relaysEnabled>":                 "<relaysEnabled>false</relaysEnabled>",
		"<natEnabled>true</natEnabled>":                       "<natEnabled>false</natEnabled>",
		"<urAccepted>0</urAccepted>":                          "<urAccepted>-1</urAccepted>",
		"<crashReportingEnabled>true</crashReportingEnabled>": "<crashReportingEnabled>false</crashReportingEnabled>",
	} {
		if !strings.Contains(cfg, old) {
			t.Fatalf("generated config.xml lacks %q — adjust the harness for this Syncthing version", old)
		}
		cfg = strings.Replace(cfg, old, repl, 1)
	}
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "serve",
		"--home="+home,
		"--gui-address="+p.gui,
		"--no-browser", "--no-restart", "--no-upgrade", "--no-port-probing",
	)
	cmd.Env = append(os.Environ(), "STNODEFAULTFOLDER=1", "STNOUPGRADE=1", "STGUIAPIKEY="+p.apiKey)
	logFile, err := os.Create(filepath.Join(home, "stdout.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		logFile.Close()
		if t.Failed() {
			data, _ := os.ReadFile(filepath.Join(home, "stdout.log"))
			if len(data) > 8000 {
				data = data[len(data)-8000:]
			}
			t.Logf("--- %s log (tail) ---\n%s", name, data)
		}
	})
	waitFor(t, name+" API", 60*time.Second, func() bool {
		return p.do(http.MethodGet, "/rest/system/ping", nil, nil) == nil
	})
	var status struct {
		MyID string `json:"myID"`
	}
	p.mustDo(t, http.MethodGet, "/rest/system/status", nil, &status)
	p.id = status.MyID
	return p
}

func (p *peerInstance) do(method, path string, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(context.Background(), method, "http://"+p.gui+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", p.apiKey)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var buf bytes.Buffer
		buf.ReadFrom(resp.Body)
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(buf.String()))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (p *peerInstance) mustDo(t *testing.T, method, path string, in, out any) {
	t.Helper()
	if err := p.do(method, path, in, out); err != nil {
		t.Fatalf("peer %s %s: %v", method, path, err)
	}
}

// connectionCount returns how many connections the peer currently holds to
// deviceID (primary plus secondaries), as /rest/system/connections reports.
func (p *peerInstance) connectionCount(t *testing.T, deviceID string) int {
	t.Helper()
	var out struct {
		Connections map[string]struct {
			Connected bool              `json:"connected"`
			Primary   *json.RawMessage  `json:"primary"`
			Secondary []json.RawMessage `json:"secondary"`
		} `json:"connections"`
	}
	p.mustDo(t, http.MethodGet, "/rest/system/connections", nil, &out)
	c, ok := out.Connections[deviceID]
	if !ok || !c.Connected {
		return 0
	}
	return 1 + len(c.Secondary)
}

func (p *peerInstance) dbStatus(t *testing.T, folderID string) peerDBStatus {
	t.Helper()
	var st peerDBStatus
	p.mustDo(t, http.MethodGet, "/rest/db/status?folder="+folderID, nil, &st)
	return st
}

// scanAndSettle triggers a scan and waits until the folder is idle again.
func (p *peerInstance) scanAndSettle(t *testing.T, folderID string) {
	t.Helper()
	p.mustDo(t, http.MethodPost, "/rest/db/scan?folder="+folderID, nil, nil)
	// The scan runs asynchronously; give it a moment to leave idle, then
	// wait for idle again.
	time.Sleep(300 * time.Millisecond)
	waitFor(t, "peer scan of "+folderID, 120*time.Second, func() bool {
		return p.dbStatus(t, folderID).State == "idle"
	})
}

func writeRound(t *testing.T, folderPath string, round, n int) {
	t.Helper()
	dir := filepath.Join(folderPath, fmt.Sprintf("round-%02d", round))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		content := fmt.Sprintf("# note %d\n\nround %d, entry %d\n", i, round, i)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("note-%05d.md", i)), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func removeRound(t *testing.T, folderPath string, round, n int) {
	t.Helper()
	dir := filepath.Join(folderPath, fmt.Sprintf("round-%02d", round))
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
}

func bridgeConnected(deviceID string) bool {
	var conns []DeviceInfo
	if err := json.Unmarshal([]byte(GetConnectionsJSON()), &conns); err != nil {
		return false
	}
	for _, c := range conns {
		if c.DeviceID == deviceID && c.Connected {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
