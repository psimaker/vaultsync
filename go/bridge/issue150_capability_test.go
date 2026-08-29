package bridge

import (
	"crypto/tls"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/locations"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/tlsutil"
)

func TestIssue150ProtectedGenericConfigurationDiffsStopBeforeLiveMutation(t *testing.T) {
	configDir := testConfigDir(t)
	const folderID = "issue150-capability-generic"
	folderPath := filepath.Join(configDir, "protected-vault")
	peerID := issue150SeedCapabilityFolderBeforeStart(t, configDir, folderID, folderPath, config.FolderTypeReceiveOnly, true)
	if got := StartSyncthing(configDir); got != "" {
		t.Fatalf("start protected bridge fixture: %s", got)
	}
	t.Cleanup(StopSyncthing)

	beforeConfig := stCfg.RawCopy()
	beforeVault := issue150SnapshotBridgeVault(t, folderPath)

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "path",
			call: func() error {
				return issue150BridgeGenericModify(func(cfg *config.Configuration) {
					for i := range cfg.Folders {
						if cfg.Folders[i].ID == folderID {
							cfg.Folders[i].Path = filepath.Join(configDir, "other-vault")
						}
					}
				})
			},
		},
		{
			name: "device extension",
			call: func() error {
				return issue150BridgeGenericModify(func(cfg *config.Configuration) {
					for i := range cfg.Folders {
						if cfg.Folders[i].ID == folderID {
							cfg.Folders[i].Devices = append(cfg.Folders[i].Devices, config.FolderDeviceConfiguration{DeviceID: peerID})
						}
					}
				})
			},
		},
		{
			name: "remove",
			call: func() error {
				waiter, err := stCfg.RemoveFolder(folderID)
				waiter.Wait()
				return err
			},
		},
		{
			name: "derived membership through device removal",
			call: func() error {
				waiter, err := stCfg.RemoveDevice(peerID)
				waiter.Wait()
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			issue150ExpectCanonicalConfigurationSafetyStop(t, test.call())
			if after := stCfg.RawCopy(); !reflect.DeepEqual(after, beforeConfig) {
				t.Fatalf("generic protected diff changed live config:\nbefore=%+v\nafter=%+v", beforeConfig, after)
			}
			if after := issue150SnapshotBridgeVault(t, folderPath); !reflect.DeepEqual(after, beforeVault) {
				t.Fatalf("generic protected diff changed vault:\nbefore=%+v\nafter=%+v", beforeVault, after)
			}
		})
	}

	t.Run("bridge device removal cannot derive a protected membership diff", func(t *testing.T) {
		if got := RemoveDevice(peerID.String()); got != conflictRetentionSafetyMarker {
			t.Fatalf("RemoveDevice() = %q, want exact path-free safety stop", got)
		}
		if after := stCfg.RawCopy(); !reflect.DeepEqual(after, beforeConfig) {
			t.Fatalf("blocked bridge device removal changed live config:\nbefore=%+v\nafter=%+v", beforeConfig, after)
		}
		if after := issue150SnapshotBridgeVault(t, folderPath); !reflect.DeepEqual(after, beforeVault) {
			t.Fatalf("blocked bridge device removal changed vault:\nbefore=%+v\nafter=%+v", beforeVault, after)
		}
	})
}

func TestIssue150ProtectedBridgeCapabilitiesApplyOnlyExpectedOperationDiffs(t *testing.T) {
	configDir := testConfigDir(t)
	const folderID = "issue150-capability-operations"
	folderPath := filepath.Join(configDir, "protected-vault")
	peerID := issue150SeedCapabilityFolderBeforeStart(t, configDir, folderID, folderPath, config.FolderTypeReceiveOnly, false)
	if got := StartSyncthing(configDir); got != "" {
		t.Fatalf("start protected bridge fixture: %s", got)
	}
	t.Cleanup(StopSyncthing)

	before := stCfg.RawCopy()
	if got := SetFolderPaused(folderID, true); got != "" {
		t.Fatalf("pause protected folder: %s", got)
	}
	issue150ExpectOnlyPauseChanged(t, before, stCfg.RawCopy(), folderID, true)

	before = stCfg.RawCopy()
	if got := SetFolderPaused(folderID, false); got != "" {
		t.Fatalf("resume protected folder: %s", got)
	}
	issue150ExpectOnlyPauseChanged(t, before, stCfg.RawCopy(), folderID, false)

	before = stCfg.RawCopy()
	if got := ShareFolderWithDevice(folderID, peerID.String()); got != "" {
		t.Fatalf("share protected folder: %s", got)
	}
	issue150ExpectOnlyMembershipChanged(t, before, stCfg.RawCopy(), folderID, peerID, true)

	before = stCfg.RawCopy()
	if got := UnshareFolderFromDevice(folderID, peerID.String()); got != "" {
		t.Fatalf("unshare protected folder: %s", got)
	}
	issue150ExpectOnlyMembershipChanged(t, before, stCfg.RawCopy(), folderID, peerID, false)
}

