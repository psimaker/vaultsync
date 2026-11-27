package main

import (
	"bytes"
	"context"
	"fmt"
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

	"github.com/psimaker/vaultsync/desktop/internal/lock"
)

// setup chooses the engine's API port before the engine starts (#413): the
// recorded one, unless another program holds it. VaultSync's own engine
// keeps its port — a run's, told by the real engine lock, and one that
// outlived its run, told by the lock Syncthing holds on its home — and so
// does a lock that cannot be looked at. Never by asking the port.
func TestIssue413_SetupTakesAFreePortOnlyWhereAnotherProgramHoldsTheRecordedOne(t *testing.T) {
	for _, c := range []struct {
		name  string
		held  bool
		lock  func(t *testing.T, lay layout)
		moves bool
	}{
		{"another program holds the recorded port", true, nil, true},
		{"a running vaultsync's engine holds it", true, func(t *testing.T, lay layout) { holdLock(t, lay.Lock) }, false},
		{"an engine that outlived its run holds it", true, func(t *testing.T, lay layout) { holdLock(t, filepath.Join(lay.Home, syncthingHomeLock)) }, false},
		{"the engine lock cannot be looked at", true, func(t *testing.T, lay layout) {
			// O_NOFOLLOW: a lock that is a link cannot be opened.
			target := filepath.Join(t.TempDir(), "elsewhere")
			writeFile(t, target, "")
			if err := os.MkdirAll(filepath.Dir(lay.Lock), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, lay.Lock); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"the recorded port is free", false, nil, false},
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
			if c.lock != nil {
				c.lock(t, eng.lay)
			}
			got, err := apiPortForSetup(eng.lay, recordedPort)
			if err != nil {
				t.Fatal(err)
			}
			if (got != recordedPort) != c.moves || got <= 0 || c.moves && !portFree(got) {
				t.Fatalf("recorded %d (held %v): setup takes %d", recordedPort, c.held, got)
			}
		})
	}
}

// holdLock holds the lock at path for the rest of the test, making it.
func holdLock(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlock)
}

