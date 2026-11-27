// vaultsync is VaultSync's desktop agent: it brings its own pinned Syncthing,
// pairs this computer with a VaultSync Hub by code, syncs Obsidian vaults
// under VaultSync's data-safety rules and runs as a background service.
//
//	vaultsync                 in a terminal: the same as `vaultsync setup`
//	vaultsync setup           install the sync engine and the background
//	                          service, pair with your Hub, choose a vault
//	vaultsync pair            sync another vault (or another Hub)
//	vaultsync status          what syncs where
//	vaultsync pause | resume  pause or resume syncing (the service keeps running)
//	vaultsync stop | start    stop or start the background service
//	vaultsync uninstall       remove the background service (vaults stay)
//	vaultsync run             the background service itself
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	layoutpkg "github.com/psimaker/vaultsync/desktop/internal/layout"
	"github.com/psimaker/vaultsync/desktop/internal/lock"
	"github.com/psimaker/vaultsync/hub/join"
	"github.com/psimaker/vaultsync/hub/pairing"
	"github.com/psimaker/vaultsync/hub/syncthing"
)

// version is stamped by the release build.
var version = "dev"

func main() {
	if err := runCLI(os.Args[1:]); err != nil {
		var r *refusal
		switch {
		case errors.Is(err, errCancelled):
			os.Exit(2)
		case errors.Is(err, errInterrupted):
			// Ctrl-C at a question: the terminal shows it, and the command
			// said what stays (#380). 128 + SIGINT, as a shell reports it.
			os.Exit(130)
		case errors.Is(err, errTerminated):
			// A termination signal at a question: nobody is there to read
			// words. 128 + SIGTERM.
			os.Exit(143)
		case serviceMode:
			// A log or the journal: a fixed summary per cause that carries
			// no path or name; the whole message stays in VaultSync's own
			// folder.
			reportServiceError(os.Stderr, err)
			if stateDirShown != "" {
				_ = writeFileAtomic(filepath.Join(stateDirShown, "last-error.txt"), []byte(redactPaths(err.Error())+"\n"), 0o600)
			}
		case errors.As(err, &r):
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, redactPaths(r.msg))
		default:
			fmt.Fprintln(os.Stderr, "error:", redactPaths(err.Error()))
		}
		os.Exit(1)
	}
}

// stateDirShown is VaultSync's folder when the service named one outside the
// home folder (run --state-dir); printed paths below it are shortened too.
var stateDirShown string

// serviceMode is set by `run` when its output goes to a log or the journal
// (stderr is no terminal): it then prints no free text at all.
var serviceMode bool

// reportServiceError writes one summary line per cause of err — a run
// that ended with the engine's failure and a cleanup that failed says
// both, where one summary of the join would name only the first.
func reportServiceError(w io.Writer, err error) {
	for _, c := range eachCause(err) {
		fmt.Fprintln(w, "error:", serviceSummary(c))
	}
}

// serviceSummary describes a failure of the background service from the
// error's kind alone — never from text that could hold a path or a name.
func serviceSummary(err error) string {
	var pe *fs.PathError
	var le *os.LinkError
	var se *os.SyscallError
	var ee *exec.ExitError
	var sm summarized
	switch {
	case errors.As(err, &sm):
		return sm.Summary()
	case errors.Is(err, ErrChecksumMismatch):
		return ErrChecksumMismatch.Error() + " — nothing was installed"
	case errors.Is(err, ErrEngineRunning):
		return ErrEngineRunning.Error()
	case errors.Is(err, ErrNoPin):
		return ErrNoPin.Error()
	case errors.Is(err, errEngineStoppedOnItsOwn):
		return errEngineStoppedOnItsOwn.Error()
	case errors.Is(err, errAppGone):
		return errAppGone.Error()
	case errors.Is(err, errOlderAgent):
		return errOlderAgent.Error()
	case errors.As(err, &ee):
		return "the sync engine stopped (" + ee.String() + ")"
	case errors.As(err, &pe):
		return "a file operation failed (" + pe.Op + ": " + pe.Err.Error() + ")"
	case errors.As(err, &le):
		return "a file operation failed (" + le.Op + ": " + le.Err.Error() + ")"
	case errors.As(err, &se):
		return "a system call failed (" + se.Syscall + ": " + se.Err.Error() + ")"
	}
	return "the background service could not run the sync engine"
}

// summarized is an error that says what it is without any path, for the
// service log.
type summarized interface {
	error
	Summary() string
}

// redactPaths shortens the paths in what is printed: under the background
// service that output lands in a log or the journal, which should carry
// neither the account's home folder nor where VaultSync keeps its files.
// VaultSync's folder becomes <VaultSync folder>, the home folder ~.
func redactPaths(msg string) string {
	home, _ := os.UserHomeDir()
	return layoutpkg.Shorten(msg, stateDirShown, home)
}

func usage(w io.Writer) {
	fmt.Fprint(w, `vaultsync — keeps your Obsidian vaults in sync with your VaultSync Hub

  vaultsync setup        install the sync engine and the background service,
                         pair with your Hub and choose a vault; run again on
                         a computer that syncs, it updates VaultSync and pairs
                         only when a flag below other than --no-service is given
  vaultsync pair         sync another vault, or pair with another Hub
  vaultsync status       what syncs where, and whether your Hub is connected
  vaultsync pause        pause syncing on this computer (until vaultsync resume)
  vaultsync resume       resume it
  vaultsync stop         stop the background service (until vaultsync start)
  vaultsync start        start it again
  vaultsync uninstall    stop and remove the background service; your vaults stay
         [--remove-data] also remove VaultSync's settings, pairing identity and
                         sync database on this computer. Never removes vault files.
  vaultsync version

Flags for setup and pair:
  --code WORD-WORD-NN   the code your Hub printed (asked when missing)
  --hub IP[:PORT]       skip the search and use the Hub at this address
  --vault NAME          the vault on your Hub (with --create: the new vault's name)
  --create              start --vault as a new vault on your Hub (refused when
                        your Hub already has one of that name)
  --path FOLDER         this computer's folder for the vault
  --yes                 agree to sync a --path that already holds files with a
                        new vault on your Hub
  --name NAME           how this computer appears on your Hub
  --no-service          (setup) no background service; run "vaultsync run" yourself
`)
}

func runCLI(args []string) error {
	cmd := "setup"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "version", "--version":
		fmt.Printf("vaultsync %s (Syncthing %s)\n", version, strings.TrimPrefix(syncthingVersion, "v"))
		return nil
	case "help", "--help", "-h":
		usage(os.Stdout)
		return nil
	}
	app, err := newApp()
	if err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	switch cmd {
	case "setup":
		return app.setup(ctx, args)
	case "pair":
		return app.pair(ctx, args)
	case "status":
		return app.status(ctx)
	case "pause":
		return app.pauseSync(ctx, true)
	case "resume":
		return app.pauseSync(ctx, false)
	case "stop":
		return app.stopService()
	case "start":
		return app.startCommand(args)
	case "uninstall":
		return app.uninstall(ctx, args)
	case "run":
		return app.run(ctx, args)
	}
	usage(os.Stderr)
	return fmt.Errorf("unknown command %q", cmd)
}

// signalContext is a command's context: it ends on Ctrl-C (SIGINT) or a
// termination signal (SIGTERM), with the signal as its cause, so that a
// question can tell the person's interrupt from a stop (#380). Like
// signal.NotifyContext, which names the signal only in an unexported error:
// later signals are caught too, until stop.
func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case s := <-ch:
			cancel(stoppedBy{s})
		case <-ctx.Done():
		}
	}()
	return ctx, func() {
		cancel(nil)
		signal.Stop(ch)
	}
}

// stoppedBy is the cause of a command's context that a signal ended.
type stoppedBy struct{ sig os.Signal }

func (e stoppedBy) Error() string { return e.sig.String() + " signal received" }

// app is the live environment: this computer, this user.
type app struct {
	goos, home string
	getenv     func(string) string
	lay        layout
	svc        service
	out        io.Writer
	// appEnv is where setup looks for VaultSync for Mac around this
	// program (appbundle.go); the zero value finds none.
	appEnv appEnv
	// discover replaces the search for Hubs on this network; a test sets it.
	discover func(context.Context) ([]pairing.DiscoveredHub, error)
}

func newApp() (*app, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	lay, err := layoutFor(runtime.GOOS, home, os.Getenv)
	if err != nil {
		return nil, err
	}
	a := &app{goos: runtime.GOOS, home: home, getenv: os.Getenv, lay: lay, out: os.Stdout, appEnv: liveAppEnv(runtime.GOOS, home)}
	a.svc = service{goos: a.goos, home: home, uid: os.Getuid(), getenv: os.Getenv, lay: lay, run: execRunner{}}
	return a, nil
}

func (a *app) userSyncthing() (userSyncthing, bool) {
	return findUserSyncthing(a.goos, a.home, a.getenv)
}

// engineOptionsFor sets up the engine an app runs: Syncthing's own ports,
// off 22000 beside the person's own Syncthing. A variable so the end-to-end
// tests can pin the engine setup makes to loopback.
var engineOptionsFor = func(hasUserSyncthing bool) engineOptions {
	return engineOptions{avoidDefaultPorts: hasUserSyncthing}
}

func (a *app) engine() engine {
	_, hasUser := a.userSyncthing()
	return engine{lay: a.lay, opts: engineOptionsFor(hasUser)}
}

func pairFlags(name string, opts *pairOptions) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.code, "code", "", "")
	fs.StringVar(&opts.hub, "hub", "", "")
	fs.StringVar(&opts.vault, "vault", "", "")
	fs.BoolVar(&opts.create, "create", false, "")
	fs.StringVar(&opts.path, "path", "", "")
	fs.BoolVar(&opts.yes, "yes", false, "")
	fs.StringVar(&opts.name, "name", "", "")
	return fs
}

func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%v — see vaultsync help", err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected %q — see vaultsync help", fs.Arg(0))
	}
	return nil
}

// --- setup ------------------------------------------------------------------

