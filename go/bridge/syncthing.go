// Syncthing instance lifecycle management.
// Provides Start/Stop/IsRunning and DeviceID for the gomobile bridge.
package bridge

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/thejerf/suture/v4"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/locations"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/svcutil"
	"github.com/syncthing/syncthing/lib/syncthing"
)

var (
	mu         sync.Mutex
	stApp      *syncthing.App
	stDB       engineDatabase // syncthing database handle
	stEvLogger events.Logger
	stCfg      config.Wrapper
	stCert     tls.Certificate
	stMyID     protocol.DeviceID
	// stRunning means StartSyncthing allocated an engine instance that has not
	// been released yet. It says nothing about whether that instance is still
	// alive — engineUpLocked does (#181).
	stRunning bool
	// stExited records that the instance's main supervisor exited on its own
	// (fatal service error, internal stop). Set by watchEngine, never by the
	// bridge's own stop. stExitReason keeps the why until the next lifecycle
	// call so the app can show it.
	stExited     bool
	stExitReason string
	// stStopPending is non-nil while a stop that overran its deadline is
	// still finishing in the background. StartSyncthing refuses to start a
	// second instance until it closes — two engines on one config directory
	// would share the database and the folders.
	stStopPending <-chan struct{}
	// Monotonic within this app process. A diagnostics check captures it so an
	// event cursor can never be reused across an engine restart.
	stEventGeneration int64

	// Early service supervisor for evLogger and config wrapper.
	stEarlyCancel context.CancelFunc
	// stEarlyDone receives once, when the early supervisor has stopped its
	// services. The config wrapper writes the configuration as its Serve
	// loop returns, so a stop that has received from it has the last commit
	// on disk (#302) — unless a service outlasted suture's stop timeout
	// (svcutil.ServiceTimeout, 10 s), which the supervisor returns without.
	// stopEngineLocked is its only receiver.
	stEarlyDone <-chan error
)

// singleConnectionPerDevice pins every peer to one connection (#185): the
// multi-connection promotion race in Syncthing 2.x can strand an index
// exchange, and a phone gains nothing from parallel connections to one peer.
// Negotiated with the peer through the hello message, so the peer also
// dials only once.
const singleConnectionPerDevice = 1

// lifecycleTimeouts bounds every wait on another goroutine in the engine
// lifecycle. Without a deadline a dead early supervisor turns the waiting
// call into a permanent hang that holds mu, and every later bridge call
// blocks behind it (#181). Synchronous startup work — opening the database,
// App.Start — is deliberately not bounded: abandoning it cannot cancel it,
// and a retry over abandoned work would open the same database twice
// (decision 038).
var lifecycleTimeouts = struct {
	// Modify + commit on the early supervisor's config loop.
	configCommit time.Duration
	// Event subscription on the early supervisor's event loop.
	subscribe time.Duration
	// App.Stop, then the early supervisor's stop with the config wrapper's
	// last save (#302). Upstream bounds the first at roughly 20 s
	// (services, then the database), suture the second at 10 s; this is
	// the backstop when they do not.
	stop time.Duration
}{
	configCommit: 30 * time.Second,
	subscribe:    10 * time.Second,
	stop:         30 * time.Second,
}

// errWaitTimedOut is what awaitWithin's error wraps when the wait expired:
// the work goes on, so a caller can tell it from a refusal (#343).
var errWaitTimedOut = errors.New("timed out")

// awaitWithin runs fn on its own goroutine and waits at most timeout for it.
// On expiry fn keeps running to completion in the background — its work is
// not cancelled, only no longer waited for — and the error names the phase
// and wraps errWaitTimedOut. A non-positive timeout expires immediately;
// tests use it to drive the timeout path deterministically.
func awaitWithin(phase string, timeout time.Duration, fn func() error) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	if timeout <= 0 {
		return fmt.Errorf("%s: %w after %s", phase, errWaitTimedOut, timeout)
	}
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return fmt.Errorf("%s: %w after %s", phase, errWaitTimedOut, timeout)
	}
}

