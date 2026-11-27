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
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/psimaker/vaultsync/desktop/internal/lock"
	"github.com/psimaker/vaultsync/hub/syncthing"
)

// A run whose engine lock is free, beside a Syncthing that holds the lock
// on the engine's home and the recorded API port, keeps that port: the
// engine that outlived its run is VaultSync's own, told by Syncthing's
// lock as setup tells it (#419, decisions 079 and 086). So does a run
// whose look at that lock fails. A port another program holds, with no
// Syncthing on the home, is still left.
func TestIssue419_ARunKeepsThePortOfAnEngineThatOutlivedItsRun(t *testing.T) {
	home := func(lay layout) string { return filepath.Join(lay.Home, syncthingHomeLock) }
	for _, c := range []struct {
		name  string
		set   func(t *testing.T, lay layout)
		keeps bool
	}{
		{"Syncthing holds the engine's home", func(t *testing.T, lay layout) { holdLock(t, home(lay)) }, true},
		{"Syncthing's lock cannot be looked at", func(t *testing.T, lay layout) { linkInPlaceOf(t, home(lay)) }, true},
		{"no Syncthing on the engine's home", func(*testing.T, layout) {}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			eng, st := engineFixture(t)
			port := st.GUIPort
			if err := saveState(eng.lay.State, st); err != nil {
				t.Fatal(err)
			}
			held, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { held.Close() })
			c.set(t, eng.lay)
			if err := eng.leaveATakenPort(&st, t.Logf); err != nil {
				t.Fatal(err)
			}
			after, err := loadState(eng.lay.State)
			if err != nil {
				t.Fatal(err)
			}
			if kept := st.GUIPort == port && after.GUIPort == port; kept != c.keeps {
				t.Fatalf("port %d held: the run took %d and agent.json records %d", port, st.GUIPort, after.GUIPort)
			}
		})
	}
}

// fakeEngineAt is newFakeEngine answering on 127.0.0.1:port, with the API
// key the agent reads from the engine's config.xml set to the fake's.
func fakeEngineAt(t *testing.T, lay layout, port int) (*fakeEngine, *httptest.Server) {
	t.Helper()
	e := &fakeEngine{
		myID:      testMyID,
		startTime: "2026-10-06T10:00:00Z",
		devices:   []syncthing.DeviceConfig{{DeviceID: testMyID, Name: "laptop"}},
		pending:   map[string]map[string]string{},
	}
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	srv := &httptest.Server{Listener: l, Config: &http.Server{Handler: http.HandlerFunc(e.serve)}}
	srv.Start()
	t.Cleanup(srv.Close)
	e.addr = l.Addr().String()
	e.client = syncthing.NewClient(srv.URL, "engine-key")
	cfg := filepath.Join(lay.Home, "config.xml")
	data, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, cfg, strings.Replace(string(data), "<apikey>k</apikey>", "<apikey>engine-key</apikey>", 1))
	return e, srv
}

// statusBeside is what vaultsync status prints while a's run runs: asked
// from a copy of a with a fake service manager of its own, since the run
// asks a's at the same time.
func statusBeside(a *app) string {
	return withCopy(a, func(b *app) error { return b.status(context.Background()) })
}

// withCopy runs cmd on a copy of a with its own output and its own fake
// service manager, and returns what it printed and the refusal it ended
// with.
func withCopy(a *app, cmd func(b *app) error) string {
	b := *a
	var out bytes.Buffer
	b.out = &out
	b.svc.run = &fakeRunner{}
	err := cmd(&b)
	return out.String() + refusalText(err)
}

