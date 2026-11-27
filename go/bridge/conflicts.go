// Conflict file detection, content reading, and resolution.
// Syncthing creates conflict copies with the pattern:
//
//	<name>.sync-conflict-<YYYYMMDD>-<HHMMSS>-<shortID>.<ext>
//
// Everything here works on regular files inside the folder's own directory
// (#151, decision 095). The folder is opened as a root, every directory on a
// path is opened one at a time and must be a directory, and the entry that is
// read or acted on must be a regular file. Each read, write, rename and
// removal is done by name in the directory that was checked. An entry that is
// refused is left as it is.
package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	// errPathTraversal is returned when a relative path attempts to escape the folder root.
	errPathTraversal = errors.New("path traversal outside folder root")

	errNoReplaceUnsupported = errors.New("atomic no-replace rename is not supported by this filesystem")

	// errNotRegularFile: the entry a path names is not a regular file, or is
	// no longer the one that was looked at.
	errNotRegularFile = errors.New("not a regular file")

	// errNotDirectory: a directory on a path is not a directory, or is no
	// longer the one that was looked at.
	errNotDirectory = errors.New("not a directory")
)

// The answers for a conflict entry that is not a regular file. They carry no
// path and no name.
const (
	conflictNotRegularError = "conflict file is not a regular file"
	originalNotRegularError = "original file is not a regular file"
)

// ConflictFile describes a single conflict copy found in a folder.
type ConflictFile struct {
	OriginalPath  string `json:"originalPath"`
	ConflictPath  string `json:"conflictPath"`
	ConflictDate  string `json:"conflictDate"`
	DeviceShortID string `json:"deviceShortID"`
}

// conflictPattern matches Syncthing conflict file names.
// Example: notes.sync-conflict-20260406-143022-ABC1234.md
var conflictPattern = regexp.MustCompile(`^(.+)\.sync-conflict-(\d{8}-\d{6})-([A-Z0-9]{7})(\..+)$`)

// maxConflictScan limits the number of files examined during conflict detection
// to prevent excessive I/O on very large vaults.
const maxConflictScan = 10000

const keepBothTargetExistsError = "keep both target already exists"

// Syncthing's fs.IsTemporary recognizes the .syncthing. prefix, so the scanner
// ignores these short-lived files instead of publishing them as vault content.
// Keep the pattern independent of the user filename to stay below conservative
// filesystem filename limits; os.CreateTemp appends the exclusive random suffix.
const conflictResolveTempPattern = ".syncthing.vaultsync-resolve-*"

type conflictTempFile interface {
	Name() string
	Write([]byte) (int, error)
	Chmod(os.FileMode) error
	Sync() error
	Close() error
}

// conflictFileOperations are the file operations of "keep the other
// version", each on a name in one directory of the folder.
type conflictFileOperations struct {
	readFile   func(name string) ([]byte, error)
	lstat      func(name string) (os.FileInfo, error)
	createTemp func(pattern string) (conflictTempFile, error)
	rename     func(oldName, newName string) error
	remove     func(name string) error
}

func systemConflictFileOperations(dir *os.Root) conflictFileOperations {
	return conflictFileOperations{
		readFile: func(name string) ([]byte, error) {
			return readRegularFile(dir, name)
		},
		lstat: func(name string) (os.FileInfo, error) {
			return lstatInFolder(dir, name)
		},
		createTemp: func(pattern string) (conflictTempFile, error) {
			return createTempIn(dir, pattern)
		},
		rename: dir.Rename,
		remove: dir.Remove,
	}
}

// keepBothFileOperations are the file operations of "keep both", each on a
// name in one directory of the folder.
type keepBothFileOperations struct {
	lstat           func(name string) (os.FileInfo, error)
	renameNoReplace func(oldName, newName string) error
}

func systemKeepBothFileOperations(dir *os.Root) keepBothFileOperations {
	return keepBothFileOperations{
		lstat: func(name string) (os.FileInfo, error) {
			return lstatInFolder(dir, name)
		},
		renameNoReplace: func(oldName, newName string) error {
			return renameNoReplaceIn(dir, oldName, newName)
		},
	}
}

