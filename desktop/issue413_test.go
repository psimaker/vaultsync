package main

import (
	"bytes"
	"context"
	"errors"
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
	"sync"
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
	a, asked, _ := aProgramAtTheRecordedPort(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ping":"pong"}`)
	})
	return a, asked
}

// aProgramAtTheRecordedPort is anotherProgramAtTheRecordedPort answering
// with h, and its server.
func aProgramAtTheRecordedPort(t *testing.T, h http.HandlerFunc) (*app, *atomic.Int32, *httptest.Server) {
	t.Helper()
	var asked atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		h(w, r)
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
		svc: service{goos: "linux", home: home, getenv: envOf(nil), lay: lay, run: &fakeRunner{}}}, &asked, other
}

// vaultsync pair, and status, pause and resume where no agent answers, ask
// the engine's port only while a lock says an engine of VaultSync's may run
// (#413, decision 079): a run holds the engine lock, or, with that lock
// free, a Syncthing that outlived its run holds the engine's home. A run
// that holds the engine lock while no agent answers is on its way up, and
// nothing is asked unless it recorded that it could not make its control
// socket (#419, decision 086). With the engine lock held, Syncthing's lock
// is not looked at: a link in its place changes nothing. With neither held,
// nothing is asked; a lock that cannot be looked at asks nothing either.
func TestIssue413_CommandsWithoutAnAgentAskOnlyARunningEngine(t *testing.T) {
	saved := pairEngineWait
	pairEngineWait = 300 * time.Millisecond
	t.Cleanup(func() { pairEngineWait = saved })
	type command struct {
		name string
		run  func(a *app) (string, error)
		// what each says with no lock held, with a lock it cannot look at,
		// and beside a run on its way up
		notRunning, notKnown, starting string
	}
	commands := []command{
		{"pair", func(a *app) (string, error) {
			err := a.pair(context.Background(), []string{"--code", "TULIP-ANCHOR-42", "--hub", "127.0.0.1:1"})
			return a.out.(*bytes.Buffer).String(), err
		}, "The sync engine is not running", "could not tell whether its sync engine runs", "VaultSync is still starting its sync engine"},
		{"status", func(a *app) (string, error) {
			err := a.status(context.Background())
			return a.out.(*bytes.Buffer).String(), err
		}, "Sync engine: not running", "Sync engine: not known", startingLine},
		{"pause", func(a *app) (string, error) {
			err := a.pauseSync(context.Background(), true)
			return a.out.(*bytes.Buffer).String(), err
		}, "nothing syncs while it is not running", "could not confirm whether the sync engine runs", "still starting its sync engine, and nothing was paused"},
		{"resume", func(a *app) (string, error) {
			err := a.pauseSync(context.Background(), false)
			return a.out.(*bytes.Buffer).String(), err
		}, "vaultsync start resumes the background service", "could not confirm whether the sync engine runs", "still starting its sync engine, and nothing was resumed"},
	}
	home := func(lay layout) string { return filepath.Join(lay.Home, syncthingHomeLock) }
	noSocket := func(t *testing.T, lay layout) {
		writeFile(t, lay.SocketErrorFile(), "listen unix: bind: address already in use\n")
	}
	locks := []struct {
		name  string
		set   func(t *testing.T, lay layout)
		asked bool
		says  func(c command) string
	}{
		{"no lock held", func(*testing.T, layout) {}, false, func(c command) string { return c.notRunning }},
		{"a run holds the engine lock", func(t *testing.T, lay layout) { holdLock(t, lay.Lock) }, false, func(c command) string { return c.starting }},
		{"a run holds the engine lock, a link in place of Syncthing's", func(t *testing.T, lay layout) {
			holdLock(t, lay.Lock)
			linkInPlaceOf(t, home(lay))
		}, false, func(c command) string { return c.starting }},
		{"a run without its control socket holds the engine lock", func(t *testing.T, lay layout) {
			holdLock(t, lay.Lock)
			noSocket(t, lay)
		}, true, nil},
		{"a run without its control socket holds the engine lock, a link in place of Syncthing's", func(t *testing.T, lay layout) {
			holdLock(t, lay.Lock)
			noSocket(t, lay)
			linkInPlaceOf(t, home(lay))
		}, true, nil},
		{"a Syncthing that outlived its run holds its home", func(t *testing.T, lay layout) { holdLock(t, home(lay)) }, true, nil},
		{"the engine lock cannot be looked at", func(t *testing.T, lay layout) { linkInPlaceOf(t, lay.Lock) }, false, func(c command) string { return c.notKnown }},
		{"Syncthing's home lock cannot be looked at, the engine lock free", func(t *testing.T, lay layout) { linkInPlaceOf(t, home(lay)) }, false, func(c command) string { return c.notKnown }},
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
					if want := l.says(c); !strings.Contains(said, want) {
						t.Fatalf("want %q in:\n%s", want, said)
					}
				}
				// Not known is not stopped: no vault is called so.
				if l.says != nil && l.says(c) == c.notKnown && strings.Contains(said, "the engine is stopped") {
					t.Fatalf("not known, but called stopped:\n%s", said)
				}
			})
		}
	}
}

