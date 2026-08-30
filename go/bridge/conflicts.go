// Conflict file detection, content reading, and resolution.
// Syncthing creates conflict copies with the pattern:
//
//	<name>.sync-conflict-<YYYYMMDD>-<HHMMSS>-<shortID>.<ext>
package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// errPathTraversal is returned when a relative path attempts to escape the folder root.
	errPathTraversal = errors.New("path traversal outside folder root")

	errNoReplaceUnsupported = errors.New("atomic no-replace rename is not supported by this filesystem")
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

const (
	// Keep both limits independent: the visit ceiling is the I/O hard bound,
	// while the collection ceiling bounds the response retained in memory.
	maxConflictScanVisitedEntries     = 10000
	maxConflictScanCollectedConflicts = 10000
	conflictInspectionJSONVersion     = 2
)

const keepBothTargetExistsError = "keep both target already exists"

// Conflict recovery is inspection-only until a separately approved
// byte-preserving recovery doctrine exists. Keep this value independent of
// runtime, folder, path, and device state so the ABI cannot leak user data.
const conflictRecoveryUnavailableError = "vaultsync-conflict-recovery-unavailable"

// Conflict inspection has a separate stable result from a verified empty
// scan. It deliberately carries no folder, path, filename, or driver detail.
const conflictInspectionUnavailableError = "vaultsync-conflict-inspection-unavailable"

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

type conflictFileOperations struct {
	readFile   func(string) ([]byte, error)
	stat       func(string) (os.FileInfo, error)
	createTemp func(string, string) (conflictTempFile, error)
	rename     func(string, string) error
	remove     func(string) error
}

func systemConflictFileOperations() conflictFileOperations {
	return conflictFileOperations{
		readFile: os.ReadFile,
		stat:     os.Stat,
		createTemp: func(dir, pattern string) (conflictTempFile, error) {
			return os.CreateTemp(dir, pattern)
		},
		rename: os.Rename,
		remove: os.Remove,
	}
}

type keepBothFileOperations struct {
	stat            func(string) (os.FileInfo, error)
	renameNoReplace func(string, string) error
}

func systemKeepBothFileOperations() keepBothFileOperations {
	return keepBothFileOperations{
		stat:            os.Stat,
		renameNoReplace: renameNoReplace,
	}
}

type conflictInspectionLimits struct {
	maxVisitedEntries     int
	maxCollectedConflicts int
}

type conflictInspectionWalk func(string, fs.WalkDirFunc) error

type conflictInspectionResultV2 struct {
	Version   int            `json:"version"`
	Conflicts []ConflictFile `json:"conflicts"`
	Complete  bool           `json:"complete"`
	Error     string         `json:"error,omitempty"`
}

func unavailableConflictInspectionV2() conflictInspectionResultV2 {
	return conflictInspectionResultV2{
		Version:   conflictInspectionJSONVersion,
		Conflicts: []ConflictFile{},
		Complete:  false,
		Error:     conflictInspectionUnavailableError,
	}
}

func configuredConflictInspection(folderID string) conflictInspectionResultV2 {
	folders := getFolderConfigs()
	if folders == nil {
		return unavailableConflictInspectionV2()
	}

	folder, exists := folders[folderID]
	if !exists {
		return unavailableConflictInspectionV2()
	}
	return inspectConflictFiles(folder.Path, conflictInspectionLimits{
		maxVisitedEntries:     maxConflictScanVisitedEntries,
		maxCollectedConflicts: maxConflictScanCollectedConflicts,
	}, filepath.WalkDir)
}

