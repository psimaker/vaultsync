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
