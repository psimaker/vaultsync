package bridge

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/locations"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/syncthing"
	"github.com/syncthing/syncthing/lib/tlsutil"
	_ "modernc.org/sqlite"
)

func TestIssue150ReceiveReadOnlyBridgeOpenDatabaseRejectsPendingMigrationWithPathFreeSafetyStop(t *testing.T) {
	if IsRunning() {
		t.Fatal("bridge engine was already running")
	}
	t.Cleanup(func() {
		if IsRunning() {
			StopSyncthing()
		}
	})

	configDir := testConfigDir(t)

	certificate, err := tlsutil.NewCertificate(
		locations.Get(locations.CertFile),
		locations.Get(locations.KeyFile),
		"syncthing",
		1,
		false,
	)
	if err != nil {
		t.Fatalf("create synthetic identity: %v", err)
	}
	localID := protocol.NewDeviceID(certificate.Certificate[0])

	raw := issue150HermeticConfig(localID)
	folder := raw.Defaults.Folder.Copy()
	folder.ID = "issue150-protected-pending-migration"
	folder.Label = "Issue 150 protected migration fixture"
	folder.Path = filepath.Join(configDir, "vault")
	folder.Type = config.FolderTypeReceiveOnly
	folder.Paused = true
	folder.RescanIntervalS = defaultRescanIntervalS
	folder.FSWatcherEnabled = false
	folder.Devices = []config.FolderDeviceConfiguration{{DeviceID: localID}}
	raw.SetFolder(folder)
	if err := os.MkdirAll(folder.Path, 0o700); err != nil {
		t.Fatalf("create synthetic vault: %v", err)
	}
	wrapper := config.Wrap(locations.Get(locations.ConfigFile), raw, localID, events.NoopLogger)
	if err := wrapper.Save(); err != nil {
		t.Fatalf("save synthetic config: %v", err)
	}

	database, err := syncthing.OpenDatabase(locations.Get(locations.Database), 24*time.Hour)
	if err != nil {
		t.Fatalf("seed synthetic database: %v", err)
	}
	if err := database.Update(folder.ID, protocol.LocalDeviceID, []protocol.FileInfo{{
		Name:     "conflict-shaped-note.md",
		Sequence: 1,
	}}); err != nil {
		database.Close()
		t.Fatalf("seed protected local row: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close seeded database: %v", err)
	}

	folderDatabases, err := filepath.Glob(filepath.Join(locations.Get(locations.Database), "folder.*.db"))
	if err != nil {
		t.Fatalf("locate protected folder database: %v", err)
	}
	if len(folderDatabases) != 1 {
		t.Fatalf("protected folder database count = %d, want 1", len(folderDatabases))
	}
	folderDatabase := folderDatabases[0]
	rawDatabase, err := sql.Open("sqlite", folderDatabase)
	if err != nil {
		t.Fatalf("open migration fixture: %v", err)
	}
	if _, err := rawDatabase.Exec(`
		DELETE FROM schemamigrations;
		INSERT INTO schemamigrations (schema_version, applied_at, syncthing_version)
		VALUES (4, 1, 'issue150-fixture');
	`); err != nil {
		rawDatabase.Close()
		t.Fatalf("mark migration fixture pending: %v", err)
	}
	if err := rawDatabase.Close(); err != nil {
		t.Fatalf("close migration fixture: %v", err)
	}

	before := issue150DatabaseTreeSnapshot(t, locations.Get(locations.Database))
	if got := StartSyncthing(configDir); got != conflictRetentionSafetyMarker {
		t.Fatalf("StartSyncthing() = %q, want exact path-free safety stop", got)
	}
	after := issue150DatabaseTreeSnapshot(t, locations.Get(locations.Database))
	if !reflect.DeepEqual(after, before) {
		for name, beforeEntry := range before {
			afterEntry, exists := after[name]
			if !exists || beforeEntry.mode != afterEntry.mode || !bytes.Equal(afterEntry.data, beforeEntry.data) {
				t.Logf("database entry %q changed: before=%#o/%d after=%#o/%d exists=%t", name, beforeEntry.mode, len(beforeEntry.data), afterEntry.mode, len(afterEntry.data), exists)
			}
		}
		for name, afterEntry := range after {
			if _, exists := before[name]; !exists {
				t.Logf("database entry %q was created with mode %#o and %d bytes", name, afterEntry.mode, len(afterEntry.data))
			}
		}
		t.Fatal("blocked database open changed the database tree")
	}
}

