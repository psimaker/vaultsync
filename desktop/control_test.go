//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	// A recorded shell with none of the guard variables set.
	if err := saveState(lay.State, agentState{GUIPort: n, Env: map[string]string{}}); err != nil {
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

	// No Hub yet: nothing to pause or resume, and the terminal says which.
	res, err := client.setPaused(ctx, true)
	if err != nil || res.Hubs != 0 {
		t.Fatalf("pause without a Hub: %+v %v", res, err)
	}
	if err := a.pauseSync(ctx, true); !strings.Contains(refusalText(err), "No Hub is paired") || !strings.Contains(refusalText(err), "nothing to pause") {
		t.Fatalf("terminal pause without a Hub: %v", err)
	}
	if err := a.pauseSync(ctx, false); !strings.Contains(refusalText(err), "nothing to resume") {
		t.Fatalf("terminal resume without a Hub: %v", err)
	}

	hubDevice(eng)
	eng.mu.Lock()
	eng.devices = append(eng.devices, syncthing.DeviceConfig{DeviceID: otherID, Name: "Second Hub"})
	eng.folders = append(eng.folders, syncthingFolder("vs-bbbbbbbbbbbb", "Work", "/home/me/Work"))
	eng.folders[1].Devices = []syncthing.FolderDevice{folderDeviceOf(testMyID), folderDeviceOf(otherID)}
	eng.folders = append(eng.folders, syncthingFolder("vs-cccccccccccc", "Mail", "/home/me/Mail"), syncthingFolder("vs-dddddddddddd", "Old", "/home/me/Old"), syncthingFolder("vs-eeeeeeeeeeee", "Busy", "/home/me/Busy"), syncthingFolder("vs-ffffffffffff", "Queue", "/home/me/Queue"))
	eng.folderState = map[string]map[string]any{
		"vs-bbbbbbbbbbbb": {"state": "error", "error": "permission denied"},
		"vs-cccccccccccc": {"state": "idle", "errors": 3},
		"vs-dddddddddddd": nil, // the engine does not answer for it
		"vs-eeeeeeeeeeee": {"state": "syncing", "globalBytes": 100, "inSyncBytes": 42},
		"vs-ffffffffffff": {"state": "idle", "needTotalItems": 3},
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
	// Work the engine is still doing on what it had taken in stays visible:
	// a pause stops the connections, not the folder workers.
	if st := byLabel["Busy"]; st != "syncing — 42 %" {
		t.Fatalf("a folder still syncing, while paused: %q", st)
	}
	if st := byLabel["Queue"]; st != "3 items left to sync" {
		t.Fatalf("items still to pull, while paused: %q", st)
	}
	if st := byLabel["Notes"]; st != "paused on this computer" {
		t.Fatalf("a healthy folder while paused: %q", st)
	}
	// Pausing again sends the PATCH again — the engine applies a change
	// before saving it, so a device that reads as paused may not have it
	// persisted — and changes nothing else. A pairing is refused while
	// paused, over the socket and in the terminal alike.
	if _, err := client.setPaused(ctx, true); err != nil || len(eng.patches) != 4 || eng.patches[2]["paused"] != true || eng.patches[3]["paused"] != true {
		t.Fatalf("second pause: %v %v", err, eng.patches)
	}
	// A PATCH the engine applied but could not save is a failure, and the
	// retry sends it again until it is saved.
	eng.mu.Lock()
	eng.failPatchSave = true
	eng.mu.Unlock()
	if _, err := client.setPaused(ctx, true); err == nil || !strings.Contains(err.Error(), "did not take the change") {
		t.Fatalf("a pause the engine could not save: %v", err)
	}
	if p, _ := pausedDevices(eng); p != 2 {
		t.Fatalf("the engine applied the change in memory: %d paused", p)
	}
	if _, err := client.setPaused(ctx, true); err != nil || len(eng.patches) != 7 {
		t.Fatalf("the retry sends the PATCH again: %v (%d patches)", err, len(eng.patches))
	}
	if pr, err := client.pair(ctx, pairRequest{Code: "x"}); err != nil || pr.OK || !strings.Contains(pr.Refusal, "paused on this computer — run vaultsync resume first") {
		t.Fatalf("pair while paused: %+v %v", pr, err)
	}
	if _, err := a.pairWith(ctx, &term{out: io.Discard}, pairOptions{code: "x"}, eng.client, pairOrigin{}); !strings.Contains(refusalText(err), "paused on this computer — run vaultsync resume first") {
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
	if err != nil || res.Paused || res.Hubs != 2 || len(eng.patches) != 9 || eng.patches[7]["paused"] != false || eng.patches[8]["paused"] != false {
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
	if err := a.pauseSync(ctx, true); err != nil || !strings.Contains(a.out.(*bytes.Buffer).String(), "✓ Syncing is paused on this computer: the connections to your Hub are off") || strings.Contains(a.out.(*bytes.Buffer).String(), "Nothing syncs") {
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
	if s, _ := a.pairWith(ctx, &term{out: io.Discard}, pairOptions{}, eng.client, pairOrigin{background: true}); s == nil || !s.env.background {
		t.Fatal("pairWith drops the background flag")
	}
	if s, _ := a.pairWith(ctx, &term{out: io.Discard}, pairOptions{}, eng.client, pairOrigin{}); s == nil || s.env.background {
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
	if err := a.pauseSync(ctx, true); err != nil || !strings.Contains(a.out.(*bytes.Buffer).String(), "✓ Syncing is paused on this computer: the connections to your Hub are off until you run vaultsync resume. Changes the engine had already received may still be applied for a moment") {
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
	// Set up, but the engine's folder cannot be looked into: that is not
	// "not set up" — the agent may be syncing — and the error is said.
	if os.Getuid() == 0 {
		t.Skip("root looks into any folder")
	}
	c := &app{goos: "linux", home: t.TempDir(), getenv: envOf(nil), out: io.Discard}
	c.lay, _ = layoutFor("linux", c.home, envOf(nil))
	c.lay.Socket = filepath.Join(shortDir(t), "agent.sock")
	writeFile(t, filepath.Join(c.lay.Home, "config.xml"), "<configuration><gui><address>127.0.0.1:1</address><apikey>k</apikey></gui></configuration>\n")
	if err := saveState(c.lay.State, agentState{GUIPort: 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(c.lay.Home, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(c.lay.Home, 0o700) })
	if !c.engine().prepared() {
		t.Fatal("a folder that cannot be looked into is not \"not prepared\"")
	}
	if err := c.pauseSync(ctx, true); !strings.Contains(refusalText(err), "could not confirm") || !strings.Contains(refusalText(err), "permission denied") || strings.Contains(refusalText(err), "not set up") {
		t.Fatalf("pause with the engine's folder inaccessible: %v", err)
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

	// A folder too deep for a socket address: a place of our own instead —
	// inside the folder named as the user's own, and nowhere without one.
	deep := filepath.Join(dir, strings.Repeat("d", 120), "agent.sock")
	if err := os.MkdirAll(filepath.Dir(deep), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := listenControl(deep, ""); err == nil || !strings.Contains(err.Error(), "no folder of the user's own") {
		t.Fatalf("too deep and no fallback folder must fail and say so: %v", err)
	}
	// A fallback that fails too is reported next to the first error.
	if os.Getuid() != 0 {
		sealed := shortDir(t)
		if err := os.Chmod(sealed, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(sealed, 0o700) })
		if _, _, _, err := listenControl(deep, sealed); err == nil || !strings.Contains(err.Error(), "fallback:") || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("both errors: %v", err)
		}
	}
	own := shortDir(t)
	l3, at, cleanup, err := listenControl(deep, own)
	if err != nil {
		t.Fatalf("no fallback: %v", err)
	}
	if at == deep || !strings.HasPrefix(at, own+string(filepath.Separator)) || !strings.HasSuffix(at, "/agent.sock") {
		t.Fatalf("fallback at %s", at)
	}
	if fi, _ := os.Stat(filepath.Dir(at)); fi.Mode().Perm() != 0o700 {
		t.Fatalf("fallback folder mode %v", fi.Mode())
	}
	l3.Close()
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(at)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the empty fallback folder stays behind")
	}

	// Whatever someone put next to the socket is not VaultSync's to remove:
	// the cleanup takes the socket and leaves a folder that is not empty.
	l4, at, cleanup, err := listenControl(deep, own)
	if err != nil {
		t.Fatal(err)
	}
	note := filepath.Join(filepath.Dir(at), "Notes", "note.md")
	writeFile(t, note, "a vault someone keeps here\n")
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(at)) })
	l4.Close()
	if err := cleanup(); err != nil {
		t.Fatalf("a folder kept on purpose is no failure: %v", err)
	}
	if _, err := os.Lstat(at); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the socket stays behind")
	}
	if data, err := os.ReadFile(note); err != nil || string(data) != "a vault someone keeps here\n" {
		t.Fatalf("the cleanup touched what is not its own: %q %v", data, err)
	}
}

// runFixture is an app ready for run: a fake engine binary with its
// checksum recorded, a prepared config with one vault, the layout's socket
// too deep for a socket address (made so here, whatever the temporary
// folder's length on this system) and a runtime directory of the user's
// own to fall back into.
func runFixture(t *testing.T) (*app, string) {
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
	lay.Socket = filepath.Join(lay.Base, strings.Repeat("d", 120), "agent.sock")
	runtimeDir := shortDir(t)
	env := envOf(map[string]string{"XDG_RUNTIME_DIR": runtimeDir})
	return &app{goos: "linux", home: home, getenv: env, lay: lay, out: io.Discard,
		svc: service{goos: "linux", home: home, getenv: env, lay: lay, run: &fakeRunner{}}}, runtimeDir
}

// run serves the socket for as long as it runs the engine, records where
// when that is not the usual place, and takes the socket down with it.
func TestIssue176_RunServesTheSocketWhileTheEngineRuns(t *testing.T) {
	a, runtimeDir := runFixture(t)
	lay := a.lay
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
	if !strings.HasPrefix(socket, runtimeDir+string(filepath.Separator)) {
		t.Fatalf("the fallback lies outside the user's runtime directory: %s", socket)
	}
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
	// And the place is out of agent.json again: nobody serves it now — and
	// the terminal, dialing the layout's overlong address, reads the engine
	// directly instead of failing.
	if st, err := loadState(lay.State); err != nil || st.ControlSocket != "" {
		t.Fatalf("the place stays recorded: %q %v", st.ControlSocket, err)
	}
	a.out = &bytes.Buffer{}
	if err := a.status(context.Background()); err != nil || !strings.Contains(a.out.(*bytes.Buffer).String(), "Sync engine: not running — nothing syncs right now") {
		t.Fatalf("status after the agent is gone: %v\n%s", err, a.out)
	}
}

// A socket that cannot be taken down on the way out is reported, with
// whatever ended the run — a place that stays recorded as served must not
// pass in silence.
func TestIssue176_RunReportsASocketLeftBehind(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root removes anything")
	}
	a, _ := runFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.run(ctx, nil) }()
	var socket string
	e2eWait(t, "the control socket", 15*time.Second, func() bool {
		st, err := loadState(a.lay.State)
		if err != nil || st.ControlSocket == "" {
			return false
		}
		socket = st.ControlSocket
		_, err = os.Stat(socket)
		return err == nil
	})
	// The socket's folder can no longer lose an entry: the removal fails.
	if err := os.Chmod(filepath.Dir(socket), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(socket), 0o700) })
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "the control socket could not be removed") {
			t.Fatalf("run must report the socket left behind, got %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("run did not stop")
	}
}