func inspectConflictFiles(root string, limits conflictInspectionLimits, walk conflictInspectionWalk) conflictInspectionResultV2 {
	result := conflictInspectionResultV2{
		Version:   conflictInspectionJSONVersion,
		Conflicts: []ConflictFile{},
		Complete:  true,
	}
	if limits.maxVisitedEntries <= 0 || limits.maxCollectedConflicts <= 0 || walk == nil {
		return unavailableConflictInspectionV2()
	}

	rootAvailable := false
	partial := false
	visitedEntries := 0
	walkErr := walk(root, func(path string, entry fs.DirEntry, entryErr error) error {
		if !rootAvailable {
			if entryErr != nil {
				return entryErr
			}
			if entry == nil || filepath.Clean(path) != filepath.Clean(root) || !entry.IsDir() {
				return fs.ErrInvalid
			}
			rootAvailable = true
			return nil
		}
		if entryErr != nil || entry == nil {
			partial = true
			return fs.SkipAll
		}

		visitedEntries++
		stopAfterEntry := visitedEntries >= limits.maxVisitedEntries
		if entry.IsDir() {
			if stopAfterEntry {
				partial = true
				return fs.SkipAll
			}
			return nil
		}

		name := entry.Name()
		if !strings.Contains(name, ".sync-conflict-") {
			if stopAfterEntry {
				partial = true
				return fs.SkipAll
			}
			return nil
		}

		matches := conflictPattern.FindStringSubmatch(name)
		if matches == nil {
			if stopAfterEntry {
				partial = true
				return fs.SkipAll
			}
			return nil
		}
		if len(result.Conflicts) >= limits.maxCollectedConflicts {
			partial = true
			return fs.SkipAll
		}

		baseName := matches[1]
		date := matches[2]
		shortID := matches[3]
		ext := matches[4]

		relPath, err := filepath.Rel(root, path)
		if err != nil {
			partial = true
			return fs.SkipAll
		}
		dir := filepath.Dir(relPath)

		originalRel := baseName + ext
		if dir != "." {
			originalRel = filepath.Join(dir, originalRel)
		}

		result.Conflicts = append(result.Conflicts, ConflictFile{
			OriginalPath:  originalRel,
			ConflictPath:  relPath,
			ConflictDate:  date,
			DeviceShortID: shortID,
		})

		if stopAfterEntry {
			partial = true
			return fs.SkipAll
		}
		return nil
	})
	if !rootAvailable || (visitedEntries == 0 && (partial || walkErr != nil)) {
		return unavailableConflictInspectionV2()
	}
	if walkErr != nil {
		partial = true
	}
	result.Complete = !partial
	return result
}

func marshalConflictInspectionV2(result conflictInspectionResultV2) string {
	data, err := json.Marshal(result)
	if err != nil {
		data, _ = json.Marshal(unavailableConflictInspectionV2())
	}
	return string(data)
}

// GetConflictFilesJSON preserves the historical bridge contract: every result
// is a JSON array. New callers must use GetConflictFilesInspectionJSONV2 so an
// unavailable or partial inspection cannot be mistaken for verified empty.
func GetConflictFilesJSON(folderID string) string {
	result := configuredConflictInspection(folderID)
	data, err := json.Marshal(result.Conflicts)
	if err != nil {
		return "[]"
	}
	return string(data)
}

// GetConflictFilesInspectionJSONV2 returns the versioned conflict-inspection
// contract. Complete=false with no error is a bounded partial list;
// Complete=false with the stable error is completely unavailable.
func GetConflictFilesInspectionJSONV2(folderID string) string {
	return marshalConflictInspectionV2(configuredConflictInspection(folderID))
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

// KeepBothConflict is retained for gomobile ABI compatibility. Conflict
// recovery is currently inspection-only, so it never accesses or mutates the
// filesystem and always returns a stable, path-free error.
func KeepBothConflict(folderID, conflictFileName string) string {
	return conflictRecoveryUnavailableError
}

func keepBothConflictFile(conflictPath, conflictFileName string, ops keepBothFileOperations) string {
	_, err := ops.stat(conflictPath)
	if errors.Is(err, os.ErrNotExist) {
		return "conflict file not found"
	}
	if err != nil {
		var pathError *os.PathError
		if errors.As(err, &pathError) {
			err = pathError.Err
		}
		return fmt.Sprintf("inspect conflict file: %v", err)
	}
	name := filepath.Base(conflictFileName)
	matches := conflictPattern.FindStringSubmatch(name)
	if matches == nil {
		return "invalid conflict filename"
	}

	// Build new name: baseName.conflict-shortID.ext
	newName := matches[1] + ".conflict-" + matches[3] + matches[4]
	newPath := filepath.Join(filepath.Dir(conflictPath), newName)

	if err := ops.renameNoReplace(conflictPath, newPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			return keepBothTargetExistsError
		}
		if errors.Is(err, errNoReplaceUnsupported) {
			return errNoReplaceUnsupported.Error()
		}
		if errors.Is(err, os.ErrNotExist) {
			return "conflict file not found"
		}
		return fmt.Sprintf("rename conflict file without replacing target: %v", err)
	}

	return ""
}

