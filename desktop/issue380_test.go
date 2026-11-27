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
	// A program that runs until it is stopped: setup --no-service starts it
	// as its temporary engine, and the fake answers in its place. A file
	// that cannot run would end that engine before the fake's answer counts.
	writeFile(t, lay.Syncthing, "#!/bin/sh\nexec sleep 120\n")
	if err := os.Chmod(lay.Syncthing, 0o755); err != nil {
		t.Fatal(err)
	}
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
			next, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			a, err := tm.ask(next, "Next: ")
			if err != nil || a != "yes" {
				t.Fatalf("the next question got %q, %v — its line went to another read", a, err)
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

// noTerminal makes a command find no terminal to ask on.
func noTerminal(t *testing.T) {
	t.Helper()
	orig := ttyPath
	ttyPath = filepath.Join(t.TempDir(), "no-tty")
	t.Cleanup(func() { ttyPath = orig })
}

const issue380NothingToPair = "✓ VaultSync is set up, and this computer syncs “Notes” with your Hub — there is nothing to pair."

// setup with no flags — the setup link run again to update — on a computer
// that already syncs a vault with a Hub ends once VaultSync is current: it
// neither searches for the Hub nor asks for a code, with a terminal or
// without one, also with --no-service; it says what syncs and names
// vaultsync pair for another vault or Hub, and succeeds (#380, decision
// 081).
func TestIssue380_SetupOnAComputerThatSyncsAsksForNoCode(t *testing.T) {
	for _, c := range []struct {
		name     string
		terminal bool
		args     []string
	}{
		{"without a terminal", false, nil},
		{"in a terminal", true, nil},
		{"without a terminal, --no-service", false, []string{"--no-service"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, _, searched := issue380App(t, true)
			typeLine := func(string) {}
			if c.terminal {
				typeLine = fifoTerminal(t)
			} else {
				noTerminal(t)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			out, done := printingInBackground(a, func() error { return a.setup(ctx, c.args) })
			asked, ended, err := waitFor(out, done, issue380CodeQuestion, 30*time.Second)
			if asked || !ended {
				// Let the question go: an interrupt, then a line.
				cancel()
				typeLine("TULIP-ANCHOR-42")
				<-done
				t.Fatalf("setup on a computer that syncs asked for the code (or never ended); searched for the Hub %d times:\n%s", searched.Load(), out)
			}
			if err != nil || searched.Load() != 0 {
				t.Fatalf("setup on a computer that syncs: %v, searched for the Hub %d times\n%s", err, searched.Load(), out)
			}
			for _, want := range []string{issue380NothingToPair, "Check on it any time:", "Another vault, or another Hub:  ~/.local/bin/vaultsync pair"} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("setup on a computer that syncs does not say %q:\n%s", want, out)
				}
			}
			if noService := strings.Contains(out.String(), "VaultSync has no background service on this computer"); noService != (c.args != nil) {
				t.Fatalf("the line for a setup without the service: %v\n%s", noService, out)
			}
			if strings.Contains(out.String(), "Syncing is paused") || strings.Contains(out.String(), "is sharing") {
				t.Fatalf("setup says what is not so:\n%s", out)
			}
		})
	}
}