// A write of agent.json that fails on the way out — the place would stay
// recorded — is reported, with whatever ended the run.
func TestIssue176_RunReportsAFailedCleanup(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root writes anywhere")
	}
	a, _ := runFixture(t)
	// run logs to the process's stderr — the service log: read it.
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = stderrW
	t.Cleanup(func() { os.Stderr = oldStderr })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.run(ctx, nil) }()
	e2eWait(t, "the control socket", 15*time.Second, func() bool {
		st, err := loadState(a.lay.State)
		if err != nil || st.ControlSocket == "" {
			return false
		}
		_, err = os.Stat(st.ControlSocket)
		return err == nil
	})
	// VaultSync's folder can no longer take a new file: the write fails.
	if err := os.Chmod(a.lay.Base, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(a.lay.Base, 0o700) })
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "could not be taken out of agent.json") {
			t.Fatalf("run must report the failed cleanup, got %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("run did not stop")
	}
	_ = stderrW.Close()
	os.Stderr = oldStderr
	logged, _ := io.ReadAll(stderrR)
	if !strings.Contains(string(logged), "its place could not be taken out of agent.json — a file operation failed") {
		t.Fatalf("the service log lacks the failed cleanup in its own words:\n%s", logged)
	}
	if strings.Contains(string(logged), a.lay.Base) {
		t.Fatalf("a path in the service log:\n%s", logged)
	}
}

