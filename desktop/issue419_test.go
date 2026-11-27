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

	"github.com/psimaker/vaultsync/desktop/internal/control"
	"github.com/psimaker/vaultsync/desktop/internal/lock"
	"github.com/psimaker/vaultsync/hub/syncthing"
)

// answeringAtTheRecordedPort answers on 127.0.0.1:port as an engine's ping
// would, and tells how many requests reached it.
func answeringAtTheRecordedPort(t *testing.T, port int) (*atomic.Int32, *httptest.Server) {
	t.Helper()
	var reached atomic.Int32
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	srv := &httptest.Server{Listener: l, Config: &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		_, _ = io.WriteString(w, `{"ping":"pong"}`)
	})}}
	srv.Start()
	t.Cleanup(srv.Close)
	return &reached, srv
}

// statusBeside is what vaultsync status prints while a's run runs: asked
// from a copy of a with a fake service manager of its own, since the run
// asks a's at the same time.
func statusBeside(a *app) string {
	return withCopy(a, func(b *app) error { return b.status(context.Background()) })
}

// withCopy runs cmd on a copy of a with its own output and its own fake
// service manager, and returns what it printed and the error it ended
// with.
func withCopy(a *app, cmd func(b *app) error) string {
	b := *a
	var out bytes.Buffer
	b.out = &out
	b.svc.run = &fakeRunner{}
	if err := cmd(&b); err != nil {
		return out.String() + err.Error()
	}
	return out.String()
}

// A run whose engine lock is free keeps the recorded port only where a
// Syncthing is seen to hold the lock on the engine's home: an engine an
// earlier run left, which the run waits for (#419, decision 086). A lock
// that cannot be looked at keeps nothing, and neither does a home no
// Syncthing holds: a taken port is left, as before.
func TestIssue419_ARunKeepsThePortOfAnEngineThatOutlivedItsRun(t *testing.T) {
	home := func(lay layout) string { return filepath.Join(lay.Home, syncthingHomeLock) }
	for _, c := range []struct {
		name  string
		set   func(t *testing.T, lay layout)
		keeps bool
	}{
		{"Syncthing holds the engine's home", func(t *testing.T, lay layout) { holdLock(t, home(lay)) }, true},
		{"Syncthing's lock cannot be looked at", func(t *testing.T, lay layout) { linkInPlaceOf(t, home(lay)) }, false},
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
				t.Fatalf("port %d taken: the run took %d and agent.json records %d", port, st.GUIPort, after.GUIPort)
			}
		})
	}
}

// untilTheRunWaits waits, through a's run's control socket alone, until the
// run says it waits for an engine an earlier run left behind: nothing is
// asked of the engine's port on the way (status asks it where no agent
// answers yet).
func untilTheRunWaits(t *testing.T, a *app) {
	t.Helper()
	untilTheRunServes(t, a)
	e2eWait(t, "the run to wait beside the earlier engine", 15*time.Second, func() bool {
		cs, err := socketOf(t, a).Status(context.Background())
		return err == nil && cs.EarlierRun
	})
}

// socketOf is the control client of a's run, at the place agent.json
// records.
func socketOf(t *testing.T, a *app) *control.Client {
	t.Helper()
	st, err := loadState(a.lay.State)
	if err != nil {
		t.Fatal(err)
	}
	return a.controlClient(st)
}