func (a *app) setup(ctx context.Context, args []string) error {
	var opts pairOptions
	fs := pairFlags("setup", &opts)
	noService := fs.Bool("no-service", false, "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	asksToPair := pairingFlagGiven(fs)
	t, closeTerm := openTerm(a.out)
	defer closeTerm()
	t.blank()
	t.say("  VaultSync — keeps your Obsidian vaults in sync with your Hub.")
	t.say("  Your vaults stay where they are; VaultSync keeps its own files in %s.", tildePath(a.home, a.lay.Base))
	t.blank()

	// One setup at a time: two would install and generate the engine's
	// identity over each other. Taken as pair takes its lock: a setup that
	// stops before its engine is in place takes back what the taking made
	// (#276) — never a lock file that was there before.
	setupLock := filepath.Join(a.lay.Base, "setup.lock")
	_, setupLockErr := os.Stat(setupLock)
	setupLockWasThere := setupLockErr == nil
	setupHandle, undoSetupLock, err := lockHandleMaking(setupLock)
	if errors.Is(err, ErrEngineRunning) {
		return refuse("Another vaultsync setup is running on this computer — wait for it to finish.")
	}
	if err != nil {
		return err
	}
	unlockSetup := setupHandle.Release
	// What the taking made goes again; a lock file that was there stays
	// (the undo would discard it too).
	giveBackSetupLock := func() {
		if !setupLockWasThere {
			undoSetupLock()
		}
		unlockSetup()
	}
	// Also without the service: the copy and the command would follow this
	// other build.
	if err := a.refuseBesideAnotherBuild(); err != nil {
		giveBackSetupLock()
		return err
	}
	st, engineChanged, err := a.ensureEngine(ctx, t)
	if err != nil {
		giveBackSetupLock()
		return err
	}
	// The copy as soon as its engine is in place, before the steps that
	// may wait: it is the program a service without VaultSync for Mac runs
	// — never one whose engine a failed download left out — and the way back
	// without the app, which must not stay behind it should this setup stop
	// at a pairing in progress. Whether the service gets a new program is
	// agent.json's record (Started), not the copy's.
	if _, err := installAgentCopy(a.lay.Agent); err != nil {
		unlockSetup()
		return fmt.Errorf("could not install VaultSync into %s: %w", tildePath(a.home, a.lay.Bin), err)
	}
	eng := a.engine()
	// The engine started below moves off an API port another program
	// holds, and this setup would wait at the old one (#413): the port is
	// chosen here, so a fresh config gets it and agent.json records it. A
	// record without a port beside a config has the config's port, which
	// the choice holds like a recorded one.
	if _, err := os.Stat(eng.configPath()); err == nil && st.GUIPort == 0 {
		port, err := eng.guiPortFromConfig()
		if err != nil {
			unlockSetup()
			return fmt.Errorf("could not set up the sync engine: %w", err)
		}
		st.GUIPort = port
	}
	port, err := apiPortForSetup(a.lay, st.GUIPort)
	if err != nil {
		unlockSetup()
		return fmt.Errorf("could not set up the sync engine: %w", err)
	}
	st.GUIPort = port
	if err := eng.prepare(ctx, &st); err != nil {
		unlockSetup()
		return fmt.Errorf("could not set up the sync engine: %w", err)
	}
	// VaultSync's copy is this build now: agent.json takes its format.
	if err := updateState(a.lay.State, func(s *agentState) { s.GUIPort = st.GUIPort; raiseStateFormat(s) }); err != nil {
		unlockSetup()
		return err
	}
	// The shell's view of where the user's Syncthing, Obsidian and the
	// cloud clients keep their settings — for a pairing run by the service.
	// Written under the pairing lock, which every reader of the record
	// takes: a pairing in flight keeps the places it loaded.
	st.Env = recordGuardEnv(a.getenv)
	pairLock, err := waitForLock(ctx, filepath.Join(a.lay.Base, "pair.lock"), 2*time.Minute, lockHandle)
	if err != nil {
		unlockSetup()
		if errors.Is(err, ErrEngineRunning) {
			return refuse("Another vaultsync pair is running on this computer — wait for it to finish, then run the setup again.")
		}
		return err
	}
	unlockPair := pairLock.Release
	err = updateState(a.lay.State, func(s *agentState) { s.Env = st.Env })
	unlockPair()
	if err != nil {
		unlockSetup()
		return err
	}
	unlockSetup()
	// An existing service moves into the app the first time setup runs
	// from it: its file changes, so install loads it afresh. The command
	// link follows.
	exe, bundle, fromApp, notApp := a.serviceProgram()

	var temporary *supervision
	if *noService {
		// A temporary engine for pairing only. Whatever happens next, it is
		// stopped and waited for before setup returns — never left behind
		// (macOS has no way to stop a child when its parent dies).
		temporary = superviseInBackground(ctx, eng, st)
		defer temporary.stop(30 * time.Second)
	} else {
		if err := a.svc.userManagerAvailable(); err != nil {
			return refuse("This computer has no systemd user session (%v), so VaultSync cannot run in the background here. Run vaultsync setup --no-service, then keep vaultsync run running — for example from your desktop's autostart.", err)
		}
		// A program other than the one the service was last started with
		// restarts it: the record, not this setup's refresh of the copy,
		// knows whether an earlier setup stopped before the restart. So does
		// a park the service noted (decision 053 (8)): this setup sets it up
		// anew, and the parked run looks no more — cleared first, so the run
		// launchd starts reads no park.
		agent, agentChanged := a.startedChanged(exe, st.Started.Service)
		parked := false
		if cur, err := loadState(a.lay.State); err == nil && cur.ParkedBesideApp != "" {
			parked = true
			a.recordStarted(func(s *agentState) { s.ParkedBesideApp = "" })
		}
		if _, err := a.svc.install(exe, agentChanged || engineChanged || parked); err != nil {
			if a.goos == "darwin" {
				return refuse("Could not start the background service (%v). Run vaultsync setup in Terminal on this Mac while you are logged in — not over SSH — or use vaultsync setup --no-service and keep vaultsync run running yourself.", err)
			}
			return refuse("Could not start the background service (%v). Check `systemctl --user status vaultsync`, or use vaultsync setup --no-service and keep vaultsync run running yourself.", err)
		}
		a.recordStarted(func(s *agentState) { s.Started.Service = agent })
		t.say("✓ Background service running — it starts again when you log in")
		if notApp != "" {
			// On the copy beside a VaultSync for Mac — one that does not
			// count, or another build: said, so the person knows the app's
			// trip to the Trash will not stop it.
			t.say("  It runs VaultSync's own copy, not %s.", notApp)
		}
		if !fromApp {
			// The menu-bar app opens at login only for a service that runs
			// from it: one on the copy beside the app's icon is the
			// arrangement decision 053 rules out.
			switch found, err := a.svc.uninstallTray(); {
			case err != nil:
				t.say("  The menu-bar icon's start at login could not be removed (%v), so the icon may still open at login although the background service does not run from VaultSync for Mac — run vaultsync setup again.", err)
			case found:
				t.say("✓ The menu-bar icon no longer opens at login: the background service does not run from VaultSync for Mac.")
			}
		}
		if fromApp {
			// The menu-bar app comes with the app; syncing does not depend
			// on it, so a start at login that could not be set up is said,
			// not fatal — and one the person turned off stays off.
			tray, trayChanged := a.startedChanged(bundle.Tray, st.Started.Tray)
			switch err := a.svc.installTray(bundle.Tray, trayChanged); {
			case errors.Is(err, errTrayDisabled):
				t.say("  The menu-bar icon stays off at login, as it was turned off for this account (launchctl enable gui/%d/%s turns it on again).", a.svc.uid, trayLabel)
			case err != nil:
				t.say("  The menu-bar icon could not be set to open at login (%v). Open VaultSync for Mac yourself to see it.", err)
			default:
				a.recordStarted(func(s *agentState) { s.Started.Tray = tray })
				t.say("✓ Menu-bar icon set up — it opens when you log in")
			}
		}
	}
	if a.dropStaleTray() {
		t.say("✓ The menu-bar icon no longer opens at login: VaultSync for Mac is not where its start at login named.")
	}
	// Set up anew: a park a service on the copy noted ends here — the
	// service now runs from the app, or this setup chose the copy with no
	// app (decision 053 (8)).
	a.recordStarted(func(s *agentState) { s.ParkedBesideApp = "" })
	// The command follows the service before anything can stop this setup:
	// a link left in an app that went to the Trash would lead nowhere.
	command := a.installCommand(t, exe)
	client, err := eng.client(st)
	if err != nil {
		return err
	}
	if err := waitEngine(ctx, client, 60*time.Second, temporary); err != nil {
		return fmt.Errorf("the sync engine did not start: %w — see %s", err, a.logHint())
	}
	noServiceNote := func() {
		if *noService {
			t.blank()
			t.say("VaultSync has no background service on this computer: run %s run to keep syncing.", command)
		}
	}
	// The setup link, run again, is how VaultSync is updated, and nobody
	// updating has a code at hand: on a computer that already syncs a vault
	// with a Hub, setup ends here, unless a flag asks for a pairing.
	// vaultsync pair syncs another vault or Hub (#380, decision 081).
	if !asksToPair {
		if synced, ok := syncedWithAHub(ctx, client); ok {
			synced.say(t, command)
			noServiceNote()
			return nil
		}
	}
	_, err = a.pairWith(ctx, t, opts, client, pairOrigin{})
	noServiceNote()
	// Every question comes before the Hub is asked to share or start a
	// vault, and before this computer's engine changes: an interrupt
	// there, like a cancel, leaves nothing paired.
	if errors.Is(err, errCancelled) || errors.Is(err, errInterrupted) {
		t.blank()
		if *noService {
			t.say("Nothing was paired. Run vaultsync pair any time.")
		} else {
			t.say("Nothing was paired. The sync engine and the background service stay installed: run vaultsync pair any time, or vaultsync uninstall to remove the background service.")
		}
		if errors.Is(err, errInterrupted) {
			return err
		}
		return nil
	}
	if err == nil {
		t.blank()
		t.say("  Check on it any time:  %s status", command)
		t.say("  Next device: run the same setup link, or in VaultSync on iPhone tap Add Hub.")
	}
	return err
}

// pairingFlagGiven: a flag of the pairing was given — the code, a Hub, a
// vault, a folder, a name or the consent — whatever its value. Each means a
// pairing only, so setup then pairs, also on a computer that already syncs,
// and an empty value (--code "") meets what it met before (decision 081).
// --no-service is setup's own.
func pairingFlagGiven(fs *flag.FlagSet) bool {
	given := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name != "no-service" {
			given = true
		}
	})
	return given
}

// syncedHere is what this computer's engine syncs with a Hub, for setup run
// again on a computer that already syncs (decision 081).
type syncedHere struct {
	vaults  []string // the vaults' names, as this computer's engine has them
	paused  bool     // syncing is paused on this computer
	waiting []pendingShare
}

