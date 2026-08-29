package bridge

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thejerf/suture/v4"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/svcutil"
	"github.com/syncthing/syncthing/lib/syncthing"
	"github.com/syncthing/syncthing/lib/tlsutil"
)

func TestIssue150ReceiveReadOnlyScannerHasherLogsAreDiscardedAtBridgeBoundary(t *testing.T) {
	var captured bytes.Buffer
	previousSlog := slog.Default()
	previousLogWriter := log.Writer()
	t.Cleanup(func() {
		slog.SetDefault(previousSlog)
		log.SetOutput(previousLogWriter)
	})
	slog.SetDefault(slog.New(slog.NewTextHandler(&captured, nil)))
	log.SetOutput(&captured)

	configurePrivacySafeLogging()
	const redactionProbe = "issue150-redaction-probe/scanner-hasher-note.md"
	slog.Error("scanner failure", slog.String("path", redactionProbe))
	log.Printf("hasher failure: %s", redactionProbe)
	if strings.Contains(captured.String(), redactionProbe) {
		t.Fatalf("scanner/hasher boundary leaked a private path: %q", captured.String())
	}
}

func TestIssue150VaultSyncStartsReceiveFoldersInGlobalReadOnlySafetyStop(t *testing.T) {
	configDir := testConfigDir(t)
	const folderID = "issue150-global-receive-read-only"
	folderPath := filepath.Join(configDir, "receive-root")
	issue150SeedBridgeFolderBeforeStart(t, configDir, folderID, folderPath, config.FolderTypeSendReceive)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("start: %s", errMsg)
	}
	t.Cleanup(StopSyncthing)

	const wantStop = "vaultsync-conflict-retention-safety-stop"
	if got := RescanFolder(folderID); got != wantStop {
		t.Fatalf("rescan result = %q, want fixed global safety stop", got)
	}
	if got := SetFolderIgnores(folderID, `["issue150-redaction-probe"]`); got != wantStop {
		t.Fatalf("set ignores result = %q, want fixed global safety stop", got)
	}
	if _, err := os.Stat(filepath.Join(folderPath, config.DefaultMarkerName)); !os.IsNotExist(err) {
		t.Fatalf("folder startup created or exposed a marker in read-only mode: %v", err)
	}
	if _, err := os.Stat(filepath.Join(folderPath, ".stignore")); !os.IsNotExist(err) {
		t.Fatalf("read-only ignore request created .stignore: %v", err)
	}

	var status FolderStatus
	if err := json.Unmarshal([]byte(GetFolderStatusJSON(folderID)), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.State != "error" || status.ErrorReason != conflictRetentionSafetyErrorReason || status.ErrorPath != "" {
		t.Fatalf("global safety status is not stable and path-free: %+v", status)
	}
}

