//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/psimaker/vaultsync/hub/pairing"
	"github.com/psimaker/vaultsync/hub/syncthing"
)

// The control socket (#176): what the running agent answers, and how the
// terminal reads it.

// shortDir is a temporary directory short enough for a socket address
// (104 bytes on macOS); t.TempDir is not.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "vs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// agentApp is an app whose engine is the fake (config.xml and agent.json
// point at it) and whose control socket has a short address.
func agentApp(t *testing.T, eng *fakeEngine) *app {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tmp, "home")
	lay, err := layoutFor("linux", home, envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(eng.addr)
	n, _ := strconv.Atoi(port)
	writeFile(t, filepath.Join(lay.Home, "config.xml"), "<configuration><gui><address>"+eng.addr+"</address><apikey>engine-key</apikey></gui></configuration>\n")
	if err := saveState(lay.State, agentState{GUIPort: n}); err != nil {
		t.Fatal(err)
	}
	lay.Socket = filepath.Join(shortDir(t), "agent.sock")
	return &app{goos: "linux", home: home, getenv: envOf(nil), lay: lay, out: &bytes.Buffer{},
		svc: service{goos: "linux", home: home, getenv: envOf(nil), lay: lay, run: &fakeRunner{}}}
}

// serveControl starts the agent's side on a.lay.Socket and returns a client.
func serveControl(t *testing.T, a *app) *controlClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ctl := &controlServer{a: a, eng: a.engine(), logf: t.Logf}
	path, stop, err := ctl.serve(ctx)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { stop(); cancel() })
	if path != a.lay.Socket {
		t.Fatalf("listened on %s, wanted %s", path, a.lay.Socket)
	}
	return newControlClient(path)
}

func hubDevice(eng *fakeEngine) {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	eng.devices = append(eng.devices, syncthing.DeviceConfig{DeviceID: testHubID, Name: "Test Hub"})
	eng.folders = append(eng.folders, syncthingFolder("vs-aaaaaaaaaaaa", "Notes", "/home/me/Notes"))
	eng.folders[0].Devices = []syncthing.FolderDevice{folderDeviceOf(testMyID), folderDeviceOf(testHubID)}
}

func TestIssue176_StatusOverTheSocketIsWhatTheTerminalPrints(t *testing.T) {
	eng := newFakeEngine(t)
	hubDevice(eng)
	a := agentApp(t, eng)
	client := serveControl(t, a)

	cs, err := client.status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cs.Engine != "running" || cs.Version != version || cs.Syncthing != strings.TrimPrefix(syncthingVersion, "v") {
		t.Fatalf("status: %+v", cs)
	}
	if len(cs.Hubs) != 1 || cs.Hubs[0].Name != "Test Hub" || cs.Hubs[0].State != "not connected" || cs.Hubs[0].ID != testHubID {
		t.Fatalf("hubs: %+v", cs.Hubs)
	}
	if len(cs.Vaults) != 1 || cs.Vaults[0].Label != "Notes" || cs.Vaults[0].Path != "/home/me/Notes" || cs.Vaults[0].State != "waiting for your Hub" {
		t.Fatalf("vaults: %+v", cs.Vaults)
	}
	if cs.Paused {
		t.Fatal("nothing is paused")
	}
	// The text is the terminal's, byte for byte.
	var direct bytes.Buffer
	if err := a.onlineStatus(context.Background(), &direct, eng.client, false); err != nil {
		t.Fatal(err)
	}
	if cs.Text != direct.String() {
		t.Fatalf("socket text:\n%s\nterminal text:\n%s", cs.Text, direct.String())
	}
	for _, want := range []string{"\nHub\n  Test Hub               not connected\n", "\nVaults\n  Notes          /home/me/Notes               waiting for your Hub\n"} {
		if !strings.Contains(cs.Text, want) {
			t.Fatalf("text lacks %q:\n%s", want, cs.Text)
		}
	}

	// The socket is the user's alone.
	if fi, err := os.Stat(a.lay.Socket); err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket: %v %v", fi, err)
	}
	// Anything else is not this API.
	if err := client.do(context.Background(), http.MethodGet, "/v2/status", nil, nil); !errors.Is(err, errNoAgent) {
		t.Fatalf("an unknown path reads as no agent, got %v", err)
	}
}