// syncedWithAHub reads what the engine's own configuration syncs with a
// Hub: its folders shared with a device other than this computer — every
// other device in VaultSync's engine is a Hub a pairing added, never an
// introducer's. ok is false when no folder is, or when the configuration
// cannot be read: setup then pairs as it did before. Nothing else decides —
// not the pairing journal, which records attempts.
func syncedWithAHub(ctx context.Context, c *syncthing.Client) (syncedHere, bool) {
	myID, err := c.MyID(ctx)
	if err != nil {
		return syncedHere{}, false
	}
	folders, err := c.Folders(ctx)
	if err != nil {
		return syncedHere{}, false
	}
	var s syncedHere
	for _, f := range folders {
		for _, d := range f.Devices {
			if d.DeviceID != myID {
				name := pairing.SanitizeName(f.Label)
				if name == "" {
					name = f.ID
				}
				s.vaults = append(s.vaults, name)
				break
			}
		}
	}
	if len(s.vaults) == 0 {
		return syncedHere{}, false
	}
	// Said beside it, never decided on: a look that fails leaves its line
	// out.
	s.paused, _ = pausedHere(ctx, c)
	s.waiting, _ = pendingFromHubs(ctx, c, myID)
	return s, true
}

// say tells the person updating that VaultSync is set up and what it syncs,
// and the commands for what they may want next.
func (s syncedHere) say(t *term, command string) {
	what := quoted(s.vaults[0])
	if len(s.vaults) > 1 {
		what = fmt.Sprintf("%d vaults", len(s.vaults))
	}
	t.blank()
	t.say("✓ VaultSync is set up, and this computer syncs %s with your Hub — there is nothing to pair.", what)
	if s.paused {
		t.say("  Syncing is paused on this computer: %s resume starts it again.", command)
	}
	// pair refuses while syncing is paused here: the way is resume first.
	way := command + " pair sets it up"
	if s.paused {
		way = fmt.Sprintf("run %s resume, then %s pair, to set it up", command, command)
	}
	for _, w := range s.waiting {
		t.say("  Your Hub %s is sharing %s with this computer, but it is not set up here yet: %s.", quoted(pairing.SanitizeName(w.hub)), quoted(pairing.SanitizeName(w.label)), way)
	}
	t.blank()
	t.say("  Check on it any time:  %s status", command)
	t.say("  Another vault, or another Hub:  %s pair", command)
}

// programGoneNote says what to do when the service's program is gone —
// VaultSync for Mac moved or in the Trash: put it back or set up again;
// go on without the app, or remove VaultSync, with the agent's own copy,
// which setup keeps for that (decision 053).
func (a *app) programGoneNote(prog string) string {
	copyCmd := shellPath(a.home, a.lay.Agent)
	return fmt.Sprintf("it runs %s, which is no longer there (VaultSync for Mac was moved, or put in the Trash). Put VaultSync for Mac back where it was, or run the setup link again; to go on without the app, run %s setup; to remove VaultSync, run %s uninstall", tildePath(a.home, prog), copyCmd, copyCmd)
}

// dropStaleTray removes the menu-bar app's start at login when the program
// it names is gone — the app moved or put in the Trash, and setup run from
// VaultSync's own copy — rather than leave launchd failing on it at every
// login. It says whether it removed one.
func (a *app) dropStaleTray() bool {
	prog := a.svc.trayProgram()
	if prog == "" {
		return false
	}
	if _, err := os.Lstat(prog); !programMissing(err) {
		return false
	}
	found, err := a.svc.uninstallTray()
	return err == nil && found
}

// serviceProgram is the program the background service runs: the agent in
// VaultSync for Mac when setup runs from the app installed in Applications
// — macOS then lists the service, and the permissions it asks for, under
// the app — else VaultSync's own copy (decision 053). Run from a VaultSync
// for Mac that does not count, notApp names it and why.
func (a *app) serviceProgram() (exe string, bundle macApp, fromApp bool, notApp string) {
	bundle, why := appOfThisProgram(a.appEnv)
	if bundle.Path != "" && why == "" {
		return bundle.Agent, bundle, true, ""
	}
	if bundle.Path != "" {
		notApp = fmt.Sprintf("VaultSync for Mac in %s: %s", tildePath(a.home, filepath.Dir(bundle.Path)), why)
	}
	// Run from VaultSync's own copy — the command after an uninstall —
	// with VaultSync for Mac still installed and holding the very same
	// agent: the service goes back into the app. On the copy it would
	// outlive the app's trip to the Trash and keep syncing (decision 053).
	// Another agent than the app's (a bare download run on purpose) still
	// decides for itself.
	if held, ok := installedAppHolding(a.appEnv, a.lay.Agent); ok {
		return held.Agent, held, true, ""
	}
	return a.lay.Agent, macApp{}, false, notApp
}

// refuseBesideAnotherBuild: with VaultSync for Mac installed — one that
// counts — the background service runs from it (decision 053). A setup from
// another build (a bare download of another release, an older copy) would
// put the service on VaultSync's own copy, beside an app whose trip to the
// Trash then does not stop it; it stops before anything changes and says
// how to update. The same build hands the service to the app.
func (a *app) refuseBesideAnotherBuild() error {
	if _, ok := findInstalledApp(a.appEnv); ok {
		return nil
	}
	apps := installedApps(a.appEnv)
	if len(apps) == 0 {
		return nil
	}
	self, err := ownProgram(a.appEnv)
	if err == nil {
		for _, app := range apps {
			if have, err := a.appEnv.readFile(app.Agent); err == nil && sameBuild(have, self) {
				return nil
			}
		}
	}
	return refuse("VaultSync for Mac is installed in %s, where the background service belongs — but this vaultsync is another build. Update VaultSync for Mac with the setup link, or run its own setup (%s setup). Nothing was changed.", tildePath(a.home, filepath.Dir(apps[0].Path)), shellPath(a.home, apps[0].Agent))
}

// ownProgram reads this program, by the path it runs from as it resolves.
func ownProgram(env appEnv) ([]byte, error) {
	if env.executable == nil || env.resolve == nil || env.readFile == nil {
		return nil, fs.ErrNotExist
	}
	self, err := env.executable()
	if err != nil {
		return nil, err
	}
	if self, err = env.resolve(self); err != nil {
		return nil, err
	}
	return env.readFile(self)
}

// supervision is an engine supervisor running in the background; done is
// closed when it returned, err holds what it returned.
type supervision struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

func superviseInBackground(ctx context.Context, eng engine, st agentState) *supervision {
	runCtx, cancel := context.WithCancel(ctx)
	s := &supervision{cancel: cancel, done: make(chan struct{})}
	go func() {
		s.err = eng.supervise(runCtx, st, func(string, ...any) {})
		close(s.done)
	}()
	return s
}

// stop ends the engine and waits for it (at most grace).
func (s *supervision) stop(grace time.Duration) {
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(grace):
	}
}

// waitEngine waits for the engine's API — and stops waiting when the
// supervisor that should start it has already given up (nil: no supervisor
// of ours, the background service starts it).
func waitEngine(ctx context.Context, c *syncthing.Client, timeout time.Duration, sup *supervision) error {
	ready := make(chan error, 1)
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { ready <- waitReady(wctx, c, timeout) }()
	var gaveUp <-chan struct{}
	if sup != nil {
		gaveUp = sup.done
	}
	select {
	case err := <-ready:
		return err
	case <-gaveUp:
		if sup.err != nil {
			return sup.err
		}
		return errors.New("it stopped")
	}
}

