// Conflict file detection, content reading, and resolution.
// Syncthing creates conflict copies with the pattern:
//
//	<name>.sync-conflict-<YYYYMMDD>-<HHMMSS>-<shortID>.<ext>
package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// errPathTraversal is returned when a relative path attempts to escape the folder root.
var errPathTraversal = errors.New("path traversal outside folder root")

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

// GetConflictFilesJSON scans the folder's directory for .sync-conflict-* files.
// Returns a JSON array of ConflictFile objects. Stops after scanning maxConflictScan files.
func GetConflictFilesJSON(folderID string) string {
	folders := getFolderConfigs()
	if folders == nil {
		return "[]"
	}

	folder, exists := folders[folderID]
	if !exists {
		return "[]"
	}

	var conflicts []ConflictFile
	scanned := 0

	filepath.WalkDir(folder.Path, func(path string, d os.DirEntry, err error) error {
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

		baseName := matches[1]
		date := matches[2]
		shortID := matches[3]
		ext := matches[4]

		relPath, _ := filepath.Rel(folder.Path, path)
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

	if _, err := os.Stat(conflictPath); os.IsNotExist(err) {
		return "conflict file not found"
	}

	name := filepath.Base(conflictFileName)
	matches := conflictPattern.FindStringSubmatch(name)
	if matches == nil {
		return "invalid conflict filename"
	}

	// Build new name: baseName.conflict-shortID.ext
	newName := matches[1] + ".conflict-" + matches[3] + matches[4]
	newPath := filepath.Join(filepath.Dir(conflictPath), newName)

	if err := os.Rename(conflictPath, newPath); err != nil {
		return fmt.Sprintf("rename conflict file: %v", err)
	}

	return ""
}

// ReadFileContent reads a text file within a folder and returns its content.
// folderID identifies the Syncthing folder; relPath is relative to the folder root.
// Returns the file content on success (may be empty for an empty file).
// Returns a string prefixed with "error:" if the file cannot be read or the path is invalid,
// allowing callers to distinguish read errors from legitimately empty files.
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
	data, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Sprintf("error:%v", err)
	}
	return string(data)
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

	if _, err := os.Stat(conflictPath); os.IsNotExist(err) {
		return "conflict file not found"
	}

	if keepConflict {
		name := filepath.Base(conflictFileName)
		matches := conflictPattern.FindStringSubmatch(name)
		if matches == nil {
			return "invalid conflict filename"
		}

		originalName := matches[1] + matches[4]
		originalPath := filepath.Join(filepath.Dir(conflictPath), originalName)

		data, err := os.ReadFile(conflictPath)
		if err != nil {
			return fmt.Sprintf("read conflict file: %v", err)
		}

		// Preserve original file permissions, default to 0644.
		perm := os.FileMode(0o644)
		if info, err := os.Stat(originalPath); err == nil {
			perm = info.Mode()
		}

		// Atomic write: write to temp file then rename to avoid partial writes.
		tmpPath := originalPath + ".vaultsync-tmp"
		if err := os.WriteFile(tmpPath, data, perm); err != nil {
			return fmt.Sprintf("write temp file: %v", err)
		}
		if err := os.Rename(tmpPath, originalPath); err != nil {
			os.Remove(tmpPath)
			return fmt.Sprintf("replace original file: %v", err)
		}
	}

	if err := os.Remove(conflictPath); err != nil {
		return fmt.Sprintf("delete conflict file: %v", err)
	}

	return ""
}