func TestIssue150ProtectedShareCapabilityRejectsPrepareDerivedAdditionalDiffAtBridge(t *testing.T) {
	configDir := testConfigDir(t)
	const folderID = "issue150-capability-derived-diff"
	folderPath := filepath.Join(configDir, "protected-vault")
	peerID := issue150SeedCapabilityFolderBeforeStart(
		t,
		configDir,
		folderID,
		folderPath,
		config.FolderTypeReceiveOnly,
		false,
	)
	certificate, err := tls.LoadX509KeyPair(
		filepath.Join(configDir, "cert.pem"),
		filepath.Join(configDir, "key.pem"),
	)
	if err != nil {
		t.Fatalf("load #150 bridge identity: %v", err)
	}
	localID := protocol.NewDeviceID(certificate.Certificate[0])
	issue150WriteStoppedBridgeConfiguration(t, configDir, localID, func(cfg *config.Configuration) {
		_, index, ok := cfg.Device(peerID)
		if !ok {
			t.Fatal("#150 fixture peer is missing")
		}
		cfg.Devices[index].IgnoredFolders = []config.ObservedFolder{{
			ID:    folderID,
			Label: "Issue 150 ignored offer fixture",
		}}
	})

	if got := StartSyncthing(configDir); got != "" {
		t.Fatalf("start protected bridge fixture: %s", got)
	}
	t.Cleanup(StopSyncthing)
	before := stCfg.RawCopy()
	beforeVault := issue150SnapshotBridgeVault(t, folderPath)

	if got := ShareFolderWithDevice(folderID, peerID.String()); got != conflictRetentionSafetyMarker {
		t.Fatalf("share with prepare-derived diff = %q, want exact path-free safety stop", got)
	}
	if after := stCfg.RawCopy(); !reflect.DeepEqual(after, before) {
		t.Fatalf("blocked share changed live config:\nbefore=%+v\nafter=%+v", before, after)
	}
	if after := issue150SnapshotBridgeVault(t, folderPath); !reflect.DeepEqual(after, beforeVault) {
		t.Fatalf("blocked share changed vault:\nbefore=%+v\nafter=%+v", beforeVault, after)
	}
}

func TestIssue150ProtectedRemoveFolderCapabilityRetainsExplicitRemovalSemantics(t *testing.T) {
	configDir := testConfigDir(t)
	const folderID = "issue150-capability-remove"
	folderPath := filepath.Join(configDir, "protected-vault")
	issue150SeedCapabilityFolderBeforeStart(t, configDir, folderID, folderPath, config.FolderTypeReceiveOnly, false)
	writeFolderMarker(t, folderPath, folderID)
	if got := StartSyncthing(configDir); got != "" {
		t.Fatalf("start protected bridge fixture: %s", got)
	}
	t.Cleanup(StopSyncthing)

	if got := RemoveFolder(folderID); got != "" {
		t.Fatalf("remove protected folder: %s", got)
	}
	if _, exists := stCfg.Folders()[folderID]; exists {
		t.Fatal("explicit protected RemoveFolder left the folder configured")
	}
	if _, err := os.Lstat(filepath.Join(folderPath, config.DefaultMarkerName)); !os.IsNotExist(err) {
		t.Fatalf("explicit protected RemoveFolder did not retain marker-removal semantics: %v", err)
	}
}

func TestIssue150ProtectedLegacyRescanValueSurvivesStartupWithoutRewrite(t *testing.T) {
	configDir := testConfigDir(t)
	const folderID = "issue150-protected-rescan-migration"
	folderPath := filepath.Join(configDir, "protected-vault")
	issue150SeedCapabilityFolderBeforeStart(t, configDir, folderID, folderPath, config.FolderTypeReceiveOnly, false)
	certificate, err := tls.LoadX509KeyPair(
		filepath.Join(configDir, "cert.pem"),
		filepath.Join(configDir, "key.pem"),
	)
	if err != nil {
		t.Fatalf("load #150 bridge identity: %v", err)
	}
	localID := protocol.NewDeviceID(certificate.Certificate[0])
	issue150WriteStoppedBridgeConfiguration(t, configDir, localID, func(cfg *config.Configuration) {
		for i := range cfg.Folders {
			if cfg.Folders[i].ID == folderID {
				cfg.Folders[i].RescanIntervalS = 3600
			}
		}
	})

	if got := StartSyncthing(configDir); got != "" {
		t.Fatalf("start protected bridge fixture: %s", got)
	}
	t.Cleanup(StopSyncthing)
	if got := stCfg.Folders()[folderID].RescanIntervalS; got != 3600 {
		t.Fatalf("protected startup rewrote RescanIntervalS=%d, want preserved 3600", got)
	}
}