// A run beside an engine an earlier run left behind (#419, decision 086):
// it keeps the recorded port, starts no engine while Syncthing's lock on
// the home is seen held, and asks no port at all meanwhile. status and the
// menu's poll say what is known; pause, resume, a pairing through the
// agent and vaultsync pair say the engine is not ready yet. Nothing reaches
// the recorded port. Once that engine has ended, the run starts its own on
// the same port. setup's engine waits alike, and setup asks no port until
// it has started its engine.
func TestIssue419_ARunWaitsForAnEngineAnEarlierRunLeft(t *testing.T) {
	saved, savedPair := earlierEngineLook, pairEngineWait
	earlierEngineLook, pairEngineWait = 50*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { earlierEngineLook, pairEngineWait = saved, savedPair })
	t.Run("a run", func(t *testing.T) {
		a, _ := runFixture(t)
		st, err := loadState(a.lay.State)
		if err != nil {
			t.Fatal(err)
		}
		port := st.GUIPort
		reached, earlier := answeringAtTheRecordedPort(t, port)
		homeLock, err := lockFile(filepath.Join(a.lay.Home, syncthingHomeLock))
		if err != nil {
			t.Fatal(err)
		}
		let := sync.OnceFunc(homeLock)
		t.Cleanup(let)
		stop := inBackground(t, func(ctx context.Context) error { return a.run(ctx, nil) })
		untilTheRunWaits(t, a)
		said := statusBeside(a)
		if !strings.Contains(said, earlierEngineLine) || strings.Contains(said, "Vaults") || strings.Contains(said, "the installed one is") {
			t.Fatalf("status while the run waits reports what it did not ask:\n%s", said)
		}
		// The menu's poll, as the menu-bar app asks it.
		for range 3 {
			cs, err := socketOf(t, a).Status(context.Background())
			if err != nil || !cs.EarlierRun || cs.Engine != "starting" || len(cs.Vaults) > 0 || cs.Syncthing != "" {
				t.Fatalf("the menu's poll while the run waits: %+v, %v", cs, err)
			}
		}
		notReady := "not ready yet: an engine an earlier vaultsync run left behind still holds the engine's folder"
		for name, cmd := range map[string]func(b *app) error{
			"pause":  func(b *app) error { return b.pauseSync(context.Background(), true) },
			"resume": func(b *app) error { return b.pauseSync(context.Background(), false) },
			"vaultsync pair": func(b *app) error {
				return b.pair(context.Background(), []string{"--code", "TULIP-ANCHOR-42", "--hub", "127.0.0.1:1"})
			},
		} {
			if said := withCopy(a, cmd); !strings.Contains(said, notReady) {
				t.Fatalf("%s while the run waits:\n%s", name, said)
			}
		}
		if _, err := socketOf(t, a).Pair(context.Background(), control.PairRequest{Code: "TULIP-ANCHOR-42", Hub: "127.0.0.1:1"}); err == nil || !strings.Contains(err.Error(), notReady) {
			t.Fatalf("a pairing through the agent while the run waits: %v", err)
		}
		time.Sleep(300 * time.Millisecond)
		if n := reached.Load(); n > 0 {
			t.Fatalf("the recorded port was asked %d times while the run waits", n)
		}
		if pids := fakeEnginePIDs(t, a.lay); len(pids) > 0 {
			t.Fatalf("the run started an engine (%v) while another held the engine's home", pids)
		}
		if now, err := loadState(a.lay.State); err != nil || now.GUIPort != port {
			t.Fatalf("agent.json records port %d (%v); it recorded %d", now.GUIPort, err, port)
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
		if err := saveState(eng.lay.State, st); err != nil {
			t.Fatal(err)
		}
		reached, earlier := answeringAtTheRecordedPort(t, st.GUIPort)
		homeLock, err := lockFile(filepath.Join(eng.lay.Home, syncthingHomeLock))
		if err != nil {
			t.Fatal(err)
		}
		let := sync.OnceFunc(homeLock)
		t.Cleanup(let)
		sup := superviseInBackground(context.Background(), eng, st)
		t.Cleanup(func() { sup.stop(30 * time.Second) })
		// setup's wait for its engine asks no port while its engine waits.
		client := syncthingClientAt(st.GUIPort)
		err = waitEngine(context.Background(), client, time.Second, sup, sup.notReadyYet)
		if err == nil || !strings.Contains(err.Error(), "an engine an earlier vaultsync run left behind") {
			t.Fatalf("setup's wait beside an earlier engine: %v", err)
		}
		if n := reached.Load(); n > 0 {
			t.Fatalf("setup asked the recorded port %d times while its engine waited", n)
		}
		if pids := fakeEnginePIDs(t, eng.lay); len(pids) > 0 {
			t.Fatalf("setup's engine started (%v) while another held the engine's home", pids)
		}
		earlier.Close()
		let()
		e2eWait(t, "setup's engine to start", 10*time.Second, func() bool { return len(fakeEnginePIDs(t, eng.lay)) > 0 })
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

// syncthingClientAt is an engine client for 127.0.0.1:port.
func syncthingClientAt(port int) *syncthing.Client {
	return syncthing.NewClient("http://127.0.0.1:"+strconv.Itoa(port), "k")
}

// A lock on the engine's home that cannot be looked at makes no run wait,
// and keeps no taken port: the run moves off it and starts its engine, as
// it did before (#419, decision 086). Nothing reaches the port it left.
func TestIssue419_ALockThatCannotBeLookedAtMakesNoRunWait(t *testing.T) {
	saved := earlierEngineLook
	earlierEngineLook = 50 * time.Millisecond
	t.Cleanup(func() { earlierEngineLook = saved })
	a, _ := runFixture(t)
	st, err := loadState(a.lay.State)
	if err != nil {
		t.Fatal(err)
	}
	reached, _ := answeringAtTheRecordedPort(t, st.GUIPort)
	linkInPlaceOf(t, filepath.Join(a.lay.Home, syncthingHomeLock))
	stop := inBackground(t, func(ctx context.Context) error { return a.run(ctx, nil) })
	e2eWait(t, "the run to start its engine", 10*time.Second, func() bool { return len(fakeEnginePIDs(t, a.lay)) > 0 })
	if now, err := loadState(a.lay.State); err != nil || now.GUIPort == st.GUIPort || now.GUIPort <= 0 {
		t.Fatalf("agent.json records port %d (%v); %d was taken", now.GUIPort, err, st.GUIPort)
	}
	if said := statusBeside(a); strings.Contains(said, "earlier vaultsync run") {
		t.Fatalf("status of a run that did not wait:\n%s", said)
	}
	if n := reached.Load(); n > 0 {
		t.Fatalf("the port the run moved off was asked %d times", n)
	}
	_ = stop()
}

// The control socket asks no port before the run has started its own
// engine (#419, decision 086): status says the engine is starting, or that
// the run waits for an engine an earlier run left behind, with nothing
// asked; pause and a pairing through the agent say the engine is not ready
// yet. Once the run has started its engine, the socket asks it as before.
func TestIssue419_TheSocketAsksNoPortBeforeTheRunsEngineStarts(t *testing.T) {
	a, _ := runFixture(t)
	st, err := loadState(a.lay.State)
	if err != nil {
		t.Fatal(err)
	}
	reached, _ := answeringAtTheRecordedPort(t, st.GUIPort)
	rs := &runState{}
	c := &controlServer{a: a, eng: a.engine(), logf: t.Logf, rs: rs, ctx: context.Background()}
	cs, err := c.currentStatus(context.Background())
	if err != nil || cs.Engine != "starting" || cs.EarlierRun {
		t.Fatalf("before the run started its engine: %+v, %v", cs, err)
	}
	rs.earlier.Store(true)
	cs, err = c.currentStatus(context.Background())
	if err != nil || cs.Engine != "starting" || !cs.EarlierRun || cs.Syncthing != "" || len(cs.Vaults) > 0 {
		t.Fatalf("while the run waits for an earlier engine: %+v, %v", cs, err)
	}
	if _, err := c.client(); !notReady(err) {
		t.Fatalf("an engine client while the run waits: %v", err)
	}
	if n := reached.Load(); n > 0 {
		t.Fatalf("the recorded port was asked %d times before the run started its engine", n)
	}
	rs.earlier.Store(false)
	rs.started.Store(true)
	if _, err := c.currentStatus(context.Background()); err == nil {
		t.Log("status once the run started its engine answered")
	}
	if reached.Load() == 0 {
		t.Fatal("once the run started its engine, the socket did not ask it")
	}
}

// vaultsync start on a Mac, while the service's run waits for an engine an
// earlier run left behind (#419, decisions 085 and 086): that run holds the
// engine lock and serves its socket, so start leaves it alone and says it
// is already running, which is true; status says what is known, and
// nothing reaches the recorded port. The waiting run goes on waiting, and
// starts its engine once the earlier one is gone.
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
	reached, earlier := answeringAtTheRecordedPort(t, st.GUIPort)
	homeLock, err := lockFile(filepath.Join(a.lay.Home, syncthingHomeLock))
	if err != nil {
		t.Fatal(err)
	}
	let := sync.OnceFunc(homeLock)
	t.Cleanup(let)
	stop := inBackground(t, func(ctx context.Context) error { return a.run(ctx, nil) })
	untilTheRunWaits(t, a)

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
	if said := statusBeside(a); !strings.Contains(said, earlierEngineLine) {
		t.Fatalf("status after start:\n%s", said)
	}
	if pids := fakeEnginePIDs(t, a.lay); len(pids) > 0 || !engineLockHeld(a.lay) {
		t.Fatalf("after start: engines %v, engine lock held %v", pids, engineLockHeld(a.lay))
	}
	if n := reached.Load(); n > 0 {
		t.Fatalf("the recorded port was asked %d times while the run waited", n)
	}
	earlier.Close()
	let()
	e2eWait(t, "the run to start its own engine", 10*time.Second, func() bool { return len(fakeEnginePIDs(t, a.lay)) > 0 })
	if err := stop(); err != nil {
		t.Fatalf("the run ended with %v", err)
	}
}

// A run waits for state.lock only so long, and not past a stop (#419): a
// holder that keeps it past runStateLockWait ends the run with a line that
// says so and an error, owning nothing and with nothing started — no wait
// on past the bound; a stop ends the wait at once.
func TestIssue419_ARunsWaitForStateLockIsBounded(t *testing.T) {
	asTheService(t)
	t.Run("past the bound", func(t *testing.T) {
		saved := runStateLockWait
		runStateLockWait = 300 * time.Millisecond
		t.Cleanup(func() { runStateLockWait = saved })
		a, _ := runFixture(t)
		before, err := os.ReadFile(a.lay.State)
		if err != nil {
			t.Fatal(err)
		}
		holdLock(t, stateLockOf(a.lay.State))
		ended := make(chan error, 1)
		start := time.Now()
		go func() { ended <- a.run(context.Background(), nil) }()
		select {
		case err = <-ended:
		case <-time.After(10 * time.Second):
			t.Fatal("the run still waits 10s after a bound of 300ms")
		}
		if !errors.Is(err, errRunStateLockHeld) || !strings.Contains(serviceSummary(err), "state.lock") {
			t.Fatalf("the run ended after %s with %v", time.Since(start).Round(time.Millisecond), err)
		}
		if engineLockHeld(a.lay) || len(fakeEnginePIDs(t, a.lay)) > 0 {
			t.Fatalf("the run left the engine lock held (%v) or an engine (%v)", engineLockHeld(a.lay), fakeEnginePIDs(t, a.lay))
		}
		if after, err := os.ReadFile(a.lay.State); err != nil || !bytes.Equal(after, before) {
			t.Fatalf("agent.json changed (%v):\n%s", err, after)
		}
	})
	t.Run("a stop", func(t *testing.T) {
		saved := runStateLockWait
		runStateLockWait = time.Minute
		t.Cleanup(func() { runStateLockWait = saved })
		a, _ := runFixture(t)
		holdLock(t, stateLockOf(a.lay.State))
		stop := inBackground(t, func(ctx context.Context) error { return a.run(ctx, nil) })
		time.Sleep(300 * time.Millisecond)
		start := time.Now()
		_ = stop()
		if took := time.Since(start); took > 2*time.Second || engineLockHeld(a.lay) {
			t.Fatalf("a stop during the wait for state.lock took %s; engine lock held %v", took.Round(time.Millisecond), engineLockHeld(a.lay))
		}
	})
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
// recorded the port it moves to does not read the recorded port: nothing
// reaches the port the run moves off (#419), for a run and setup's engine
// alike. The run is held in that instant at its take of the engine lock.
// Let go within the command's wait, the command reads what the run left —
// the port it moved to, or a run still on its way up, which it asks
// nothing; held past it, the command says it cannot tell and asks nothing.
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
					// The test answers at the recorded port, to see whether a
					// command asks it there.
					reached, _ := answeringAtTheRecordedPort(t, st.GUIPort)
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
					n := reached.Load()
					let()
					_ = stop()
					if n > 0 {
						t.Fatalf("%s reached the port the run moved off %d times:\n%s", c.name, n, said)
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
			a, reached := anotherProgramAtTheRecordedPort(t)
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
			if n := reached.Load(); n > 0 || !strings.Contains(said, c.says) || !strings.Contains(said, "state.lock") {
				t.Fatalf("the recorded port was asked %d times after %s:\n%s", n, time.Since(start).Round(time.Millisecond), said)
			}
		})
	}
}

