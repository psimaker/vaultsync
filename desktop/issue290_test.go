package main

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A copy of VaultSync started by its path beside a newer VaultSync for Mac
// — whose run, the moment the service starts, puts its own build in the
// copy's place and installs a newer engine — must not take that service
// for itself: the newer service syncs, and the copy's "does not sync" would
// be read as true. Since #416 the run that came up says which build it is,
// and start says that another build runs (#290).
func TestIssue290_AnOlderCopyBesideANewerAppSaysAnotherBuildRuns(t *testing.T) {
	run := &fakeRunner{}
	a := installedApp(t, "darwin", run)
	app := fakeMacApp(t, filepath.Join(a.home, "Applications"), appBundleName, shippedInfoPlist(t))
	appAgent := filepath.Join(app, "Contents", "MacOS", "vaultsync")
	writeFile(t, appAgent, "a newer VaultSync for Mac")
	data, err := a.svc.unitFile(appAgent)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, mustUnitPath(t, a.svc), string(data))
	runsAs(t, a, a.lay.Agent, "an older copy of VaultSync")
	replaced := false
	run.onCall = func(call string) {
		if !strings.Contains(call, "bootstrap") {
			return
		}
		replaced = true
		// The newer app's run as it comes up: its build in the copy's
		// place (keepCopyAtAppBuild), its engine recorded, its socket.
		writeFile(t, a.lay.Agent, "a newer VaultSync for Mac")
		newerEngineHere(t, a.lay)
		agentOfBuild(t, a, "a newer VaultSync for Mac")
	}
	err = a.startService()
	if !replaced {
		t.Fatal("start loaded no job: the newer run never came up, so this test tests nothing")
	}
	out := a.out.(*bytes.Buffer).String()
	if err != nil || strings.Contains(out, "does not sync") {
		t.Fatalf("a stale copy took the newer service for itself: %v\n%s", err, out)
	}
	if !strings.Contains(out, "runs another VaultSync build than this vaultsync") || strings.Contains(out, "Nothing was changed") {
		t.Fatalf("start beside a newer app: %v\n%s", err, out)
	}
}

// VaultSync for Mac replaced while start loads its job — dragged over in
// the Finder, or updated by the setup link — runs the newer build launchd
// finds at the path then. That run says which build it is, and start says
// only that another build runs, not "does not sync" (#290).
func TestIssue290_AnAppReplacedWhileStartLoadsItIsAnotherBuild(t *testing.T) {
	run := &fakeRunner{}
	a := installedApp(t, "darwin", run)
	app := fakeMacApp(t, filepath.Join(a.home, "Applications"), appBundleName, shippedInfoPlist(t))
	appAgent := filepath.Join(app, "Contents", "MacOS", "vaultsync")
	writeFile(t, appAgent, "the app's build")
	data, err := a.svc.unitFile(appAgent)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, mustUnitPath(t, a.svc), string(data))
	runsAs(t, a, a.lay.Agent, "the app's build") // the copy at the app's build
	replaced := false
	run.onCall = func(call string) {
		if !strings.Contains(call, "bootstrap") {
			return
		}
		replaced = true
		writeFile(t, appAgent, "a newer VaultSync for Mac")
		newerEngineHere(t, a.lay)
		agentOfBuild(t, a, "a newer VaultSync for Mac")
	}
	err = a.startService()
	if !replaced {
		t.Fatal("start loaded no job: the app was never replaced, so this test tests nothing")
	}
	out := a.out.(*bytes.Buffer).String()
	if err != nil || strings.Contains(out, "does not sync") || !strings.Contains(out, "runs another VaultSync build than this vaultsync") {
		t.Fatalf("start while the app was replaced: %v\n%s", err, out)
	}
}

