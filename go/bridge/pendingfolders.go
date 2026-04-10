// Pending folder offers: query and accept folders shared by remote devices.
package bridge

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/protocol"
)

// PendingFolderInfo represents a folder offered by one or more remote devices.
type PendingFolderInfo struct {
	ID          string                `json:"id"`
	Label       string                `json:"label"`
	OfferedBy   []PendingDeviceInfo   `json:"offeredBy"`
}

// PendingDeviceInfo identifies a device that offered a pending folder.
type PendingDeviceInfo struct {
	DeviceID string `json:"deviceID"`
	Name     string `json:"name"`
	Time     string `json:"time"`
}

// GetPendingFoldersJSON returns a JSON array of folders offered by remote
// devices that have not yet been accepted (i.e. not configured locally).
func GetPendingFoldersJSON() string {
	mu.Lock()
	defer mu.Unlock()

	if !stRunning || stApp == nil || stApp.Internals == nil {
		return "[]"
	}

	pending, err := stApp.Internals.PendingFolders(protocol.EmptyDeviceID)
	if err != nil {
		return "[]"
	}

	if len(pending) == 0 {
		return "[]"
	}

	// Build device name lookup from config.
	deviceNames := make(map[string]string)
	if stCfg != nil {
		for _, dev := range stCfg.Devices() {
			deviceNames[dev.DeviceID.String()] = dev.Name
		}
	}

	infos := make([]PendingFolderInfo, 0, len(pending))
	for folderID, pf := range pending {
		offered := make([]PendingDeviceInfo, 0, len(pf.OfferedBy))
		label := ""
		for devID, obs := range pf.OfferedBy {
			devIDStr := devID.String()
			name := deviceNames[devIDStr]
			offered = append(offered, PendingDeviceInfo{
				DeviceID: devIDStr,
				Name:     name,
				Time:     obs.Time.Format("2006-01-02T15:04:05Z07:00"),
			})
			if label == "" && obs.Label != "" {
				label = obs.Label
			}
		}
		infos = append(infos, PendingFolderInfo{
			ID:        folderID,
			Label:     label,
			OfferedBy: offered,
		})
	}

	data, err := json.Marshal(infos)
	if err != nil {
		return "[]"
	}
	return string(data)
}

// AcceptPendingFolder creates a new SendReceive folder with the given ID and
// path, and shares it with all devices that offered it. This is the counterpart
// to a remote device sharing a folder — the user picks a local directory and
// the folder is configured to sync with the offering peers.
// Returns empty string on success, error message on failure.
func AcceptPendingFolder(folderID, label, path string) string {
	mu.Lock()
	defer mu.Unlock()

	if !stRunning || stApp == nil || stCfg == nil {
		return "syncthing not running"
	}

	if folderID == "" {
		return "folder ID is required"
	}

	// Reject if folder already configured.
	if _, exists := stCfg.Folders()[folderID]; exists {
		return "folder already exists"
	}

	// Look up which devices offered this folder.
	var offeringDevices []protocol.DeviceID
	if stApp.Internals != nil {
		pending, err := stApp.Internals.PendingFolders(protocol.EmptyDeviceID)
		if err == nil {
			if pf, ok := pending[folderID]; ok {
				for devID := range pf.OfferedBy {
					offeringDevices = append(offeringDevices, devID)
				}
			}
		}
	}

	// Ensure folder path exists.
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Sprintf("create folder path: %v", err)
	}

	// Build device list: local device + all offering devices.
	devices := []config.FolderDeviceConfiguration{
		{DeviceID: stMyID},
	}
	for _, devID := range offeringDevices {
		devices = append(devices, config.FolderDeviceConfiguration{
			DeviceID: devID,
		})
	}

	newFolder := config.FolderConfiguration{
		ID:               folderID,
		Label:            label,
		Path:             path,
		Type:             config.FolderTypeSendReceive,
		RescanIntervalS:  3600,
		FSWatcherEnabled: true,
		FSWatcherDelayS:  10,
		AutoNormalize:    true,
		MaxConflicts:     10,
		Devices:          devices,
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