// A command's wait for state.lock ends with its context: a Ctrl-C during
// the wait ends it at once (#419).
func TestIssue419_ACommandsWaitForStateLockEndsWithItsContext(t *testing.T) {
	saved := stateLockWait
	stateLockWait = time.Minute
	t.Cleanup(func() { stateLockWait = saved })
	a, _ := runFixture(t)
	holdLock(t, stateLockOf(a.lay.State))
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	_, _, err := engineMayRunAtItsPort(ctx, a.lay)
	if !errors.Is(err, context.Canceled) || time.Since(start) > 2*time.Second {
		t.Fatalf("a Ctrl-C during the wait for state.lock: %v after %s", err, time.Since(start).Round(time.Millisecond))
	}
}

// A record gone while a command looks (a removal under way) is not taken
// for nothing running: the locks are still looked at, and with an engine
// lock held the command cannot tell and asks nothing (#419).
func TestIssue419_ARecordGoneMeanwhileIsNotTakenForNothingRunning(t *testing.T) {
	a, reached := anotherProgramAtTheRecordedPort(t)
	holdLock(t, a.lay.Lock)
	before, err := loadState(a.lay.State)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(a.lay.State); err != nil {
		t.Fatal(err)
	}
	err = a.noAgentForPause(context.Background(), before, true)
	if n := reached.Load(); n > 0 || !strings.Contains(refusalText(err), "could not confirm whether the sync engine runs") {
		t.Fatalf("the recorded port was asked %d times: %v", n, err)
	}
	if _, err := os.Stat(stateLockOf(a.lay.State)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the look left state.lock behind a removed record: %v", err)
	}
}