func TestIssue150ReceiveConfigurationABIsStopBeforeFilesystemOrConfigMutation(t *testing.T) {
	configDir := testConfigDir(t)
	const folderID = "issue150-set-path-stub"
	originalPath := filepath.Join(configDir, "original")
	issue150SeedBridgeFolderBeforeStart(t, configDir, folderID, originalPath, config.FolderTypeReceiveOnly)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("start: %s", errMsg)
	}
	t.Cleanup(StopSyncthing)

	const wantStop = "vaultsync-conflict-retention-safety-stop"
	addPath := filepath.Join(configDir, "add-target")
	acceptPath := filepath.Join(configDir, "accept-target")
	if got := AddFolder("issue150-add-stub", "Issue 150 add", addPath); got != wantStop {
		t.Fatalf("AddFolder() = %q, want fixed safety stop", got)
	}
	if got := AcceptPendingFolder("issue150-accept-stub", "Issue 150 accept", acceptPath, true); got != wantStop {
		t.Fatalf("AcceptPendingFolder() = %q, want fixed safety stop", got)
	}
	for _, path := range []string{addPath, acceptPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("receive configuration ABI created a target path: %v", err)
		}
	}
	if _, exists := stCfg.Folders()["issue150-add-stub"]; exists {
		t.Fatal("AddFolder mutated config before the safety stop")
	}
	if _, exists := stCfg.Folders()["issue150-accept-stub"]; exists {
		t.Fatal("AcceptPendingFolder mutated config before the safety stop")
	}

	targetPath := filepath.Join(configDir, "target")
	writeFolderMarker(t, targetPath, folderID)
	if got := SetFolderPath(folderID, targetPath); got != wantStop {
		t.Fatalf("SetFolderPath() = %q, want fixed safety stop", got)
	}
	if got := filepath.Clean(stCfg.Folders()[folderID].Path); got != filepath.Clean(originalPath) {
		t.Fatalf("SetFolderPath changed receive config: got %q want %q", got, originalPath)
	}

	for name, call := range map[string]func() string{
		"SetFolderPath":        func() string { return SetFolderPath("issue150-unknown", targetPath) },
		"SetFolderIgnores":     func() string { return SetFolderIgnores("issue150-unknown", `{not-json`) },
		"EnsureDefaultIgnores": func() string { return EnsureDefaultIgnores("issue150-unknown", `{not-json`) },
		"RescanFolder":         func() string { return RescanFolder("issue150-unknown") },
	} {
		if got := call(); got != wantStop {
			t.Errorf("%s(unknown) = %q, want fixed safety stop", name, got)
		}
	}
}

func TestIssue150GlobalReceiveCreationABIStubsAreStableForEveryStateAndInput(t *testing.T) {
	StopSyncthing()
	const wantStop = "vaultsync-conflict-retention-safety-stop"
	inputs := []struct {
		name string
		call func() string
	}{
		{"AddFolder empty", func() string { return AddFolder("", "", "") }},
		{"AddFolder traversal", func() string { return AddFolder("../outside", "Outside", "../../outside") }},
		{"AcceptPendingFolder empty", func() string { return AcceptPendingFolder("", "", "", false) }},
		{"AcceptPendingFolder traversal", func() string { return AcceptPendingFolder("../outside", "Outside", "../../outside", true) }},
	}
	for _, input := range inputs {
		t.Run(input.name, func(t *testing.T) {
			if got := input.call(); got != wantStop {
				t.Fatalf("stub result = %q, want fixed safety stop", got)
			}
		})
	}
}

func TestIssue150SendOnlyConfigurationABIsRetainExistingSemantics(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("start: %s", errMsg)
	}
	t.Cleanup(StopSyncthing)

	const folderID = "issue150-send-only-configuration"
	folderPath := filepath.Join(configDir, "send-only-root")
	issue150ConfigureBridgeFolder(t, folderID, folderPath, config.FolderTypeSendOnly, true)
	if got := SetFolderPath(folderID, folderPath); got != "" {
		t.Fatalf("send-only SetFolderPath no-op = %q", got)
	}
	if got := SetFolderIgnores(folderID, `["*.tmp"]`); got != "" {
		t.Fatalf("send-only SetFolderIgnores = %q", got)
	}
	if got := EnsureDefaultIgnores(folderID, `[".DS_Store"]`); got != "" {
		t.Fatalf("send-only EnsureDefaultIgnores = %q", got)
	}
	if got := RescanFolder(folderID); got != "" {
		t.Fatalf("send-only RescanFolder = %q", got)
	}
}