// A run beside an engine an earlier run left (#419, decision 086): it keeps
// the recorded port, starts no engine while Syncthing's lock on the home is
// held, and status says what is true — that engine answers and syncs, and
// VaultSync starts its own once it has ended. Once it has, the run starts
// its own on the same port. setup's engine, without a socket, waits alike.
func TestIssue419_ARunWaitsForAnEngineAnEarlierRunLeft(t *testing.T) {
	saved := earlierEngineLook
	earlierEngineLook = 50 * time.Millisecond
	t.Cleanup(func() { earlierEngineLook = saved })
	t.Run("a run", func(t *testing.T) {
		a, _ := runFixture(t)
		st, err := loadState(a.lay.State)
		if err != nil {
			t.Fatal(err)
		}
		port := st.GUIPort
		_, earlier := fakeEngineAt(t, a.lay, port)
		homeLock, err := lockFile(filepath.Join(a.lay.Home, syncthingHomeLock))
		if err != nil {
			t.Fatal(err)
		}
		let := sync.OnceFunc(homeLock)
		t.Cleanup(let)
		stop := inBackground(t, func(ctx context.Context) error { return a.run(ctx, nil) })
		var said string
		e2eWait(t, "status to say what runs", 15*time.Second, func() bool {
			said = statusBeside(a)
			return strings.Contains(said, "Sync engine: running — the one an earlier vaultsync run left behind")
		})
		if !strings.Contains(said, "VaultSync starts its own engine once it has ended") || !strings.Contains(said, "Hub") {
			t.Fatalf("status while the run waits:\n%s", said)
		}
		if strings.Contains(said, "the installed one is") {
			t.Fatalf("status names a Syncthing version for an engine this run did not start:\n%s", said)
		}
		time.Sleep(300 * time.Millisecond)
		if pids := fakeEnginePIDs(t, a.lay); len(pids) > 0 {
			t.Fatalf("the run started an engine (%v) while another held the engine's home", pids)
		}
		if now, err := loadState(a.lay.State); err != nil || now.GUIPort != port {
			t.Fatalf("agent.json records port %d (%v); the engine an earlier run left answers on %d", now.GUIPort, err, port)
		}
		// The earlier engine ends: its port and its lock go.
		earlier.Close()
		let()
		e2eWait(t, "the run to start its own engine", 10*time.Second, func() bool { return len(fakeEnginePIDs(t, a.lay)) > 0 })
		pid := fakeEnginePIDs(t, a.lay)[0]
		args, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
		if err != nil || !strings.Contains(string(args), "--gui-address=http://127.0.0.1:"+strconv.Itoa(port)+" ") {
			t.Fatalf("the run's engine runs as %q (%v), not on port %d", args, err, port)
		}
		if said := statusBeside(a); strings.Contains(said, "earlier vaultsync run") {
			t.Fatalf("status once the run's own engine runs:\n%s", said)
		}
		if err := stop(); err != nil {
			t.Fatalf("the run ended with %v", err)
		}
	})
	t.Run("setup's engine", func(t *testing.T) {
		eng, st := engineFixture(t)
		fakeEngineBinary(t, eng.lay)
		homeLock, err := lockFile(filepath.Join(eng.lay.Home, syncthingHomeLock))
		if err != nil {
			t.Fatal(err)
		}
		let := sync.OnceFunc(homeLock)
		t.Cleanup(let)
		stop := inBackground(t, func(ctx context.Context) error { return eng.supervise(ctx, st, t.Logf) })
		time.Sleep(500 * time.Millisecond)
		if pids := fakeEnginePIDs(t, eng.lay); len(pids) > 0 {
			t.Fatalf("setup's engine started (%v) while another held the engine's home", pids)
		}
		let()
		e2eWait(t, "setup's engine to start", 10*time.Second, func() bool { return len(fakeEnginePIDs(t, eng.lay)) > 0 })
		if err := stop(); err != nil {
			t.Fatalf("setup's engine ended with %v", err)
		}
	})
	t.Run("a run stopped while it waits", func(t *testing.T) {
		eng, st := engineFixture(t)
		fakeEngineBinary(t, eng.lay)
		holdLock(t, filepath.Join(eng.lay.Home, syncthingHomeLock))
		stop := inBackground(t, func(ctx context.Context) error { return eng.supervise(ctx, st, t.Logf) })
		time.Sleep(200 * time.Millisecond)
		start := time.Now()
		if err := stop(); err != nil || time.Since(start) > 2*time.Second {
			t.Fatalf("stopped while it waits: %v after %s", err, time.Since(start).Round(time.Millisecond))
		}
		if pids := fakeEnginePIDs(t, eng.lay); len(pids) > 0 {
			t.Fatalf("an engine started (%v)", pids)
		}
	})
}

