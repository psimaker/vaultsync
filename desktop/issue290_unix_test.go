//go:build unix

package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The order of run's first steps that decision 085 depends on, driven
// through the real run: vaultsync start leaves a job alone only where its
// run holds the engine lock, so a run must take that lock before anything
// slow, every park must come before it, a run refused at it must hold
// nothing, and the control socket must be served only under it. A change
// to run's first steps that breaks one of these fails here (#290).

// socketRecorded: agent.json names a control socket a run serves.
func socketRecorded(t *testing.T, lay layout) bool {
	t.Helper()
	st, err := loadState(lay.State)
	return err == nil && st.ControlSocket != ""
}

// Every park comes before the engine lock, and so does the wait for
// setup.lock: a parked or waiting run holds no lock, so vaultsync start
// loads it afresh as before rather than leaving it alone (decision 085
// depends on this). The run that does not park is the control: it holds
// the lock within the same time (#290).
func TestIssue290_EveryParkComesBeforeTheEngineLock(t *testing.T) {
	for name, arrange := range map[string]func(t *testing.T) *app{
		"VaultSync's copy beside VaultSync for Mac (decision 053)": func(t *testing.T) *app {
			a, _ := copyBesideApp(t, true)
			return a
		},
		"a build older than the one set up (#264)": func(t *testing.T) *app {
			a, _ := runFixture(t)
			newerEngineHere(t, a.lay)
			return a
		},
		"the engine's data without its program (#276)": func(t *testing.T) *app {
			a, _ := runFixture(t)
			if err := os.Remove(a.lay.Syncthing); err != nil {
				t.Fatal(err)
			}
			return a
		},
		"waiting for setup.lock": func(t *testing.T) *app {
			a, _ := runFixture(t)
			unlock, err := lockFile(filepath.Join(a.lay.Base, "setup.lock"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(unlock)
			return a
		},
	} {
		t.Run(name, func(t *testing.T) {
			asTheService(t)
			a := arrange(t)
			ended := make(chan error, 1)
			stop := inBackground(t, func(ctx context.Context) error {
				err := a.run(ctx, nil)
				ended <- err
				return err
			})
			for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
				select {
				case err := <-ended:
					t.Fatalf("the run ended instead of parking: %v", err)
				default:
				}
				if engineLockHeld(a.lay) || socketRecorded(t, a.lay) {
					t.Fatal("a parked run holds the engine lock or serves a socket")
				}
			}
			// A park ends without an error when stopped; the wait for
			// setup.lock ends with the stop's.
			if err := stop(); err != nil && !errors.Is(err, context.Canceled) {
				t.Fatalf("a parked run ended with %v when stopped", err)
			}
		})
	}
	t.Run("the control: a run that does not park", func(t *testing.T) {
		asTheService(t)
		a, _ := runFixture(t)
		stop := inBackground(t, func(ctx context.Context) error { return a.run(ctx, nil) })
		// Milliseconds here; the limit only keeps a loaded machine from
		// failing the control.
		e2eWait(t, "the engine lock", 5*time.Second, func() bool { return engineLockHeld(a.lay) })
		_ = stop()
	})
}

// A run takes the engine lock before anything slow: while its pin
// downloads, it holds the lock and serves no socket yet, which is what
// vaultsync start leaves alone (decision 085 depends on this) (#290).
func TestIssue290_ARunHoldsTheEngineLockWhileItsPinDownloads(t *testing.T) {
	a, _ := runFixture(t)
	writeFile(t, a.lay.Syncthing, "a changed engine") // installed again from the pin
	asked := make(chan struct{}, 1)
	pinServedBy(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case asked <- struct{}{}:
		default:
		}
		<-r.Context().Done() // a download that hangs
	})
	inATerminal(t)
	stop := inBackground(t, func(ctx context.Context) error { return a.run(ctx, nil) })
	select {
	case <-asked:
	case <-time.After(15 * time.Second):
		t.Fatal("the run did not begin its pin's download")
	}
	if !engineLockHeld(a.lay) {
		t.Fatal("a run downloads its pin without the engine lock")
	}
	if socketRecorded(t, a.lay) {
		t.Fatal("a run serves its socket before its install")
	}
	_ = stop()
}

// A run refused at the engine lock holds nothing afterwards — not
// setup.lock, no socket, no engine — so its job shows no living run and
// start loads it afresh (decision 085 depends on this) (#290).
func TestIssue290_ARunRefusedAtTheEngineLockHoldsNothing(t *testing.T) {
	a, _ := runFixture(t)
	holdEngineLock(t, a)
	asTheService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := a.run(ctx, nil); !strings.Contains(refusalText(err), "already running") {
		t.Fatalf("a run beside the lock's holder: %v", err)
	}
	unlock, err := lockFile(filepath.Join(a.lay.Base, "setup.lock"))
	if err != nil {
		t.Fatalf("a refused run keeps setup.lock: %v", err)
	}
	unlock()
	if socketRecorded(t, a.lay) {
		t.Fatal("a refused run left a socket recorded")
	}
	if pids := fakeEnginePIDs(t, a.lay); len(pids) > 0 {
		t.Fatalf("a refused run started an engine: %v", pids)
	}
}

// The control socket is served only under the engine lock, so an answer
// there comes from the run that holds it — the one start left alone — and
// goes with the lock (decision 085 depends on this) (#290).
func TestIssue290_TheSocketIsServedOnlyUnderTheEngineLock(t *testing.T) {
	a, _ := runFixture(t)
	asTheService(t)
	held := make(chan bool, 1)
	beforeSocketPublish = func() { held <- engineLockHeld(a.lay) }
	t.Cleanup(func() { beforeSocketPublish = nil })
	stop := inBackground(t, func(ctx context.Context) error { return a.run(ctx, nil) })
	select {
	case ok := <-held:
		if !ok {
			t.Fatal("the socket is served without the engine lock")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not reach its socket")
	}
	_ = stop()
	if engineLockHeld(a.lay) || socketRecorded(t, a.lay) {
		t.Fatal("the lock or the socket's record outlived the run")
	}
}
