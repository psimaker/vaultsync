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

	if !engineUpLocked() || stCfg == nil {
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

	// Same hard floor as AcceptPendingFolder: never let two folders overlap on
	// disk — equal paths merge two vaults into one directory (issue #45), and
	// nested paths make the outer folder sync the inner one's files as its own.
	for _, f := range folders {
		if msg := folderPathOverlapError(f.Path, path); msg != "" {
			return msg
		}
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

	if err := commitConfigLocked(func(cfg *config.Configuration) {
		cfg.Folders = append(cfg.Folders, newFolder)
	}); err != nil {
		return fmt.Sprintf("modify config: %v", err)
	}

	return ""
}

// RemoveFolder removes a folder by its ID.
// Returns empty string on success, error message on failure.
func RemoveFolder(id string) string {
	mu.Lock()
	defer mu.Unlock()

	if !engineUpLocked() || stCfg == nil {
		return "syncthing not running"
	}

	// Check if folder exists.
	folders := stCfg.Folders()
	if _, exists := folders[id]; !exists {
		return "folder not found"
	}

	if err := commitConfigLocked(func(cfg *config.Configuration) {
		filtered := make([]config.FolderConfiguration, 0, len(cfg.Folders))
		for _, f := range cfg.Folders {
			if f.ID != id {
				filtered = append(filtered, f)
			}
		}
		cfg.Folders = filtered
	}); err != nil {
		return fmt.Sprintf("modify config: %v", err)
	}

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
// restore, or migration). The folder goes on with the index it has, so the
// new path must be the folder's storage: whatever differs there from the
// index is taken for a local change on the first scan and sent to every peer,
// a missing file as a deletion, an older one as the new version. Three things
// have to hold, and each refusal leaves the folder where it is:
//
//   - the folder's own directory is gone — while it exists the folder is
//     live, and a live folder is never re-pointed (decision 003);
//   - the new path is a directory with this folder's `.stfolder` marker. This
//     call deliberately does NOT create the directory: an already-indexed
//     folder will not recreate its marker (yielding ErrMarkerMissing);
//   - the directory agrees with the folder's index (verifyFolderStorage). The
//     marker alone is no proof: a copy made by hand carries it (#315,
//     decision 063).
//
// The checks read the disk and the whole index, so they run without the
// bridge lock; the folder is looked up again before the change is written.
//
// Returns empty string on success — including a no-op when the path is already
// equivalent — or an error message on failure.
func SetFolderPath(folderID, newPath string) string {
	mu.Lock()
	if !engineUpLocked() || stCfg == nil {
		mu.Unlock()
		return "syncthing not running"
	}
	folder, exists := stCfg.Folders()[folderID]
	index, _ := stDB.(localIndex)
	mu.Unlock()

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

	// Syncthing's marker is a `.stfolder` directory containing
	// `syncthing-folder-<hash(folderID)>.txt`: without that exact file the
	// target is an empty directory or another folder's.
	if markerErr := verifyFolderMarker(folder, newPath); markerErr != "" {
		return markerErr
	}

	// A folder whose own directory is still there keeps syncing in it: its
	// index moves with every file that arrives, and nothing compared against
	// it below would still hold when the change is written.
	if own, err := os.Stat(folder.Path); err == nil && own.IsDir() {
		return "the folder's own directory still exists"
	}

	if storageErr := verifyFolderStorage(folder, newPath, index); storageErr != "" {
		return storageErr
	}

	return writeFolderPath(folderID, folder.Path, newPath)
}

// writeFolderPath re-points a folder at newPath if it is still the folder
// SetFolderPath checked: configured, and at checkedPath. The checks ran
// without the bridge lock, so the folder may have been removed, added again
// or moved by another call since. That would be another question, and its
// answer is not known.
func writeFolderPath(folderID, checkedPath, newPath string) string {
	mu.Lock()
	defer mu.Unlock()

	if !engineUpLocked() || stCfg == nil {
		return "syncthing not running"
	}
	if current, ok := stCfg.Folders()[folderID]; !ok || current.Path != checkedPath {
		return "the folder changed while the target was checked"
	}

	if err := commitConfigLocked(func(cfg *config.Configuration) {
		for i := range cfg.Folders {
			if cfg.Folders[i].ID == folderID {
				cfg.Folders[i].Path = newPath
				break
			}
		}
	}); err != nil {
		return fmt.Sprintf("modify config: %v", err)
	}

	return ""
}

// verifyFolderMarker returns "" if newPath holds the Syncthing folder marker for
// this folder, or a user-facing error string if it does not. For the default
// `.stfolder` marker it checks the folder-ID-specific fingerprint file
// (`syncthing-folder-<hash>.txt`), which tells this folder's directory from an
// empty or a foreign one — and not from a copy of it, which is why
// verifyFolderStorage follows. For a custom marker name it falls back to
// checking the marker entry's presence.
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

// SetFolderPaused pauses or resumes an existing folder by ID. A paused folder is
// neither scanned nor exchanged with peers, so pausing is the non-destructive way
// to immediately halt an in-progress collision where two folders share one local
// path and keep merging their contents into each other (issue #45). Nothing on
// disk is touched — only the folder's run state changes — so it is fully
// reversible by the user.
//
// Idempotent: a no-op (and no folder-runner restart) when the folder is already
// in the requested state. Returns empty string on success, error message on
// failure.
func SetFolderPaused(folderID string, paused bool) string {
	mu.Lock()
	defer mu.Unlock()

	if !engineUpLocked() || stCfg == nil {
		return "syncthing not running"
	}

	folders := stCfg.Folders()
	folder, exists := folders[folderID]
	if !exists {
		return "folder not found"
	}

	// No-op when already in the requested state — avoids a needless folder
	// runner restart on every launch in the common (already-correct) case.
	if folder.Paused == paused {
		return ""
	}

	if err := commitConfigLocked(func(cfg *config.Configuration) {
		for i := range cfg.Folders {
			if cfg.Folders[i].ID == folderID {
				cfg.Folders[i].Paused = paused
				break
			}
		}
	}); err != nil {
		return fmt.Sprintf("modify config: %v", err)
	}

	return ""
}

// GetFoldersJSON returns a JSON array of all configured folders.
func GetFoldersJSON() string {
	mu.Lock()
	defer mu.Unlock()

	if !engineUpLocked() || stCfg == nil {
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

	if !engineUpLocked() || stCfg == nil {
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

	if err := commitConfigLocked(func(cfg *config.Configuration) {
		for i, f := range cfg.Folders {
			if f.ID == folderID {
				cfg.Folders[i].Devices = append(cfg.Folders[i].Devices, config.FolderDeviceConfiguration{
					DeviceID: devID,
				})
				break
			}
		}
	}); err != nil {
		return fmt.Sprintf("modify config: %v", err)
	}

	return ""
}

// UnshareFolderFromDevice removes a peer device from a folder's share list.
// Cannot remove the local device.
// Returns empty string on success, error message on failure.
func UnshareFolderFromDevice(folderID, deviceID string) string {
	mu.Lock()
	defer mu.Unlock()

	if !engineUpLocked() || stCfg == nil {
		return "syncthing not running"
	}

	devID, err := protocol.DeviceIDFromString(deviceID)
	if err != nil {
		return fmt.Sprintf("invalid device ID: %v", err)
	}

	if devID == stMyID {
		return "cannot unshare from own device"
	}

	if err := commitConfigLocked(func(cfg *config.Configuration) {
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
	}); err != nil {
		return fmt.Sprintf("modify config: %v", err)
	}

	return ""
}
