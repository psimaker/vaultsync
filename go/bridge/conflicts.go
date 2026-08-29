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

// maxConflictScan limits the number of files examined during conflict detection
// to prevent excessive I/O on very large vaults.
const maxConflictScan = 10000

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

// GetConflictFilesJSON scans the folder's directory for .sync-conflict-* files.
// It returns a JSON array only after a complete bounded walk; an unavailable,
// failed, or truncated inspection returns the stable path-free error instead.
func GetConflictFilesJSON(folderID string) string {
	folders := getFolderConfigs()
	if folders == nil {
		return conflictInspectionUnavailableError
	}

	folder, exists := folders[folderID]
	if !exists {
		return conflictInspectionUnavailableError
	}

	var conflicts []ConflictFile
	scanned := 0
	truncated := false

	walkErr := filepath.WalkDir(folder.Path, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		scanned++
		if scanned > maxConflictScan {
			truncated = true
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

		baseName := matches[1]
		date := matches[2]
		shortID := matches[3]
		ext := matches[4]

		relPath, err := filepath.Rel(folder.Path, path)
		if err != nil {
			return err
		}
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
	if walkErr != nil || truncated {
		return conflictInspectionUnavailableError
	}

	if conflicts == nil {
		conflicts = []ConflictFile{}
	}

	data, err := json.Marshal(conflicts)
	if err != nil {
		return conflictInspectionUnavailableError
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

// ReadFileContent reads a text file within a folder and returns a JSON envelope.
// The exported Go signature stays ABI-compatible, while the envelope keeps an
// empty file and legitimate "error:" content distinct from an unavailable
// inspection. Failures expose only the fixed path-free code.
func ReadFileContent(folderID, relPath string) string {
	type result struct {
		Content *string `json:"content,omitempty"`
		Error   string  `json:"error,omitempty"`
	}
	emit := func(value result) string {
		data, err := json.Marshal(value)
		if err != nil {
			return `{"error":"vaultsync-conflict-inspection-unavailable"}`
		}
		return string(data)
	}
	unavailable := func() string {
		return emit(result{Error: conflictInspectionUnavailableError})
	}

	folders := getFolderConfigs()
	if folders == nil {
		return unavailable()
	}
	folder, exists := folders[folderID]
	if !exists {
		return unavailable()
	}
	absPath, err := safePath(folder.Path, relPath)
	if err != nil {
		return unavailable()
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return unavailable()
	}
	content := string(data)
	return emit(result{Content: &content})
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
	return `{"removed":0,"error":"vaultsync-conflict-recovery-unavailable"}`
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