// A pairing run by the service has the service's environment, not the
// shell's: the guards look where the terminal looks as well — the user's
// own Syncthing and Obsidian's registry under the shell's XDG folders, as
// setup and pair recorded them — so the socket refuses what the terminal
// refuses, and offers what the terminal offers.
func TestIssue176_SocketPairingLooksWhereTheShellLooks(t *testing.T) {
	eng := newFakeEngine(t)
	hub := newFakeHub(t, eng)
	a := agentApp(t, eng)
	client := serveControl(t, a)
	ctx := context.Background()

	// The shell's XDG_STATE_HOME holds the user's own Syncthing, which
	// syncs ~/Notes; its XDG_CONFIG_HOME holds Obsidian's registry naming
	// a vault outside the home folder, where only the registry leads. The
	// service's environment (the app's getenv) has neither variable.
	shellState, shellConfig := filepath.Join(t.TempDir(), "state"), filepath.Join(t.TempDir(), "config")
	notes := filepath.Join(a.home, "Notes")
	work := filepath.Join(t.TempDir(), "elsewhere", "Work")
	mkVault(t, notes)
	mkVault(t, work)
	writeFile(t, filepath.Join(shellState, "syncthing", "config.xml"), "<configuration><folder id=\"x\" label=\"Notes\" path=\""+notes+"\"></folder></configuration>\n")
	writeFile(t, filepath.Join(shellConfig, "obsidian", "obsidian.json"), `{"vaults":{"abc":{"path":"`+work+`","ts":1}}}`)
	st, err := loadState(a.lay.State)
	if err != nil {
		t.Fatal(err)
	}
	st.Env = map[string]string{"XDG_STATE_HOME": shellState, "XDG_CONFIG_HOME": shellConfig}
	if err := saveState(a.lay.State, st); err != nil {
		t.Fatal(err)
	}

	pr, err := client.pair(ctx, pairRequest{Code: hub.code, Hub: hub.addr, Vault: "Notes", Create: true, Path: notes, Yes: true})
	if err != nil || pr.OK || !strings.Contains(pr.Refusal, "already synced by the Syncthing on this computer") || eng.folderCount() != 0 {
		t.Fatalf("a folder the shell's Syncthing syncs: %+v %v (folders %d)", pr, err, eng.folderCount())
	}
	assertUntouched(t, notes)
	localPaths := func(m *menuJSON) []string {
		var out []string
		if m != nil {
			for _, e := range m.Local {
				out = append(out, e.Path)
			}
		}
		return out
	}
	pr, err = client.pair(ctx, pairRequest{Code: hub.code, Hub: hub.addr})
	if err != nil || !contains(localPaths(pr.Menu), work) {
		t.Fatalf("the shell's Obsidian registry in the menu: %v %v", localPaths(pr.Menu), err)
	}
	if contains(localPaths(pr.Menu), notes) {
		t.Fatalf("a folder the shell's Syncthing syncs is not on offer: %v", localPaths(pr.Menu))
	}

	// Without any record — an agent.json from before — the service's own
	// environment is all there is, which may not be where the user's own
	// Syncthing is: the socket refuses to pair until a terminal recorded
	// the shell, and offers nothing.
	st.Env = nil
	if err := saveState(a.lay.State, st); err != nil {
		t.Fatal(err)
	}
	pr, err = client.pair(ctx, pairRequest{Code: hub.code, Hub: hub.addr, Vault: "Notes", Create: true, Path: notes})
	if err != nil || pr.OK || !strings.Contains(pr.Refusal, "not recorded where this computer keeps its settings") || !strings.Contains(pr.Refusal, "vaultsync pair in a terminal") || pr.Menu != nil || eng.folderCount() != 0 {
		t.Fatalf("without the record: %+v %v", pr, err)
	}

	// The other way round: the shell had none of the variables set — its
	// defaults under the home folder apply — while the service has its own
	// XDG folders elsewhere. The shell's default places still count.
	svcState, svcConfig := filepath.Join(t.TempDir(), "svc-state"), filepath.Join(t.TempDir(), "svc-config")
	a.getenv = envOf(map[string]string{"XDG_STATE_HOME": svcState, "XDG_CONFIG_HOME": svcConfig})
	work2 := filepath.Join(t.TempDir(), "elsewhere2", "Work2")
	mkVault(t, work2)
	writeFile(t, filepath.Join(a.home, ".config", "obsidian", "obsidian.json"), `{"vaults":{"def":{"path":"`+work2+`","ts":1}}}`)
	writeFile(t, filepath.Join(a.home, ".local", "state", "syncthing", "config.xml"), "<configuration><folder id=\"y\" label=\"Notes\" path=\""+notes+"\"></folder></configuration>\n")
	st.Env = map[string]string{}
	if err := saveState(a.lay.State, st); err != nil {
		t.Fatal(err)
	}
	pr, err = client.pair(ctx, pairRequest{Code: hub.code, Hub: hub.addr})
	if err != nil || !contains(localPaths(pr.Menu), work2) || contains(localPaths(pr.Menu), notes) {
		t.Fatalf("the shell's defaults with an empty record: %v %v", localPaths(pr.Menu), err)
	}
	pr, err = client.pair(ctx, pairRequest{Code: hub.code, Hub: hub.addr, Vault: "Notes", Create: true, Path: notes})
	if err != nil || pr.OK || !strings.Contains(pr.Refusal, "already synced by the Syncthing on this computer") || eng.folderCount() != 0 {
		t.Fatalf("a folder the shell's default Syncthing syncs: %+v %v", pr, err)
	}

	// Every config that exists counts, not only the first found: here the
	// shell's names nothing of interest, the service's syncs ~/Notes2.
	notes2 := filepath.Join(a.home, "Notes2")
	mkVault(t, notes2)
	writeFile(t, filepath.Join(shellState, "syncthing", "config.xml"), "<configuration><folder id=\"z\" label=\"Other\" path=\""+filepath.Join(a.home, "Other")+"\"></folder></configuration>\n")
	writeFile(t, filepath.Join(svcState, "syncthing", "config.xml"), "<configuration><folder id=\"w\" label=\"Notes2\" path=\""+notes2+"\"></folder></configuration>\n")
	st.Env = map[string]string{"XDG_STATE_HOME": shellState}
	if err := saveState(a.lay.State, st); err != nil {
		t.Fatal(err)
	}
	pr, err = client.pair(ctx, pairRequest{Code: hub.code, Hub: hub.addr, Vault: "Notes2", Create: true, Path: notes2})
	if err != nil || pr.OK || !strings.Contains(pr.Refusal, "already synced by the Syncthing on this computer") || eng.folderCount() != 0 {
		t.Fatalf("a folder the second config syncs: %+v %v", pr, err)
	}

	// The shell's runtime directory, where GNOME mounts online accounts: a
	// vault below it is a cloud folder for the socket's pairing too.
	shellRuntime := filepath.Join(t.TempDir(), "rt")
	cloudVault := filepath.Join(shellRuntime, "gvfs", "google-drive:host=example.com", "Vault")
	mkVault(t, cloudVault)
	st.Env = map[string]string{"XDG_RUNTIME_DIR": shellRuntime}
	if err := saveState(a.lay.State, st); err != nil {
		t.Fatal(err)
	}
	a.getenv = envOf(nil)
	pr, err = client.pair(ctx, pairRequest{Code: hub.code, Hub: hub.addr, Vault: "Vault", Create: true, Path: cloudVault})
	if err != nil || pr.OK || !strings.Contains(pr.Refusal, "cannot sync with VaultSync there") || eng.folderCount() != 0 {
		t.Fatalf("a vault under the shell's runtime directory: %+v %v", pr, err)
	}
	if ce := cloudEnvFor("linux", a.home, envOf(map[string]string{"XDG_RUNTIME_DIR": "/rt"})); ce.runtimeDir != "/rt" {
		t.Fatalf("runtime directory from the environment: %q", ce.runtimeDir)
	}
	if ce := cloudEnvFor("linux", a.home, envOf(nil)); !strings.HasPrefix(ce.runtimeDir, "/run/user/") {
		t.Fatalf("runtime directory default: %q", ce.runtimeDir)
	}
}