// A look whose context ends just as it takes state.lock lets it go again:
// no lock is left held behind a Ctrl-C or a stop, for a command's look and
// a run's take of the engine lock alike (#419).
func TestIssue419_AStopAsStateLockIsTakenLetsItGo(t *testing.T) {
	a, _ := runFixture(t)
	path := stateLockOf(a.lay.State)
	unlock, err := lockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := engineMayRunAtItsPort(ctx, a.lay); !errors.Is(err, context.Canceled) {
		t.Fatalf("a command's look after a Ctrl-C: %v", err)
	}
	if held, err := lock.Probe(path); err != nil || held {
		t.Fatalf("a command's look left state.lock held: %v %v", held, err)
	}
	st, err := loadState(a.lay.State)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.engine().takeEngineLockLeavingATakenPort(ctx, &st, t.Logf); !errors.Is(err, context.Canceled) {
		t.Fatalf("a run's take after a stop: %v", err)
	}
	if held, err := lock.Probe(path); err != nil || held {
		t.Fatalf("a run's take left state.lock held: %v %v", held, err)
	}
	if engineLockHeld(a.lay) {
		t.Fatal("a run's take after a stop left the engine lock held")
	}
}

// agentSaying serves a control socket at the place a's agent.json records,
// answering status, with this build's mark, as say says at that moment.
func agentSaying(t *testing.T, a *app, say func() controlStatus) {
	t.Helper()
	path := filepath.Join(shortDir(t), "agent.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	mark := a.mark()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(control.BuildHeader, mark)
		if r.Method != http.MethodGet || r.URL.Path != "/v1/status" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, say())
	})}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	recordSocket(t, a, path)
}

