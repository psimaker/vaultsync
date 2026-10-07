//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

// serveControl starts the agent's side on a.lay.Socket and returns a client,
// the server, and what the agent logged.
func serveControl(t *testing.T, a *app) *controlClient {
	t.Helper()
	c, _, _ := serveControlLogged(t, a)
	return c
}

// logLines collects what the agent logged, from any goroutine.
type logLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *logLines) add(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
}

func (l *logLines) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

func serveControlLogged(t *testing.T, a *app) (*controlClient, *controlServer, *logLines) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	logged := &logLines{}
	ctl := &controlServer{a: a, eng: a.engine(), logf: func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		logged.add(line)
		t.Log(line)
	}}
	path, stop, err := ctl.serve(ctx)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { stop(); cancel() })
	if path != a.lay.Socket {
		t.Fatalf("listened on %s, wanted %s", path, a.lay.Socket)
	}
	return newControlClient(path), ctl, logged
}

func hubDevice(eng *fakeEngine) {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	eng.devices = append(eng.devices, syncthing.DeviceConfig{DeviceID: testHubID, Name: "Test Hub"})
	eng.folders = append(eng.folders, syncthingFolder("vs-aaaaaaaaaaaa", "Notes", "/home/me/Notes"))
	eng.folders[0].Devices = []syncthing.FolderDevice{folderDeviceOf(testMyID), folderDeviceOf(testHubID)}
}

func pausedDevices(eng *fakeEngine) (paused, hubs int) {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	for _, d := range eng.devices {
		if d.DeviceID == testMyID {
			continue
		}
		hubs++
		if d.Paused {
			paused++
		}
	}
	return paused, hubs
}

func TestIssue176_StatusOverTheSocketIsWhatTheTerminalPrints(t *testing.T) {
	eng := newFakeEngine(t)
	hubDevice(eng)
	a := agentApp(t, eng)
	client, ctl, logged := serveControlLogged(t, a)

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
	// The server's own diagnostics go to a private file, not to the log.
	ctl.srv.ErrorLog.Printf("http: Accept error: accept unix %s: too many open files", a.lay.Socket)
	diag := filepath.Join(a.lay.Base, "control-socket.log")
	if data, err := os.ReadFile(diag); err != nil || !strings.Contains(string(data), "too many open files") {
		t.Fatalf("diagnostics: %q %v", data, err)
	}
	if fi, _ := os.Stat(diag); fi.Mode().Perm() != 0o600 {
		t.Fatalf("diagnostics mode %v", fi.Mode())
	}
	// Read after the write: nothing of it reaches the agent's log.
	for _, line := range logged.all() {
		if strings.Contains(line, a.lay.Socket) || strings.Contains(line, "too many open files") {
			t.Fatalf("the server's diagnostics reached the log: %q", line)
		}
	}
	// Anything else is not this API.
	if err := client.do(context.Background(), http.MethodGet, "/v2/status", nil, nil); !errors.Is(err, errNoAgent) {
		t.Fatalf("an unknown path reads as no agent, got %v", err)
	}
}