// lstatInFolder is dir.Lstat: it looks at an entry itself, not at what a link
// there names. A variable, so a test can change an entry between the look
// and the open that follows it.
var lstatInFolder = func(dir *os.Root, name string) (os.FileInfo, error) {
	return dir.Lstat(name)
}

// openFolderEntry opens the directory that the entry at cleanedPath lies in,
// inside the folder at folderPath, and returns it with the entry's name, a
// single path element. cleanedPath is what safePath returned for it. The
// folder is opened as a root and each directory on the way is opened one at a
// time: it must be a directory, and the directory opened must be the one
// looked at. A path that names the folder itself has no entry and is refused
// as errNotRegularFile. The caller closes the directory.
func openFolderEntry(folderPath, cleanedPath string) (*os.Root, string, error) {
	rel, err := filepath.Rel(folderPath, cleanedPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, "", errNotRegularFile
	}
	dir, err := os.OpenRoot(folderPath)
	if err != nil {
		return nil, "", err
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		sub, err := openDirectoryIn(dir, part)
		dir.Close()
		if err != nil {
			return nil, "", err
		}
		dir = sub
	}
	return dir, parts[len(parts)-1], nil
}

// openDirectoryIn opens the directory name inside dir. A root follows a link
// whose target lies inside it, so the directory opened is held to the one
// looked at: a link, or a directory replaced by one in between, is refused.
func openDirectoryIn(dir *os.Root, name string) (*os.Root, error) {
	looked, err := lstatInFolder(dir, name)
	if err != nil {
		return nil, err
	}
	if !looked.IsDir() {
		return nil, errNotDirectory
	}
	sub, err := dir.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, err := sub.Stat(".")
	if err != nil {
		sub.Close()
		return nil, err
	}
	if !opened.IsDir() || !os.SameFile(looked, opened) {
		sub.Close()
		return nil, errNotDirectory
	}
	return sub, nil
}

// lstatRegularFile returns what name is in dir if it is a regular file.
func lstatRegularFile(dir *os.Root, name string) (os.FileInfo, error) {
	info, err := lstatInFolder(dir, name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errNotRegularFile
	}
	return info, nil
}

// openRegularFile opens name in dir for reading if it is a regular file. A
// root follows a link whose target lies inside it, so the file opened is held
// to the one looked at: an entry replaced by a link in between is refused.
func openRegularFile(dir *os.Root, name string) (*os.File, os.FileInfo, error) {
	looked, err := lstatRegularFile(dir, name)
	if err != nil {
		return nil, nil, err
	}
	file, err := dir.Open(name)
	if err != nil {
		return nil, nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(looked, opened) {
		file.Close()
		return nil, nil, errNotRegularFile
	}
	return file, opened, nil
}

func readRegularFile(dir *os.Root, name string) ([]byte, error) {
	file, _, err := openRegularFile(dir, name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

// createTempIn creates a new file in dir the way os.CreateTemp does: the
// pattern's last "*" becomes a random number, and a name that is taken is
// never opened (O_EXCL, which does not follow a link either).
func createTempIn(dir *os.Root, pattern string) (*os.File, error) {
	prefix, suffix := pattern, ""
	if i := strings.LastIndexByte(pattern, '*'); i >= 0 {
		prefix, suffix = pattern[:i], pattern[i+1:]
	}
	for try := 0; try < 10000; try++ {
		name := prefix + strconv.FormatUint(uint64(rand.Uint32()), 10) + suffix
		file, err := dir.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return file, err
	}
	return nil, os.ErrExist
}

// renameNoReplaceIn renames oldName to newName inside dir and fails when
// newName is taken. Both are single path elements, looked up from dir's
// handle, never from the folder's path again.
func renameNoReplaceIn(dir *os.Root, oldName, newName string) error {
	handle, err := dir.Open(".")
	if err != nil {
		return err
	}
	defer handle.Close()
	conn, err := handle.SyscallConn()
	if err != nil {
		return err
	}
	var renameErr error
	if err := conn.Control(func(fd uintptr) {
		renameErr = renameNoReplaceAt(int(fd), oldName, newName)
	}); err != nil {
		return err
	}
	return renameErr
}

// conflictEntryError is the answer of an action for a conflict entry it could
// not reach. It carries no path and no name.
func conflictEntryError(err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "conflict file not found"
	case errors.Is(err, errNotRegularFile), errors.Is(err, errNotDirectory):
		return conflictNotRegularError
	default:
		return fmt.Sprintf("inspect conflict file: %v", withoutPath(err))
	}
}

// GetConflictFilesJSON scans the folder's directory for .sync-conflict-* files.
// Returns a JSON array of ConflictFile objects. Stops after scanning maxConflictScan files.
// Only regular files are listed: the walk goes through the folder opened as a
// root and does not enter a link (#151).
func GetConflictFilesJSON(folderID string) string {
	folders := getFolderConfigs()
	if folders == nil {
		return "[]"
	}

	folder, exists := folders[folderID]
	if !exists {
		return "[]"
	}

	root, err := os.OpenRoot(folder.Path)
	if err != nil {
		return "[]"
	}
	defer root.Close()

	var conflicts []ConflictFile
	scanned := 0

	fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}

		scanned++
		if scanned > maxConflictScan {
			return filepath.SkipAll
		}

		name := d.Name()
		if !strings.Contains(name, ".sync-conflict-") {
			return nil
		}

		matches := conflictPattern.FindStringSubmatch(name)
		if matches == nil {
			return nil
		}
		// A conflict copy the app shows and acts on is a regular file.
		if !d.Type().IsRegular() {
			return nil
		}

		baseName := matches[1]
		date := matches[2]
		shortID := matches[3]
		ext := matches[4]

		relPath := filepath.FromSlash(path)
		dir := filepath.Dir(relPath)

		originalRel := baseName + ext
		if dir != "." {
			originalRel = filepath.Join(dir, originalRel)
		}

		conflicts = append(conflicts, ConflictFile{
			OriginalPath:  originalRel,
			ConflictPath:  relPath,
			ConflictDate:  date,
			DeviceShortID: shortID,
		})

		return nil
	})

	if conflicts == nil {
		conflicts = []ConflictFile{}
	}

	data, err := json.Marshal(conflicts)
	if err != nil {
		return "[]"
	}
	return string(data)
}

