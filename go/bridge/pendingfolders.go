// Pending folder offers: query and accept folders shared by remote devices.
package bridge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/protocol"
)

// PendingFolderInfo represents a folder offered by one or more remote devices.
type PendingFolderInfo struct {
	ID        string              `json:"id"`
	Label     string              `json:"label"`
	OfferedBy []PendingDeviceInfo `json:"offeredBy"`
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
//
// allowNonEmpty must be false unless the user explicitly confirmed syncing
// into an existing directory that already holds content: accepting into a
// non-empty target merges two content sets and pushes the mix to every
// offering peer (#54). The floor treats a directory holding at most
// Obsidian's `.obsidian` configuration folder as empty — mirror of the Swift
// `VaultManager.isEmptyVaultListing` rule; the two layers must decide
// emptiness identically or a share refused above would slip through below.
// Returns empty string on success, error message on failure.
func AcceptPendingFolder(folderID, label, path string, allowNonEmpty bool) string {
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

	// Reject if this local path overlaps another configured folder's path —
	// the same directory, one inside it, or one containing it. Two folder IDs
	// on one directory makes Syncthing merge their contents and push the mix
	// back to every peer (issue #45); nesting is the same corruption one level
	// down: the outer folder scans the inner folder's files as its own content
	// and syncs them to its peers, and a peer deleting that stray copy then
	// propagates the deletion into the inner folder everywhere (#45 follow-up).
	// The local path is the safety boundary, so enforce it here as the hard
	// floor even if the client computed an overlapping path.
	for _, f := range stCfg.Folders() {
		if msg := folderPathOverlapError(f.Path, path); msg != "" {
			return msg
		}
	}

	// Reject a target directory that already holds content unless the caller
	// carries the user's explicit confirmation: syncing into it merges two
	// content sets and pushes the mix to every offering peer (#54). Same
	// hard-floor posture as the overlap check above — the client decides the
	// UX, this floor guarantees no caller can merge silently.
	if !allowNonEmpty {
		if msg := nonEmptyTargetError(path); msg != "" {
			return msg
		}
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

	// Note: offeringDevices may be empty if the offer disappeared between
	// listing and acceptance. The folder is still created with local device
	// only; the user can manually share it later.

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
		RescanIntervalS:  defaultRescanIntervalS,
		FSWatcherEnabled: true,
		FSWatcherDelayS:  10,
		AutoNormalize:    true,
		MaxConflicts:     10,
		IgnorePerms:      true, // iOS has no Unix permissions; prevents endless sync loops
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

// nonEmptyTargetError returns a non-empty error message when path exists and
// holds anything beyond Obsidian's `.obsidian` configuration folder — notes,
// a `.stfolder` sync marker, hidden leftovers all count as content, because
// syncing a share into them merges two content sets (#54). A missing path is
// fine (the accept creates it); an unreadable one is refused — emptiness that
// cannot be verified must not be assumed. Name comparison is
// case-insensitive: the iOS data volume is case-folding APFS. Mirror of the
// Swift `VaultManager.isEmptyVaultListing` rule — keep the two in lockstep.
func nonEmptyTargetError(path string) string {
	entries, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		return fmt.Sprintf("read folder path: %v", err)
	}
	for _, entry := range entries {
		if !strings.EqualFold(entry.Name(), ".obsidian") {
			return "the target folder already contains files"
		}
	}
	return ""
}

// folderPathOverlapError returns a non-empty error message when candidate
// overlaps existing: the same directory, a directory inside it, or a directory
// containing it. Paths are cleaned (so `./` and trailing-slash differences do
// not matter) and compared case-insensitively: the iOS data volume is
// case-folding APFS, where "Vault" and "vault" are one and the same directory.
// The prefix checks are boundary-aware, so "/VaultA" never matches "/VaultAx".
func folderPathOverlapError(existing, candidate string) string {
	e := strings.ToLower(filepath.Clean(existing))
	c := strings.ToLower(filepath.Clean(candidate))
	sep := string(filepath.Separator)
	switch {
	case e == c:
		return "another folder already syncs to this path"
	case strings.HasPrefix(c, e+sep):
		return "this path is inside a directory another folder already syncs"
	case strings.HasPrefix(e, c+sep):
		return "another folder already syncs a directory inside this path"
	}
	return ""
}