// vaultsync start on a Mac, while the service's run waits for an engine an
// earlier run left (#419, decisions 085 and 086): that run holds the engine
// lock and serves its socket, so start leaves it alone and says it is
// already running, which is true; status says that the earlier engine
// syncs and that VaultSync starts its own once it has ended. The waiting
// run goes on waiting, and starts its engine once the earlier one is gone.
func TestIssue419_StartLeavesARunThatWaitsForAnEarlierEngineAlone(t *testing.T) {
	saved := earlierEngineLook
	earlierEngineLook = 50 * time.Millisecond
	t.Cleanup(func() { earlierEngineLook = saved })
	quickAnswerWait(t)
	asTheService(t)
	a, _ := runFixture(t)
	st, err := loadState(a.lay.State)
	if err != nil {
		t.Fatal(err)
	}
	_, earlier := fakeEngineAt(t, a.lay, st.GUIPort)
	homeLock, err := lockFile(filepath.Join(a.lay.Home, syncthingHomeLock))
	if err != nil {
		t.Fatal(err)
	}
	let := sync.OnceFunc(homeLock)
	t.Cleanup(let)
	stop := inBackground(t, func(ctx context.Context) error { return a.run(ctx, nil) })
	e2eWait(t, "the run to wait beside the earlier engine", 15*time.Second, func() bool {
		return strings.Contains(statusBeside(a), "the one an earlier vaultsync run left behind")
	})

	// The service on a Mac, as launchd shows it: its job loaded from the
	// LaunchAgent, which names VaultSync's own copy, this very build.
	run := &fakeRunner{}
	s := &app{goos: "darwin", home: a.home, getenv: envOf(nil), lay: a.lay, out: &bytes.Buffer{},
		svc: service{goos: "darwin", home: a.home, uid: 501, getenv: envOf(nil), lay: a.lay, run: run, pause: func(time.Duration) {}}}
	data, err := s.svc.unitFile(s.lay.Agent)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, mustUnitPath(t, s.svc), string(data))
	runsAs(t, s, s.lay.Agent, "this build")
	run.loaded, run.loadedProgram = true, s.lay.Agent
	if err := s.startService(); err != nil {
		t.Fatalf("start beside the waiting run: %v\n%s", err, startText(s))
	}
	if calls := strings.Join(run.calls, "\n"); strings.Contains(calls, "launchctl bootout") || strings.Contains(calls, "launchctl bootstrap") {
		t.Fatalf("start loaded the waiting run afresh:\n%s", calls)
	}
	if out := startText(s); out != "✓ VaultSync is already running.\n" {
		t.Fatalf("start beside the waiting run says:\n%s", out)
	}
	if said := statusBeside(a); !strings.Contains(said, "Sync engine: running — the one an earlier vaultsync run left behind; it syncs, and VaultSync starts its own engine once it has ended") {
		t.Fatalf("status after start:\n%s", said)
	}
	if pids := fakeEnginePIDs(t, a.lay); len(pids) > 0 || !engineLockHeld(a.lay) {
		t.Fatalf("after start: engines %v, engine lock held %v", pids, engineLockHeld(a.lay))
	}
	earlier.Close()
	let()
	e2eWait(t, "the run to start its own engine", 10*time.Second, func() bool { return len(fakeEnginePIDs(t, a.lay)) > 0 })
	if err := stop(); err != nil {
		t.Fatalf("the run ended with %v", err)
	}
}