// engineDatabase is what the bridge itself asks of the engine's database. A
// named set of methods, so an engine version that changes one of them stops
// the build instead of quietly switching off what depends on it.
type engineDatabase interface {
	Close() error
	storageRecords
}

// stConfigCommits counts the configuration changes the bridge has asked for,
// one that timed out included: it may still land. Work that reads without
// the lock compares it to know whether what it read is still the question.
// Guarded by mu.
var stConfigCommits uint64

// configTaken is what the engine did with a change the bridge handed it:
// refused it (err), or took it into its configuration, altering it or not.
type configTaken struct {
	altered bool
	err     error
}

// commitConfigLocked applies fn to the live configuration, writes config.xml
// once the engine has taken the change, and waits, bounded by
// lifecycleTimeouts.configCommit, for the change to reach every subscriber.
// Caller holds mu.
//
// The write comes before the wait (#465, decision 096). The engine takes a
// change into its configuration and only then hands it to its services; the
// model stops a removed folder, takes its marker away and drops its database
// there. config.xml was written only by the wrapper's five-second timer, and
// an app ended before it — closed in the app switcher, or ended by iOS —
// started from a file that still named the folder: the engine took it for a
// new one and synced its directory again. A file ahead of the services is
// completed by the next start; a file behind them is not. A change that
// alters nothing writes nothing, unless the last write failed, and a failed
// write does not change the answer: the change is live.
//
// A change still queued when the wait ends may land later; the caller has
// reported the wait's end, and the engine's writer writes the change when it
// lands, without mu (configWrites).
func commitConfigLocked(fn config.ModifyFunction) error {
	cfg, writes := stCfg, stConfigWrites
	stConfigCommits++
	timeout := lifecycleTimeouts.configCommit
	timedOut := fmt.Errorf("config commit: %w after %s", errWaitTimedOut, timeout)

	taken := make(chan configTaken, 1)
	through := make(chan struct{})
	go func() {
		var altered bool
		waiter, err := cfg.Modify(func(c *config.Configuration) {
			// The wrapper's own test for a change, on copies of both sides.
			before := c.Copy()
			fn(c)
			altered = !reflect.DeepEqual(before, c.Copy())
		})
		taken <- configTaken{altered: altered && err == nil, err: err}
		if err == nil {
			waiter.Wait()
		}
		close(through)
	}()

	if timeout <= 0 {
		go writes.writeWhenTaken(taken)
		return timedOut
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	select {
	case t := <-taken:
		if t.err != nil {
			return t.err
		}
		if t.altered || writes.unwritten() {
			_ = writes.write()
		}
	case <-deadline.C:
		commitDeadlinePassed()
		go writes.writeWhenTaken(taken)
		return timedOut
	}
	select {
	case <-through:
		return nil
	case <-deadline.C:
		return timedOut
	}
}

// commitDeadlinePassed runs when the bridge's wait ends before the engine has
// taken the change. Tests use it to let the change land in that instant.
var commitDeadlinePassed = func() {}

// stConfigWrites writes config.xml for the running engine. Guarded by mu; nil
// while no engine runs.
var stConfigWrites *configWrites

// configWrites writes one engine's configuration to config.xml (#465).
//
// A change still queued when the bridge's wait ends is written when the
// engine takes it, by writeWhenTaken, and not under mu: another call can hold
// mu for its whole wait while the model already drops a removed folder's
// database. Its own lock keeps that write to the engine's life: the stop ends
// the writer before it stops the engine, after a write in progress, so no
// late write runs once a stop has begun, and the stop's own last write comes
// after it (#302). A start that fails cancels the early services without a
// stop, and the writer ends with them.
type configWrites struct {
	cfg   config.Wrapper
	mu    sync.Mutex
	live  bool          // guarded by mu
	ended chan struct{} // closed when live turns false
	// failed records that the last write did not reach the disk: the next
	// change writes again, altered or not.
	failed atomic.Bool
}

func newConfigWrites(ctx context.Context, cfg config.Wrapper) *configWrites {
	w := &configWrites{cfg: cfg, live: true, ended: make(chan struct{})}
	go func() {
		<-ctx.Done()
		w.end()
	}()
	return w
}

// end stops the writer: no late write begins after it returns. It waits for
// a write in progress.
func (w *configWrites) end() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.live {
		w.live = false
		close(w.ended)
	}
}

