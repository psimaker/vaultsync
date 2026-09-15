// Syncthing instance lifecycle management.
// Provides Start/Stop/IsRunning and DeviceID for the gomobile bridge.
package bridge

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"log/slog"
	"path/filepath"
	"sync"
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
	stDB       interface{ Close() error } // syncthing database handle
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
)

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
	// App.Stop. Upstream bounds it at roughly 20 s (services, then the
	// database); this is the backstop when it does not.
	stop time.Duration
}{
	configCommit: 30 * time.Second,
	subscribe:    10 * time.Second,
	stop:         30 * time.Second,
}

// awaitWithin runs fn on its own goroutine and waits at most timeout for it.
// On expiry fn keeps running to completion in the background — its work is
// not cancelled, only no longer waited for — and the error names the phase.
// A non-positive timeout expires immediately; tests use it to drive the
// timeout path deterministically.
func awaitWithin(phase string, timeout time.Duration, fn func() error) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	if timeout <= 0 {
		return fmt.Errorf("%s: timed out after %s", phase, timeout)
	}
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		return fmt.Errorf("%s: timed out after %s", phase, timeout)
	}
}

// commitConfigLocked applies fn to the live configuration and waits, bounded
// by lifecycleTimeouts.configCommit, for the commit to reach every
// subscriber. Caller holds mu. A timed-out commit may still land later; the
// caller has already reported the failure, and the next poll shows the
// actual state.
func commitConfigLocked(fn config.ModifyFunction) error {
	cfg := stCfg
	return awaitWithin("config commit", lifecycleTimeouts.configCommit, func() error {
		waiter, err := cfg.Modify(fn)
		if err != nil {
			return err
		}
		waiter.Wait()
		return nil
	})
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
	earlySvc.ServeBackground(ctx)

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
	app, sdb, earlyCancel := stApp, stDB, stEarlyCancel
	stApp, stDB, stEarlyCancel = nil, nil, nil
	stEventSub = nil
	stRunning = false
	stExited = false
	stExitReason = ""

	// Order matters: the App's services still use the config wrapper and
	// the event logger while they shut down, so the early supervisor is
	// canceled only after the App has stopped.
	release := func() {
		if sdb != nil {
			sdb.Close()
		}
		if earlyCancel != nil {
			earlyCancel()
		}
	}
	if app == nil {
		release()
		return ""
	}

	err := awaitWithin("stop", lifecycleTimeouts.stop, func() error {
		app.Stop(svcutil.ExitSuccess)
		return nil
	})
	if err == nil {
		release()
		return ""
	}

	pending := make(chan struct{})
	stStopPending = pending
	go func() {
		app.Wait()
		release()
		close(pending)
	}()
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
