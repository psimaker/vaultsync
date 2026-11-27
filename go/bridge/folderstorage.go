// Which directory a folder synced in (#315, decision 063).
package bridge

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/protocol"
)

// The marker says which folder a directory belongs to, and a copy made by
// hand carries it (#315). What a copy cannot carry is the marker's file
// number: a copy is a new file, a directory that was moved or whose parent
// was renamed holds the same one. So while a folder's directory is there the
// bridge writes down the file number of its marker, a folder whose directory
// is gone is re-pointed only at the directory that has it, and the engine
// syncs a folder only while the directory at its path has it (#329). No
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
//
// The engine must not give a folder a new index ID while it keeps the
// folder's index: every record of a folder whose directory is gone would
// stop applying at that moment. No version in use does; a test holds the
// engine's database migrations to the ones that were read for this.

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

// errNoFileNumbers is fileNumber's answer where the file system has none to
// give. That is no failed read: without file numbers there are no records.
var errNoFileNumbers = errors.New("no file numbers on this file system")

// stStorageAnchor is the file number of this install's anchor, read when the
// engine started; 0 where there are no file numbers, and then nothing is
// recorded and no record applies. stStorageAnchorErr says that the anchor
// could not be read: nothing is recorded then either, and a folder that has
// a record is not re-pointed until the anchor reads again (decision 039).
// Both guarded by mu.
var (
	stStorageAnchor    uint64
	stStorageAnchorErr error
)

// stDirectoryCheck is what the running engine's CheckPath asks of the
// records (#329), removed once that engine has stopped. Guarded by mu.
var stDirectoryCheck *func(config.FolderConfiguration) error

// storageRecords is the part of the engine's database the records need.
type storageRecords interface {
	GetKV(key string) ([]byte, error)
	PutKV(key string, val []byte) error
	GetIndexID(folder string, device protocol.DeviceID) (protocol.IndexID, error)
}

// readStorageAnchor is ensureStorageAnchor. A variable, so a test can make
// the read fail.
var readStorageAnchor = ensureStorageAnchor

// ensureStorageAnchor returns the file number of the anchor in configDir and
// creates the file if it is not there. The file is looked at before it is
// created: an install that has its anchor must not lose its records to a
// create that fails for a reason of its own.
func ensureStorageAnchor(configDir string) (uint64, error) {
	path := filepath.Join(configDir, storageAnchorName)
	number, err := fileNumber(path)
	if os.IsNotExist(err) {
		file, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if createErr == nil {
			file.Close()
		} else if !os.IsExist(createErr) {
			return 0, createErr
		}
		number, err = fileNumber(path)
	}
	if errors.Is(err, errNoFileNumbers) {
		return 0, nil
	}
	return number, err
}

// NoteFolderStorage writes down, for every folder whose directory is there
// with its marker and that has no record that applies, which directory the
// folder syncs in. A folder whose directory is gone keeps the record it has:
// that record is what its directory is known by when it shows up again. And
// so does a folder whose record applies: the record names the directory the
// folder synced in, also when another directory has been put in its place
// since (#329, decision 084).
//
// The app calls this at the start of every path reconcile — after an engine
// start, after another folder was selected, in a background run — so a
// folder gets its record at the first moment its directory can be reached,
// and before the reconcile decides anything.
//
// The markers are read without the bridge lock. These are paths the user
// chose, and a file system that takes its time there must not hold up every
// other call.
func NoteFolderStorage() {
	if seen, ok := readFolderMarkers(); ok {
		seen.record()
	}
}

// folderMarkers is what one pass over the configured folders read: the file
// number of each marker that was there, and which configuration it was read
// for.
type folderMarkers struct {
	commits uint64
	folders map[string]config.FolderConfiguration
	numbers map[string]uint64
}

func readFolderMarkers() (folderMarkers, bool) {
	mu.Lock()
	if !engineUpLocked() || stCfg == nil || stStorageAnchor == 0 {
		mu.Unlock()
		return folderMarkers{}, false
	}
	seen := folderMarkers{commits: stConfigCommits, folders: stCfg.Folders(), numbers: make(map[string]uint64)}
	mu.Unlock()

	for id, folder := range seen.folders {
		if number, err := fileNumber(folderMarkerPath(folder, folder.Path)); err == nil {
			seen.numbers[id] = number
		}
	}
	return seen, true
}

