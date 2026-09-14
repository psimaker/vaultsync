package main

import (
	"context"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// End-to-end against two real Syncthing instances: a Hub and a device. Runs
// only when VAULTSYNC_HUB_SYNCTHING_BIN points at a syncthing binary (CI
// builds one from go/_syncthing_patched, exactly like the notify E2E). Both
// instances are pinned to loopback with discovery, relays and NAT disabled, so
// the test never touches a real network.
//
// It proves the whole Milestone-1 promise: `init` shapes the Hub, a device
// pairs with the code, the Hub creates and shares the vault, the device accepts
// it into an empty directory, and a file written on either side arrives on the
// other.

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

type stInstance struct {
	home   string
	gui    string
	listen string
	client *SyncthingClient
	id     string
}

func startSyncthing(t *testing.T, bin, name string) *stInstance {
	t.Helper()
	home := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	guiPort := freePort(t)
	listenPort := freePort(t)
	inst := &stInstance{
		home:   home,
		gui:    "127.0.0.1:" + strconv.Itoa(guiPort),
		listen: "tcp://127.0.0.1:" + strconv.Itoa(listenPort),
	}
	// Generate the identity and config first and pin the instance to loopback
	// *before* it ever starts: no default listener on :22000, no discovery
	// announcement, no relay — the test must not touch a real network.
	gen := exec.Command(bin, "generate", "--home="+home, "--no-port-probing")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("syncthing generate: %v\n%s", err, out)
	}
	cfgPath := filepath.Join(home, "config.xml")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(raw)
	for old, repl := range map[string]string{
		"<listenAddress>default</listenAddress>":              "<listenAddress>" + inst.listen + "</listenAddress>",
		"<globalAnnounceEnabled>true</globalAnnounceEnabled>": "<globalAnnounceEnabled>false</globalAnnounceEnabled>",
		"<localAnnounceEnabled>true</localAnnounceEnabled>":   "<localAnnounceEnabled>false</localAnnounceEnabled>",
		"<relaysEnabled>true</relaysEnabled>":                 "<relaysEnabled>false</relaysEnabled>",
		"<natEnabled>true</natEnabled>":                       "<natEnabled>false</natEnabled>",
		"<urAccepted>0</urAccepted>":                          "<urAccepted>-1</urAccepted>",
		"<crashReportingEnabled>true</crashReportingEnabled>": "<crashReportingEnabled>false</crashReportingEnabled>",
	} {
		if !strings.Contains(cfg, old) {
			t.Fatalf("generated config.xml lacks %q — adjust the test for this Syncthing version", old)
		}
		cfg = strings.Replace(cfg, old, repl, 1)
	}
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "serve",
		"--home="+home,
		"--gui-address="+inst.gui,
		"--no-browser", "--no-restart", "--no-upgrade", "--no-port-probing",
	)
	cmd.Env = append(os.Environ(), "STNODEFAULTFOLDER=1", "STNOUPGRADE=1", "STGUIAPIKEY=e2e-"+name)
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
			t.Logf("--- %s log ---\n%s", name, data)
		}
	})

	inst.client = NewSyncthingClient("http://"+inst.gui, "e2e-"+name)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for {
		if err := inst.client.Ping(ctx); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s: API did not come up", name)
		case <-time.After(500 * time.Millisecond):
		}
	}
	inst.id, err = inst.client.MyID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	opts, err := inst.client.Options(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if la, _ := opts["listenAddresses"].([]any); len(la) != 1 || la[0] != inst.listen || opts["globalAnnounceEnabled"] != false {
		t.Fatalf("%s is not pinned to loopback: %v", name, opts["listenAddresses"])
	}
	return inst
}

