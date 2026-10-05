package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psimaker/vaultsync/hub/pairing"
)

// --- envelope helpers ---------------------------------------------------------

type hubTestEnvelope struct {
	V     int             `json:"v"`
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *hubError       `json:"error"`
}

func decodeHubEnvelope(t *testing.T, raw string) hubTestEnvelope {
	t.Helper()
	var e hubTestEnvelope
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		t.Fatalf("not an envelope: %v", err)
	}
	if e.V != hubEnvelopeVersion {
		t.Fatalf("envelope version %d", e.V)
	}
	if e.OK == (e.Error != nil) {
		t.Fatalf("envelope must carry exactly one of data and error: ok=%v error=%v", e.OK, e.Error)
	}
	return e
}

func wantHubKind(t *testing.T, raw, kind string) {
	t.Helper()
	e := decodeHubEnvelope(t, raw)
	if e.OK || e.Error.Kind != kind {
		got := "ok"
		if e.Error != nil {
			got = e.Error.Kind
		}
		t.Fatalf("kind %q, want %q", got, kind)
	}
}

func wantHubOK[T any](t *testing.T, raw string) T {
	t.Helper()
	e := decodeHubEnvelope(t, raw)
	if !e.OK {
		t.Fatalf("failed with %q", e.Error.Kind)
	}
	var out T
	if err := json.Unmarshal(e.Data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

type hubHandshakeData struct {
	HubName          string         `json:"hubName"`
	HubDeviceID      string         `json:"hubDeviceID"`
	CatalogAvailable bool           `json:"catalogAvailable"`
	Vaults           []hubVaultJSON `json:"vaults"`
}

type hubProvisionData struct {
	Provisioned hubVaultJSON `json:"provisioned"`
}

// --- pure rules -----------------------------------------------------------------

func TestIssue174_CodeAndAddressRules(t *testing.T) {
	if got := HubPairingNormalizeCode("  tulip anchor 7 "); got != "TULIP-ANCHOR-07" {
		t.Fatalf("normalize: %q", got)
	}
	if got := HubPairingNormalizeCode("tulip-zzzz-07"); got != "" {
		t.Fatalf("unknown word normalized to %q", got)
	}
	ok := wantHubOK[map[string]string](t, HubPairingCheckAddress("192.168.1.20"))
	if ok["address"] != "192.168.1.20:8390" {
		t.Fatalf("address: %q", ok["address"])
	}
	wantHubKind(t, HubPairingCheckAddress("8.8.8.8:8390"), "notLocal")
	wantHubKind(t, HubPairingCheckAddress("nas.local:8390"), "notLocal")
	wantHubKind(t, HubPairingCheckAddress("http://192.168.1.20:8390"), "badAddress")
}

// A flow belongs to the sheet that began it: a newer flow ends the older one,
// and nothing can act on a flow that ended (#174).
func TestIssue174_FlowsAreIsolated(t *testing.T) {
	first := HubPairingBegin()
	second := HubPairingBegin()
	if first == "" || second == "" || first == second {
		t.Fatalf("flow ids %q %q", first, second)
	}
	wantHubKind(t, HubPairingProvision(first, "vs-x", "iPhone"), "staleFlow")
	HubPairingEnd(first) // an old sheet's teardown must not end the new flow
	wantHubKind(t, HubPairingProvision(second, "vs-x", "iPhone"), "noSession")
	HubPairingEnd(second)
	wantHubKind(t, HubPairingProvision(second, "vs-x", "iPhone"), "staleFlow")
	wantHubKind(t, HubPairingDiscover("", 10), "staleFlow")
}

// An address from a link never reaches a socket unless it is on the local
// network — checked before any request is built.
func TestIssue174_HandshakeRefusesNonLocalAddresses(t *testing.T) {
	flow := HubPairingBegin()
	defer HubPairingEnd(flow)
	start := time.Now()
	wantHubKind(t, HubPairingHandshake(flow, "203.0.113.9:8390", "TULIP-ANCHOR-42"), "notLocal")
	wantHubKind(t, HubPairingHandshake(flow, "[2001:db8::1]:8390", "TULIP-ANCHOR-42"), "notLocal")
	wantHubKind(t, HubPairingHandshake(flow, "192.168.1.20:8390", "TULIP-NOTAWORD-42"), "badCode")
	if time.Since(start) > time.Second {
		t.Fatal("refusals waited for the network")
	}
}

func TestIssue174_DiscoveryAnswersAreFilteredToLocalSenders(t *testing.T) {
	got := hubLocalAnswers([]pairing.DiscoveredHub{
		{Address: "192.168.1.20:8390", Name: "Home"},
		{Address: "203.0.113.9:8390", Name: "Elsewhere"},
		{Address: "[fd00::2]:8390", Name: "ULA"},
	})
	if len(got) != 2 || got[0].Name != "Home" || got[1].Address != "[fd00::2]:8390" {
		t.Fatalf("answers: %+v", got)
	}
}

func TestIssue174_VaultSelectorMustBeUnambiguous(t *testing.T) {
	catalog := []pairing.VaultInfo{{ID: "vs-aaaaaaaaaaaa", Label: "Notes"}, {ID: "vs-bbbbbbbbbbbb", Label: "vs-cccccccccccc"}, {ID: "vs-cccccccccccc", Label: "Work"}}
	if err := hubCheckVault(catalog, "vs-aaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	if err := hubCheckVault(catalog, "vs-cccccccccccc"); err == nil {
		t.Fatal("an ID that is also another vault's label was accepted")
	}
	if err := hubCheckVault(catalog, "vs-dddddddddddd"); err == nil {
		t.Fatal("a vault outside the catalog was accepted")
	}
	if err := hubCheckVault(nil, "vs-aaaaaaaaaaaa"); err == nil {
		t.Fatal("no catalog accepted a vault")
	}
}

// A provision that broke after its request left may have been carried out:
// that is "outcome unknown", never a definite failure the user would retry.
func TestIssue174_ProvisionFailureKinds(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&pairing.APIError{Status: http.StatusForbidden, Body: "unknown or expired pairing session"}, "sessionExpired"},
		{&pairing.APIError{Status: http.StatusBadRequest, Body: "replayed request"}, "other"},
		{&net.OpError{Op: "dial", Err: errors.New("connection refused")}, "unreachable"},
		{fmt.Errorf("dial: %w", pairing.ErrNotLocal), "notLocal"},
		{&net.OpError{Op: "read", Err: errors.New("connection reset by peer")}, "outcomeUnknown"},
		{context.DeadlineExceeded, "outcomeUnknown"},
		{errors.New("hub reply could not be decrypted"), "outcomeUnknown"},
	}
	for _, c := range cases {
		if got := hubProvisionFailureKind(c.err); got != c.want {
			t.Errorf("%v: %q, want %q", c.err, got, c.want)
		}
	}
}

