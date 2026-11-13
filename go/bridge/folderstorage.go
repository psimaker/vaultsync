// The proof that a directory is a folder's storage (#315, decision 063).
package bridge

import (
	"context"
	"fmt"
	"iter"
	"path/filepath"
	"time"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/fs"
	"github.com/syncthing/syncthing/lib/osutil"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/scanner"
)

// folderStorageMismatch is how SetFolderPath refuses a directory that carries
// a folder's marker and does not hold what the folder last synced. It says
// nothing about which entry differs: names are user data.
const folderStorageMismatch = "target does not hold what this folder last synced"

// FolderStorageMismatchError returns the text SetFolderPath refuses a
// directory with that is not the folder's storage. The app tells this refusal
// from the others — its user gets the way out for an earlier copy — and reads
// the text here instead of keeping a copy of it.
func FolderStorageMismatchError() string {
	return folderStorageMismatch
}

// localIndex is what the proof reads from the engine's database: the entries
// this device recorded for a folder, and the modification times Syncthing
// keeps beside a filesystem that cannot hold them exactly (lib/fs asks for
// the three calls together; reading a directory entry uses GetMtime only).
type localIndex interface {
	AllLocalFiles(folder string, device protocol.DeviceID) (iter.Seq[protocol.FileInfo], func() error)
	GetMtime(folder, name string) (ondisk, virtual time.Time)
	PutMtime(folder, name string, ondisk, virtual time.Time) error
	DeleteMtime(folder, name string) error
}

// verifyFolderStorage returns "" if the directory at newPath agrees with what
// this device recorded for the folder, or an error message if it does not or
// if that cannot be read.
//
// A folder moved to another path goes on with its index, and its first scan
// there sends every difference to the peers as a change made on this device.
// So the directory has to hold what the index says, read the way the scanner
// reads it: every entry the index holds is there with its kind, size and
// modification time, and every entry the index holds as deleted is absent.
// Then that scan finds nothing to delete, nothing to replace and nothing to
// bring back. Two differences take nothing away and pass. A file the index
// has never seen goes out as a new file. A file whose time alone differs is
// read, and passes when its bytes are the recorded ones: the scan sends the
// other time and the same content. A restore from a backup can leave every
// file like that, with its time rounded.
//
// The directory the folder was last scanned in agrees by construction — moved
// as a whole, or under a container that changed its path (#25). A copy agrees
// for as long as nothing has synced since it was made, and continuing in it
// then changes nothing for anyone. A directory changed since the last scan is
// refused as well, the folder's own included: here an edit made in the vault
// cannot be told from an older copy of it, and the way on for both is to
// remove the vault and accept its share again, which merges and deletes
// nothing (decisions 002, 007).
//
// An index with no entries agrees with every directory: all that the scan
// finds there is new.
func verifyFolderStorage(folder config.FolderConfiguration, newPath string, index localIndex) string {
	if index == nil {
		return "check folder storage: the index is not available"
	}

	target := folder
	target.Path = newPath
	// The folder's own view of a directory: case conflicts as the engine
	// reports them, and the recorded modification times where it keeps any.
	storage := target.Filesystem(fs.NewMtimeOption(index, folder.ID))
	check := storageCheck{
		filesystem:    storage,
		scanOwnership: folder.SendOwnership || folder.SyncOwnership,
		scanXattrs:    folder.SendXattrs || folder.SyncXattrs,
		xattrFilter:   folder.XattrFilter,
		parents:       make(map[string]bool),
	}
	// The comparison of the scanner (lib/scanner, walkRegular and walkDir),
	// with every local flag masked: the flags that matter are read in holds.
	check.comparison = protocol.FileInfoComparison{
		ModTimeWindow:   target.ModTimeWindow(),
		IgnorePerms:     folder.IgnorePerms,
		IgnoreBlocks:    true,
		IgnoreFlags:     protocol.LocalAllFlags,
		IgnoreOwnership: !check.scanOwnership,
		IgnoreXattrs:    !check.scanXattrs,
	}

	agrees := true
	entries, readErr := index.AllLocalFiles(folder.ID, protocol.LocalDeviceID)
	for recorded := range entries {
		if !check.holds(recorded) {
			agrees = false
			break
		}
	}
	// A read that failed midway has shown only that the entries before it
	// agree. That is not the answer (decision 039).
	if err := readErr(); err != nil {
		return fmt.Sprintf("check folder storage: read the index: %v", err)
	}
	if !agrees {
		return folderStorageMismatch
	}
	return ""
}

// storageCheck compares index entries with one directory.
type storageCheck struct {
	filesystem    fs.Filesystem
	comparison    protocol.FileInfoComparison
	scanOwnership bool
	scanXattrs    bool
	xattrFilter   scanner.XattrFilter
	// parents remembers, per directory, whether the way to it is free of
	// symlinks, so a directory full of notes is walked up once.
	parents map[string]bool
}

// holds reports whether the directory has the entry as the index recorded it.
// Whatever cannot be read counts as a difference.
func (c *storageCheck) holds(recorded protocol.FileInfo) bool {
	switch {
	case recorded.IsIgnored() || recorded.IsUnsupported():
		// Not part of what the folder sends: the scan leaves it alone.
		return true
	case recorded.MustRescan():
		// The index itself says it does not know what is on disk here. The
		// scan will take whatever it finds for the new version.
		return false
	case recorded.IsDeleted():
		// A deleted entry that is there again would go out as a new file and
		// bring back on every device what was deleted.
		return osutil.IsDeleted(c.filesystem, recorded.Name)
	}

	if !c.reachable(filepath.Dir(recorded.Name)) {
		return false
	}
	info, err := c.filesystem.Lstat(recorded.Name)
	if err != nil {
		return false
	}
	onDisk, err := scanner.CreateFileInfo(info, recorded.Name, c.filesystem, c.scanOwnership, c.scanXattrs, c.xattrFilter)
	if err != nil {
		return false
	}
	if recorded.IsEquivalentOptional(onDisk, c.comparison) {
		return true
	}

	// Not what the scanner would pass over. One difference is still no loss:
	// a file that differs in its time and in nothing else the scanner
	// compares, and holds the recorded bytes.
	if recorded.Type != protocol.FileInfoTypeFile || onDisk.Type != protocol.FileInfoTypeFile {
		return false
	}
	retimed := onDisk
	retimed.ModifiedS, retimed.ModifiedNs = recorded.ModifiedS, recorded.ModifiedNs
	if !recorded.IsEquivalentOptional(retimed, c.comparison) {
		return false
	}
	return c.holdsContent(recorded)
}

// holdsContent reports whether the file in the directory has the bytes the
// index recorded for it, block by block. An entry without recorded blocks
// proves nothing and counts as a difference.
func (c *storageCheck) holdsContent(recorded protocol.FileInfo) bool {
	if len(recorded.Blocks) == 0 {
		return false
	}
	file, err := c.filesystem.Open(recorded.Name)
	if err != nil {
		return false
	}
	defer file.Close()
	blocks, err := scanner.Blocks(context.Background(), file, recorded.BlockSize(), recorded.Size, nil)
	if err != nil {
		return false
	}
	return recorded.BlocksEqual(protocol.FileInfo{Blocks: blocks})
}

// reachable reports whether dir is reached through directories only. The
// scanner does not follow a symlink, so what lies behind one is deleted to it.
func (c *storageCheck) reachable(dir string) bool {
	if ok, known := c.parents[dir]; known {
		return ok
	}
	ok := osutil.TraversesSymlink(c.filesystem, dir) == nil
	c.parents[dir] = ok
	return ok
}