// waitForLock takes a lock another VaultSync process may hold for a moment
// (setup preparing the engine), waiting at most timeout, with the given
// taking (lockFile, or one that makes no folder).
func waitForLock(ctx context.Context, path string, timeout time.Duration, take func(string) (*lock.Handle, error)) (*lock.Handle, error) {
	deadline := time.Now().Add(timeout)
	for {
		h, err := take(path)
		if !errors.Is(err, ErrEngineRunning) || time.Now().After(deadline) {
			return h, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// commandLink is where setup links `vaultsync` on the usual per-user command
// path; uninstall --remove-data removes only that link.
func commandLink(home string) string {
	return filepath.Join(home, ".local", "bin", "vaultsync")
}

// installCommand puts `vaultsync` on the usual per-user command path
// (~/.local/bin) as a link to exe, the program the service runs — unless
// something else already has that name — and says how to call it. A link
// setup made earlier to another of VaultSync's programs (the copy, or the
// app's agent) follows to exe: the terminal then runs the program macOS
// asked the local-network permission for.
func (a *app) installCommand(t *term, exe string) string {
	link := commandLink(a.home)
	if target, err := os.Readlink(link); err == nil && target == exe {
		// already ours
	} else if err == nil && a.ownCommandTarget(target) {
		_ = replaceLink(link, exe)
	} else if _, err := os.Lstat(link); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err == nil {
			_ = os.Symlink(exe, link)
		}
	}
	return a.commandFor(exe)
}

// commandFor is how a shell runs exe: "vaultsync" alone only when it
// reaches this very program, the command link where that leads to it,
// otherwise exe by its path.
func (a *app) commandFor(exe string) string {
	if found, err := exec.LookPath("vaultsync"); err == nil {
		agent, aerr := filepath.EvalSymlinks(exe)
		if r, err := filepath.EvalSymlinks(found); err == nil && aerr == nil && r == agent {
			return "vaultsync"
		}
	}
	link := commandLink(a.home)
	if target, err := os.Readlink(link); err == nil && target == exe {
		return tildePath(a.home, link)
	}
	return shellQuote(exe)
}

// ownCommandTarget: a command link pointing there is one setup made — to
// the agent's copy, or to the agent in VaultSync for Mac in /Applications
// or ~/Applications.
func (a *app) ownCommandTarget(target string) bool {
	if target == a.lay.Agent {
		return true
	}
	// The app is looked at where setup looks for it (appEnv), so a test's
	// stand-in for /Applications is the one consulted.
	env := a.appEnv
	if env.lstat == nil || env.resolve == nil || env.readFile == nil {
		env = liveAppEnv(a.goos, a.home)
	}
	for _, dir := range []string{"/Applications", filepath.Join(a.home, "Applications")} {
		// Setup links the app's agent by its resolved path (appbundle.go). A
		// folder that no longer resolves — a link to a disk that is gone —
		// is read as the link it is.
		places := []string{dir}
		if resolved, err := env.resolve(dir); err == nil {
			places = append(places, resolved)
		} else if to, err := os.Readlink(dir); err == nil {
			if !filepath.IsAbs(to) {
				to = filepath.Join(filepath.Dir(dir), to)
			}
			places = append(places, filepath.Clean(to))
		}
		// Looked at where it leads, as setup looked at it.
		where := places[len(places)-1]
		for _, d := range places {
			if target == filepath.Join(d, appBundleName, "Contents", "MacOS", "vaultsync") {
				return appBundleIsOursOrGone(env, filepath.Join(where, appBundleName))
			}
		}
	}
	return false
}

// appBundleIsOursOrGone: the bundle at app is VaultSync for Mac, or nothing
// is there any more (the app in the Trash) — not an app of someone else's
// that happens to have the name.
func appBundleIsOursOrGone(env appEnv, app string) bool {
	if _, err := env.lstat(app); errors.Is(err, fs.ErrNotExist) {
		return true
	}
	data, err := env.readFile(filepath.Join(app, "Contents", "Info.plist"))
	if err != nil {
		return false
	}
	id, ok := plistString(data, "CFBundleIdentifier")
	return ok && id == appBundleID
}

// replaceLink points link at target in one step: a new link beside it,
// renamed over it — never a moment without a link, never a half-made one.
func replaceLink(link, target string) error {
	tmp := filepath.Join(filepath.Dir(link), ".vaultsync-link-"+strconv.Itoa(os.Getpid()))
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// shellQuote quotes a path for copying into a shell when it needs it.
func shellQuote(p string) string {
	if strings.ContainsAny(p, " '\"$`\\") {
		return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
	}
	return p
}

// shellPath is p as one word to type into a shell, shortened to ~ under the
// home folder as printed paths are (redactPaths would shorten a full one):
// a ~ inside single quotes would not expand, so the rest is escaped
// character by character instead. Outside the home folder it is
// shellQuote's word.
func shellPath(home, p string) string {
	short := tildePath(home, p)
	if !strings.HasPrefix(short, "~/") {
		return shellQuote(p)
	}
	var b strings.Builder
	b.WriteString("~/")
	for _, r := range short[2:] {
		if strings.ContainsRune(" \t'\"$`\\!*?[]{}()&;|<>#~=", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ensureEngine installs the pinned Syncthing unless the installed one is
// exactly it (re-verified by its checksum).
func (a *app) ensureEngine(ctx context.Context, t *term) (agentState, bool, error) {
	st, err := loadState(a.lay.State)
	// Never an older engine over the state a newer one wrote (#264).
	if olderThanSetUp(st, err) {
		return st, false, refuse("%s", a.olderNote(st, err))
	}
	if err != nil {
		return st, false, err
	}
	// The engine's folder holds data without its engine (#276). With no
	// engine recorded, which engine wrote it cannot be told, and an older
	// one must never run on a newer one's data: only this build's own
	// engine is taken back — its program and its config both there, the
	// program the very one the pin installs, compared with the verified
	// download before anything is replaced. With the recorded engine's
	// program gone, it is what a removal that stopped midway left, which
	// no engine may start on. Everything else changes nothing.
	//
	// A record whose engine's program and data are both gone leaves
	// nothing of an engine to protect: setup sets VaultSync up anew there,
	// as on a fresh computer — 054's check above still applies — and the
	// computer pairs again.
	var replace func(binSum string) error
	switch state := engineDataStateOf(a.lay, st); state {
	case engineDataUnrecorded:
		_, binErr := os.Stat(a.lay.Syncthing)
		_, cfgErr := os.Stat(filepath.Join(a.lay.Home, "config.xml"))
		if binErr != nil || cfgErr != nil {
			return st, false, refuse("%s", a.unrecordedEngineNote(state))
		}
		replace = func(binSum string) error {
			if have, err := fileSHA256(a.lay.Syncthing); err != nil || have != binSum {
				return refuse("%s", a.unrecordedEngineNote(state))
			}
			return nil
		}
	case engineDataWithoutProgram:
		return st, false, refuse("%s", a.unrecordedEngineNote(state))
	}
	if st.Syncthing.Version == syncthingVersion && st.Syncthing.BinarySHA256 != "" {
		if sum, err := fileSHA256(a.lay.Syncthing); err == nil && sum == st.Syncthing.BinarySHA256 {
			t.say("✓ Sync engine ready (Syncthing %s)", strings.TrimPrefix(syncthingVersion, "v"))
			return st, false, nil
		}
	}
	in, err := newInstaller(a.lay.Bin, a.goos, runtime.GOARCH)
	if err != nil {
		return st, false, refuse("VaultSync has no sync engine for this computer (%s/%s) yet.", a.goos, runtime.GOARCH)
	}
	fmt.Fprintf(t.out, "  Downloading Syncthing %s from github.com… ", strings.TrimPrefix(syncthingVersion, "v"))
	got, err := in.install(ctx, replace)
	if err != nil {
		t.blank()
		if errors.As(err, new(*refusal)) {
			return st, false, err
		}
		if replace != nil && !errors.Is(err, ErrChecksumMismatch) {
			// Whether the engine there is this VaultSync's own is told only
			// against its download (#276).
			return st, false, refuse("VaultSync's folder holds a sync engine's data (%s), but agent.json records no engine; whether the engine there is this VaultSync's own is told by comparing it with the engine this VaultSync installs, and installing that failed (%v). Nothing was changed. Run %s again once that is fixed.", tildePath(a.home, a.lay.Home), err, a.setupAgain())
		}
		if errors.Is(err, ErrChecksumMismatch) {
			return st, false, refuse("The sync engine download failed its integrity check and was not installed. Run %s again. If it fails again, report it at https://github.com/psimaker/vaultsync/issues.", a.setupAgain())
		}
		return st, false, refuse("Could not download Syncthing %s (%v). No new sync engine was installed. Check your internet connection, then run %s again.", strings.TrimPrefix(syncthingVersion, "v"), err, a.setupAgain())
	}
	t.say("done")
	st.Syncthing.Version = syncthingVersion
	st.Syncthing.BinarySHA256 = got.SHA256
	if err := updateState(a.lay.State, func(s *agentState) { s.Syncthing = st.Syncthing; raiseStateFormat(s) }); err != nil {
		return st, false, err
	}
	t.say("✓ Sync engine installed (Syncthing %s, checksum verified)", strings.TrimPrefix(syncthingVersion, "v"))
	return st, true, nil
}

// installAgentCopy puts this executable where the service runs it from, so
// the service never depends on where the download landed.
func installAgentCopy(dst string) (bool, error) {
	self, err := os.Executable()
	if err != nil {
		return false, err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return false, err
	}
	if same, _ := filepath.EvalSymlinks(dst); same == self {
		return false, nil
	}
	want, err := fileSHA256(self)
	if err != nil {
		return false, err
	}
	if have, err := fileSHA256(dst); err == nil && have == want {
		return false, nil
	}
	data, err := os.ReadFile(self)
	if err != nil {
		return false, err
	}
	if err := writeFileAtomic(dst, data, 0o755); err != nil {
		return false, err
	}
	return true, nil
}

// startedChanged reads the SHA-256 of prog, the program setup is about to
// start a job with, and says whether it differs from the one the job was
// last started with (agent.json's Started) — also when it cannot be read: a
// restart too many costs a moment, one too few leaves an old build running.
func (a *app) startedChanged(prog, recorded string) (sum string, changed bool) {
	sum, err := fileSHA256(prog)
	if err != nil {
		return "", true
	}
	return sum, sum != recorded
}

// recordStarted notes in agent.json the program a job now runs — only while
// VaultSync is set up: an uninstall --remove-data that finished meanwhile
// stays finished. A record that cannot be written costs the next setup one
// restart more, nothing else.
func (a *app) recordStarted(fn func(*agentState)) {
	_ = updateStateIfSetUp(a.lay.State, fn)
}

// keepCopyAtAppBuild brings VaultSync's own copy — the way back without
// VaultSync for Mac — to the build of the installed app this program runs
// from: the app may have been updated without a setup (dragged over in the
// Finder), and the way back must not run an older build, which would put
// its older engine back (#264) and the service on the copy.
func (a *app) keepCopyAtAppBuild() error {
	if _, ok := findInstalledApp(a.appEnv); !ok {
		return nil
	}
	// Only where VaultSync is set up: the copy never makes its folder.
	if _, err := os.Stat(a.lay.State); err != nil {
		return nil
	}
	// An older build writes nothing (054): not its program over the newer
	// one's copy, not agent.json (decision 091). Its run parks before this,
	// and its uninstall refuses; this holds for every caller.
	if a.olderBuild() {
		return nil
	}
	if _, err := installAgentCopy(a.lay.Agent); err != nil {
		return err
	}
	// The copy is this build now: agent.json takes its format. One that
	// cannot be written (damaged, say) is not the copy's failure, which is
	// what the callers report.
	_ = updateStateIfSetUp(a.lay.State, raiseStateFormat)
	return nil
}

func (a *app) logHint() string {
	if a.goos == "darwin" {
		return tildePath(a.home, a.lay.LogFile())
	}
	return "journalctl --user -u vaultsync"
}

// --- pair -------------------------------------------------------------------

func (a *app) pair(ctx context.Context, args []string) error {
	var opts pairOptions
	if err := parseFlags(pairFlags("pair", &opts), args); err != nil {
		return err
	}
	// A build older than the one set up here pairs nothing: it would write
	// agent.json and the newer engine's configuration in its own shape,
	// behind its own older checks (decision 091). Before any lock, any look
	// at the engine and any question.
	if st, err := loadState(a.lay.State); olderThanSetUp(st, err) {
		return a.olderRefusal("pair", args, st, err)
	}
	st, err := loadState(a.lay.State)
	if err != nil {
		return err
	}
	eng := a.engine()
	if !eng.prepared() || st.GUIPort == 0 {
		return refuse("VaultSync is not set up on this computer yet. Run vaultsync setup.")
	}
	if _, err := eng.client(st); err != nil {
		return err
	}
	// The engine's port is asked only while a lock says an engine of
	// VaultSync's may run, and then the port agent.json records at that
	// moment. A run records a port it moved to as it takes the engine lock
	// (#413). pair waits for a lock as long as it waited for the engine
	// before. The wait for the engine's API shares that deadline, and gets
	// at least three seconds once a lock is seen.
	deadline := time.Now().Add(pairEngineWait)
	may, err := waitEngineMayRun(ctx, a.lay, deadline)
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case err != nil:
		return refuse("VaultSync could not tell whether its sync engine runs: %s. Once that is fixed, try again.", a.lockLookNote(err))
	case !may && a.backgroundEngine():
		return refuse("The background service runs, but its sync engine does not. vaultsync status says why.")
	case !may:
		return refuse("The sync engine is not running. Start it with vaultsync start (or vaultsync setup), then try again.")
	}
	if st, err = loadState(a.lay.State); err != nil {
		return err
	}
	client, err := eng.client(st)
	if err != nil {
		return err
	}
	if err := waitReady(ctx, client, max(time.Until(deadline), 3*time.Second)); err != nil {
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case nobodyListens(err):
			// A lock is held, and nothing listens yet: a run still starting.
			return refuse("VaultSync is still starting its sync engine. Try again in a moment; vaultsync status says more.")
		}
		return refuse("The sync engine does not answer (%s). vaultsync status says more.", redactPaths(err.Error()))
	}
	t, closeTerm := openTerm(a.out)
	defer closeTerm()
	_, err = a.pairWith(ctx, t, opts, client, pairOrigin{})
	if errors.Is(err, errCancelled) || errors.Is(err, errInterrupted) {
		t.blank()
		t.say("Nothing was paired.")
		if errors.Is(err, errInterrupted) {
			return err
		}
		return nil
	}
	return err
}

// lockLookNote says which of VaultSync's locks could not be looked at, and
// why, with no other path in its words.
func (a *app) lockLookNote(err error) string {
	var le *lockLookError
	if errors.As(err, &le) {
		return fmt.Sprintf("its lock file %s could not be looked at (%v)", tildePath(a.home, le.path), errnoOf(le.err))
	}
	return redactPaths(err.Error())
}

// pairEngineWait is how long vaultsync pair waits for an engine of
// VaultSync's and its answer: as long as it waited for the engine's API
// before (#413).
var pairEngineWait = 10 * time.Second

// errNotSetUpAnymore: no agent.json — never set up, or removed since.
var errNotSetUpAnymore = refuse("VaultSync is not set up on this computer (anymore). Run vaultsync setup.")

// rememberShellEnv records the shell's guard variables in agent.json when
// they differ from what is recorded, so a pairing run by the service looks
// where this terminal looks. Called under the pairing lock only (the
// pairing flow does it): uninstall --remove-data takes agent.json away
// after seeing that lock free, and a record written outside it could
// bring the file back after the removal was reported done.
func rememberShellEnv(statePath string, getenv func(string) string) error {
	// Only a state that is there and set up takes the record: loadState
	// reads a missing agent.json as a fresh one, and a record written then
	// would make the file — after an uninstall --remove-data that finished
	// between the pairing's first looks and its lock, that would bring
	// VaultSync's folder back from the dead.
	if _, err := os.Stat(statePath); errors.Is(err, fs.ErrNotExist) {
		return errNotSetUpAnymore
	} else if err != nil {
		return fmt.Errorf("VaultSync's settings could not be read (%w) — nothing was changed", err)
	}
	st, err := loadState(statePath)
	if err != nil {
		return fmt.Errorf("VaultSync's settings could not be read (%w) — nothing was changed", err)
	}
	if st.GUIPort == 0 {
		return refuse("VaultSync is not set up on this computer yet. Run vaultsync setup.")
	}
	recorded := recordGuardEnv(getenv)
	if st.Env != nil && maps.Equal(recorded, st.Env) {
		return nil
	}
	return updateState(statePath, func(s *agentState) { s.Env = recorded })
}

// pairOrigin says where a pairing runs. background: inside the background
// service (the control socket), whose permissions macOS grants to
// "vaultsync", not to a terminal app. service: a process whose own
// environment is not the shell's — the guards then look where the shell's
// record (loaded under the pairing lock) says, and where the process
// looks. A pairing from a terminal (neither) records the shell's
// variables itself, under the pairing lock.
type pairOrigin struct {
	background bool
	service    bool
}

// pairWith runs the pairing flow on t and returns the session for what it
// kept (the menu for a caller without a terminal).
func (a *app) pairWith(ctx context.Context, t *term, opts pairOptions, client *syncthing.Client, origin pairOrigin) (*pairSession, error) {
	unitPath, _ := a.svc.unitPath()
	// The environments the guards look in: the shell's record when this is
	// the service's pairing (loaded under the pairing lock into the slot,
	// so read at use), and the process's own.
	shell := new(map[string]string)
	envs := func() []func(string) string {
		e := []func(string) string{a.getenv}
		if *shell != nil {
			rec := *shell
			e = append([]func(string) string{func(k string) string { return rec[k] }}, e...)
		}
		return e
	}
	var shellRecord func() (map[string]string, error)
	if origin.service {
		shellRecord = func() (map[string]string, error) {
			st, err := loadState(a.lay.State)
			if err != nil {
				return nil, fmt.Errorf("VaultSync's settings could not be read (%w) — nothing was changed", err)
			}
			if st.Env == nil {
				// An agent.json from before the record: the guards would look
				// only where the service looks, which may not be where the
				// user's own Syncthing or cloud settings are — the terminal
				// records that once.
				return nil, refuse("VaultSync has not recorded where this computer keeps its settings yet. Run vaultsync pair in a terminal once, then try again here.")
			}
			return st.Env, nil
		}
	}
	// A terminal's pairing is the shell: it records where it looks, so
	// the service's pairings can look there too.
	rememberShell := !origin.background && !origin.service
	s := &pairSession{
		t:    t,
		opts: opts,
		env: pairEnv{
			goos: a.goos, home: a.home, getenv: a.getenv, lay: a.lay, engine: client,
			discover: func(ctx context.Context) ([]pairing.DiscoveredHub, error) {
				if a.discover != nil {
					return a.discover(ctx)
				}
				return pairing.DiscoverHubs(ctx, pairing.DefaultPort, hubDiscoveryWait)
			},
			dial: pairing.NewLocalClient,
			registries: func() []string {
				var out []string
				for _, env := range envs() {
					out = append(out, obsidianRegistries(a.goos, a.home, env)...)
				}
				return uniqStrings(out)
			},
			scanRoots: []string{a.home},
			cloud: func() []cloudRoot {
				var roots []cloudRoot
				for _, env := range envs() {
					roots = append(roots, cloudRoots(cloudEnvFor(a.goos, a.home, env))...)
				}
				return uniqRoots(roots)
			},
			userST:         func() (userSyncthing, bool) { return findUserSyncthingIn(a.goos, a.home, envs()...) },
			unitDir:        filepath.Dir(unitPath),
			pendingTimeout: join.DefaultPendingTimeout,
			deviceName:     computerName(a.goos),
			now:            time.Now,
			background:     origin.background,
			rememberShell:  rememberShell,
			shellRecord:    shellRecord,
			shellSlot:      shell,
		},
	}
	return s, s.run(ctx)
}

// uniqRoots keeps the first of each cloud root, by path.
func uniqRoots(in []cloudRoot) []cloudRoot {
	seen := map[string]bool{}
	var out []cloudRoot
	for _, r := range in {
		if !seen[r.Path] {
			seen[r.Path] = true
			out = append(out, r)
		}
	}
	return out
}

// --- run (the background service) -------------------------------------------

func (a *app) run(ctx context.Context, args []string) (retErr error) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateDir := fs.String("state-dir", "", "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *stateDir != "" {
		// The service names the folder setup used (layout.AtStateDir).
		lay, err := a.lay.AtStateDir(*stateDir)
		if err != nil {
			return err
		}
		a.lay, a.svc.lay = lay, lay
	}
	// Started from VaultSync for Mac, the service runs while the app is
	// there — watched from the first moment, by the path it was started
	// with, so an app moved or put in the Trash at any point ends it, not
	// the next login (decision 053).
	ctx, watchEnded, stopWatch := watchOwnApp(ctx, a.appEnv)
	defer stopWatch()
	defer func() {
		switch watchEnded() {
		case programGone:
			retErr = withAppGone(retErr)
		case programReplaced:
			// The service ends without an error, and launchd starts what
			// is there now; a run in a terminal says why it stopped.
			if !serviceMode && (retErr == nil || errors.Is(retErr, context.Canceled)) {
				retErr = errProgramReplaced
			}
		}
	}()
	stateDirShown = a.lay.Base
	if !stderrIsTerminal() {
		serviceMode = true
	}
	logger := log.New(os.Stderr, "", log.LstdFlags)
	logf := func(format string, args ...any) { logger.Printf(format, args...) }
	// On a Mac, while VaultSync for Mac is installed only its agent runs the
	// engine (decision 053 (8)): a run of any other program — VaultSync's own
	// copy, an older download — does not sync beside the app, whose trip to
	// the Trash would not stop it. The service parks — no engine, no lock,
	// no socket — until it is stopped or set up anew (launchd would only
	// start an exiting one again); a run in a terminal says why and ends.
	if _, own := ownAppAgent(a.appEnv); !own && a.appEnv.goos == "darwin" {
		if why, app := a.parkReason(); why != "" {
			if !serviceMode {
				return refuse("%s", why)
			}
			// Only the service that parks notes it — so it stays parked
			// also once the app is gone again — never a run that was only
			// refused.
			// An older build writes nothing: it would drop what a newer
			// one keeps in agent.json (decision 054).
			if app != "" && !a.olderBuild() {
				_ = updateStateIfSetUp(a.lay.State, func(s *agentState) { s.ParkedBesideApp = app })
			}
			logf("%s", errParked)
			<-ctx.Done()
			return nil
		}
		var came func() (macApp, bool)
		var stopLook func()
		ctx, came, stopLook = whileNoAppCounts(ctx, a.appEnv, appCheckEvery)
		defer stopLook()
		defer func() {
			// An app that came while the engine ran: the engine stopped;
			// noted, so the run launchd starts next parks (and says so).
			if app, ok := came(); ok {
				if !a.olderBuild() {
					_ = updateStateIfSetUp(a.lay.State, func(s *agentState) { s.ParkedBesideApp = app.Path })
				}
				retErr = nil
			}
		}()
	}
	// Installing and preparing the engine happens under the same lock setup
	// takes: two preparations at once would generate identities over each
	// other.
	_, stErr := os.Stat(a.lay.State)
	hadState := stErr == nil
	setupLock := filepath.Join(a.lay.Base, "setup.lock")
	take := lockHandle
	if hadState {
		// Set up: the folder is there, and must not be made again should
		// an uninstall take it apart while this run waits for the lock.
		take = lockFileNoCreate
	}
	removedMeanwhile := refuse("VaultSync was removed from this computer while this run waited; run vaultsync setup to set it up again.")
	_, setupLockErr := os.Stat(setupLock)
	setupLockWasThere := setupLockErr == nil
	setupHandle, err := waitForLock(ctx, setupLock, 2*time.Minute, take)
	if err != nil {
		if hadState && errors.Is(err, os.ErrNotExist) {
			return removedMeanwhile
		}
		return err
	}
	unlockSetup := setupHandle.Release
	// A run that changes nothing takes back the lock file it made; one that
	// was there before — a stopped removal's — stays (#276).
	leaveSetupLock := func() {
		if setupLockWasThere {
			setupHandle.Release()
			return
		}
		_, _ = setupHandle.Discard()
	}
	if _, err := os.Stat(a.lay.State); hadState && errors.Is(err, os.ErrNotExist) {
		// Set up when this run began, gone while it waited for the lock:
		// an uninstall took VaultSync's folder apart (its lock held the
		// wait). A run does not set VaultSync up afresh behind it, and
		// the lock file its take may have made goes with it.
		_, _ = setupHandle.Discard()
		return removedMeanwhile
	}
	st, err := loadState(a.lay.State)
	// An older VaultSync — an old copy, an older VaultSync for Mac put in
	// Applications — never puts its older engine over the state the newer
	// one wrote, nor its copy over the newer one, nor its own agent.json
	// (#264). As the service it parks — launchd would only start an exiting
	// one again every 10 s — with one line in its log, until it is stopped
	// or its program is replaced (the setup link putting the current app in
	// place; watchOwnApp); in a terminal it says so and ends.
	if olderThanSetUp(st, err) {
		unlockSetup()
		if !serviceMode {
			return errOlderAgent
		}
		logf("%s", errOlderAgent)
		<-ctx.Done()
		return nil
	}
	if err != nil {
		unlockSetup()
		return err
	}
	// The engine's folder holds data without its engine, or the record
	// names an engine whose program and data are both gone (#276): nothing
	// is installed or started — before the engine lock, so a run refused
	// here owns nothing. setup takes back this build's own engine where
	// none is recorded, and sets VaultSync up anew over a record alone; a
	// run, unattended, does neither — not behind a removal that stopped.
	// The state is read after the wait, so a run that waited through such
	// a removal stops too.
	if state := engineDataStateOf(a.lay, st); state != engineDataOK {
		leaveSetupLock()
		return parkOrRefuse(ctx, logf, a.unrecordedEngineNote(state), engineDataLogLine(state))
	}
	eng := a.engine()
	// The engine lock makes this process the engine's only owner — and the
	// control socket's: a socket file found now belongs to an agent that is
	// gone. The socket is a convenience for status and the menu-bar app;
	// an engine that cannot have one still runs. It comes before anything
	// is touched: a run refused here owns nothing, and leaves the engine
	// and agent.json to the VaultSync that holds it, a desktop-v0.1.0 too.
	unlock, err := takeEngineLock(eng.lay.Lock)
	if errors.Is(err, ErrEngineRunning) {
		unlockSetup()
		return refuse("VaultSync is already running on this computer (the background service). See vaultsync status.")
	}
	if err != nil {
		unlockSetup()
		return err
	}
	defer unlock()
	// A port another program took while the engine was down is left now,
	// before anything slow such as a pin's download. From the engine lock
	// on, agent.json names the port this run's engine answers on, and a
	// command that sees the lock asks that one (#413).
	if err := eng.leaveATakenPort(&st, logf); err != nil {
		unlockSetup()
		return err
	}
	kept := false
	sum, sumErr := fileSHA256(a.lay.Syncthing)
	if st.Syncthing.Version != syncthingVersion || sumErr != nil || sum != st.Syncthing.BinarySHA256 {
		// Missing, another version, or changed since it was installed:
		// install the pinned one again (verified against the checksum).
		logf("vaultsync %s: installing Syncthing %s", version, syncthingVersion)
		// The service runs on with an older engine its record names,
		// unchanged and with its config, should the pin fail to install
		// (#267, decision 078); a run in a terminal ends and says why, as
		// setup does, to the person who started it. Nothing syncs while the
		// attempt runs, so a download that makes no progress ends then.
		keep := serviceMode && a.engineToKeep(st)
		stall := time.Duration(0)
		if keep {
			stall = keepEngineStallWait
		}
		got, err := a.installPin(ctx, stall)
		switch {
		case err == nil:
			st.Syncthing.Version, st.Syncthing.BinarySHA256 = syncthingVersion, got.SHA256
			if err := updateState(a.lay.State, func(s *agentState) { s.Syncthing = st.Syncthing; raiseStateFormat(s) }); err != nil {
				unlockSetup()
				return err
			}
		case keep && ctx.Err() == nil && !errors.Is(err, ErrNoPin) && a.engineToKeep(st):
			// Not stopped, no build missing for this computer, and the
			// engine hashed again — a failed install leaves it as it was,
			// and only that very engine runs on. The record stays.
			kept = true
			logf("vaultsync %s: Syncthing %s could not be installed — %s; the installed Syncthing %s runs on, and the next start tries again (vaultsync stop, then vaultsync start)", version, strings.TrimPrefix(syncthingVersion, "v"), installFailureSummary(err), strings.TrimPrefix(st.Syncthing.Version, "v"))
		default:
			unlockSetup()
			return err
		}
	}
	if err := eng.prepare(ctx, &st); err != nil {
		unlockSetup()
		return err
	}
	if err := updateState(a.lay.State, func(s *agentState) { s.GUIPort = st.GUIPort }); err != nil {
		unlockSetup()
		return err
	}
	ctl := &controlServer{a: a, eng: eng, logf: logf, background: a.backgroundEngine(), engineVersion: st.Syncthing.Version}
	// The record of a socket that could not be made or kept at an earlier
	// start no longer holds — and it goes before this start's own attempt,
	// so a fresh record that attempt may write (a socket that stops
	// serving right away) is never the one removed. On the way out it
	// goes too: after a clean stop, nobody answering means not running,
	// which is what the menu-bar app reads without the record.
	removeSocketRecord := func(when string) {
		if err := os.Remove(a.lay.SocketErrorFile()); err != nil && !errors.Is(err, os.ErrNotExist) {
			logf("control socket: control-socket-error.txt could not be removed %s — %s", when, serviceSummary(err))
		}
	}
	removeSocketRecord("before the socket is made")
	defer removeSocketRecord("on the way out")
	if socket, stop, err := ctl.serve(ctx); err != nil {
		logSocketError(logf, a.lay, err)
	} else {
		// On the way out the place is taken out of agent.json again: a
		// recorded place nobody serves is one another account could take.
		// A write that fails is reported with whatever ended the run.
		defer func() {
			if err := stop(); err != nil {
				retErr = errors.Join(retErr, err)
			}
			if err := updateState(a.lay.State, func(s *agentState) { s.ControlSocket = "" }); err != nil {
				// Said here in its own words (the kind of the cause, no path):
				// the error returned is summarized at the service boundary,
				// and the file that keeps full details may be unwritable too.
				logf("control socket: its place could not be taken out of agent.json — %s", serviceSummary(err))
				retErr = errors.Join(retErr, fmt.Errorf("the control socket's place could not be taken out of agent.json: %w", err))
			}
		}()
		if socket == a.lay.Socket {
			socket = ""
		}
		// Published while the setup lock is still held: a second run that
		// waits on it loads agent.json only afterwards and saves the place
		// back as it found it, instead of writing an older copy over it.
		if beforeSocketPublish != nil {
			beforeSocketPublish()
		}
		st.ControlSocket = socket
		if err := updateState(a.lay.State, func(s *agentState) { s.ControlSocket = socket }); err != nil {
			unlockSetup()
			return err
		}
	}
	// The copy follows the app's build where this build has taken over:
	// with its engine kept, it has not — the copy and agent.json's format
	// stay as they were until the pin is installed (#264, #267).
	if !kept {
		if err := a.keepCopyAtAppBuild(); err != nil {
			logf("VaultSync's own copy could not be brought to the app's build — %s", serviceSummary(err))
		}
	}
	unlockSetup()
	logf("vaultsync %s: running the sync engine", version)
	return eng.superviseLocked(ctx, st, logf)
}

// errParked is what a service that does not sync beside VaultSync for Mac
// says in its log, once, with no path (decision 053 (8)).
var errParked = errors.New("VaultSync for Mac is installed (or was, when this service last looked): this service runs VaultSync's own copy and does not sync beside it — run the setup link, or the app's own setup; vaultsync status says more")

// parkReason says why a run of a program other than VaultSync for Mac's
// agent does not run the engine — the app is installed (app: its path), or
// a service found it so and setup has not set the service up anew since
// (agent.json's ParkedBesideApp) — or "" when it may run. It changes
// nothing: only the service that parks notes it (run).
func (a *app) parkReason() (why, app string) {
	st, err := loadState(a.lay.State)
	if err != nil && !errors.Is(err, errStateNewer) {
		return "", "" // the run's own look at agent.json says what is wrong
	}
	// An agent.json newer than this build reads does not hide the app: an
	// older copy beside it parks here, not after a restart every 10 s.
	if apps := installedApps(a.appEnv); len(apps) > 0 {
		return fmt.Sprintf("VaultSync for Mac is installed in %s, and this is VaultSync's own copy, which does not sync beside it. Run the setup link, or %s setup, to sync from the app.", tildePath(a.home, filepath.Dir(apps[0].Path)), shellPath(a.home, apps[0].Agent)), apps[0].Path
	}
	if st.ParkedBesideApp != "" {
		return fmt.Sprintf("This is VaultSync's own copy, which stopped syncing when it found VaultSync for Mac installed. Run %s setup to go on without the app, or the setup link to use the app again.", shellPath(a.home, a.lay.Agent)), ""
	}
	return "", ""
}

// olderThanSetUp: this build is older than the VaultSync set up here —
// agent.json is in a newer format, or records a newer (or unknown) engine
// version (decision 054).
func olderThanSetUp(st agentState, loadErr error) bool {
	if errors.Is(loadErr, errStateNewer) {
		return true
	}
	return loadErr == nil && recordedEngineNewer(st.Syncthing.Version)
}

// engineDataState is what VaultSync's folder holds of its sync engine where
// that is not a recorded engine with its program in place (#276).
type engineDataState int

const (
	// engineDataOK: an engine recorded with its program there (whatever its
	// state — a changed program is reinstalled), or nothing of an engine.
	engineDataOK engineDataState = iota
	// engineDataUnrecorded: no engine recorded, and the engine's folder
	// holds data or cannot be read — which engine wrote it cannot be told
	// without the record, and decision 054 says not from content either.
	engineDataUnrecorded
	// engineDataWithoutProgram: an engine recorded, its program not there,
	// its data there — what a removal that stopped midway leaves (the
	// program goes first); an engine started on it could take a new
	// identity or bring back notes the other devices deleted.
	engineDataWithoutProgram
	// engineRecordOnly: an engine recorded, neither its program nor its
	// data there — a removal that stopped after both went.
	engineRecordOnly
)

// engineDataStateOf reads that state. Keyed on the recorded engine, not on
// agent.json itself, which a record without an engine may stand for. The
// program is "there" by Lstat: a removal takes it away, a link at its
// place as a link, so only no entry at all is a removal's trace — a link
// whose target cannot be reached (a disk not mounted) is the person's own
// layout, which 046 (1) heals by installing the pin over it, as before.
// So is a bin that is a link: a removal takes it away whole, so a program
// missing behind it is no trace of one.
func engineDataStateOf(lay layout, st agentState) engineDataState {
	entries, err := os.ReadDir(lay.Home)
	hasData := err == nil && len(entries) > 0 || err != nil && !errors.Is(err, fs.ErrNotExist)
	if st.Syncthing.Version == "" {
		if hasData {
			return engineDataUnrecorded
		}
		return engineDataOK
	}
	if _, err := os.Lstat(lay.Syncthing); !errors.Is(err, fs.ErrNotExist) {
		return engineDataOK
	}
	if info, err := os.Lstat(lay.Bin); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return engineDataOK
	}
	if hasData {
		return engineDataWithoutProgram
	}
	return engineRecordOnly
}

// engineDataWhat says what is there, for the notes below (#276).
func (a *app) engineDataWhat(state engineDataState) string {
	home := tildePath(a.home, a.lay.Home)
	switch state {
	case engineDataUnrecorded:
		return fmt.Sprintf("VaultSync's folder holds a sync engine's data (%s), but agent.json records no engine, so which engine wrote it cannot be told — and an older engine must never run on a newer one's data.", home)
	case engineDataWithoutProgram:
		return fmt.Sprintf("VaultSync's folder holds its sync engine's data (%s), but not the engine's program: an uninstall --remove-data most likely stopped midway, and an engine started on what it left could bring back notes your other devices deleted.", home)
	default:
		return "VaultSync's record names its sync engine, but neither the engine's program nor its data is there: an uninstall --remove-data most likely stopped midway."
	}
}

// engineDataWayOut is the way out of each state, and what it costs: each
// way leads to a state setup goes ahead in (#276).
func (a *app) engineDataWayOut(state engineDataState) string {
	home := tildePath(a.home, a.lay.Home)
	switch state {
	case engineDataWithoutProgram:
		return fmt.Sprintf("Finish the removal with vaultsync uninstall --remove-data, or move %s aside and run vaultsync setup again: this computer then pairs with your Hub again, and your vault files stay where they are.", home)
	case engineRecordOnly:
		return "Finish the removal with vaultsync uninstall --remove-data, or set VaultSync up anew with vaultsync setup: this computer then pairs with your Hub again."
	}
	way := fmt.Sprintf("Move %s aside, then run vaultsync setup again: this computer then pairs with your Hub again, and your vault files stay where they are.", home)
	if _, err := os.Stat(a.lay.State); err == nil {
		// The removal refuses a folder without agent.json as not
		// VaultSync's own: offered only where that is there.
		way += " To remove VaultSync instead, run vaultsync uninstall --remove-data."
	}
	return way
}

// engineDataLogLine is the one line the service logs when it parks in
// such a state: fixed, with no path or name, as every line of the service
// log (main); vaultsync status gives the whole note (#276).
func engineDataLogLine(state engineDataState) string {
	switch state {
	case engineDataUnrecorded:
		return "VaultSync's folder holds a sync engine's data, but agent.json records no engine — nothing was started; vaultsync status says how to go on"
	case engineDataWithoutProgram:
		return "VaultSync's folder holds its sync engine's data, but not the engine's program — nothing was started; vaultsync status says how to go on"
	}
	return "VaultSync's record names its sync engine, but neither the engine's program nor its data is there — nothing was started; vaultsync status says how to go on"
}

// unrecordedEngineNote says why setup or run changed nothing (#276).
func (a *app) unrecordedEngineNote(state engineDataState) string {
	return a.engineDataWhat(state) + " Nothing was changed. " + a.engineDataWayOut(state)
}

// unrecordedEngineStatus is what status says of it: what setup and the
// background service will do.
func (a *app) unrecordedEngineStatus(state engineDataState) string {
	switch state {
	case engineDataUnrecorded:
		return fmt.Sprintf("%s vaultsync setup takes it back only over this VaultSync's own engine (Syncthing %s) and otherwise changes nothing, as the background service does. %s", a.engineDataWhat(state), strings.TrimPrefix(syncthingVersion, "v"), a.engineDataWayOut(state))
	case engineRecordOnly:
		return a.engineDataWhat(state) + " The background service changes nothing here. " + a.engineDataWayOut(state)
	}
	return a.engineDataWhat(state) + " vaultsync setup and the background service change nothing there. " + a.engineDataWayOut(state)
}

// parkOrRefuse ends a run that must not go on: in a terminal it says why;
// as the service it logs logged once — a fixed line, as the service log
// carries no path or name — and parks: launchd would only start an exiting
// one again every 10 s, until it is stopped or its program is replaced
// (decision 054's pattern).
func parkOrRefuse(ctx context.Context, logf func(string, ...any), why, logged string) error {
	if !serviceMode {
		return refuse("%s", why)
	}
	logf("%s", logged)
	<-ctx.Done()
	return nil
}

// olderBuild: olderThanSetUp, by a look at agent.json now.
func (a *app) olderBuild() bool {
	st, err := loadState(a.lay.State)
	return olderThanSetUp(st, err)
}

// olderNote says so for a person, with the versions where they are known.
func (a *app) olderNote(st agentState, loadErr error) string {
	return a.olderFact(st, loadErr) + " Nothing was changed — run the setup link to get the current VaultSync."
}

// olderRefusal refuses a command of a build older than the one set up here
// (decision 091): pair would pair through the newer engine in this build's
// shape, behind its own older checks, and write agent.json in it; uninstall
// would remove what the newer build set up with this build's knowledge of
// it, and put this build's program over VaultSync's copy; pause and resume
// come here only where no agent answers, or the place of its socket does not
// read in this build's shape, and would read agent.json and ask the engine
// themselves (they never write it). Nothing is changed, locked or asked. The way on names
// the program that holds the VaultSync set up here, with the command and
// the arguments this run was given; only where none is found, the setup
// link. Where the background service runs this very build, it parks (054):
// nothing syncs, and only the setup link brings a current build to the
// service — uninstall can still go through another program. uninstall and
// pause say how syncing stops now, where that is known.
func (a *app) olderRefusal(command string, args []string, st agentState, loadErr error) error {
	self, selfErr := ownProgram(a.appEnv)
	installed := a.svc.installed()
	serviceIsThis := selfErr == nil && installed && a.serviceRunsThisBuild(self)
	prog, found := "", false
	if selfErr == nil && (command == "uninstall" || !serviceIsThis) {
		prog, found = a.setUpProgram(self, st, loadErr)
	}
	line := command + shellArgs(args)
	var way string
	switch {
	case found && command == "uninstall":
		way = fmt.Sprintf("to remove VaultSync, run %s %s, the VaultSync set up here.", a.commandFor(prog), line)
	case found:
		way = fmt.Sprintf("run %s %s, the VaultSync set up here.", a.commandFor(prog), line)
	default:
		way = fmt.Sprintf("run the setup link to get the current VaultSync, then vaultsync %s.", line)
	}
	msg := a.olderFact(st, loadErr) + " Nothing was changed — " + way
	switch {
	case serviceIsThis:
		msg += " Nothing syncs here meanwhile: the background service runs this VaultSync too."
	case command != "uninstall" && command != "pause":
	case installed:
		// stop is no command an older build refuses: this very program, by
		// the path it runs from, where no other was found.
		stopper := a.ownCommand()
		if found {
			stopper = a.commandFor(prog)
		}
		msg += fmt.Sprintf(" %s stop stops syncing now.", stopper)
	default:
		// stop stops the background service only; a run started by hand
		// is stopped where it runs.
		msg += " No background service is installed here, so stop stops nothing: a vaultsync run started by hand, in a terminal or at login, syncs until it is stopped there."
	}
	return refuse("%s", msg)
}

// ownCommand is how a shell runs this very program (commandFor), by the
// path it runs from; "vaultsync" where that cannot be told.
func (a *app) ownCommand() string {
	if a.appEnv.executable == nil {
		return "vaultsync"
	}
	self, err := a.appEnv.executable()
	if err != nil {
		return "vaultsync"
	}
	if a.appEnv.resolve != nil {
		if resolved, err := a.appEnv.resolve(self); err == nil {
			self = resolved
		}
	}
	return a.commandFor(self)
}

// shellArgs is args as a shell reads them back, each one word after a
// space: what this run was given, carried into the command a refusal
// names, so the command pasted does what was asked for (decision 091).
func shellArgs(args []string) string {
	var b strings.Builder
	for _, arg := range args {
		b.WriteString(" ")
		b.WriteString(shellWord(arg))
	}
	return b.String()
}

// shellWord is s as one shell word: as it is where it holds only
// characters no shell reads specially, else in single quotes.
func shellWord(s string) string {
	plain := s != ""
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:,@%+", r)) {
			plain = false
			break
		}
	}
	if plain {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// setUpProgram is the program that holds the VaultSync set up here, for an
// older build's way on (decision 091): the program the background service
// runs (on a Mac, the one its LaunchAgent names) or VaultSync's own copy —
// one that is not this build, and is the very program agent.json records
// the last setup started the service with (Started.Service); failing that,
// VaultSync's own copy where it is not this build, since every build that
// takes over keeps the copy at its own (setup, and VaultSync for Mac's run
// and uninstall). agent.json records no place, and a newer format is not
// read for one. Each program is looked at and read through appEnv, as this
// program is. None found: "", false.
func (a *app) setUpProgram(self []byte, st agentState, loadErr error) (string, bool) {
	env := a.appEnv
	if env.resolve == nil || env.readFile == nil {
		return "", false
	}
	lstat := env.lstat
	if lstat == nil {
		lstat = os.Lstat
	}
	// another reads the program at p — a regular file, also behind a link —
	// where it is not this build.
	another := func(p string) ([]byte, bool) {
		resolved, err := env.resolve(p)
		if err != nil {
			return nil, false
		}
		if info, err := lstat(resolved); err != nil || !info.Mode().IsRegular() {
			return nil, false
		}
		have, err := env.readFile(p)
		if err != nil || sameBuild(have, self) {
			return nil, false
		}
		return have, true
	}
	var candidates []string
	if a.goos == "darwin" {
		if prog := a.svc.program(); prog != "" {
			candidates = append(candidates, prog)
		}
	}
	candidates = append(candidates, a.lay.Agent)
	if loadErr == nil && st.Started.Service != "" {
		for _, p := range candidates {
			have, ok := another(p)
			if !ok {
				continue
			}
			if sum := sha256.Sum256(have); hex.EncodeToString(sum[:]) == st.Started.Service {
				return p, true
			}
		}
	}
	if _, ok := another(a.lay.Agent); ok {
		return a.lay.Agent, true
	}
	return "", false
}

// keepEngineStallWait ends a run's attempt to install its pin where an
// engine is there to keep, once the download makes no progress that long
// (#267, decision 078): the download client alone waits up to ten minutes on
// a connection that never answers, and no engine runs meanwhile. A slow
// download that goes on is not cut. A test shortens it.
var keepEngineStallWait = 30 * time.Second

// installPin installs this build's pinned Syncthing into bin; stallAfter > 0
// ends a download that makes no progress that long.
func (a *app) installPin(ctx context.Context, stallAfter time.Duration) (installedEngine, error) {
	in, err := newInstaller(a.lay.Bin, a.goos, runtime.GOARCH)
	if err != nil {
		return installedEngine{}, err
	}
	in.stallAfter = stallAfter
	if stallAfter > 0 {
		// The watchdog ends a download that makes no progress; the client's
		// total timeout would also cut a slow one that goes on, at every
		// start (#267). Its redirect checks stay.
		c := *in.client
		c.Timeout = 0
		in.client = &c
	}
	return in.install(ctx, nil)
}

// engineToKeep: the engine agent.json records may run on should this build's
// pin fail to install (#267, decision 078) — older than the pin, at or above
// keepEngineFrom, its program the very one recorded (hashed now; a record
// with no checksum keeps nothing), and its config there: without one,
// prepare would make it with the older engine and edit it in the pin's
// shape (054).
func (a *app) engineToKeep(st agentState) bool {
	rec := st.Syncthing.Version
	if st.Syncthing.BinarySHA256 == "" || !engineNewer(syncthingVersion, rec) || engineNewer(keepEngineFrom, rec) {
		return false
	}
	if sum, err := fileSHA256(a.lay.Syncthing); err != nil || sum != st.Syncthing.BinarySHA256 {
		return false
	}
	_, err := os.Stat(filepath.Join(a.lay.Home, "config.xml"))
	return err == nil
}

// installFailureSummary is why a pin could not be installed, in fixed words
// with no path or address, for the line a run logs when it keeps its engine
// (#267).
func installFailureSummary(err error) string {
	var ue *url.Error
	var pe *fs.PathError
	var le *os.LinkError
	var se *os.SyscallError
	switch {
	case errors.Is(err, errDownloadStalled):
		return "the download made no progress for " + keepEngineStallWait.String()
	case errors.Is(err, ErrChecksumMismatch):
		return serviceSummary(err)
	case errors.As(err, &ue):
		// Before the file errors: a refused or unreachable address wraps a
		// system call's error, and is the download's trouble all the same.
		return "the download did not get through"
	case errors.As(err, &pe), errors.As(err, &le), errors.As(err, &se):
		return serviceSummary(err)
	}
	return "the download did not get through"
}

// setupAgain is how a refused setup is run again (#267): by VaultSync for
// Mac's agent where setup runs as that agent — the vaultsync command may
// still lead to an older copy — and as vaultsync setup otherwise; the setup
// link runs a vaultsync from a folder it removes, so naming that path would
// lead nowhere.
func (a *app) setupAgain() string {
	if agent, ok := ownAppAgent(a.appEnv); ok {
		return a.commandFor(agent) + " setup"
	}
	return "vaultsync setup"
}

// olderFact is what olderNote says of the two builds, without what it
// tells to do: after a start that brought up another build, something did
// change, and the program at this path may be the current one by now
// (#290).
func (a *app) olderFact(st agentState, loadErr error) string {
	if loadErr == nil && st.Syncthing.Version != "" {
		return fmt.Sprintf("this VaultSync is older than the one set up on this computer: its sync engine is Syncthing %s, this vaultsync brings %s.", strings.TrimPrefix(st.Syncthing.Version, "v"), strings.TrimPrefix(syncthingVersion, "v"))
	}
	return "this VaultSync is older than the one set up on this computer."
}

// stderrIsTerminal: this run writes to a terminal, not a log — a person
// runs it; a test says so for the run it starts.
var stderrIsTerminal = func() bool {
	st, err := os.Stderr.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// errOlderAgent is what a run of a VaultSync older than the one set up on
// this computer says — in a terminal as its error, as the service once in
// its log before it parks: it pins an older Syncthing than agent.json
// records (or agent.json is in a newer format), and putting its engine over
// the state the newer one wrote is what it must never do (#264). It names
// no path.
var errOlderAgent = errors.New("this VaultSync is older than the one set up on this computer, whose sync engine is newer — nothing was changed; run the setup link to get the current VaultSync")

// errProgramReplaced ends a run in a terminal whose VaultSync for Mac was
// replaced by another build while it ran — the setup link's update, or one
// dragged over it. It names no path.
var errProgramReplaced = errors.New("VaultSync for Mac was replaced by another build while this run synced, so this run stopped — run it again to sync with the new one")

// errAppGone ends a service whose VaultSync for Mac went away; it names no
// path, so the service log carries it as it is.
var errAppGone = errors.New("VaultSync for Mac is no longer where the background service runs it from — moved, or put in the Trash — so the service stopped syncing; put it back where it was, or run the setup link again; to remove VaultSync, run bin/vaultsync uninstall in VaultSync's folder")

// withAppGone is what a run the app's departure ended returns: errAppGone,
// beside any other failure on the way out — the cancellation it caused is
// none.
func withAppGone(err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return errAppGone
	}
	return errors.Join(errAppGone, err)
}

// beforeSocketPublish runs right before run records the control socket's
// place in agent.json; a test uses it to look at the locks held then.
var beforeSocketPublish func()

// --- stop / start -----------------------------------------------------------

// startCommand is `vaultsync start`, with --state-dir naming VaultSync's
// folder as the menu-bar app knows it (tray.StartArgs). The program the
// menu runs inherits the menu's environment and took its folder from it: on
// Linux, a menu started with --state-dir in a session whose XDG_STATE_HOME
// names another place made start read another agent.json than the menu and
// the service, and wait its whole answer wait on a socket nobody serves
// (#290). Any other argument is refused, as by every other command; until
// then start ignored all of them, and released builds still do.
func (a *app) startCommand(args []string) error {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateDir := fs.String("state-dir", "", "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	lay, err := a.lay.AtStateDir(*stateDir)
	if err != nil {
		return err
	}
	a.lay, a.svc.lay = lay, lay
	return a.startService()
}

func (a *app) stopService() error {
	if !a.svc.installed() {
		return refuse("The background service is not installed.")
	}
	if err := a.svc.stop(); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "✓ VaultSync is paused on this computer. Nothing syncs until you run vaultsync start.")
	return nil
}

func (a *app) startService() error {
	// A job whose program is gone cannot start: said, not loaded in vain.
	if prog, gone := a.svc.programGone(); gone {
		return refuse("The background service cannot start: %s.", a.programGoneNote(prog))
	}
	// Whether the service runs this very build is judged before it starts
	// and again after it, and taken only where both agree (#290). A run of
	// VaultSync for Mac puts its own build in the copy's place as it comes
	// up, so a copy started by its path, judged after the start alone,
	// compared the service with the build that had just replaced it. And
	// launchd runs whatever is at the program's path once it starts, so an
	// app replaced meanwhile makes the look before the start alone stale.
	// Either way a newer service that syncs was called one that does not;
	// where the two looks differ, start says only that another build runs.
	// Both compare the service with this build as it was when start began,
	// read once: by the look after the start, the file at this program's
	// path may be the newer build too.
	self, selfErr := ownProgram(a.appEnv)
	before := selfErr == nil && a.serviceRunsThisBuild(self)
	left, err := a.svc.start()
	if err != nil {
		return err
	}
	// Say "running" only for a service that runs.
	notRunning := func() error {
		return refuse("VaultSync asked the system to start its background service, but it is not running. See %s, or run vaultsync setup again.", a.logHint())
	}
	if !a.svc.waitRunning() {
		return notRunning()
	}
	// Nor "running again" for one that runs but does not sync.
	if note := a.parkedNote(); note != "" {
		return refuse("The background service runs, but it does not sync: %s", note)
	}
	st, err := loadState(a.lay.State)
	older := olderThanSetUp(st, err)
	thisBuild := before && a.serviceRunsThisBuild(self)
	if thisBuild && older {
		// What this program knows of itself holds for the service.
		return refuse("The background service runs, but it does not sync: %s", a.olderNote(st, err))
	}
	// The service manager counts a run as running from its first moment;
	// its agent answers only once it has come up, which a pin's download
	// can delay for minutes, and a run that ends before then never does.
	// So "running" is said only on an answer (#290, decision 085).
	answered := a.agentAnswers()
	if left && !answered {
		// A run left alone that gave no answer is looked at once more: one
		// that is gone or no longer running is loaded afresh and waited for
		// once more, so a job launchd was still removing is never left
		// down until the next login (decision 085's guard).
		again, err := a.svc.startAgainIfGone()
		if err != nil {
			return err
		}
		if again {
			left = false
			if !a.svc.waitRunning() {
				return notRunning()
			}
			answered = a.agentAnswers()
		}
	}
	if thisBuild {
		switch {
		case !answered:
			fmt.Fprintln(a.out, a.noAnswerYet())
		case left:
			fmt.Fprintln(a.out, "✓ VaultSync is already running.")
		default:
			fmt.Fprintln(a.out, "✓ VaultSync is running again.")
		}
		return nil
	}
	// The service runs another build, of which this program's pin says
	// nothing — a newer one syncs, an older one parks (#264). start says
	// only what it knows, and leaves whether it syncs to status, which
	// asks the running agent.
	if answered {
		fmt.Fprintln(a.out, "✓ The background service is running. It runs another VaultSync build than this vaultsync — vaultsync status says whether it syncs.")
	} else {
		fmt.Fprintln(a.out, "The background service runs, but it does not answer yet. It runs another VaultSync build than this vaultsync — vaultsync status says whether it syncs.")
	}
	if older {
		fmt.Fprintln(a.out, "Note: "+a.olderFact(st, err))
	}
	return nil
}

// noAnswerYet is start's word for a service that runs and whose agent has
// not answered: no refusal — it may be on its way up — and the way out for
// one that stays so, which vaultsync stop ends however it hangs. A run that
// recorded that it could not open its control socket syncs on without one
// and never answers there; a stop and start would only make the same
// socket fail again, so that is said instead (control-socket-error.txt goes
// once a run serves its socket, and with a clean stop).
func (a *app) noAnswerYet() string {
	if _, err := os.Stat(a.lay.SocketErrorFile()); err == nil {
		return fmt.Sprintf("VaultSync's background service runs, but it does not answer on its control socket: it recorded that it could not open it (%s says why). vaultsync status asks the sync engine directly and says whether it syncs.", tildePath(a.home, a.lay.SocketErrorFile()))
	}
	return fmt.Sprintf("VaultSync's background service runs, but it does not answer yet — it may still be installing its sync engine. vaultsync status says where it stands; if it stays so, run vaultsync stop, then vaultsync start (its log: %s).", a.logHint())
}

// serviceRunsThisBuild: the background service runs this very build — the
// program its LaunchAgent names on a Mac, VaultSync's own copy on Linux
// (the only one setup gives the unit), byte for byte or by its code
// directory. Either one not to be read: not known to be this build.
func (a *app) serviceRunsThisBuild(self []byte) bool {
	prog := a.lay.Agent
	if a.goos == "darwin" {
		prog = a.svc.program()
	}
	if prog == "" {
		return false
	}
	have, err := a.appEnv.readFile(prog)
	return err == nil && sameBuild(have, self)
}
