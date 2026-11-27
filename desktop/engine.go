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
	"sync/atomic"
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
	return waitReadyGated(ctx, c, timeout, nil)
}

// waitReadyGated is waitReady asking the engine only while gate, when not
// nil, says nothing against it: gate's error says why the port is not to be
// asked yet, and is what the wait ends with when the timeout passes first —
// or at once, where it wraps errStopWaiting.
func waitReadyGated(ctx context.Context, c *syncthing.Client, timeout time.Duration, gate func(context.Context) error) error {
	deadline := time.Now().Add(timeout)
	for {
		var err error
		if gate != nil {
			err = gate(ctx)
		}
		if errors.Is(err, errStopWaiting) {
			return err
		}
		if err == nil {
			pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err = c.Ping(pctx)
			cancel()
			if err == nil {
				return nil
			}
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
	return e.superviseWith(ctx, st, logf, nil)
}

// superviseWith is supervise telling rs what it does (setup's engine, whose
// setup waits only for an engine this supervisor started).
func (e engine) superviseWith(ctx context.Context, st agentState, logf func(string, ...any), rs *runState) error {
	unlock, err := e.takeEngineLockLeavingATakenPort(ctx, &st, logf)
	if err != nil {
		return err
	}
	defer unlock()
	return e.superviseLocked(ctx, st, logf, rs)
}

// runState is what a run's supervisor tells the rest of the run (#419,
// decision 086): whether it has started its own engine yet, and whether it
// waits for one an earlier run left. Until it has started its own, the
// recorded port is no engine's this run knows of, and nothing of the run
// asks it.
type runState struct {
	// started: this run has started its own engine, at least once.
	started atomic.Bool
	// earlier: it waits for an engine an earlier run left to end.
	earlier atomic.Bool
}

// engineStarted says whether rs's run has started its own engine; a nil
// rs is no run that tells, and says yes.
func (rs *runState) engineStarted() bool { return rs == nil || rs.started.Load() }

// waitsForAnEarlierEngine says whether rs's run waits for an engine an
// earlier run left.
func (rs *runState) waitsForAnEarlierEngine() bool { return rs != nil && rs.earlier.Load() }

// superviseLocked is supervise for a caller that holds the engine lock
// already (run, which serves the control socket under the same lock).
// rs, when not nil, is told what it does.
func (e engine) superviseLocked(ctx context.Context, st agentState, logf func(string, ...any), rs *runState) error {
	if st.GUIPort <= 0 {
		return errors.New("the sync engine is not set up yet — run vaultsync setup")
	}
	for {
		if !e.waitForAnEarlierEngine(ctx, logf, rs) {
			return nil
		}
		if err := e.leaveATakenPort(&st, logf); err != nil {
			return err
		}
		if rs != nil {
			rs.started.Store(true)
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

// leaveATakenPort moves the engine's API port off a taken one, taken while
// the engine was down, and records the new one in agent.json. A run calls
// it as it takes the engine lock (takeEngineLockLeavingATakenPort) and
// before each start of its engine. From the lock on, agent.json names the
// port the run's engine answers on (#413). The port stays only where a
// Syncthing is seen to hold the lock on the engine's home: an engine an
// earlier run left, which the run then waits for and asks nothing (#419,
// decision 086). The run holds the engine lock, so no Syncthing of
// VaultSync's is starting while it looks. A lock that cannot be looked at
// keeps nothing: the run moves off a taken port, as it did before.
func (e engine) leaveATakenPort(st *agentState, logf func(string, ...any)) error {
	return e.leaveATakenPortWith(st, logf, updateState)
}

// leaveATakenPortWith is leaveATakenPort recording the move with write:
// updateState, or updateStateHeld for a caller that holds state.lock.
func (e engine) leaveATakenPortWith(st *agentState, logf func(string, ...any), write func(string, func(*agentState)) error) error {
	if st.GUIPort <= 0 || portFree(st.GUIPort) || engineHomeHeld(e.lay) {
		return nil
	}
	port, err := freeLoopbackPort()
	if err != nil {
		return err
	}
	logf("the engine's API port was taken; moving it to %d", port)
	st.GUIPort = port
	return write(e.lay.State, func(s *agentState) { s.GUIPort = port })
}

// runStateLockWait is how long a run waits for state.lock as it takes the
// engine lock. Its holders hold it for a moment; past the wait the run
// ends, owning nothing, and the service manager starts it again. A
// variable for the tests.
var runStateLockWait = 10 * time.Second

// errRunStateLockHeld: a run found state.lock held past runStateLockWait.
var errRunStateLockHeld = errors.New("state.lock, the lock on VaultSync's record, stayed held by another VaultSync process, so the sync engine was not started — try again in a moment; the background service starts again by itself")

// takeEngineLockLeavingATakenPort takes the engine lock and leaves a taken
// port, both under state.lock: taken just before the engine lock and let go
// once the port is recorded. A command looks at the engine lock and reads
// the port under state.lock too (lookAtTheEngine), so it does not read the
// recorded port in the instant a run moves off it (#419); a record of a
// control socket an earlier run could not make goes in the same hold, so a
// command takes the new run for one on its way up. The wait for state.lock
// ends with ctx, and after runStateLockWait with errRunStateLockHeld: the
// run then owns nothing, and every later write of agent.json would wait
// for the same holder. The engine lock still comes before anything slow, a
// pin's download among them. A refusal at the engine lock is
// ErrEngineRunning, and the run then owns nothing.
func (e engine) takeEngineLockLeavingATakenPort(ctx context.Context, st *agentState, logf func(string, ...any)) (func(), error) {
	h, err := takeStateLock(ctx, stateLockOf(e.lay.State), runStateLockWait)
	switch {
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case errors.Is(err, errStateLockHeld):
		return nil, errRunStateLockHeld
	case err != nil:
		return nil, err
	}
	defer h.Release()
	unlock, err := takeEngineLock(e.lay.Lock)
	if err != nil {
		return nil, err
	}
	removeEarlierSocketRecord(e.lay)
	if err := e.leaveATakenPortWith(st, logf, updateStateHeld); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}

// earlierEngineLook is how often a run looks again at an engine an earlier
// run left (waitForAnEarlierEngine). A variable for the tests.
var earlierEngineLook = time.Second

// waitForAnEarlierEngine waits while a Syncthing is seen to hold the lock
// on the engine's home, although this run, which holds the engine lock,
// has started none: an engine an earlier run left, which outlived that run
// (#419, decision 086). That engine may sync on. This run starts no second
// engine on the same home, which would stop on that lock at once, and
// looks again every earlierEngineLook. It neither signals that engine nor
// asks any port: rs tells the rest of the run that it waits. A look holds
// Syncthing's lock for an instant, which no Syncthing of VaultSync's
// meets: none starts while this run holds the engine lock. A lock that
// cannot be looked at is not waited for: the engine is started, and
// Syncthing's own lock decides. It returns false when ctx ends first.
func (e engine) waitForAnEarlierEngine(ctx context.Context, logf func(string, ...any), rs *runState) bool {
	said := false
	if rs != nil {
		defer rs.earlier.Store(false)
	}
	for {
		if !engineHomeHeld(e.lay) {
			if said {
				logf("the sync engine an earlier vaultsync run left has ended; this run starts its own")
			}
			return true
		}
		if rs != nil {
			rs.earlier.Store(true)
		}
		if !said {
			said = true
			logf("a sync engine an earlier vaultsync run left behind still holds the engine's folder and may still sync; this run starts its own once that one has ended")
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(earlierEngineLook):
		}
	}
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
// one an earlier run left, told by the lock Syncthing holds on its home
// (engineHomeHeld). Neither is told by asking the port. A lock that cannot
// be looked at keeps the port too. No port recorded is left to prepare,
// which picks one.
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

// engineHomeHeld says whether a Syncthing is seen to hold the lock on the
// engine's home (#419). With no vaultsync run holding the engine lock, or
// with this run holding it before it starts its engine, that Syncthing is
// an engine an earlier run left. Under the background service none
// outlives its run: launchd ends the job's process group and systemd its
// control group. A run in a terminal on a Mac, ended by a kill or a crash,
// leaves its engine running until that engine stops by itself. A lock that
// cannot be looked at is not seen held. Looked at only while this process
// holds the engine lock, or no run does: Syncthing tries that lock once as
// it starts, and a look holds it for an instant (decision 079).
func engineHomeHeld(lay layout) bool {
	held, err := lock.Probe(filepath.Join(lay.Home, syncthingHomeLock))
	return err == nil && held
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
	_, may, err := engineLocks(lay)
	return may, err
}

// engineLocks is engineMayRun, also telling whether it is a vaultsync run
// that holds the engine lock (run).
func engineLocks(lay layout) (run, may bool, err error) {
	held, err := lock.Probe(lay.Lock)
	if err != nil {
		return false, false, &lockLookError{path: lay.Lock, err: err}
	}
	if held {
		return true, true, nil
	}
	home := filepath.Join(lay.Home, syncthingHomeLock)
	if held, err = lock.Probe(home); err != nil {
		return false, false, &lockLookError{path: home, err: err}
	}
	return false, held, nil
}

// stateLockWait is how long a command waits for state.lock before it looks
// at the engine (engineMayRunAtItsPort): a run holds it for a moment as it
// takes the engine lock. A variable for the tests.
var stateLockWait = 2 * time.Second

// errStateLockHeld: state.lock stayed held for all of stateLockWait.
var errStateLockHeld = errors.New("another VaultSync process held it throughout")

// engineMayRunAtItsPort is engineMayRun, with the agent.json read when an
// engine of VaultSync's may run — both under state.lock, which a run holds
// from just before its engine lock until it has recorded the port it moves
// to. A command that sees a run's engine lock then reads the port that
// run's engine answers on, never the recorded port in the instant the run
// moves off it (#419). The lock is waited for at most stateLockWait, and
// not past ctx; one held longer, or one that cannot be looked at, is a
// *lockLookError: not known either way. VaultSync's folder is never made:
// none there is nothing of VaultSync's that runs. A record gone meanwhile
// (a removal) gives back the lock file the look made, and the locks are
// looked at all the same, with no port read: an engine that may run then
// has none recorded. An agent.json that cannot be read is returned as that
// error.
func engineMayRunAtItsPort(ctx context.Context, lay layout) (bool, agentState, error) {
	l, err := lookAtTheEngine(ctx, lay)
	return l.may, l.st, err
}

// engineLook is what a command reads of VaultSync's engine, under
// state.lock, before it asks the engine's port (lookAtTheEngine).
type engineLook struct {
	// may: an engine of VaultSync's may run here (engineMayRun).
	may bool
	// run: a vaultsync run, or setup's own engine, holds the engine lock.
	run bool
	// starting: a vaultsync run — or setup's own engine — holds the engine
	// lock, and nothing says it answers nowhere: a run that has not served
	// its control socket yet is on its way up, its engine perhaps not
	// started — a pin still to install, an engine an earlier run left
	// behind to wait for. A command whose agent does not answer asks no
	// port then (#419, decision 086).
	starting bool
	// st: agent.json, read when an engine may run.
	st agentState
}

// lookAtTheEngine is engineMayRunAtItsPort, also telling whether a run that
// holds the engine lock is on its way up (engineLook.starting). A run that
// holds it and answers nowhere — it recorded that it could not make its
// control socket, or the record is desktop-v0.1.0's, whose run serves none
// — is asked as decision 079 says. A record of a socket that could not be
// made is removed by the run that takes the engine lock, under the same
// state.lock, before it is read here: one an earlier run left is never
// taken for the new run's.
func lookAtTheEngine(ctx context.Context, lay layout) (engineLook, error) {
	path := stateLockOf(lay.State)
	h, err := takeStateLock(ctx, path, stateLockWait)
	switch {
	case ctx.Err() != nil:
		return engineLook{}, ctx.Err()
	case errors.Is(err, fs.ErrNotExist):
		return engineLook{}, nil
	case err != nil:
		return engineLook{}, &lockLookError{path: path, err: err}
	}
	if _, err := os.Stat(lay.State); errors.Is(err, fs.ErrNotExist) {
		_, _ = h.Discard()
		may, err := engineMayRun(lay)
		return engineLook{may: may}, err
	}
	defer h.Release()
	run, may, err := engineLocks(lay)
	if err != nil || !may {
		return engineLook{}, err
	}
	st, err := loadState(lay.State)
	if err != nil {
		return engineLook{}, err
	}
	return engineLook{may: true, run: run, starting: run && !runAnswersNowhere(lay, st), st: st}, nil
}

// runAnswersNowhere says whether a vaultsync run that holds the engine lock
// runs its engine with no control socket that will answer: it recorded
// that it could not make one (control-socket-error.txt), or the record is
// desktop-v0.1.0's, whose run serves none. Its engine is then asked as
// decision 079 says.
func runAnswersNowhere(lay layout, st agentState) bool {
	if _, err := os.Lstat(lay.SocketErrorFile()); err == nil {
		return true
	}
	return st.Version == v010StateVersion
}

// removeEarlierSocketRecord removes the record of a control socket that
// could not be made, which a run that ended without its way out left: it
// is no longer true once the engine lock is taken again, and a command
// reads it as "this run answers nowhere" (lookAtTheEngine). Called under
// state.lock, right after the engine lock; a record that cannot be removed
// stays, and the run says so as it makes its socket.
func removeEarlierSocketRecord(lay layout) {
	_ = os.Remove(lay.SocketErrorFile())
}

// takeStateLock takes state.lock without making VaultSync's folder,
// waiting at most wait for a holder to let go, and not past ctx.
func takeStateLock(ctx context.Context, path string, wait time.Duration) (*lock.Handle, error) {
	deadline := time.Now().Add(wait)
	for {
		h, err := lockFileNoCreate(path)
		if err == nil && ctx.Err() != nil {
			// Taken as ctx ended: let go, so no caller holds it unseen.
			h.Release()
			return nil, ctx.Err()
		}
		if !errors.Is(err, ErrEngineRunning) {
			return h, err
		}
		if time.Now().After(deadline) {
			return nil, errStateLockHeld
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// waitEngineMayRun is engineMayRunAtItsPort, asked again until an engine of
// VaultSync's may run or the deadline passes: a run takes the engine lock a
// moment after it starts.
func waitEngineMayRun(ctx context.Context, lay layout, deadline time.Time) (bool, agentState, error) {
	for {
		may, st, err := engineMayRunAtItsPort(ctx, lay)
		if err != nil || may || time.Now().After(deadline) {
			return may, st, err
		}
		select {
		case <-ctx.Done():
			return false, agentState{}, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// lockLookError is a lock of VaultSync's that cannot be looked at: a link
// or a permission in its place, or state.lock held past stateLockWait.
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
