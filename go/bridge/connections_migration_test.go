package bridge

import (
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
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
	// wait for the flush (tracked in #151 as configuration durability).
	// Waiting for the device ID alone is not enough: AddDevice already
	// wrote it with the pin, so the restart could read numConnections=1
	// and the assertion below would pass without the migration ever
	// running. Wait for the legacy value to reach the file.
	waitFor(t, "config.xml to hold the legacy connection count", 10*time.Second, func() bool {
		n, ok := persistedNumConnections(t, configDir, peer)
		return ok && n == 0
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

// persistedNumConnections reads the device's connection count straight from
// config.xml — what the next start will actually load. The attribute is
// absent when it was never written, which is the legacy state (0).
func persistedNumConnections(t *testing.T, configDir, deviceID string) (int, bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(configDir, "config.xml"))
	if err != nil {
		return 0, false
	}
	var cfg struct {
		Devices []struct {
			ID             string `xml:"id,attr"`
			NumConnections *int   `xml:"numConnections"`
		} `xml:"device"`
	}
	if err := xml.Unmarshal(raw, &cfg); err != nil {
		return 0, false
	}
	for _, d := range cfg.Devices {
		if d.ID != deviceID {
			continue
		}
		if d.NumConnections == nil {
			return 0, true
		}
		return *d.NumConnections, true
	}
	return 0, false
}