// --- against the real Hub ---------------------------------------------------------

// A checksum-valid device ID for the fake Hub (Syncthing's own test vector),
// never this engine's.
const hubTestDeviceID = "P56IOI7-MZJNU2Y-IQGDREY-DM2MGTI-MGL3BXN-PQ6W5BM-TBBZ4TJ-XZWICQ2"

// hubFakeSyncthing is the REST subset `vaultsync-hub serve` uses: startup
// defaults, the vault catalog, device registration and sharing.
type hubFakeSyncthing struct {
	apiKey  string
	mu      sync.Mutex
	devices []map[string]any
	folders []map[string]any
	files   map[string]int64
}

func (f *hubFakeSyncthing) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-API-Key") != f.apiKey {
		http.Error(w, "Not Authorized", http.StatusForbidden)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	write := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	decode := func(v any) bool {
		if err := json.NewDecoder(r.Body).Decode(v); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return false
		}
		return true
	}
	path := r.URL.Path
	switch {
	case path == "/rest/system/ping":
		write(map[string]string{"ping": "pong"})
	case path == "/rest/system/status":
		write(map[string]string{"myID": hubTestDeviceID})
	case path == "/rest/config/options" && r.Method == http.MethodGet:
		write(map[string]any{"urAccepted": -1})
	case path == "/rest/config/options" && r.Method == http.MethodPatch,
		path == "/rest/config/defaults/folder" && r.Method == http.MethodPatch:
		var ignored map[string]any
		decode(&ignored)
	case path == "/rest/config/devices" && r.Method == http.MethodGet:
		write(f.devices)
	case path == "/rest/config/devices" && r.Method == http.MethodPost:
		var d map[string]any
		if decode(&d) {
			f.devices = append(f.devices, d)
		}
	case strings.HasPrefix(path, "/rest/config/devices/") && r.Method == http.MethodPatch:
		var patch map[string]any
		if !decode(&patch) {
			return
		}
		id := strings.TrimPrefix(path, "/rest/config/devices/")
		for _, d := range f.devices {
			if d["deviceID"] == id {
				for k, v := range patch {
					d[k] = v
				}
				return
			}
		}
		http.NotFound(w, r)
	case path == "/rest/config/folders" && r.Method == http.MethodGet:
		write(f.folders)
	case strings.HasPrefix(path, "/rest/config/folders/") && r.Method == http.MethodGet:
		id := strings.TrimPrefix(path, "/rest/config/folders/")
		for _, folder := range f.folders {
			if folder["id"] == id {
				write(folder)
				return
			}
		}
		http.NotFound(w, r)
	case strings.HasPrefix(path, "/rest/config/folders/") && r.Method == http.MethodPut:
		id := strings.TrimPrefix(path, "/rest/config/folders/")
		var folder map[string]any
		if !decode(&folder) {
			return
		}
		for i := range f.folders {
			if f.folders[i]["id"] == id {
				f.folders[i] = folder
				return
			}
		}
		http.NotFound(w, r)
	case path == "/rest/db/status":
		write(map[string]any{"localFiles": f.files[r.URL.Query().Get("folder")], "state": "idle"})
	default:
		http.NotFound(w, r)
	}
}

