//go:build unix

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeEngineBinary is a stand-in for syncthing: it records its arguments,
// then exits with the codes listed in a file, one per start, and finally
// runs until it is stopped.
func fakeEngineBinary(t *testing.T, lay layout, codes ...string) string {
	t.Helper()
	calls := filepath.Join(t.TempDir(), "calls")
	codesFile := filepath.Join(t.TempDir(), "codes")
	if err := os.WriteFile(codesFile, []byte(strings.Join(codes, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
echo "$@" >> ` + calls + `
code=$(head -n 1 ` + codesFile + `)
tail -n +2 ` + codesFile + ` > ` + codesFile + `.next && mv ` + codesFile + `.next ` + codesFile + `
if [ -n "$code" ]; then exit "$code"; fi
trap 'exit 0' TERM
while :; do sleep 1; done
`
	writeFile(t, lay.Syncthing, script)
	if err := os.Chmod(lay.Syncthing, 0o755); err != nil {
		t.Fatal(err)
	}
	return calls
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
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- eng.supervise(ctx, st, t.Logf) }()
	e2eWait(t, "two engine starts", 10*time.Second, func() bool {
		data, _ := os.ReadFile(calls)
		return strings.Count(string(data), "\n") >= 2
	})
	// One owner only.
	if err := eng.supervise(context.Background(), st, t.Logf); !errors.Is(err, ErrEngineRunning) {
		t.Fatalf("a second supervisor must be refused, got %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a requested stop is not an error: %v", err)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("the supervisor did not stop the engine")
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
	if err := eng.adjustFreshConfig(41234); err != nil {
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
	if err := eng.adjustFreshConfig(41234); err == nil || !strings.Contains(err.Error(), "unexpected shape") {
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
	f := syncthingFolder("vs-1", "Notes", "/v")
	f.Devices = append(f.Devices, folderDeviceOf("ME"), folderDeviceOf(hub))
	cases := []struct {
		sum       folderSummary
		connected bool
		want      string
	}{
		{folderSummary{State: "idle"}, true, "up to date"},
		{folderSummary{State: "idle"}, false, "waiting for your Hub"},
		{folderSummary{State: "syncing", GlobalBytes: 200, InSyncBytes: 50}, true, "syncing — 25 %"},
		{folderSummary{State: "scanning"}, true, "scanning the folder"},
		{folderSummary{State: "error", Error: "folder path missing"}, true, "error: folder path missing"},
		{folderSummary{State: "idle", Error: "open /v: operation not permitted"}, true, "Privacy & Security"},
		{folderSummary{State: "idle", NeedTotal: 3}, true, "3 items left to sync"},
	}
	for _, c := range cases {
		if got := describeFolder(f, c.sum, conns(c.connected), "ME"); !strings.Contains(got, c.want) {
			t.Errorf("%+v connected=%v: %q, want %q", c.sum, c.connected, got, c.want)
		}
	}
	f.Paused = true
	if got := describeFolder(f, folderSummary{State: "idle"}, conns(true), "ME"); got != "paused" {
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
