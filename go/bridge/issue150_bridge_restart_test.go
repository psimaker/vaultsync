package bridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/syncthing"
)

func TestIssue150PublicBridgeSameHomeRestartPreservesIdentityCompleteConfigNeedAndVault(t *testing.T) {
	StopSyncthing()
	previousDefaultListenAddresses := append([]string(nil), config.DefaultListenAddresses...)
	config.DefaultListenAddresses = []string{"tcp://127.0.0.1:0"}
	t.Cleanup(func() { config.DefaultListenAddresses = previousDefaultListenAddresses })

	configDir := testConfigDir(t)
	if got := StartSyncthing(configDir); got != "" {
		t.Fatalf("bootstrap StartSyncthing: %s", got)
	}
	localID, err := protocol.DeviceIDFromString(DeviceID())
	if err != nil {
		t.Fatalf("parse bootstrap identity: %v", err)
	}
	if err := stCfg.Save(); err != nil {
		t.Fatalf("save bootstrap config: %v", err)
	}
	StopSyncthing()

	const folderID = "issue150-public-bridge-restart"
	providerCert := issue150TestCertificate(t)
	providerID := protocol.NewDeviceID(providerCert.Certificate[0])
	providerHome := t.TempDir()
	providerVault := filepath.Join(providerHome, "provider-vault")
	if err := os.MkdirAll(providerVault, 0o700); err != nil {
		t.Fatalf("create provider vault: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(providerVault, "remote-note.sync-conflict-20260829-120000.md"),
		[]byte("remote issue 150 bytes\n"),
		0o600,
	); err != nil {
		t.Fatalf("write provider file: %v", err)
	}
	providerRaw := issue150HermeticConfig(providerID)
	providerRaw.Options.RawListenAddresses = []string{issue150AvailableTCPListenAddress(t)}
	providerRaw.SetDevice(config.DeviceConfiguration{
		DeviceID:  localID,
		Name:      "Issue 150 bridge peer",
		Addresses: []string{"dynamic"},
	})
	providerFolder := providerRaw.Defaults.Folder.Copy()
	providerFolder.ID = folderID
	providerFolder.Label = "Issue 150 provider folder"
	providerFolder.Path = providerVault
	providerFolder.Type = config.FolderTypeSendOnly
	providerFolder.RescanIntervalS = 60
	providerFolder.FSWatcherEnabled = false
	providerFolder.IgnorePerms = true
	providerFolder.MaxConflicts = 17
	providerFolder.Devices = []config.FolderDeviceConfiguration{
		{DeviceID: providerID},
		{DeviceID: localID},
	}
	providerRaw.SetFolder(providerFolder)
	provider, providerEvents := issue150StartHermeticApp(t, providerHome, providerRaw, providerCert, false)
	providerAddress := issue150WaitForTCPListenAddress(t, providerEvents)

	localVault := t.TempDir()
	if err := os.MkdirAll(filepath.Join(localVault, "notes"), 0o700); err != nil {
		t.Fatalf("create local notes directory: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(localVault, ".stversions", "archive"), 0o700); err != nil {
		t.Fatalf("create local versions directory: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(localVault, "notes", "local-note.sync-conflict-20260829-120001.md"),
		[]byte("local issue 150 bytes\n"),
		0o600,
	); err != nil {
		t.Fatalf("write local conflict-shaped file: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(localVault, ".stversions", "archive", "retained.sync-conflict-20260829-120002.md"),
		[]byte("retained issue 150 version bytes\n"),
		0o640,
	); err != nil {
		t.Fatalf("write local retained version: %v", err)
	}
	vaultBefore := issue150SnapshotBridgeVault(t, localVault)

	issue150WriteStoppedBridgeConfiguration(t, configDir, localID, func(cfg *config.Configuration) {
		// Keep the public bridge listener TCP-only. StartSyncthing still owns
		// the normal embedded-service options; this avoids unrelated QUIC/STUN
		// activity in the regression fixture.
		cfg.Options.RawListenAddresses = []string{"tcp://127.0.0.1:0"}

		providerDevice := cfg.Defaults.Device.Copy()
		providerDevice.DeviceID = providerID
		providerDevice.Name = "Issue 150 configured provider"
		// A single static address is user-managed, so the existing address-cache
		// policy must leave it byte-for-byte unchanged.
		providerDevice.Addresses = []string{providerAddress}
		providerDevice.MaxRequestKiB = 512
		providerDevice.RawNumConnections = 1
		cfg.SetDevice(providerDevice)

		folder := cfg.Defaults.Folder.Copy()
		folder.ID = folderID
		folder.Label = "Issue 150 complete receive configuration"
		folder.Path = localVault
		folder.Type = config.FolderTypeReceiveOnly
		folder.RescanIntervalS = 60
		folder.FSWatcherEnabled = false
		folder.FSWatcherDelayS = 13
		folder.FSWatcherTimeoutS = 29
		folder.IgnorePerms = true
		folder.AutoNormalize = false
		folder.IgnoreDelete = true
		folder.MaxConflicts = 23
		folder.DisableSparseFiles = true
		folder.RawModTimeWindowS = 2
		folder.MaxConcurrentWrites = 2
		folder.DisableFsync = true
		folder.Devices = []config.FolderDeviceConfiguration{
			{DeviceID: localID},
			{DeviceID: providerID},
		}
		cfg.SetFolder(folder)
	})

	if got := StartSyncthing(configDir); got != "" {
		t.Fatalf("first receive StartSyncthing: %s", got)
	}
	t.Cleanup(StopSyncthing)
	firstIdentity := DeviceID()
	firstCert := issue150ReadBridgeFile(t, filepath.Join(configDir, "cert.pem"))
	firstKey := issue150ReadBridgeFile(t, filepath.Join(configDir, "key.pem"))
	issue150WaitForBridgeNeed(t, folderID, 1)
	firstNeed := issue150BridgeNeed(t, folderID)
	if firstNeed.Files != 1 || firstNeed.Directories != 0 || firstNeed.Symlinks != 0 || firstNeed.Deleted != 0 {
		t.Fatalf("first lifecycle need = %+v, want exactly one remote file", firstNeed)
	}
	if localSize, err := stApp.Internals.LocalSize(folderID); err != nil || localSize.TotalItems() != 0 {
		t.Fatalf("first lifecycle local index = %+v, error=%v, want empty", localSize, err)
	}
	issue150AssertBridgeSafetyStatus(t, folderID)
	if got := issue150SnapshotBridgeVault(t, localVault); !reflect.DeepEqual(got, vaultBefore) {
		t.Fatalf("first public lifecycle mutated the receive vault:\nbefore=%+v\nafter=%+v", vaultBefore, got)
	}

	firstConfig := stCfg.RawCopy()
	if err := stCfg.Save(); err != nil {
		t.Fatalf("save first lifecycle config: %v", err)
	}
	provider.stop(t)
	StopSyncthing()
	firstConfigBytes := issue150ReadBridgeFile(t, filepath.Join(configDir, "config.xml"))

	// The provider stays stopped. Any Need visible after this public close/open
	// cycle therefore came from the persisted remote index, not a replayed
	// network message.
	if got := StartSyncthing(configDir); got != "" {
		t.Fatalf("same-home StartSyncthing: %s", got)
	}
	if got := DeviceID(); got != firstIdentity {
		t.Fatalf("same-home identity changed: got %s want %s", got, firstIdentity)
	}
	if got := issue150ReadBridgeFile(t, filepath.Join(configDir, "cert.pem")); !bytes.Equal(got, firstCert) {
		t.Fatal("same-home restart replaced the persisted certificate")
	}
	if got := issue150ReadBridgeFile(t, filepath.Join(configDir, "key.pem")); !bytes.Equal(got, firstKey) {
		t.Fatal("same-home restart replaced the persisted private key")
	}
	issue150WaitForBridgeNeed(t, folderID, 1)
	secondNeed := issue150BridgeNeed(t, folderID)
	if secondNeed != firstNeed {
		t.Fatalf("persisted Need changed across provider-offline restart:\nbefore=%+v\nafter=%+v", firstNeed, secondNeed)
	}
	if localSize, err := stApp.Internals.LocalSize(folderID); err != nil || localSize.TotalItems() != 0 {
		t.Fatalf("restarted lifecycle local index = %+v, error=%v, want empty", localSize, err)
	}

	secondConfig := stCfg.RawCopy()
	if !reflect.DeepEqual(secondConfig.Folders, firstConfig.Folders) {
		t.Fatalf("complete folder configuration changed across public restart:\nbefore=%+v\nafter=%+v", firstConfig.Folders, secondConfig.Folders)
	}
	if !reflect.DeepEqual(secondConfig.Devices, firstConfig.Devices) {
		t.Fatalf("complete device configuration changed across public restart:\nbefore=%+v\nafter=%+v", firstConfig.Devices, secondConfig.Devices)
	}
	if err := stCfg.Save(); err != nil {
		t.Fatalf("save restarted lifecycle config: %v", err)
	}
	secondConfigBytes := issue150ReadBridgeFile(t, filepath.Join(configDir, "config.xml"))
	if !bytes.Equal(secondConfigBytes, firstConfigBytes) {
		t.Fatalf("config.xml bytes changed across provider-offline public restart:\nbefore-sha256=%x\nafter-sha256=%x", sha256.Sum256(firstConfigBytes), sha256.Sum256(secondConfigBytes))
	}
	issue150AssertBridgeSafetyStatus(t, folderID)
	if got := issue150SnapshotBridgeVault(t, localVault); !reflect.DeepEqual(got, vaultBefore) {
		t.Fatalf("same-home public restart mutated the receive vault:\nbefore=%+v\nafter=%+v", vaultBefore, got)
	}
	if _, err := os.Lstat(filepath.Join(localVault, config.DefaultMarkerName)); !os.IsNotExist(err) {
		t.Fatalf("public bridge lifecycle created a receive marker: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(localVault, "remote-note.sync-conflict-20260829-120000.md")); !os.IsNotExist(err) {
		t.Fatalf("public bridge lifecycle materialized the remote file: %v", err)
	}
}

type issue150BridgeVaultEntry struct {
	Mode       os.FileMode
	Size       int64
	ModTimeNS  int64
	ContentSHA [sha256.Size]byte
}

func issue150SnapshotBridgeVault(t *testing.T, root string) map[string]issue150BridgeVaultEntry {
	t.Helper()
	result := make(map[string]issue150BridgeVaultEntry)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		snapshot := issue150BridgeVaultEntry{
			Mode:      info.Mode(),
			Size:      info.Size(),
			ModTimeNS: info.ModTime().UnixNano(),
		}
		if info.Mode().IsRegular() {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			snapshot.ContentSHA = sha256.Sum256(content)
		}
		result[relative] = snapshot
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot receive vault: %v", err)
	}
	return result
}

func issue150WriteStoppedBridgeConfiguration(t *testing.T, configDir string, localID protocol.DeviceID, modify func(*config.Configuration)) {
	t.Helper()
	path := filepath.Join(configDir, "config.xml")
	input, err := os.Open(path)
	if err != nil {
		t.Fatalf("open stopped bridge config: %v", err)
	}
	cfg, _, err := config.ReadXML(input, localID)
	closeErr := input.Close()
	if err != nil {
		t.Fatalf("decode stopped bridge config: %v", err)
	}
	if closeErr != nil {
		t.Fatalf("close stopped bridge config: %v", closeErr)
	}
	modify(&cfg)
	var encoded bytes.Buffer
	if err := cfg.WriteXML(&encoded); err != nil {
		t.Fatalf("encode stopped bridge config: %v", err)
	}
	if err := os.WriteFile(path, encoded.Bytes(), 0o600); err != nil {
		t.Fatalf("write stopped bridge config: %v", err)
	}
}

func issue150ReadBridgeFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read bridge lifecycle file: %v", err)
	}
	return content
}

func issue150BridgeNeed(t *testing.T, folderID string) syncthing.Counts {
	t.Helper()
	mu.Lock()
	app := stApp
	running := stRunning
	mu.Unlock()
	if !running || app == nil {
		t.Fatal("public bridge is not running while reading Need")
	}
	need, err := app.Internals.NeedSize(folderID, protocol.LocalDeviceID)
	if err != nil {
		t.Fatalf("read public bridge Need: %v", err)
	}
	return need
}

func issue150WaitForBridgeNeed(t *testing.T, folderID string, files int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		need := issue150BridgeNeed(t, folderID)
		if need.Files == files {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("public bridge Need did not stabilize at %d files: %+v", files, issue150BridgeNeed(t, folderID))
}

func issue150AssertBridgeSafetyStatus(t *testing.T, folderID string) {
	t.Helper()
	if got := RescanFolder(folderID); got != conflictRetentionSafetyMarker {
		t.Fatalf("receive rescan = %q, want fixed safety stop", got)
	}
	var status FolderStatus
	if err := json.Unmarshal([]byte(GetFolderStatusJSON(folderID)), &status); err != nil {
		t.Fatalf("decode receive safety status: %v", err)
	}
	if status.State != "error" || status.ErrorReason != conflictRetentionSafetyErrorReason || status.ErrorPath != "" {
		t.Fatalf("receive safety status is not stable and path-free: %+v", status)
	}
}
