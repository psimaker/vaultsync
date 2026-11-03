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
	var looks atomic.Int32
	var gone atomic.Bool
	there, err := os.Lstat(os.Args[0]) // a program, as the app's agent is
	if err != nil {
		t.Fatal(err)
	}
	lstat := func(string) (fs.FileInfo, error) {
		if looks.Add(1) == 2 || gone.Load() { // the second look falls into the update's instant
			return nil, fs.ErrNotExist
		}
		return there, nil
	}
	ctx, appGone, stop := whileProgramStays(context.Background(), "/Applications/"+appBundleName+"/Contents/MacOS/vaultsync", lstat, time.Millisecond, 5*time.Millisecond)
	defer stop()
	deadline := time.Now().Add(2 * time.Second)
	for looks.Load() < 10 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if ctx.Err() != nil || appGone() {
		t.Fatalf("a program missing at one look counted as gone (%d looks)", looks.Load())
	}
	gone.Store(true)
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a program missing at two looks did not count as gone")
	}
	if !appGone() {
		t.Fatal("the end was not said to be the app's departure")
	}
	// Any program but VaultSync for Mac's agent is not watched.
	for _, prog := range []string{"/usr/local/bin/vaultsync", "/Applications/" + appBundleName + "/Contents/MacOS/vaultsync-tray", "/Users/me/Library/Application Support/VaultSync/bin/vaultsync"} {
		env := appEnv{goos: "darwin", executable: func() (string, error) { return prog, nil }, lstat: func(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist }}
		watched, gone, stop := watchOwnApp(context.Background(), env)
		time.Sleep(20 * time.Millisecond)
		if watched.Err() != nil || gone() {
			t.Fatalf("%s was watched as the app's agent", prog)
		}
		stop()
	}
}