// safePath validates that relPath stays within folderRoot after cleaning.
// Returns the absolute cleaned path or an error if it escapes the root.
func safePath(folderRoot, relPath string) (string, error) {
	cleaned := filepath.Join(folderRoot, filepath.Clean(filepath.FromSlash(relPath)))
	// Ensure the result is still under folderRoot.
	if !strings.HasPrefix(cleaned, folderRoot+string(filepath.Separator)) && cleaned != folderRoot {
		return "", errPathTraversal
	}
	return cleaned, nil
}

// KeepBothConflict renames a conflict file so Syncthing no longer treats it as a conflict,
// preserving both the original and the conflict version as regular files.
// The conflict file is renamed from "name.sync-conflict-DATE-SHORTID.ext" to "name.conflict-SHORTID.ext".
// Returns empty string on success, error message on failure.
func KeepBothConflict(folderID, conflictFileName string) string {
	folders := getFolderConfigs()
	if folders == nil {
		return "syncthing not running"
	}

	folder, exists := folders[folderID]
	if !exists {
		return "folder not found"
	}

	conflictPath, err := safePath(folder.Path, conflictFileName)
	if err != nil {
		return "invalid path: outside folder root"
	}
	dir, name, err := openFolderEntry(folder.Path, conflictPath)
	if err != nil {
		return conflictEntryError(err)
	}
	defer dir.Close()
	return keepBothConflictFile(name, systemKeepBothFileOperations(dir))
}

// keepBothConflictFile renames the regular file conflictName to its keep-both
// name in the same directory, without replacing anything there.
func keepBothConflictFile(conflictName string, ops keepBothFileOperations) string {
	info, err := ops.lstat(conflictName)
	if errors.Is(err, os.ErrNotExist) {
		return "conflict file not found"
	}
	if err != nil {
		return fmt.Sprintf("inspect conflict file: %v", withoutPath(err))
	}
	if !info.Mode().IsRegular() {
		return conflictNotRegularError
	}
	matches := conflictPattern.FindStringSubmatch(conflictName)
	if matches == nil {
		return "invalid conflict filename"
	}

	// Build new name: baseName.conflict-shortID.ext
	newName := matches[1] + ".conflict-" + matches[3] + matches[4]

	if err := ops.renameNoReplace(conflictName, newName); err != nil {
		if errors.Is(err, os.ErrExist) {
			return keepBothTargetExistsError
		}
		if errors.Is(err, errNoReplaceUnsupported) {
			return errNoReplaceUnsupported.Error()
		}
		if errors.Is(err, os.ErrNotExist) {
			return "conflict file not found"
		}
		return fmt.Sprintf("rename conflict file without replacing target: %v", withoutPath(err))
	}

	return ""
}

