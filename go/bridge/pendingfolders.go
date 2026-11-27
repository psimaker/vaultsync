// Pending folder offers: query and accept folders shared by remote devices.
package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/syncthing"
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
// The folders, and the devices offering each, come in the same order on every
// call for the same offers (#342): see sortPendingFolders.
func GetPendingFoldersJSON() string {
	mu.Lock()
	defer mu.Unlock()

	if !engineUpLocked() || stApp == nil || stApp.Internals == nil {
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
		offers := make([]pendingOffer, 0, len(pf.OfferedBy))
		for devID, obs := range pf.OfferedBy {
			devIDStr := devID.String()
			offers = append(offers, pendingOffer{
				device: PendingDeviceInfo{
					DeviceID: devIDStr,
					Name:     deviceNames[devIDStr],
					Time:     obs.Time.Format("2006-01-02T15:04:05Z07:00"),
				},
				label: obs.Label,
			})
		}
		infos = append(infos, pendingFolderInfo(folderID, offers))
	}
	sortPendingFolders(infos)

	data, err := json.Marshal(infos)
	if err != nil {
		return "[]"
	}
	return string(data)
}

// pendingOffer is one device's offer of a pending folder, with the label that
// device gives it.
type pendingOffer struct {
	device PendingDeviceInfo
	label  string
}

// pendingFolderInfo builds a folder's row from its offers, which arrive in
// map order: the engine keeps the offers of one folder in a map keyed by
// device. The devices are put in the order of their device IDs, and the
// folder takes the label of the first of them that gives one, so neither the
// list nor the label changes from one call to the next (#342). Not by the
// devices' names: the engine fills in a name when a device connects, and the
// user can edit it, and the label — which names the folder a share is
// accepted into — would then change with it.
func pendingFolderInfo(folderID string, offers []pendingOffer) PendingFolderInfo {
	slices.SortFunc(offers, func(a, b pendingOffer) int {
		return strings.Compare(a.device.DeviceID, b.device.DeviceID)
	})
	info := PendingFolderInfo{ID: folderID, OfferedBy: make([]PendingDeviceInfo, 0, len(offers))}
	for _, o := range offers {
		info.OfferedBy = append(info.OfferedBy, o.device)
		if info.Label == "" {
			info.Label = o.label
		}
	}
	return info
}