func (w *configWrites) unwritten() bool {
	return w != nil && w.failed.Load()
}

// write writes the configuration the engine runs with to config.xml through
// the wrapper's own Save: a temporary file in the same directory, synced and
// renamed over the old one, under the wrapper's lock. The caller holds mu, or
// w.mu with w live, so the engine is the running one. A write that fails logs
// one line with the error's kind — never the path, which the error names —
// and the next change writes again.
func (w *configWrites) write() error {
	if w == nil {
		return errors.New("syncthing not running")
	}
	err := w.cfg.Save()
	w.failed.Store(err != nil)
	if err != nil {
		configWriteLog.Printf("config.xml was not written (%s); the next change, the engine's save timer or a stop writes it again", configWriteErrorKind(err))
	}
	return err
}

// writeWhenTaken writes config.xml for a change that was still queued when
// the bridge's wait ended, as soon as the engine takes it. It ends with its
// engine: a change the engine never takes leaves nothing waiting here.
func (w *configWrites) writeWhenTaken(taken <-chan configTaken) {
	if w == nil {
		return
	}
	var t configTaken
	select {
	case t = <-taken:
	case <-w.ended:
		return
	}
	if t.err != nil || (!t.altered && !w.failed.Load()) {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.live {
		_ = w.write()
	}
}

// configWriteLog says that config.xml could not be written. A logger of the
// bridge's own: the engine's log output is discarded at the process boundary
// (configurePrivacySafeLogging), and this line names no path and no vault.
// The bridge has no log the app keeps; FlushConfiguration's answer carries
// the same text to a caller that asks.
var configWriteLog = log.New(os.Stderr, "vaultsync bridge: ", 0)

// configWriteErrorKind names what went wrong with a write without its path.
func configWriteErrorKind(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno.Error()
	}
	return "write failed"
}

// FlushConfiguration writes the configuration the engine runs with to
// config.xml now. It answers "" once the file holds it, or why not — the
// error's kind, never a path (#465, decision 096). For a caller that must know
// a change is on disk before it records that the change was made: a change's
// own answer stays success when its write fails, because the change is live.
func FlushConfiguration() string {
	mu.Lock()
	defer mu.Unlock()

	if !engineUpLocked() || stConfigWrites == nil {
		return "syncthing not running"
	}
	if err := stConfigWrites.write(); err != nil {
		return "config.xml was not written: " + configWriteErrorKind(err)
	}
	return ""
}

// engineUpLocked reports whether the engine can serve calls: an instance is
// allocated and its supervisor has not exited. Caller holds mu. Every
// "is the engine running" guard in the bridge goes through this, so a dead
// engine answers "not running" consistently instead of serving stale config
// as if it were alive (#181).
func engineUpLocked() bool {
	return stRunning && stApp != nil && !stExited
}

// watchEngine blocks until the given instance's supervisor exits and records
// the exit if the bridge still owns that instance. A stop through the bridge
// releases the instance before this runs, so only exits the bridge did not
// initiate are recorded.
func watchEngine(app *syncthing.App) {
	status := app.Wait()
	err := app.Error()

	mu.Lock()
	defer mu.Unlock()
	if stApp != app {
		return
	}
	stExited = true
	stExitReason = fmt.Sprintf("sync engine exited (status %d)", status.AsInt())
	if err != nil {
		stExitReason += ": " + err.Error()
	}
}