func readFileContent(folderID, relPath string) (string, bool) {
	folders := getFolderConfigs()
	if folders == nil {
		return "", false
	}
	folder, exists := folders[folderID]
	if !exists {
		return "", false
	}
	absPath, err := safePath(folder.Path, relPath)
	if err != nil {
		return "", false
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// ReadFileContent preserves the historical raw-text bridge contract. Failures
// retain the legacy error: prefix but expose only the fixed path-free code.
func ReadFileContent(folderID, relPath string) string {
	content, ok := readFileContent(folderID, relPath)
	if !ok {
		return "error:" + conflictInspectionUnavailableError
	}
	return content
}

type readFileContentResultV2 struct {
	Version int     `json:"version"`
	Content *string `json:"content,omitempty"`
	Error   string  `json:"error,omitempty"`
}

func unavailableReadFileContentV2() readFileContentResultV2 {
	return readFileContentResultV2{
		Version: conflictInspectionJSONVersion,
		Error:   conflictInspectionUnavailableError,
	}
}

// ReadFileContentJSONV2 returns an unambiguous versioned envelope so empty
// files and legitimate content beginning with error: remain distinguishable.
func ReadFileContentJSONV2(folderID, relPath string) string {
	content, ok := readFileContent(folderID, relPath)
	result := unavailableReadFileContentV2()
	if ok {
		result = readFileContentResultV2{
			Version: conflictInspectionJSONVersion,
			Content: &content,
		}
	}
	data, err := json.Marshal(result)
	if err != nil {
		data, _ = json.Marshal(unavailableReadFileContentV2())
	}
	return string(data)
}

// ResolveConflict is retained for gomobile ABI compatibility. Conflict
// recovery is currently inspection-only, so it never accesses or mutates the
// filesystem and always returns a stable, path-free error.
func ResolveConflict(folderID, conflictFileName string, keepConflict bool) string {
	return conflictRecoveryUnavailableError
}

func replaceConflictAndRemoveSource(conflictPath, originalPath string, ops conflictFileOperations) error {
	if err := replaceConflictOriginal(conflictPath, originalPath, ops); err != nil {
		return err
	}
	if err := ops.remove(conflictPath); err != nil {
		return fmt.Errorf("delete conflict file: %w", err)
	}
	return nil
}

func replaceConflictOriginal(conflictPath, originalPath string, ops conflictFileOperations) error {
	data, err := ops.readFile(conflictPath)
	if err != nil {
		return fmt.Errorf("read conflict file: %w", err)
	}

	// Preserve the current original's mode. A missing original is a supported
	// promotion case and keeps the historical 0644 default; every other stat
	// error is ambiguous and must fail before creating or changing anything.
	perm := os.FileMode(0o644)
	if info, statErr := ops.stat(originalPath); statErr == nil {
		perm = info.Mode()
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("stat original file: %w", statErr)
	}

	tempFile, err := ops.createTemp(filepath.Dir(originalPath), conflictResolveTempPattern)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tempPath := tempFile.Name()

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
		return fmt.Errorf("close temp file: %w", err)
	}

	// The successful same-directory rename is the commit point. Do not defer a
	// path-based Remove: after this rename the old temp path is free and may be
	// reused by another process, so that pathname no longer proves ownership.
	if err := ops.rename(tempPath, originalPath); err != nil {
		// Leave our reserved temp entry in place on failure. Syncthing ignores the
		// prefix and its normal stale-temp policy can remove abandoned entries.
		return fmt.Errorf("replace original file: %w", err)
	}

	return nil
}

func closeConflictTempAfterError(tempFile conflictTempFile, operation string, operationErr error) error {
	if closeErr := tempFile.Close(); closeErr != nil {
		return fmt.Errorf("%s: %v; close temp file: %v", operation, operationErr, closeErr)
	}
	return fmt.Errorf("%s: %w", operation, operationErr)
}

// RemoveConflictFilesForOriginal is retained for gomobile ABI compatibility.
// Conflict recovery is currently inspection-only, so it never accesses or
// mutates the filesystem.
//
// Returns a JSON string of the form:
//
//	{"removed": <int>, "error": "<msg or empty>"}
//
// Symmetric with GetConflictFilesJSON's JSON-return style — keeps the gomobile
// surface uniform (no tuple returns across the bridge).
func RemoveConflictFilesForOriginal(folderID, originalPath string) string {
	return fmt.Sprintf(`{"removed":0,"error":%q}`, conflictRecoveryUnavailableError)
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