func (f *hubFakeSyncthing) hasDevice(id string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.devices {
		if d["deviceID"] == id {
			name, _ := d["name"].(string)
			return name, true
		}
	}
	return "", false
}

func (f *hubFakeSyncthing) sharedWith(folderID, deviceID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, folder := range f.folders {
		if folder["id"] != folderID {
			continue
		}
		devices, _ := folder["devices"].([]any)
		for _, d := range devices {
			if m, ok := d.(map[string]any); ok && m["deviceID"] == deviceID {
				return true
			}
		}
	}
	return false
}

// hubBinary returns vaultsync-hub built from this repository's hub/ for this
// run (VAULTSYNC_BRIDGE_HUB_BIN when CI built it once). Never cached across
// runs: the Hub's source changes with the repository.
func hubBinary(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("VAULTSYNC_BRIDGE_HUB_BIN"); bin != "" {
		return bin
	}
	bin := filepath.Join(t.TempDir(), "vaultsync-hub")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = filepath.Join("..", "..", "hub")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build vaultsync-hub: %v\n%s", err, out)
	}
	return bin
}

type hubUnderTest struct {
	bin   string
	env   []string
	state string
	addr  string
	fake  *hubFakeSyncthing
}

func startHubUnderTest(t *testing.T) *hubUnderTest {
	t.Helper()
	dir := t.TempDir()
	fake := &hubFakeSyncthing{
		apiKey:  "bridge-test-key",
		devices: []map[string]any{{"deviceID": hubTestDeviceID, "name": "host"}},
		folders: []map[string]any{{
			"id": "vs-aaaaaaaaaaaa", "label": "Notes", "path": filepath.Join(dir, "vaults", "notes"),
			"type": "sendreceive", "devices": []any{map[string]any{"deviceID": hubTestDeviceID}},
		}},
		files: map[string]int64{"vs-aaaaaaaaaaaa": 3},
	}
	st := httptest.NewServer(fake)
	t.Cleanup(st.Close)
	configXML := filepath.Join(dir, "config.xml")
	if err := os.WriteFile(configXML, []byte(`<configuration><gui><apikey>bridge-test-key</apikey></gui></configuration>`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &hubUnderTest{
		bin:   hubBinary(t),
		state: filepath.Join(dir, "state.json"),
		fake:  fake,
	}
	port := freeTCPPort(t)
	h.addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	h.env = append(os.Environ(),
		"SYNCTHING_CONFIG="+configXML,
		"SYNCTHING_API_URL="+st.URL,
		"VAULTSYNC_HUB_STATE="+h.state,
		"VAULTSYNC_HUB_VAULTS="+filepath.Join(dir, "vaults"),
		"VAULTSYNC_HUB_PORT="+strconv.Itoa(port),
		"VAULTSYNC_HUB_NAME=Test Hub",
	)
	ctx, cancel := context.WithCancel(context.Background())
	serve := exec.CommandContext(ctx, h.bin, "serve", "--listen", h.addr, "--no-discovery")
	serve.Env = h.env
	var serveLog bytes.Buffer
	serve.Stdout, serve.Stderr = &serveLog, &serveLog
	if err := serve.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = serve.Wait()
	})
	// Ready when the info endpoint answers — never probe with a handshake,
	// which would count against the code.
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := http.Get("http://" + h.addr + "/v1/hub")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("vaultsync-hub serve did not come up:\n%s", serveLog.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	return h
}

var hubCodePattern = regexp.MustCompile(`\b[A-Z]{3,9}-[A-Z]{3,9}-\d{2}\b`)

// issueCode runs `vaultsync-hub code` and returns the code it printed. Its
// output carries the code, so it is never logged.
func (h *hubUnderTest) issueCode(t *testing.T) string {
	t.Helper()
	cmd := exec.Command(h.bin, "code", "--no-qr")
	cmd.Env = h.env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("vaultsync-hub code failed: %v", err)
	}
	code := hubCodePattern.FindString(string(out))
	if code == "" {
		t.Fatal("vaultsync-hub code printed no code")
	}
	return code
}