// maxReadFileBytes bounds ReadFileContent (#184). A conflict note above it is
// not loaded at all: the app shows a "too large to compare" state and the
// user compares the versions in Obsidian. The app renders the same bound,
// read through MaxReadFileBytes, so the two layers never disagree.
const maxReadFileBytes int64 = 1 << 20

// MaxReadFileBytes returns the ReadFileContent bound in bytes (#184).
func MaxReadFileBytes() int64 {
	return maxReadFileBytes
}

// ReadFileContent reads a text file within a folder and returns its content.
// folderID identifies the Syncthing folder; relPath is relative to the folder root.
// Returns the file content on success (may be empty for an empty file).
// Returns a string prefixed with "error:" if the file cannot be read or the path is invalid,
// allowing callers to distinguish read errors from legitimately empty files.
// Only a regular file inside the folder is read (#151); the error carries no
// path and no name.
func ReadFileContent(folderID, relPath string) string {
	folders := getFolderConfigs()
	if folders == nil {
		return "error:syncthing not running"
	}
	folder, exists := folders[folderID]
	if !exists {
		return "error:folder not found"
	}
	absPath, err := safePath(folder.Path, relPath)
	if err != nil {
		return "error:invalid path"
	}
	dir, name, err := openFolderEntry(folder.Path, absPath)
	if err != nil {
		return readEntryError(err)
	}
	defer dir.Close()
	f, info, err := openRegularFile(dir, name)
	if err != nil {
		return readEntryError(err)
	}
	defer f.Close()
	// Refuse before reading: a note above the bound is never loaded into
	// memory, let alone handed across the bridge (#184).
	if info.Size() > maxReadFileBytes {
		return fmt.Sprintf("error:too large:%d", info.Size())
	}
	// Read at most one byte past the bound, so a file that grew since the
	// stat is still refused instead of loaded in full.
	data, err := io.ReadAll(io.LimitReader(f, maxReadFileBytes+1))
	if err != nil {
		return fmt.Sprintf("error:%v", withoutPath(err))
	}
	if int64(len(data)) > maxReadFileBytes {
		// The observed length is the lower bound: a file that grew and then
		// shrank again must not report a size at or below the limit.
		size := int64(len(data))
		if grown, statErr := f.Stat(); statErr == nil && grown.Size() > size {
			size = grown.Size()
		}
		return fmt.Sprintf("error:too large:%d", size)
	}
	return string(data)
}

// readEntryError is ReadFileContent's answer for an entry it could not open.
func readEntryError(err error) string {
	switch {
	case errors.Is(err, errNotRegularFile), errors.Is(err, errNotDirectory):
		return "error:not a regular file"
	default:
		return fmt.Sprintf("error:%v", withoutPath(err))
	}
}

// ResolveConflict resolves a sync conflict for a folder.
// conflictFileName is the relative path of the conflict file within the folder.
// If keepConflict is true, the conflict version replaces the original.
// If keepConflict is false, the conflict file is simply deleted.
// Returns empty string on success, error message on failure.
func ResolveConflict(folderID, conflictFileName string, keepConflict bool) string {
	folders := getFolderConfigs()
	if folders == nil {
		return "syncthing not running"
	}

	folder, exists := folders[folderID]
	if !exists {
		return "folder not found"
	}

	conflictPath, err := safePath(folder.Path, conflictFileName)
	if err != nil {
		return "invalid path: outside folder root"
	}
	dir, name, err := openFolderEntry(folder.Path, conflictPath)
	if err != nil {
		return conflictEntryError(err)
	}
	defer dir.Close()

	if _, err := lstatRegularFile(dir, name); err != nil {
		return conflictEntryError(err)
	}

	if keepConflict {
		matches := conflictPattern.FindStringSubmatch(name)
		if matches == nil {
			return "invalid conflict filename"
		}

		originalName := matches[1] + matches[4]

		if err := replaceConflictAndRemoveSource(name, originalName, systemConflictFileOperations(dir)); err != nil {
			if errors.Is(err, errNotRegularFile) {
				return conflictNotRegularError
			}
			if errors.Is(err, errOriginalNotRegular) {
				return originalNotRegularError
			}
			return err.Error()
		}
		return ""
	}

	if err := dir.Remove(name); err != nil {
		return fmt.Sprintf("delete conflict file: %v", withoutPath(err))
	}

	return ""
}