// setup over a record whose API port another program holds, with the real
// engine (#413): setup waits for the engine where it runs, never at a port
// another program holds, and agent.json follows the port the engine got.
// The ways the record outlives its engine's port: the engine's folder
// moved aside (#276's way out), an engine that was stopped while another
// program took its port, and a record without a port beside the engine's
// folder, whose config names it.
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
		name            string
		engineFolder    bool
		portInTheRecord bool
	}{
		{"the engine's folder moved aside", false, true},
		{"the engine's folder there", true, true},
		{"a record without a port beside the engine's folder", true, false},
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
			// Every engine this test runs stays on loopback, on a sync port
			// outside the range the API port below is taken from (#403).
			listenPort, err := freeSyncPort()
			if err != nil {
				t.Fatal(err)
			}
			saved := engineOptionsFor
			engineOptionsFor = func(bool) engineOptions { return engineOptions{loopbackOnly: true, listenPort: listenPort} }
			t.Cleanup(func() { engineOptionsFor = saved })
			eng := a.engine()
			if c.engineFolder {
				if err := eng.prepare(ctx, &st); err != nil {
					t.Fatal(err)
				}
			}
			recorded := st
			if !c.portInTheRecord {
				recorded.GUIPort = 0
			}
			if err := saveState(lay.State, recorded); err != nil {
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

// The lock setup reads as VaultSync's own engine where no run holds the
// engine lock is the one Syncthing holds on its home while it runs (#413).
// A Syncthing bump that moves it would make setup take such an engine for
// another program: this test fails first.
func TestIssue413_E2EAnEngineLocksItsHome(t *testing.T) {
	archivePath := os.Getenv("VAULTSYNC_DESKTOP_SYNCTHING_ARCHIVE")
	if archivePath == "" {
		t.Skip("set VAULTSYNC_DESKTOP_SYNCTHING_ARCHIVE to the pinned Syncthing archive for this platform")
	}
	data, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	lay, err := layoutFor("linux", t.TempDir(), envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	in, err := newInstaller(lay.Bin, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	in.baseURL = serveArchive(t, in.pin.archive, data).URL
	if _, err := in.install(ctx, nil); err != nil {
		t.Fatal(err)
	}
	listenPort, err := freeSyncPort()
	if err != nil {
		t.Fatal(err)
	}
	eng := engine{lay: lay, opts: engineOptions{loopbackOnly: true, listenPort: listenPort}}
	var st agentState
	if err := eng.prepare(ctx, &st); err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- eng.supervise(runCtx, st, t.Logf) }()
	t.Cleanup(func() { stop(); <-done })
	client, err := eng.client(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitReady(ctx, client, 60*time.Second); err != nil {
		t.Fatal(err)
	}
	if held, err := lock.Probe(filepath.Join(lay.Home, syncthingHomeLock)); err != nil || !held {
		t.Fatalf("the running engine holds no lock on %s in its home: held %v, %v", syncthingHomeLock, held, err)
	}
}

// anotherProgramAtTheRecordedPort is an app set up as setup leaves it, its
// agent not answering, whose recorded API port a program that is not
// VaultSync's engine holds: it answers every request and counts them.
func anotherProgramAtTheRecordedPort(t *testing.T) (*app, *atomic.Int32) {
	t.Helper()
	var asked atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		_, _ = io.WriteString(w, `{"ping":"pong"}`)
	}))
	t.Cleanup(other.Close)
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tmp, "home")
	lay, err := layoutFor("linux", home, envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	addr := strings.TrimPrefix(other.URL, "http://")
	_, port, _ := net.SplitHostPort(addr)
	n, _ := strconv.Atoi(port)
	writeFile(t, filepath.Join(lay.Home, "config.xml"), "<configuration><gui><address>"+addr+"</address><apikey>engine-key</apikey></gui></configuration>\n")
	writeFile(t, lay.Syncthing, "the engine")
	if err := saveState(lay.State, recorded(agentState{GUIPort: n, Env: map[string]string{}})); err != nil {
		t.Fatal(err)
	}
	lay.Socket = filepath.Join(shortDir(t), "agent.sock")
	return &app{goos: "linux", home: home, getenv: envOf(nil), lay: lay, out: &bytes.Buffer{},
		svc: service{goos: "linux", home: home, getenv: envOf(nil), lay: lay, run: &fakeRunner{}}}, &asked
}

// A command that finds no agent asks the engine's recorded port only while
// an engine of VaultSync's may run there: a run holds the engine lock, or a
// Syncthing holds its lock on the engine's home (#413, decision 079). With
// neither, it says the engine is not running, and nothing is asked; a lock
// that cannot be looked at asks nothing either. With a lock held — an
// older agent without a control socket — the engine is asked as before.
func TestIssue413_CommandsWithoutAnAgentAskOnlyARunningEngine(t *testing.T) {
	commands := []struct {
		name string
		run  func(a *app) (string, error)
		// what each says with no lock held, and with a lock it cannot look at
		notRunning, notKnown string
	}{
		{"pair", func(a *app) (string, error) {
			err := a.pair(context.Background(), []string{"--code", "TULIP-ANCHOR-42", "--hub", "127.0.0.1:1"})
			return a.out.(*bytes.Buffer).String(), err
		}, "The sync engine is not running", "could not tell whether its sync engine runs"},
		{"status", func(a *app) (string, error) {
			err := a.status(context.Background())
			return a.out.(*bytes.Buffer).String(), err
		}, "Sync engine: not running", "Sync engine: not known"},
		{"pause", func(a *app) (string, error) {
			err := a.pauseSync(context.Background(), true)
			return a.out.(*bytes.Buffer).String(), err
		}, "nothing syncs while it is not running", "could not confirm whether the sync engine runs"},
		{"resume", func(a *app) (string, error) {
			err := a.pauseSync(context.Background(), false)
			return a.out.(*bytes.Buffer).String(), err
		}, "vaultsync start resumes the background service", "could not confirm whether the sync engine runs"},
	}
	locks := []struct {
		name  string
		set   func(t *testing.T, lay layout)
		asked bool
		says  func(notRunning, notKnown string) string
	}{
		{"no lock held", func(*testing.T, layout) {}, false, func(nr, _ string) string { return nr }},
		{"a run holds the engine lock", func(t *testing.T, lay layout) { holdLock(t, lay.Lock) }, true, nil},
		{"a Syncthing holds its home", func(t *testing.T, lay layout) { holdLock(t, filepath.Join(lay.Home, syncthingHomeLock)) }, true, nil},
		{"the engine lock cannot be looked at", func(t *testing.T, lay layout) {
			target := filepath.Join(t.TempDir(), "elsewhere")
			writeFile(t, target, "")
			if err := os.MkdirAll(filepath.Dir(lay.Lock), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, lay.Lock); err != nil {
				t.Fatal(err)
			}
		}, false, func(_, nk string) string { return nk }},
	}
	for _, c := range commands {
		for _, l := range locks {
			t.Run(c.name+", "+l.name, func(t *testing.T) {
				a, asked := anotherProgramAtTheRecordedPort(t)
				l.set(t, a.lay)
				out, err := c.run(a)
				said := out + refusalText(err) + fmt.Sprint(err)
				if n := asked.Load(); (n > 0) != l.asked {
					t.Fatalf("the program at the recorded port was asked %d times:\n%s", n, said)
				}
				if l.says != nil {
					if want := l.says(c.notRunning, c.notKnown); !strings.Contains(said, want) {
						t.Fatalf("want %q in:\n%s", want, said)
					}
				}
			})
		}
	}
}