func (h *hubUnderTest) pairingState(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(h.state)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	p, _ := st["pairing"].(map[string]any)
	return p
}

// expireCode moves the code's expiry into the past. Only while no request is
// in flight: the Hub rewrites the file on every failed attempt.
func (h *hubUnderTest) expireCode(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile(h.state)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatal(err)
	}
	st["pairing"].(map[string]any)["expiresAt"] = time.Now().Add(-time.Minute).Format(time.RFC3339Nano)
	out, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	tmp := h.state + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, h.state); err != nil {
		t.Fatal(err)
	}
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// wrongCode turns a valid code into another valid one: same words, other
// number — so the Hub runs the full key confirmation and counts the failure.
func wrongCode(code string) string {
	parts := strings.Split(code, "-")
	n, _ := strconv.Atoi(parts[2])
	return fmt.Sprintf("%s-%s-%02d", parts[0], parts[1], (n+1)%100)
}

// The complete pairing against the real vaultsync-hub with a fake Syncthing
// behind it (#174): a wrong code fails after the Hub counted it, the right
// code pairs, provisioning registers this device on the Hub and shares the
// vault, the Hub becomes a device here — and nothing is accepted locally:
// the share is left to the app's pending-share flow. An expired code and an
// expired session are refused.
func TestIssue174_PairsWithTheRealHub(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs vaultsync-hub")
	}
	hub := startHubUnderTest(t)
	code := hub.issueCode(t)

	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	myID := DeviceID()

	flow := HubPairingBegin()
	defer HubPairingEnd(flow)

	// Wrong code: rejected, counted by the Hub, nothing registered.
	wantHubKind(t, HubPairingHandshake(flow, hub.addr, wrongCode(code)), "codeRejected")
	if failures, _ := hub.pairingState(t)["failures"].(float64); failures != 1 {
		t.Fatalf("the hub did not count the wrong code: failures=%v", failures)
	}
	wantHubKind(t, HubPairingProvision(flow, "vs-aaaaaaaaaaaa", "Test iPhone"), "noSession")

	// Right code, typed loosely: the bridge normalizes like the CLI.
	hello := wantHubOK[hubHandshakeData](t, HubPairingHandshake(flow, hub.addr, strings.ToLower(strings.ReplaceAll(code, "-", " "))))
	if hello.HubName != "Test Hub" || hello.HubDeviceID != hubTestDeviceID || !hello.CatalogAvailable {
		t.Fatalf("hello: %+v", hello)
	}
	if len(hello.Vaults) != 1 || hello.Vaults[0].ID != "vs-aaaaaaaaaaaa" || hello.Vaults[0].Label != "Notes" || hello.Vaults[0].Files != 3 {
		t.Fatalf("catalog: %+v", hello.Vaults)
	}
	wantHubKind(t, HubPairingProvision(flow, "vs-not-on-hub", "Test iPhone"), "unknownVault")

	got := wantHubOK[hubProvisionData](t, HubPairingProvision(flow, "vs-aaaaaaaaaaaa", "Test iPhone"))
	if got.Provisioned.ID != "vs-aaaaaaaaaaaa" || got.Provisioned.Label != "Notes" {
		t.Fatalf("provisioned: %+v", got.Provisioned)
	}
	if name, ok := hub.fake.hasDevice(myID); !ok || name != "Test iPhone" {
		t.Fatalf("this device is not registered on the hub (%q, %v)", name, ok)
	}
	if !hub.fake.sharedWith("vs-aaaaaaaaaaaa", myID) {
		t.Fatal("the hub did not share the vault with this device")
	}
	var devices []DeviceInfo
	if err := json.Unmarshal([]byte(GetDevicesJSON()), &devices); err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].DeviceID != hubTestDeviceID || devices[0].Name != "Test Hub" {
		t.Fatalf("the hub is not a device here: %+v", devices)
	}
	var folders []json.RawMessage
	if err := json.Unmarshal([]byte(GetFoldersJSON()), &folders); err != nil {
		t.Fatal(err)
	}
	if len(folders) != 0 {
		t.Fatalf("pairing accepted a folder by itself: %d folders", len(folders))
	}

	// Pairing again keeps the configured Hub as it is (the user may have
	// renamed it).
	if errMsg := RenameDevice(hubTestDeviceID, "My Hub"); errMsg != "" {
		t.Fatal(errMsg)
	}
	wantHubOK[hubHandshakeData](t, HubPairingHandshake(flow, hub.addr, code))
	wantHubOK[hubProvisionData](t, HubPairingProvision(flow, "vs-aaaaaaaaaaaa", "Test iPhone"))
	if err := json.Unmarshal([]byte(GetDevicesJSON()), &devices); err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].Name != "My Hub" {
		t.Fatalf("re-pairing changed the configured hub: %+v", devices)
	}

	// The local deadline ends a session before the Hub's five minutes do.
	wantHubOK[hubHandshakeData](t, HubPairingHandshake(flow, hub.addr, code))
	hubNow = func() time.Time { return time.Now().Add(hubSessionDeadline + time.Second) }
	wantHubKind(t, HubPairingProvision(flow, "vs-aaaaaaaaaaaa", "Test iPhone"), "sessionExpired")
	hubNow = time.Now

	// An expired code is refused before any key exchange.
	hub.expireCode(t)
	wantHubKind(t, HubPairingHandshake(flow, hub.addr, code), "codeExpired")
}
