package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/psimaker/vaultsync/desktop/internal/lock"
	"github.com/psimaker/vaultsync/hub/syncthing"
)

// The engine is the agent's own Syncthing: the pinned binary, run as a child
// process with a private home directory, its REST API on 127.0.0.1 only, the
// key from its own config.xml, and a web UI password nobody knows (so other
// accounts on this computer cannot use the web UI either). A Syncthing the
// user runs on their own is never read for its keys, started or stopped.

// ErrEngineRunning means another `vaultsync run` already owns the engine.
var ErrEngineRunning = errors.New("VaultSync's sync engine is already running on this computer")

// errEngineStoppedOnItsOwn: the engine ended without being asked to.
var errEngineStoppedOnItsOwn = errors.New("the sync engine stopped on its own")

type engineOptions struct {
	// loopbackOnly pins the engine to 127.0.0.1 with every way out of the
	// computer switched off. Only tests set it; the CLI has no switch.
	loopbackOnly bool
	// listenPort is the sync port; 0 means Syncthing's default (22000),
	// unless avoidDefaultPorts picks another.
	listenPort int
	// avoidDefaultPorts keeps the engine off 22000 when the user runs a
	// Syncthing of their own, which expects that port.
	avoidDefaultPorts bool
}

type engine struct {
	lay  layout
	opts engineOptions
}

func (e engine) configPath() string { return filepath.Join(e.lay.Home, "config.xml") }

// prepared reports whether the engine already has its identity and config.
// Only a config that is not there means "not prepared": one that cannot be
// looked at (a permission, an I/O error) is treated as there, so nothing
// is generated over it and the read that follows reports the error.
func (e engine) prepared() bool {
	_, err := os.Stat(e.configPath())
	return !errors.Is(err, fs.ErrNotExist)
}

// prepare gives a fresh engine its identity and config, adjusted before its
// first start. Both are made in a staging folder and moved into place in one
// rename only after the adjustments succeeded, so an interrupted prepare
// never leaves an engine that looks ready but runs on stock settings. An
// existing config is never regenerated: it holds the identity the Hub knows.
func (e engine) prepare(ctx context.Context, st *agentState) error {
	// A config that is there and can be looked at is kept; one that is not
	// there is made below; one that cannot be looked at is reported by the
	// read of the folder below — nothing is made over it either way.
	if _, err := os.Stat(e.configPath()); err == nil {
		if st.GUIPort == 0 {
			// agent.json was lost or never written; the port is in the config.
			port, err := e.guiPortFromConfig()
			if err != nil {
				return err
			}
			st.GUIPort = port
		}
		return nil
	}
	entries, err := os.ReadDir(e.lay.Home)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return fmt.Errorf("the sync engine's folder %s cannot be read (%v); nothing in it was changed", e.lay.Home, err)
	case len(entries) > 0:
		return fmt.Errorf("the sync engine's folder %s is incomplete (it has no config.xml); nothing in it was changed — move it aside, then run vaultsync setup again", e.lay.Home)
	}
	staging := e.lay.Home + ".new"
	// Only an interrupted prepare leaves this folder behind; it never held
	// anything but a half-made engine identity that nobody knows yet.
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(staging) // a no-op after the rename
	var pw [24]byte
	if _, err := rand.Read(pw[:]); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, e.lay.Syncthing, "generate",
		"--home="+staging, "--gui-user=vaultsync", "--gui-password=-", "--no-port-probing")
	// The password travels on stdin, never on the command line, and is
	// forgotten right away: only the API key is ever used.
	cmd.Stdin = strings.NewReader(hex.EncodeToString(pw[:]) + "\n")
	cmd.Env = engineEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("syncthing generate: %v: %s", err, lastLine(out))
	}
	port := st.GUIPort
	if port == 0 {
		var err error
		if port, err = freeLoopbackPort(); err != nil {
			return err
		}
	}
	if err := e.adjustFreshConfig(filepath.Join(staging, "config.xml"), port); err != nil {
		return err
	}
	_ = os.Remove(e.lay.Home) // an empty folder from an earlier attempt
	if err := os.Rename(staging, e.lay.Home); err != nil {
		return err
	}
	st.GUIPort = port
	return nil
}

