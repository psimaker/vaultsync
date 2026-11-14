// Which directory a folder synced in (#315, decision 063).
package bridge

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/protocol"
)

// The marker says which folder a directory belongs to, and a copy made by
// hand carries it (#315). What a copy cannot carry is the marker's file
// number: a copy is a new file, a directory that was moved or whose parent
// was renamed holds the same one. So while a folder's directory is there the
// bridge writes down the file number of its marker, and a folder whose
// directory is gone is re-pointed only at the directory that has it. No
// content is read, so whatever changed in that directory while the engine
// did not look goes out as the change it is.
//
// A file number means something on the file system it was read from, in the
// index it was read for, and nowhere else. A restore or a migration creates
// every file anew, the app's own included: each record therefore holds the
// file number of an anchor, a file in the configuration directory that is
// created once and never written again. And a folder that was removed and
// added again starts a new index: each record holds the index ID it was
// written for. A record with another anchor or another index ID says nothing
// about the directory offered, and the marker decides as it did before there
// were records.
//
// Records are derived state. They live in the engine's database beside the
// index they stand for, are written by the bridge only, and a missing one
// costs nothing but the protection: the folder falls back to the marker.

// folderStorageMismatch is how SetFolderPath refuses a directory that carries
// a folder's marker and is not the directory the folder synced in.
const folderStorageMismatch = "target is not the directory this folder synced in"

// FolderStorageMismatchError returns the text SetFolderPath refuses such a
// directory with. The app tells this refusal from the others — its user gets
// the way on from a copy — and reads the text here instead of keeping a copy
// of it.
func FolderStorageMismatchError() string {
	return folderStorageMismatch
}

// folderStorageRecord is what the bridge remembers of a folder's directory.
type folderStorageRecord struct {
	// Marker is the file number of the folder's marker.
	Marker uint64 `json:"marker"`
	// Index is the folder's local index ID when Marker was read.
	Index uint64 `json:"index"`
	// Anchor is the file number of this install's anchor when Marker was read.
	Anchor uint64 `json:"anchor"`
}

// folderStorageKeyPrefix names the records in the engine's key-value store.
// The 1 is the format's version.
const folderStorageKeyPrefix = "vaultsync/folder-storage/1/"

// storageAnchorName is the anchor's file in the configuration directory.
const storageAnchorName = "vaultsync-storage-anchor"

// stStorageAnchor is the file number of this install's anchor, read when the
// engine started, or 0 when there is none to be had. Without an anchor
// nothing is recorded and no record applies. Guarded by mu.
var stStorageAnchor uint64

// storageRecords is the part of the engine's database the records need.
type storageRecords interface {
	GetKV(key string) ([]byte, error)
	PutKV(key string, val []byte) error
	GetIndexID(folder string, device protocol.DeviceID) (protocol.IndexID, error)
}

// ensureStorageAnchor returns the file number of the anchor in configDir,
// creating the file if it is not there, or 0 when that fails.
func ensureStorageAnchor(configDir string) uint64 {
	path := filepath.Join(configDir, storageAnchorName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err == nil {
		file.Close()
	} else if !os.IsExist(err) {
		return 0
	}
	number, err := fileNumber(path)
	if err != nil {
		return 0
	}
	return number
}

// noteFolderStorageLocked writes down the directory a folder syncs in, if its
// marker is there to be read. A folder whose directory is gone keeps the
// record it has: that record is what its directory is known by when it shows
// up again. Caller holds mu.
//
// It notes whatever directory the folder is configured at, so a live folder's
// record follows the directory the engine scans — also when that directory
// was replaced in its place, which no record can tell (#329).
func noteFolderStorageLocked(folder config.FolderConfiguration) {
	store, ok := stDB.(storageRecords)
	if !ok || stStorageAnchor == 0 {
		return
	}
	marker, err := fileNumber(folderMarkerPath(folder, folder.Path))
	if err != nil {
		return
	}
	index, err := store.GetIndexID(folder.ID, protocol.LocalDeviceID)
	if err != nil {
		return
	}
	record := folderStorageRecord{Marker: marker, Index: uint64(index), Anchor: stStorageAnchor}
	if existing, found, err := readFolderStorage(store, folder.ID); err == nil && found && existing == record {
		return
	}
	value, err := json.Marshal(record)
	if err != nil {
		return
	}
	_ = store.PutKV(folderStorageKeyPrefix+folder.ID, value)
}

// readFolderStorage returns a folder's record and whether there is one. An
// error is a read that failed, never a record that is missing.
func readFolderStorage(store storageRecords, folderID string) (folderStorageRecord, bool, error) {
	value, err := store.GetKV(folderStorageKeyPrefix + folderID)
	if errors.Is(err, sql.ErrNoRows) {
		return folderStorageRecord{}, false, nil
	}
	if err != nil {
		return folderStorageRecord{}, false, err
	}
	var record folderStorageRecord
	if err := json.Unmarshal(value, &record); err != nil {
		// Not a record this version wrote. It says nothing.
		return folderStorageRecord{}, false, nil
	}
	return record, true, nil
}

// folderStorageRefusalLocked returns "" if newPath may be taken for the
// folder's directory as far as the records know, or the refusal. newPath
// holds the folder's marker; the caller has checked that. Caller holds mu.
//
//   - A record that applies — written on this file system, for the index the
//     folder has now — and the marker at newPath has the recorded file
//     number: this is the directory the folder synced in.
//   - A record that applies and another file number: a directory that was
//     made from the folder's own, a copy. Refused.
//   - No record, or one that does not apply: nothing is known, and the marker
//     has already decided.
//
// What cannot be read is not taken for unknown (decision 039): a failed read
// of the record, of the index ID or of the marker's file number refuses.
func folderStorageRefusalLocked(folder config.FolderConfiguration, newPath string) string {
	store, ok := stDB.(storageRecords)
	if !ok || stStorageAnchor == 0 {
		return ""
	}
	record, found, err := readFolderStorage(store, folder.ID)
	if err != nil {
		return fmt.Sprintf("check folder storage: read the record: %v", err)
	}
	if !found || record.Anchor != stStorageAnchor {
		return ""
	}
	index, err := store.GetIndexID(folder.ID, protocol.LocalDeviceID)
	if err != nil {
		return fmt.Sprintf("check folder storage: read the index ID: %v", err)
	}
	if record.Index != uint64(index) {
		return ""
	}
	marker, err := fileNumber(folderMarkerPath(folder, newPath))
	if err != nil {
		return "check folder storage: read the marker's file number"
	}
	if marker != record.Marker {
		return folderStorageMismatch
	}
	return ""
}