// holdTheRunAtItsEngineLock makes the next take of lay's engine lock hold
// that lock and wait there until the returned release is called: a run is
// held in the instant after its engine lock, before anything it does next.
// held is closed once the lock is taken.
func holdTheRunAtItsEngineLock(t *testing.T, lay layout) (held <-chan struct{}, release func()) {
	t.Helper()
	reached, let := make(chan struct{}), make(chan struct{})
	var once, letOnce sync.Once
	orig := takeLockFile
	takeLockFile = func(path string) (*lock.Handle, error) {
		h, err := orig(path)
		if err == nil && path == lay.Lock {
			once.Do(func() {
				close(reached)
				<-let
			})
		}
		return h, err
	}
	release = func() { letOnce.Do(func() { close(let) }) }
	// Registered before the run's own cleanup: the run is stopped first.
	t.Cleanup(func() { takeLockFile = orig })
	t.Cleanup(release)
	return reached, release
}

// A command that looks while a run has taken the engine lock but not yet
// recorded the port it moves to asks nothing at the port from before the
// move, which another program holds (#419): a run and setup's engine alike.
// The run is held in that instant at its take of the engine lock. Let go
// within the command's wait, the command reads the port the run moved to;
// held past it, the command says it cannot tell and asks nothing.
func TestIssue419_ACommandAsksNoPortARunIsMovingOff(t *testing.T) {
	saved, savedWait := pairEngineWait, stateLockWait
	pairEngineWait, stateLockWait = 300*time.Millisecond, 500*time.Millisecond
	t.Cleanup(func() { pairEngineWait, stateLockWait = saved, savedWait })
	commands := []struct {
		name string
		run  func(a *app) error
		// what it says when it cannot tell whether the engine runs
		notKnown string
	}{
		{"status", func(a *app) error { return a.status(context.Background()) }, "Sync engine: not known"},
		{"pause", func(a *app) error { return a.pauseSync(context.Background(), true) }, "could not confirm whether the sync engine runs"},
		{"resume", func(a *app) error { return a.pauseSync(context.Background(), false) }, "could not confirm whether the sync engine runs"},
		{"pair", func(a *app) error {
			return a.pair(context.Background(), []string{"--code", "TULIP-ANCHOR-42", "--hub", "127.0.0.1:1"})
		}, "could not tell whether its sync engine runs"},
	}
	starters := []struct {
		name  string
		start func(a *app) func(ctx context.Context) error
	}{
		{"a run", func(a *app) func(ctx context.Context) error {
			return func(ctx context.Context) error { return a.run(ctx, nil) }
		}},
		{"setup's engine", func(a *app) func(ctx context.Context) error {
			eng := a.engine()
			st, _ := loadState(a.lay.State)
			return func(ctx context.Context) error { return eng.supervise(ctx, st, func(string, ...any) {}) }
		}},
	}
	for _, s := range starters {
		for _, c := range commands {
			for _, letGoWithin := range []bool{true, false} {
				name := s.name + ", " + c.name + ", held past the wait"
				if letGoWithin {
					name = s.name + ", " + c.name + ", let go within the wait"
				}
				t.Run(name, func(t *testing.T) {
					a, _ := runFixture(t)
					st, err := loadState(a.lay.State)
					if err != nil {
						t.Fatal(err)
					}
					// Another program takes the recorded port while no engine runs.
					var asked atomic.Int32
					l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(st.GUIPort))
					if err != nil {
						t.Fatal(err)
					}
					other := &httptest.Server{Listener: l, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						asked.Add(1)
						_, _ = io.WriteString(w, `{"ping":"pong"}`)
					})}}
					other.Start()
					t.Cleanup(other.Close)
					// Held between its engine lock and its record of the port
					// it moves to.
					held, let := holdTheRunAtItsEngineLock(t, a.lay)
					stop := inBackground(t, s.start(a))
					select {
					case <-held:
					case <-time.After(10 * time.Second):
						t.Fatal("the run never took the engine lock")
					}
					done := make(chan string, 1)
					go func() { done <- withCopy(a, c.run) }()
					if letGoWithin {
						time.Sleep(100 * time.Millisecond)
						let()
					}
					var said string
					select {
					case said = <-done:
					case <-time.After(30 * time.Second):
						t.Fatal("the command did not end")
					}
					n := asked.Load()
					let()
					_ = stop()
					if n > 0 {
						t.Fatalf("%s asked the port another program holds %d times:\n%s", c.name, n, said)
					}
					if !letGoWithin && !strings.Contains(said, c.notKnown) {
						t.Fatalf("held past the wait, want %q in:\n%s", c.notKnown, said)
					}
					if letGoWithin {
						if now, err := loadState(a.lay.State); err != nil || now.GUIPort == st.GUIPort {
							t.Fatalf("the run did not record its move: port %d (%v)", now.GUIPort, err)
						}
						if strings.Contains(said, c.notKnown) {
							t.Fatalf("let go within the wait, yet:\n%s", said)
						}
					}
				})
			}
		}
	}
}