// VaultSync for Mac replaced while start loads its job, and its run putting
// its own build in the copy's place as it comes up: by then the files at
// both paths are the newer build. start goes by what the run says, which
// is the newer build, not this one (#290).
func TestIssue290_AnAppAndCopyReplacedWhileStartLoadsThemAreAnotherBuild(t *testing.T) {
	run := &fakeRunner{}
	a := installedApp(t, "darwin", run)
	app := fakeMacApp(t, filepath.Join(a.home, "Applications"), appBundleName, shippedInfoPlist(t))
	appAgent := filepath.Join(app, "Contents", "MacOS", "vaultsync")
	writeFile(t, appAgent, "the app's build")
	data, err := a.svc.unitFile(appAgent)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, mustUnitPath(t, a.svc), string(data))
	runsAs(t, a, a.lay.Agent, "the app's build") // the copy at the app's build
	replaced := false
	run.onCall = func(call string) {
		if !strings.Contains(call, "bootstrap") {
			return
		}
		replaced = true
		writeFile(t, appAgent, "a newer VaultSync for Mac")
		writeFile(t, a.lay.Agent, "a newer VaultSync for Mac")
		newerEngineHere(t, a.lay)
		agentOfBuild(t, a, "a newer VaultSync for Mac")
	}
	err = a.startService()
	if !replaced {
		t.Fatal("start loaded no job: nothing was replaced, so this test tests nothing")
	}
	out := a.out.(*bytes.Buffer).String()
	if err != nil || strings.Contains(out, "does not sync") || !strings.Contains(out, "runs another VaultSync build than this vaultsync") {
		t.Fatalf("start while the app and the copy were replaced: %v\n%s", err, out)
	}
}

// A service whose program becomes this very build only while start loads its
// job — VaultSync for Mac replaced by this build meanwhile — runs this
// build: launchd starts what is at the path then. Before #416 start went by
// the files before and after the start, which differed, and said only that
// another build runs; the run that came up says it is this build (#290).
func TestIssue290_AServiceReplacedByThisBuildWhileStartLoadsItIsThisBuild(t *testing.T) {
	run := &fakeRunner{}
	a := installedApp(t, "darwin", run)
	app := fakeMacApp(t, filepath.Join(a.home, "Applications"), appBundleName, shippedInfoPlist(t))
	appAgent := filepath.Join(app, "Contents", "MacOS", "vaultsync")
	writeFile(t, appAgent, "another build")
	data, err := a.svc.unitFile(appAgent)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, mustUnitPath(t, a.svc), string(data))
	runsAs(t, a, a.lay.Agent, "this very build")
	replaced := false
	run.onCall = func(call string) {
		if !strings.Contains(call, "bootstrap") {
			return
		}
		replaced = true
		writeFile(t, appAgent, "this very build")
		agentOfBuild(t, a, "this very build")
	}
	err = a.startService()
	if !replaced {
		t.Fatal("start loaded no job: the app was never replaced, so this test tests nothing")
	}
	out := a.out.(*bytes.Buffer).String()
	if err != nil || !strings.Contains(out, "✓ VaultSync is running again.") {
		t.Fatalf("start where this build came up: %v\n%s", err, out)
	}
}

// darwinServiceOfThisBuild is a Mac whose LaunchAgent names VaultSync's own
// copy, which is this very build, and whose job launchd has loaded from that
// file, running.
func darwinServiceOfThisBuild(t *testing.T) (*app, *fakeRunner) {
	t.Helper()
	run := &fakeRunner{}
	a := installedApp(t, "darwin", run)
	data, err := a.svc.unitFile(a.lay.Agent)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, mustUnitPath(t, a.svc), string(data))
	runsAs(t, a, a.lay.Agent, "this build")
	run.loaded, run.loadedProgram = true, a.lay.Agent
	return a, run
}

// linuxServiceOfThisBuild is a Linux computer whose unit runs VaultSync's
// own copy, this very build: inactive until start enables it.
func linuxServiceOfThisBuild(t *testing.T) (*app, *fakeRunner) {
	t.Helper()
	run := &fakeRunner{systemd: true}
	a := installedApp(t, "linux", run)
	runsAs(t, a, a.lay.Agent, "this build")
	return a, run
}

// holdEngineLock holds the engine lock as a run of VaultSync's does from
// before its install on (run, in main.go).
func holdEngineLock(t *testing.T, a *app) {
	t.Helper()
	unlock, err := lockFile(a.lay.Lock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlock)
}