// sortPendingFolders puts the pending folders in the order of their IDs. The
// engine hands them out as a map, whose order differs from one call to the
// next; unsorted, the app's rows swapped places between two polls and every
// swap ran its automatic accept pass again (#342). The app lists them by name
// in the user's language (SyncthingManager.pendingFoldersInDisplayOrder),
// which Go cannot sort by without a collation library.
func sortPendingFolders(infos []PendingFolderInfo) {
	slices.SortFunc(infos, func(a, b PendingFolderInfo) int {
		return strings.Compare(a.ID, b.ID)
	})
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
//
// root is the folder the user selected for their vaults. The share is only
// accepted into a direct subfolder of it (#270, see notDirectChildError).
// Returns empty string on success, error message on failure.
func AcceptPendingFolder(folderID, label, path, root string, allowNonEmpty bool) string {
	mu.Lock()
	defer mu.Unlock()

	if !engineUpLocked() || stApp == nil || stCfg == nil {
		return "syncthing not running"
	}

	if folderID == "" {
		return "folder ID is required"
	}

	// Reject if folder already configured.
	if _, exists := stCfg.Folders()[folderID]; exists {
		return "folder already exists"
	}
	// An accept of this share the bridge handed to the engine earlier and has
	// not seen reach the configuration is still queued: queueing a second
	// one could configure the share twice, or in another place than the one
	// the first accept chose (#341). Nothing is created or queued for it.
	if acceptInFlight(folderID) {
		return folderAcceptNotConfirmed
	}

	// Reject any path that is not a direct subfolder of the selected folder.
	// The client maps every share to `root/<name>`; this floor holds even if
	// it computed the selected folder itself or a path further down (#270).
	if msg := notDirectChildError(root, path); msg != "" {
		return msg
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
	// An accept still queued in the engine counts as a folder already: its
	// path is not in the configuration yet, and a second share accepted into
	// it, or into a folder in or around it, would sync one directory with it
	// once both have landed (#341, decision 001). The same comparison and the
	// same refusal as for a configured folder; a target a queued accept is
	// headed for is taken, so the non-empty check below never has to judge
	// it, merge consent or not.
	if msg := queuedAcceptOverlapError(path, ""); msg != "" {
		return msg
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

	// Look up which devices offered this folder. A failed read refuses the
	// accept: going ahead would create a local-only folder that no peer
	// shares, indistinguishable from a real accept, and the caller cannot
	// tell "no offer" from "could not read the offers" (#182). Only a
	// successful read that finds no offer — the offer was withdrawn between
	// listing and acceptance — still creates the folder with the local
	// device only; the user can share it manually later.
	if stApp.Internals == nil {
		return "read pending folder offers: engine internals unavailable"
	}
	offeringDevices, err := offeringDevicesFor(stApp.Internals, folderID)
	if err != nil {
		return fmt.Sprintf("read pending folder offers: %v", err)
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
		RescanIntervalS:  defaultRescanIntervalS,
		FSWatcherEnabled: true,
		FSWatcherDelayS:  10,
		AutoNormalize:    true,
		MaxConflicts:     10,
		IgnorePerms:      true, // iOS has no Unix permissions; prevents endless sync loops
		Devices:          devices,
	}

	markAcceptInFlight(folderID, path)
	err = commitConfigLockedTaken(func(cfg *config.Configuration) {
		cfg.Folders = append(cfg.Folders, newFolder)
	}, func() {
		// The engine has taken the change from its queue: it is in the
		// configuration now, or the engine refused it — a second folder of
		// one ID among the refusals. Only from here on does the floor above
		// see the folder itself instead of the queued accept.
		clearAcceptInFlight(folderID)
	})
	// The engine takes a change into its configuration first and then waits
	// for its services: an accept that reached the configuration before the
	// wait ended is accepted, whatever the services still do (#341, as for
	// a removal in #343). Only a folder at this accept's own path is this
	// accept: another one of the ID is an earlier accept that landed, and
	// this one was refused as its duplicate.
	if f, ok := stCfg.Folder(folderID); ok {
		if filepath.Clean(f.Path) != filepath.Clean(path) {
			return "folder already exists"
		}
		noteFolderStorageLocked(newFolder)
		return ""
	}
	if err == nil {
		// Applied and gone again, or never applied: nothing to confirm.
		return "accept did not reach the configuration"
	}
	if errors.Is(err, errWaitTimedOut) && acceptInFlight(folderID) {
		// Still queued behind another change. It lands once that one is
		// through, or the engine refuses it. The engine works through its
		// queue while it stops and saves what it applied, so it may also be
		// configured at the next start; only a stop that ends before the
		// change is taken loses it. Until it is taken every accept of the
		// share gets this answer.
		return folderAcceptNotConfirmed
	}
	return fmt.Sprintf("modify config: %v", err)
}

// folderAcceptNotConfirmed is how AcceptPendingFolder answers an accept it
// handed to the engine and did not see reach the configuration within its
// wait, and every further accept of that share until it has (#341).
const folderAcceptNotConfirmed = "folder accept not confirmed yet"

// FolderAcceptNotConfirmedError returns the text AcceptPendingFolder answers
// such an accept with. The app tells it from a refusal — the accept may
// still land — and reads the text here instead of keeping a copy of it.
func FolderAcceptNotConfirmedError() string {
	return folderAcceptNotConfirmed
}

// AcceptInFlight reports whether an accept of the share the bridge answered
// with FolderAcceptNotConfirmedError is still waiting in the engine's queue.
// Once it is not, the share is configured — the accept landed — or it is
// not, and the engine refused it or a stop lost it (#341). It takes no
// engine lock and never waits.
func AcceptInFlight(folderID string) bool {
	return acceptInFlight(folderID)
}

// The shares whose accept the bridge handed to the engine and has not seen
// taken from the engine's queue, with the path each is headed for. Guarded
// by its own lock, not mu: the engine's configuration loop clears an entry
// while a later bridge call may hold mu and wait for that same loop.
var (
	acceptsInFlightMu sync.Mutex
	acceptsInFlight   = map[string]string{}
)

func acceptInFlight(folderID string) bool {
	acceptsInFlightMu.Lock()
	defer acceptsInFlightMu.Unlock()
	_, ok := acceptsInFlight[folderID]
	return ok
}

func markAcceptInFlight(folderID, path string) {
	acceptsInFlightMu.Lock()
	acceptsInFlight[folderID] = path
	acceptsInFlightMu.Unlock()
}

// queuedAcceptOverlapError holds path against every accept still queued in
// the engine, except the one of folder except, as against a configured
// folder: folderPathOverlapError's comparison and its refusal.
func queuedAcceptOverlapError(path, except string) string {
	acceptsInFlightMu.Lock()
	defer acceptsInFlightMu.Unlock()
	for id, queued := range acceptsInFlight {
		if id == except {
			continue
		}
		if msg := folderPathOverlapError(queued, path); msg != "" {
			return msg
		}
	}
	return ""
}

func clearAcceptInFlight(folderID string) {
	acceptsInFlightMu.Lock()
	delete(acceptsInFlight, folderID)
	acceptsInFlightMu.Unlock()
}

// forgetAcceptsInFlight drops every entry: a new engine has an empty queue,
// and an accept the last one never took is gone with it.
func forgetAcceptsInFlight() {
	acceptsInFlightMu.Lock()
	acceptsInFlight = map[string]string{}
	acceptsInFlightMu.Unlock()
}

// offeringDevicesFor lists the devices currently offering folderID. A
// variable so tests can inject a failed read of the pending offers (#182).
var offeringDevicesFor = func(internals *syncthing.Internals, folderID string) ([]protocol.DeviceID, error) {
	pending, err := internals.PendingFolders(protocol.EmptyDeviceID)
	if err != nil {
		return nil, err
	}
	var devices []protocol.DeviceID
	if pf, ok := pending[folderID]; ok {
		for devID := range pf.OfferedBy {
			devices = append(devices, devID)
		}
	}
	return devices, nil
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

// notDirectChildError returns a non-empty error message unless path is a
// direct subfolder of root, the folder the user selected for their vaults.
// Obsidian lists the direct subfolders of its folder as vaults and nothing
// else: a share synced into the folder itself, or two levels down, arrives in
// full and never shows up as a vault (#270). Both paths are cleaned first, so
// a name of "." or ".." cannot stand in for the selected folder or its parent.
// Compared case-insensitively like the overlap check: the iOS data volume is
// case-folding APFS.
func notDirectChildError(root, path string) string {
	if root == "" {
		return "the selected folder is required"
	}
	r := filepath.Clean(root)
	p := filepath.Clean(path)
	if strings.EqualFold(p, r) || !strings.EqualFold(filepath.Dir(p), r) {
		return "the folder must be a direct subfolder of the selected folder"
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
