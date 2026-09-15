package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/syncthing/syncthing/lib/config"
)

// Every peer is pinned to one connection (#185, decision 042): devices added
// through the bridge and devices written by older builds alike. The multi-
// connection promotion race that stranded a heavy index exchange needs a
// second connection to land on; with one there is none.
func TestIssue185_OneConnectionPerDevice(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}

	const peer = "MFZWI3D-BONSGYC-YLTMRWG-C43ENR5-QXGZDMM-FZWI3DP-BONSGYY-LTMRWAD"
	if errMsg := AddDevice(peer, "Peer"); errMsg != "" {
		t.Fatalf("AddDevice() = %q", errMsg)
	}
	if got := numConnectionsFor(t, peer); got != 1 {
		t.Fatalf("AddDevice() pinned numConnections=%d, want 1", got)
	}

	// An older build's config: the device without the pin (Syncthing's
	// default of three connections). The next start must migrate it.
	mu.Lock()
	err := commitConfigLocked(func(cfg *config.Configuration) {
		for i := range cfg.Devices {
			if cfg.Devices[i].DeviceID.String() == peer {
				cfg.Devices[i].RawNumConnections = 0
			}
		}
	})
	mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if got := numConnectionsFor(t, peer); got != 0 {
		t.Fatalf("test setup: numConnections=%d, want the legacy 0", got)
	}
	if errMsg := StopSyncthing(); errMsg != "" {
		t.Fatalf("StopSyncthing() = %q", errMsg)
	}
	// The config wrapper saves asynchronously and StopSyncthing does not
	// wait for the flush (tracked in #151 as configuration durability);
	// give the file time to carry the device before the restart reads it.
	waitFor(t, "config.xml to hold the device", 5*time.Second, func() bool {
		raw, err := os.ReadFile(filepath.Join(configDir, "config.xml"))
		return err == nil && strings.Contains(string(raw), peer)
	})

	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("restart StartSyncthing() failed: %s", errMsg)
	}
	if got := numConnectionsFor(t, peer); got != 1 {
		t.Fatalf("migration left numConnections=%d, want 1", got)
	}
}

func numConnectionsFor(t *testing.T, deviceID string) int {
	t.Helper()
	var cfg struct {
		Devices []struct {
			DeviceID       string `json:"deviceID"`
			NumConnections int    `json:"numConnections"`
		} `json:"devices"`
	}
	if err := json.Unmarshal([]byte(GetConfigJSON()), &cfg); err != nil {
		t.Fatalf("GetConfigJSON() unmarshal: %v", err)
	}
	for _, d := range cfg.Devices {
		if d.DeviceID == deviceID {
			return d.NumConnections
		}
	}
	t.Fatalf("device %s not in config", deviceID)
	return -1
}