func waitUntil(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func TestE2EHubPairsDeviceAndSyncsBothWays(t *testing.T) {
	bin := os.Getenv("VAULTSYNC_HUB_SYNCTHING_BIN")
	if bin == "" {
		t.Skip("set VAULTSYNC_HUB_SYNCTHING_BIN to a syncthing binary to run the real-Syncthing E2E")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	hubST := startSyncthing(t, bin, "hub")
	devST := startSyncthing(t, bin, "device")

	// --- Hub side: init + pairing service ---------------------------------
	if _, err := applyHubDefaults(ctx, hubST.client, "E2E Hub", true); err != nil {
		t.Fatal(err)
	}
	vaultsRoot := filepath.Join(hubST.home, "vaults")
	prov := newProvisioner(hubST.client, vaultsRoot, vaultsRoot)
	store := newStateStore(filepath.Join(hubST.home, "hub-state.json"))
	srv := newPairingServer(store, prov, "E2E Hub", "e2e")
	srv.logf = t.Logf
	pairingHTTP := httptest.NewServer(srv.handler())
	defer pairingHTTP.Close()
	code, err := generateCode()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := store.update(func(st *hubState) error {
		st.Pairing = &pairingCodeState{Scalar: passwordScalarForCode(code), CreatedAt: now, ExpiresAt: now.Add(pairingCodeTTL)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// --- Device side: pair, then accept locally ----------------------------
	client := newPairClient(strings.TrimPrefix(pairingHTTP.URL, "http://"))
	hello, err := client.handshake(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if hello.HubDeviceID != hubST.id {
		t.Fatalf("hub announced %s, real id %s", hello.HubDeviceID, hubST.id)
	}
	localProv := newProvisioner(devST.client, "", "")
	if err := localProv.ensureDevice(ctx, hello.HubDeviceID, hello.HubName); err != nil {
		t.Fatal(err)
	}
	// Loopback has no discovery: tell the device where the hub listens.
	if err := devST.client.PatchDevice(ctx, hubST.id, map[string]any{"addresses": []string{hubST.listen}}); err != nil {
		t.Fatal(err)
	}
	reply, err := client.provision(ctx, 1, devST.id, "E2E Device", "Notes", true)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Error != "" || reply.Provisioned == nil {
		t.Fatalf("provision: %+v", reply)
	}
	vault := *reply.Provisioned

	// The hub must have created the directory and configured the folder with
	// hub defaults, shared with exactly hub + device.
	hubFolders, err := hubST.client.Folders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hubFolders) != 1 || hubFolders[0].ID != vault.ID || hubFolders[0].Path != filepath.Join(vaultsRoot, "notes") {
		t.Fatalf("hub folders: %+v", hubFolders)
	}
	if hubFolders[0].MaxConflicts != -1 || hubFolders[0].Versioning.Type != "staggered" || len(hubFolders[0].Devices) != 2 {
		t.Fatalf("hub folder config: %+v", hubFolders[0])
	}
	if st, err := os.Stat(hubFolders[0].Path); err != nil || !st.IsDir() {
		t.Fatalf("vault directory missing: %v", err)
	}

	devicePath := filepath.Join(devST.home, "Notes")
	if err := acceptShareLocally(ctx, devST.client, localProv, hubST.id, devST.id, vault, devicePath); err != nil {
		t.Fatal(err)
	}

	// --- Both directions sync -------------------------------------------
	waitUntil(t, "devices to connect", 60*time.Second, func() bool {
		c, err := hubST.client.ConnectedDevices(ctx)
		return err == nil && c[devST.id]
	})
	if err := os.WriteFile(filepath.Join(hubFolders[0].Path, "from-hub.md"), []byte("# hub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(devicePath, "from-device.md"), []byte("# device\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "hub file on device", 90*time.Second, func() bool {
		data, err := os.ReadFile(filepath.Join(devicePath, "from-hub.md"))
		return err == nil && string(data) == "# hub\n"
	})
	waitUntil(t, "device file on hub", 90*time.Second, func() bool {
		data, err := os.ReadFile(filepath.Join(hubFolders[0].Path, "from-device.md"))
		return err == nil && string(data) == "# device\n"
	})

	// Re-running the accept is a no-op, and a second pairing into a non-empty
	// local directory against a non-empty hub vault is refused.
	if err := acceptShareLocally(ctx, devST.client, localProv, hubST.id, devST.id, vault, devicePath); err != nil {
		t.Fatalf("second accept: %v", err)
	}
	other := filepath.Join(devST.home, "Other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "existing.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Hub vault now has files (>0) — ask the hub for a fresh view.
	reply, err = client.provision(ctx, 2, devST.id, "E2E Device", "", false)
	if err != nil || reply.Error != "" {
		t.Fatalf("list: %+v %v", reply, err)
	}
	seq := uint64(2)
	waitUntil(t, "hub file count", 30*time.Second, func() bool {
		seq++
		r, err := client.provision(ctx, seq, devST.id, "E2E Device", "", false)
		return err == nil && len(r.Vaults) == 1 && r.Vaults[0].Files > 0
	})
	seq++
	r, err := client.provision(ctx, seq, devST.id, "E2E Device", "", false)
	if err != nil {
		t.Fatal(err)
	}
	fake := vaultInfo{ID: "vs-000000000000", Label: "Other", Files: r.Vaults[0].Files}
	if err := acceptShareLocally(ctx, devST.client, localProv, hubST.id, devST.id, fake, other); err == nil || !strings.Contains(err.Error(), "never merges") {
		t.Fatalf("non-empty/non-empty accept must refuse: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "existing.md")); err != nil {
		t.Fatal("refusal touched the existing directory")
	}
}
