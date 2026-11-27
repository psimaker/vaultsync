// Which directory a folder synced in (#315, decision 063).
package bridge

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/protocol"
)

// The marker says which folder a directory belongs to, and a copy made by
// hand carries it (#315). What a copy cannot carry is the marker's file
// number: a copy is a new file, a directory that was moved or whose parent
// was renamed holds the same one. So while a folder's directory is there the
// bridge writes down the file number of its marker and of the `.stfolder`
// that holds it, a folder whose directory is gone is re-pointed only at the
// directory that has that marker, and the engine syncs a folder only while
// the `.stfolder` at its path is that one (#329). No content is read, so
// whatever changed in that directory while the engine did not look goes out
// as the change it is.
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
	// Stfolder is the file number of the directory that holds the marker,
	// read through one handle with Marker (#329, decision 084); 0 while that
	// directory has not been seen since the record took this shape.
	Stfolder uint64 `json:"stfolder,omitempty"`
	// Strict is set in every record this version writes: the engine's check
	// holds the folder to Stfolder, or to Marker while Stfolder is 0. A
	// record without it was written by VaultSync 3.0.x, or by a version that
	// does not know these fields and dropped them on its next write; the
	// engine's check holds it to nothing, and the first reconcile writes it
	// in this shape (decision 084, "The update").
	Strict bool `json:"strict,omitempty"`
}

// folderStorageKeyPrefix names the records in the engine's key-value store.
// The 1 is the format's version. Stfolder and Strict joined it without a new
// version: a reader that does not know them reads the rest as it always did
// (VaultSync 3.0.1 unmarshals into a type with Marker, Index and Anchor only).
const folderStorageKeyPrefix = "vaultsync/folder-storage/1/"

// markerReading is what markerNumbers read: the file number of the
// directory that holds a folder's marker, and the marker's, read through one
// handle of that directory. markerErr is the marker's own read; it leaves
// the directory's number standing.
type markerReading struct {
	dir       uint64
	marker    uint64
	markerErr error
}

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

// NoteFolderStorage writes down, for every configured folder, what its record
// must say (#329, decision 084): a folder whose directory is there with its
// marker and no record that applies gets one; a record written by 3.0.x takes
// this version's shape, whatever the folder's state; a record whose `.stfolder`
// was not seen yet gets its number once the marker there is the recorded one;
// and a marker file made anew inside the recorded `.stfolder` gets its new
// number. A folder with another directory in its place keeps what it has: the
// record names the directory the folder synced in.
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

// folderMarkers is what one pass over the configured folders read: for each
// folder, the file numbers of its marker's directory and of the marker in it
// (markerErr set where the marker could not be read), or why that directory
// could not be opened; and which configuration they were read for.
type folderMarkers struct {
	commits  uint64
	folders  map[string]config.FolderConfiguration
	readings map[string]markerReading
	errs     map[string]error
}

func readFolderMarkers() (folderMarkers, bool) {
	mu.Lock()
	if !engineUpLocked() || stCfg == nil || stStorageAnchor == 0 {
		mu.Unlock()
		return folderMarkers{}, false
	}
	seen := folderMarkers{
		commits:  stConfigCommits,
		folders:  stCfg.Folders(),
		readings: make(map[string]markerReading),
		errs:     make(map[string]error),
	}
	mu.Unlock()

	for id, folder := range seen.folders {
		if reading, err := markerNumbers(folderMarkerPath(folder, folder.Path)); err == nil {
			seen.readings[id] = reading
		} else {
			seen.errs[id] = err
		}
	}
	return seen, true
}

// folderNotThere says that a read of the folder's marker directory found no
// directory to hold the folder to: the path or `.stfolder` is missing, a
// link or a file stands in its place, or the app may not reach it. Any other
// error may pass by itself, and a record waits for a read that answers.
func folderNotThere(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) ||
		errors.Is(err, syscall.ELOOP) || errors.Is(err, fs.ErrPermission)
}

