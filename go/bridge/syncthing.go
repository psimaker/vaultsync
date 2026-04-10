// Syncthing instance lifecycle management.
// Provides Start/Stop/IsRunning and DeviceID for the gomobile bridge.
package bridge

import (
	"context"
	"crypto/tls"
	"fmt"
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
	stRunning  bool

	// Early service supervisor for evLogger and config wrapper.
	stEarlyCancel context.CancelFunc
)

// StartSyncthing initializes and starts the embedded Syncthing instance.
// configDir is the base directory for config, certs, and database.
// Returns empty string on success, error message on failure.
func StartSyncthing(configDir string) string {
	mu.Lock()
	defer mu.Unlock()

	if stRunning {
		return "already running"
	}

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

	// Load or generate TLS certificate (persisted in configDir).
	var err error
	stCert, err = syncthing.LoadOrGenerateCertificate(
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
	waiter, err := stCfg.Modify(func(cfg *config.Configuration) {
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
	waiter.Wait()

	// Open database.
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

	if err := stApp.Start(); err != nil {
		sdb.Close()
		cancel()
		stApp = nil
		return fmt.Sprintf("start: %v", err)
	}

	stDB = sdb

	// Create a buffered event subscription for the bridge.
	sub := stEvLogger.Subscribe(events.AllEvents)
	stEventSub = events.NewBufferedSubscription(sub, 200)

	stRunning = true
	return ""
}

// StopSyncthing gracefully stops the running Syncthing instance.
func StopSyncthing() {
	mu.Lock()
	defer mu.Unlock()

	if !stRunning || stApp == nil {
		return
	}

	stApp.Stop(svcutil.ExitSuccess)
	stApp = nil
	stEventSub = nil

	// Close database handle.
	if stDB != nil {
		stDB.Close()
		stDB = nil
	}

	// Stop early services (evLogger, config wrapper).
	if stEarlyCancel != nil {
		stEarlyCancel()
		stEarlyCancel = nil
	}

	stRunning = false
}

// IsRunning returns true if Syncthing is currently running.
func IsRunning() bool {
	mu.Lock()
	defer mu.Unlock()
	return stRunning
}

// DeviceID returns this device's ID in canonical format (e.g. XXXXXXX-XXXXXXX-...).
// Returns empty string if Syncthing has not been started.
func DeviceID() string {
	mu.Lock()
	defer mu.Unlock()

	if !stRunning {
		return ""
	}
	return stMyID.String()
}