func TestIssue150PersistedMaxConflictsValuesSurviveSameHomeReloadWithoutRewrite(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("first start: %s", errMsg)
	}

	values := []int{0, 10, -1, 23}
	paths := make(map[string]string, len(values))
	for _, value := range values {
		folderID := issue150ConfigurationFolderID(value)
		folderPath := filepath.Join(configDir, folderID)
		paths[folderID] = folderPath
		issue150ConfigureBridgeFolder(t, folderID, folderPath, config.FolderTypeSendOnly, false)
		issue150AssertConfiguredMaxConflicts(t, folderID, 10)
	}

	waiter, err := stCfg.Modify(func(cfg *config.Configuration) {
		for index := range cfg.Folders {
			for _, value := range values {
				if cfg.Folders[index].ID == issue150ConfigurationFolderID(value) {
					cfg.Folders[index].MaxConflicts = value
				}
			}
		}
	})
	if err != nil {
		StopSyncthing()
		t.Fatalf("set persisted values: %v", err)
	}
	waiter.Wait()
	for _, value := range values {
		issue150AssertConfiguredMaxConflicts(t, issue150ConfigurationFolderID(value), value)
	}
	identityBefore := DeviceID()
	StopSyncthing()
	time.Sleep(100 * time.Millisecond)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("same-home reload: %s", errMsg)
	}
	t.Cleanup(StopSyncthing)
	if DeviceID() != identityBefore {
		t.Fatal("device identity changed across same-home reload")
	}
	for _, value := range values {
		folderID := issue150ConfigurationFolderID(value)
		issue150AssertConfiguredMaxConflicts(t, folderID, value)
		folder := stCfg.Folders()[folderID]
		if filepath.Clean(folder.Path) != filepath.Clean(paths[folderID]) {
			t.Fatalf("folder %s path changed across reload: got %q want %q", folderID, folder.Path, paths[folderID])
		}
	}
}

func TestIssue150GenuinePendingFolderOfferRemainsInspectionOnly(t *testing.T) {
	configurePrivacySafeLogging()
	if IsRunning() {
		t.Fatal("bridge engine was already running")
	}

	localCert := issue150TestCertificate(t)
	localID := protocol.NewDeviceID(localCert.Certificate[0])
	providerCert := issue150TestCertificate(t)
	providerID := protocol.NewDeviceID(providerCert.Certificate[0])

	const folderID = "issue150-genuine-pending"
	providerHome := t.TempDir()
	providerFolderPath := filepath.Join(providerHome, "offered")
	if err := os.MkdirAll(providerFolderPath, 0o700); err != nil {
		t.Fatalf("create provider folder: %v", err)
	}
	providerRaw := issue150HermeticConfig(providerID)
	providerRaw.Options.RawListenAddresses = []string{issue150AvailableTCPListenAddress(t)}
	providerRaw.SetDevice(config.DeviceConfiguration{
		DeviceID:  localID,
		Addresses: []string{"dynamic"},
	})
	providerFolder := providerRaw.Defaults.Folder.Copy()
	providerFolder.ID = folderID
	providerFolder.Label = "Issue 150 synthetic offer"
	providerFolder.Path = providerFolderPath
	providerFolder.Type = config.FolderTypeSendReceive
	providerFolder.FSWatcherEnabled = false
	providerFolder.Devices = []config.FolderDeviceConfiguration{
		{DeviceID: providerID},
		{DeviceID: localID},
	}
	providerRaw.SetFolder(providerFolder)

	_, providerEvents := issue150StartHermeticApp(t, providerHome, providerRaw, providerCert, false)
	providerAddress := issue150WaitForTCPListenAddress(t, providerEvents)

	localHome := t.TempDir()
	localRaw := issue150HermeticConfig(localID)
	localRaw.SetDevice(config.DeviceConfiguration{
		DeviceID:  providerID,
		Addresses: []string{providerAddress},
	})
	local, _ := issue150StartHermeticApp(t, localHome, localRaw, localCert, true)

	issue150WaitForPendingOffer(t, local.app, folderID, providerID)

	mu.Lock()
	stApp = local.app
	stCfg = local.cfg
	stMyID = localID
	stRunning = true
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		stApp = nil
		stCfg = nil
		stMyID = protocol.EmptyDeviceID
		stRunning = false
		mu.Unlock()
	})

	acceptedPath := filepath.Join(localHome, "accepted")
	const wantStop = "vaultsync-conflict-retention-safety-stop"
	if errMsg := AcceptPendingFolder(folderID, "Issue 150 accepted", acceptedPath, false); errMsg != wantStop {
		t.Fatalf("accept genuine pending folder = %q, want fixed safety stop", errMsg)
	}
	if _, ok := local.cfg.Folders()[folderID]; ok {
		t.Fatal("inspection-only pending accept mutated live config")
	}
	if _, err := os.Stat(acceptedPath); !os.IsNotExist(err) {
		t.Fatalf("inspection-only pending accept created a target: %v", err)
	}
	pending, err := local.app.Internals.PendingFolders(protocol.EmptyDeviceID)
	if err != nil {
		t.Fatalf("read pending folders after blocked accept: %v", err)
	}
	if _, remains := pending[folderID]; !remains {
		t.Fatal("blocked pending offer disappeared instead of remaining inspectable")
	}
}