// guiPortFromConfig reads the port the agent wrote into the engine's GUI
// address.
func (e engine) guiPortFromConfig() (int, error) {
	f, err := os.Open(e.configPath())
	if err != nil {
		return 0, err
	}
	defer f.Close()
	gui, err := syncthing.ParseGUIConfig(f)
	if err != nil {
		return 0, fmt.Errorf("read the engine's config: %w", err)
	}
	_, p, err := net.SplitHostPort(strings.TrimSpace(gui.Address))
	port, perr := strconv.Atoi(p)
	if err != nil || perr != nil || port <= 0 || port == 8384 {
		return 0, fmt.Errorf("the engine's config has no VaultSync API address (%q)", gui.Address)
	}
	return port, nil
}

// adjustFreshConfig edits the config.xml `syncthing generate` just wrote —
// before the engine ever starts, so it never reports usage, sends a crash
// report, opens a browser or checks for upgrades (the agent owns upgrades).
// The pinned version's config shape is known; anything else stops setup
// instead of being guessed at.
func (e engine) adjustFreshConfig(path string, guiPort int) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	edits := [][2]string{
		{"<address>127.0.0.1:8384</address>", "<address>127.0.0.1:" + strconv.Itoa(guiPort) + "</address>"},
		{"<startBrowser>true</startBrowser>", "<startBrowser>false</startBrowser>"},
		{"<urAccepted>0</urAccepted>", "<urAccepted>-1</urAccepted>"},
		{"<crashReportingEnabled>true</crashReportingEnabled>", "<crashReportingEnabled>false</crashReportingEnabled>"},
		{"<autoUpgradeIntervalH>12</autoUpgradeIntervalH>", "<autoUpgradeIntervalH>0</autoUpgradeIntervalH>"},
	}
	switch {
	case e.opts.loopbackOnly:
		port := e.opts.listenPort
		edits = append(edits,
			[2]string{"<listenAddress>default</listenAddress>", "<listenAddress>tcp://127.0.0.1:" + strconv.Itoa(port) + "</listenAddress>"},
			[2]string{"<globalAnnounceEnabled>true</globalAnnounceEnabled>", "<globalAnnounceEnabled>false</globalAnnounceEnabled>"},
			[2]string{"<localAnnounceEnabled>true</localAnnounceEnabled>", "<localAnnounceEnabled>false</localAnnounceEnabled>"},
			[2]string{"<relaysEnabled>true</relaysEnabled>", "<relaysEnabled>false</relaysEnabled>"},
			[2]string{"<natEnabled>true</natEnabled>", "<natEnabled>false</natEnabled>"},
		)
	case e.opts.avoidDefaultPorts || e.opts.listenPort != 0:
		port := e.opts.listenPort
		if port == 0 {
			if port, err = freeSyncPort(); err != nil {
				return err
			}
		}
		p := strconv.Itoa(port)
		edits = append(edits, [2]string{"<listenAddress>default</listenAddress>",
			"<listenAddress>tcp://:" + p + "</listenAddress>\n        <listenAddress>quic://:" + p + "</listenAddress>\n        <listenAddress>dynamic+https://relays.syncthing.net/endpoint</listenAddress>"})
	}
	for _, ed := range edits {
		if n := bytes.Count(data, []byte(ed[0])); n != 1 {
			return fmt.Errorf("the new engine's config.xml has an unexpected shape (%q appears %d times) — nothing was started", ed[0], n)
		}
		data = bytes.Replace(data, []byte(ed[0]), []byte(ed[1]), 1)
	}
	return writeFileAtomic(path, data, 0o600)
}

// apiKey reads the engine's API key from its own config.xml.
func (e engine) apiKey() (string, error) {
	f, err := os.Open(e.configPath())
	if err != nil {
		return "", err
	}
	defer f.Close()
	gui, err := syncthing.ParseGUIConfig(f)
	if err != nil {
		return "", fmt.Errorf("read the engine's config: %w", err)
	}
	if strings.TrimSpace(gui.APIKey) == "" {
		return "", errors.New("the engine's config has no API key")
	}
	return strings.TrimSpace(gui.APIKey), nil
}

// client returns a REST client for the engine on its loopback port.
func (e engine) client(st agentState) (*syncthing.Client, error) {
	if st.GUIPort == 0 {
		return nil, errors.New("the sync engine is not set up yet")
	}
	key, err := e.apiKey()
	if err != nil {
		return nil, err
	}
	return syncthing.NewClient("http://127.0.0.1:"+strconv.Itoa(st.GUIPort), key), nil
}