// agent.json has several writers — setup, run, the supervisor moving the
// engine's port, pair recording the shell: each update is one locked step
// that keeps the others' fields.
func TestIssue176_StateUpdatesNeverLoseEachOthersFields(t *testing.T) {
	eng := newFakeEngine(t)
	a := agentApp(t, eng)
	if err := saveState(a.lay.State, agentState{GUIPort: 0, ControlSocket: "/tmp/x/agent.sock", Env: map[string]string{"XDG_STATE_HOME": "/data/state"}}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := updateState(a.lay.State, func(s *agentState) { s.GUIPort++ }); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	st, err := loadState(a.lay.State)
	if err != nil || st.GUIPort != 20 {
		t.Fatalf("twenty updates: port %d %v", st.GUIPort, err)
	}
	// The supervisor moving the port keeps the socket and the record.
	if st.ControlSocket != "/tmp/x/agent.sock" || st.Env["XDG_STATE_HOME"] != "/data/state" {
		t.Fatalf("fields lost: %+v", st)
	}
	// pair recording the shell from a copy it loaded before run published
	// the socket keeps the socket.
	stale := st
	if err := updateState(a.lay.State, func(s *agentState) { s.ControlSocket = "/tmp/y/agent.sock" }); err != nil {
		t.Fatal(err)
	}
	a.getenv = envOf(map[string]string{"XDG_CONFIG_HOME": "/data/config"})
	if err := a.rememberShellEnv(&stale); err != nil {
		t.Fatal(err)
	}
	if st, err = loadState(a.lay.State); err != nil || st.ControlSocket != "/tmp/y/agent.sock" || st.Env["XDG_CONFIG_HOME"] != "/data/config" || st.GUIPort != 20 {
		t.Fatalf("after the interleaving: %+v %v", st, err)
	}
	if fi, err := os.Stat(filepath.Join(a.lay.Base, "state.lock")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("state lock: %v %v", fi, err)
	}
}

// setup and pair record the shell's guard variables — set ones only — so
// the service's pairings can look where the shell looks.
func TestIssue176_TheShellsEnvironmentIsRecorded(t *testing.T) {
	got := recordGuardEnv(envOf(map[string]string{"XDG_STATE_HOME": "/data/state", "HOME": "/h", "APPDATA": ""}))
	if len(got) != 1 || got["XDG_STATE_HOME"] != "/data/state" {
		t.Fatalf("recorded %v", got)
	}
	// A shell with none set is a record too — its defaults apply — and not
	// the same as no record (nil, an agent.json from before).
	if none := recordGuardEnv(envOf(nil)); none == nil || len(none) != 0 {
		t.Fatalf("nothing set records an empty environment, got %v", none)
	}
	// pair in a terminal refreshes the record when the shell changed, and
	// leaves agent.json alone when it did not.
	eng := newFakeEngine(t)
	a := agentApp(t, eng)
	a.getenv = envOf(map[string]string{"XDG_CONFIG_HOME": "/data/config"})
	st, err := loadState(a.lay.State)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.rememberShellEnv(&st); err != nil {
		t.Fatal(err)
	}
	if st, err = loadState(a.lay.State); err != nil || st.Env["XDG_CONFIG_HOME"] != "/data/config" || len(st.Env) != 1 {
		t.Fatalf("recorded: %v %v", st.Env, err)
	}
	before, _ := os.Stat(a.lay.State)
	if err := a.rememberShellEnv(&st); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Stat(a.lay.State); !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("an unchanged shell rewrote agent.json")
	}
}