// answeringAgent serves a control socket that answers, at the place
// agent.json records, as a run of this very build: with its build mark.
func answeringAgent(t *testing.T, a *app) *fakeAgent {
	t.Helper()
	f := newFakeAgent(t)
	f.build = a.build
	recordSocket(t, a, f.path)
	return f
}

func recordSocket(t *testing.T, a *app, path string) {
	t.Helper()
	if err := updateState(a.lay.State, func(s *agentState) { s.ControlSocket = path }); err != nil {
		t.Fatal(err)
	}
}

func startText(a *app) string { return a.out.(*bytes.Buffer).String() }

// A run that lives and holds the engine lock — on its way up, its pin
// downloading, or syncing — is left alone by vaultsync start on a Mac. start
// booted it out and loaded it afresh, and the download began again from its
// first byte (seen on macOS 27.2). start still undoes a stop's disable, and
// says "already running" once the agent answers (#290).
func TestIssue290_StartLeavesALivingRunAlone(t *testing.T) {
	a, run := darwinServiceOfThisBuild(t)
	holdEngineLock(t, a)
	answeringAgent(t, a)
	run.disabled = true
	if err := a.startService(); err != nil {
		t.Fatalf("start: %v", err)
	}
	calls := strings.Join(run.calls, "\n")
	if strings.Contains(calls, "launchctl bootout") || strings.Contains(calls, "launchctl bootstrap") {
		t.Fatalf("start loaded a living run afresh:\n%s", calls)
	}
	if run.disabled || !strings.HasPrefix(calls, "launchctl enable") {
		t.Fatalf("start left the job disabled:\n%s", calls)
	}
	if out := startText(a); !strings.Contains(out, "✓ VaultSync is already running.") {
		t.Fatalf("start beside a living run:\n%s", out)
	}
}

// Every job that is no living run of its own is loaded afresh, as before:
// one on its way out (SIGTERMed — a stop that ran out of time, its run
// still holding the lock) and one between two spawns, as launchd showed
// them on macOS 12.7.6 and 27.2; an idle one; a run that holds no engine
// lock (just spawned, or parked); an older definition still loaded; and a
// lock that cannot be looked at (#290).
func TestIssue290_StartLoadsAfreshWhatIsNoLivingRun(t *testing.T) {
	for name, arrange := range map[string]func(t *testing.T, a *app, run *fakeRunner){
		"on its way out, its run holding the lock": func(t *testing.T, a *app, run *fakeRunner) {
			holdEngineLock(t, a)
			run.loaded, run.lingering = false, 2
		},
		"between two spawns": func(t *testing.T, a *app, run *fakeRunner) {
			holdEngineLock(t, a)
			run.neverRuns = true
			run.onCall = func(call string) {
				if strings.HasPrefix(call, "launchctl bootstrap") {
					run.neverRuns = false
				}
			}
		},
		"idle": func(t *testing.T, a *app, run *fakeRunner) {
			holdEngineLock(t, a)
			run.idle = true
		},
		"running without the engine lock": func(t *testing.T, a *app, run *fakeRunner) {},
		"an older definition loaded": func(t *testing.T, a *app, run *fakeRunner) {
			holdEngineLock(t, a)
			run.loadedProgram = filepath.Join(a.home, "Applications", appBundleName, "Contents", "MacOS", "vaultsync")
		},
		"a lock that cannot be looked at": func(t *testing.T, a *app, run *fakeRunner) {
			if err := os.Symlink(filepath.Join(a.home, "elsewhere"), a.lay.Lock); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			a, run := darwinServiceOfThisBuild(t)
			answeringAgent(t, a)
			arrange(t, a, run)
			if err := a.startService(); err != nil {
				t.Fatalf("start: %v (%v)", err, run.calls)
			}
			calls := strings.Join(run.calls, "\n")
			if !strings.Contains(calls, "launchctl bootout") || !strings.Contains(calls, "launchctl bootstrap") {
				t.Fatalf("not loaded afresh:\n%s", calls)
			}
			if out := startText(a); !strings.Contains(out, "✓ VaultSync is running again.") {
				t.Fatalf("start:\n%s", out)
			}
		})
	}
}

