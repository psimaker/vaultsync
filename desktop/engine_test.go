//go:build unix

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psimaker/vaultsync/desktop/internal/control"
)

// fakeEngineBinary is a stand-in for syncthing: it records its arguments,
// then exits with the codes listed in a file, one per start, and finally
// runs until it is stopped — recording its process ID for fakeEnginePIDs.
func fakeEngineBinary(t *testing.T, lay layout, codes ...string) string {
	t.Helper()
	calls := filepath.Join(t.TempDir(), "calls")
	codesFile := filepath.Join(t.TempDir(), "codes")
	if err := os.WriteFile(codesFile, []byte(strings.Join(codes, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	next := codesFile + ".next"
	script := `#!/bin/sh
echo "$@" >> ` + shWord(calls) + `
code=$(head -n 1 ` + shWord(codesFile) + `)
tail -n +2 ` + shWord(codesFile) + ` > ` + shWord(next) + ` && mv ` + shWord(next) + ` ` + shWord(codesFile) + `
if [ -n "$code" ]; then exit "$code"; fi
echo $$ >> ` + shWord(fakeEnginePIDFile(lay)) + `
trap 'exit 0' TERM
while :; do sleep 1; done
`
	writeFile(t, lay.Syncthing, script)
	if err := os.Chmod(lay.Syncthing, 0o755); err != nil {
		t.Fatal(err)
	}
	return calls
}

// shWord is p as one word of a shell script, whatever it holds.
func shWord(p string) string { return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'" }

func fakeEnginePIDFile(lay layout) string { return lay.Syncthing + ".pids" }

// fakeEnginePIDs lists the process IDs of the fake engines that ran until
// stopped under lay, in the order they started.
func fakeEnginePIDs(t *testing.T, lay layout) []int {
	t.Helper()
	data, err := os.ReadFile(fakeEnginePIDFile(lay))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	// Only whole lines: a start may be writing its own right now.
	text := string(data)
	text = text[:strings.LastIndexByte(text, '\n')+1]
	var pids []int
	for _, line := range strings.Fields(text) {
		pid, err := strconv.Atoi(line)
		if err != nil {
			t.Fatalf("a fake engine's process ID: %q", line)
		}
		pids = append(pids, pid)
	}
	return pids
}

// errStillRunning: what inBackground's stop says when fn did not return.
var errStillRunning = errors.New("still running 30 s after it was asked to stop")

// inBackground runs fn in a goroutine of its own until the test calls the
// returned stop — or until the test ends, however it ends: a t.Fatal on the
// way leaves nothing running. fn's engine is a child process, and on macOS
// nothing else stops it: there is no parent-death signal there
// (child_other.go), so an engine a failed test forgot loops on long after
// the tests and their folders are gone (#256). stop cancels fn's context,
// waits for fn to return and returns what fn returned (errStillRunning after
// 30 s); calling it again returns the same.
func inBackground(t *testing.T, fn func(context.Context) error) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- fn(ctx) }()
	var once sync.Once
	var err error
	stop = func() error {
		once.Do(func() {
			cancel()
			select {
			case err = <-done:
			case <-time.After(30 * time.Second):
				err = errStillRunning
			}
		})
		return err
	}
	// What fn returned is the test's to judge, when it stops fn itself;
	// only a run that will not stop is this cleanup's to report.
	t.Cleanup(func() {
		if err := stop(); errors.Is(err, errStillRunning) {
			t.Errorf("a run started in the background is %v", err)
		}
	})
	return stop
}

func engineFixture(t *testing.T) (engine, agentState) {
	t.Helper()
	lay, err := layoutFor("linux", t.TempDir(), envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	port, err := freeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	return engine{lay: lay}, agentState{GUIPort: port}
}

func TestIssue175_SupervisorRestartsOnRequestAndStopsCleanly(t *testing.T) {
	eng, st := engineFixture(t)
	calls := fakeEngineBinary(t, eng.lay, "3")
	stop := inBackground(t, func(ctx context.Context) error { return eng.supervise(ctx, st, t.Logf) })
	e2eWait(t, "two engine starts", 10*time.Second, func() bool {
		data, _ := os.ReadFile(calls)
		return strings.Count(string(data), "\n") >= 2
	})
	// One owner only.
	if err := eng.supervise(context.Background(), st, t.Logf); !errors.Is(err, ErrEngineRunning) {
		t.Fatalf("a second supervisor must be refused, got %v", err)
	}
	if err := stop(); err != nil {
		t.Fatalf("a requested stop is not an error: %v", err)
	}
	data, _ := os.ReadFile(calls)
	first := strings.SplitN(string(data), "\n", 2)[0]
	for _, want := range []string{"serve", "--home=" + eng.lay.Home, "--gui-address=http://127.0.0.1:", "--no-browser", "--no-restart", "--no-upgrade", "--log-file="} {
		if !strings.Contains(first, want) {
			t.Errorf("engine started without %q: %s", want, first)
		}
	}
	if strings.Contains(first, "apikey") {
		t.Error("the API key must never be on the command line")
	}
}

func TestIssue175_SupervisorReportsAnEngineThatStopsOnItsOwn(t *testing.T) {
	eng, st := engineFixture(t)
	fakeEngineBinary(t, eng.lay, "1")
	err := eng.supervise(context.Background(), st, t.Logf)
	if err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("got %v", err)
	}
}

// The engine's config is adjusted before its first start; a shape this
// agent does not know stops setup instead of being guessed at.
func TestIssue175_FreshConfigIsAdjustedBeforeTheFirstStart(t *testing.T) {
	shape := `<configuration version="52">
    <gui enabled="true" tls="false" sendBasicAuthPrompt="false">
        <address>127.0.0.1:8384</address>
        <apikey>generated-key</apikey>
    </gui>
    <options>
        <listenAddress>default</listenAddress>
        <globalAnnounceEnabled>true</globalAnnounceEnabled>
        <localAnnounceEnabled>true</localAnnounceEnabled>
        <relaysEnabled>true</relaysEnabled>
        <startBrowser>true</startBrowser>
        <natEnabled>true</natEnabled>
        <urAccepted>0</urAccepted>
        <autoUpgradeIntervalH>12</autoUpgradeIntervalH>
        <crashReportingEnabled>true</crashReportingEnabled>
    </options>
</configuration>
`
	eng, _ := engineFixture(t)
	eng.opts = engineOptions{avoidDefaultPorts: true, listenPort: 22007}
	writeFile(t, eng.configPath(), shape)
	if err := eng.adjustFreshConfig(eng.configPath(), 41234); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(eng.configPath())
	got := string(data)
	for _, want := range []string{
		"<address>127.0.0.1:41234</address>",
		"<startBrowser>false</startBrowser>",
		"<urAccepted>-1</urAccepted>",
		"<crashReportingEnabled>false</crashReportingEnabled>",
		"<autoUpgradeIntervalH>0</autoUpgradeIntervalH>",
		"<listenAddress>tcp://:22007</listenAddress>",
		"<listenAddress>quic://:22007</listenAddress>",
		"<listenAddress>dynamic+https://relays.syncthing.net/endpoint</listenAddress>",
		"<globalAnnounceEnabled>true</globalAnnounceEnabled>", // discovery stays on outside tests
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config lacks %q", want)
		}
	}
	if key, err := eng.apiKey(); err != nil || key != "generated-key" {
		t.Fatalf("api key: %q %v", key, err)
	}
	if info, _ := os.Stat(eng.configPath()); info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode %v", info.Mode())
	}

	writeFile(t, eng.configPath(), strings.Replace(shape, "<urAccepted>0</urAccepted>", "", 1))
	if err := eng.adjustFreshConfig(eng.configPath(), 41234); err == nil || !strings.Contains(err.Error(), "unexpected shape") {
		t.Fatalf("an unknown shape must stop setup, got %v", err)
	}
}