// errOriginalNotRegular: the original a conflict copy would replace is there
// and is not a regular file.
var errOriginalNotRegular = errors.New(originalNotRegularError)

// replaceConflictAndRemoveSource writes the conflict copy's bytes over the
// original and then removes the copy, both names in one directory.
func replaceConflictAndRemoveSource(conflictName, originalName string, ops conflictFileOperations) error {
	if err := replaceConflictOriginal(conflictName, originalName, ops); err != nil {
		return err
	}
	if err := ops.remove(conflictName); err != nil {
		return fmt.Errorf("delete conflict file: %w", withoutPath(err))
	}
	return nil
}

func replaceConflictOriginal(conflictName, originalName string, ops conflictFileOperations) error {
	data, err := ops.readFile(conflictName)
	if err != nil {
		return fmt.Errorf("read conflict file: %w", withoutPath(err))
	}

	// Preserve the current original's mode. A missing original is a supported
	// promotion case and keeps the historical 0644 default; every other stat
	// error is ambiguous and must fail before creating or changing anything,
	// and an original that is not a regular file is left as it is (#151).
	perm := os.FileMode(0o644)
	if info, statErr := ops.lstat(originalName); statErr == nil {
		if !info.Mode().IsRegular() {
			return errOriginalNotRegular
		}
		perm = info.Mode()
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("stat original file: %w", withoutPath(statErr))
	}

	tempFile, err := ops.createTemp(conflictResolveTempPattern)
	if err != nil {
		return fmt.Errorf("create temp file: %w", withoutPath(err))
	}
	tempName := filepath.Base(tempFile.Name())

	if n, writeErr := tempFile.Write(data); writeErr != nil || n != len(data) {
		if writeErr == nil {
			writeErr = io.ErrShortWrite
		}
		return closeConflictTempAfterError(tempFile, "write temp file", writeErr)
	}
	if err := tempFile.Chmod(perm); err != nil {
		return closeConflictTempAfterError(tempFile, "set temp permissions", err)
	}
	if err := tempFile.Sync(); err != nil {
		return closeConflictTempAfterError(tempFile, "sync temp file", err)
	}
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", withoutPath(err))
	}

	// The successful same-directory rename is the commit point. Do not defer a
	// name-based Remove: after this rename the old temp name is free and may be
	// reused by another process, so that name no longer proves ownership.
	if err := ops.rename(tempName, originalName); err != nil {
		// Leave our reserved temp entry in place on failure. Syncthing ignores the
		// prefix and its normal stale-temp policy can remove abandoned entries.
		return fmt.Errorf("replace original file: %w", withoutPath(err))
	}

	return nil
}

func closeConflictTempAfterError(tempFile conflictTempFile, operation string, operationErr error) error {
	operationErr = withoutPath(operationErr)
	if closeErr := tempFile.Close(); closeErr != nil {
		return fmt.Errorf("%s: %v; close temp file: %v", operation, operationErr, withoutPath(closeErr))
	}
	return fmt.Errorf("%s: %w", operation, operationErr)
}