func TestIssue176_PauseAndResumeTouchEveryHubAndNothingElse(t *testing.T) {
	eng := newFakeEngine(t)
	a := agentApp(t, eng)
	client := serveControl(t, a)
	ctx := context.Background()

	// No Hub yet: nothing to pause, and the terminal says so.
	res, err := client.setPaused(ctx, true)
	if err != nil || res.Hubs != 0 {
		t.Fatalf("pause without a Hub: %+v %v", res, err)
	}
	if err := a.pauseSync(ctx, true); !strings.Contains(refusalText(err), "No Hub is paired") {
		t.Fatalf("terminal pause without a Hub: %v", err)
	}

	hubDevice(eng)
	res, err = client.setPaused(ctx, true)
	if err != nil || !res.Paused || res.Hubs != 1 {
		t.Fatalf("pause: %+v %v", res, err)
	}
	if len(eng.patches) != 1 || eng.patches[0]["paused"] != true || len(eng.patches[0]) != 1 {
		t.Fatalf("pause patches exactly the paused flag of the Hub: %v", eng.patches)
	}
	cs, err := client.status(ctx)
	if err != nil || !cs.Paused {
		t.Fatalf("status after pause: %+v %v", cs, err)
	}
	if cs.Hubs[0].State != "paused on this computer — vaultsync resume starts syncing again" || cs.Vaults[0].State != "paused on this computer" {
		t.Fatalf("paused states: %+v %+v", cs.Hubs, cs.Vaults)
	}
	// Pausing again changes nothing; a pairing is refused while paused.
	if _, err := client.setPaused(ctx, true); err != nil || len(eng.patches) != 1 {
		t.Fatalf("second pause: %v %v", err, eng.patches)
	}
	if pr, err := client.pair(ctx, pairRequest{Code: "x"}); err != nil || pr.OK || !strings.Contains(pr.Refusal, "paused") {
		t.Fatalf("pair while paused: %+v %v", pr, err)
	}

	res, err = client.setPaused(ctx, false)
	if err != nil || res.Paused || res.Hubs != 1 || len(eng.patches) != 2 || eng.patches[1]["paused"] != false {
		t.Fatalf("resume: %+v %v %v", res, err, eng.patches)
	}
	if cs, err := client.status(ctx); err != nil || cs.Paused || cs.Hubs[0].State != "not connected" {
		t.Fatalf("status after resume: %+v %v", cs, err)
	}

	// The terminal's words, through the same socket.
	a.out = &bytes.Buffer{}
	if err := a.pauseSync(ctx, true); err != nil || !strings.Contains(a.out.(*bytes.Buffer).String(), "✓ Syncing is paused on this computer") {
		t.Fatalf("terminal pause: %v %s", err, a.out)
	}
	a.out = &bytes.Buffer{}
	if err := a.pauseSync(ctx, false); err != nil || !strings.Contains(a.out.(*bytes.Buffer).String(), "✓ Syncing resumed") {
		t.Fatalf("terminal resume: %v %s", err, a.out)
	}
}

// A pairing over the socket is vaultsync pair's flag flow: every decision
// the flags did not make is refused with the flag to pass — and with the
// menu, so the caller can choose and ask again with the same code.
func TestIssue176_PairOverTheSocketIsTheFlagFlow(t *testing.T) {
	eng := newFakeEngine(t)
	hub := newFakeHub(t, eng, pairing.VaultInfo{ID: "vs-aaaaaaaaaaaa", Label: "Notes", Files: 0})
	a := agentApp(t, eng)
	client := serveControl(t, a)
	ctx := context.Background()

	pr, err := client.pair(ctx, pairRequest{Hub: hub.addr})
	if err != nil || pr.OK || !strings.Contains(pr.Refusal, "--code") {
		t.Fatalf("without a code: %+v %v", pr, err)
	}
	pr, err = client.pair(ctx, pairRequest{Code: hub.code, Hub: hub.addr})
	if err != nil || pr.OK || !strings.Contains(pr.Refusal, "--vault NAME and --path FOLDER") {
		t.Fatalf("without a vault: %+v %v", pr, err)
	}
	if pr.Menu == nil || len(pr.Menu.Hub) != 1 || pr.Menu.Hub[0].Label != "Notes" || pr.Menu.Hub[0].Vault != "vs-aaaaaaaaaaaa" || pr.Menu.Local == nil || pr.Menu.Already == nil {
		t.Fatalf("the refusal carries the menu: %+v", pr.Menu)
	}
	if !strings.Contains(pr.Output, "✓ Connected to") || eng.folderCount() != 0 {
		t.Fatalf("output:\n%s\nfolders: %d", pr.Output, eng.folderCount())
	}

	notes := filepath.Join(a.home, "Notes")
	pr, err = client.pair(ctx, pairRequest{Code: hub.code, Hub: hub.addr, Vault: "Notes", Path: notes})
	if err != nil || !pr.OK || pr.Refusal != "" {
		t.Fatalf("with vault and path: %+v %v", pr, err)
	}
	if eng.folderCount() != 1 || eng.folders[0].Path != notes || eng.folders[0].Label != "Notes" {
		t.Fatalf("folders: %+v", eng.folders)
	}
	if !strings.Contains(pr.Output, "is set up to sync at") {
		t.Fatalf("output:\n%s", pr.Output)
	}

	// One pairing at a time — the terminal's lock is the socket's too.
	hold, err := lockFile(filepath.Join(a.lay.Base, "pair.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	if _, err := client.pair(ctx, pairRequest{Code: hub.code, Hub: hub.addr}); err == nil || !strings.Contains(err.Error(), "another vaultsync pair is running") {
		t.Fatalf("busy: %v", err)
	}
}

// fakeAgent is the agent's side as the terminal sees it: a socket that
// answers status, pause and resume with what the test put there.
type fakeAgent struct {
	path   string
	status controlStatus
	hubs   int
	calls  []string
	srv    *http.Server
}

func newFakeAgent(t *testing.T) *fakeAgent {
	t.Helper()
	f := &fakeAgent{path: filepath.Join(shortDir(t), "agent.sock")}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, f.status) })
	mux.HandleFunc("POST /v1/{verb}", func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, r.PathValue("verb"))
		writeJSON(w, 200, pauseResponse{Paused: r.PathValue("verb") == "pause", Hubs: f.hubs})
	})
	l, err := net.Listen("unix", f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.srv = &http.Server{Handler: mux}
	go func() { _ = f.srv.Serve(l) }()
	t.Cleanup(func() { _ = f.srv.Close() })
	return f
}