func TestIssue150ReceiveReadOnlySameHomeRestartPreservesIdentityConfigAndNeed(t *testing.T) {
	configurePrivacySafeLogging()
	const folderID = "issue150-same-home-need"

	localHome := t.TempDir()
	localCertPath := filepath.Join(localHome, "cert.pem")
	localKeyPath := filepath.Join(localHome, "key.pem")
	localCert, err := tlsutil.NewCertificate(localCertPath, localKeyPath, "syncthing", 365, false)
	if err != nil {
		t.Fatalf("create persisted local identity: %v", err)
	}
	localID := protocol.NewDeviceID(localCert.Certificate[0])
	providerCert := issue150TestCertificate(t)
	providerID := protocol.NewDeviceID(providerCert.Certificate[0])

	providerHome := t.TempDir()
	providerFolderPath := filepath.Join(providerHome, "source")
	if err := os.MkdirAll(providerFolderPath, 0o700); err != nil {
		t.Fatalf("create provider folder: %v", err)
	}
	if err := os.WriteFile(filepath.Join(providerFolderPath, "remote-note.md"), []byte("remote bytes\n"), 0o600); err != nil {
		t.Fatalf("write provider file: %v", err)
	}
	providerRaw := issue150HermeticConfig(providerID)
	providerRaw.Options.RawListenAddresses = []string{issue150AvailableTCPListenAddress(t)}
	providerRaw.SetDevice(config.DeviceConfiguration{DeviceID: localID, Addresses: []string{"dynamic"}})
	providerFolder := providerRaw.Defaults.Folder.Copy()
	providerFolder.ID = folderID
	providerFolder.Label = "Issue 150 provider"
	providerFolder.Path = providerFolderPath
	providerFolder.Type = config.FolderTypeSendOnly
	providerFolder.FSWatcherEnabled = false
	providerFolder.Devices = []config.FolderDeviceConfiguration{{DeviceID: providerID}, {DeviceID: localID}}
	providerRaw.SetFolder(providerFolder)
	provider, providerEvents := issue150StartHermeticApp(t, providerHome, providerRaw, providerCert, false)
	providerAddress := issue150WaitForTCPListenAddress(t, providerEvents)

	localFolderPath := filepath.Join(localHome, "destination")
	if err := os.MkdirAll(localFolderPath, 0o700); err != nil {
		t.Fatalf("create local folder: %v", err)
	}
	localRaw := issue150HermeticConfig(localID)
	localRaw.SetDevice(config.DeviceConfiguration{DeviceID: providerID, Addresses: []string{providerAddress}})
	localFolder := localRaw.Defaults.Folder.Copy()
	localFolder.ID = folderID
	localFolder.Label = "Issue 150 local"
	localFolder.Path = localFolderPath
	localFolder.Type = config.FolderTypeSendReceive
	localFolder.FSWatcherEnabled = false
	localFolder.Devices = []config.FolderDeviceConfiguration{{DeviceID: localID}, {DeviceID: providerID}}
	localRaw.SetFolder(localFolder)
	local, _ := issue150StartHermeticApp(t, localHome, localRaw, localCert, true)

	issue150WaitForNeed(t, local.app, folderID, 1)
	beforeNeed, err := local.app.Internals.NeedSize(folderID, protocol.LocalDeviceID)
	if err != nil {
		t.Fatalf("read need before restart: %v", err)
	}
	beforeConfig := local.cfg.Folders()[folderID]
	if localSize, err := local.app.Internals.LocalSize(folderID); err != nil || localSize.TotalItems() != 0 {
		t.Fatalf("read-only local inventory before restart = %+v, error=%v", localSize, err)
	}
	if _, err := os.Stat(filepath.Join(localFolderPath, config.DefaultMarkerName)); !os.IsNotExist(err) {
		t.Fatalf("read-only startup created a local marker: %v", err)
	}

	provider.stop(t)
	local.stop(t)
	reloadedCert, err := tls.LoadX509KeyPair(localCertPath, localKeyPath)
	if err != nil {
		t.Fatalf("reload persisted local identity: %v", err)
	}
	restarted := issue150RestartHermeticAppFromSameHome(t, localHome, reloadedCert)
	if got := protocol.NewDeviceID(reloadedCert.Certificate[0]); got != localID {
		t.Fatalf("same-home identity changed: got %s want %s", got, localID)
	}
	afterConfig := restarted.cfg.Folders()[folderID]
	if afterConfig.ID != beforeConfig.ID || afterConfig.Path != beforeConfig.Path || afterConfig.Type != beforeConfig.Type || afterConfig.Paused != beforeConfig.Paused {
		t.Fatalf("same-home folder config changed:\nbefore=%+v\nafter=%+v", beforeConfig, afterConfig)
	}
	afterNeed, err := restarted.app.Internals.NeedSize(folderID, protocol.LocalDeviceID)
	if err != nil {
		t.Fatalf("read need after restart: %v", err)
	}
	if afterNeed != beforeNeed || afterNeed.Files != 1 {
		t.Fatalf("same-home need changed: before=%+v after=%+v", beforeNeed, afterNeed)
	}
	if localSize, err := restarted.app.Internals.LocalSize(folderID); err != nil || localSize.TotalItems() != 0 {
		t.Fatalf("read-only local inventory after restart = %+v, error=%v", localSize, err)
	}
	if _, err := os.Stat(filepath.Join(localFolderPath, config.DefaultMarkerName)); !os.IsNotExist(err) {
		t.Fatalf("same-home restart created a local marker: %v", err)
	}
}

