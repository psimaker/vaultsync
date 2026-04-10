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
