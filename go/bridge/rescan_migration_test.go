package bridge

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/syncthing/syncthing/lib/config"
)

func TestAddFolder_DefaultsTo60sRescan(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	folderPath := filepath.Join(configDir, "rescan-default")
	if errMsg := AddFolder("rescan-default", "Rescan Default", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	folder, exists := stCfg.Folders()["rescan-default"]
	if !exists {
		t.Fatalf("folder not found in config after AddFolder")
	}
	if folder.RescanIntervalS != 60 {
		t.Errorf("RescanIntervalS = %d, want 60 (Syncthing's standard default)", folder.RescanIntervalS)
	}
}

func TestStart_MigratesLegacy3600(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("first StartSyncthing() failed: %s", errMsg)
	}

	folderPath := filepath.Join(configDir, "legacy-folder")
	if errMsg := AddFolder("legacy", "Legacy", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	// Simulate a pre-migration on-disk state: an existing folder still carrying
	// the old VaultSync default of 3600 seconds.
	waiter, err := stCfg.Modify(func(cfg *config.Configuration) {
		for i := range cfg.Folders {
			if cfg.Folders[i].ID == "legacy" {
				cfg.Folders[i].RescanIntervalS = 3600
			}
		}
	})
	if err != nil {
		t.Fatalf("setup Modify failed: %v", err)
	}
	waiter.Wait()

	StopSyncthing()
	time.Sleep(100 * time.Millisecond) // allow async config flush

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("second StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	folder, exists := stCfg.Folders()["legacy"]
	if !exists {
		t.Fatalf("folder not found after restart")
	}
	if folder.RescanIntervalS != 60 {
		t.Errorf("RescanIntervalS = %d after migration, want 60", folder.RescanIntervalS)
	}
}

func TestStart_PreservesCustomInterval(t *testing.T) {
	configDir := testConfigDir(t)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("first StartSyncthing() failed: %s", errMsg)
	}

	folderPath := filepath.Join(configDir, "custom-folder")
	if errMsg := AddFolder("custom", "Custom", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	// User-customised interval — neither the legacy 3600 nor the new default 60.
	waiter, err := stCfg.Modify(func(cfg *config.Configuration) {
		for i := range cfg.Folders {
			if cfg.Folders[i].ID == "custom" {
				cfg.Folders[i].RescanIntervalS = 120
			}
		}
	})
	if err != nil {
		t.Fatalf("setup Modify failed: %v", err)
	}
	waiter.Wait()

	StopSyncthing()
	time.Sleep(100 * time.Millisecond)

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("second StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	folder, exists := stCfg.Folders()["custom"]
	if !exists {
		t.Fatalf("folder not found after restart")
	}
	if folder.RescanIntervalS != 120 {
		t.Errorf("RescanIntervalS = %d after migration, want 120 (user-customised value must be preserved)", folder.RescanIntervalS)
	}
}