type issue150HermeticApp struct {
	app       *syncthing.App
	cfg       config.Wrapper
	db        interface{ Close() error }
	cancel    context.CancelFunc
	earlyDone <-chan error
	stopOnce  sync.Once
}

func (app *issue150HermeticApp) stop(t *testing.T) {
	t.Helper()
	app.stopOnce.Do(func() {
		app.app.Stop(svcutil.ExitSuccess)
		if err := app.db.Close(); err != nil {
			t.Errorf("close synthetic database: %v", err)
		}
		app.cancel()
		select {
		case <-app.earlyDone:
		case <-time.After(5 * time.Second):
			t.Error("synthetic config supervisor did not stop")
		}
	})
}

func issue150TestCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	cert, err := tlsutil.NewCertificateInMemory("syncthing", 1)
	if err != nil {
		t.Fatalf("create synthetic certificate: %v", err)
	}
	return cert
}

func issue150HermeticConfig(id protocol.DeviceID) config.Configuration {
	cfg := config.New(id)
	cfg.GUI.Enabled = false
	cfg.Options.RawListenAddresses = nil
	cfg.Options.GlobalAnnEnabled = false
	cfg.Options.LocalAnnEnabled = false
	cfg.Options.RelaysEnabled = false
	cfg.Options.NATEnabled = false
	cfg.Options.StartBrowser = false
	cfg.Options.URAccepted = -1
	cfg.Options.CREnabled = false
	cfg.Options.AutoUpgradeIntervalH = 0
	cfg.Options.MinHomeDiskFree.Value = 0
	return cfg
}

func issue150AvailableTCPListenAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve synthetic TCP endpoint: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release synthetic TCP endpoint: %v", err)
	}
	return "tcp://" + address
}