func TestIssue175_StatusWording(t *testing.T) {
	hub := "AAAAAAA"
	conns := func(connected bool) connectionsView {
		var c connectionsView
		c.Connections = map[string]struct {
			Connected bool   `json:"connected"`
			Type      string `json:"type"`
		}{hub: {Connected: connected}}
		return c
	}
	complete := map[string]remoteCompletion{hub: {Completion: 100, RemoteState: "valid"}}
	f := syncthingFolder("vs-1", "Notes", "/v")
	f.Devices = append(f.Devices, folderDeviceOf("ME"), folderDeviceOf(hub))
	cases := []struct {
		sum       folderSummary
		connected bool
		remote    map[string]remoteCompletion
		want      string
	}{
		{folderSummary{State: "idle"}, true, complete, "up to date"},
		{folderSummary{State: "idle"}, false, complete, "waiting for your Hub"},
		{folderSummary{State: "syncing", GlobalBytes: 200, InSyncBytes: 50}, true, complete, "syncing — 25 %"},
		{folderSummary{State: "scanning"}, true, complete, "scanning the folder"},
		{folderSummary{State: "error", Error: "folder path missing"}, true, complete, "error: folder path missing"},
		{folderSummary{State: "idle", Error: "open /v: operation not permitted"}, true, complete, "Privacy & Security"},
		// The engine checks a folder it could not open again only at its next
		// full scan (an hour by default); the manual test needed a restart.
		{folderSummary{State: "idle", Error: "open /v: operation not permitted"}, true, complete, "then run vaultsync stop and vaultsync start"},
		// Never Full Disk Access for a vault: Files & Folders is enough.
		{folderSummary{State: "idle", NeedTotal: 3}, true, complete, "3 items left to sync"},
		{folderSummary{State: "idle", NeedTotal: 3}, false, complete, "3 items left to sync"},
		// Idle here does not mean the Hub has everything.
		{folderSummary{State: "idle"}, true, map[string]remoteCompletion{hub: {Completion: 60, NeedItems: 40, RemoteState: "valid"}}, "uploading to your Hub — 60 %"},
		{folderSummary{State: "idle"}, true, map[string]remoteCompletion{hub: {Completion: 100, RemoteState: "notSharing"}}, "waiting for your Hub to take it"},
		{folderSummary{State: "idle"}, true, map[string]remoteCompletion{}, "waiting for your Hub to take it"},
		{folderSummary{State: "idle"}, true, map[string]remoteCompletion{hub: {Completion: 100, RemoteState: "paused"}}, "paused on your Hub"},
		// Byte-based 100 % with an empty note or a deletion still outstanding.
		{folderSummary{State: "idle"}, true, map[string]remoteCompletion{hub: {Completion: 100, NeedItems: 1, RemoteState: "valid"}}, "uploading to your Hub — 1 item left"},
		{folderSummary{State: "idle"}, true, map[string]remoteCompletion{hub: {Completion: 100, NeedDeletes: 2, RemoteState: "valid"}}, "uploading to your Hub — 2 items left"},
		{folderSummary{State: "idle"}, true, map[string]remoteCompletion{hub: {Completion: 100, RemoteState: ""}}, "waiting for your Hub to take it"},
	}
	for _, c := range cases {
		if got, _ := describeFolder(f, c.sum, conns(c.connected), c.remote, "ME", true); !strings.Contains(got, c.want) {
			t.Errorf("%+v connected=%v remote=%v: %q, want %q", c.sum, c.connected, c.remote, got, c.want)
		}
	}
	denied := folderSummary{State: "idle", Error: "open /v: operation not permitted"}
	if got, _ := describeFolder(f, denied, conns(true), complete, "ME", true); strings.Contains(got, "Full Disk Access") {
		t.Errorf("more access than a vault needs: %q", got)
	}
	// Without the background service the engine runs in a terminal: the
	// terminal app needs the access, and `vaultsync run` the restart.
	if got, _ := describeFolder(f, denied, conns(true), complete, "ME", false); !strings.Contains(got, "the app you run vaultsync in") || !strings.Contains(got, "restart vaultsync run") || strings.Contains(got, "vaultsync stop") {
		t.Errorf("foreground engine: %q", got)
	}
	if !strings.Contains(localNetworkHint(true), "vaultsync stop and vaultsync start") || !strings.Contains(localNetworkHint(false), "restart vaultsync run") {
		t.Errorf("local network hints: %q / %q", localNetworkHint(true), localNetworkHint(false))
	}
	f.Paused = true
	if got, kind := describeFolder(f, folderSummary{State: "idle"}, conns(true), complete, "ME", true); got != "paused" || kind != control.KindPaused {
		t.Errorf("paused: %q", got)
	}

	refused := "dial tcp 192.168.1.20:22000: connect: no route to host"
	other := "dial tcp 203.0.113.9:22000: connect: no route to host"
	var sys systemStatusView
	sys.LastDialStatus = map[string]struct {
		Error *string `json:"error"`
	}{"tcp://203.0.113.9:22000": {Error: &other}}
	if localNetworkRefused(sys) {
		t.Error("a public address is not the local network")
	}
	sys.LastDialStatus["tcp://192.168.1.20:22000"] = struct {
		Error *string `json:"error"`
	}{Error: &refused}
	if !localNetworkRefused(sys) {
		t.Error("no route to a LAN address is how macOS refuses local network access")
	}
}

