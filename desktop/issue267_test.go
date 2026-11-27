package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// pinServedBy points the downloads of this computer's real pin at a local
// server answering as h does (#267).
func pinServedBy(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	saved := newInstaller
	newInstaller = func(binDir, goos, goarch string) (installer, error) {
		in, err := saved(binDir, goos, goarch)
		if err != nil {
			return in, err
		}
		in.baseURL = srv.URL
		return in, nil
	}
	t.Cleanup(func() { newInstaller = saved })
}

// olderEngineHere records what an earlier VaultSync installed: an older pin,
// with the very program that is there (#267).
func olderEngineHere(t *testing.T, a *app, recorded string) {
	t.Helper()
	if err := updateState(a.lay.State, func(s *agentState) { s.Syncthing.Version = recorded }); err != nil {
		t.Fatal(err)
	}
}

// asTheService runs a vaultsync run as the background service for the test:
// only the service keeps its engine (#267).
func asTheService(t *testing.T) {
	t.Helper()
	origMode, origTerm := serviceMode, stderrIsTerminal
	t.Cleanup(func() { serviceMode, stderrIsTerminal = origMode, origTerm })
	serviceMode = false
	stderrIsTerminal = func() bool { return false }
}

// stallAt shortens how long a download may make no progress.
func stallAt(t *testing.T, d time.Duration) {
	t.Helper()
	saved := keepEngineStallWait
	keepEngineStallWait = d
	t.Cleanup(func() { keepEngineStallWait = saved })
}

// inATerminal runs a vaultsync run as in a terminal for the test.
func inATerminal(t *testing.T) {
	t.Helper()
	origMode, origTerm := serviceMode, stderrIsTerminal
	t.Cleanup(func() { serviceMode, stderrIsTerminal = origMode, origTerm })
	serviceMode = false
	stderrIsTerminal = func() bool { return true }
}

// keptRun runs a until it serves, and returns what it logged meanwhile and
// its stop (#267).
func keptRun(t *testing.T, a *app) (string, func() error) {
	t.Helper()
	var stop func() error
	logged := serviceLogOf(t, func() {
		stop = inBackground(t, func(ctx context.Context) error { return a.run(ctx, nil) })
		untilTheRunServes(t, a)
	})
	return logged, stop
}

// A run whose pin cannot be installed — its download fails — keeps the
// engine its record names, unchanged since: it starts that engine instead of
// ending at every start until the download works, says so in one fixed line
// with no address, and leaves the record as it is (#267).
func TestIssue267_ARunKeepsTheEngineItHasWhenThePinCannotBeInstalled(t *testing.T) {
	asTheService(t)
	pinServedBy(t, http.NotFound)
	a, _ := runFixture(t)
	olderEngineHere(t, a, "v2.1.6")
	before, err := loadState(a.lay.State)
	if err != nil {
		t.Fatal(err)
	}
	logged, stop := keptRun(t, a)
	if err := stop(); err != nil {
		t.Fatalf("the run ended with %v", err)
	}
	after, err := loadState(a.lay.State)
	if err != nil {
		t.Fatal(err)
	}
	if after.Syncthing != before.Syncthing {
		t.Fatalf("the record of the kept engine changed: %+v → %+v", before.Syncthing, after.Syncthing)
	}
	want := "Syncthing 2.1.7 could not be installed — the download did not get through; the installed Syncthing 2.1.6 runs on, and the next start tries again (vaultsync stop, then vaultsync start)"
	if !strings.Contains(logged, want) || strings.Contains(logged, "127.0.0.1") || strings.Contains(logged, "http") {
		t.Fatalf("what the run logged:\n%s", logged)
	}
}

// A download that does not match its checksum is the download's failure, not
// the installed engine's: that engine runs on, and the line says the
// mismatch in its own words (#267).
func TestIssue267_AChecksumMismatchKeepsTheEngineToo(t *testing.T) {
	asTheService(t)
	pinServedBy(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("not the archive")) })
	a, _ := runFixture(t)
	olderEngineHere(t, a, "v2.1.6")
	logged, stop := keptRun(t, a)
	if err := stop(); err != nil {
		t.Fatalf("the run ended with %v", err)
	}
	if !strings.Contains(logged, "could not be installed — "+ErrChecksumMismatch.Error()+" — nothing was installed; the installed Syncthing 2.1.6 runs on") {
		t.Fatalf("what the run logged:\n%s", logged)
	}
}