func TestIssue150SendOnlyBridgeOperationsRetainExistingSemantics(t *testing.T) {
	configDir := testConfigDir(t)
	if got := StartSyncthing(configDir); got != "" {
		t.Fatalf("start SendOnly bridge fixture: %s", got)
	}
	t.Cleanup(StopSyncthing)

	peerCertificate := issue150TestCertificate(t)
	peerID := protocol.NewDeviceID(peerCertificate.Certificate[0])
	if got := AddDevice(peerID.String(), "Issue 150 peer"); got != "" {
		t.Fatalf("add SendOnly peer: %s", got)
	}

	const folderID = "issue150-sendonly-capability-control"
	folderPath := filepath.Join(configDir, "sendonly-vault")
	if got := addFolderForTesting(folderID, "Issue 150 SendOnly", folderPath); got != "" {
		t.Fatalf("add SendOnly folder: %s", got)
	}
	for _, paused := range []bool{true, false} {
		if got := SetFolderPaused(folderID, paused); got != "" {
			t.Fatalf("set SendOnly paused=%t: %s", paused, got)
		}
	}
	if got := ShareFolderWithDevice(folderID, peerID.String()); got != "" {
		t.Fatalf("share SendOnly folder: %s", got)
	}
	if got := UnshareFolderFromDevice(folderID, peerID.String()); got != "" {
		t.Fatalf("unshare SendOnly folder: %s", got)
	}
	if got := RemoveFolder(folderID); got != "" {
		t.Fatalf("remove SendOnly folder: %s", got)
	}
}

func TestIssue150EmbeddedDatabaseLayoutStaysInsideConfiguredPrivateHome(t *testing.T) {
	configDir := testConfigDir(t)
	if got := StartSyncthing(configDir); got != "" {
		t.Fatalf("start bridge layout fixture: %s", got)
	}
	t.Cleanup(StopSyncthing)

	wantDatabasePath := filepath.Join(configDir, "data", "index-v2")
	if got := filepath.Clean(locations.Get(locations.Database)); got != filepath.Clean(wantDatabasePath) {
		t.Fatalf("database path = %q, want configured private home %q", got, wantDatabasePath)
	}
	if got := filepath.Clean(locations.Get(locations.ConfigFile)); got != filepath.Join(filepath.Clean(configDir), "config.xml") {
		t.Fatalf("config path = %q, want configured private home", got)
	}

	vaultPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultPath, "ownership-probe.md"), []byte("ownership probe\n"), 0o600); err != nil {
		t.Fatalf("write separate vault fixture: %v", err)
	}
	const folderID = "issue150-layout-sendonly"
	if got := addFolderForTesting(folderID, "Issue 150 layout", vaultPath); got != "" {
		t.Fatalf("add separate vault fixture: %s", got)
	}
	if got := RescanFolder(folderID); got != "" {
		t.Fatalf("scan separate vault fixture: %s", got)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		folderDatabases, err := filepath.Glob(filepath.Join(wantDatabasePath, "folder.*.db"))
		if err != nil {
			t.Fatalf("glob folder databases: %v", err)
		}
		if len(folderDatabases) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("folder database was not created inside the configured engine home")
		}
		time.Sleep(10 * time.Millisecond)
	}

	entries, err := os.ReadDir(wantDatabasePath)
	if err != nil {
		t.Fatalf("read configured database directory: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if name != "main.db" && !strings.HasPrefix(name, "folder.") && !strings.HasSuffix(name, "-wal") && !strings.HasSuffix(name, "-shm") {
			continue
		}
		path := filepath.Join(wantDatabasePath, name)
		if relative, err := filepath.Rel(configDir, path); err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatalf("database artifact escaped configured private home: %q", name)
		}
		if relative, err := filepath.Rel(vaultPath, path); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatalf("database artifact entered the security-scoped vault fixture: %q", name)
		}
	}
}