// linkInPlaceOf puts a link where the lock at path goes: a lock that cannot
// be looked at (lock.Probe opens it with O_NOFOLLOW).
func linkInPlaceOf(t *testing.T, path string) {
	t.Helper()
	target := filepath.Join(t.TempDir(), "elsewhere")
	writeFile(t, target, "")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

// pair waits for a run to take the engine lock, then asks the port the run
// records: a run moves off a recorded port another program holds as it
// takes that lock, and the old port is never asked (#413). The run's agent
// says its engine runs once the run has recorded its port, as a run's
// socket answers only after that (#419).
func TestIssue413_PairWaitsForARunAndAsksThePortItRecords(t *testing.T) {
	a, other := anotherProgramAtTheRecordedPort(t)
	var running atomic.Bool
	agentSaying(t, a, func() controlStatus {
		if running.Load() {
			return controlStatus{Engine: "running"}
		}
		return controlStatus{Engine: "starting"}
	})
	var engineAsked atomic.Int32
	engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		engineAsked.Add(1)
		_, _ = io.WriteString(w, `{"ping":"pong"}`)
	}))
	t.Cleanup(engine.Close)
	_, p, _ := net.SplitHostPort(strings.TrimPrefix(engine.URL, "http://"))
	moved, _ := strconv.Atoi(p)
	// The run, in its order: state.lock, the engine lock, the moved port's
	// record, then state.lock let go (#419). Its take of the engine lock is
	// retried, as a look holds it for an instant. It acts between two of
	// pair's looks, 200 ms apart.
	held := make(chan func(), 1)
	go func() {
		time.Sleep(300 * time.Millisecond)
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			unlockState, err := lockFileWait(stateLockOf(a.lay.State))
			if err != nil {
				break
			}
			if unlock, err := lockFile(a.lay.Lock); err == nil {
				_ = updateStateHeld(a.lay.State, func(s *agentState) { s.GUIPort = moved })
				unlockState()
				running.Store(true)
				held <- unlock
				return
			}
			unlockState()
		}
		held <- func() {}
	}()
	t.Cleanup(func() {
		select {
		case unlock := <-held:
			unlock()
		case <-time.After(10 * time.Second):
		}
	})
	start := time.Now()
	err := a.pair(context.Background(), []string{"--code", "TULIP-ANCHOR-42", "--hub", "127.0.0.1:1"})
	took := time.Since(start)
	if took < 300*time.Millisecond || other.Load() > 0 || engineAsked.Load() == 0 || strings.Contains(refusalText(err), "not running") {
		t.Fatalf("pair while a run starts: took %s, the old port asked %d, the engine %d: %v", took.Round(time.Millisecond), other.Load(), engineAsked.Load(), err)
	}
}