// An attempt that hangs — a connection that never answers — ends once it
// makes no progress, where an engine is there to keep: no engine runs
// meanwhile (#267).
func TestIssue267_AHangingDownloadIsBoundedWhereAnEngineIsKept(t *testing.T) {
	asTheService(t)
	pinServedBy(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	stallAt(t, 300*time.Millisecond)
	a, _ := runFixture(t)
	olderEngineHere(t, a, "v2.1.6")
	logged, stop := keptRun(t, a)
	if err := stop(); err != nil {
		t.Fatalf("the run ended with %v", err)
	}
	if !strings.Contains(logged, "could not be installed — the download made no progress for 300ms") {
		t.Fatalf("what the run logged:\n%s", logged)
	}
}

// vaultsync stop during the download ends the run: a stop is no failed
// install, and starts no engine (#267).
func TestIssue267_AStopDuringTheDownloadEndsTheRun(t *testing.T) {
	asTheService(t)
	asked := make(chan struct{}, 1)
	pinServedBy(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case asked <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	})
	a, _ := runFixture(t)
	olderEngineHere(t, a, "v2.1.6")
	logged := serviceLogOf(t, func() {
		stop := inBackground(t, func(ctx context.Context) error { return a.run(ctx, nil) })
		select {
		case <-asked:
		case <-time.After(10 * time.Second):
			t.Fatal("the run never asked for its pin")
		}
		_ = stop()
	})
	if strings.Contains(logged, "runs on") {
		t.Fatalf("a stop during the download kept the engine:\n%s", logged)
	}
	if pids := fakeEnginePIDs(t, a.lay); len(pids) > 0 {
		t.Fatalf("a stop during the download started the engine: %v", pids)
	}
	if st, err := loadState(a.lay.State); err != nil || st.ControlSocket != "" {
		t.Fatalf("a stop during the download served: %+v %v", st, err)
	}
}

