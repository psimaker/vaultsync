package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// unrecordedHere makes a's folder #276's case: the engine's data there,
// and agent.json gone (record false) or naming no engine (record true).
func unrecordedHere(t *testing.T, a *app, record bool) {
	t.Helper()
	withInstallLocks(t, a.lay)
	writeFile(t, filepath.Join(a.lay.Home, "index-v2", "db"), "the engine's database")
	if err := os.Remove(a.lay.State); err != nil {
		t.Fatal(err)
	}
	if record {
		if err := saveState(a.lay.State, agentState{GUIPort: 1}); err != nil {
			t.Fatal(err)
		}
	}
}

// withInstallLocks puts the lock files a set-up computer has: every setup,
// run and pair leaves its own, and a refusal must keep them (#276).
func withInstallLocks(t *testing.T, lay layout) {
	t.Helper()
	for _, name := range []string{"setup.lock", "pair.lock", "agent.lock", "state.lock"} {
		writeFile(t, filepath.Join(lay.Base, name), "")
	}
}

// snapshot is every folder and file under dir, with each file's bytes.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	got := map[string]string{}
	if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			got[rel+"/"] = ""
			return nil
		}
		data, err := os.ReadFile(p)
		got[rel] = string(data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return got
}

func unchangedFolder(t *testing.T, what string, before, after map[string]string) {
	t.Helper()
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("%s changed VaultSync's folder:\nbefore %v\nafter  %v", what, before, after)
	}
}

// noDownloads makes any installer this test reaches fail at once: a setup
// or run that should change nothing must not get as far as a download.
func noDownloads(t *testing.T) {
	t.Helper()
	saved := newInstaller
	newInstaller = func(string, string, string) (installer, error) {
		return installer{}, errors.New("this test downloads nothing")
	}
	t.Cleanup(func() { newInstaller = saved })
}

// With no engine recorded and the engine's data in place, setup changes
// nothing — not a byte, not its own lock file — and says why, the way out
// and what it costs (#276).
func TestIssue276_SetupLeavesDataNoRecordNamesAlone(t *testing.T) {
	for _, c := range []struct {
		name           string
		record, binary bool
	}{
		{"no record, the program gone", false, false},
		{"no record, another engine's program", false, true},
		{"a record naming no engine", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			noDownloads(t)
			a, _ := runFixture(t)
			if !c.binary {
				if err := os.Remove(a.lay.Syncthing); err != nil {
					t.Fatal(err)
				}
			}
			unrecordedHere(t, a, c.record)
			if c.binary {
				// Not this build's: setup refuses before it would compare.
				writeFile(t, a.lay.Syncthing, "another engine")
				if err := os.Remove(filepath.Join(a.lay.Home, "config.xml")); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshot(t, a.lay.Base)
			err := a.setup(context.Background(), nil)
			if err == nil || !strings.Contains(err.Error(), "agent.json records no engine") || !strings.Contains(err.Error(), "pairs with your Hub again") {
				t.Fatalf("setup over an unrecorded engine's data: %v", err)
			}
			unchangedFolder(t, "a refused setup", before, snapshot(t, a.lay.Base))
		})
	}
}