// record writes what was read, unless the configuration has changed since:
// a folder that was removed, added again or moved meanwhile is another
// folder than the one whose marker was read, and the call that changed it
// has noted it itself. For each folder:
//
//   - No record that applies, and its directory there with its marker: the
//     directory is recorded (063).
//   - A record from 3.0.x: this pass writes it in this version's shape, for
//     every folder, and the engine's check holds the folder to it from then
//     on (decision 084, "The update"). Its directory there with the recorded
//     marker: `.stfolder`'s number is added. There with another marker, or
//     without one: nothing on the iPhone tells a copy put in place before
//     the update from the folder's own directory, and what is there is
//     recorded, `.stfolder`'s number with the marker there or, without one,
//     the recorded marker; 3.0.x would have taken it the same way. Not
//     there: the record keeps its marker and waits for `.stfolder`; until
//     then the folder is held to that marker.
//   - A record waiting for `.stfolder`, and the recorded marker there: the
//     number of the directory that holds it is added.
//   - The recorded `.stfolder` there, with a marker file made anew in it:
//     only the marker's number is written. The repair (SetFolderPath)
//     compares markers, and a marker read inside that directory is still the
//     marker of the directory the folder synced in.
//   - Another directory in the folder's place, or what the record already
//     says: nothing. The engine refuses the other directory
//     (folderDirectoryCheck), and writing it down would let the next scan
//     take it.
//
// What cannot be read is not written over. The engine's check never writes:
// a record changes here, and with an add, an accept or a re-point.
func (m folderMarkers) record() {
	mu.Lock()
	defer mu.Unlock()
	if !engineUpLocked() || stConfigCommits != m.commits {
		return
	}
	if stDB == nil {
		return
	}
	for id, folder := range m.folders {
		reading, read := m.readings[id]
		marked := read && reading.markerErr == nil
		existing, applies, unread := applyingRecord(stDB, stStorageAnchor, stStorageAnchorErr, id)
		switch {
		case unread != "":
		case !applies:
			if marked {
				writeFolderStorageLocked(folder, reading.marker, reading.dir)
			}
		case !existing.Strict:
			switch {
			case marked:
				writeFolderStorageLocked(folder, reading.marker, reading.dir)
			case read:
				writeFolderStorageLocked(folder, existing.Marker, reading.dir)
			case folderNotThere(m.errs[id]):
				writeFolderStorageLocked(folder, existing.Marker, 0)
			}
		case existing.Stfolder == 0:
			if marked && reading.marker == existing.Marker {
				writeFolderStorageLocked(folder, reading.marker, reading.dir)
			}
		case marked && reading.dir == existing.Stfolder && reading.marker != existing.Marker:
			writeFolderStorageLocked(folder, reading.marker, reading.dir)
		}
	}
}

// noteFolderStorageLocked writes down the directory a folder syncs in, if its
// marker is there to be read: for the calls that have just put a folder at a
// path — added, accepted, re-pointed — and hold the lock. It also takes the
// folder's local index ID for the engine's check, which does not ask the
// engine for it. Caller holds mu.
func noteFolderStorageLocked(folder config.FolderConfiguration) {
	if stDB != nil && stIndexes != nil {
		if index, err := stDB.GetIndexID(folder.ID, protocol.LocalDeviceID); err == nil {
			stIndexes.set(folder.ID, uint64(index))
		}
	}
	if stStorageAnchor == 0 {
		return
	}
	if reading, err := markerNumbers(folderMarkerPath(folder, folder.Path)); err == nil && reading.markerErr == nil {
		writeFolderStorageLocked(folder, reading.marker, reading.dir)
	}
}