// fakeDirInfo is a directory as Lstat would describe it: mode, owner, link.
type fakeDirInfo struct {
	mode os.FileMode
	uid  uint32
}

func (f fakeDirInfo) Name() string       { return "d" }
func (f fakeDirInfo) Size() int64        { return 0 }
func (f fakeDirInfo) Mode() os.FileMode  { return f.mode }
func (f fakeDirInfo) ModTime() time.Time { return time.Time{} }
func (f fakeDirInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeDirInfo) Sys() any           { return &syscall.Stat_t{Uid: f.uid} }

// The fallback folder for the socket is the user's own — the session's
// runtime directory on Linux, the per-user temporary directory on macOS —
// and only when it is theirs alone: owned by them, 0700, no link. Never a
// shared /tmp, which another account could prepare or take over.
func TestIssue176_FallbackFolderIsTheUsersOwnOrNone(t *testing.T) {
	uid := 501
	dirs := map[string]os.FileInfo{
		"/run/user/501":  fakeDirInfo{mode: os.ModeDir | 0o700, uid: 501},
		"/rt/own":        fakeDirInfo{mode: os.ModeDir | 0o700, uid: 501},
		"/rt/open":       fakeDirInfo{mode: os.ModeDir | 0o755, uid: 501},
		"/rt/theirs":     fakeDirInfo{mode: os.ModeDir | 0o700, uid: 502},
		"/rt/link":       fakeDirInfo{mode: os.ModeSymlink | 0o700, uid: 501},
		"/tmp":           fakeDirInfo{mode: os.ModeDir | os.ModeSticky | 0o777, uid: 0},
		"/var/folders/T": fakeDirInfo{mode: os.ModeDir | 0o700, uid: 501},
	}
	lstat := func(p string) (os.FileInfo, error) {
		if fi, ok := dirs[p]; ok {
			return fi, nil
		}
		return nil, os.ErrNotExist
	}
	cases := []struct {
		goos string
		env  map[string]string
		want string
	}{
		{"linux", map[string]string{"XDG_RUNTIME_DIR": "/rt/own"}, "/rt/own"},
		{"linux", map[string]string{"XDG_RUNTIME_DIR": "/rt/open"}, "/run/user/501"},
		{"linux", map[string]string{"XDG_RUNTIME_DIR": "/rt/theirs"}, "/run/user/501"},
		{"linux", map[string]string{"XDG_RUNTIME_DIR": "/rt/link"}, "/run/user/501"},
		{"linux", map[string]string{"XDG_RUNTIME_DIR": "/tmp"}, "/run/user/501"},
		{"linux", nil, "/run/user/501"},
		{"darwin", map[string]string{"TMPDIR": "/var/folders/T"}, "/var/folders/T"},
		{"darwin", map[string]string{"TMPDIR": "/tmp"}, ""},
		{"darwin", nil, ""},
	}
	for _, c := range cases {
		if got := socketFallbackDir(c.goos, envOf(c.env), uid, lstat); got != c.want {
			t.Errorf("%s %v: got %q, want %q", c.goos, c.env, got, c.want)
		}
	}
	// Without a runtime directory of the user's own on Linux: none.
	if got := socketFallbackDir("linux", envOf(nil), 777, lstat); got != "" {
		t.Errorf("no folder of uid 777's own, got %q", got)
	}
}