// Where the engine is not there to keep, a run ends as before when its pin
// cannot be installed: with no config (a new one would be made by the older
// engine and edited in the pin's shape), with no checksum recorded, with a
// changed program, with an engine older than keepEngineFrom, and where this
// build has no pin for this computer at all (#267).
func TestIssue267_ARunKeepsNoEngineItCannotTrust(t *testing.T) {
	for _, c := range []struct {
		name  string
		setUp func(t *testing.T, a *app)
		noPin bool
	}{
		{"no config", func(t *testing.T, a *app) {
			if err := os.Remove(filepath.Join(a.lay.Home, "config.xml")); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"no checksum recorded", func(t *testing.T, a *app) {
			if err := updateState(a.lay.State, func(s *agentState) { s.Syncthing.BinarySHA256 = "" }); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"a changed program", func(t *testing.T, a *app) { writeFile(t, a.lay.Syncthing, "a changed engine") }, false},
		{"older than keepEngineFrom", func(t *testing.T, a *app) { olderEngineHere(t, a, "v2.1.5") }, false},
		{"no pin for this computer", func(t *testing.T, a *app) {}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			asTheService(t)
			if c.noPin {
				saved := newInstaller
				newInstaller = func(string, string, string) (installer, error) { return installer{}, ErrNoPin }
				t.Cleanup(func() { newInstaller = saved })
			} else {
				pinServedBy(t, http.NotFound)
			}
			a, _ := runFixture(t)
			olderEngineHere(t, a, "v2.1.6")
			c.setUp(t, a)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var err error
			logged := serviceLogOf(t, func() { err = a.run(ctx, nil) })
			if err == nil || strings.Contains(logged, "runs on") {
				t.Fatalf("the run went on without its pin: %v\n%s", err, logged)
			}
			if st, err := loadState(a.lay.State); err != nil || st.ControlSocket != "" {
				t.Fatalf("the run served: %+v %v", st, err)
			}
		})
	}
}

// A record in desktop-v0.1.0's format stays in it where the run keeps its
// engine: the run takes over nothing an older VaultSync must be kept from
// (#264, #267).
func TestIssue267_AKeptEngineLeavesTheFormatAsItWas(t *testing.T) {
	asTheService(t)
	pinServedBy(t, http.NotFound)
	a, _ := runFixture(t)
	olderEngineHere(t, a, "v2.1.6")
	asV010(t, a.lay.State)
	_, stop := keptRun(t, a)
	if err := stop(); err != nil {
		t.Fatalf("the run ended with %v", err)
	}
	if f := stateFormat(t, a.lay.State); f != 1 {
		t.Fatalf("a run that kept its engine left agent.json in format %d", f)
	}
}

// status and the running agent name the engine that runs — the older one
// kept — and status says what tries the pin again (#267).
func TestIssue267_StatusNamesTheEngineThatRuns(t *testing.T) {
	asTheService(t)
	pinServedBy(t, http.NotFound)
	a, _ := runFixture(t)
	olderEngineHere(t, a, "v2.1.6")
	out := statusText(t, a)
	if !strings.Contains(out, "sync engine Syncthing 2.1.6\n") || !strings.Contains(out, "this VaultSync brings Syncthing 2.1.7, which vaultsync setup installs.") {
		t.Fatalf("status with an older engine and no service of this build:\n%s", out)
	}
	_, stop := keptRun(t, a)
	defer func() { _ = stop() }()
	st, err := loadState(a.lay.State)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cs, err := a.controlClient(st).Status(ctx)
	if err != nil || cs.Syncthing != "2.1.6" {
		t.Fatalf("the agent names %q as its engine: %v", cs.Syncthing, err)
	}
}

// What status says tries the pin again does so on both systems, run as
// written: vaultsync stop ends the service, and vaultsync start loads it
// afresh — a new run, which tries the pin again (#267).
func TestIssue267_StopThenStartStartsTheServiceAfresh(t *testing.T) {
	for _, c := range []struct {
		goos, stop, start string
	}{
		{"darwin", "launchctl bootout", "launchctl bootstrap"},
		{"linux", "systemctl --user disable --now vaultsync.service", "systemctl --user enable --now vaultsync.service"},
	} {
		t.Run(c.goos, func(t *testing.T) {
			run := &fakeRunner{loaded: true, answers: map[string]string{"systemctl --user is-active vaultsync.service": "active"}}
			a := installedApp(t, c.goos, run)
			if err := a.stopService(); err != nil {
				t.Fatalf("vaultsync stop: %v", err)
			}
			if err := a.startService(); err != nil {
				t.Fatalf("vaultsync start: %v", err)
			}
			calls := strings.Join(run.calls, "\n")
			stopAt, startAt := strings.Index(calls, c.stop), strings.LastIndex(calls, c.start)
			if stopAt < 0 || startAt < stopAt {
				t.Fatalf("stop, then start, on %s:\n%s", c.goos, calls)
			}
		})
	}
}

// setup's refusal, where it runs as VaultSync for Mac's agent, names that
// agent to run it again — the vaultsync command may still lead to an older
// copy — and keeps today's words elsewhere (#267).
func TestIssue267_SetupFromTheAppNamesTheAppToRunAgain(t *testing.T) {
	pinServedBy(t, http.NotFound)
	a, _ := runFixture(t)
	if err := updateState(a.lay.State, func(s *agentState) { s.Syncthing.BinarySHA256 = "changed" }); err != nil {
		t.Fatal(err)
	}
	tm, done := openTerm(&bytes.Buffer{})
	defer done()
	if _, _, err := a.ensureEngine(context.Background(), tm); !strings.Contains(refusalText(err), "then run vaultsync setup again") {
		t.Fatalf("setup's refusal from a vaultsync of its own: %v", err)
	}
	app := fakeMacApp(t, filepath.Join(a.home, "Applications"), appBundleName, shippedInfoPlist(t))
	agent := filepath.Join(app, "Contents", "MacOS", "vaultsync")
	a.appEnv = macAppEnv(a.home, filepath.Join(t.TempDir(), "none"), agent)
	if _, _, err := a.ensureEngine(context.Background(), tm); !strings.Contains(refusalText(err), "then run "+a.commandFor(agent)+" setup again") {
		t.Fatalf("setup's refusal from the app: %v", err)
	}
}

// The program is hashed again after the failed attempt: one changed while
// the run waited for its pin is no longer the engine its record names, and
// is not kept (#267).
func TestIssue267_AProgramChangedDuringTheAttemptIsNotKept(t *testing.T) {
	asTheService(t)
	a, _ := runFixture(t)
	olderEngineHere(t, a, "v2.1.6")
	pinServedBy(t, func(w http.ResponseWriter, r *http.Request) {
		writeFile(t, a.lay.Syncthing, "changed while the run waited")
		http.NotFound(w, r)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var err error
	logged := serviceLogOf(t, func() { err = a.run(ctx, nil) })
	if err == nil || strings.Contains(logged, "runs on") {
		t.Fatalf("the run kept a program changed during the attempt: %v\n%s", err, logged)
	}
	if st, err := loadState(a.lay.State); err != nil || st.ControlSocket != "" {
		t.Fatalf("the run served: %+v %v", st, err)
	}
}

// status says what installs the pin by what the service runs: this very
// build tries it at its next start — vaultsync stop, then vaultsync start
// (#267).
func TestIssue267_StatusNamesStopThenStartWhereTheServiceRunsThisBuild(t *testing.T) {
	a := installedApp(t, "linux", &fakeRunner{answers: map[string]string{"systemctl --user is-active vaultsync.service": "active"}})
	sum, err := fileSHA256(a.lay.Syncthing)
	if err != nil {
		t.Fatal(err)
	}
	if err := updateState(a.lay.State, func(s *agentState) { s.Syncthing.Version, s.Syncthing.BinarySHA256 = "v2.1.6", sum }); err != nil {
		t.Fatal(err)
	}
	runsAs(t, a, a.lay.Agent, "this very build")
	if out := statusText(t, a); !strings.Contains(out, "which its background service tries to install at its next start — vaultsync stop, then vaultsync start") {
		t.Fatalf("status where the service runs this build:\n%s", out)
	}
}

// A run in a terminal ends and says why when its pin cannot be installed, as
// setup does: a person started it and sees it. Only the service keeps its
// engine (#267).
func TestIssue267_ARunInATerminalKeepsNothing(t *testing.T) {
	inATerminal(t)
	pinServedBy(t, http.NotFound)
	a, _ := runFixture(t)
	olderEngineHere(t, a, "v2.1.6")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var err error
	logged := serviceLogOf(t, func() { err = a.run(ctx, nil) })
	if err == nil || strings.Contains(logged, "runs on") {
		t.Fatalf("a run in a terminal kept its engine: %v\n%s", err, logged)
	}
}

// slowly serves data in chunks, with a gap after each: a slow download that
// goes on.
func slowly(data []byte, chunk int, gap time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		for i := 0; i < len(data); i += chunk {
			if _, err := w.Write(data[i:min(i+chunk, len(data))]); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-time.After(gap):
			case <-r.Context().Done():
				return
			}
		}
	}
}

// A slow download that goes on is not cut where an engine is there to keep —
// only one that makes no progress is: it installs the pin, and no engine is
// kept (#267).
func TestIssue267_ASlowDownloadThatGoesOnInstallsThePin(t *testing.T) {
	asTheService(t)
	stallAt(t, 300*time.Millisecond)
	a, _ := runFixture(t)
	olderEngineHere(t, a, "v2.1.6")
	engineScript, err := os.ReadFile(a.lay.Syncthing)
	if err != nil {
		t.Fatal(err)
	}
	key := a.goos + "/" + runtime.GOARCH
	pin := syncthingPins[key]
	data := realTarGz(t, strings.TrimSuffix(pin.archive, ".tar.gz"), engineScript)
	syncthingPins[key] = syncthingPin{archive: pin.archive, sha256: sum(data), binary: pin.binary}
	t.Cleanup(func() { syncthingPins[key] = pin })
	chunk := max(1, len(data)/12)
	pinServedBy(t, slowly(data, chunk, 100*time.Millisecond))
	// A total timeout shorter than the whole slow download, as the
	// client's ten minutes are on a slow link: it must not cut it.
	served := newInstaller
	newInstaller = func(binDir, goos, goarch string) (installer, error) {
		in, err := served(binDir, goos, goarch)
		if err == nil {
			c := *in.client
			c.Timeout = 400 * time.Millisecond
			in.client = &c
		}
		return in, err
	}
	t.Cleanup(func() { newInstaller = served })
	logged, stop := keptRun(t, a)
	if err := stop(); err != nil {
		t.Fatalf("the run ended with %v", err)
	}
	st, err := loadState(a.lay.State)
	if err != nil {
		t.Fatal(err)
	}
	if st.Syncthing.Version != syncthingVersion || strings.Contains(logged, "runs on") {
		t.Fatalf("a slow download that went on was cut: %+v\n%s", st.Syncthing, logged)
	}
}

// A download that stops midway — an answer, a few bytes, then nothing — ends
// once it makes no progress, and the service keeps its engine (#267).
func TestIssue267_ADownloadThatStopsMidwayKeepsTheEngine(t *testing.T) {
	asTheService(t)
	stallAt(t, 300*time.Millisecond)
	pinServedBy(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		_, _ = w.Write([]byte("PK"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	a, _ := runFixture(t)
	olderEngineHere(t, a, "v2.1.6")
	logged, stop := keptRun(t, a)
	if err := stop(); err != nil {
		t.Fatalf("the run ended with %v", err)
	}
	if !strings.Contains(logged, "could not be installed — the download made no progress for 300ms; the installed Syncthing 2.1.6 runs on") {
		t.Fatalf("what the run logged:\n%s", logged)
	}
}

// A download refused at once — nothing listens there — is the download's
// trouble, and said as such, not as a system call's (#267).
func TestIssue267_ARefusedDownloadIsSaidAsTheDownloads(t *testing.T) {
	asTheService(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "http://" + l.Addr().String()
	_ = l.Close()
	saved := newInstaller
	newInstaller = func(binDir, goos, goarch string) (installer, error) {
		in, err := saved(binDir, goos, goarch)
		in.baseURL = closed
		return in, err
	}
	t.Cleanup(func() { newInstaller = saved })
	a, _ := runFixture(t)
	olderEngineHere(t, a, "v2.1.6")
	logged, stop := keptRun(t, a)
	if err := stop(); err != nil {
		t.Fatalf("the run ended with %v", err)
	}
	if !strings.Contains(logged, "could not be installed — the download did not get through;") || strings.Contains(logged, "system call") {
		t.Fatalf("what the run logged:\n%s", logged)
	}
}

// Beside an installed VaultSync for Mac, a run that keeps its engine leaves
// VaultSync's copy and agent.json's format as they were: this build has not
// taken over until its pin is installed (#264, #267).
func TestIssue267_AKeptEngineBesideTheAppLeavesTheCopyAndTheFormat(t *testing.T) {
	asTheService(t)
	pinServedBy(t, http.NotFound)
	a, _ := runFixture(t)
	app := fakeMacApp(t, filepath.Join(a.home, "Applications"), appBundleName, shippedInfoPlist(t))
	a.appEnv = macAppEnv(a.home, filepath.Join(t.TempDir(), "none"), filepath.Join(app, "Contents", "MacOS", "vaultsync"))
	olderEngineHere(t, a, "v2.1.6")
	asV010(t, a.lay.State)
	_, copyErr := os.Lstat(a.lay.Agent)
	logged, stop := keptRun(t, a)
	if err := stop(); err != nil {
		t.Fatalf("the run ended with %v", err)
	}
	if !strings.Contains(logged, "runs on") {
		t.Fatalf("the run beside the app did not keep its engine:\n%s", logged)
	}
	if f := stateFormat(t, a.lay.State); f != 1 {
		t.Fatalf("a kept engine beside the app left agent.json in format %d", f)
	}
	if _, err := os.Lstat(a.lay.Agent); (err == nil) != (copyErr == nil) {
		t.Fatalf("a kept engine beside the app put a copy in place or took it: before %v, after %v", copyErr, err)
	}
}

// Where the agent that answers runs another Syncthing than the one
// installed — a service not started again since setup installed its pin —
// status says which runs, by the agent's own word (#267).
func TestIssue267_StatusSaysWhichEngineRunsWhereTheAgentRunsAnother(t *testing.T) {
	agent := newFakeAgent(t)
	agent.status = controlStatus{Version: "9.9.9", Syncthing: "2.1.6", Engine: "running", Report: statusReport{Text: "\nVaults\n"}}
	a := terminalApp(t, agent.path)
	if err := a.status(context.Background()); err != nil {
		t.Fatal(err)
	}
	out := a.out.(*bytes.Buffer).String()
	if !strings.Contains(out, "Sync engine: running Syncthing 2.1.6 — the installed Syncthing "+strings.TrimPrefix(syncthingVersion, "v")+" runs from the service's next start") {
		t.Fatalf("status where the agent runs another engine:\n%s", out)
	}
}