// writeFolderStorageLocked records marker and stfolder — the file numbers of
// the folder's marker and of the directory that holds it, 0 where that
// directory was not seen — for the index and the anchor the engine has now,
// in this version's shape. Caller holds mu.
func writeFolderStorageLocked(folder config.FolderConfiguration, marker, stfolder uint64) {
	if stDB == nil || stStorageAnchor == 0 {
		return
	}
	index, err := stDB.GetIndexID(folder.ID, protocol.LocalDeviceID)
	if err != nil {
		return
	}
	if stIndexes != nil {
		stIndexes.set(folder.ID, uint64(index))
	}
	record := folderStorageRecord{Marker: marker, Index: uint64(index), Anchor: stStorageAnchor, Stfolder: stfolder, Strict: true}
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
// not be read, and then nothing is known. It never carries a path: it ends
// up in the folder's error and in refusals the app shows (safety rules 6 and
// 10), and the anchor's error names the app's configuration directory.
func applyingRecord(store storageRecords, anchor uint64, anchorErr error, folderID string) (record folderStorageRecord, applies bool, unread string) {
	record, found, err := readFolderStorage(store, folderID)
	if err != nil {
		return folderStorageRecord{}, false, fmt.Sprintf("read the record: %v", withoutPath(err))
	}
	if !found {
		return folderStorageRecord{}, false, ""
	}
	if anchorErr != nil {
		return folderStorageRecord{}, false, fmt.Sprintf("read the anchor: %v", withoutPath(anchorErr))
	}
	if record.Anchor != anchor {
		return folderStorageRecord{}, false, ""
	}
	index, err := store.GetIndexID(folderID, protocol.LocalDeviceID)
	if err != nil {
		return folderStorageRecord{}, false, fmt.Sprintf("read the index ID: %v", withoutPath(err))
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
	// storageOther: a record applies, and the directory has another marker
	// (folderStorageVerdict) or another directory holding its marker
	// (liveDirectoryVerdict). A directory made from the folder's own, a copy.
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
func readFolderStorage(store recordReader, folderID string) (folderStorageRecord, bool, error) {
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
//
// The engine's question is about the directory that holds the marker, not
// the marker itself: a marker file made anew or gone inside the folder's own
// `.stfolder` does not make the folder a copy, and a copy carries a
// `.stfolder` of its own whatever marker it holds. The repair keeps asking
// the marker (folderStorageVerdict), for every record.

// folderNotItsDirectory is the engine's refusal of a folder whose path holds
// another directory than the one the folder synced in: a copy, most likely.
const folderNotItsDirectory = "folder directory is not the directory this folder synced in"

// folderDirectoryUnchecked begins the engine's refusal of a folder whose
// directory could not be held against its record because a read failed.
// That is no finding about the directory — nobody is told it is a copy —
// and the next CheckPath asks again.
const folderDirectoryUnchecked = "folder directory could not be checked"

var errFolderNotItsDirectory = errors.New(folderNotItsDirectory)

// folderDirectoryCheck answers the engine for one engine run. It reads, and
// never writes: not the record, not the anchor, not the engine's database.
// What needs making is made at the start or in the reconcile.
type folderDirectoryCheck struct {
	store   recordReader
	anchor  *anchorReading
	indexes *indexSnapshot
}

// recordReader is all the check asks of the engine's database: a read of the
// key-value store, which makes nothing.
type recordReader interface {
	GetKV(key string) ([]byte, error)
}

// anchorReading is the anchor as one engine run's check knows it: what the
// start read, and read again for as long as a read has failed. A read that
// failed once must not stop every folder with a record for the rest of the
// run; the next check asks again (decision 084). The read again never makes
// the file (peekStorageAnchor).
type anchorReading struct {
	read func() (uint64, error)

	mu     sync.Mutex
	number uint64
	err    error
}

// get returns the anchor's file number, reading it again while the last
// read failed.
func (a *anchorReading) get() (uint64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		a.number, a.err = a.read()
	}
	return a.number, a.err
}

// peekStorageAnchor is the check's read of the anchor: its file number,
// without making the file. Where the anchor is gone, no record applies — as
// at the next start, which makes a new one, or after a restore — so the
// check holds no folder to a record then. A variable, so a test can make the
// read fail.
var peekStorageAnchor = func(configDir string) (uint64, error) {
	number, err := fileNumber(filepath.Join(configDir, storageAnchorName))
	if os.IsNotExist(err) || errors.Is(err, errNoFileNumbers) {
		return 0, nil
	}
	return number, err
}

// indexSnapshot is each folder's local index ID as the bridge last took it
// from the engine: at the start, and with every add, accept, re-point and
// record write. The check reads it here, because the engine's read makes an
// index ID where there is none. During a run the engine gives a running
// folder no new index ID; a folder removed and added again gets one, and its
// add takes it here. A folder the snapshot does not know has no record that
// applies, as far as the check can tell without writing.
type indexSnapshot struct {
	mu  sync.RWMutex
	ids map[string]uint64
}

func (s *indexSnapshot) get(folderID string) (uint64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	index, ok := s.ids[folderID]
	return index, ok
}

func (s *indexSnapshot) set(folderID string, index uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids[folderID] = index
}

// stIndexes is the running engine's snapshot. The pointer is guarded by mu.
var stIndexes *indexSnapshot

// answer is what CheckPath returns once the engine has found the folder's
// directory and its marker entry. It takes no bridge lock: the engine asks
// while the bridge may hold mu and wait for the engine.
func (c folderDirectoryCheck) answer(folder config.FolderConfiguration) error {
	switch verdict, unread := liveDirectoryVerdict(c.store, c.anchor.get, c.indexes, folder); verdict {
	case storageOther:
		return errFolderNotItsDirectory
	case storageUnread:
		return fmt.Errorf("%s: %s", folderDirectoryUnchecked, unread)
	default:
		return nil
	}
}

// liveDirectoryVerdict is the engine's question about a folder it is about
// to scan or pull at the path it has: is the directory there the one the
// record names? The record is read first, so a read that could only matter
// to a record that holds the folder to something is made only for such one.
//
//   - No record, or one from 3.0.x: nothing to hold the folder to, and it
//     goes on as before, also when the anchor cannot be read. The first
//     reconcile writes such a record in this version's shape.
//   - A record for another anchor or another index: it says nothing.
//   - A record with `.stfolder`'s number, and the `.stfolder` at the path
//     has it: the folder's own, whatever its marker file is now — made anew
//     or missing. The record's marker follows at the next reconcile.
//   - A record waiting for `.stfolder` (the folder was not there when the
//     record took this shape): held to its marker — the recorded marker at
//     the path passes, and anything else there is refused.
//   - Another `.stfolder`, or a link or a file in its place: not the
//     directory the folder synced in. A copy carries its own `.stfolder`, so
//     a marker whose number was carried into it changes nothing here. (The
//     repair still asks the marker alone: folderStorageVerdict.)
//   - What cannot be read refuses (039).
func liveDirectoryVerdict(store recordReader, anchorOf func() (uint64, error), indexes *indexSnapshot, folder config.FolderConfiguration) (verdict storageVerdict, unread string) {
	record, found, err := readFolderStorage(store, folder.ID)
	if err != nil {
		return storageUnread, fmt.Sprintf("read the record: %v", withoutPath(err))
	}
	if !found || !record.Strict {
		return storageUnknown, ""
	}
	anchor, err := anchorOf()
	if err != nil {
		return storageUnread, fmt.Sprintf("read the anchor: %v", withoutPath(err))
	}
	if record.Anchor != anchor {
		return storageUnknown, ""
	}
	if index, known := indexes.get(folder.ID); !known || record.Index != index {
		return storageUnknown, ""
	}

	reading, err := markerNumbers(folderMarkerPath(folder, folder.Path))
	switch {
	case errors.Is(err, syscall.ELOOP), errors.Is(err, syscall.ENOTDIR):
		return storageOther, ""
	case err != nil:
		return storageUnread, "read the file number of the marker's directory"
	}
	if record.Stfolder != 0 {
		if reading.dir != record.Stfolder {
			return storageOther, ""
		}
		return storageOwn, ""
	}
	switch {
	case errors.Is(reading.markerErr, fs.ErrNotExist):
		return storageOther, ""
	case reading.markerErr != nil:
		return storageUnread, "read the marker's file number"
	case reading.marker != record.Marker:
		return storageOther, ""
	default:
		return storageOwn, ""
	}
}

// observeDirectoryCheck, when set before an engine starts, is told every
// answer that engine's check gives, and how long it took. For tests.
var observeDirectoryCheck func(folderID string, err error, took time.Duration)

// installDirectoryCheckLocked makes the engine's CheckPath ask the records in
// store, for an engine that is about to start, and returns what to remove
// once that engine has stopped, and the index snapshot the bridge keeps for
// it. anchor and anchorErr are what the start read in configDir. The
// snapshot takes the local index ID of every folder whose record holds it to
// something, now, before the engine starts: the start may make what the
// check must not. Caller holds mu.
func installDirectoryCheckLocked(store storageRecords, configDir string, anchor uint64, anchorErr error) (*func(config.FolderConfiguration) error, *indexSnapshot) {
	indexes := &indexSnapshot{ids: make(map[string]uint64)}
	if stCfg != nil {
		for id := range stCfg.Folders() {
			if record, found, err := readFolderStorage(store, id); err == nil && found && record.Strict {
				if index, err := store.GetIndexID(id, protocol.LocalDeviceID); err == nil {
					indexes.set(id, uint64(index))
				}
			}
		}
	}
	// The read is taken now, under mu: a test that swaps peekStorageAnchor
	// does so between engine runs, never under a running check.
	peek := peekStorageAnchor
	reading := &anchorReading{
		read:   func() (uint64, error) { return peek(configDir) },
		number: anchor,
		err:    anchorErr,
	}
	check := folderDirectoryCheck{store: store, anchor: reading, indexes: indexes}
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
	return &ask, indexes
}

// removeDirectoryCheck removes installed, and only that: a check that a
// later start has installed meanwhile stays.
func removeDirectoryCheck(installed *func(config.FolderConfiguration) error) {
	if installed != nil {
		config.DirectoryCheck.CompareAndSwap(installed, nil)
	}
}