// systemd leaves an active unit alone by itself (seen with a run on its way
// up): start on Linux sends what it sent before, whatever the lock says,
// and one look before it tells a unit that was already active, which is
// "already running" as on a Mac (#290).
func TestIssue290_StartOnLinuxIsEnableNowAsBefore(t *testing.T) {
	for _, c := range []struct {
		active bool
		said   string
	}{
		{false, "✓ VaultSync is running again."},
		{true, "✓ VaultSync is already running."},
	} {
		a, run := linuxServiceOfThisBuild(t)
		run.unitActive = c.active
		holdEngineLock(t, a)
		answeringAgent(t, a)
		if err := a.startService(); err != nil {
			t.Fatal(err)
		}
		var actions []string
		for _, call := range run.calls {
			if !strings.Contains(call, "is-active") {
				actions = append(actions, call)
			}
		}
		if strings.Join(actions, "|") != "systemctl --user enable --now vaultsync.service" {
			t.Fatalf("start on Linux: %v", run.calls)
		}
		if out := startText(a); !strings.Contains(out, c.said) {
			t.Fatalf("start, the unit active before: %v:\n%s", c.active, out)
		}
	}
}

// The service manager counts a run as running from its first moment, and
// start said "running again" then — on macOS 27.2 and with systemd, while
// the run downloaded its pin or waited for a lock, no socket there yet.
// start now says so only once an agent answers; until then it says that
// the service does not answer yet and names what ends it, and refuses
// nothing (#290).
func TestIssue290_StartSaysRunningOnlyOnceTheAgentAnswers(t *testing.T) {
	for goos, arrange := range map[string]func(t *testing.T) (*app, *fakeRunner){
		"darwin": darwinServiceOfThisBuild,
		"linux":  linuxServiceOfThisBuild,
	} {
		t.Run(goos, func(t *testing.T) {
			a, _ := arrange(t)
			// Only the answer wait's own pauses are counted: its interval
			// is made one no other wait of start's uses.
			orig := startAnswerEvery
			startAnswerEvery = 7 * time.Millisecond
			defer func() { startAnswerEvery = orig }()
			looks := 0
			a.svc.pause = func(d time.Duration) {
				if d == startAnswerEvery {
					looks++
				}
			}
			err := a.startService()
			out := startText(a)
			if err != nil || strings.Contains(out, "running again") || strings.Contains(out, "✓") {
				t.Fatalf("start with no agent answering: %v\n%s", err, out)
			}
			for _, want := range []string{"does not answer yet", "vaultsync status says where it stands", "run vaultsync stop, then vaultsync start", a.logHint()} {
				if !strings.Contains(out, want) {
					t.Fatalf("%q missing:\n%s", want, out)
				}
			}
			if looks != startAnswerLooks-1 {
				t.Fatalf("start's answer wait paused %d times, not %d", looks, startAnswerLooks-1)
			}

			a, _ = arrange(t)
			answeringAgent(t, a)
			if err := a.startService(); err != nil || !strings.Contains(startText(a), "✓ VaultSync is running again.") {
				t.Fatalf("start with the agent answering: %v\n%s", err, startText(a))
			}
		})
	}
}

// A run records its socket's place in agent.json as it comes up: each look
// reads it afresh, so an agent that comes after the first looks is heard,
// and start looks no further once it has answered (#290).
func TestIssue290_StartHearsAnAgentThatComesUpWhileItWaits(t *testing.T) {
	a, _ := linuxServiceOfThisBuild(t)
	pauses := 0
	a.svc.pause = func(time.Duration) {
		if pauses++; pauses == 5 {
			answeringAgent(t, a)
		}
	}
	if err := a.startService(); err != nil || !strings.Contains(startText(a), "✓ VaultSync is running again.") {
		t.Fatalf("an agent that came up while start waited: %v\n%s", err, startText(a))
	}
	if pauses != 5 {
		t.Fatalf("start went on looking after the answer: %d pauses", pauses)
	}
}