// A service that runs but whose run never takes a lock (parked: an older
// build than the one set up, or engine data without its engine) is named
// after pair's wait: vaultsync start would change nothing (#413).
func TestIssue413_PairSaysWhenTheServiceRunsButItsEngineDoesNot(t *testing.T) {
	saved := pairEngineWait
	pairEngineWait = 300 * time.Millisecond
	t.Cleanup(func() { pairEngineWait = saved })
	a, other := anotherProgramAtTheRecordedPort(t)
	unit, err := a.svc.unitPath()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, unit, "[Service]\n")
	a.svc.run.(*fakeRunner).answers = map[string]string{"systemctl --user is-active vaultsync.service": "active"}
	err = a.pair(context.Background(), []string{"--code", "TULIP-ANCHOR-42", "--hub", "127.0.0.1:1"})
	if !strings.Contains(refusalText(err), "The background service runs, but its sync engine does not") || other.Load() > 0 {
		t.Fatalf("pair beside a service whose engine does not run: asked %d, %v", other.Load(), err)
	}
}

// A run leaves a recorded port another program holds as it takes the engine
// lock, before anything slow: while its pin's download still blocks,
// agent.json already names the port its engine will answer on (#413).
func TestIssue413_ARunLeavesATakenPortBeforeItsPinDownload(t *testing.T) {
	a, _ := runFixture(t)
	olderEngineHere(t, a, "v2.1.6")
	st, err := loadState(a.lay.State)
	if err != nil {
		t.Fatal(err)
	}
	taken := st.GUIPort
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(taken))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	downloading, unblock := make(chan struct{}), make(chan struct{})
	var once sync.Once
	pinServedBy(t, func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(downloading) })
		select {
		case <-unblock:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(unblock) })
	stop := inBackground(t, func(ctx context.Context) error { return a.run(ctx, nil) })
	select {
	case <-downloading:
	case <-time.After(10 * time.Second):
		t.Fatal("the run never began the pin's download")
	}
	now, err := loadState(a.lay.State)
	if err != nil || now.GUIPort == taken || now.GUIPort <= 0 {
		t.Fatalf("while the download blocks, agent.json names port %d (%v); another program holds %d", now.GUIPort, err, taken)
	}
	_ = stop()
}

// A look at the engine lock holds it for an instant: a run, and the
// supervisor setup starts, that meet it held wait for it rather than take
// it for another run's (#413). The grace is lengthened here, and the lock
// held well inside it: neither may give up while it is held.
func TestIssue413_ARunTakesTheEngineLockALookHoldsForAnInstant(t *testing.T) {
	saved := engineLockGrace
	engineLockGrace = 3 * time.Second
	t.Cleanup(func() { engineLockGrace = saved })
	heldThenLetGo := func(t *testing.T, lay layout, start func(ctx context.Context) error, reached func() bool) {
		t.Helper()
		unlock, err := lockFile(lay.Lock)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- start(ctx) }()
		t.Cleanup(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(30 * time.Second):
			}
		})
		e2eWait(t, "the run to reach the engine lock", 10*time.Second, reached)
		time.Sleep(300 * time.Millisecond)
		select {
		case err := <-done:
			t.Fatalf("it ended while the lock was held: %v", err)
		default:
		}
		unlock()
		e2eWait(t, "the engine to start", 10*time.Second, func() bool { return len(fakeEnginePIDs(t, lay)) > 0 })
	}
	t.Run("the supervisor", func(t *testing.T) {
		eng, st := engineFixture(t)
		fakeEngineBinary(t, eng.lay)
		heldThenLetGo(t, eng.lay, func(ctx context.Context) error { return eng.supervise(ctx, st, t.Logf) }, func() bool { return true })
	})
	t.Run("a run", func(t *testing.T) {
		a, _ := runFixture(t)
		// The run holds setup.lock up to its look at the engine lock.
		heldThenLetGo(t, a.lay, func(ctx context.Context) error { return a.run(ctx, nil) }, func() bool {
			held, err := lock.Probe(filepath.Join(a.lay.Base, "setup.lock"))
			return err == nil && held
		})
	})
}

