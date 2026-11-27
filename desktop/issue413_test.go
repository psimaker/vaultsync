package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// setup chooses the engine's API port before the engine starts (#413): the
// recorded one, unless another program holds it while no vaultsync run
// does. A run's own engine holds its port, and the port stays — told by the
// real engine lock, never by asking the port.
func TestIssue413_SetupTakesAFreePortOnlyWhereAnotherProgramHoldsTheRecordedOne(t *testing.T) {
	for _, c := range []struct {
		name          string
		held, running bool
		moves         bool
	}{
		{"another program holds the recorded port", true, false, true},
		{"a running vaultsync's engine holds it", true, true, false},
		{"the recorded port is free", false, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			eng, st := engineFixture(t)
			recordedPort := st.GUIPort
			if c.held {
				l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(recordedPort))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { l.Close() })
			}
			if c.running {
				unlock, err := lockFile(eng.lay.Lock)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(unlock)
			}
			got, err := apiPortForSetup(eng.lay, recordedPort)
			if err != nil {
				t.Fatal(err)
			}
			if (got != recordedPort) != c.moves || got <= 0 || c.moves && !portFree(got) {
				t.Fatalf("recorded %d (held %v, a run %v): setup takes %d", recordedPort, c.held, c.running, got)
			}
		})
	}
}

// setup over a record whose API port another program holds, with the real
// engine (#413): setup waits for the engine where it runs, never at a port
// another program holds, and agent.json follows the port the engine got.
// Both ways the record outlives its engine's port: the engine's folder
// moved aside (#276's way out), and an engine that was stopped while
// another program took its port.
func TestIssue413_E2ESetupOverARecordedPortAnotherProgramHolds(t *testing.T) {
	archivePath := os.Getenv("VAULTSYNC_DESKTOP_SYNCTHING_ARCHIVE")
	if archivePath == "" {
		t.Skip("set VAULTSYNC_DESKTOP_SYNCTHING_ARCHIVE to the pinned Syncthing archive for this platform")
	}
	data, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name         string
		engineFolder bool
	}{
		{"the engine's folder moved aside", false},
		{"the engine's folder there", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			tmp, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			home := filepath.Join(tmp, "home")
			lay, err := layoutFor("linux", home, envOf(nil))
			if err != nil {
				t.Fatal(err)
			}
			// The pinned engine, installed and recorded as a setup leaves it.
			in, err := newInstaller(lay.Bin, runtime.GOOS, runtime.GOARCH)
			if err != nil {
				t.Fatal(err)
			}
			in.baseURL = serveArchive(t, in.pin.archive, data).URL
			got, err := in.install(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			port, err := freeLoopbackPort()
			if err != nil {
				t.Fatal(err)
			}
			st := agentState{GUIPort: port}
			st.Syncthing.Version, st.Syncthing.BinarySHA256 = syncthingVersion, got.SHA256
			env := envOf(nil)
			a := &app{goos: "linux", home: home, getenv: env, lay: lay, out: io.Discard,
				svc: service{goos: "linux", home: home, getenv: env, lay: lay, run: &fakeRunner{}}}
			// Every engine this test runs stays on loopback.
			listenPort := e2eFreePort(t)
			saved := engineOptionsFor
			engineOptionsFor = func(bool) engineOptions { return engineOptions{loopbackOnly: true, listenPort: listenPort} }
			t.Cleanup(func() { engineOptionsFor = saved })
			eng := a.engine()
			if c.engineFolder {
				if err := eng.prepare(ctx, &st); err != nil {
					t.Fatal(err)
				}
			}
			if err := saveState(lay.State, st); err != nil {
				t.Fatal(err)
			}
			// Another program takes the recorded port while no engine runs.
			var asked atomic.Int32
			held, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(st.GUIPort))
			if err != nil {
				t.Fatal(err)
			}
			other := &httptest.Server{Listener: held, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				asked.Add(1)
				http.NotFound(w, r)
			})}}
			other.Start()
			t.Cleanup(other.Close)

			start := time.Now()
			out, err := setupFrom(t, a, "--no-service")
			t.Logf("setup took %s and ended with: %v", time.Since(start).Round(time.Second), err)
			// The engine answered where setup waited: setup went on to the
			// Hub, which this test leaves unreachable.
			if !strings.Contains(refusalText(err), "Your Hub did not answer") {
				t.Errorf("setup over a recorded port another program holds: %v\n%s", err, out)
			}
			if n := asked.Load(); n > 0 {
				t.Errorf("setup asked the program that holds the recorded port %d times", n)
			}
			after, err := loadState(lay.State)
			if err != nil {
				t.Fatal(err)
			}
			if after.GUIPort == port || after.GUIPort <= 0 {
				t.Errorf("agent.json records port %d after setup; another program holds %d", after.GUIPort, port)
			}
			// The engine stayed on loopback, as every test engine must.
			if cfg, err := os.ReadFile(filepath.Join(lay.Home, "config.xml")); err != nil || !strings.Contains(string(cfg), "<listenAddress>tcp://127.0.0.1:"+strconv.Itoa(listenPort)+"</listenAddress>") || !strings.Contains(string(cfg), "<globalAnnounceEnabled>false</globalAnnounceEnabled>") {
				t.Errorf("the test's engine was not pinned to loopback (%v)", err)
			}
			if !c.engineFolder {
				if inConfig, err := eng.guiPortFromConfig(); err != nil || inConfig != after.GUIPort {
					t.Errorf("the fresh engine's config names %d (%v), agent.json %d", inConfig, err, after.GUIPort)
				}
			}
		})
	}
}