// setup takes the engine back only where it is this build's own — its
// program the very one the pin installs, compared with the verified
// download before anything is replaced; another engine's program changes
// nothing (#276).
func TestIssue276_SetupTakesBackOnlyItsOwnEngine(t *testing.T) {
	archive := "syncthing-linux-amd64-" + syncthingVersion + ".tar.gz"
	top := strings.TrimSuffix(archive, ".tar.gz")
	data := realTarGz(t, top, fakeBinary)
	srv := serveArchive(t, archive, data)
	saved := newInstaller
	newInstaller = func(binDir, goos, goarch string) (installer, error) {
		in := testInstaller(t, srv.URL, archive, sum(data), top+"/syncthing")
		in.binDir = binDir
		return in, nil
	}
	defer func() { newInstaller = saved }()

	another, _ := runFixture(t)
	unrecordedHere(t, another, false)
	writeFile(t, another.lay.Syncthing, "another engine")
	before := snapshot(t, another.lay.Base)
	tm, done := openTerm(&bytes.Buffer{})
	defer done()
	if _, _, err := another.ensureEngine(context.Background(), tm); err == nil || !strings.Contains(err.Error(), "agent.json records no engine") {
		t.Fatalf("another engine's program taken back: %v", err)
	}
	unchangedFolder(t, "a refused take-back", before, snapshot(t, another.lay.Base))

	own, _ := runFixture(t)
	unrecordedHere(t, own, false)
	writeFile(t, own.lay.Syncthing, string(fakeBinary))
	config, err := os.ReadFile(filepath.Join(own.lay.Home, "config.xml"))
	if err != nil {
		t.Fatal(err)
	}
	st, _, err := own.ensureEngine(context.Background(), tm)
	if err != nil {
		t.Fatalf("this build's own engine was not taken back: %v", err)
	}
	if st.Syncthing.Version != syncthingVersion || st.Syncthing.BinarySHA256 != sum(fakeBinary) {
		t.Fatalf("the record after the take-back: %+v", st.Syncthing)
	}
	if after, _ := os.ReadFile(filepath.Join(own.lay.Home, "config.xml")); string(after) != string(config) {
		t.Fatal("the take-back changed the engine's config")
	}
}

// A run never takes the engine back: in a terminal it says why and ends,
// as the service it parks; neither changes a byte (#276).
func TestIssue276_RunLeavesDataNoRecordNamesAlone(t *testing.T) {
	origMode, origTerm := serviceMode, stderrIsTerminal
	defer func() { serviceMode, stderrIsTerminal = origMode, origTerm }()

	noDownloads(t)
	a, _ := runFixture(t)
	unrecordedHere(t, a, false)
	before := snapshot(t, a.lay.Base)
	serviceMode = false
	stderrIsTerminal = func() bool { return true }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := a.run(ctx, nil); err == nil || !strings.Contains(err.Error(), "agent.json records no engine") {
		t.Fatalf("a run in a terminal over an unrecorded engine's data: %v", err)
	}
	unchangedFolder(t, "a refused run", before, snapshot(t, a.lay.Base))

	stderrIsTerminal = func() bool { return false }
	parked, stop := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer stop()
	if err := a.run(parked, nil); err != nil {
		t.Fatalf("the service over an unrecorded engine's data did not park: %v", err)
	}
	unchangedFolder(t, "a parked service", before, snapshot(t, a.lay.Base))
}

// status says what setup will do with such data, not "run vaultsync
// setup" as if nothing were there (#276).
func TestIssue276_StatusSaysWhatSetupWillDo(t *testing.T) {
	a, _ := runFixture(t)
	unrecordedHere(t, a, false)
	out := statusText(t, a)
	if !strings.Contains(out, "takes it back only over this VaultSync's own engine") || strings.Contains(out, "not set up on this computer yet") {
		t.Fatalf("status over an unrecorded engine's data:\n%s", out)
	}
}