// Only the user's own account talks to the user's own agent: the kernel
// says whose a connection is, before a byte is sent or read.
func TestIssue176_OnlyTheOwnerTalksToTheAgent(t *testing.T) {
	eng := newFakeEngine(t)
	a := agentApp(t, eng)
	client := serveControl(t, a)
	if _, err := client.status(context.Background()); err != nil {
		t.Fatalf("the owner's own connection: %v", err)
	}
	foreign := peerFunc(func(*net.UnixConn) (int, error) { return os.Getuid() + 1, nil })
	peerUIDHook.Store(&foreign)
	defer peerUIDHook.Store(nil)
	// The terminal does not send to a socket served by another account
	// (a fresh client: a connection once verified is kept and reused).
	err := newControlClient(a.lay.Socket).do(context.Background(), http.MethodGet, "/v1/status", nil, nil)
	if err == nil || errors.Is(err, errNoAgent) || !errors.Is(err, errForeignSocket) {
		t.Fatalf("a socket served by another account: %v", err)
	}
	st, _ := loadState(a.lay.State)
	st.ControlSocket = a.lay.Socket
	if err := saveState(a.lay.State, st); err != nil {
		t.Fatal(err)
	}
	a.out = &bytes.Buffer{}
	if err := a.status(context.Background()); err == nil || !strings.Contains(err.Error(), "another account") {
		t.Fatalf("status must not read past a foreign socket: %v", err)
	}
	if err := a.pauseSync(context.Background(), true); err == nil || !strings.Contains(err.Error(), "another account") {
		t.Fatalf("pause must not send to a foreign socket: %v", err)
	}
	// The agent closes a connection from another account unread.
	raw, err := net.Dial("unix", a.lay.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := raw.Write([]byte("GET /v1/status HTTP/1.1\r\nHost: vaultsync\r\n\r\n")); err == nil {
		buf := make([]byte, 64)
		if n, err := raw.Read(buf); err == nil && n > 0 {
			t.Fatalf("the agent answered another account: %q", buf[:n])
		}
	}
	if data, _ := os.ReadFile(filepath.Join(a.lay.Base, "control-socket.log")); !strings.Contains(string(data), "another account") {
		t.Fatalf("the closed connection is noted in the private log: %q", data)
	}
}

// VaultSync's own places include the socket's folder, read from
// agent.json: when that cannot be read, a pairing refuses rather than
// trusts a list it does not have.
func TestIssue176_UnreadableStateStopsAPairingBeforeAnyCheck(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads anything")
	}
	eng := newFakeEngine(t)
	hub := newFakeHub(t, eng)
	a := agentApp(t, eng)
	fresh := filepath.Join(a.home, "Fresh")
	if err := os.Chmod(a.lay.State, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(a.lay.State, 0o600) })
	_, err := a.pairWith(context.Background(), &term{out: io.Discard}, pairOptions{code: hub.code, hub: hub.addr, vault: "Fresh", create: true, path: fresh}, eng.client, pairOrigin{})
	if err == nil || isRefusal(err) || !strings.Contains(err.Error(), "VaultSync's own places are not known") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("a pairing with agent.json unreadable: %v", err)
	}
	if eng.folderCount() != 0 {
		t.Fatal("a folder was added without the reserved places known")
	}
	if _, err := os.Stat(fresh); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the folder was made")
	}
}

// When the socket's details cannot be written, the log says so instead
// of pointing at a file that is not there — and still names no path.
func TestIssue176_SocketErrorLogSaysWhenTheDetailsCouldNotBeWritten(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root writes anywhere")
	}
	base := t.TempDir()
	if err := os.Chmod(base, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(base, 0o700) })
	var logged []string
	err := &os.PathError{Op: "listen", Path: filepath.Join(base, "agent.sock"), Err: errors.New("invalid argument")}
	logSocketError(func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }, base, err)
	all := strings.Join(logged, "\n")
	if !strings.Contains(all, "could not be written") || strings.Contains(all, "details in control-socket-error.txt") || strings.Contains(all, base) {
		t.Fatalf("log: %v", logged)
	}
	if _, serr := os.Stat(filepath.Join(base, "control-socket-error.txt")); !errors.Is(serr, os.ErrNotExist) {
		t.Fatal("the details file exists after all")
	}

	// Several causes at once — the diagnostic file that could not be
	// opened and the socket that then could not be removed — each reach
	// the log on their own, in fixed words with the kind of the cause,
	// even though the file with the full words cannot be written either.
	defer func(real func(string) error) { removeEntry = real }(removeEntry)
	removeEntry = func(p string) error { return &os.PathError{Op: "remove", Path: p, Err: syscall.EACCES} }
	eng := newFakeEngine(t)
	a := agentApp(t, eng)
	if err := os.Chmod(a.lay.Base, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(a.lay.Base, 0o700) })
	ctl := &controlServer{a: a, eng: a.engine(), logf: t.Logf}
	_, _, serr := ctl.serve(context.Background())
	removeEntry = os.Remove
	_ = os.Remove(a.lay.Socket)
	if serr == nil {
		t.Fatal("serve must fail")
	}
	logged = nil
	logSocketError(func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }, a.lay.Base, serr)
	all = strings.Join(logged, "\n")
	if !strings.Contains(all, "the control socket could not be removed — a file operation failed (remove: permission denied)") || !strings.Contains(all, "open: permission denied") || !strings.Contains(all, "could not be written") {
		t.Fatalf("every startup cause in the log: %v", logged)
	}
	if strings.Contains(all, a.lay.Base) || strings.Contains(all, a.lay.Socket) {
		t.Fatalf("a path in the log: %v", logged)
	}
}

// Errors of the socket's own say what they are without a path, and the
// causes of an errors.Join are listed one by one.
func TestIssue176_SocketErrorsSummarizeThemselves(t *testing.T) {
	left := &socketLeftBehindError{cause: &os.PathError{Op: "remove", Path: "/secret/place/agent.sock", Err: syscall.EACCES}}
	if sm := serviceSummary(left); strings.Contains(sm, "/secret") || !strings.Contains(sm, "could not be removed") || !strings.Contains(sm, "remove: permission denied") {
		t.Fatalf("left behind: %q", sm)
	}
	if !errors.Is(left, errSocketLeftBehind) {
		t.Fatal("the typed error is errSocketLeftBehind")
	}
	if sm := serviceSummary(&notASocketError{path: "/secret/place/agent.sock"}); strings.Contains(sm, "/secret") || !strings.Contains(sm, "not a socket") {
		t.Fatalf("not a socket: %q", sm)
	}
	nf := &noFallbackDirError{cause: &os.PathError{Op: "listen", Path: "/secret/deep/agent.sock", Err: syscall.EINVAL}}
	if sm := serviceSummary(nf); strings.Contains(sm, "/secret") || !strings.Contains(sm, "no folder of the user's own") || !strings.Contains(sm, "listen: invalid argument") {
		t.Fatalf("no fallback folder: %q", sm)
	}
	joined := errors.Join(nf, left, errors.Join(&notASocketError{path: "x"}))
	if causes := eachCause(joined); len(causes) != 3 {
		t.Fatalf("causes of a join: %d", len(causes))
	}
	if causes := eachCause(left); len(causes) != 1 || causes[0] != left {
		t.Fatalf("a single error is its own cause: %v", causes)
	}
}