// takeEngineLock waits engineLockGrace for a lock a look holds for an
// instant, and takes a lock still held after it for another run's (#413).
func TestIssue413_TakeEngineLockWaitsOnlyItsGrace(t *testing.T) {
	saved := engineLockGrace
	t.Cleanup(func() { engineLockGrace = saved })
	path := filepath.Join(t.TempDir(), "agent.lock")
	t.Run("a look lets go inside the grace", func(t *testing.T) {
		engineLockGrace = 3 * time.Second
		unlock, err := lockFile(path)
		if err != nil {
			t.Fatal(err)
		}
		time.AfterFunc(100*time.Millisecond, unlock)
		start := time.Now()
		release, err := takeEngineLock(path)
		if err != nil {
			t.Fatalf("after %s: %v", time.Since(start).Round(time.Millisecond), err)
		}
		release()
	})
	t.Run("another run holds it", func(t *testing.T) {
		engineLockGrace = 300 * time.Millisecond
		unlock, err := lockFile(path)
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		start := time.Now()
		if _, err := takeEngineLock(path); !errors.Is(err, ErrEngineRunning) {
			t.Fatalf("a lock held throughout: %v", err)
		}
		if took := time.Since(start); took < engineLockGrace || took > engineLockGrace+time.Second {
			t.Fatalf("refused after %s, the grace is %s", took.Round(time.Millisecond), engineLockGrace)
		}
	})
}

// With a lock held, pair says what the engine does when it does not answer
// (#413): nothing listening yet is a run still starting; an answer that is
// no engine's, beside a run that could not make its control socket and
// is asked as before (#419), is said as the engine not answering; a Ctrl-C
// ends the wait.
func TestIssue413_PairSaysWhatTheEngineDoesWhenItDoesNotAnswer(t *testing.T) {
	saved := pairEngineWait
	pairEngineWait = 300 * time.Millisecond
	t.Cleanup(func() { pairEngineWait = saved })
	pair := func(ctx context.Context, a *app) error {
		return a.pair(ctx, []string{"--code", "TULIP-ANCHOR-42", "--hub", "127.0.0.1:1"})
	}
	t.Run("nothing listens yet", func(t *testing.T) {
		a, _, other := aProgramAtTheRecordedPort(t, func(http.ResponseWriter, *http.Request) {})
		other.Close()
		holdLock(t, a.lay.Lock)
		if err := pair(context.Background(), a); !strings.Contains(refusalText(err), "still starting its sync engine") {
			t.Fatalf("nothing listens: %v", err)
		}
	})
	t.Run("an answer that is no engine's", func(t *testing.T) {
		a, _, _ := aProgramAtTheRecordedPort(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "forbidden", http.StatusForbidden) })
		holdLock(t, a.lay.Lock)
		writeFile(t, a.lay.SocketErrorFile(), "listen unix: bind: address already in use\n")
		if err := pair(context.Background(), a); !strings.Contains(refusalText(err), "The sync engine does not answer") || strings.Contains(refusalText(err), "still starting") {
			t.Fatalf("a refused answer: %v", err)
		}
	})
	t.Run("a Ctrl-C", func(t *testing.T) {
		a, _, other := aProgramAtTheRecordedPort(t, func(http.ResponseWriter, *http.Request) {})
		other.Close()
		holdLock(t, a.lay.Lock)
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(500*time.Millisecond, cancel)
		if err := pair(ctx, a); !errors.Is(err, context.Canceled) {
			t.Fatalf("a Ctrl-C during the wait: %v", err)
		}
	})
}

// The port is the one agent.json records when the engine is asked. A record
// that, read again then, names no port leaves nothing to ask, and never the
// port read before (#413).
func TestIssue413_ARecordReadAgainWithoutAPortAsksNothing(t *testing.T) {
	a, asked := anotherProgramAtTheRecordedPort(t)
	holdLock(t, a.lay.Lock)
	// A run without its control socket: one on its way up would be asked
	// nothing anyway (#419).
	writeFile(t, a.lay.SocketErrorFile(), "listen unix: bind: address already in use\n")
	before, err := loadState(a.lay.State)
	if err != nil {
		t.Fatal(err)
	}
	if err := updateState(a.lay.State, func(s *agentState) { s.GUIPort = 0 }); err != nil {
		t.Fatal(err)
	}
	err = a.noAgentForPause(context.Background(), before, true)
	if n := asked.Load(); n > 0 || !strings.Contains(refusalText(err), "could not confirm whether the sync engine runs") {
		t.Fatalf("asked %d times: %v", n, err)
	}
}
