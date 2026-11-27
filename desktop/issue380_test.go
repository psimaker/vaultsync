//go:build unix

package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/psimaker/vaultsync/hub/pairing"
	"github.com/psimaker/vaultsync/hub/syncthing"
)

// The setup link, run again to update a computer that already syncs with a
// Hub, ends once VaultSync is current — no code, no search for the Hub —
// and a question in a terminal ends on Ctrl-C (#380, decision 081).

const issue380CodeQuestion = "The code your Hub printed"

// lockedBuffer is what a command prints, read by the test while it runs.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// issue380App is a Linux computer as setup leaves it: VaultSync's engine
// installed and recorded, answered by a fake engine that holds the lock a
// running Syncthing holds on its home (#413), the service manager a fake.
// With syncs, the engine syncs a vault with a Hub, as after a pairing. A
// search for Hubs finds the fake Hub, and is counted.
func issue380App(t *testing.T, syncs bool) (*app, *fakeEngine, *atomic.Int32) {
	t.Helper()
	eng := newFakeEngine(t)
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tmp, "home")
	lay, err := layoutFor("linux", home, envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	if syncs {
		eng.devices = append(eng.devices, syncthing.DeviceConfig{DeviceID: testHubID, Name: "Test Hub"})
		f := syncthingFolder("vs-notes", "Notes", filepath.Join(home, "Vaults", "Notes"))
		f.Devices = []syncthing.FolderDevice{folderDeviceOf(testMyID), folderDeviceOf(testHubID)}
		eng.folders = append(eng.folders, f)
	}
	writeFile(t, lay.Syncthing, "the engine")
	sum, err := fileSHA256(lay.Syncthing)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(lay.Home, "config.xml"), "<configuration><gui><address>"+eng.addr+"</address><apikey>engine-key</apikey></gui></configuration>\n")
	holdLock(t, filepath.Join(lay.Home, syncthingHomeLock))
	_, port, _ := net.SplitHostPort(eng.addr)
	n, _ := strconv.Atoi(port)
	st := agentState{GUIPort: n, Env: map[string]string{}}
	st.Syncthing.Version, st.Syncthing.BinarySHA256 = syncthingVersion, sum
	if err := saveState(lay.State, st); err != nil {
		t.Fatal(err)
	}
	lay.Socket = filepath.Join(shortDir(t), "agent.sock")
	hub := newFakeHub(t, eng)
	searched := &atomic.Int32{}
	a := &app{goos: "linux", home: home, getenv: envOf(nil), lay: lay, out: &bytes.Buffer{},
		svc: service{goos: "linux", home: home, getenv: envOf(nil), lay: lay, run: &fakeRunner{}},
		discover: func(context.Context) ([]pairing.DiscoveredHub, error) {
			searched.Add(1)
			return []pairing.DiscoveredHub{{Address: hub.addr, Name: "Test Hub"}}, nil
		}}
	t.Setenv("PATH", "/usr/bin:/bin")
	return a, eng, searched
}

// fifoTerminal makes a named pipe the terminal a command asks on: nothing
// is answered until typeLine writes a line, as a person who has not typed
// yet.
func fifoTerminal(t *testing.T) (typeLine func(string)) {
	t.Helper()
	path := filepath.Join(shortDir(t), "tty")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	orig := ttyPath
	ttyPath = path
	t.Cleanup(func() { ttyPath = orig })
	return func(line string) {
		f, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			t.Errorf("the terminal: %v", err)
			return
		}
		defer f.Close()
		_, _ = f.WriteString(line + "\n")
	}
}

// printingInBackground runs cmd with a's output in a lockedBuffer and returns that
// buffer and a channel that carries cmd's result.
func printingInBackground(a *app, cmd func() error) (*lockedBuffer, <-chan error) {
	out := &lockedBuffer{}
	a.out = out
	done := make(chan error, 1)
	go func() { done <- cmd() }()
	return out, done
}

// waitFor waits up to d until out holds want or the command ended: seen
// says whether out holds want, ended whether the command returned (with
// err).
func waitFor(out *lockedBuffer, done <-chan error, want string, d time.Duration) (seen, ended bool, err error) {
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		select {
		case err := <-done:
			return strings.Contains(out.String(), want), true, err
		default:
		}
		if strings.Contains(out.String(), want) {
			return true, false, nil
		}
	}
	return false, false, nil
}

// endsWithin waits up to d for the command's result; at the deadline it
// types a code so the question lets go, and fails.
func endsWithin(t *testing.T, done <-chan error, d time.Duration, typeLine func(string), out *lockedBuffer, what string) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		typeLine("TULIP-ANCHOR-42")
		<-done
		t.Fatalf("%s outlasted the interrupt by %s:\n%s", what, d, out)
		return nil
	}
}

