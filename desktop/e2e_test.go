//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psimaker/vaultsync/hub/pairing"
	"github.com/psimaker/vaultsync/hub/syncthing"
)

// The acceptance test of #175, end to end and on loopback only:
//
//   - a fresh home directory; the agent installs Syncthing from the real,
//     pinned release archive (served locally, verified against the checksum
//     compiled into the agent);
//   - the agent's engine and the Hub's Syncthing are two instances of that
//     stock binary, pinned to 127.0.0.1 with discovery, relays and NAT off;
//   - the real vaultsync-hub (built from ../hub) runs the Hub;
//   - the agent pairs by code, starts "Notes" on the Hub from a folder that
//     already holds notes (with consent), and files sync both ways;
//   - a folder that already holds files is never connected to the Hub's
//     populated vault, and a new vault from a folder with files needs consent.
//
// Runs when VAULTSYNC_DESKTOP_SYNCTHING_ARCHIVE names the pinned archive for
// this platform (CI's Desktop Syncthing E2E downloads it).

func e2eFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func e2eWait(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// e2eHubBinary builds vaultsync-hub from this repository's hub/ (or uses
// VAULTSYNC_DESKTOP_HUB_BIN).
func e2eHubBinary(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("VAULTSYNC_DESKTOP_HUB_BIN"); bin != "" {
		return bin
	}
	bin := filepath.Join(t.TempDir(), "vaultsync-hub")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = filepath.Join("..", "hub")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build vaultsync-hub: %v\n%s", err, out)
	}
	return bin
}

type e2eHub struct {
	home, vaults, listen string
	client               *syncthing.Client
	id                   string
	bin                  string
	env                  []string
	pairAddr             string
}