// An engine that refuses the agent's probe, or does not answer it in time,
// is not "stopped": the socket reports the failure, and the terminal
// shows it instead of reading past the agent.
func TestIssue176_StatusReportsAnEngineThatDoesNotAnswer(t *testing.T) {
	eng := newFakeEngine(t)
	a := agentApp(t, eng)
	writeFile(t, filepath.Join(a.lay.Home, "config.xml"), "<configuration><gui><address>"+eng.addr+"</address><apikey>another-key</apikey></gui></configuration>\n")
	client := serveControl(t, a)
	if _, err := client.status(context.Background()); err == nil || !strings.Contains(err.Error(), "the sync engine does not answer") || !strings.Contains(err.Error(), "403") {
		t.Fatalf("status with the engine refusing the probe: %v", err)
	}
	// The terminal, through agent.json's socket, shows the agent's error.
	st, err := loadState(a.lay.State)
	if err != nil {
		t.Fatal(err)
	}
	st.ControlSocket = a.lay.Socket
	if err := saveState(a.lay.State, st); err != nil {
		t.Fatal(err)
	}
	a.out = &bytes.Buffer{}
	if err := a.status(context.Background()); err == nil || !strings.Contains(err.Error(), "could not report") || !strings.Contains(err.Error(), "403") {
		t.Fatalf("terminal status with the agent's engine refusing: %v", err)
	}

	// An engine that accepts and never answers.
	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			if c, err := silent.Accept(); err == nil {
				defer c.Close()
			} else {
				return
			}
		}
	}()
	t.Cleanup(func() { silent.Close() })
	writeFile(t, filepath.Join(a.lay.Home, "config.xml"), "<configuration><gui><address>"+silent.Addr().String()+"</address><apikey>engine-key</apikey></gui></configuration>\n")
	st.GUIPort = silent.Addr().(*net.TCPAddr).Port
	if err := saveState(a.lay.State, st); err != nil {
		t.Fatal(err)
	}
	if _, err := client.status(context.Background()); err == nil || !strings.Contains(err.Error(), "the sync engine does not answer") {
		t.Fatalf("status with the engine not answering in time: %v", err)
	}
}

