// Folder management: add, remove, list, share with devices.
package bridge

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/protocol"
)

// defaultRescanIntervalS is the safety-net rescan interval applied to folders
// created by VaultSync. Matches Syncthing's own default — short enough to
// recover quickly when the FSWatcher misses an event (e.g. cross-sandbox
// writes from Obsidian via a security-scoped bookmark on iOS).
const defaultRescanIntervalS = 60

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
		RescanIntervalS:  defaultRescanIntervalS,
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

// SetFolderPath rewrites the on-disk path of an existing folder IN PLACE,
// preserving its devices, label, ignore patterns, and index database. The
// database is keyed by folder ID, so Syncthing restarts the folder runner at
// the new path WITHOUT re-hashing the data or re-downloading it from peers
// (changing Path is a restart-only config change in lib/model).
//
// VaultSync uses this to re-point a folder at the current sandbox location
// after iOS changes the absolute container path (which it does on reinstall,
// restore, or migration). The new path MUST already exist as a directory that
// physically holds the folder's data and its `.stfolder` marker. This call
// deliberately does NOT create the directory: pointing a send-receive folder at
// a fresh empty directory looks like "all files deleted" and could propagate
// deletions to peers, and an already-indexed folder will not recreate its
// marker (yielding ErrMarkerMissing).
//
// Returns empty string on success — including a no-op when the path is already
// equivalent — or an error message on failure.
func SetFolderPath(folderID, newPath string) string {
	mu.Lock()
	defer mu.Unlock()

	if !stRunning || stCfg == nil {
		return "syncthing not running"
	}

	folders := stCfg.Folders()
	folder, exists := folders[folderID]
	if !exists {
		return "folder not found"
	}

	// No-op when the path is effectively unchanged — avoids a needless folder
	// restart on every launch in the common (already-correct) case.
	if filepath.Clean(folder.Path) == filepath.Clean(newPath) {
		return ""
	}

	// Refuse to rebase onto a path that does not already exist as a directory.
	info, err := os.Stat(newPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "target path does not exist"
		}
		return fmt.Sprintf("stat target path: %v", err)
	}
	if !info.IsDir() {
		return "target path is not a directory"
	}

	// Defense in depth against a destructive rebase: only re-point a folder at a
	// directory that already holds THIS folder's data. Syncthing's marker is a
	// `.stfolder` directory containing `syncthing-folder-<hash(folderID)>.txt`,
	// so the presence of that exact file proves the target was this very
	// folder's root (not an empty dir, and not a foreign folder). Without this,
	// rebasing a send-receive folder onto an empty or mismatched directory would
	// make Syncthing treat every indexed file as deleted and propagate those
	// deletions to peers.
	if markerErr := verifyFolderMarker(folder, newPath); markerErr != "" {
		return markerErr
	}

	waiter, err := stCfg.Modify(func(cfg *config.Configuration) {
		for i := range cfg.Folders {
			if cfg.Folders[i].ID == folderID {
				cfg.Folders[i].Path = newPath
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

// verifyFolderMarker returns "" if newPath holds the Syncthing folder marker for
// this folder, or a user-facing error string if it does not. For the default
// `.stfolder` marker it checks the folder-ID-specific fingerprint file
// (`syncthing-folder-<hash>.txt`), which proves the target was this very
// folder's root rather than an empty or foreign directory. For a custom marker
// name it falls back to checking the marker entry's presence.
func verifyFolderMarker(folder config.FolderConfiguration, newPath string) string {
	markerName := folder.MarkerName
	if markerName == "" {
		markerName = config.DefaultMarkerName
	}

	markerPath := filepath.Join(newPath, markerName)
	if markerName == config.DefaultMarkerName {
		h := sha256.Sum256([]byte(folder.ID))
		markerPath = filepath.Join(markerPath, fmt.Sprintf("syncthing-folder-%x.txt", h[:3]))
	}

	if _, err := os.Stat(markerPath); err != nil {
		if os.IsNotExist(err) {
			return "target does not contain this folder's data (marker missing)"
		}
		return fmt.Sprintf("stat folder marker: %v", err)
	}
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