// record writes a record for each folder that has none that applies, unless
// the configuration has changed since the markers were read: a folder that
// was removed, added again or moved meanwhile is another folder than the one
// whose marker was read, and the call that changed it has noted it itself.
//
// A record that applies is never written over here. It names the directory
// the folder synced in, and a live folder whose marker has another number
// now has another directory in its place — a copy, most likely (#329). The
// engine refuses that directory (folderDirectoryCheck), and writing the new
// number down would let the next scan take it. A record that applies
// changes only with an act on the folder: an add, an accept, a re-point
// (decision 084). What cannot be read is not written over either.
func (m folderMarkers) record() {
	mu.Lock()
	defer mu.Unlock()
	if !engineUpLocked() || stConfigCommits != m.commits {
		return
	}
	if stDB == nil {
		return
	}
	for id, number := range m.numbers {
		folder := m.folders[id]
		if _, applies, unread := applyingRecord(stDB, stStorageAnchor, stStorageAnchorErr, id); applies || unread != "" {
			continue
		}
		writeFolderStorageLocked(folder, number)
	}
}

// noteFolderStorageLocked writes down the directory a folder syncs in, if its
// marker is there to be read: for the calls that have just put a folder at a
// path — added, accepted, re-pointed — and hold the lock. Caller holds mu.
func noteFolderStorageLocked(folder config.FolderConfiguration) {
	if stStorageAnchor == 0 {
		return
	}
	if marker, err := fileNumber(folderMarkerPath(folder, folder.Path)); err == nil {
		writeFolderStorageLocked(folder, marker)
	}
}

// writeFolderStorageLocked records marker as the file number of the folder's
// marker, for the index and the anchor the engine has now. Caller holds mu.
func writeFolderStorageLocked(folder config.FolderConfiguration, marker uint64) {
	if stDB == nil || stStorageAnchor == 0 {
		return
	}
	index, err := stDB.GetIndexID(folder.ID, protocol.LocalDeviceID)
	if err != nil {
		return
	}
	record := folderStorageRecord{Marker: marker, Index: uint64(index), Anchor: stStorageAnchor}
	if existing, found, err := readFolderStorage(stDB, folder.ID); err == nil && found && existing == record {
		return
	}
	value, err := json.Marshal(record)
	if err != nil {
		return
	}
	_ = stDB.PutKV(folderStorageKeyPrefix+folder.ID, value)
}

// applyingRecord returns the folder's record if it applies to the folder as
// the engine has it now: written for this install's anchor and for the
// folder's current index. A record with another anchor or another index ID
// says nothing about any directory (decision 063). unread names what could
// not be read, without a path, and then nothing is known.
func applyingRecord(store storageRecords, anchor uint64, anchorErr error, folderID string) (record folderStorageRecord, applies bool, unread string) {
	record, found, err := readFolderStorage(store, folderID)
	if err != nil {
		return folderStorageRecord{}, false, fmt.Sprintf("read the record: %v", err)
	}
	if !found {
		return folderStorageRecord{}, false, ""
	}
	if anchorErr != nil {
		return folderStorageRecord{}, false, fmt.Sprintf("read the anchor: %v", anchorErr)
	}
	if record.Anchor != anchor {
		return folderStorageRecord{}, false, ""
	}
	index, err := store.GetIndexID(folderID, protocol.LocalDeviceID)
	if err != nil {
		return folderStorageRecord{}, false, fmt.Sprintf("read the index ID: %v", err)
	}
	if record.Index != uint64(index) {
		return folderStorageRecord{}, false, ""
	}
	return record, true, ""
}

// storageVerdict is what the records know of a directory offered as a
// folder's.
type storageVerdict int

const (
	// storageUnknown: no record applies, and the marker decides as it did
	// before there were records.
	storageUnknown storageVerdict = iota
	// storageOwn: a record applies, and the directory's marker has the
	// recorded file number. The directory the folder synced in.
	storageOwn
	// storageOther: a record applies, and the directory's marker has another
	// file number. A directory made from the folder's own, a copy.
	storageOther
	// storageNoMarker: a record applies, and the directory holds no marker
	// file for the folder. Not the directory the folder synced in, which had
	// one.
	storageNoMarker
	// storageUnread: something the verdict rests on could not be read
	// (decision 039). Nothing is known, and that is not taken for unknown.
	storageUnread
)