// Only a missing socket file or nobody listening on it means "no agent":
// a socket this account may not open is reported as that, and the
// terminal neither reads past it nor calls the agent absent.
func TestIssue176_ASocketThatCannotBeOpenedIsNotNoAgent(t *testing.T) {
	path := filepath.Join(shortDir(t), "agent.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() == 0 {
		t.Skip("root opens any socket")
	}
	err = newControlClient(path).do(context.Background(), http.MethodGet, "/v1/status", nil, nil)
	if err == nil || errors.Is(err, errNoAgent) || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("a socket that cannot be opened: %v", err)
	}
	a := terminalApp(t, path)
	if err := a.status(context.Background()); err == nil || !strings.Contains(err.Error(), "could not report") || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("terminal status: %v", err)
	}
	if err := a.pauseSync(context.Background(), true); err == nil || isRefusal(err) || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("pause: %v", err)
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
	eng.mu.Lock()
	eng.devices = append(eng.devices, syncthing.DeviceConfig{DeviceID: otherID, Name: "Second Hub"})
	eng.folders = append(eng.folders, syncthingFolder("vs-bbbbbbbbbbbb", "Work", "/home/me/Work"))
	eng.folders[1].Devices = []syncthing.FolderDevice{folderDeviceOf(testMyID), folderDeviceOf(otherID)}
	eng.folders = append(eng.folders, syncthingFolder("vs-cccccccccccc", "Mail", "/home/me/Mail"), syncthingFolder("vs-dddddddddddd", "Old", "/home/me/Old"))
	eng.folderState = map[string]map[string]any{
		"vs-bbbbbbbbbbbb": {"state": "error", "error": "permission denied"},
		"vs-cccccccccccc": {"state": "idle", "errors": 3},
		"vs-dddddddddddd": nil, // the engine does not answer for it
	}
	eng.mu.Unlock()
	res, err = client.setPaused(ctx, true)
	if err != nil || !res.Paused || res.Hubs != 2 {
		t.Fatalf("pause: %+v %v", res, err)
	}
	if p, h := pausedDevices(eng); p != 2 || h != 2 {
		t.Fatalf("every Hub is paused: %d of %d", p, h)
	}
	if len(eng.patches) != 2 || eng.patches[0]["paused"] != true || len(eng.patches[0]) != 1 || eng.patches[1]["paused"] != true || len(eng.patches[1]) != 1 {
		t.Fatalf("pause patches exactly the paused flag of each Hub: %v", eng.patches)
	}
	cs, err := client.status(ctx)
	if err != nil || !cs.Paused {
		t.Fatalf("status after pause: %+v %v", cs, err)
	}
	if cs.Hubs[0].State != "paused on this computer — vaultsync resume starts syncing again" || cs.Hubs[1].State != cs.Hubs[0].State {
		t.Fatalf("paused states: %+v", cs.Hubs)
	}
	// What a pause does not change stays visible: a folder's own error with
	// its remedy, files that could not sync, an engine that does not answer
	// for a folder.
	byLabel := map[string]string{}
	for _, v := range cs.Vaults {
		byLabel[v.Label] = v.State
	}
	if st := byLabel["Work"]; !strings.HasPrefix(st, "error: permission denied") || !strings.Contains(st, "allow access to this folder") {
		t.Fatalf("a folder error while paused: %q", st)
	}
	if st := byLabel["Mail"]; st != "3 files could not sync — see the engine's log" {
		t.Fatalf("files that could not sync, while paused: %q", st)
	}
	if st := byLabel["Old"]; st != "unknown" {
		t.Fatalf("a folder the engine does not answer for, while paused: %q", st)
	}
	if st := byLabel["Notes"]; st != "paused on this computer" {
		t.Fatalf("a healthy folder while paused: %q", st)
	}
	// Pausing again changes nothing; a pairing is refused while paused —
	// over the socket and in the terminal alike.
	if _, err := client.setPaused(ctx, true); err != nil || len(eng.patches) != 2 {
		t.Fatalf("second pause: %v %v", err, eng.patches)
	}
	if pr, err := client.pair(ctx, pairRequest{Code: "x"}); err != nil || pr.OK || !strings.Contains(pr.Refusal, "paused on this computer — run vaultsync resume first") {
		t.Fatalf("pair while paused: %+v %v", pr, err)
	}
	if _, err := a.pairWith(ctx, &term{out: io.Discard}, pairOptions{code: "x"}, eng.client, false); !strings.Contains(refusalText(err), "paused on this computer — run vaultsync resume first") {
		t.Fatalf("terminal pair while paused: %v", err)
	}
	// A pairing in flight holds the pairing lock; a pause waits for it.
	hold, err := lockFile(filepath.Join(a.lay.Base, "pair.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.setPaused(ctx, false); err == nil || !strings.Contains(err.Error(), "a pairing is running") {
		t.Fatalf("resume during a pairing: %v", err)
	}
	hold()

	res, err = client.setPaused(ctx, false)
	if err != nil || res.Paused || res.Hubs != 2 || len(eng.patches) != 4 || eng.patches[2]["paused"] != false || eng.patches[3]["paused"] != false {
		t.Fatalf("resume: %+v %v %v", res, err, eng.patches)
	}
	if p, _ := pausedDevices(eng); p != 0 {
		t.Fatalf("%d Hubs still paused", p)
	}
	cs, err = client.status(ctx)
	if err != nil || cs.Paused || cs.Hubs[0].State != "not connected" {
		t.Fatalf("status after resume: %+v %v", cs, err)
	}
	for _, v := range cs.Vaults {
		if v.Label == "Notes" && v.State != "waiting for your Hub" {
			t.Fatalf("a healthy folder after resume: %q", v.State)
		}
	}

	// No agent on the socket while the engine runs on (an older agent, a
	// socket that could not be made): pause says so instead of "nothing
	// syncs".
	b := agentApp(t, eng)
	if err := b.pauseSync(ctx, true); !strings.Contains(refusalText(err), "could not be paused and continues") || !strings.Contains(refusalText(err), "vaultsync stop") {
		t.Fatalf("pause without an agent but with a running engine: %v", err)
	}
	if err := b.pauseSync(ctx, false); !strings.Contains(refusalText(err), "cannot tell whether syncing is paused") {
		t.Fatalf("resume without an agent but with a running engine: %v", err)
	}
	// An engine that refuses the probe (another API key), one that does not
	// answer in time, and a config that cannot be read are no evidence that
	// nothing syncs: the refusal says it could not confirm.
	writeFile(t, filepath.Join(b.lay.Home, "config.xml"), "<configuration><gui><address>"+eng.addr+"</address><apikey>another-key</apikey></gui></configuration>\n")
	if err := b.pauseSync(ctx, true); !strings.Contains(refusalText(err), "could not confirm") || !strings.Contains(refusalText(err), "syncing may continue") {
		t.Fatalf("pause with the engine refusing the probe: %v", err)
	}
	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			if c, err := silent.Accept(); err == nil {
				defer c.Close()
			} else {
				return
			}
		}
	}()
	t.Cleanup(func() { silent.Close() })
	if err := saveState(b.lay.State, agentState{GUIPort: silent.Addr().(*net.TCPAddr).Port}); err != nil {
		t.Fatal(err)
	}
	if err := b.pauseSync(ctx, true); !strings.Contains(refusalText(err), "could not confirm") {
		t.Fatalf("pause with the engine not answering in time: %v", err)
	}
	if err := os.Remove(filepath.Join(b.lay.Home, "config.xml")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(b.lay.Home, "config.xml"), "<configuration><gui><address>"+eng.addr+"</address></gui></configuration>\n")
	if err := b.pauseSync(ctx, false); !strings.Contains(refusalText(err), "could not confirm") || !strings.Contains(refusalText(err), "no API key") {
		t.Fatalf("resume with an unreadable engine config: %v", err)
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
	// An engine that stops while the menu is built is reported — not hidden
	// behind the refusal that names the flags.
	eng.mu.Lock()
	eng.failFolders = true
	eng.mu.Unlock()
	if _, err := client.pair(ctx, pairRequest{Code: hub.code, Hub: hub.addr}); err == nil || !strings.Contains(err.Error(), "the sync engine does not answer") {
		t.Fatalf("the engine failing during the menu: %v", err)
	}

	// The socket's own folder, when it lives outside VaultSync's, is
	// VaultSync's too: no vault may be paired into it.
	st, err := loadState(a.lay.State)
	if err != nil {
		t.Fatal(err)
	}
	fallback := shortDir(t)
	st.ControlSocket = filepath.Join(fallback, "agent.sock")
	if err := saveState(a.lay.State, st); err != nil {
		t.Fatal(err)
	}
	pr, err = client.pair(ctx, pairRequest{Code: hub.code, Hub: hub.addr, Vault: "Notes", Path: filepath.Join(fallback, "Notes")})
	if err != nil || pr.OK || !strings.Contains(pr.Refusal, "VaultSync keeps its own files in") || eng.folderCount() != 0 {
		t.Fatalf("a vault inside the socket's folder: %+v %v (folders %d)", pr, err, eng.folderCount())
	}
	st.ControlSocket = ""
	if err := saveState(a.lay.State, st); err != nil {
		t.Fatal(err)
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

	// A folder with files becomes a new vault only with the consent the
	// terminal would ask for: yes, passed by the caller — never assumed.
	work := filepath.Join(a.home, "Work")
	mkVault(t, work)
	pr, err = client.pair(ctx, pairRequest{Code: hub.code, Hub: hub.addr, Vault: "Work", Create: true, Path: work})
	if err != nil || pr.OK || !strings.Contains(pr.Refusal, "already contains files") || !strings.Contains(pr.Refusal, "--yes") {
		t.Fatalf("a folder with files without consent: %+v %v", pr, err)
	}
	if eng.folderCount() != 1 {
		t.Fatalf("a refusal added a folder: %+v", eng.folders)
	}
	assertUntouched(t, work)
	pr, err = client.pair(ctx, pairRequest{Code: hub.code, Hub: hub.addr, Vault: "Work", Create: true, Path: work, Yes: true})
	if err != nil || !pr.OK {
		t.Fatalf("with consent: %+v %v", pr, err)
	}
	if eng.folderCount() != 2 || eng.folders[1].Path != work || eng.folders[1].Label != "Work" {
		t.Fatalf("folders: %+v", eng.folders)
	}

	// The pairing knows whose process it runs in: the socket hands the
	// service's ownership on, so a folder macOS refuses names "vaultsync".
	if s, _ := a.pairWith(ctx, &term{out: io.Discard}, pairOptions{}, eng.client, true); s == nil || !s.env.background {
		t.Fatal("pairWith drops the background flag")
	}
	if s, _ := a.pairWith(ctx, &term{out: io.Discard}, pairOptions{}, eng.client, false); s == nil || s.env.background {
		t.Fatal("a terminal pairing is not a background one")
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
	// broken: the agent answers status with an error.
	broken bool
}

func newFakeAgent(t *testing.T) *fakeAgent {
	t.Helper()
	f := &fakeAgent{path: filepath.Join(shortDir(t), "agent.sock")}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		if f.broken {
			fail(w, http.StatusInternalServerError, errors.New("the sync engine does not answer: boom"))
			return
		}
		writeJSON(w, 200, f.status)
	})
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

	// The agent is up, the engine not yet: its offline report is printed.
	agent.status.Engine = "starting"
	agent.status.Text = "\nVaults (not syncing while the engine is stopped)\n  Notes          ~/Notes\n"
	a.out = &bytes.Buffer{}
	if err := a.status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if out := a.out.(*bytes.Buffer).String(); !strings.Contains(out, "Sync engine: starting — VaultSync is bringing it up\n\nVaults (not syncing while the engine is stopped)\n  Notes") || strings.Contains(out, "up to date") {
		t.Fatalf("status while the engine starts:\n%s", out)
	}

	// An agent that answers and fails is the news — not a look past it.
	agent.broken = true
	a.out = &bytes.Buffer{}
	if err := a.status(context.Background()); err == nil || !strings.Contains(err.Error(), "could not report") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("status with a failing agent: %v", err)
	}
	if out := a.out.(*bytes.Buffer).String(); strings.Contains(out, "Sync engine") {
		t.Fatalf("a failing agent must not be read past:\n%s", out)
	}
	agent.broken = false

	// A connection that is made and dropped is not "no agent" either.
	dropping := filepath.Join(shortDir(t), "agent.sock")
	l, err := net.Listen("unix", dropping)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	t.Cleanup(func() { l.Close() })
	if err := newControlClient(dropping).do(context.Background(), http.MethodGet, "/v1/status", nil, nil); err == nil || errors.Is(err, errNoAgent) {
		t.Fatalf("a dropped connection: %v", err)
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
		t.Fatal("the empty fallback folder stays behind")
	}

	// Whatever someone put next to the socket is not VaultSync's to remove:
	// the cleanup takes the socket and leaves a folder that is not empty.
	l4, at, cleanup, err := listenControl(deep)
	if err != nil {
		t.Fatal(err)
	}
	note := filepath.Join(filepath.Dir(at), "Notes", "note.md")
	writeFile(t, note, "a vault someone keeps here\n")
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(at)) })
	l4.Close()
	cleanup()
	if _, err := os.Lstat(at); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the socket stays behind")
	}
	if data, err := os.ReadFile(note); err != nil || string(data) != "a vault someone keeps here\n" {
		t.Fatalf("the cleanup touched what is not its own: %q %v", data, err)
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
	writeFile(t, filepath.Join(lay.Home, "config.xml"), "<configuration><folder id=\"vs-aaaaaaaaaaaa\" label=\"Notes\" path=\""+filepath.Join(home, "Notes")+"\"></folder><gui><address>127.0.0.1:"+strconv.Itoa(port)+"</address><apikey>k</apikey></gui></configuration>\n")
	// The layout's socket lies too deep here (a test's temporary folder):
	// run falls back and records the place.
	a := &app{goos: "linux", home: home, getenv: envOf(nil), lay: lay, out: io.Discard,
		svc: service{goos: "linux", home: home, getenv: envOf(nil), lay: lay, run: &fakeRunner{}}}
	// At the moment run records the socket's place, it still holds the
	// setup lock: a second run cannot load agent.json in between and save
	// an older copy over the place (a lost update).
	held := make(chan error, 1)
	beforeSocketPublish = func() {
		unlock, err := lockFile(filepath.Join(lay.Base, "setup.lock"))
		if err == nil {
			unlock()
		}
		held <- err
	}
	t.Cleanup(func() { beforeSocketPublish = nil })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.run(ctx, nil) }()
	select {
	case err := <-held:
		if !errors.Is(err, ErrEngineRunning) {
			t.Fatalf("the setup lock must be held while the socket is published, got %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not reach the socket's publication")
	}
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
	// Until the engine answers, the report is the config file's: the same
	// vaults the terminal lists while the engine is stopped.
	if len(cs.Vaults) != 1 || cs.Vaults[0].Label != "Notes" || cs.Vaults[0].State != "not syncing — the engine is stopped" || !strings.Contains(cs.Text, "\nVaults (not syncing while the engine is stopped)\n  Notes          ~/Notes\n") {
		t.Fatalf("starting report: %+v\n%s", cs.Vaults, cs.Text)
	}
	// The terminal finds it through agent.json.
	a.out = &bytes.Buffer{}
	if err := a.status(ctx); err != nil || !strings.Contains(a.out.(*bytes.Buffer).String(), "Sync engine: starting — VaultSync is bringing it up\n\nVaults (not syncing while the engine is stopped)\n  Notes") {
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
		v, ok := raw[k]
		if !ok || !strings.HasPrefix(strings.TrimSpace(string(v)), "[") {
			t.Fatalf("%s is not a list: %s", k, v)
		}
	}
	for _, k := range []string{"version", "syncthing", "engine", "paused", "text"} {
		if _, ok := raw[k]; !ok {
			t.Fatalf("%s is missing", k)
		}
	}
}