// A rollback that fails is reported next to what made it necessary: the
// fallback's folder that could not be removed after its listen failed,
// the socket that could not be removed after the diagnostic file could
// not be opened.
func TestIssue176_StartupRollbackFailuresAreReported(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root removes anything")
	}
	deep := filepath.Join(shortDir(t), strings.Repeat("d", 120), "agent.sock")
	if err := os.MkdirAll(filepath.Dir(deep), 0o700); err != nil {
		t.Fatal(err)
	}
	defer func(real func(string) error) { removeEntry = real }(removeEntry)
	removeEntry = func(string) error { return &os.PathError{Op: "remove", Path: "x", Err: syscall.EACCES} }
	// The fallback folder lies too deep itself: its listen fails, and the
	// folder's removal fails too — both are said with the first error.
	tooDeep := filepath.Join(shortDir(t), strings.Repeat("f", 90))
	if err := os.MkdirAll(tooDeep, 0o700); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := listenControl(deep, tooDeep)
	if err == nil || !strings.Contains(err.Error(), "fallback:") || !strings.Contains(err.Error(), "the socket's folder could not be removed") {
		t.Fatalf("fallback listen and rollback: %v", err)
	}
	// The diagnostic file cannot be opened: the socket is taken down, and
	// a removal that fails is said next to the open that failed.
	eng := newFakeEngine(t)
	a := agentApp(t, eng)
	if err := os.Chmod(a.lay.Base, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(a.lay.Base, 0o700) })
	ctl := &controlServer{a: a, eng: a.engine(), logf: t.Logf}
	_, _, err = ctl.serve(context.Background())
	if err == nil || !strings.Contains(err.Error(), "control-socket.log") || !strings.Contains(err.Error(), "the control socket could not be removed") {
		t.Fatalf("diagnostic open and rollback: %v", err)
	}
	removeEntry = os.Remove
	_ = os.Remove(a.lay.Socket)

	// A socket that could not be made owner-only is taken down again; when
	// that fails, the socket left behind is said with the chmod error — and
	// no fallback, however able, hides it.
	usual := filepath.Join(shortDir(t), "agent.sock")
	defer func(real func(string, os.FileMode) error) { chmodEntry = real }(chmodEntry)
	chmodEntry = func(p string, m os.FileMode) error {
		if p == usual {
			return &os.PathError{Op: "chmod", Path: "x", Err: syscall.EPERM}
		}
		return os.Chmod(p, m)
	}
	removeEntry = func(string) error { return &os.PathError{Op: "remove", Path: "x", Err: syscall.EACCES} }
	_, _, _, err = listenControl(usual, shortDir(t))
	if err == nil || !strings.Contains(err.Error(), "could not be made owner-only") || !errors.Is(err, errSocketLeftBehind) {
		t.Fatalf("chmod and removal failing: %v", err)
	}
	removeEntry = os.Remove
	_ = os.Remove(usual)
	// A chmod that fails with a removal that works: the fallback may serve.
	fb := shortDir(t)
	l, at, cleanup, err := listenControl(usual, fb)
	if err != nil || !strings.HasPrefix(at, fb+string(filepath.Separator)) {
		t.Fatalf("chmod failing at the usual place, fallback serving: %v at %s", err, at)
	}
	if _, serr := os.Lstat(usual); !errors.Is(serr, os.ErrNotExist) {
		t.Fatal("the socket at the usual place stays behind")
	}
	chmodEntry = os.Chmod
	l.Close()
	_ = cleanup()
}

// The diagnostic file keeps the first write that failed, and its close
// reports it — without a path.
func TestIssue176_DiagnosticsThatCouldNotBeWrittenAreReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control-socket.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o600) // open read-only: every write fails
	if err != nil {
		t.Fatal(err)
	}
	d := &diagLog{f: f}
	logger := log.New(d, "", 0)
	logger.Printf("accept: %s", path)
	logger.Printf("second")
	err = d.close()
	if err == nil || !strings.Contains(err.Error(), "could not all be kept") || !strings.Contains(err.Error(), "writing:") || strings.Contains(err.Error(), "closing:") || strings.Contains(err.Error(), path) {
		t.Fatalf("close after failed writes: %v", err)
	}
	// Both a write and the close failing are said, each on its own.
	h, err := os.Create(filepath.Join(t.TempDir(), "closed.log"))
	if err != nil {
		t.Fatal(err)
	}
	h.Close() // every write and the close fail from here on
	both := &diagLog{f: h}
	log.New(both, "", 0).Printf("gone: %s", path)
	err = both.close()
	if err == nil || !strings.Contains(err.Error(), "writing:") || !strings.Contains(err.Error(), "closing:") || strings.Contains(err.Error(), path) {
		t.Fatalf("close after failed write and close: %v", err)
	}
	// A file that takes every write closes without a word.
	g, err := os.Create(filepath.Join(t.TempDir(), "ok.log"))
	if err != nil {
		t.Fatal(err)
	}
	ok := &diagLog{f: g}
	log.New(ok, "", 0).Printf("fine")
	if err := ok.close(); err != nil {
		t.Fatalf("a healthy file: %v", err)
	}
}