// folderStorageVerdict holds the directory at path against the folder's
// record by the rule of decision 063, the file number of the folder's
// marker. No content is read. For storageUnread, unread names what could not
// be read, without a path.
func folderStorageVerdict(store storageRecords, anchor uint64, anchorErr error, folder config.FolderConfiguration, path string) (verdict storageVerdict, unread string) {
	record, applies, unread := applyingRecord(store, anchor, anchorErr, folder.ID)
	if unread != "" {
		return storageUnread, unread
	}
	if !applies {
		return storageUnknown, ""
	}
	marker, err := fileNumber(folderMarkerPath(folder, path))
	switch {
	case os.IsNotExist(err):
		return storageNoMarker, ""
	case err != nil:
		return storageUnread, "read the marker's file number"
	case marker != record.Marker:
		return storageOther, ""
	default:
		return storageOwn, ""
	}
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
// of the record, of the anchor, of the index ID or of the marker's file
// number refuses.
func folderStorageRefusalLocked(folder config.FolderConfiguration, newPath string) string {
	if stDB == nil {
		return ""
	}
	switch verdict, unread := folderStorageVerdict(stDB, stStorageAnchor, stStorageAnchorErr, folder, newPath); verdict {
	case storageOther:
		return folderStorageMismatch
	case storageNoMarker:
		// The caller found the marker a moment ago.
		return "check folder storage: read the marker's file number"
	case storageUnread:
		return "check folder storage: " + unread
	default:
		return ""
	}
}

// The engine's side (#329, decision 084). A copy put in a folder's own
// place, under the folder's own path, needs no re-point: the engine finds a
// directory with the marker there and scans it, and every difference goes
// out as a change made on this device — what the copy lacks as deletions,
// its older notes as the current versions. So the engine's CheckPath, which
// every scan, pull, commit of scan results and write of the ignores asks
// first, asks the records too (fork patch 007): a folder whose path holds
// another directory than the one its record names stays in error, and
// nothing in that directory reaches another device. Nothing is paused,
// moved or re-pointed, and the folder goes on by itself once its own
// directory is back in its place.

// folderNotItsDirectory is the engine's refusal of a folder whose path holds
// another directory than the one the folder synced in: a copy, most likely.
const folderNotItsDirectory = "folder directory is not the directory this folder synced in"

// folderDirectoryUnchecked begins the engine's refusal of a folder whose
// directory could not be held against its record because a read failed.
// That is no finding about the directory — nobody is told it is a copy —
// and the next CheckPath asks again.
const folderDirectoryUnchecked = "folder directory could not be checked"

var errFolderNotItsDirectory = errors.New(folderNotItsDirectory)

// folderDirectoryCheck answers the engine for one engine run, with the
// database and the anchor of that run.
type folderDirectoryCheck struct {
	store     storageRecords
	anchor    uint64
	anchorErr error
}

// answer is what CheckPath returns once the engine has found the folder's
// directory and its marker entry. It takes no bridge lock: the engine asks
// while the bridge may hold mu and wait for the engine.
func (c folderDirectoryCheck) answer(folder config.FolderConfiguration) error {
	switch verdict, unread := folderStorageVerdict(c.store, c.anchor, c.anchorErr, folder, folder.Path); verdict {
	case storageOther, storageNoMarker:
		return errFolderNotItsDirectory
	case storageUnread:
		return fmt.Errorf("%s: %s", folderDirectoryUnchecked, unread)
	default:
		return nil
	}
}

// observeDirectoryCheck, when set before an engine starts, is told every
// answer that engine's check gives, and how long it took. For tests.
var observeDirectoryCheck func(folderID string, err error, took time.Duration)

// installDirectoryCheckLocked makes the engine's CheckPath ask the records in
// store, for an engine that is about to start, and returns what to remove
// once that engine has stopped. Caller holds mu.
func installDirectoryCheckLocked(store storageRecords, anchor uint64, anchorErr error) *func(config.FolderConfiguration) error {
	check := folderDirectoryCheck{store: store, anchor: anchor, anchorErr: anchorErr}
	ask := check.answer
	if observe := observeDirectoryCheck; observe != nil {
		ask = func(folder config.FolderConfiguration) error {
			began := time.Now()
			err := check.answer(folder)
			observe(folder.ID, err, time.Since(began))
			return err
		}
	}
	config.DirectoryCheck.Store(&ask)
	return &ask
}

// removeDirectoryCheck removes installed, and only that: a check that a
// later start has installed meanwhile stays.
func removeDirectoryCheck(installed *func(config.FolderConfiguration) error) {
	if installed != nil {
		config.DirectoryCheck.CompareAndSwap(installed, nil)
	}
}