// theServicesRunAnswers stands for the run of the background service that
// setup starts, which a fake service manager does not start: its agent says
// its engine runs, and setup asks the engine's port only then (#419,
// decision 086).
func theServicesRunAnswers(t *testing.T, a *app) {
	t.Helper()
	agentSaying(t, a, func() controlStatus { return controlStatus{Engine: "running"} })
}

// pinHeldBack makes a's run install its pin, and holds the download until
// release: the run is held after its engine lock and its record of the
// port, before its control socket — the stretch a pin's download lasts.
// asked is closed once the run asks for the pin.
func pinHeldBack(t *testing.T, a *app) (asked <-chan struct{}, release func()) {
	t.Helper()
	olderEngineHere(t, a, "v0.0.1")
	reached, let := make(chan struct{}), make(chan struct{})
	var once, letOnce sync.Once
	pinServedBy(t, func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(reached) })
		select {
		case <-let:
		case <-r.Context().Done():
		}
		http.NotFound(w, r)
	})
	release = func() { letOnce.Do(func() { close(let) }) }
	// Before the server closes, which waits for the download held here.
	t.Cleanup(release)
	return reached, release
}

// Between a run's engine lock and its control socket — as long as a pin's
// download lasts — no command asks the recorded port (#419, decision 086).
// Beside an engine an earlier run left behind, whose port the run keeps,
// status says the engine is starting, pause and resume say so and change
// nothing, vaultsync pair waits and then says the same, a pairing through
// the agent finds no socket to ask, and setup's wait for the service's
// engine asks nothing. A record of a socket an earlier run could not make
// is not taken for this run's: the run removes it as it takes the engine
// lock.
func TestIssue419_NoCommandAsksThePortBeforeTheRunsSocket(t *testing.T) {
	saved := pairEngineWait
	pairEngineWait = 300 * time.Millisecond
	t.Cleanup(func() { pairEngineWait = saved })
	inATerminal(t)
	a, _ := runFixture(t)
	st, err := loadState(a.lay.State)
	if err != nil {
		t.Fatal(err)
	}
	port := st.GUIPort
	// The engine an earlier run left behind: it holds the engine's home and
	// answers at the recorded port.
	reached, _ := answeringAtTheRecordedPort(t, port)
	holdLock(t, filepath.Join(a.lay.Home, syncthingHomeLock))
	writeFile(t, a.lay.SocketErrorFile(), "listen unix: bind: address already in use\n")
	asked, _ := pinHeldBack(t, a)
	stop := inBackground(t, func(ctx context.Context) error { return a.run(ctx, nil) })
	select {
	case <-asked:
	case <-time.After(10 * time.Second):
		t.Fatal("the run never asked for its pin")
	}
	if !engineLockHeld(a.lay) {
		t.Fatal("the run downloads its pin without the engine lock")
	}
	if _, err := os.Stat(a.lay.SocketErrorFile()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the record an earlier run left stayed: %v", err)
	}
	for _, c := range []struct {
		name, says string
		run        func(b *app) error
	}{
		{"status", startingLine, func(b *app) error { return b.status(context.Background()) }},
		{"pause", "still starting its sync engine, and nothing was paused", func(b *app) error { return b.pauseSync(context.Background(), true) }},
		{"resume", "still starting its sync engine, and nothing was resumed", func(b *app) error { return b.pauseSync(context.Background(), false) }},
		{"vaultsync pair", "still starting its sync engine", func(b *app) error {
			return b.pair(context.Background(), []string{"--code", "TULIP-ANCHOR-42", "--hub", "127.0.0.1:1"})
		}},
	} {
		if said := withCopy(a, c.run); !strings.Contains(said, c.says) {
			t.Fatalf("%s before the run's socket:\n%s", c.name, said)
		}
	}
	if _, err := socketOf(t, a).Pair(context.Background(), control.PairRequest{Code: "TULIP-ANCHOR-42", Hub: "127.0.0.1:1"}); !errors.Is(err, errNoAgent) {
		t.Fatalf("a pairing through the agent before its socket: %v", err)
	}
	// setup's wait for the engine of the service it started, as setup waits.
	b := *a
	if err := waitEngine(context.Background(), syncthingClientAt(port), 700*time.Millisecond, nil, b.runEngineGate); !errors.Is(err, errEngineNotStarted) {
		t.Fatalf("setup's wait before the run's socket: %v", err)
	}
	if n := reached.Load(); n > 0 {
		t.Fatalf("the recorded port was asked %d times before the run's socket", n)
	}
	if now, err := loadState(a.lay.State); err != nil || now.GUIPort != port {
		t.Fatalf("agent.json records port %d (%v); the run keeps %d", now.GUIPort, err, port)
	}
	_ = stop()
}