// A pairing flag asks for a pairing: setup goes on to it as before, also on
// a computer that already syncs — a script that sets up a second vault, or
// runs its setup again with the same flags (#228), meets what it met before
// (#380, decision 081).
func TestIssue380_SetupWithAPairingFlagPairsAsBefore(t *testing.T) {
	for _, args := range [][]string{
		{"--code", "TULIP-ANCHOR-42"},
		{"--hub", "127.0.0.1:1"},
		{"--vault", "Notes"},
		{"--create"},
		{"--path", "~/Vaults/Work"},
		{"--yes"},
		{"--name", "Work laptop"},
	} {
		t.Run(args[0], func(t *testing.T) {
			a, _, searched := issue380App(t, true)
			noTerminal(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			out, done := printingInBackground(a, func() error { return a.setup(ctx, args) })
			err := <-done
			if strings.Contains(out.String(), "nothing to pair") || !isRefusal(err) {
				t.Fatalf("setup %v on a computer that syncs: %v, searched for the Hub %d times\n%s", args, err, searched.Load(), out)
			}
			if args[0] != "--hub" && searched.Load() != 1 {
				t.Fatalf("setup %v searched for the Hub %d times", args, searched.Load())
			}
		})
	}
}

// A computer that syncs nothing with a Hub yet still pairs: set up but
// never paired, a Hub paired whose vault is not set up here, a folder
// shared with no other device — and where the engine's configuration
// cannot be read, nothing is decided on it (#380, decision 081).
func TestIssue380_SetupOnAComputerThatSyncsNothingYetPairs(t *testing.T) {
	syncingFolder := func(home string) syncthing.FolderConfig {
		f := syncthingFolder("vs-notes", "Notes", filepath.Join(home, "Vaults", "Notes"))
		f.Devices = []syncthing.FolderDevice{folderDeviceOf(testMyID), folderDeviceOf(testHubID)}
		return f
	}
	hubDevice := syncthing.DeviceConfig{DeviceID: testHubID, Name: "Test Hub"}
	for _, c := range []struct {
		name    string
		prepare func(eng *fakeEngine, home string)
	}{
		{"never paired", func(*fakeEngine, string) {}},
		{"a Hub, no vault", func(eng *fakeEngine, home string) {
			eng.devices = append(eng.devices, hubDevice)
		}},
		{"a folder shared with no other device", func(eng *fakeEngine, home string) {
			f := syncthingFolder("vs-notes", "Notes", filepath.Join(home, "Vaults", "Notes"))
			f.Devices = []syncthing.FolderDevice{folderDeviceOf(testMyID)}
			eng.folders = append(eng.folders, f)
		}},
		{"the configuration cannot be read", func(eng *fakeEngine, home string) {
			eng.devices = append(eng.devices, hubDevice)
			eng.folders = append(eng.folders, syncingFolder(home))
			eng.failFolders = true
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, eng, searched := issue380App(t, false)
			eng.mu.Lock()
			c.prepare(eng, a.home)
			eng.mu.Unlock()
			noTerminal(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			out, done := printingInBackground(a, func() error { return a.setup(ctx, nil) })
			err := <-done
			var r *refusal
			if !errors.As(err, &r) || r.needs != "code" || searched.Load() != 1 || strings.Contains(out.String(), "nothing to pair") {
				t.Fatalf("setup on a computer that syncs nothing yet: %v, searched for the Hub %d times\n%s", err, searched.Load(), out)
			}
		})
	}
}

// What setup says beside it on a computer that syncs: how many vaults, that
// syncing is paused here, and each share the Hub sends that is not set up
// yet, with the command for it — and it still asks nothing (#380).
func TestIssue380_SetupSaysWhatIsPausedAndWhatWaits(t *testing.T) {
	a, eng, searched := issue380App(t, true)
	eng.mu.Lock()
	for i := range eng.devices {
		if eng.devices[i].DeviceID == testHubID {
			eng.devices[i].Paused = true
		}
	}
	f := syncthingFolder("vs-work", "Work", filepath.Join(a.home, "Vaults", "Work"))
	f.Devices = []syncthing.FolderDevice{folderDeviceOf(testMyID), folderDeviceOf(testHubID)}
	eng.folders = append(eng.folders, f)
	eng.mu.Unlock()
	eng.offer("vs-diary", testHubID, "Diary")
	noTerminal(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, done := printingInBackground(a, func() error { return a.setup(ctx, nil) })
	if err := <-done; err != nil || searched.Load() != 0 {
		t.Fatalf("setup: %v, searched for the Hub %d times\n%s", err, searched.Load(), out)
	}
	for _, want := range []string{
		"✓ VaultSync is set up, and this computer syncs 2 vaults with your Hub — there is nothing to pair.",
		"Syncing is paused on this computer: ~/.local/bin/vaultsync resume starts it again.",
		"Your Hub “Test Hub” is sharing “Diary” with this computer, but it is not set up here yet: ~/.local/bin/vaultsync pair sets it up.",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("setup does not say %q:\n%s", want, out)
		}
	}
}