// fakeGenerate is a stand-in for `syncthing generate`: it writes an identity
// and the config in shape into --home.
func fakeGenerate(t *testing.T, lay layout, shape string) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "config.xml")
	writeFile(t, cfg, shape)
	script := `#!/bin/sh
for a in "$@"; do case "$a" in --home=*) home="${a#--home=}" ;; esac; done
cat > /dev/null
mkdir -p "$home" && cp ` + cfg + ` "$home/config.xml" && echo cert > "$home/cert.pem" && echo key > "$home/key.pem"
`
	writeFile(t, lay.Syncthing, script)
	if err := os.Chmod(lay.Syncthing, 0o755); err != nil {
		t.Fatal(err)
	}
}

const generatedShape = `<configuration version="52">
    <gui enabled="true" tls="false" sendBasicAuthPrompt="false">
        <address>127.0.0.1:8384</address>
        <apikey>generated-key</apikey>
    </gui>
    <options>
        <listenAddress>default</listenAddress>
        <startBrowser>true</startBrowser>
        <urAccepted>0</urAccepted>
        <autoUpgradeIntervalH>12</autoUpgradeIntervalH>
        <crashReportingEnabled>true</crashReportingEnabled>
    </options>
</configuration>
`

// An interrupted or failed prepare must never leave an engine that looks
// ready but would start on stock settings (crash reports on, port 22000).
func TestIssue175_InterruptedPrepareLeavesNoHalfReadyEngine(t *testing.T) {
	eng, _ := engineFixture(t)
	var st agentState
	fakeGenerate(t, eng.lay, strings.Replace(generatedShape, "<urAccepted>0</urAccepted>", "", 1))
	if err := eng.prepare(context.Background(), &st); err == nil || !strings.Contains(err.Error(), "unexpected shape") {
		t.Fatalf("expected the shape refusal, got %v", err)
	}
	if eng.prepared() || st.GUIPort != 0 {
		t.Fatalf("a failed prepare left a ready-looking engine: prepared=%v port=%d", eng.prepared(), st.GUIPort)
	}
	if _, err := os.Stat(eng.lay.Home + ".new"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the staging folder was left behind")
	}
	if err := eng.supervise(context.Background(), st, t.Logf); err == nil || !strings.Contains(err.Error(), "not set up") {
		t.Fatalf("an engine without its API port must never start, got %v", err)
	}

	// The next prepare starts over and succeeds.
	fakeGenerate(t, eng.lay, generatedShape)
	if err := eng.prepare(context.Background(), &st); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(eng.configPath())
	if !eng.prepared() || st.GUIPort == 0 || !strings.Contains(string(data), "<crashReportingEnabled>false</crashReportingEnabled>") ||
		!strings.Contains(string(data), "<address>127.0.0.1:"+strconv.Itoa(st.GUIPort)+"</address>") {
		t.Fatalf("prepared=%v port=%d config:\n%s", eng.prepared(), st.GUIPort, data)
	}

	// agent.json lost: the port comes back from the engine's own config.
	lost := agentState{}
	if err := eng.prepare(context.Background(), &lost); err != nil || lost.GUIPort != st.GUIPort {
		t.Fatalf("recovered port %d (%v), want %d", lost.GUIPort, err, st.GUIPort)
	}
}

// An engine folder that cannot be read is no empty one: setup stops before
// generating anything.
func TestIssue175_UnreadableEngineFolderStopsSetup(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any folder")
	}
	eng, _ := engineFixture(t)
	if err := os.MkdirAll(eng.lay.Home, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(eng.lay.Home, "cert.pem"), "identity")
	if err := os.Chmod(eng.lay.Home, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(eng.lay.Home, 0o700)
	fakeGenerate(t, eng.lay, generatedShape)
	var st agentState
	if err := eng.prepare(context.Background(), &st); err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(eng.lay.Home + ".new"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("setup generated an identity despite the unreadable folder")
	}
}

// A non-empty engine folder without a config is never overwritten.
func TestIssue175_IncompleteEngineFolderIsLeftAlone(t *testing.T) {
	eng, _ := engineFixture(t)
	writeFile(t, filepath.Join(eng.lay.Home, "cert.pem"), "someone's identity")
	fakeGenerate(t, eng.lay, generatedShape)
	var st agentState
	if err := eng.prepare(context.Background(), &st); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("got %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(eng.lay.Home, "cert.pem")); string(data) != "someone's identity" {
		t.Fatal("the existing identity was changed")
	}
}