// With state.lock held past the wait, by whatever holds it, a command
// cannot tell whether the engine runs, says so, and asks nothing (#419).
func TestIssue419_ACommandMeetingAHeldStateLockAsksNothing(t *testing.T) {
	saved, savedWait := pairEngineWait, stateLockWait
	pairEngineWait, stateLockWait = 300*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { pairEngineWait, stateLockWait = saved, savedWait })
	for _, c := range []struct {
		name, says string
		run        func(a *app) error
	}{
		{"status", "Sync engine: not known — its lock file", func(a *app) error { return a.status(context.Background()) }},
		{"pause", "could not confirm whether the sync engine runs", func(a *app) error { return a.pauseSync(context.Background(), true) }},
		{"pair", "could not tell whether its sync engine runs: its lock file", func(a *app) error {
			return a.pair(context.Background(), []string{"--code", "TULIP-ANCHOR-42", "--hub", "127.0.0.1:1"})
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, asked := anotherProgramAtTheRecordedPort(t)
			holdLock(t, a.lay.Lock)
			holdLock(t, stateLockOf(a.lay.State))
			start := time.Now()
			done := make(chan string, 1)
			go func() { done <- withCopy(a, c.run) }()
			var said string
			select {
			case said = <-done:
			case <-time.After(10 * time.Second):
				t.Fatalf("still waiting for state.lock after %s; the wait is %s", time.Since(start).Round(time.Millisecond), stateLockWait)
			}
			if n := asked.Load(); n > 0 || !strings.Contains(said, c.says) || !strings.Contains(said, "state.lock") {
				t.Fatalf("asked %d times after %s:\n%s", n, time.Since(start).Round(time.Millisecond), said)
			}
		})
	}
}