// RemoveConflictFilesForOriginal removes every sync-conflict copy of the file
// at originalPath inside the given folder. The original file is NOT touched.
//
// Returns a JSON string of the form:
//
//	{"removed": <int>, "error": "<msg or empty>"}
//
// Possible error envelopes: "syncthing not running", "folder not found",
// "invalid path: outside folder root", or "remove <name>: <err>" if an
// individual deletion failed mid-loop.
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

	dir := filepath.Dir(absOriginal)
	baseName := filepath.Base(originalPath)
	ext := filepath.Ext(baseName)
	stem := strings.TrimSuffix(baseName, ext)
	// Common prefix of every conflict copy of this file.
	conflictPrefix := stem + ".sync-conflict-"

	entries, err := os.ReadDir(dir)
	if err != nil {
		// If the directory does not exist there are simply no conflicts to remove.
		if os.IsNotExist(err) {
			return emit(result{Removed: 0})
		}
		return emit(result{Error: fmt.Sprintf("read dir: %v", err)})
	}

	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
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
		fullPath := filepath.Join(dir, name)
		if err := os.Remove(fullPath); err != nil {
			return emit(result{Removed: removed, Error: fmt.Sprintf("remove %s: %v", name, err)})
		}
		removed++
	}

	return emit(result{Removed: removed})
}

// stateDirName marks the directory whose contents count as app state (not
// user notes) for conflict auto-resolution.
const stateDirName = ".obsidian"

// isStateFilePath reports whether the folder-relative path points inside a
// `.obsidian` directory at any depth — covers both the single-vault layout
// (`.obsidian/...`) and the Obsidian-root layout (`MyVault/.obsidian/...`).
func isStateFilePath(relPath string) bool {
	dir := filepath.ToSlash(filepath.Dir(relPath))
	for _, part := range strings.Split(dir, "/") {
		if part == stateDirName {
			return true
		}
	}
	return false
}

// AutoResolveStateConflicts resolves conflict copies of app-state files
// (anything inside a `.obsidian` directory) without user interaction, using
// last-writer-wins: a conflict copy newer than its original replaces the
// original, an older (or equally old) copy is discarded. A copy whose
// original no longer exists is promoted to be the original so no data is
// lost. Files outside `.obsidian` directories are never touched.
//
// Stops after scanning maxConflictScan files, like GetConflictFilesJSON.
//
// Returns a JSON string of the form:
//
//	{"resolved": <int>, "error": "<msg or empty>"}
//
// On a mid-loop failure the envelope carries the partial count plus the error.
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
	folder, exists := folders[folderID]
	if !exists {
		return emit(result{Error: "folder not found"})
	}

	// Collect first, mutate after the walk: renaming/removing entries while
	// WalkDir iterates the same directories has platform-dependent results.
	type candidate struct {
		conflictPath string // absolute
		originalPath string // absolute
	}
	var candidates []candidate
	scanned := 0
	filepath.WalkDir(folder.Path, func(path string, d os.DirEntry, err error) error {
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
		relPath, relErr := filepath.Rel(folder.Path, path)
		if relErr != nil || !isStateFilePath(relPath) {
			return nil
		}
		originalName := matches[1] + matches[4]
		candidates = append(candidates, candidate{
			conflictPath: path,
			originalPath: filepath.Join(filepath.Dir(path), originalName),
		})
		return nil
	})

	resolved := 0
	for _, c := range candidates {
		conflictInfo, err := os.Stat(c.conflictPath)
		if err != nil {
			// Copy vanished since the walk (resolved elsewhere) — nothing to do.
			continue
		}
		var keepCopy bool
		originalInfo, err := os.Stat(c.originalPath)
		switch {
		case os.IsNotExist(err):
			// The conflict copy is the only surviving version — promote it.
			keepCopy = true
		case err != nil:
			return emit(result{Resolved: resolved, Error: fmt.Sprintf("stat %s: %v", filepath.Base(c.originalPath), err)})
		default:
			keepCopy = conflictInfo.ModTime().After(originalInfo.ModTime())
		}
		if keepCopy {
			// Atomic on POSIX: replaces the original in one step.
			if err := os.Rename(c.conflictPath, c.originalPath); err != nil {
				return emit(result{Resolved: resolved, Error: fmt.Sprintf("promote %s: %v", filepath.Base(c.conflictPath), err)})
			}
		} else {
			if err := os.Remove(c.conflictPath); err != nil {
				return emit(result{Resolved: resolved, Error: fmt.Sprintf("remove %s: %v", filepath.Base(c.conflictPath), err)})
			}
		}
		resolved++
	}

	return emit(result{Resolved: resolved})
}
