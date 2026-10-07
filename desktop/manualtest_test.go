package main

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/psimaker/vaultsync/hub/pairing"
)

// Regression tests for what the first manual run of the agent found (#175):
// a fresh macOS 27 user, a Raspberry Pi with Debian 13, a Hub on Syncthing 1.

// launchd removes a booted-out job a moment after `launchctl bootout`
// returns (100–200 ms on macOS 27). `vaultsync stop; vaultsync start` used to
// find the job still there, do nothing, leave it disabled and report that
// VaultSync runs again — while nothing ran, not even after the next login.
func TestIssue175_StopThenStartResumesTheLaunchAgent(t *testing.T) {
	run := &fakeRunner{lingerAfterBootout: 2}
	svc, _ := testService(t, "darwin", run)
	if _, err := svc.install(svc.lay.Agent, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.stop(); err != nil {
		t.Fatal(err)
	}
	if run.loaded || !run.disabled {
		t.Fatalf("stop returned before launchd let go of the job: loaded=%v disabled=%v %v", run.loaded, run.disabled, run.calls)
	}
	if err := svc.start(); err != nil {
		t.Fatal(err)
	}
	if !run.loaded || run.disabled {
		t.Fatalf("start right after stop: loaded=%v disabled=%v %v", run.loaded, run.disabled, run.calls)
	}
}

// A job that is loaded but disabled (a stop that ran into a timeout, or a
// `launchctl disable` by hand) is enabled and started again by start.
func TestIssue175_StartEnablesALoadedJob(t *testing.T) {
	run := &fakeRunner{}
	svc, _ := testService(t, "darwin", run)
	if _, err := svc.install(svc.lay.Agent, true); err != nil {
		t.Fatal(err)
	}
	run.disabled, run.idle = true, true
	run.calls = nil
	if err := svc.start(); err != nil {
		t.Fatal(err)
	}
	if run.disabled || run.idle {
		t.Fatalf("start left the job disabled=%v idle=%v: %v", run.disabled, run.idle, run.calls)
	}
}

// start says so when the background service does not come up, instead of
// "running again".
func TestIssue175_StartSaysSoWhenTheServiceDoesNotRun(t *testing.T) {
	run := &fakeRunner{neverRuns: true}
	svc, _ := testService(t, "darwin", run)
	if _, err := svc.install(svc.lay.Agent, true); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	a := &app{goos: "darwin", home: svc.home, getenv: envOf(nil), lay: svc.lay, svc: svc, out: out}
	err := a.startService()
	if err == nil || strings.Contains(out.String(), "running again") {
		t.Fatalf("err=%v out=%q", err, out)
	}
	if !strings.Contains(refusalText(err), "vaultsync.log") {
		t.Fatalf("the refusal should point at the log: %q", refusalText(err))
	}
}

// A vault that syncs and that Obsidian also lists was shown twice under
// "Already syncing": once from Obsidian's list, once from the Hub's.
func TestIssue175_MenuListsASyncingVaultOnce(t *testing.T) {
	eng := newFakeEngine(t)
	hub := newFakeHub(t, eng)
	s, _ := testSession(t, eng, hub, pairOptions{})
	local := filepath.Join(s.env.home, "Documents", "Testvault")
	mkVault(t, local)
	eng.folders = append(eng.folders, syncthingFolder("vs-1", "Testvault", local))
	registry := filepath.Join(s.env.home, "obsidian.json")
	writeFile(t, registry, fmt.Sprintf(`{"vaults":{"9f561515d74acab7":{"path":%q,"ts":%d,"open":true}}}`, local, time.Now().UnixMilli()))
	s.env.registries = []string{registry}
	s.hello.Vaults = []pairing.VaultInfo{
		{ID: "vs-1", Label: "Testvault", Files: 6},
		{ID: "vs-2", Label: "Hub-Test", Files: 1},
		{ID: "vs-3", Label: "Notizen", Files: 4},
	}
	m, err := s.buildMenu(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(m.already) != 1 || m.already[0].label != "Testvault" {
		t.Fatalf("already syncing: %+v", m.already)
	}
	if len(m.hub) != 2 || m.hub[0].note != "1 file on your Hub" || m.hub[1].note != "4 files on your Hub" {
		t.Fatalf("on your Hub: %+v", m.hub)
	}
}

// With "Don't Allow" on macOS's local network question, macOS refuses the
// agent's connection to the Hub at once ("no route to host") — also when
// vaultsync runs in Terminal, because the decision belongs to the program.
// Pairing said "Your Hub did not answer" and never named the setting.
func TestIssue175_PairingNamesTheLocalNetworkSetting(t *testing.T) {
	refused := &url.Error{Op: "Post", URL: "http://192.168.8.70:8390/v1/pair/start", Err: &net.OpError{
		Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.EHOSTUNREACH),
	}}
	s := &pairSession{env: pairEnv{goos: "darwin"}, hub: pairing.DiscoveredHub{Address: "192.168.8.70:8390"}}
	got := handshakeText(refused, s)
	if !strings.Contains(got, "Local Network") || !strings.Contains(got, "“vaultsync”") {
		t.Fatalf("darwin, no route to host: %q", got)
	}
	if !strings.Contains(localNetworkHint(true), "“vaultsync”") {
		t.Fatalf("System Settings lists the program as “vaultsync”: %q", localNetworkHint(true))
	}
	s.env.goos = "linux"
	if got := handshakeText(refused, s); strings.Contains(got, "Local Network") || !strings.Contains(got, "did not answer") {
		t.Fatalf("linux: %q", got)
	}
	s.env.goos = "darwin"
	down := &url.Error{Op: "Post", URL: "http://192.168.8.70:8390/v1/pair/start", Err: &net.OpError{
		Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED),
	}}
	if got := handshakeText(down, s); strings.Contains(got, "Local Network") {
		t.Fatalf("a refused port is no privacy setting: %q", got)
	}
}