func TestIssue150DatabaseSafetyStopPrecedesConfigUpgradeMutation(t *testing.T) {
	if IsRunning() {
		t.Fatal("bridge engine was already running")
	}
	t.Cleanup(func() {
		if IsRunning() {
			StopSyncthing()
		}
	})

	configDir := testConfigDir(t)

	certificate, err := tlsutil.NewCertificate(
		locations.Get(locations.CertFile),
		locations.Get(locations.KeyFile),
		"syncthing",
		1,
		false,
	)
	if err != nil {
		t.Fatalf("create identity: %v", err)
	}
	localID := protocol.NewDeviceID(certificate.Certificate[0])

	raw := issue150HermeticConfig(localID)
	folder := raw.Defaults.Folder.Copy()
	folder.ID = "issue150-config-preflight-order"
	folder.Label = "Issue 150 config preflight order"
	folder.Path = filepath.Join(configDir, "vault")
	folder.Type = config.FolderTypeReceiveOnly
	folder.Paused = true
	folder.FSWatcherEnabled = false
	folder.Devices = []config.FolderDeviceConfiguration{{DeviceID: localID}}
	raw.SetFolder(folder)
	if err := os.MkdirAll(folder.Path, 0o700); err != nil {
		t.Fatalf("create vault: %v", err)
	}
	wrapper := config.Wrap(locations.Get(locations.ConfigFile), raw, localID, events.NoopLogger)
	if err := wrapper.Save(); err != nil {
		t.Fatalf("save config: %v", err)
	}

	configPath := locations.Get(locations.ConfigFile)
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	currentVersion := []byte(fmt.Sprintf(`version="%d"`, config.CurrentVersion))
	previousVersion := config.CurrentVersion - 1
	if got := bytes.Count(configBytes, currentVersion); got != 1 {
		t.Fatalf("current config version occurrences = %d, want 1", got)
	}
	configBytes = bytes.Replace(
		configBytes,
		currentVersion,
		[]byte(fmt.Sprintf(`version="%d"`, previousVersion)),
		1,
	)
	if err := os.WriteFile(configPath, configBytes, 0o600); err != nil {
		t.Fatalf("write previous-version config: %v", err)
	}

	database, err := syncthing.OpenDatabase(locations.Get(locations.Database), 24*time.Hour)
	if err != nil {
		t.Fatalf("seed database: %v", err)
	}
	if err := database.Update(folder.ID, protocol.LocalDeviceID, []protocol.FileInfo{{
		Name:     "recognizable-note.md",
		Sequence: 1,
	}}); err != nil {
		database.Close()
		t.Fatalf("seed local row: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close seeded database: %v", err)
	}

	folderDatabases, err := filepath.Glob(filepath.Join(locations.Get(locations.Database), "folder.*.db"))
	if err != nil {
		t.Fatalf("locate folder database: %v", err)
	}
	if len(folderDatabases) != 1 {
		t.Fatalf("folder database count = %d, want 1", len(folderDatabases))
	}
	rawDatabase, err := sql.Open("sqlite", folderDatabases[0])
	if err != nil {
		t.Fatalf("open database fixture: %v", err)
	}
	if _, err := rawDatabase.Exec(`
		DELETE FROM schemamigrations;
		INSERT INTO schemamigrations (schema_version, applied_at, syncthing_version)
		VALUES (4, 1, 'issue150-fixture');
	`); err != nil {
		rawDatabase.Close()
		t.Fatalf("mark migration pending: %v", err)
	}
	if err := rawDatabase.Close(); err != nil {
		t.Fatalf("close database fixture: %v", err)
	}

	archivePath := configPath + fmt.Sprintf(".v%d", previousVersion)
	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Fatalf("config archive exists before startup: %v", err)
	}
	databaseBefore := issue150DatabaseTreeSnapshot(t, locations.Get(locations.Database))
	if got := StartSyncthing(configDir); got != conflictRetentionSafetyMarker {
		t.Fatalf("StartSyncthing() = %q, want exact path-free safety stop", got)
	}
	configAfter, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config after safety stop: %v", err)
	}
	if !bytes.Equal(configAfter, configBytes) {
		t.Fatal("database safety stop rewrote the previous-version config")
	}
	if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
		t.Fatalf("database safety stop created a config archive: %v", err)
	}
	databaseAfter := issue150DatabaseTreeSnapshot(t, locations.Get(locations.Database))
	if !reflect.DeepEqual(databaseAfter, databaseBefore) {
		t.Fatal("database safety stop changed the database tree")
	}
}

type issue150DatabaseTreeEntry struct {
	mode os.FileMode
	data []byte
}

func issue150DatabaseTreeSnapshot(t *testing.T, databasePath string) map[string]issue150DatabaseTreeEntry {
	t.Helper()
	snapshot := make(map[string]issue150DatabaseTreeEntry)
	err := filepath.Walk(databasePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == databasePath {
			return nil
		}
		relative, err := filepath.Rel(databasePath, path)
		if err != nil {
			return err
		}
		entry := issue150DatabaseTreeEntry{mode: info.Mode()}
		if info.Mode().IsRegular() {
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry.data = bytes.Clone(contents)
		}
		snapshot[relative] = entry
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot database tree: %v", err)
	}
	return snapshot
}