// serveSocket serves h on a control socket at the place agent.json records.
func serveSocket(t *testing.T, a *app, h http.Handler) {
	t.Helper()
	path := filepath.Join(shortDir(t), "agent.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	recordSocket(t, a, path)
}

// Any answer of the agent's counts — what it says about syncing is
// status's to tell: the 404 every agent from desktop-v0.2.0 on gives for a
// path it does not serve, and an error it answers with (#290).
func TestIssue290_AnyAnswerOfTheAgentCounts(t *testing.T) {
	for name, h := range map[string]http.Handler{
		"its 404": http.NotFoundHandler(),
		"an error": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fail(w, http.StatusInternalServerError, errors.New("boom"))
		}),
	} {
		t.Run(name, func(t *testing.T) {
			a, _ := linuxServiceOfThisBuild(t)
			serveSocket(t, a, ofBuild(a.build, h))
			if err := a.startService(); err != nil || !strings.Contains(startText(a), "✓ VaultSync is running again.") {
				t.Fatalf("%v\n%s", err, startText(a))
			}
		})
	}
}

// start asks nothing the agent has to ask its engine for: status asks the
// engine first and took up to 8 s right after a start on a macOS VM, and
// an agent that has come up has answered whatever its engine says (#290).
func TestIssue290_StartDoesNotWaitForTheEngine(t *testing.T) {
	a, _ := linuxServiceOfThisBuild(t)
	release := make(chan struct{})
	defer close(release)
	var mu sync.Mutex
	asked := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked++
		mu.Unlock()
		select { // an engine that takes its time
		case <-release:
		case <-r.Context().Done():
		}
	})
	serveSocket(t, a, ofBuild(a.build, mux))
	orig := startAskTimeout
	startAskTimeout = 2 * time.Second
	defer func() { startAskTimeout = orig }()
	if err := a.startService(); err != nil || !strings.Contains(startText(a), "✓ VaultSync is running again.") {
		t.Fatalf("%v\n%s", err, startText(a))
	}
	mu.Lock()
	defer mu.Unlock()
	if asked != 0 {
		t.Fatalf("start asked the agent's status %d times", asked)
	}
}

// A socket that takes the request and never answers costs start one ask,
// not one per look (#290).
func TestIssue290_ASilentSocketCostsOneAsk(t *testing.T) {
	a, _ := linuxServiceOfThisBuild(t)
	path := filepath.Join(shortDir(t), "agent.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var held []net.Conn
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		l.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range held {
			c.Close()
		}
	})
	recordSocket(t, a, path)
	orig := startAskTimeout
	startAskTimeout = 200 * time.Millisecond
	defer func() { startAskTimeout = orig }()
	if err := a.startService(); err != nil || !strings.Contains(startText(a), "does not answer yet") {
		t.Fatalf("%v\n%s", err, startText(a))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(held) != 1 {
		t.Fatalf("start asked a silent socket %d times", len(held))
	}
}

// Beside a service that does not answer, start says so and names no build:
// only a running agent says which build runs, and the program at the
// service's path — another build here — may not be the one that runs
// (#290, #416). It refuses nothing.
func TestIssue290_AnotherBuildThatDoesNotAnswer(t *testing.T) {
	a, _ := linuxServiceOfThisBuild(t)
	quickAnswerWait(t)
	writeFile(t, a.lay.Agent, "another build the service runs")
	runsAs(t, a, filepath.Join(t.TempDir(), "vaultsync"), "this build")
	if err := a.startService(); err != nil {
		t.Fatal(err)
	}
	out := startText(a)
	if !strings.Contains(out, "does not answer yet") || strings.Contains(out, "another VaultSync build") || strings.Contains(out, "✓") {
		t.Fatalf("start beside a service that does not answer:\n%s", out)
	}
}

// quickAnswerWait makes the answer wait's interval short and one no other
// wait of start's uses, so a pause hook can tell its pauses apart.
func quickAnswerWait(t *testing.T) {
	t.Helper()
	orig := startAnswerEvery
	startAnswerEvery = 7 * time.Millisecond
	t.Cleanup(func() { startAnswerEvery = orig })
}