// StartSyncthing initializes and starts the embedded Syncthing instance.
// configDir is the base directory for config, certs, and database.
// Returns empty string on success, error message on failure.
func StartSyncthing(configDir string) string {
	mu.Lock()
	defer mu.Unlock()
	configurePrivacySafeLogging()

	if stRunning {
		if !stExited {
			return "already running"
		}
		// The supervisor exited on its own. The app sees "not running" and
		// restarts (decision 009); release the dead instance first — its
		// database is already closed by the App, the early services are not.
		if errMsg := stopEngineLocked(); errMsg != "" {
			return fmt.Sprintf("release exited engine: %s", errMsg)
		}
	}
	if stStopPending != nil {
		select {
		case <-stStopPending:
			stStopPending = nil
		default:
			return "start: the previous engine is still stopping"
		}
	}
	stExited = false
	stExitReason = ""

	// Set base directories so locations.Get() resolves correctly.
	if err := locations.SetBaseDir(locations.ConfigBaseDir, configDir); err != nil {
		return fmt.Sprintf("set config dir: %v", err)
	}
	dataDir := filepath.Join(configDir, "data")
	if err := locations.SetBaseDir(locations.DataBaseDir, dataDir); err != nil {
		return fmt.Sprintf("set data dir: %v", err)
	}

	// Ensure directories exist.
	if err := syncthing.EnsureDir(configDir, 0o700); err != nil {
		return fmt.Sprintf("ensure config dir: %v", err)
	}
	if err := syncthing.EnsureDir(dataDir, 0o700); err != nil {
		return fmt.Sprintf("ensure data dir: %v", err)
	}

	// Load the TLS certificate (persisted in configDir), generating it only
	// on a confirmed first launch. Never falls back to regeneration: a new
	// certificate is a new device identity, which silently invalidates every
	// peer pairing (#135).
	var err error
	stCert, err = loadOrCreateIdentity(
		locations.Get(locations.CertFile),
		locations.Get(locations.KeyFile),
	)
	if err != nil {
		return fmt.Sprintf("certificate: %v", err)
	}

	// Derive device ID from certificate.
	stMyID = protocol.NewDeviceID(stCert.Certificate[0])

	// Start an early service supervisor for the event logger and config wrapper.
	// The config wrapper's Modify() requires its Serve() loop to be running.
	ctx, cancel := context.WithCancel(context.Background())
	stEarlyCancel = cancel
	earlySvc := suture.New("early", svcutil.SpecWithDebugLogger())
	stEarlyDone = earlySvc.ServeBackground(ctx)

	// Create and register event logger.
	stEvLogger = events.NewLogger()
	earlySvc.Add(stEvLogger)

	// Load existing config or create a default one.
	// skipPortProbing=true because iOS doesn't need port probing.
	stCfg, err = syncthing.LoadConfigAtStartup(
		locations.Get(locations.ConfigFile),
		stCert, stEvLogger, false, true,
	)
	if err != nil {
		cancel()
		return fmt.Sprintf("config: %v", err)
	}
	earlySvc.Add(stCfg)
	// Writes config.xml for this engine's changes (#465); ends with the
	// early services, so with every failed start below.
	stConfigWrites = newConfigWrites(ctx, stCfg)

	// Configure for embedded iOS use.
	err = commitConfigLocked(func(cfg *config.Configuration) {
		cfg.GUI.Enabled = false
		cfg.Options.URAccepted = -1 // disable usage reporting
		cfg.Options.CREnabled = false
		cfg.Options.AutoUpgradeIntervalH = 0
		cfg.Options.AnnounceLANAddresses = true
		cfg.Options.LocalAnnEnabled = true
		cfg.Options.GlobalAnnEnabled = true
		cfg.Options.RelaysEnabled = true
		cfg.Options.NATEnabled = true
	})
	if err != nil {
		cancel()
		return fmt.Sprintf("configure: %v", err)
	}

	// Migration: lower the over-conservative 60-minute rescan fallback that
	// older VaultSync builds wrote to disk down to Syncthing's standard 60 s.
	// Only touches folders that still carry the legacy default — user-customised
	// values are preserved.
	err = commitConfigLocked(func(cfg *config.Configuration) {
		for i := range cfg.Folders {
			if cfg.Folders[i].RescanIntervalS == 3600 {
				cfg.Folders[i].RescanIntervalS = defaultRescanIntervalS
			}
		}
	})
	if err != nil {
		cancel()
		return fmt.Sprintf("migrate rescan interval: %v", err)
	}

	// Migration: one connection per peer (#185). Syncthing 2.x opens up to
	// three connections to a device by default and promotes one of them per
	// side to carry index messages; the two sides can promote different
	// connections, and an index that then arrives on the other one closes
	// that connection with "folder is not running". A heavy index widens
	// that window until the exchange never completes. Either side asking
	// for a single connection makes both sides use one, so the race has no
	// second connection to land on. Idempotent; applies to devices added by
	// older builds and by peers' share offers.
	err = commitConfigLocked(func(cfg *config.Configuration) {
		for i := range cfg.Devices {
			if cfg.Devices[i].RawNumConnections != singleConnectionPerDevice {
				cfg.Devices[i].RawNumConnections = singleConnectionPerDevice
			}
		}
	})
	if err != nil {
		cancel()
		return fmt.Sprintf("migrate connection count: %v", err)
	}

	// Open database. Synchronous work, not a wait — see lifecycleTimeouts.
	sdb, err := syncthing.OpenDatabase(
		locations.Get(locations.Database),
		24*time.Hour,
	)
	if err != nil {
		cancel()
		return fmt.Sprintf("database: %v", err)
	}

	// Create and start Syncthing.
	opts := syncthing.Options{
		NoUpgrade: true,
	}
	stApp, err = syncthing.New(stCfg, sdb, stEvLogger, stCert, opts)
	if err != nil {
		sdb.Close()
		cancel()
		return fmt.Sprintf("create app: %v", err)
	}

	// Synchronous startup work, not a wait — see lifecycleTimeouts.
	if err := stApp.Start(); err != nil {
		sdb.Close()
		cancel()
		stApp = nil
		return fmt.Sprintf("start: %v", err)
	}

	stDB = sdb
	stRunning = true
	go watchEngine(stApp)

	// What this install's records of folder directories are tied to (#315).
	// The directories themselves are read by NoteFolderStorage, not here:
	// nothing in a start leaves the app's own container.
	stStorageAnchor, stStorageAnchorErr = readStorageAnchor(configDir)

	// Create a buffered event subscription for the bridge.
	var sub events.Subscription
	evLogger := stEvLogger
	err = awaitWithin("event subscription", lifecycleTimeouts.subscribe, func() error {
		sub = evLogger.Subscribe(events.AllEvents)
		return nil
	})
	if err != nil {
		// The engine is up but the bridge cannot observe it. Stop it again
		// rather than run blind; a stop that overruns its own deadline
		// leaves stStopPending set, so the next start waits for it.
		stopEngineLocked()
		return fmt.Sprintf("events: %v", err)
	}
	stEventSub = events.NewBufferedSubscription(sub, 200)
	stEventGeneration++

	// Remember successful outbound connection addresses so the next cold
	// start can dial peers immediately, without a discovery round trip.
	// Stops when the early-supervisor context is canceled in StopSyncthing.
	startAddressCache(ctx, stCfg, stEvLogger)

	return ""
}