// A request is one JSON document of bounded size — nothing after it, and
// nothing bigger, reaches a handler.
func TestIssue176_RequestsAreOneBoundedDocument(t *testing.T) {
	var req pairRequest
	if err := readJSON(strings.NewReader(`{"code":"TULIP-ANCHOR-42"}`), &req); err != nil || req.Code != "TULIP-ANCHOR-42" {
		t.Fatalf("one document: %v %+v", err, req)
	}
	if err := readJSON(strings.NewReader(`{"code":"a"} {"yes":true}`), &req); err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Fatalf("two documents: %v", err)
	}
	if err := readJSON(strings.NewReader(`{"code":"a"}`+strings.Repeat(" ", controlRequestLimit)), &req); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("oversized: %v", err)
	}
	if err := readJSON(strings.NewReader(`{"code":`), &req); err == nil {
		t.Fatal("a broken document")
	}
}

// Without a socket the service's log says so without the error's words —
// they name paths — and keeps them in VaultSync's folder.
func TestIssue176_SocketErrorStaysOutOfTheLog(t *testing.T) {
	base := t.TempDir()
	var logged []string
	err := &os.PathError{Op: "listen", Path: filepath.Join(base, "agent.sock"), Err: errors.New("invalid argument")}
	logSocketError(func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }, base, err)
	if len(logged) != 1 || strings.Contains(logged[0], base) || !strings.Contains(logged[0], "no control socket") || !strings.Contains(logged[0], "vaultsync status reads the engine directly") {
		t.Fatalf("log: %v", logged)
	}
	data, serr := os.ReadFile(filepath.Join(base, "control-socket-error.txt"))
	if serr != nil || !strings.Contains(string(data), err.Error()) {
		t.Fatalf("details: %q %v", data, serr)
	}
	if fi, _ := os.Stat(filepath.Join(base, "control-socket-error.txt")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("details mode %v", fi.Mode())
	}
}
