// Folder management: add, remove, list, share with devices.
package bridge

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/protocol"
)

// FolderInfo is the JSON-serializable representation of a configured folder.
type FolderInfo struct {
	ID        string   `json:"id"`
	Label     string   `json:"label"`
	Path      string   `json:"path"`
	Type      string   `json:"type"`
	Paused    bool     `json:"paused"`
	DeviceIDs []string `json:"deviceIDs"`
}

// AddFolder adds a new folder with SendReceive type.
// The local device is automatically included in the share list.
// Returns empty string on success, error message on failure.
func AddFolder(id, label, path string) string {
	mu.Lock()
	defer mu.Unlock()

	if !stRunning || stCfg == nil {
		return "syncthing not running"
	}

	if id == "" {
		return "folder ID is required"
	}

	// Check if folder already exists.
	folders := stCfg.Folders()
	if _, exists := folders[id]; exists {
		return "folder already exists"
	}

	// Ensure folder path exists.
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Sprintf("create folder path: %v", err)
	}

	newFolder := config.FolderConfiguration{
		ID:               id,
		Label:            label,
		Path:             path,
		Type:             config.FolderTypeSendReceive,
		RescanIntervalS:  3600,
		FSWatcherEnabled: true,
		FSWatcherDelayS:  10,
		AutoNormalize:    true,
		MaxConflicts:     10,
		IgnorePerms:      true, // iOS has no Unix permissions; prevents endless sync loops
		Devices: []config.FolderDeviceConfiguration{
			{DeviceID: stMyID},
		},
	}

	waiter, err := stCfg.Modify(func(cfg *config.Configuration) {
		cfg.Folders = append(cfg.Folders, newFolder)
	})
	if err != nil {
		return fmt.Sprintf("modify config: %v", err)
	}
	waiter.Wait()

	return ""
}

// RemoveFolder removes a folder by its ID.
// Returns empty string on success, error message on failure.
func RemoveFolder(id string) string {
	mu.Lock()
	defer mu.Unlock()

	if !stRunning || stCfg == nil {
		return "syncthing not running"
	}

	// Check if folder exists.
	folders := stCfg.Folders()
	if _, exists := folders[id]; !exists {
		return "folder not found"
	}

	waiter, err := stCfg.Modify(func(cfg *config.Configuration) {
		filtered := make([]config.FolderConfiguration, 0, len(cfg.Folders))
		for _, f := range cfg.Folders {
			if f.ID != id {
				filtered = append(filtered, f)
			}
		}
		cfg.Folders = filtered
	})
	if err != nil {
		return fmt.Sprintf("modify config: %v", err)
	}
	waiter.Wait()

	return ""
}

// GetFoldersJSON returns a JSON array of all configured folders.
func GetFoldersJSON() string {
	mu.Lock()
	defer mu.Unlock()

	if !stRunning || stCfg == nil {
		return "[]"
	}

	folders := stCfg.Folders()
	infos := make([]FolderInfo, 0, len(folders))
	for _, f := range folders {
		deviceIDs := make([]string, 0, len(f.Devices))
		for _, d := range f.Devices {
			if d.DeviceID != stMyID {
				deviceIDs = append(deviceIDs, d.DeviceID.String())
			}
		}
		infos = append(infos, FolderInfo{
			ID:        f.ID,
			Label:     f.Label,
			Path:      f.Path,
			Type:      f.Type.String(),
			Paused:    f.Paused,
			DeviceIDs: deviceIDs,
		})
	}

	data, err := json.Marshal(infos)
	if err != nil {
		return "[]"
	}
	return string(data)
}

// ShareFolderWithDevice shares an existing folder with a peer device.
// Returns empty string on success, error message on failure.
func ShareFolderWithDevice(folderID, deviceID string) string {
	mu.Lock()
	defer mu.Unlock()

	if !stRunning || stCfg == nil {
		return "syncthing not running"
	}

	devID, err := protocol.DeviceIDFromString(deviceID)
	if err != nil {
		return fmt.Sprintf("invalid device ID: %v", err)
	}

	folders := stCfg.Folders()
	folder, exists := folders[folderID]
	if !exists {
		return "folder not found"
	}

	for _, d := range folder.Devices {
		if d.DeviceID == devID {
			return "folder already shared with device"
		}
	}

	waiter, err := stCfg.Modify(func(cfg *config.Configuration) {
		for i, f := range cfg.Folders {
			if f.ID == folderID {
				cfg.Folders[i].Devices = append(cfg.Folders[i].Devices, config.FolderDeviceConfiguration{
					DeviceID: devID,
				})
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

// UnshareFolderFromDevice removes a peer device from a folder's share list.
// Cannot remove the local device.
// Returns empty string on success, error message on failure.
func UnshareFolderFromDevice(folderID, deviceID string) string {
	mu.Lock()
	defer mu.Unlock()

	if !stRunning || stCfg == nil {
		return "syncthing not running"
	}

	devID, err := protocol.DeviceIDFromString(deviceID)
	if err != nil {
		return fmt.Sprintf("invalid device ID: %v", err)
	}

	if devID == stMyID {
		return "cannot unshare from own device"
	}

	waiter, err := stCfg.Modify(func(cfg *config.Configuration) {
		for i, f := range cfg.Folders {
			if f.ID == folderID {
				devices := make([]config.FolderDeviceConfiguration, 0, len(f.Devices))
				for _, d := range f.Devices {
					if d.DeviceID != devID {
						devices = append(devices, d)
					}
				}
				cfg.Folders[i].Devices = devices
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