// vaultsync pair waits for a run that is starting its engine, as it waits
// for the engine (#413), and pairs once the run's agent says its engine
// runs. Beside an engine an earlier run left behind it says so at once,
// without waiting out its wait (#419).
func TestIssue419_PairWaitsForARunStartingItsEngine(t *testing.T) {
	saved := pairEngineWait
	pairEngineWait = 5 * time.Second
	t.Cleanup(func() { pairEngineWait = saved })
	pair := func(a *app) error {
		return a.pair(context.Background(), []string{"--code", "TULIP-ANCHOR-42", "--hub", "127.0.0.1:1"})
	}
	t.Run("starting", func(t *testing.T) {
		a := agentApp(t, newFakeEngine(t))
		var running atomic.Bool
		agentSaying(t, a, func() controlStatus {
			if running.Load() {
				return controlStatus{Engine: "running"}
			}
			return controlStatus{Engine: "starting"}
		})
		time.AfterFunc(600*time.Millisecond, func() { running.Store(true) })
		start := time.Now()
		err := pair(a)
		took := time.Since(start)
		if took < 600*time.Millisecond || strings.Contains(refusalText(err), "still starting") || strings.Contains(refusalText(err), "not ready yet") {
			t.Fatalf("pair beside a run starting its engine ended after %s with: %v", took.Round(time.Millisecond), err)
		}
	})
	t.Run("beside an engine an earlier run left behind", func(t *testing.T) {
		a := agentApp(t, newFakeEngine(t))
		agentSaying(t, a, func() controlStatus { return controlStatus{Engine: "starting", EarlierRun: true} })
		start := time.Now()
		err := pair(a)
		if took := time.Since(start); took > 2*time.Second || !strings.Contains(refusalText(err), "an engine an earlier vaultsync run left behind still holds the engine's folder") {
			t.Fatalf("pair beside a run that waits for an earlier engine ended after %s with: %v", took.Round(time.Millisecond), err)
		}
	})
}