func issue150StartHermeticApp(t *testing.T, home string, raw config.Configuration, cert tls.Certificate, receiveSideReadOnly bool) (*issue150HermeticApp, events.Subscription) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	early := suture.New("issue150-hermetic", svcutil.SpecWithDebugLogger())
	earlyDone := early.ServeBackground(ctx)
	logger := events.NewLogger()
	early.Add(logger)
	cfg := config.Wrap(filepath.Join(home, "config.xml"), raw, protocol.NewDeviceID(cert.Certificate[0]), logger)
	early.Add(cfg)
	if err := cfg.Save(); err != nil {
		cancel()
		t.Fatalf("save synthetic config: %v", err)
	}
	listenEvents := logger.Subscribe(events.AllEvents)
	var databaseOptions []syncthing.DatabaseOption
	if receiveSideReadOnly {
		databaseOptions = append(databaseOptions, syncthing.WithReceiveSideReadOnlyConfig(cfg))
	}
	database, err := syncthing.OpenDatabase(filepath.Join(home, "database"), 24*time.Hour, databaseOptions...)
	if err != nil {
		cancel()
		t.Fatalf("open synthetic database: %v", err)
	}
	app, err := syncthing.New(cfg, database, logger, cert, syncthing.Options{
		NoUpgrade:           true,
		ReceiveSideReadOnly: receiveSideReadOnly,
	})
	if err != nil {
		database.Close()
		cancel()
		t.Fatalf("create synthetic app: %v", err)
	}
	harness := &issue150HermeticApp{app: app, cfg: cfg, db: database, cancel: cancel, earlyDone: earlyDone}
	t.Cleanup(func() { harness.stop(t) })
	if err := app.Start(); err != nil {
		t.Fatalf("start synthetic app: %v", err)
	}
	return harness, listenEvents
}

func issue150RestartHermeticAppFromSameHome(t *testing.T, home string, cert tls.Certificate) *issue150HermeticApp {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	early := suture.New("issue150-same-home-restart", svcutil.SpecWithDebugLogger())
	earlyDone := early.ServeBackground(ctx)
	logger := events.NewLogger()
	early.Add(logger)
	cfg, err := syncthing.LoadConfigAtStartup(filepath.Join(home, "config.xml"), cert, logger, false, true)
	if err != nil {
		cancel()
		t.Fatalf("load same-home config: %v", err)
	}
	early.Add(cfg)
	database, err := syncthing.OpenDatabase(
		filepath.Join(home, "database"),
		24*time.Hour,
		syncthing.WithReceiveSideReadOnlyConfig(cfg),
	)
	if err != nil {
		cancel()
		t.Fatalf("reopen same-home database: %v", err)
	}
	app, err := syncthing.New(cfg, database, logger, cert, syncthing.Options{
		NoUpgrade:           true,
		ReceiveSideReadOnly: true,
	})
	if err != nil {
		database.Close()
		cancel()
		t.Fatalf("create same-home app: %v", err)
	}
	harness := &issue150HermeticApp{app: app, cfg: cfg, db: database, cancel: cancel, earlyDone: earlyDone}
	t.Cleanup(func() { harness.stop(t) })
	if err := app.Start(); err != nil {
		t.Fatalf("start same-home app: %v", err)
	}
	return harness
}

func issue150WaitForTCPListenAddress(t *testing.T, subscription events.Subscription) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var seen []string
	for time.Now().Before(deadline) {
		event, err := subscription.Poll(time.Until(deadline))
		if err != nil {
			t.Fatalf("wait for synthetic TCP listener after events %v: %v", seen, err)
		}
		seen = append(seen, event.Type.String())
		if event.Type != events.ListenAddressesChanged {
			continue
		}
		data, ok := event.Data.(map[string]interface{})
		if !ok {
			continue
		}
		addresses, ok := data["lan"].([]*url.URL)
		if !ok {
			continue
		}
		for _, address := range addresses {
			if address != nil && address.Scheme == "tcp" && address.Port() != "0" {
				return address.String()
			}
		}
	}
	t.Fatal("synthetic TCP listener did not publish a usable address")
	return ""
}