// RemoveConflictFilesForOriginal removes every sync-conflict copy of the file
// at originalPath inside the given folder. The original file is NOT touched,
// and only copies that are regular files are removed (#151).
//
// Returns a JSON string of the form:
//
//	{"removed": <int>, "error": "<msg or empty>"}
//
// Possible error envelopes: "syncthing not running", "folder not found",
// "invalid path: outside folder root", "the original's directory is not a
// directory", or "remove conflict copy: <err>" if an individual deletion
// failed mid-loop. None carries a path or a name.
//
// Symmetric with GetConflictFilesJSON's JSON-return style — keeps the gomobile
// surface uniform (no tuple returns across the bridge).
func RemoveConflictFilesForOriginal(folderID, originalPath string) string {
	type result struct {
		Removed int    `json:"removed"`
		Error   string `json:"error"`
	}
	emit := func(r result) string {
		data, err := json.Marshal(r)
		if err != nil {
			return `{"removed":0,"error":"marshal failed"}`
		}
		return string(data)
	}

	folders := getFolderConfigs()
	if folders == nil {
		return emit(result{Error: "syncthing not running"})
	}

	folder, exists := folders[folderID]
	if !exists {
		return emit(result{Error: "folder not found"})
	}

	// Validate the original path is inside the folder root.
	absOriginal, err := safePath(folder.Path, originalPath)
	if err != nil {
		return emit(result{Error: "invalid path: outside folder root"})
	}
	// Reject paths that resolve to the folder root itself — there is no
	// "original file" at the root, and walking its parent would scan
	// outside the folder.
	if absOriginal == folder.Path {
		return emit(result{Error: "invalid path: outside folder root"})
	}

	dir, baseName, err := openFolderEntry(folder.Path, absOriginal)
	if err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist):
			// If the directory does not exist there are simply no conflicts to remove.
			return emit(result{Removed: 0})
		case errors.Is(err, errNotDirectory):
			return emit(result{Error: "the original's directory is not a directory"})
		case errors.Is(err, errNotRegularFile):
			return emit(result{Error: "invalid path: outside folder root"})
		default:
			return emit(result{Error: fmt.Sprintf("open the original's directory: %v", withoutPath(err))})
		}
	}
	defer dir.Close()
	ext := filepath.Ext(baseName)
	stem := strings.TrimSuffix(baseName, ext)
	// Common prefix of every conflict copy of this file.
	conflictPrefix := stem + ".sync-conflict-"

	entries, err := fs.ReadDir(dir.FS(), ".")
	if err != nil {
		return emit(result{Error: fmt.Sprintf("read dir: %v", withoutPath(err))})
	}

	removed := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, conflictPrefix) {
			continue
		}
		// Must also match the canonical conflict regex so we only delete real
		// Syncthing-generated copies, not user files that happen to share the prefix.
		matches := conflictPattern.FindStringSubmatch(name)
		if matches == nil {
			continue
		}
		// Defensive: matched stem must equal what we expected.
		if matches[1] != stem {
			continue
		}
		// Extension on the conflict copy must equal the original's extension
		// (handles files where stem itself contains dots).
		if matches[4] != ext {
			continue
		}
		// Only a copy that is a regular file is removed; anything else under
		// a conflict copy's name is left as it is.
		if _, err := lstatRegularFile(dir, name); err != nil {
			if errors.Is(err, errNotRegularFile) || errors.Is(err, os.ErrNotExist) {
				continue
			}
			return emit(result{Removed: removed, Error: fmt.Sprintf("inspect conflict copy: %v", withoutPath(err))})
		}
		if err := dir.Remove(name); err != nil {
			return emit(result{Removed: removed, Error: fmt.Sprintf("remove conflict copy: %v", withoutPath(err))})
		}
		removed++
	}

	return emit(result{Removed: removed})
}

// AutoResolveStateConflicts is retained for gomobile ABI compatibility but no
// longer mutates conflict files. Conflict copies that are still present remain
// visible through the manual conflict APIs.
//
// Returns a JSON string of the form:
//
//	{"resolved": <int>, "error": "<msg or empty>"}
func AutoResolveStateConflicts(folderID string) string {
	type result struct {
		Resolved int    `json:"resolved"`
		Error    string `json:"error"`
	}
	emit := func(r result) string {
		data, err := json.Marshal(r)
		if err != nil {
			return `{"resolved":0,"error":"marshal failed"}`
		}
		return string(data)
	}

	folders := getFolderConfigs()
	if folders == nil {
		return emit(result{Error: "syncthing not running"})
	}
	_, exists := folders[folderID]
	if !exists {
		return emit(result{Error: "folder not found"})
	}

	return emit(result{})
}