// startE2EHub runs the Hub: its own Syncthing (the stock binary the agent
// installed) and `vaultsync-hub serve`, both on loopback.
func startE2EHub(t *testing.T, ctx context.Context, syncthingBin string) *e2eHub {
	t.Helper()
	h := &e2eHub{home: filepath.Join(t.TempDir(), "hub"), bin: e2eHubBinary(t)}
	h.vaults = filepath.Join(h.home, "vaults")
	if err := os.MkdirAll(h.vaults, 0o700); err != nil {
		t.Fatal(err)
	}
	gen := exec.Command(syncthingBin, "generate", "--home="+h.home, "--no-port-probing")
	gen.Env = engineEnv()
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("hub syncthing generate: %v\n%s", err, out)
	}
	cfgPath := filepath.Join(h.home, "config.xml")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	h.listen = "tcp://127.0.0.1:" + strconv.Itoa(e2eFreePort(t))
	gui := "127.0.0.1:" + strconv.Itoa(e2eFreePort(t))
	cfg := string(raw)
	for old, repl := range map[string]string{
		"<listenAddress>default</listenAddress>":              "<listenAddress>" + h.listen + "</listenAddress>",
		"<globalAnnounceEnabled>true</globalAnnounceEnabled>": "<globalAnnounceEnabled>false</globalAnnounceEnabled>",
		"<localAnnounceEnabled>true</localAnnounceEnabled>":   "<localAnnounceEnabled>false</localAnnounceEnabled>",
		"<relaysEnabled>true</relaysEnabled>":                 "<relaysEnabled>false</relaysEnabled>",
		"<natEnabled>true</natEnabled>":                       "<natEnabled>false</natEnabled>",
		"<urAccepted>0</urAccepted>":                          "<urAccepted>-1</urAccepted>",
		"<crashReportingEnabled>true</crashReportingEnabled>": "<crashReportingEnabled>false</crashReportingEnabled>",
		// `vaultsync-hub serve` switches global discovery on (Hub
		// defaults); its announcements then go nowhere.
		"<globalAnnounceServer>default</globalAnnounceServer>": "<globalAnnounceServer>https://127.0.0.1:1/</globalAnnounceServer>",
	} {
		if strings.Count(cfg, old) != 1 {
			t.Fatalf("the Hub's generated config.xml lacks %q — adjust the test for this Syncthing version", old)
		}
		cfg = strings.Replace(cfg, old, repl, 1)
	}
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	guiCfg, err := syncthing.ParseGUIConfig(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	st := exec.Command(syncthingBin, "serve", "--home="+h.home, "--gui-address=http://"+gui,
		"--no-browser", "--no-restart", "--no-upgrade", "--log-file="+filepath.Join(h.home, "syncthing.log"))
	st.Env = engineEnv()
	st.Stdout, st.Stderr = io.Discard, io.Discard
	if err := st.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = st.Process.Kill()
		_, _ = st.Process.Wait()
		if t.Failed() {
			data, _ := os.ReadFile(filepath.Join(h.home, "syncthing.log"))
			t.Logf("--- Hub Syncthing log ---\n%s", data)
		}
	})
	h.client = syncthing.NewClient("http://"+gui, guiCfg.APIKey)
	if err := waitReady(ctx, h.client, 60*time.Second); err != nil {
		t.Fatal(err)
	}
	if h.id, err = h.client.MyID(ctx); err != nil {
		t.Fatal(err)
	}

	port := e2eFreePort(t)
	h.pairAddr = "127.0.0.1:" + strconv.Itoa(port)
	h.env = append(os.Environ(),
		"SYNCTHING_CONFIG="+cfgPath,
		"SYNCTHING_API_URL=http://"+gui,
		"VAULTSYNC_HUB_STATE="+filepath.Join(h.home, "hub-state.json"),
		"VAULTSYNC_HUB_VAULTS="+h.vaults,
		"VAULTSYNC_HUB_PORT="+strconv.Itoa(port),
		"VAULTSYNC_HUB_NAME=E2E Hub",
	)
	serveCtx, cancel := context.WithCancel(ctx)
	serve := exec.CommandContext(serveCtx, h.bin, "serve", "--listen", h.pairAddr, "--no-discovery")
	serve.Env = h.env
	var serveLog bytes.Buffer
	serve.Stdout, serve.Stderr = &serveLog, &serveLog
	if err := serve.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = serve.Wait()
		if t.Failed() {
			t.Logf("--- vaultsync-hub serve ---\n%s", serveLog.String())
		}
	})
	e2eWait(t, "vaultsync-hub serve", 30*time.Second, func() bool {
		resp, err := http.Get("http://" + h.pairAddr + "/v1/hub")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
	// serve applied the Hub defaults, which switch discovery, relays and NAT
	// on; switch them off again — this test stays on loopback.
	if err := h.client.PatchOptions(ctx, map[string]any{
		"globalAnnounceEnabled": false, "localAnnounceEnabled": false, "relaysEnabled": false, "natEnabled": false,
	}); err != nil {
		t.Fatal(err)
	}
	return h
}

var e2eCodePattern = regexp.MustCompile(`\b[A-Z]{3,9}-[A-Z]{3,9}-\d{2}\b`)

// issueCode runs `vaultsync-hub code`; its output holds the code, so it is
// never logged.
func (h *e2eHub) issueCode(t *testing.T) string {
	t.Helper()
	cmd := exec.Command(h.bin, "code", "--no-qr")
	cmd.Env = h.env
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("vaultsync-hub code failed: %v", err)
	}
	code := e2eCodePattern.FindString(string(out))
	if code == "" {
		t.Fatal("vaultsync-hub code printed no code")
	}
	return code
}