// setup --no-service beside a vaultsync run that runs the engine, as before
// #419: setup's own engine finds the engine lock held, and setup goes on
// with the run's engine once that run's agent says it runs.
func TestIssue419_SetupWithoutTheServiceBesideARun(t *testing.T) {
	a, _, _ := macSetupFixture(t)
	holdLock(t, a.lay.Lock)
	agentSaying(t, a, func() controlStatus { return controlStatus{Engine: "running"} })
	out, err := setupFrom(t, a, "--no-service")
	// No Hub answers at this address: setup stops at the pairing, after
	// its wait for the engine.
	if strings.Contains(fmt.Sprint(err), "did not start") || !strings.Contains(refusalText(err)+fmt.Sprint(err), "did not answer") {
		t.Fatalf("setup --no-service beside a run: %v\n%s", err, out)
	}
}

// A run beside an engine that outlived its run, with the real engine
// (#419, decision 086): the engine's home is locked by that engine, which
// answers on the recorded port. The run neither moves the record off that
// port nor starts a second engine on the same home, and status says what
// is known without asking. Once that engine has ended, the run starts its
// own on the same port.
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

	// While that engine runs, status says what is known, and the run starts
	// none.
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
	if !strings.Contains(said, earlierEngineLine) || strings.Contains(said, "Vaults") {
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
	e2eWait(t, "status to say the run's own engine runs", 30*time.Second, func() bool {
		said = statusBeside(a)
		return strings.Contains(said, "Sync engine: running") && !strings.Contains(said, "earlier vaultsync run")
	})
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