func TestIssue150ConcurrentBridgeStartsMaintainSingleDatabaseOwner(t *testing.T) {
	configDir := testConfigDir(t)
	if got := StartSyncthing(configDir); got != "" {
		t.Fatalf("start bridge owner fixture: %s", got)
	}
	t.Cleanup(StopSyncthing)
	identity := DeviceID()
	generation := EventStreamGeneration()

	const attempts = 8
	results := make(chan string, attempts)
	var group sync.WaitGroup
	for range attempts {
		group.Go(func() {
			results <- StartSyncthing(configDir)
		})
	}
	group.Wait()
	close(results)
	for result := range results {
		if result != "already running" {
			t.Fatalf("concurrent start = %q, want single-owner rejection", result)
		}
	}
	if got := DeviceID(); got != identity {
		t.Fatal("concurrent start changed the active engine identity")
	}
	if got := EventStreamGeneration(); got != generation {
		t.Fatalf("concurrent start changed engine generation from %d to %d", generation, got)
	}
}

func issue150BridgeGenericModify(modify config.ModifyFunction) error {
	waiter, err := stCfg.Modify(modify)
	waiter.Wait()
	return err
}

func issue150ExpectCanonicalConfigurationSafetyStop(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, config.ErrVaultSyncReceiveSideSafetyStop) {
		t.Fatalf("configuration error = %v, want canonical #150 safety code", err)
	}
	if got := err.Error(); got != conflictRetentionSafetyMarker {
		t.Fatalf("configuration error = %q, want exact path-free %q", got, conflictRetentionSafetyMarker)
	}
}

func issue150ExpectOnlyPauseChanged(t *testing.T, before, after config.Configuration, folderID string, paused bool) {
	t.Helper()
	folder, index, exists := after.Folder(folderID)
	if !exists || folder.Paused != paused {
		t.Fatalf("pause operation did not set folder %q to paused=%t", folderID, paused)
	}
	after.Folders[index].Paused = !paused
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("pause capability changed more than the expected field:\nbefore=%+v\nafter-normalized=%+v", before, after)
	}
}

func issue150ExpectOnlyMembershipChanged(t *testing.T, before, after config.Configuration, folderID string, deviceID protocol.DeviceID, shared bool) {
	t.Helper()
	folder, index, exists := after.Folder(folderID)
	if !exists || issue150FolderHasDevice(folder, deviceID) != shared {
		t.Fatalf("membership operation did not set folder %q device shared=%t", folderID, shared)
	}
	if shared {
		filtered := make([]config.FolderDeviceConfiguration, 0, len(folder.Devices)-1)
		for _, device := range folder.Devices {
			if device.DeviceID != deviceID {
				filtered = append(filtered, device)
			}
		}
		after.Folders[index].Devices = filtered
	} else {
		beforeFolder, _, ok := before.Folder(folderID)
		if !ok {
			t.Fatalf("membership fixture folder %q missing before operation", folderID)
		}
		after.Folders[index].Devices = append([]config.FolderDeviceConfiguration(nil), beforeFolder.Devices...)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("membership capability changed more than the expected device:\nbefore=%+v\nafter-normalized=%+v", before, after)
	}
}

func issue150SeedCapabilityFolderBeforeStart(t *testing.T, configDir, folderID, folderPath string, folderType config.FolderType, shared bool) protocol.DeviceID {
	t.Helper()
	certificate, err := tlsutil.NewCertificate(
		filepath.Join(configDir, "cert.pem"),
		filepath.Join(configDir, "key.pem"),
		"syncthing",
		1,
		false,
	)
	if err != nil {
		t.Fatalf("create #150 bridge identity: %v", err)
	}
	localID := protocol.NewDeviceID(certificate.Certificate[0])
	peerID := protocol.NewDeviceID(issue150TestCertificate(t).Certificate[0])
	raw := issue150HermeticConfig(localID)
	raw.SetDevice(config.DeviceConfiguration{DeviceID: peerID, Name: "Issue 150 peer"})
	folder := raw.Defaults.Folder.Copy()
	folder.ID = folderID
	folder.Label = "Issue 150 protected fixture"
	folder.Path = folderPath
	folder.Type = folderType
	folder.RescanIntervalS = defaultRescanIntervalS
	folder.FSWatcherEnabled = false
	folder.Devices = []config.FolderDeviceConfiguration{{DeviceID: localID}}
	if shared {
		folder.Devices = append(folder.Devices, config.FolderDeviceConfiguration{DeviceID: peerID})
	}
	raw.SetFolder(folder)
	if err := os.MkdirAll(folderPath, 0o700); err != nil {
		t.Fatalf("create #150 folder root: %v", err)
	}
	wrapper := config.Wrap(filepath.Join(configDir, "config.xml"), raw, localID, events.NoopLogger)
	if err := wrapper.Save(); err != nil {
		t.Fatalf("save #150 bridge config: %v", err)
	}
	return peerID
}