func TestIssue175_E2EFreshHomeInstallsPairsAndSyncs(t *testing.T) {
	archivePath := os.Getenv("VAULTSYNC_DESKTOP_SYNCTHING_ARCHIVE")
	if archivePath == "" {
		t.Skip("set VAULTSYNC_DESKTOP_SYNCTHING_ARCHIVE to the pinned Syncthing archive for this platform")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	// A fresh home directory.
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	lay, err := layoutFor(runtime.GOOS, home, envOf(nil))
	if err != nil {
		t.Fatal(err)
	}

	// The agent installs the pinned Syncthing; the archive comes from a
	// local server, the checksum is the one compiled into the agent.
	in, err := newInstaller(lay.Bin, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	in.baseURL = serveArchive(t, in.pin.archive, data).URL
	got, err := in.install(ctx, nil)
	if err != nil {
		t.Fatalf("installing the pinned Syncthing: %v", err)
	}
	var st agentState
	st.Syncthing.Version, st.Syncthing.BinarySHA256 = syncthingVersion, got.SHA256

	// The agent's engine, pinned to loopback, run by its own supervisor.
	eng := engine{lay: lay, opts: engineOptions{loopbackOnly: true, listenPort: e2eFreePort(t)}}
	if err := eng.prepare(ctx, &st); err != nil {
		t.Fatal(err)
	}
	if err := saveState(lay.State, st); err != nil {
		t.Fatal(err)
	}
	runCtx, stopEngine := context.WithCancel(ctx)
	engineDone := make(chan error, 1)
	// What the agent itself logs goes to the service log or journal: it must
	// never name a vault or a path (checked at the end).
	var serviceLog strings.Builder
	var logMu sync.Mutex
	logf := func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		serviceLog.WriteString(fmt.Sprintf(format, args...) + "\n")
		t.Logf(format, args...)
	}
	go func() { engineDone <- eng.supervise(runCtx, st, logf) }()
	t.Cleanup(func() {
		stopEngine()
		<-engineDone
		if t.Failed() {
			data, _ := os.ReadFile(filepath.Join(lay.Home, "syncthing.log"))
			t.Logf("--- agent engine log ---\n%s", data)
		}
	})
	client, err := eng.client(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitReady(ctx, client, 60*time.Second); err != nil {
		t.Fatal(err)
	}
	opts, err := client.Options(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if la, _ := opts["listenAddresses"].([]any); len(la) != 1 || !strings.HasPrefix(la[0].(string), "tcp://127.0.0.1:") ||
		opts["globalAnnounceEnabled"] != false || opts["relaysEnabled"] != false || opts["urAccepted"] != float64(-1) ||
		opts["crashReportingEnabled"] != false || opts["autoUpgradeIntervalH"] != float64(0) || opts["startBrowser"] != false {
		t.Fatalf("the agent's engine is not set up as prepared: %v", opts)
	}
	// Another process taking over the engine is refused.
	if _, err := lockFile(lay.Lock); err != ErrEngineRunning {
		t.Fatalf("a second engine owner was not refused: %v", err)
	}

	hub, newSession, code := e2ePairAndSync(t, ctx, home, lay, client, got.Path)

	// A folder that already holds files never joins the Hub's populated
	// vault: nothing is asked of the Hub, nothing is added, nothing changes.
	other := filepath.Join(home, "Other")
	mkVault(t, other)
	before, _ := client.Folders(ctx)
	err = newSession(pairOptions{vault: "Notes", path: other}).run(ctx)
	if !strings.Contains(refusalText(err), "only into a new or empty folder") {
		t.Fatalf("expected a refusal, got %v", err)
	}
	after, _ := client.Folders(ctx)
	if len(after) != len(before) {
		t.Fatal("a folder was added")
	}
	assertUntouched(t, other)

	// A new vault from a folder with files needs consent; without it the
	// Hub is not asked to start anything.
	third := filepath.Join(home, "Third")
	mkVault(t, third)
	err = newSession(pairOptions{vault: "Third", create: true, path: third}).run(ctx)
	if !strings.Contains(refusalText(err), "--yes") {
		t.Fatalf("expected the consent refusal, got %v", err)
	}
	if fs, _ := hub.client.Folders(ctx); len(fs) != 1 {
		t.Fatalf("the Hub started a vault without consent: %+v", fs)
	}
	assertUntouched(t, third)

	logMu.Lock()
	logged := serviceLog.String()
	logMu.Unlock()
	for _, secret := range []string{"Notes", "Third", "Other", home, code} {
		if strings.Contains(logged, secret) {
			t.Errorf("the agent's own log names %q:\n%s", secret, logged)
		}
	}
}

// e2ePairAndSync pairs this computer's engine — client, whoever runs it —
// with a fresh Hub running binary: an existing vault becomes "Notes" on the
// Hub with consent, and a note crosses each way. Pairing is where the agent
// edits the engine's config (#175, #267). It returns the Hub, a maker of
// sessions with the Hub's pairing code, and that code.
func e2ePairAndSync(t *testing.T, ctx context.Context, home string, lay layout, client *syncthing.Client, binary string) (*e2eHub, func(pairOptions) *pairSession, string) {
	t.Helper()
	hub := startE2EHub(t, ctx, binary)
	code := hub.issueCode(t)

	newSession := func(opts pairOptions) *pairSession {
		opts.code, opts.hub = code, hub.pairAddr
		return &pairSession{
			t:    &term{out: &testWriter{t}},
			opts: opts,
			env: pairEnv{
				goos: runtime.GOOS, home: home, getenv: envOf(nil), lay: lay, engine: client,
				discover:       func(context.Context) ([]pairing.DiscoveredHub, error) { return nil, nil },
				dial:           pairing.NewLocalClient,
				registries:     func() []string { return obsidianRegistries(runtime.GOOS, home, envOf(nil)) },
				scanRoots:      []string{home},
				cloud:          func() []cloudRoot { return cloudRoots(cloudEnv{goos: runtime.GOOS, home: home, getenv: envOf(nil)}) },
				userST:         func() (userSyncthing, bool) { return findUserSyncthing(runtime.GOOS, home, envOf(nil)) },
				unitDir:        filepath.Join(home, "units"),
				pendingTimeout: 90 * time.Second,
				hubSyncAddress: hub.listen,
				deviceName:     "E2E Laptop",
				now:            time.Now,
			},
		}
	}

	// The main conversion: an existing vault becomes "Notes" on the Hub,
	// with consent.
	notes := filepath.Join(home, "Vaults", "Notes")
	mkVault(t, notes)
	if err := newSession(pairOptions{vault: "Notes", create: true, path: notes, yes: true}).run(ctx); err != nil {
		t.Fatalf("pairing: %v", err)
	}
	hubFolders, err := hub.client.Folders(ctx)
	if err != nil || len(hubFolders) != 1 || hubFolders[0].Label != "Notes" {
		t.Fatalf("Hub folders: %+v %v", hubFolders, err)
	}
	hubNotes := hubFolders[0].Path
	if hubFolders[0].MaxConflicts != -1 || hubFolders[0].Versioning.Type != "staggered" {
		t.Fatalf("the Hub's vault lacks the Hub defaults: %+v", hubFolders[0])
	}
	e2eWait(t, "the local note on the Hub", 120*time.Second, func() bool {
		b, err := os.ReadFile(filepath.Join(hubNotes, "Welcome.md"))
		return err == nil && string(b) == "# hello\n"
	})
	if err := os.WriteFile(filepath.Join(hubNotes, "from-hub.md"), []byte("# from the hub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e2eWait(t, "the Hub's note on this computer", 120*time.Second, func() bool {
		b, err := os.ReadFile(filepath.Join(notes, "from-hub.md"))
		return err == nil && string(b) == "# from the hub\n"
	})
	return hub, newSession, code
}

// testWriter sends the agent's output to the test log.
type testWriter struct{ t *testing.T }

func (w *testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// keptEnginePins are desktop-v0.2.0's pins — Syncthing at keepEngineFrom —
// for the E2E of an engine a run keeps (#267). The checksums are that
// release's, from Syncthing's signed sha256sum.txt.asc.
var keptEnginePins = map[string]syncthingPin{
	"darwin/amd64": {archive: "syncthing-macos-amd64-v2.1.6.zip", sha256: "d1464be24a7157440dfb52e2d84939f83caf89c129761945abcdd4956e09ffb1", binary: "syncthing-macos-amd64-v2.1.6/syncthing"},
	"darwin/arm64": {archive: "syncthing-macos-arm64-v2.1.6.zip", sha256: "f86bfa0bdbb5b1b13229a7e5643e898e3c4e017a4fff6dd525994ebaf28ff6e2", binary: "syncthing-macos-arm64-v2.1.6/syncthing"},
	"linux/amd64":  {archive: "syncthing-linux-amd64-v2.1.6.tar.gz", sha256: "524ef4e1df1850b719e2378c8369956f4e5f2ab9a46fdf7f45f2b925012147aa", binary: "syncthing-linux-amd64-v2.1.6/syncthing"},
	"linux/arm64":  {archive: "syncthing-linux-arm64-v2.1.6.tar.gz", sha256: "f054de02462d5d58bd126908f2f92b63aba74f162a5be85e50e96d656e7db47d", binary: "syncthing-linux-arm64-v2.1.6/syncthing"},
}

// The older engine a run keeps — Syncthing at keepEngineFrom — runs under
// this agent as its own: the run starts it where this build's pin cannot be
// installed, and it pairs with a Hub and syncs both ways, the agent editing
// its config through the engine's API as pairing does (#267). Set
// VAULTSYNC_DESKTOP_SYNCTHING_KEPT_ARCHIVE to that version's archive for
// this platform.
func TestIssue267_E2EAKeptOlderEnginePairsAndSyncs(t *testing.T) {
	archivePath := os.Getenv("VAULTSYNC_DESKTOP_SYNCTHING_KEPT_ARCHIVE")
	if archivePath == "" {
		t.Skip("set VAULTSYNC_DESKTOP_SYNCTHING_KEPT_ARCHIVE to the Syncthing " + keepEngineFrom + " archive for this platform")
	}
	pin, ok := keptEnginePins[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		t.Skipf("no Syncthing %s archive known for %s/%s", keepEngineFrom, runtime.GOOS, runtime.GOARCH)
	}
	if !strings.Contains(pin.archive, keepEngineFrom) {
		t.Fatalf("keptEnginePins names %s, keepEngineFrom is %s: the E2E must run on the version kept", pin.archive, keepEngineFrom)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	inATerminal(t)

	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tmp, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	lay, err := layoutFor(runtime.GOOS, home, envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	// What an earlier VaultSync installed: the older engine, from its
	// verified archive, recorded.
	data, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	in := installer{baseURL: serveArchive(t, pin.archive, data).URL, pin: pin, binDir: lay.Bin, client: downloadClient()}
	got, err := in.install(ctx, nil)
	if err != nil {
		t.Fatalf("installing Syncthing %s: %v", keepEngineFrom, err)
	}
	var st agentState
	st.Syncthing.Version, st.Syncthing.BinarySHA256 = keepEngineFrom, got.SHA256
	// Its home prepared loopback-only first: the run keeps that config, so
	// the real engine never leaves the loopback.
	prep := engine{lay: lay, opts: engineOptions{loopbackOnly: true, listenPort: e2eFreePort(t)}}
	if err := prep.prepare(ctx, &st); err != nil {
		t.Fatal(err)
	}
	if err := saveState(lay.State, st); err != nil {
		t.Fatal(err)
	}

	// This build's pin cannot be installed; the run keeps the older engine.
	pinServedBy(t, http.NotFound)
	runtimeDir := shortDir(t)
	env := envOf(map[string]string{"XDG_RUNTIME_DIR": runtimeDir})
	lay.Socket = filepath.Join(lay.Base, strings.Repeat("d", 120), "agent.sock")
	a := &app{goos: runtime.GOOS, home: home, getenv: env, lay: lay, out: io.Discard,
		svc: service{goos: runtime.GOOS, home: home, getenv: env, lay: lay, run: &fakeRunner{}}}
	var stop func() error
	logged := serviceLogOf(t, func() {
		stop = inBackground(t, func(ctx context.Context) error { return a.run(ctx, nil) })
		untilTheRunServes(t, a)
	})
	t.Cleanup(func() {
		if err := stop(); err != nil {
			t.Errorf("the run ended with %v", err)
		}
		if t.Failed() {
			data, _ := os.ReadFile(filepath.Join(lay.Home, "syncthing.log"))
			t.Logf("--- kept engine log ---\n%s", data)
		}
	})
	if !strings.Contains(logged, "the installed Syncthing "+strings.TrimPrefix(keepEngineFrom, "v")+" runs on") {
		t.Fatalf("the run did not keep the older engine:\n%s", logged)
	}
	client, err := prep.client(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitReady(ctx, client, 60*time.Second); err != nil {
		t.Fatal(err)
	}
	// The engine that answers is the older one, by its own word.
	if v := e2eEngineVersion(t, ctx, prep, st); v != keepEngineFrom {
		t.Fatalf("the engine that runs is Syncthing %s, not the kept %s", v, keepEngineFrom)
	}
	e2ePairAndSync(t, ctx, home, lay, client, got.Path)
}

// e2eEngineVersion asks the engine on its loopback API which Syncthing it is.
func e2eEngineVersion(t *testing.T, ctx context.Context, e engine, st agentState) string {
	t.Helper()
	key, err := e.apiKey()
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(st.GUIPort)+"/rest/system/version", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-API-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var v struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v.Version
}