// terminalApp is an app with a prepared engine whose API port nobody
// answers on, and the service installed.
func terminalApp(t *testing.T, socket string) *app {
	t.Helper()
	run := &fakeRunner{}
	svc, _ := testService(t, "linux", run)
	port, err := freeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(svc.lay.Home, "config.xml"), "<configuration><gui><address>127.0.0.1:"+strconv.Itoa(port)+"</address><apikey>k</apikey></gui></configuration>\n")
	if err := saveState(svc.lay.State, agentState{GUIPort: port, ControlSocket: socket}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, mustUnitPath(t, svc), "installed\n")
	return &app{goos: "linux", home: svc.home, getenv: envOf(nil), lay: svc.lay, svc: svc, out: &bytes.Buffer{}}
}

func TestIssue176_StatusReadsTheAgentFirst(t *testing.T) {
	agent := newFakeAgent(t)
	agent.status = controlStatus{Version: "9.9.9", Engine: "running", statusReport: statusReport{Text: "\nHub\n  Test Hub               connected\n\nVaults\n  Notes          ~/Notes                      up to date\n"}}
	a := terminalApp(t, agent.path)
	if err := a.status(context.Background()); err != nil {
		t.Fatal(err)
	}
	out := a.out.(*bytes.Buffer).String()
	if !strings.Contains(out, "Sync engine: running\n\nHub\n  Test Hub               connected\n") || !strings.Contains(out, "up to date") {
		t.Fatalf("status from the agent:\n%s", out)
	}

	// The agent is up, the engine not yet.
	agent.status.Engine = "starting"
	a.out = &bytes.Buffer{}
	if err := a.status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if out := a.out.(*bytes.Buffer).String(); !strings.Contains(out, "Sync engine: starting — VaultSync is bringing it up") || strings.Contains(out, "up to date") {
		t.Fatalf("status while the engine starts:\n%s", out)
	}

	// No agent: the engine is asked directly — and does not answer here.
	_ = agent.srv.Close()
	a.out = &bytes.Buffer{}
	if err := a.status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if out := a.out.(*bytes.Buffer).String(); !strings.Contains(out, "Sync engine: not running — nothing syncs right now") {
		t.Fatalf("status without an agent:\n%s", out)
	}
}

func TestIssue176_PauseFromTheTerminal(t *testing.T) {
	agent := newFakeAgent(t)
	agent.hubs = 1
	a := terminalApp(t, agent.path)
	ctx := context.Background()
	if err := a.pauseSync(ctx, true); err != nil || !strings.Contains(a.out.(*bytes.Buffer).String(), "✓ Syncing is paused on this computer. Nothing syncs until you run vaultsync resume.") {
		t.Fatalf("pause: %v %s", err, a.out)
	}
	a.out = &bytes.Buffer{}
	if err := a.pauseSync(ctx, false); err != nil || !strings.Contains(a.out.(*bytes.Buffer).String(), "✓ Syncing resumed on this computer.") {
		t.Fatalf("resume: %v %s", err, a.out)
	}
	if strings.Join(agent.calls, ",") != "pause,resume" {
		t.Fatalf("agent saw %v", agent.calls)
	}

	_ = agent.srv.Close()
	if err := a.pauseSync(ctx, true); !strings.Contains(refusalText(err), "nothing to pause") || !strings.Contains(refusalText(err), "not running") {
		t.Fatalf("pause without an agent: %v", err)
	}
	if err := a.pauseSync(ctx, false); !strings.Contains(refusalText(err), "vaultsync start resumes the background service") {
		t.Fatalf("resume without an agent: %v", err)
	}

	// Not set up at all.
	b := &app{goos: "linux", home: t.TempDir(), getenv: envOf(nil), out: io.Discard}
	b.lay, _ = layoutFor("linux", b.home, envOf(nil))
	if err := b.pauseSync(ctx, true); !strings.Contains(refusalText(err), "not set up") {
		t.Fatalf("pause before setup: %v", err)
	}
}