// The guard behind leaving a run alone (decision 085): launchd's states
// were measured on macOS 12.7.6 and 27.2 only. A job start took for a
// living run, and that is gone, between two spawns or on its way out by
// the end of the answer wait — one a stop was still removing, shown as
// running by some version in between — is loaded afresh and waited for
// once more. Left alone, the service would stay down until the next login
// (#175, #290).
func TestIssue290_ALivingRunThatIsGoneByTheEndOfTheWaitIsLoadedAfresh(t *testing.T) {
	for name, vanish := range map[string]func(run *fakeRunner){
		"gone":               func(run *fakeRunner) { run.loaded = false },
		"on its way out":     func(run *fakeRunner) { run.loaded, run.lingering = false, 2 },
		"between two spawns": func(run *fakeRunner) { run.neverRuns = true },
	} {
		t.Run(name, func(t *testing.T) {
			a, run := darwinServiceOfThisBuild(t)
			quickAnswerWait(t)
			unlock, err := lockFile(a.lay.Lock)
			if err != nil {
				t.Fatal(err)
			}
			var once sync.Once
			release := func() { once.Do(unlock) }
			t.Cleanup(release)
			vanished, served := false, false
			a.svc.pause = func(d time.Duration) {
				if d == startAnswerEvery && !vanished {
					vanished = true
					release() // the run ends, and its lock with it
					vanish(run)
				}
			}
			bootstraps := 0
			run.onCall = func(call string) {
				if strings.HasPrefix(call, "launchctl bootstrap") {
					bootstraps++
					run.neverRuns = false
					if !served {
						served = true
						answeringAgent(t, a) // the run loaded afresh answers
					}
				}
			}
			if err := a.startService(); err != nil {
				t.Fatalf("start: %v\n%s", err, strings.Join(run.calls, "\n"))
			}
			if !vanished || bootstraps != 1 {
				t.Fatalf("vanished=%v, bootstraps=%d:\n%s", vanished, bootstraps, strings.Join(run.calls, "\n"))
			}
			if out := startText(a); !strings.Contains(out, "✓ VaultSync is running again.") {
				t.Fatalf("start:\n%s", out)
			}
		})
	}
}

// A run left alone that still runs at the end of the answer wait stays as
// it is: the guard looks once and loads nothing (#290).
func TestIssue290_ALivingRunThatStaysIsLookedAtOnce(t *testing.T) {
	a, run := darwinServiceOfThisBuild(t)
	quickAnswerWait(t)
	holdEngineLock(t, a)
	if err := a.startService(); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(run.calls, "\n")
	if strings.Contains(calls, "launchctl bootout") || strings.Contains(calls, "launchctl bootstrap") {
		t.Fatalf("a run that stays was loaded afresh:\n%s", calls)
	}
	if out := startText(a); !strings.Contains(out, "does not answer yet") || strings.Contains(out, "✓") {
		t.Fatalf("start:\n%s", out)
	}
}

// A run that could not open its control socket syncs on without it and
// never answers there; a stop and start only makes the same socket fail
// again. start says what is true instead: the recorded socket error, and
// that status asks the engine itself (#290).
func TestIssue290_ARunWithoutItsSocketIsNotToldToStopAndStart(t *testing.T) {
	for goos, arrange := range map[string]func(t *testing.T) (*app, *fakeRunner){
		"darwin": darwinServiceOfThisBuild,
		"linux":  linuxServiceOfThisBuild,
	} {
		t.Run(goos, func(t *testing.T) {
			a, run := arrange(t)
			quickAnswerWait(t)
			holdEngineLock(t, a)
			run.unitActive = true
			writeFile(t, a.lay.SocketErrorFile(), "listen unix: bind: address already in use\n")
			if err := a.startService(); err != nil {
				t.Fatal(err)
			}
			out := startText(a)
			for _, want := range []string{"does not answer on its control socket", "could not open it", tildePath(a.home, a.lay.SocketErrorFile()), "vaultsync status asks the sync engine directly"} {
				if !strings.Contains(out, want) {
					t.Fatalf("%q missing:\n%s", want, out)
				}
			}
			if strings.Contains(out, "vaultsync stop") || strings.Contains(out, "installing") || strings.Contains(out, "✓") {
				t.Fatalf("start sends a run without its socket the wrong way:\n%s", out)
			}
			if calls := strings.Join(run.calls, "\n"); strings.Contains(calls, "launchctl bootout") {
				t.Fatalf("a living run was loaded afresh:\n%s", calls)
			}
		})
	}
}