// waitReady polls until the engine answers or the timeout passes.
func waitReady(ctx context.Context, c *syncthing.Client, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := c.Ping(pctx)
		cancel()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the sync engine did not answer within %s: %w", timeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func engineEnv() []string {
	// No default "Sync" folder, ever; upgrades come only with a new agent.
	return append(os.Environ(), "STNODEFAULTFOLDER=1", "STNOUPGRADE=1")
}

// --- running the engine -----------------------------------------------------

// exitRestart is Syncthing's exit code for "restart me" (a config change that
// needs a restart); with --no-restart the supervisor does it.
const exitRestart = 3

// supervise runs the engine until ctx ends: it holds the lock that makes it
// the engine's only owner, restarts the engine when it asks for a restart,
// and returns an error when the engine stops on its own — the service
// manager then restarts the agent after its throttle interval.
func (e engine) supervise(ctx context.Context, st agentState, logf func(string, ...any)) error {
	unlock, err := takeEngineLock(e.lay.Lock)
	if err != nil {
		return err
	}
	defer unlock()
	return e.superviseLocked(ctx, st, logf)
}

// superviseLocked is supervise for a caller that holds the engine lock
// already (run, which serves the control socket under the same lock).
func (e engine) superviseLocked(ctx context.Context, st agentState, logf func(string, ...any)) error {
	if st.GUIPort <= 0 {
		return errors.New("the sync engine is not set up yet — run vaultsync setup")
	}
	for {
		if err := e.leaveATakenPort(&st, logf); err != nil {
			return err
		}
		code, err := e.runOnce(ctx, st.GUIPort, st.Syncthing.Version, logf)
		if ctx.Err() != nil {
			return nil
		}
		if code == exitRestart {
			logf("the engine asked for a restart")
			continue
		}
		if err != nil {
			return fmt.Errorf("the sync engine stopped: %w", err)
		}
		return errEngineStoppedOnItsOwn
	}
}

// leaveATakenPort moves the engine's API port off one another program took
// while the engine was down, and records the new one in agent.json. A run
// calls it as it takes the engine lock and before each start of its
// engine. From the lock on, agent.json names the port the run's engine
// answers on (#413).
func (e engine) leaveATakenPort(st *agentState, logf func(string, ...any)) error {
	if st.GUIPort <= 0 || portFree(st.GUIPort) {
		return nil
	}
	port, err := freeLoopbackPort()
	if err != nil {
		return err
	}
	logf("the engine's API port was taken; moving it to %d", port)
	st.GUIPort = port
	return updateState(e.lay.State, func(s *agentState) { s.GUIPort = port })
}

// runOnce starts the engine and waits for it to exit (or stops it when ctx
// ends). It returns the engine's exit code. running is the Syncthing that
// runs, for the log: the pin, or the older engine a run kept (#267); empty
// is the pin.
func (e engine) runOnce(ctx context.Context, guiPort int, running string, logf func(string, ...any)) (int, error) {
	if running == "" {
		running = syncthingVersion
	}
	cmd := exec.Command(e.lay.Syncthing, "serve",
		"--home="+e.lay.Home,
		"--gui-address=http://127.0.0.1:"+strconv.Itoa(guiPort),
		"--no-browser", "--no-restart", "--no-upgrade",
		"--log-file="+filepath.Join(e.lay.Home, "syncthing.log"),
		"--log-max-size=10485760", "--log-max-old-files=3",
	)
	cmd.Env = engineEnv()
	// Syncthing's log names vaults and paths, so it stays in the engine's
	// private folder (0700): its rotated log file, and a crash's stderr in
	// syncthing-stderr.log. Nothing of it reaches the service log or journal.
	cmd.Stdout = io.Discard
	stderr, err := openStderrLog(filepath.Join(e.lay.Home, "syncthing-stderr.log"))
	if err != nil {
		return -1, err
	}
	defer stderr.Close()
	cmd.Stderr = stderr
	setChildAttrs(cmd)
	if err := cmd.Start(); err != nil {
		return -1, err
	}
	logf("sync engine started (Syncthing %s, pid %d)", strings.TrimPrefix(running, "v"), cmd.Process.Pid)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return exitCode(err), err
	case <-ctx.Done():
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-done:
			return exitCode(err), nil
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill()
			err := <-done
			return exitCode(err), nil
		}
	}
}

// openStderrLog appends to the engine's crash log, starting it over once it
// passes 1 MiB.
func openStderrLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if st, err := os.Stat(path); err == nil && st.Size() > 1<<20 {
		flags |= os.O_TRUNC
	}
	return os.OpenFile(path, flags, 0o600)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

func freeLoopbackPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func portFree(port int) bool {
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

// apiPortForSetup is the API port setup starts the engine on (#413): the
// recorded one, unless another program holds it — the engine started next
// would move off it, and setup would wait at the old one. VaultSync's own
// engine keeps its port: one a vaultsync run holds the engine lock for, and
// one that outlived its run (macOS stops no child when its parent dies),
// told by the lock Syncthing holds on its home while it runs. Neither is
// told by asking the port. A lock that cannot be looked at keeps the port
// too. No port recorded is left to prepare, which picks one.
func apiPortForSetup(lay layout, recorded int) (int, error) {
	if recorded <= 0 || portFree(recorded) {
		return recorded, nil
	}
	for _, held := range []string{lay.Lock, filepath.Join(lay.Home, syncthingHomeLock)} {
		if h, err := lock.Probe(held); err != nil || h {
			return recorded, nil
		}
	}
	return freeLoopbackPort()
}

// engineMayRun says whether an engine of VaultSync's may run here, told by
// its locks and never by asking its port (#413, decision 079):
//   - a vaultsync run holds the engine lock;
//   - with that lock free, a Syncthing that outlived its run holds its lock
//     on the engine's home.
//
// Syncthing's lock is looked at only while no run holds the engine lock.
// Syncthing tries that lock once as it starts and exits if it is taken,
// and a look holds it for an instant. With no run holding the engine lock,
// no Syncthing of VaultSync's can be starting. A lock that cannot be looked
// at is a *lockLookError.
func engineMayRun(lay layout) (bool, error) {
	held, err := lock.Probe(lay.Lock)
	if err != nil {
		return false, &lockLookError{path: lay.Lock, err: err}
	}
	if held {
		return true, nil
	}
	home := filepath.Join(lay.Home, syncthingHomeLock)
	if held, err = lock.Probe(home); err != nil {
		return false, &lockLookError{path: home, err: err}
	}
	return held, nil
}

// waitEngineMayRun is engineMayRun, asked again until an engine of
// VaultSync's may run or the deadline passes: a run takes the engine lock a
// moment after it starts.
func waitEngineMayRun(ctx context.Context, lay layout, deadline time.Time) (bool, error) {
	for {
		may, err := engineMayRun(lay)
		if err != nil || may || time.Now().After(deadline) {
			return may, err
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// lockLookError is a lock of VaultSync's that cannot be looked at: a link
// or a permission in its place.
type lockLookError struct {
	path string
	err  error
}

func (e *lockLookError) Error() string { return e.err.Error() }
func (e *lockLookError) Unwrap() error { return e.err }

// engineLockGrace is how long a run waits for the engine lock before it
// takes it as another run's. A look at the lock holds it for an instant:
// lock.Probe from setup's port choice, status, pair, pause, resume and
// uninstall. That look must not refuse a run of this build that starts in
// the same instant (#413). A service that still runs an older build waits
// for nothing. A variable for the tests.
var engineLockGrace = 250 * time.Millisecond

// takeEngineLock takes the engine lock, waiting engineLockGrace for a look
// that holds it for an instant.
func takeEngineLock(path string) (func(), error) {
	deadline := time.Now().Add(engineLockGrace)
	for {
		unlock, err := lockFile(path)
		if !errors.Is(err, ErrEngineRunning) || time.Now().After(deadline) {
			return unlock, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// syncthingHomeLock is the file Syncthing locks in its home while it runs
// (its LockFile location, ${data}/syncthing.lock; data is the home here).
// TestIssue413_E2EAnEngineLocksItsHome fails when a Syncthing bump moves it.
const syncthingHomeLock = "syncthing.lock"

// freeSyncPort finds a sync port next to Syncthing's default, free for TCP
// and UDP (QUIC), so the user's own Syncthing keeps 22000.
func freeSyncPort() (int, error) {
	for p := 22001; p <= 22100; p++ {
		t, err := net.Listen("tcp", ":"+strconv.Itoa(p))
		if err != nil {
			continue
		}
		u, err := net.ListenPacket("udp", ":"+strconv.Itoa(p))
		t.Close()
		if err != nil {
			continue
		}
		u.Close()
		return p, nil
	}
	return 0, errors.New("no free sync port between 22001 and 22100")
}

func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return lines[len(lines)-1]
}