func TestIssue176_SocketIsFreshOwnerOnlyAndGoneAfterwards(t *testing.T) {
	dir := shortDir(t)
	path := filepath.Join(dir, "agent.sock")

	// A socket an agent left behind is replaced.
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
	if _, err := os.Lstat(path); err != nil {
		t.Fatal("the leftover is the point of this test")
	}
	l2, err := listenUnix(path)
	if err != nil {
		t.Fatalf("a leftover socket must be replaced: %v", err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v", fi.Mode())
	}
	l2.Close()

	// Anything else at that name is left alone.
	writeFile(t, path, "not a socket\n")
	if _, err := listenUnix(path); err == nil || !strings.Contains(err.Error(), "not a socket") {
		t.Fatalf("a file in the way: %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "not a socket\n" {
		t.Fatal("the file was touched")
	}

	// A folder too deep for a socket address: a place of our own instead.
	deep := filepath.Join(dir, strings.Repeat("d", 120), "agent.sock")
	if err := os.MkdirAll(filepath.Dir(deep), 0o700); err != nil {
		t.Fatal(err)
	}
	l3, at, cleanup, err := listenControl(deep)
	if err != nil {
		t.Fatalf("no fallback: %v", err)
	}
	if at == deep || !strings.HasPrefix(at, os.TempDir()) || !strings.HasSuffix(at, "/agent.sock") {
		t.Fatalf("fallback at %s", at)
	}
	if fi, _ := os.Stat(filepath.Dir(at)); fi.Mode().Perm() != 0o700 {
		t.Fatalf("fallback folder mode %v", fi.Mode())
	}
	l3.Close()
	cleanup()
	if _, err := os.Stat(filepath.Dir(at)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the fallback folder stays behind")
	}
}

// run serves the socket for as long as it runs the engine, records where
// when that is not the usual place, and takes the socket down with it.
func TestIssue176_RunServesTheSocketWhileTheEngineRuns(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tmp, "home")
	lay, err := layoutFor("linux", home, envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	fakeEngineBinary(t, lay)
	sum, err := fileSHA256(lay.Syncthing)
	if err != nil {
		t.Fatal(err)
	}
	port, err := freeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	st := agentState{GUIPort: port}
	st.Syncthing.Version, st.Syncthing.BinarySHA256 = syncthingVersion, sum
	if err := saveState(lay.State, st); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(lay.Home, "config.xml"), "<configuration><gui><address>127.0.0.1:"+strconv.Itoa(port)+"</address><apikey>k</apikey></gui></configuration>\n")
	// The layout's socket lies too deep here (a test's temporary folder):
	// run falls back and records the place.
	a := &app{goos: "linux", home: home, getenv: envOf(nil), lay: lay, out: io.Discard,
		svc: service{goos: "linux", home: home, getenv: envOf(nil), lay: lay, run: &fakeRunner{}}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.run(ctx, nil) }()
	var socket string
	e2eWait(t, "the control socket", 15*time.Second, func() bool {
		st, err := loadState(lay.State)
		if err != nil {
			return false
		}
		socket = st.ControlSocket
		if socket == "" {
			socket = lay.Socket
		}
		_, err = os.Stat(socket)
		return err == nil
	})
	cs, err := newControlClient(socket).status(ctx)
	if err != nil {
		t.Fatalf("status over the socket at %s: %v", socket, err)
	}
	if cs.Engine != "starting" || cs.Version != version {
		t.Fatalf("status: %+v", cs)
	}
	// The terminal finds it through agent.json.
	a.out = &bytes.Buffer{}
	if err := a.status(ctx); err != nil || !strings.Contains(a.out.(*bytes.Buffer).String(), "Sync engine: starting") {
		t.Fatalf("terminal status: %v\n%s", err, a.out)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("run did not stop")
	}
	if _, err := os.Stat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the socket stays behind at %s", socket)
	}
}

// JSON shapes the menu-bar app will read: lists are never null.
func TestIssue176_StatusJSONHasNoNulls(t *testing.T) {
	eng := newFakeEngine(t)
	a := agentApp(t, eng)
	client := serveControl(t, a)
	var raw map[string]json.RawMessage
	if err := client.do(context.Background(), http.MethodGet, "/v1/status", nil, &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"hubs", "vaults", "waiting", "attempts"} {
		if string(raw[k]) == "null" {
			t.Fatalf("%s is null: %s", k, raw[k])
		}
	}
}
