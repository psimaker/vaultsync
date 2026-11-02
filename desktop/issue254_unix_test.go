//go:build unix

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A service that runs from VaultSync for Mac ends when the app goes to the
// Trash — the engine stopped, the run over with errAppGone — instead of
// syncing on until the next login (decision 053): the person who trashed
// the app believes VaultSync stopped, and what they delete next would reach
// every device. While the app stays, the run goes on.
func TestIssue254_ARunFromTheAppEndsWhenTheAppGoesToTheTrash(t *testing.T) {
	a, _ := runFixture(t)
	app := fakeMacApp(t, filepath.Join(a.home, "Applications"), appBundleName, shippedInfoPlist(t))
	a.appEnv = macAppEnv(a.home, filepath.Join(t.TempDir(), "none"), filepath.Join(app, "Contents", "MacOS", "vaultsync"))
	orig := appCheckEvery
	appCheckEvery = 20 * time.Millisecond
	defer func() { appCheckEvery = orig }()
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
	// The service log says it in so many words, with no path.
	if got := serviceSummary(errAppGone); got != errAppGone.Error() || strings.Contains(got, a.home) {
		t.Fatalf("the service log says %q", got)
	}
}
