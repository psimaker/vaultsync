// Device management: add, remove, list configured Syncthing peers.
package bridge

import (
	"encoding/json"
	"fmt"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/protocol"
)

// DeviceInfo is the JSON-serializable representation of a configured device.
// Used by both GetDevicesJSON and GetConnectionsJSON.
type DeviceInfo struct {
	DeviceID  string `json:"deviceID"`
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
	Paused    bool   `json:"paused"`
}

// AddDevice adds a peer device by its Device ID string.
// Returns empty string on success, error message on failure.
func AddDevice(deviceID string, name string) string {
	mu.Lock()
	defer mu.Unlock()

	if !stRunning || stCfg == nil {
		return "syncthing not running"
	}

	id, err := protocol.DeviceIDFromString(deviceID)
	if err != nil {
		return fmt.Sprintf("invalid device ID: %v", err)
	}

	if id == stMyID {
		return "cannot add own device ID"
	}

	// Check if device already exists.
	for _, dev := range stCfg.Devices() {
		if dev.DeviceID == id {
			return "device already exists"
		}
	}

	newDevice := config.DeviceConfiguration{
		DeviceID: id,
		Name:     name,
	}

	waiter, err := stCfg.Modify(func(cfg *config.Configuration) {
		cfg.Devices = append(cfg.Devices, newDevice)
	})
	if err != nil {
		return fmt.Sprintf("modify config: %v", err)
	}
	waiter.Wait()

	return ""
}

// RemoveDevice removes a peer device by its Device ID string.
// Returns empty string on success, error message on failure.
func RemoveDevice(deviceID string) string {
	mu.Lock()
	defer mu.Unlock()

	if !stRunning || stCfg == nil {
		return "syncthing not running"
	}

	id, err := protocol.DeviceIDFromString(deviceID)
	if err != nil {
		return fmt.Sprintf("invalid device ID: %v", err)
	}

	waiter, err := stCfg.Modify(func(cfg *config.Configuration) {
		devices := make([]config.DeviceConfiguration, 0, len(cfg.Devices))
		for _, dev := range cfg.Devices {
			if dev.DeviceID != id {
				devices = append(devices, dev)
			}
		}
		cfg.Devices = devices
	})
	if err != nil {
		return fmt.Sprintf("modify config: %v", err)
	}
	waiter.Wait()

	return ""
}

// RenameDevice updates the name of an existing peer device.
// Returns empty string on success, error message on failure.
func RenameDevice(deviceID string, newName string) string {
	mu.Lock()
	defer mu.Unlock()

	if !stRunning || stCfg == nil {
		return "syncthing not running"
	}

	id, err := protocol.DeviceIDFromString(deviceID)
	if err != nil {
		return fmt.Sprintf("invalid device ID: %v", err)
	}

	if id == stMyID {
		return "cannot rename own device"
	}

	found := false
	for _, dev := range stCfg.Devices() {
		if dev.DeviceID == id {
			found = true
			break
		}
	}
	if !found {
		return "device not found"
	}

	waiter, err := stCfg.Modify(func(cfg *config.Configuration) {
		for i, dev := range cfg.Devices {
			if dev.DeviceID == id {
				cfg.Devices[i].Name = newName
				break
			}
		}
	})
	if err != nil {
		return fmt.Sprintf("modify config: %v", err)
	}
	waiter.Wait()

	return ""
}

// GetDevicesJSON returns a JSON array of all configured devices (excluding self).
func GetDevicesJSON() string {
	mu.Lock()
	defer mu.Unlock()

	if !stRunning || stCfg == nil {
		return "[]"
	}

	devices := stCfg.Devices()
	infos := make([]DeviceInfo, 0, len(devices))
	for _, dev := range devices {
		if dev.DeviceID == stMyID {
			continue
		}
		connected := false
		if stApp != nil && stApp.Internals != nil {
			connected = stApp.Internals.IsConnectedTo(dev.DeviceID)
		}
		infos = append(infos, DeviceInfo{
			DeviceID:  dev.DeviceID.String(),
			Name:      dev.Name,
			Connected: connected,
			Paused:    dev.Paused,
		})
	}

	data, err := json.Marshal(infos)
	if err != nil {
		return "[]"
	}
	return string(data)
}