// A run that waited through a removal that stopped — its engine gone, the
// record kept — does not set the engine up again behind it: it says how
// to finish or keep VaultSync (#276).
func TestIssue276_RunStopsWhenItsEngineWentWhileItWaited(t *testing.T) {
	origMode, origTerm := serviceMode, stderrIsTerminal
	defer func() { serviceMode, stderrIsTerminal = origMode, origTerm }()
	serviceMode = false
	stderrIsTerminal = func() bool { return true }

	noDownloads(t)
	a, _ := runFixture(t)
	unlock, err := lockFile(filepath.Join(a.lay.Base, "setup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 1)
	go func() { ended <- a.run(context.Background(), nil) }()
	time.Sleep(300 * time.Millisecond)
	if err := os.Remove(a.lay.Syncthing); err != nil {
		t.Fatal(err)
	}
	unlock()
	select {
	case err := <-ended:
		if err == nil || !strings.Contains(err.Error(), "stopped midway") || !strings.Contains(err.Error(), "uninstall --remove-data") || !strings.Contains(err.Error(), "vaultsync setup") {
			t.Fatalf("a run whose engine went while it waited: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the run did not end")
	}
	if _, err := os.Stat(a.lay.Syncthing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the run put an engine back: %v", err)
	}
	if _, err := os.Stat(a.lay.State); err != nil {
		t.Fatalf("the record went: %v", err)
	}

	// The removal stopped once the data was gone too: the run does not set
	// VaultSync up anew behind it either.
	b, _ := runFixture(t)
	unlockB, err := lockFile(filepath.Join(b.lay.Base, "setup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	endedB := make(chan error, 1)
	go func() { endedB <- b.run(context.Background(), nil) }()
	time.Sleep(300 * time.Millisecond)
	if err := os.Remove(b.lay.Syncthing); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(b.lay.Home); err != nil {
		t.Fatal(err)
	}
	unlockB()
	select {
	case err := <-endedB:
		if !strings.Contains(refusalText(err), "neither the engine's program nor its data is there") || !strings.Contains(refusalText(err), "pairs with your Hub again") {
			t.Fatalf("a run whose engine and data went while it waited: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the run did not end")
	}
	if _, err := os.Stat(b.lay.Home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the run set the engine up anew: %v", err)
	}
}

// status in such a state with the service installed and running says that
// it starts no engine here: a parked service counts as running (#276).
func TestIssue276_StatusSaysARunningServiceStartsNoEngine(t *testing.T) {
	a := installedApp(t, "darwin", &fakeRunner{loaded: true})
	if err := os.Remove(a.lay.Syncthing); err != nil {
		t.Fatal(err)
	}
	out := statusText(t, a)
	if !strings.Contains(out, "but not the engine's program") || !strings.Contains(out, "Background service: running, but it starts no sync engine here") {
		t.Fatalf("status with the service running:\n%s", out)
	}
	// An engine it started before the program went runs on: the engine
	// lock says so.
	unlock, err := lockFile(a.lay.Lock)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if out := statusText(t, a); !strings.Contains(out, "Background service: running, and a sync engine still runs here") {
		t.Fatalf("status with an engine running:\n%s", out)
	}
}

// A bin that is the person's own link — to a disk not mounted, or to a
// folder without the program — is no trace of a removal, which takes a
// linked bin away whole: the engine's data beside it stays an ordinary
// state, and status says nothing of a removal (#276).
func TestIssue276_ABinThatIsTheirOwnLinkIsNoTraceOfARemoval(t *testing.T) {
	for name, target := range map[string]func(t *testing.T) string{
		"a disk not mounted":           func(t *testing.T) string { return filepath.Join(t.TempDir(), "not-mounted", "bin") },
		"a folder without the program": func(t *testing.T) string { return t.TempDir() },
	} {
		t.Run(name, func(t *testing.T) {
			a := stateHere(t, "a record naming the engine", true, true)
			if err := os.RemoveAll(a.lay.Bin); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target(t), a.lay.Bin); err != nil {
				t.Fatal(err)
			}
			st, err := loadState(a.lay.State)
			if err != nil {
				t.Fatal(err)
			}
			if got := engineDataStateOf(a.lay, st); got != engineDataOK {
				t.Fatalf("the person's own bin link reads as state %d", got)
			}
			a.out = &bytes.Buffer{}
			_ = a.status(context.Background())
			if out := a.out.(*bytes.Buffer).String(); strings.Contains(out, "stopped midway") {
				t.Fatalf("status calls the person's own link a removal's trace:\n%s", out)
			}
		})
	}
}

// serviceLogOf runs f with os.Stderr — where the service logs — written to
// a file, and returns what f wrote there.
func serviceLogOf(t *testing.T, f func()) string {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	saved := os.Stderr
	os.Stderr = file
	defer func() { os.Stderr = saved }()
	f()
	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// stateHere makes a's folder one row of the table below (#276): the record
// — none, one naming no engine, or one naming the engine whose program
// changed since, so that setup and run would install it again — the
// engine's program, and the engine's data (its config and database).
func stateHere(t *testing.T, record string, program, data bool) *app {
	t.Helper()
	a, _ := runFixture(t)
	withInstallLocks(t, a.lay)
	switch record {
	case "no agent.json":
		if err := os.Remove(a.lay.State); err != nil {
			t.Fatal(err)
		}
	case "a record naming no engine":
		if err := saveState(a.lay.State, agentState{GUIPort: 1}); err != nil {
			t.Fatal(err)
		}
	case "a record naming the engine":
		st, err := loadState(a.lay.State)
		if err != nil {
			t.Fatal(err)
		}
		st.Syncthing.BinarySHA256 = "changed"
		if err := saveState(a.lay.State, st); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("no such record: %q", record)
	}
	if !program {
		if err := os.Remove(a.lay.Syncthing); err != nil {
			t.Fatal(err)
		}
	}
	if data {
		writeFile(t, filepath.Join(a.lay.Home, "index-v2", "db"), "the engine's database")
	} else if err := os.RemoveAll(a.lay.Home); err != nil {
		t.Fatal(err)
	}
	return a
}

// Every combination of what VaultSync's folder can hold of its engine — no
// agent.json, a record naming no engine, a record naming it; the engine's
// program; its data — and what setup, run and status do there. Decision
// 077 holds the same table. Where a note names a way out, it is taken as
// written, and leads to a setup that goes on (#276).
func TestIssue276_TheStateDecidesSetupRunAndStatus(t *testing.T) {
	origMode, origTerm := serviceMode, stderrIsTerminal
	defer func() { serviceMode, stderrIsTerminal = origMode, origTerm }()
	serviceMode = false
	stderrIsTerminal = func() bool { return true }
	noDownloads(t)
	// Downloads stubbed: setup that goes on ends at its installer with
	// this refusal, run with the stub's error.
	const setupGoesOn, runGoesOn = "no sync engine for this computer", "this test downloads nothing"
	const (
		none     = "no agent.json"
		noEngine = "a record naming no engine"
		engine   = "a record naming the engine"
	)
	const (
		unrecorded     = "agent.json records no engine"
		withoutProgram = "but not the engine's program"
		recordOnly     = "neither the engine's program nor its data"
	)
	for _, c := range []struct {
		record        string
		program, data bool
		state         engineDataState
		setupGoesOn   bool   // to its installer: a fresh install, or the take-back, which compares with the download
		says          string // run's refusal and status's note; "" where both go on
	}{
		{none, false, false, engineDataOK, true, ""},
		{none, true, false, engineDataOK, true, ""},
		{none, false, true, engineDataUnrecorded, false, unrecorded},
		{none, true, true, engineDataUnrecorded, true, unrecorded},
		{noEngine, false, false, engineDataOK, true, ""},
		{noEngine, true, false, engineDataOK, true, ""},
		{noEngine, false, true, engineDataUnrecorded, false, unrecorded},
		{noEngine, true, true, engineDataUnrecorded, true, unrecorded},
		{engine, true, true, engineDataOK, true, ""},
		{engine, true, false, engineDataOK, true, ""},
		{engine, false, true, engineDataWithoutProgram, false, withoutProgram},
		{engine, false, false, engineRecordOnly, true, recordOnly},
	} {
		t.Run(fmt.Sprintf("%s, program %v, data %v", c.record, c.program, c.data), func(t *testing.T) {
			a := stateHere(t, c.record, c.program, c.data)
			st, err := loadState(a.lay.State)
			if err != nil {
				t.Fatal(err)
			}
			if got := engineDataStateOf(a.lay, st); got != c.state {
				t.Fatalf("the state is %d; the table says %d", got, c.state)
			}
			before := snapshot(t, a.lay.Base)

			err = a.setup(context.Background(), nil)
			if c.setupGoesOn {
				if !strings.Contains(refusalText(err), setupGoesOn) {
					t.Fatalf("setup did not go on to its installer: %v", err)
				}
			} else {
				if !strings.Contains(refusalText(err), c.says) {
					t.Fatalf("setup: %v", err)
				}
				unchangedFolder(t, "a refused setup", before, snapshot(t, a.lay.Base))
			}

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			err = a.run(ctx, nil)
			note := refusalText(err)
			if c.says == "" {
				if !strings.Contains(fmt.Sprint(err), runGoesOn) {
					t.Fatalf("run did not go on to its installer: %v", err)
				}
			} else {
				if !strings.Contains(note, c.says) {
					t.Fatalf("run: %v", err)
				}
				unchangedFolder(t, "a refused run", before, snapshot(t, a.lay.Base))
				// As the service it parks, and its log says the state in a
				// fixed line with no path: the service log carries none.
				stderrIsTerminal = func() bool { return false }
				parked, stop := context.WithTimeout(context.Background(), 300*time.Millisecond)
				logged := serviceLogOf(t, func() {
					if err := a.run(parked, nil); err != nil {
						t.Errorf("the service did not park: %v", err)
					}
				})
				stop()
				serviceMode, stderrIsTerminal = false, func() bool { return true }
				if !strings.Contains(logged, c.says) || strings.Contains(logged, a.home) || strings.Contains(logged, tildePath(a.home, a.lay.Home)) {
					t.Fatalf("the service's log line:\n%s", logged)
				}
				unchangedFolder(t, "a parked service", before, snapshot(t, a.lay.Base))
			}

			a.out = &bytes.Buffer{}
			err = a.status(context.Background())
			out := a.out.(*bytes.Buffer).String()
			if c.says == "" {
				for _, said := range []string{unrecorded, withoutProgram, recordOnly} {
					if strings.Contains(out, said) {
						t.Fatalf("status where nothing is wrong:\n%s", out)
					}
				}
				return
			}
			if err != nil || !strings.Contains(out, "Note: ") || !strings.Contains(out, c.says) || !strings.Contains(out, "Background service: ") || strings.Contains(out, "vaultsync start") {
				t.Fatalf("status: %v\n%s", err, out)
			}

			// The ways out, as the note says them: the removal where it is
			// offered — exactly where agent.json is there — and keeping
			// VaultSync.
			if offered := strings.Contains(note, "uninstall --remove-data"); offered != (c.record != none) {
				t.Fatalf("the removal offered: %v, with %s: %s", offered, c.record, note)
			} else if offered {
				b := stateHere(t, c.record, c.program, c.data)
				if _, _, err := removeOwnData(b.lay, nil, "linux"); err != nil {
					t.Fatalf("the removal the note offers: %v", err)
				}
				if _, err := os.Stat(b.lay.Base); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("the removal the note offers left VaultSync's folder: %v", err)
				}
			}
			kept := stateHere(t, c.record, c.program, c.data)
			if strings.Contains(note, "aside") {
				if err := os.Rename(kept.lay.Home, filepath.Join(t.TempDir(), "syncthing")); err != nil {
					t.Fatal(err)
				}
			} else if c.state != engineRecordOnly {
				t.Fatalf("no way to keep VaultSync: %s", note)
			}
			if err := kept.setup(context.Background(), nil); !strings.Contains(refusalText(err), setupGoesOn) {
				t.Fatalf("setup after the way out: %v", err)
			}
		})
	}
}