// A folder in Documents, Desktop or Downloads that macOS does not let the
// terminal read fails with "operation not permitted". Pairing said "check its
// permissions or reconnect its disk".
func TestIssue175_ReadRefusalNamesFilesAndFolders(t *testing.T) {
	home := "/Users/vstest"
	path := filepath.Join(home, "Documents", "Testvault")
	tcc := &fs.PathError{Op: "open", Path: path, Err: syscall.EPERM}
	got := refusalText(cannotRead("darwin", home, path, tcc, false))
	// Inside the background service (the control socket's pairing) it is
	// "vaultsync" that macOS has to allow, not the terminal app.
	if bg := refusalText(cannotRead("darwin", home, path, tcc, true)); !strings.Contains(bg, "Allow “vaultsync”") || strings.Contains(bg, "Terminal") {
		t.Fatalf("background wording: %s", bg)
	}
	if !strings.Contains(got, "Files & Folders") || !strings.Contains(got, "~/Documents/Testvault") {
		t.Fatalf("darwin, operation not permitted: %q", got)
	}
	for _, c := range []struct {
		goos string
		err  error
	}{
		{"linux", tcc},
		{"darwin", &fs.PathError{Op: "open", Path: path, Err: syscall.EACCES}},
	} {
		if got := refusalText(cannotRead(c.goos, home, path, c.err, false)); strings.Contains(got, "Files & Folders") || !strings.Contains(got, "Check its permissions") {
			t.Fatalf("%s %v: %q", c.goos, c.err, got)
		}
	}
}

// A stop that ran out of time leaves a job that launchd is still removing;
// launchd shows it like a running one. start loaded nothing, saw "running"
// and reported success — then the job was gone.
func TestIssue175_StartAfterAStopThatRanOutOfTime(t *testing.T) {
	run := &fakeRunner{lingerAfterBootout: 302} // longer than stop waits
	svc, _ := testService(t, "darwin", run)
	if _, err := svc.install(svc.lay.Agent, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.stop(); err == nil {
		t.Fatal("stop should report a job launchd did not let go of")
	}
	out := &bytes.Buffer{}
	a := &app{goos: "darwin", home: svc.home, getenv: envOf(nil), lay: svc.lay, svc: svc, out: out}
	if err := a.startService(); err != nil {
		t.Fatalf("start: %v (%v)", err, run.calls)
	}
	if !run.loaded || run.disabled || run.lingering != 0 {
		t.Fatalf("start reported success for a job that is gone: loaded=%v disabled=%v lingering=%d", run.loaded, run.disabled, run.lingering)
	}
}

// When macOS keeps vaultsync off the local network, its search for a Hub may
// find nothing or fail; on a Mac both say where to look.
func TestIssue175_DiscoveryNamesTheLocalNetworkSetting(t *testing.T) {
	for _, c := range []struct {
		goos string
		err  error
		want bool
	}{
		{"darwin", nil, true},
		{"darwin", pairing.ErrNoProbeSent, true},
		{"linux", nil, false},
		{"linux", pairing.ErrNoProbeSent, false},
	} {
		eng := newFakeEngine(t)
		s, _ := testSession(t, eng, nil, pairOptions{})
		s.env.goos = c.goos
		s.env.discover = func(context.Context) ([]pairing.DiscoveredHub, error) { return nil, c.err }
		got := refusalText(s.findHub(context.Background()))
		if strings.Contains(got, "Local Network") != c.want || !strings.Contains(got, "--hub") {
			t.Errorf("%s, %v: %q", c.goos, c.err, got)
		}
	}
}

// Obsidian Sync cannot be seen from the vault, so the consent for a folder
// with files says what to turn off.
func TestIssue175_ConsentMentionsOtherSyncServices(t *testing.T) {
	eng := newFakeEngine(t)
	s, out := testSession(t, eng, nil, pairOptions{}, "", "n")
	local := filepath.Join(s.env.home, "Notes")
	mkVault(t, local)
	if _, err := s.planForLocal(context.Background(), local); err != errCancelled {
		t.Fatalf("no consent: %v", err)
	}
	if !strings.Contains(out.String(), "If Obsidian Sync or another service also syncs this folder, turn that off for it first.") {
		t.Fatalf("consent:\n%s", out)
	}
}

// status picks its remedies by who runs the engine: an installed but stopped
// service (vaultsync stop, then vaultsync run in a terminal) is no background
// engine, so Terminal needs the access and `vaultsync run` the restart.
func TestIssue175_StatusKnowsAForegroundEngine(t *testing.T) {
	run := &fakeRunner{}
	svc, _ := testService(t, "darwin", run)
	a := &app{goos: "darwin", home: svc.home, getenv: envOf(nil), lay: svc.lay, svc: svc, out: &bytes.Buffer{}}
	if a.backgroundEngine() {
		t.Fatal("no service installed: the engine runs in a terminal")
	}
	if _, err := svc.install(svc.lay.Agent, true); err != nil {
		t.Fatal(err)
	}
	if !a.backgroundEngine() {
		t.Fatal("installed and running: the background service owns the engine")
	}
	if err := svc.stop(); err != nil {
		t.Fatal(err)
	}
	if a.backgroundEngine() {
		t.Fatal("installed but stopped: a running engine is a vaultsync run in a terminal")
	}
}