// What the stop could not do is in the service log in its own words,
// whatever becomes of the error afterwards: the socket that stayed, the
// diagnostics that could not be kept.
func TestIssue176_StopSaysInTheServiceLogWhatItCouldNotDo(t *testing.T) {
	eng := newFakeEngine(t)
	a := agentApp(t, eng)
	defer func(real func(string) (*os.File, error)) { openDiagLog = real }(openDiagLog)
	openDiagLog = func(path string) (*os.File, error) {
		return os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o600) // takes no write
	}
	logged := &logLines{}
	ctl := &controlServer{a: a, eng: a.engine(), logf: func(format string, args ...any) { logged.add(fmt.Sprintf(format, args...)) }}
	_, stop, err := ctl.serve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctl.srv.ErrorLog.Printf("http: Accept error: accept unix %s: too many open files", a.lay.Socket)
	// The socket cannot be taken down either: both are said, each in fixed
	// words with the kind of its cause — the socket's path may lie where
	// no redaction reaches.
	defer func(real func(string) error) { removeEntry = real }(removeEntry)
	removeEntry = func(p string) error { return &os.PathError{Op: "remove", Path: p, Err: syscall.EACCES} }
	err = stop()
	removeEntry = os.Remove
	_ = os.Remove(a.lay.Socket)
	if err == nil || !strings.Contains(err.Error(), "writing:") || !errors.Is(err, errSocketLeftBehind) {
		t.Fatalf("stop: %v", err)
	}
	var saidDiag, saidSocket bool
	for _, line := range logged.all() {
		if strings.Contains(line, "could not all be kept") && strings.Contains(line, "writing:") {
			saidDiag = true
		}
		if strings.Contains(line, "could not be taken down on the way out") && strings.Contains(line, "remove: permission denied") {
			saidSocket = true
		}
		if strings.Contains(line, a.lay.Socket) || strings.Contains(line, filepath.Dir(a.lay.Socket)) || strings.Contains(line, a.lay.Base) {
			t.Fatalf("a path in the service log: %q", line)
		}
	}
	if !saidDiag || !saidSocket {
		t.Fatalf("the service log lacks the stop's words (diagnostics %v, socket %v): %v", saidDiag, saidSocket, logged.all())
	}
}

// A port nobody serves answers with a refusal — or with a reset, when a
// listener closed before accepting, as the supervisor's own port check
// does for a moment; both say the same.
func TestIssue176_ARefusedOrResetConnectIsNobodyListening(t *testing.T) {
	dial := func(e error) error {
		return &net.OpError{Op: "dial", Err: &os.SyscallError{Syscall: "connect", Err: e}}
	}
	for _, err := range []error{dial(syscall.ECONNREFUSED), dial(syscall.ECONNRESET)} {
		if !nobodyListens(err) {
			t.Fatalf("%v is nobody listening", err)
		}
	}
	// A reset or a failure after the connect is a server that failed; a
	// bare error without the dial is not a connect that found nobody.
	for _, err := range []error{
		&net.OpError{Op: "read", Err: &os.SyscallError{Syscall: "read", Err: syscall.ECONNRESET}},
		&net.OpError{Op: "write", Err: &os.SyscallError{Syscall: "write", Err: syscall.ECONNRESET}},
		syscall.ECONNRESET, syscall.ECONNREFUSED,
		dial(syscall.EACCES), context.DeadlineExceeded, errors.New("403 Forbidden"),
	} {
		if nobodyListens(err) {
			t.Fatalf("%v says nothing about a listener", err)
		}
	}

	// An engine that accepts, takes the request and then resets the
	// connection is an engine that failed, not one on its way up: the
	// agent's status says so. (It reads the request first, so the reset
	// lands after the connect — a reset racing the connect itself would be
	// the supervisor's port check, which is nobody listening.)
	eng := newFakeEngine(t)
	a := agentApp(t, eng)
	resetting, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := resetting.Accept()
			if err != nil {
				return
			}
			_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, _ = c.Read(make([]byte, 1))
			_ = c.(*net.TCPConn).SetLinger(0)
			c.Close()
		}
	}()
	t.Cleanup(func() { resetting.Close() })
	writeFile(t, filepath.Join(a.lay.Home, "config.xml"), "<configuration><gui><address>"+resetting.Addr().String()+"</address><apikey>engine-key</apikey></gui></configuration>\n")
	if err := saveState(a.lay.State, agentState{GUIPort: resetting.Addr().(*net.TCPAddr).Port, Env: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	client := serveControl(t, a)
	if _, err := client.status(context.Background()); err == nil || !strings.Contains(err.Error(), "the sync engine does not answer") {
		t.Fatalf("an engine that accepts and resets: %v", err)
	}
}

// A socket address too long for this system is no agent: the terminal
// reads the engine directly — after an agent that fell back is gone and
// its place is out of agent.json, and for an agent that has no folder of
// the user's own to fall back into.
func TestIssue176_AnOverlongSocketAddressIsNoAgent(t *testing.T) {
	deep := filepath.Join(shortDir(t), strings.Repeat("d", 120), "agent.sock")
	if err := newControlClient(deep).do(context.Background(), http.MethodGet, "/v1/status", nil, nil); !errors.Is(err, errNoAgent) {
		t.Fatalf("an overlong address: %v", err)
	}
	a := terminalApp(t, deep)
	if err := a.status(context.Background()); err != nil || !strings.Contains(a.out.(*bytes.Buffer).String(), "Sync engine: not running — nothing syncs right now") {
		t.Fatalf("status with an overlong default address: %v\n%s", err, a.out)
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
	all := strings.Join(logged, "\n")
	if len(logged) != 2 || strings.Contains(all, base) || !strings.Contains(all, "no control socket") || !strings.Contains(all, "vaultsync status reads the engine directly") || !strings.Contains(all, "details in control-socket-error.txt") {
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
