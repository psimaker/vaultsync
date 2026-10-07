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
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

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
		case serviceMode:
			// A log or the journal: a fixed summary that carries no path or
			// name; the whole message stays in VaultSync's own folder.
			fmt.Fprintln(os.Stderr, "error:", serviceSummary(err))
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

// serviceSummary describes a failure of the background service from the
// error's kind alone — never from text that could hold a path or a name.
func serviceSummary(err error) string {
	var pe *fs.PathError
	var le *os.LinkError
	var se *os.SyscallError
	var ee *exec.ExitError
	switch {
	case errors.Is(err, ErrChecksumMismatch):
		return ErrChecksumMismatch.Error() + " — nothing was installed"
	case errors.Is(err, ErrEngineRunning):
		return ErrEngineRunning.Error()
	case errors.Is(err, ErrNoPin):
		return ErrNoPin.Error()
	case errors.Is(err, errEngineStoppedOnItsOwn):
		return errEngineStoppedOnItsOwn.Error()
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

// redactPaths shortens the paths in what is printed: under the background
// service that output lands in a log or the journal, which should carry
// neither the account's home folder nor where VaultSync keeps its files.
// VaultSync's folder becomes <VaultSync folder>, the home folder ~.
func redactPaths(msg string) string {
	replace := func(path, with string) {
		if path == "" || path == "/" {
			return
		}
		if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved != path {
			msg = strings.ReplaceAll(msg, resolved, with)
		}
		msg = strings.ReplaceAll(msg, path, with)
	}
	replace(stateDirShown, "<VaultSync folder>")
	if home, err := os.UserHomeDir(); err == nil {
		replace(home, "~")
	}
	return msg
}

func usage(w io.Writer) {
	fmt.Fprint(w, `vaultsync — keeps your Obsidian vaults in sync with your VaultSync Hub

  vaultsync setup        install the sync engine and the background service,
                         pair with your Hub and choose a vault
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
  --create              start --vault on your Hub when it does not exist yet
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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
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
		return app.startService()
	case "uninstall":
		return app.uninstall(ctx, args)
	case "run":
		return app.run(ctx, args)
	}
	usage(os.Stderr)
	return fmt.Errorf("unknown command %q", cmd)
}

// app is the live environment: this computer, this user.
type app struct {
	goos, home string
	getenv     func(string) string
	lay        layout
	svc        service
	out        io.Writer
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
	a := &app{goos: runtime.GOOS, home: home, getenv: os.Getenv, lay: lay, out: os.Stdout}
	a.svc = service{goos: a.goos, home: home, uid: os.Getuid(), getenv: os.Getenv, lay: lay, run: execRunner{}}
	return a, nil
}

func (a *app) userSyncthing() (userSyncthing, bool) {
	return findUserSyncthing(a.goos, a.home, a.getenv)
}

func (a *app) engine() engine {
	_, hasUser := a.userSyncthing()
	return engine{lay: a.lay, opts: engineOptions{avoidDefaultPorts: hasUser}}
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
	t, closeTerm := openTerm(a.out)
	defer closeTerm()
	t.blank()
	t.say("  VaultSync — keeps your Obsidian vaults in sync with your Hub.")
	t.say("  Your vaults stay where they are; VaultSync keeps its own files in %s.", tildePath(a.home, a.lay.Base))
	t.blank()

	// One setup at a time: two would install and generate the engine's
	// identity over each other.
	unlockSetup, err := lockFile(filepath.Join(a.lay.Base, "setup.lock"))
	if errors.Is(err, ErrEngineRunning) {
		return refuse("Another vaultsync setup is running on this computer — wait for it to finish.")
	}
	if err != nil {
		return err
	}
	st, engineChanged, err := a.ensureEngine(ctx, t)
	if err != nil {
		unlockSetup()
		return err
	}
	eng := a.engine()
	if err := eng.prepare(ctx, &st); err != nil {
		unlockSetup()
		return fmt.Errorf("could not set up the sync engine: %w", err)
	}
	if err := saveState(a.lay.State, st); err != nil {
		unlockSetup()
		return err
	}
	agentChanged, err := installAgentCopy(a.lay.Agent)
	unlockSetup()
	if err != nil {
		return fmt.Errorf("could not install VaultSync into %s: %w", tildePath(a.home, a.lay.Bin), err)
	}

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
		if _, err := a.svc.install(a.lay.Agent, agentChanged || engineChanged); err != nil {
			if a.goos == "darwin" {
				return refuse("Could not start the background service (%v). Run vaultsync setup in Terminal on this Mac while you are logged in — not over SSH — or use vaultsync setup --no-service and keep vaultsync run running yourself.", err)
			}
			return refuse("Could not start the background service (%v). Check `systemctl --user status vaultsync`, or use vaultsync setup --no-service and keep vaultsync run running yourself.", err)
		}
		t.say("✓ Background service running — it starts again when you log in")
	}
	client, err := eng.client(st)
	if err != nil {
		return err
	}
	if err := waitEngine(ctx, client, 60*time.Second, temporary); err != nil {
		return fmt.Errorf("the sync engine did not start: %w — see %s", err, a.logHint())
	}
	command := a.installCommand(t)
	_, err = a.pairWith(ctx, t, opts, client, false)
	if *noService {
		t.blank()
		t.say("VaultSync has no background service on this computer: run %s run to keep syncing.", command)
	}
	if errors.Is(err, errCancelled) {
		t.blank()
		if *noService {
			t.say("Nothing was paired. Run vaultsync pair any time.")
		} else {
			t.say("Nothing was paired. The sync engine and the background service stay installed: run vaultsync pair any time, or vaultsync uninstall to remove the background service.")
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
// (setup preparing the engine), waiting at most timeout.
func waitForLock(ctx context.Context, path string, timeout time.Duration) (func(), error) {
	deadline := time.Now().Add(timeout)
	for {
		unlock, err := lockFile(path)
		if !errors.Is(err, ErrEngineRunning) || time.Now().After(deadline) {
			return unlock, err
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
// (~/.local/bin) as a link to the copy the service runs — unless something
// else already has that name — and says how to call it.
func (a *app) installCommand(t *term) string {
	link := commandLink(a.home)
	if target, err := os.Readlink(link); err == nil && target == a.lay.Agent {
		// already ours
	} else if _, err := os.Lstat(link); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err == nil {
			_ = os.Symlink(a.lay.Agent, link)
		}
	}
	// "vaultsync" alone only when it reaches this very program.
	if found, err := exec.LookPath("vaultsync"); err == nil {
		agent, aerr := filepath.EvalSymlinks(a.lay.Agent)
		if r, err := filepath.EvalSymlinks(found); err == nil && aerr == nil && r == agent {
			return "vaultsync"
		}
	}
	if target, err := os.Readlink(link); err == nil && target == a.lay.Agent {
		return tildePath(a.home, link)
	}
	return shellQuote(a.lay.Agent)
}

// shellQuote quotes a path for copying into a shell when it needs it.
func shellQuote(p string) string {
	if strings.ContainsAny(p, " '\"$`\\") {
		return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
	}
	return p
}

// ensureEngine installs the pinned Syncthing unless the installed one is
// exactly it (re-verified by its checksum).
func (a *app) ensureEngine(ctx context.Context, t *term) (agentState, bool, error) {
	st, err := loadState(a.lay.State)
	if err != nil {
		return st, false, err
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
	got, err := in.install(ctx)
	if err != nil {
		t.blank()
		if errors.Is(err, ErrChecksumMismatch) {
			return st, false, refuse("The sync engine download failed its integrity check and was not installed. Run vaultsync setup again. If it fails again, report it at https://github.com/psimaker/vaultsync/issues.")
		}
		return st, false, refuse("Could not download Syncthing %s (%v). No new sync engine was installed. Check your internet connection, then run vaultsync setup again.", strings.TrimPrefix(syncthingVersion, "v"), err)
	}
	t.say("done")
	st.Syncthing.Version = syncthingVersion
	st.Syncthing.BinarySHA256 = got.SHA256
	if err := saveState(a.lay.State, st); err != nil {
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

func (a *app) logHint() string {
	if a.goos == "darwin" {
		return tildePath(a.home, filepath.Join(a.lay.Logs, "vaultsync.log"))
	}
	return "journalctl --user -u vaultsync"
}

// --- pair -------------------------------------------------------------------

func (a *app) pair(ctx context.Context, args []string) error {
	var opts pairOptions
	if err := parseFlags(pairFlags("pair", &opts), args); err != nil {
		return err
	}
	st, err := loadState(a.lay.State)
	if err != nil {
		return err
	}
	eng := a.engine()
	if !eng.prepared() || st.GUIPort == 0 {
		return refuse("VaultSync is not set up on this computer yet. Run vaultsync setup.")
	}
	client, err := eng.client(st)
	if err != nil {
		return err
	}
	if err := waitReady(ctx, client, 10*time.Second); err != nil {
		return refuse("The sync engine is not running. Start it with vaultsync start (or vaultsync setup), then try again.")
	}
	t, closeTerm := openTerm(a.out)
	defer closeTerm()
	_, err = a.pairWith(ctx, t, opts, client, false)
	if errors.Is(err, errCancelled) {
		t.blank()
		t.say("Nothing was paired.")
		return nil
	}
	return err
}

// pairWith runs the pairing flow on t and returns the session for what it
// kept (the menu for a caller without a terminal). background says the
// flow runs inside the background service (the control socket), whose
// permissions macOS grants to "vaultsync", not to a terminal app.
func (a *app) pairWith(ctx context.Context, t *term, opts pairOptions, client *syncthing.Client, background bool) (*pairSession, error) {
	unitPath, _ := a.svc.unitPath()
	s := &pairSession{
		t:    t,
		opts: opts,
		env: pairEnv{
			goos: a.goos, home: a.home, getenv: a.getenv, lay: a.lay, engine: client,
			discover: func(ctx context.Context) ([]pairing.DiscoveredHub, error) {
				return pairing.DiscoverHubs(ctx, pairing.DefaultPort, hubDiscoveryWait)
			},
			dial:           pairing.NewLocalClient,
			registries:     obsidianRegistries(a.goos, a.home, a.getenv),
			scanRoots:      []string{a.home},
			cloud:          func() []cloudRoot { return cloudRoots(liveCloudEnv(a.goos, a.home)) },
			userST:         a.userSyncthing,
			unitDir:        filepath.Dir(unitPath),
			pendingTimeout: join.DefaultPendingTimeout,
			deviceName:     computerName(a.goos),
			now:            time.Now,
			background:     background,
		},
	}
	return s, s.run(ctx)
}

// --- run (the background service) -------------------------------------------

func (a *app) run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	stateDir := fs.String("state-dir", "", "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *stateDir != "" {
		// The service names the folder setup used (see layout.at).
		if !filepath.IsAbs(*stateDir) {
			return errors.New("--state-dir must be an absolute path")
		}
		a.lay = a.lay.at(filepath.Clean(*stateDir))
		a.svc.lay = a.lay
	}
	stateDirShown = a.lay.Base
	if st, err := os.Stderr.Stat(); err == nil && st.Mode()&os.ModeCharDevice == 0 {
		serviceMode = true
	}
	logger := log.New(os.Stderr, "", log.LstdFlags)
	logf := func(format string, args ...any) { logger.Printf(format, args...) }
	// Installing and preparing the engine happens under the same lock setup
	// takes: two preparations at once would generate identities over each
	// other.
	unlockSetup, err := waitForLock(ctx, filepath.Join(a.lay.Base, "setup.lock"), 2*time.Minute)
	if err != nil {
		return err
	}
	st, err := loadState(a.lay.State)
	if err != nil {
		unlockSetup()
		return err
	}
	eng := a.engine()
	sum, sumErr := fileSHA256(a.lay.Syncthing)
	if st.Syncthing.Version != syncthingVersion || sumErr != nil || sum != st.Syncthing.BinarySHA256 {
		// Missing, another version, or changed since it was installed:
		// install the pinned one again (verified against the checksum).
		logf("vaultsync %s: installing Syncthing %s", version, syncthingVersion)
		in, err := newInstaller(a.lay.Bin, a.goos, runtime.GOARCH)
		if err != nil {
			unlockSetup()
			return err
		}
		got, err := in.install(ctx)
		if err != nil {
			unlockSetup()
			return err
		}
		st.Syncthing.Version, st.Syncthing.BinarySHA256 = syncthingVersion, got.SHA256
		if err := saveState(a.lay.State, st); err != nil {
			unlockSetup()
			return err
		}
	}
	if err := eng.prepare(ctx, &st); err != nil {
		unlockSetup()
		return err
	}
	if err := saveState(a.lay.State, st); err != nil {
		unlockSetup()
		return err
	}
	// The engine lock makes this process the engine's only owner — and the
	// control socket's: a socket file found now belongs to an agent that is
	// gone. The socket is a convenience for status and the menu-bar app;
	// an engine that cannot have one still runs.
	unlock, err := lockFile(eng.lay.Lock)
	if errors.Is(err, ErrEngineRunning) {
		unlockSetup()
		return refuse("VaultSync is already running on this computer (the background service). See vaultsync status.")
	}
	if err != nil {
		unlockSetup()
		return err
	}
	defer unlock()
	ctl := &controlServer{a: a, eng: eng, logf: logf, background: a.backgroundEngine()}
	if socket, stop, err := ctl.serve(ctx); err != nil {
		logSocketError(logf, a.lay.Base, err)
	} else {
		defer stop()
		if socket == a.lay.Socket {
			socket = ""
		}
		// Published while the setup lock is still held: a second run that
		// waits on it loads agent.json only afterwards and saves the place
		// back as it found it, instead of writing an older copy over it.
		if beforeSocketPublish != nil {
			beforeSocketPublish()
		}
		if st.ControlSocket != socket {
			st.ControlSocket = socket
			if err := saveState(a.lay.State, st); err != nil {
				unlockSetup()
				return err
			}
		}
	}
	unlockSetup()
	logf("vaultsync %s: running the sync engine", version)
	return eng.superviseLocked(ctx, st, logf)
}

// beforeSocketPublish runs right before run records the control socket's
// place in agent.json; a test uses it to look at the locks held then.
var beforeSocketPublish func()

// --- stop / start -----------------------------------------------------------

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
	if err := a.svc.start(); err != nil {
		return err
	}
	// Say "running" only for a service that runs.
	if !a.svc.waitRunning() {
		return refuse("VaultSync asked the system to start its background service, but it is not running. See %s, or run vaultsync setup again.", a.logHint())
	}
	fmt.Fprintln(a.out, "✓ VaultSync is running again.")
	return nil
}