// Syncthing's upstream log attributes can contain file paths, folder IDs, and
// peer IDs even at info/warning level. The embedded app surfaces sanitized
// state through the bridge instead, so upstream stdout logging is disabled at
// the process boundary rather than attempting incomplete value redaction.
func configurePrivacySafeLogging() {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	log.SetOutput(io.Discard)
}

// stopEngineLocked stops the current instance and releases everything
// StartSyncthing allocated for it. Caller holds mu. The bridge state is
// released before the stop completes, so a stop that overruns its deadline
// never wedges mu: the instance finishes stopping in the background, and
// stStopPending keeps StartSyncthing from starting a second one until it has.
// Returns "" on success, otherwise the timeout text.
func stopEngineLocked() string {
	app, sdb, earlyCancel, earlyDone := stApp, stDB, stEarlyCancel, stEarlyDone
	stApp, stDB, stEarlyCancel, stEarlyDone = nil, nil, nil, nil
	stStorageAnchor, stStorageAnchorErr = 0, nil
	stEventSub = nil
	stRunning = false
	stExited = false
	stExitReason = ""

	// No late write of config.xml begins once the stop has begun, and one in
	// progress finishes first: the stop's own last write below comes after it
	// (#465).
	stConfigWrites.end()
	stConfigWrites = nil

	// Order matters: the App's services still use the config wrapper and
	// the event logger while they shut down, so the early supervisor is
	// canceled only after the App has stopped. Its config wrapper writes the
	// configuration as it stops, and the stop returns only once the early
	// supervisor has stopped it, the write done, within suture's stop
	// timeout: a stop that returned before it left the last commit off disk,
	// for a process iOS suspends or ends right after the stop, and for a
	// start that read config.xml first (#302). earlyDone receives once, so
	// this is its only receive; a cancel without a channel leaves nothing to
	// wait for.
	release := func() {
		if sdb != nil {
			sdb.Close()
		}
		if earlyCancel != nil {
			earlyCancel()
			if earlyDone != nil {
				<-earlyDone
			}
		}
	}

	// One bounded wait for the whole stop, the save included, so no wait
	// under mu is unbounded (#181). On expiry the stop finishes in the
	// background, and stStopPending keeps the next start from reading
	// config.xml before the save has landed.
	stopped := make(chan struct{})
	err := awaitWithin("stop", lifecycleTimeouts.stop, func() error {
		defer close(stopped)
		if app != nil {
			app.Stop(svcutil.ExitSuccess)
		}
		release()
		return nil
	})
	if err == nil {
		return ""
	}
	stStopPending = stopped
	return err.Error()
}