func issue150WaitForPendingOffer(t *testing.T, app *syncthing.App, folderID string, providerID protocol.DeviceID) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		pending, err := app.Internals.PendingFolders(protocol.EmptyDeviceID)
		if err != nil {
			t.Fatalf("read synthetic pending folders: %v", err)
		}
		if folder, ok := pending[folderID]; ok {
			if _, offered := folder.OfferedBy[providerID]; !offered {
				t.Fatal("pending folder was not offered by the synthetic provider")
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("synthetic provider did not produce a genuine pending-folder offer")
}

func issue150WaitForNeed(t *testing.T, app *syncthing.App, folderID string, files int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		need, err := app.Internals.NeedSize(folderID, protocol.LocalDeviceID)
		if err == nil && need.Files == files {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	need, err := app.Internals.NeedSize(folderID, protocol.LocalDeviceID)
	t.Fatalf("need did not stabilize: got %+v error=%v want files=%d", need, err, files)
}

func issue150FolderHasDevice(folder config.FolderConfiguration, id protocol.DeviceID) bool {
	for _, device := range folder.Devices {
		if device.DeviceID == id {
			return true
		}
	}
	return false
}

func issue150ConfigurationFolderID(value int) string {
	if value < 0 {
		return "issue150-max-minus-one"
	}
	return fmt.Sprintf("issue150-max-%d", value)
}

func issue150ConfigureBridgeFolder(t *testing.T, folderID, folderPath string, folderType config.FolderType, watcherEnabled bool) {
	t.Helper()
	if folderType != config.FolderTypeSendOnly {
		t.Fatalf("live #150 fixture must be send-only, got %s", folderType)
	}
	folder := stCfg.RawCopy().Defaults.Folder.Copy()
	folder.ID = folderID
	folder.Label = "Issue 150 synthetic"
	folder.Path = folderPath
	folder.Type = folderType
	folder.RescanIntervalS = defaultRescanIntervalS
	folder.FSWatcherEnabled = watcherEnabled
	folder.MaxConflicts = 10
	folder.Devices = []config.FolderDeviceConfiguration{{DeviceID: stMyID}}
	waiter, err := stCfg.Modify(func(cfg *config.Configuration) {
		cfg.SetFolder(folder)
	})
	if err != nil {
		t.Fatalf("configure synthetic folder: %v", err)
	}
	waiter.Wait()
}

func issue150SeedBridgeFolderBeforeStart(t *testing.T, configDir, folderID, folderPath string, folderType config.FolderType) {
	t.Helper()
	certificate, err := tlsutil.NewCertificate(
		filepath.Join(configDir, "cert.pem"),
		filepath.Join(configDir, "key.pem"),
		"syncthing",
		1,
		false,
	)
	if err != nil {
		t.Fatalf("create synthetic bridge identity: %v", err)
	}
	localID := protocol.NewDeviceID(certificate.Certificate[0])
	raw := issue150HermeticConfig(localID)
	folder := raw.Defaults.Folder.Copy()
	folder.ID = folderID
	folder.Label = "Issue 150 synthetic"
	folder.Path = folderPath
	folder.Type = folderType
	folder.RescanIntervalS = defaultRescanIntervalS
	folder.FSWatcherEnabled = false
	folder.MaxConflicts = 10
	folder.Devices = []config.FolderDeviceConfiguration{{DeviceID: localID}}
	raw.SetFolder(folder)
	if err := os.MkdirAll(folderPath, 0o700); err != nil {
		t.Fatalf("create synthetic folder root: %v", err)
	}
	wrapper := config.Wrap(filepath.Join(configDir, "config.xml"), raw, localID, events.NoopLogger)
	if err := wrapper.Save(); err != nil {
		t.Fatalf("save synthetic bridge config: %v", err)
	}
}

func issue150AssertConfiguredMaxConflicts(t *testing.T, folderID string, want int) {
	t.Helper()
	folder, exists := stCfg.Folders()[folderID]
	if !exists {
		t.Fatalf("folder %q missing from live config", folderID)
	}
	if folder.MaxConflicts != want {
		t.Fatalf("folder %q MaxConflicts=%d want %d", folderID, folder.MaxConflicts, want)
	}
}
