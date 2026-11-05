//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fastAppWatch makes the service's look at its app quick for a test.
func fastAppWatch(t *testing.T) {
	t.Helper()
	every, grace := appCheckEvery, appGoneGrace
	appCheckEvery, appGoneGrace = 20*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { appCheckEvery, appGoneGrace = every, grace })
}

// A service that runs from VaultSync for Mac ends when the app goes to the
// Trash — the engine stopped, the run over with errAppGone — instead of
// syncing on until the next login (decision 053): the person who trashed
// the app believes VaultSync stopped, and what they delete next would reach
// every device. While the app stays, the run goes on, and VaultSync's own
// copy is brought to the app's build — the way back without the app must
// not run an older one.
func TestIssue254_ARunFromTheAppEndsWhenTheAppGoesToTheTrash(t *testing.T) {
	a, _ := runFixture(t)
	app := fakeMacApp(t, filepath.Join(a.home, "Applications"), appBundleName, shippedInfoPlist(t))
	a.appEnv = macAppEnv(a.home, filepath.Join(t.TempDir(), "none"), filepath.Join(app, "Contents", "MacOS", "vaultsync"))
	writeFile(t, a.lay.Agent, "the build before")
	fastAppWatch(t)
	ended := make(chan error, 1)
	stop := inBackground(t, func(ctx context.Context) error {
		err := a.run(ctx, nil)
		ended <- err
		return err
	})
	e2eWait(t, "the engine", 15*time.Second, func() bool { return len(fakeEnginePIDs(t, a.lay)) > 0 })
	select {
	case err := <-ended:
		t.Fatalf("the run ended while the app was there: %v", err)
	case <-time.After(10 * appCheckEvery):
	}
	if have, _ := os.ReadFile(a.lay.Agent); !bytes.Equal(have, ownBytes(t)) {
		t.Fatal("a run from the app left VaultSync's copy at another build than the app's")
	}

	trash := filepath.Join(a.home, ".Trash")
	if err := os.MkdirAll(trash, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(app, filepath.Join(trash, appBundleName)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-ended:
		if !errors.Is(err, errAppGone) {
			t.Fatalf("the run ended with %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the run went on syncing after its app went to the Trash")
	}
	for _, pid := range fakeEnginePIDs(t, a.lay) {
		if runs, err := runsFakeEngine(pid, a.lay); runs || err != nil {
			t.Fatalf("the engine (pid %d) outlived the run: %v %v", pid, runs, err)
		}
	}
	_ = stop()
	// The service log says it in so many words, with no path — and the way
	// out for someone who meant to remove VaultSync.
	if got := serviceSummary(errAppGone); got != errAppGone.Error() || strings.Contains(got, a.home) || !strings.Contains(got, "uninstall") {
		t.Fatalf("the service log says %q", got)
	}
}

// The look at the app starts with the run, not once the engine is ready:
// an app put in the Trash while the run still waits for a setup to finish
// ends the run before any engine starts — whatever the run would have found
// of the bundle by then, a program started from VaultSync for Mac is
// watched (fail closed).
func TestIssue254_ARunFromTheAppIsWatchedFromItsFirstMoment(t *testing.T) {
	a, _ := runFixture(t)
	app := fakeMacApp(t, filepath.Join(a.home, "Applications"), appBundleName, shippedInfoPlist(t))
	a.appEnv = macAppEnv(a.home, filepath.Join(t.TempDir(), "none"), filepath.Join(app, "Contents", "MacOS", "vaultsync"))
	fastAppWatch(t)
	unlockSetup, err := lockFile(filepath.Join(a.lay.Base, "setup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlockSetup()
	ended := make(chan error, 1)
	stop := inBackground(t, func(ctx context.Context) error {
		err := a.run(ctx, nil)
		ended <- err
		return err
	})
	defer func() { _ = stop() }()
	time.Sleep(5 * appCheckEvery) // waiting for setup.lock
	if err := os.RemoveAll(app); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-ended:
		if !errors.Is(err, errAppGone) {
			t.Fatalf("the run ended with %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("a run whose app went while it waited went on waiting")
	}
	if pids := fakeEnginePIDs(t, a.lay); len(pids) > 0 {
		t.Fatalf("an engine started for a run whose app was gone: %v", pids)
	}
}

// The setup link's update leaves the app's place empty for an instant
// between its two renames: a program missing at one look and back at the
// next is no departure. Missing at both is.
func TestIssue254_AnUpdateIsNoTripToTheTrash(t *testing.T) {
	there, err := os.Lstat(os.Args[0]) // a program, as the app's agent is
	if err != nil {
		t.Fatal(err)
	}
	// The update's instant falls on one look, whichever: the first ones
	// after the start are the replaced look and the missing look of the
	// first round, and either may meet it.
	for miss := int32(2); miss <= 5; miss++ {
		var looks atomic.Int32
		lstat := func(string) (fs.FileInfo, error) {
			if looks.Add(1) == miss {
				return nil, fs.ErrNotExist
			}
			return there, nil
		}
		ctx, ended, stop := whileProgramStays(context.Background(), "/Applications/"+appBundleName+"/Contents/MacOS/vaultsync", lstat, time.Millisecond, 5*time.Millisecond)
		deadline := time.Now().Add(2 * time.Second)
		for looks.Load() < miss+10 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if ctx.Err() != nil || ended() != programHeld {
			t.Fatalf("a program missing at look %d alone ended the run (%d looks, %v)", miss, looks.Load(), ended())
		}
		stop()
	}
	var gone atomic.Bool
	lstat := func(string) (fs.FileInfo, error) {
		if gone.Load() {
			return nil, fs.ErrNotExist
		}
		return there, nil
	}
	ctx, ended, stop := whileProgramStays(context.Background(), "/Applications/"+appBundleName+"/Contents/MacOS/vaultsync", lstat, time.Millisecond, 5*time.Millisecond)
	defer stop()
	gone.Store(true)
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a program missing at two looks did not count as gone")
	}
	if ended() != programGone {
		t.Fatal("the end was not said to be the app's departure")
	}
	// Any program but VaultSync for Mac's agent is not watched.
	for _, prog := range []string{"/usr/local/bin/vaultsync", "/Applications/" + appBundleName + "/Contents/MacOS/vaultsync-tray", "/Users/me/Library/Application Support/VaultSync/bin/vaultsync"} {
		env := appEnv{goos: "darwin", executable: func() (string, error) { return prog, nil }, lstat: func(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist }}
		watched, ended, stop := watchOwnApp(context.Background(), env)
		time.Sleep(20 * time.Millisecond)
		if watched.Err() != nil || ended() != programHeld {
			t.Fatalf("%s was watched as the app's agent", prog)
		}
		stop()
	}
}

// copyBesideApp is a run fixture whose run is VaultSync's own copy, with an
// app that counts in ~/Applications when withApp; it returns the app's
// path as the look at Applications finds it.
func copyBesideApp(t *testing.T, withApp bool) (*app, string) {
	t.Helper()
	a, _ := runFixture(t)
	a.appEnv = macAppEnv(a.home, filepath.Join(t.TempDir(), "none"), a.lay.Agent)
	fastAppWatch(t)
	orig := serviceMode
	serviceMode = true // the service, not a terminal: it parks
	t.Cleanup(func() { serviceMode = orig })
	if !withApp {
		return a, ""
	}
	app := fakeMacApp(t, filepath.Join(a.home, "Applications"), appBundleName, shippedInfoPlist(t))
	resolved, err := filepath.EvalSymlinks(app)
	if err != nil {
		t.Fatal(err)
	}
	return a, resolved
}

// parkedRun runs a in the background and checks it parks: no engine, not
// ended, ends when stopped.
func parkedRun(t *testing.T, a *app) {
	t.Helper()
	ended := make(chan error, 1)
	stop := inBackground(t, func(ctx context.Context) error {
		err := a.run(ctx, nil)
		ended <- err
		return err
	})
	select {
	case err := <-ended:
		t.Fatalf("the run ended instead of parking: %v", err)
	case <-time.After(20 * appCheckEvery):
	}
	if pids := fakeEnginePIDs(t, a.lay); len(pids) > 0 {
		t.Fatalf("an engine started beside the app: %v", pids)
	}
	if err := stop(); err != nil {
		t.Fatalf("a parked run ended with %v when stopped", err)
	}
}

// On a Mac, while VaultSync for Mac is installed only its agent runs the
// engine (decision 053 (8)): the service on VaultSync's own copy parks — no
// engine — and notes it, until it is stopped.
func TestIssue254_TheCopyDoesNotSyncBesideTheApp(t *testing.T) {
	a, app := copyBesideApp(t, true)
	parkedRun(t, a)
	if st, err := loadState(a.lay.State); err != nil || st.ParkedBesideApp != app {
		t.Fatalf("the park was not noted: %q %v", st.ParkedBesideApp, err)
	}
}

// A park outlasts the app: put in the Trash again, the copy does not
// resume syncing on its own — only a setup sets it up anew.
func TestIssue254_AParkedCopyStaysParkedWithoutTheApp(t *testing.T) {
	a, _ := copyBesideApp(t, false)
	if err := updateState(a.lay.State, func(s *agentState) { s.ParkedBesideApp = "/Applications/" + appBundleName }); err != nil {
		t.Fatal(err)
	}
	parkedRun(t, a)
}

// An app that comes while the copy syncs — dragged into Applications, or
// put back from the Trash after "go on without the app" — stops the copy's
// engine within moments and notes the park, so the next start parks.
func TestIssue254_AnAppThatComesStopsTheCopy(t *testing.T) {
	a, _ := copyBesideApp(t, false)
	ended := make(chan error, 1)
	stop := inBackground(t, func(ctx context.Context) error {
		err := a.run(ctx, nil)
		ended <- err
		return err
	})
	defer func() { _ = stop() }()
	e2eWait(t, "the engine", 15*time.Second, func() bool { return len(fakeEnginePIDs(t, a.lay)) > 0 })
	app := fakeMacApp(t, filepath.Join(a.home, "Applications"), appBundleName, shippedInfoPlist(t))
	select {
	case err := <-ended:
		if err != nil {
			t.Fatalf("the run ended with %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the copy went on syncing beside an app that came")
	}
	for _, pid := range fakeEnginePIDs(t, a.lay) {
		if runs, err := runsFakeEngine(pid, a.lay); runs || err != nil {
			t.Fatalf("the engine (pid %d) outlived the run: %v %v", pid, runs, err)
		}
	}
	resolved, _ := filepath.EvalSymlinks(app)
	if st, _ := loadState(a.lay.State); st.ParkedBesideApp != resolved {
		t.Fatalf("the park was not noted: %q", st.ParkedBesideApp)
	}
}

// Without an app and without a park, the copy runs; and off a Mac there is
// no app to park beside.
func TestIssue254_TheCopyRunsWithoutTheApp(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			a, _ := copyBesideApp(t, goos == "linux")
			a.appEnv.goos = goos
			stop := inBackground(t, func(ctx context.Context) error { return a.run(ctx, nil) })
			e2eWait(t, "the engine", 15*time.Second, func() bool { return len(fakeEnginePIDs(t, a.lay)) > 0 })
			_ = stop()
		})
	}
}

// A run in a terminal beside the app is refused, with the reason — and
// notes no park: only the service that parks does.
func TestIssue254_ARefusedTerminalRunNotesNoPark(t *testing.T) {
	a, _ := copyBesideApp(t, true)
	serviceMode = false
	orig := stderrIsTerminal
	stderrIsTerminal = func() bool { return true }
	defer func() { stderrIsTerminal = orig }()
	err := a.run(context.Background(), nil)
	if !strings.Contains(refusalText(err), "which does not sync beside it") {
		t.Fatalf("a terminal run beside the app: %v", err)
	}
	if st, _ := loadState(a.lay.State); st.ParkedBesideApp != "" {
		t.Fatalf("a refused run noted a park: %q", st.ParkedBesideApp)
	}
}