// StopSyncthing gracefully stops the running Syncthing instance.
// Returns empty string on success, or the error text when the stop overran
// its deadline — the instance then finishes stopping in the background and
// the next StartSyncthing is refused until it has.
func StopSyncthing() string {
	mu.Lock()
	defer mu.Unlock()

	if !stRunning {
		return ""
	}
	return stopEngineLocked()
}

// IsRunning returns true if Syncthing is currently running: an instance was
// started through the bridge and its supervisor has not exited (#181).
func IsRunning() bool {
	mu.Lock()
	defer mu.Unlock()
	return engineUpLocked()
}

// EngineExitReason describes why the engine stopped on its own, for the app
// to show next to its "stopped unexpectedly" message. Empty while the engine
// runs, after a stop through the bridge, and once a new start has begun.
// Contains upstream error text, which may name paths — treat as private.
func EngineExitReason() string {
	mu.Lock()
	defer mu.Unlock()
	return stExitReason
}

// EventStreamGeneration identifies the currently running event stream.
// It contains no device or folder information and is never persisted.
// Zero means that no event stream is currently running.
func EventStreamGeneration() int64 {
	mu.Lock()
	defer mu.Unlock()
	if !engineUpLocked() || stEventSub == nil {
		return 0
	}
	return stEventGeneration
}

// DeviceID returns this device's ID in canonical format (e.g. XXXXXXX-XXXXXXX-...).
// Returns empty string if Syncthing has not been started.
func DeviceID() string {
	mu.Lock()
	defer mu.Unlock()

	if !engineUpLocked() {
		return ""
	}
	return stMyID.String()
}