// Ctrl-C at a question ends it: the interrupt cancels the command's
// context, and ask and confirm return errInterrupted without waiting for
// Return, on a line of their own. A question asked after the interrupt
// asks nothing. A read the interrupt left waiting hands its line to the
// next question, so no two reads share the terminal (#380).
func TestIssue380_AnInterruptEndsEveryQuestion(t *testing.T) {
	for _, c := range []struct {
		name string
		ask  func(ctx context.Context, tm *term) error
	}{
		{"ask", func(ctx context.Context, tm *term) error { _, err := tm.ask(ctx, "Q: "); return err }},
		{"confirm", func(ctx context.Context, tm *term) error { _, err := tm.confirm(ctx, "Q?"); return err }},
	} {
		t.Run(c.name, func(t *testing.T) {
			pr, pw := io.Pipe()
			t.Cleanup(func() { pw.Close() })
			out := &lockedBuffer{}
			tm := &term{out: out, in: bufio.NewReader(pr)}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- c.ask(ctx, tm) }()
			time.Sleep(50 * time.Millisecond)
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, errInterrupted) {
					t.Fatalf("an interrupted %s returned %v", c.name, err)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("an interrupted %s waits for Return", c.name)
			}
			if !strings.HasPrefix(out.String(), "Q") || !strings.HasSuffix(out.String(), "\n") {
				t.Fatalf("what the interrupted %s printed: %q", c.name, out)
			}

			// Asked again with the context ended: nothing printed, nothing
			// read.
			before := out.String()
			if err := c.ask(ctx, tm); !errors.Is(err, errInterrupted) || out.String() != before {
				t.Fatalf("a question after the interrupt: %v, printed %q", err, strings.TrimPrefix(out.String(), before))
			}

			// The line the person types next goes to the next question.
			go func() { _, _ = io.WriteString(pw, "yes\n") }()
			a, err := tm.ask(context.Background(), "Next: ")
			if err != nil || a != "yes" {
				t.Fatalf("the next question got %q, %v", a, err)
			}
		})
	}
}

// setup's code question on a computer set up but never paired — where the
// question is the one to ask — ends on Ctrl-C: setup says that nothing was
// paired and what stays installed, and returns errInterrupted, which main
// turns into exit 130 (#380).
func TestIssue380_AnInterruptEndsSetupsQuestion(t *testing.T) {
	a, _, _ := issue380App(t, false)
	typeLine := fifoTerminal(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, done := printingInBackground(a, func() error { return a.setup(ctx, nil) })
	if seen, ended, err := waitFor(out, done, issue380CodeQuestion, 30*time.Second); !seen || ended {
		t.Fatalf("setup on a computer never paired did not reach the code question: %v\n%s", err, out)
	}
	cancel()
	err := endsWithin(t, done, 5*time.Second, typeLine, out, "setup's code question")
	if !errors.Is(err, errInterrupted) {
		t.Fatalf("an interrupted setup returned %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "Nothing was paired. The sync engine and the background service stay installed") {
		t.Fatalf("an interrupted setup does not say what stays:\n%s", out)
	}
}

// pair's code question ends on Ctrl-C the same way (#380).
func TestIssue380_AnInterruptEndsPairsQuestion(t *testing.T) {
	a, _, _ := issue380App(t, false)
	typeLine := fifoTerminal(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, done := printingInBackground(a, func() error { return a.pair(ctx, nil) })
	if seen, ended, err := waitFor(out, done, issue380CodeQuestion, 30*time.Second); !seen || ended {
		t.Fatalf("pair did not reach the code question: %v\n%s", err, out)
	}
	cancel()
	err := endsWithin(t, done, 5*time.Second, typeLine, out, "pair's code question")
	if !errors.Is(err, errInterrupted) || !strings.Contains(out.String(), "Nothing was paired.") {
		t.Fatalf("an interrupted pair: %v\n%s", err, out)
	}
}

// An interrupt is never the "go back" an empty answer is: at the menu's
// Another folder… question it ends the pairing, where an empty answer
// shows the menu again (#380).
func TestIssue380_AnInterruptIsNeverGoBack(t *testing.T) {
	eng := newFakeEngine(t)
	hub := newFakeHub(t, eng)
	s, _ := testSession(t, eng, hub, pairOptions{})
	s.opts.code = hub.code
	out := &lockedBuffer{}
	s.t.out = out
	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })
	s.t.in = bufio.NewReader(pr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.run(ctx) }()
	go func() { _, _ = io.WriteString(pw, "A\n") }()
	const folderQuestion = "Folder on this computer (Enter to go back)"
	if seen, ended, err := waitFor(out, done, folderQuestion, 30*time.Second); !seen || ended {
		t.Fatalf("the pairing did not reach %q: %v\n%s", folderQuestion, err, out)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, errInterrupted) {
			t.Fatalf("an interrupt at %q returned %v\n%s", folderQuestion, err, out)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("an interrupt at %q did not end the pairing:\n%s", folderQuestion, out)
	}
	if n := strings.Count(out.String(), "Choose a vault to sync"); n != 1 {
		t.Fatalf("the menu was shown %d times:\n%s", n, out)
	}
	hub.mu.Lock()
	provisions := len(hub.provisions)
	hub.mu.Unlock()
	if provisions != 0 || eng.folderCount() != 0 {
		t.Fatalf("the Hub was asked %d times, %d folders were added", provisions, eng.folderCount())
	}
}