// A run beside an engine that outlived its run, with the real engine
// (#419, decision 086): the engine's home is locked by that engine, which
// answers on the recorded port. The run neither moves the record off that
// port nor starts a second engine on the same home, and status says that
// engine runs and syncs. Once it has ended, the run starts its own on the
// same port.
func TestIssue419_E2EARunBesideAnEngineThatOutlivedItsRun(t *testing.T) {
	archivePath := os.Getenv("VAULTSYNC_DESKTOP_SYNCTHING_ARCHIVE")
	if archivePath == "" {
		t.Skip("set VAULTSYNC_DESKTOP_SYNCTHING_ARCHIVE to the pinned Syncthing archive for this platform")
	}
	data, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
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
	in, err := newInstaller(lay.Bin, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	in.baseURL = serveArchive(t, in.pin.archive, data).URL
	got, err := in.install(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	listenPort, err := freeSyncPort()
	if err != nil {
		t.Fatal(err)
	}
	saved := engineOptionsFor
	engineOptionsFor = func(bool) engineOptions { return engineOptions{loopbackOnly: true, listenPort: listenPort} }
	t.Cleanup(func() { engineOptionsFor = saved })
	env := envOf(map[string]string{"XDG_RUNTIME_DIR": shortDir(t)})
	a := &app{goos: "linux", home: home, getenv: env, lay: lay, out: io.Discard,
		svc: service{goos: "linux", home: home, getenv: env, lay: lay, run: &fakeRunner{}}}
	eng := a.engine()
	var st agentState
	st.Syncthing.Version, st.Syncthing.BinarySHA256 = syncthingVersion, got.SHA256
	if err := eng.prepare(ctx, &st); err != nil {
		t.Fatal(err)
	}
	if err := saveState(lay.State, st); err != nil {
		t.Fatal(err)
	}
	port := st.GUIPort

	// The engine that outlived its run: started as runOnce starts it, by
	// nothing that supervises it any more.
	outlived := exec.Command(lay.Syncthing, "serve", "--home="+lay.Home,
		"--gui-address=http://127.0.0.1:"+strconv.Itoa(port),
		"--no-browser", "--no-restart", "--no-upgrade",
		"--log-file="+filepath.Join(lay.Home, "syncthing.log"))
	outlived.Env = engineEnv()
	if err := outlived.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = outlived.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = outlived.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			_ = outlived.Process.Kill()
			<-done
		}
	})
	client, err := eng.client(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitReady(ctx, client, 60*time.Second); err != nil {
		t.Fatal(err)
	}
	logBefore, _ := os.ReadFile(filepath.Join(lay.Home, "syncthing.log"))

	saved2 := earlierEngineLook
	earlierEngineLook = 100 * time.Millisecond
	t.Cleanup(func() { earlierEngineLook = saved2 })
	runDone := make(chan error, 1)
	runCtx, stopRun := context.WithCancel(ctx)
	go func() { runDone <- a.run(runCtx, nil) }()
	t.Cleanup(func() {
		stopRun()
		select {
		case <-runDone:
		case <-time.After(30 * time.Second):
			t.Error("the run did not stop")
		}
	})

	// While that engine runs, status says so, and the run starts none.
	var said string
	start := time.Now()
	for {
		if said = statusBeside(a); strings.Contains(said, "earlier vaultsync run") {
			break
		}
		select {
		case err := <-runDone:
			runDone <- err
			t.Fatalf("the run beside it ended after %s with %v; status:\n%s", time.Since(start).Round(100*time.Millisecond), err, said)
		case <-time.After(200 * time.Millisecond):
		}
		if time.Since(start) > 20*time.Second {
			t.Fatalf("status beside the engine that outlived its run:\n%s", said)
		}
	}
	t.Logf("status beside the engine that outlived its run:\n%s", said)
	if !strings.Contains(said, "Sync engine: running — the one an earlier vaultsync run left behind") {
		t.Errorf("status while the run waits:\n%s", said)
	}
	after, err := loadState(lay.State)
	if err != nil {
		t.Fatal(err)
	}
	if after.GUIPort != port {
		t.Errorf("agent.json records port %d; the engine that outlived its run answers on %d", after.GUIPort, port)
	}
	logAfter, _ := os.ReadFile(filepath.Join(lay.Home, "syncthing.log"))
	if second := strings.TrimPrefix(string(logAfter), string(logBefore)); strings.Contains(second, "Failed to acquire lock") {
		t.Errorf("the run started a second engine on the same home, which stopped on Syncthing's lock:\n%s", lastLines(second, 3))
	}

	// That engine ends; the run starts its own, on the same port.
	_ = outlived.Process.Signal(syscall.SIGTERM)
	if err := waitForExit(outlived, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	e2eWait(t, "the run's own engine", 60*time.Second, func() bool {
		pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		return client.Ping(pctx) == nil
	})
	if held, err := lock.Probe(filepath.Join(lay.Home, syncthingHomeLock)); err != nil || !held {
		t.Fatalf("no engine holds the engine's home: %v, %v", held, err)
	}
	if now, err := loadState(lay.State); err != nil || now.GUIPort != port {
		t.Fatalf("agent.json records port %d (%v) once the run's own engine runs; it answers on %d", now.GUIPort, err, port)
	}
	if said := statusBeside(a); !strings.Contains(said, "Sync engine: running") || strings.Contains(said, "earlier vaultsync run") {
		t.Errorf("status once the run's own engine runs:\n%s", said)
	}
}

// waitForExit waits for a started command to exit, at most timeout.
func waitForExit(cmd *exec.Cmd, timeout time.Duration) error {
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("pid %d did not exit within %s", cmd.Process.Pid, timeout)
	}
}

// lastLines is the last n lines of s.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
