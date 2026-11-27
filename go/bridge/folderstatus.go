// Folder sync status, completion, and .stignore management.
package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/syncthing"
)

// FolderStatus is the JSON-serializable sync status of a folder.
type FolderStatus struct {
	State           string  `json:"state"`
	StateChanged    string  `json:"stateChanged"`
	CompletionPct   float64 `json:"completionPct"`
	GlobalBytes     int64   `json:"globalBytes"`
	GlobalFiles     int     `json:"globalFiles"`
	LocalBytes      int64   `json:"localBytes"`
	LocalFiles      int     `json:"localFiles"`
	NeedBytes       int64   `json:"needBytes"`
	NeedFiles       int     `json:"needFiles"`
	InProgressBytes int64   `json:"inProgressBytes"`
	ErrorReason     string  `json:"errorReason,omitempty"`
	ErrorMessage    string  `json:"errorMessage,omitempty"`
	ErrorPath       string  `json:"errorPath,omitempty"`
	ErrorChanged    string  `json:"errorChanged,omitempty"`
}

// getInternals returns the Internals pointer if Syncthing is running.
// Caller must NOT hold mu.
func getInternals() *syncthing.Internals {
	mu.Lock()
	defer mu.Unlock()
	if !engineUpLocked() || stApp == nil {
		return nil
	}
	return stApp.Internals
}

// getFolderConfig returns one folder's configuration. running is false while
// no engine runs; exists is false when the engine has no such folder.
func getFolderConfig(folderID string) (folder config.FolderConfiguration, running, exists bool) {
	mu.Lock()
	defer mu.Unlock()
	if !engineUpLocked() || stCfg == nil {
		return config.FolderConfiguration{}, false, false
	}
	folder, exists = stCfg.Folder(folderID)
	return folder, true, exists
}

// getFolderConfigs returns configured folders keyed by folder ID.
func getFolderConfigs() map[string]config.FolderConfiguration {
	mu.Lock()
	defer mu.Unlock()
	if !engineUpLocked() || stCfg == nil {
		return nil
	}
	return stCfg.Folders()
}

// GetFolderStatusJSON returns the sync status of a folder as JSON.
// Includes state (idle/scanning/syncing/error), completion percentage, and file counts.
func GetFolderStatusJSON(folderID string) string {
	internals := getInternals()
	if internals == nil {
		return "{}"
	}

	status := FolderStatus{}

	// Get folder state (idle, scanning, syncing, error, etc.).
	state, stateChanged, err := internals.FolderState(folderID)
	if err != nil {
		status.State = "error"
		status.StateChanged = time.Now().Format("2006-01-02T15:04:05Z07:00")
		status.ErrorReason = classifyFolderErrorReason(err.Error())
		status.ErrorMessage = err.Error()

		if inferred, ok := inferFolderPathErrorDetail(folderID); ok {
			if status.ErrorReason == "" || status.ErrorReason == "unknown_error" {
				status.ErrorReason = inferred.Reason
			}
			if status.ErrorMessage == "" {
				status.ErrorMessage = inferred.Message
			}
			if status.ErrorPath == "" {
				status.ErrorPath = inferred.Path
			}
			if status.ErrorChanged == "" {
				status.ErrorChanged = inferred.Changed
			}
		}
		return marshalFolderStatus(status)
	}
	status.State = state
	status.StateChanged = stateChanged.Format("2006-01-02T15:04:05Z07:00")

	// Some Syncthing internals return an empty state for unknown folders
	// instead of an explicit error. Normalize this into an error contract.
	if status.State == "" {
		status.State = "error"
		status.StateChanged = time.Now().Format("2006-01-02T15:04:05Z07:00")
		if inferred, ok := inferFolderPathErrorDetail(folderID); ok {
			status.ErrorReason = inferred.Reason
			status.ErrorMessage = inferred.Message
			status.ErrorPath = inferred.Path
			status.ErrorChanged = inferred.Changed
		} else {
			status.ErrorReason = "folder_state_unavailable"
			status.ErrorMessage = "Folder state is unavailable for this folder ID."
		}
		return marshalFolderStatus(status)
	}

	// Get completion for local device (how much of global state we have).
	completion, err := internals.Completion(protocol.LocalDeviceID, folderID)
	if err == nil {
		status.CompletionPct = completion.CompletionPct
		status.NeedBytes = completion.NeedBytes
		status.NeedFiles = completion.NeedItems
		status.GlobalBytes = completion.GlobalBytes
		status.GlobalFiles = completion.GlobalItems
	}

	// Get local file counts.
	localCounts, err := internals.LocalSize(folderID)
	if err == nil {
		status.LocalBytes = localCounts.Bytes
		status.LocalFiles = localCounts.Files
	}

	// Get in-progress bytes.
	status.InProgressBytes = internals.FolderProgressBytesCompleted(folderID)

	if status.State == "error" {
		if detail, ok := getFolderErrorDetail(folderID); ok {
			status.ErrorReason = detail.Reason
			status.ErrorMessage = detail.Message
			status.ErrorPath = detail.Path
			status.ErrorChanged = detail.Changed
		}

		if inferred, ok := inferFolderPathErrorDetail(folderID); ok {
			if status.ErrorReason == "" || status.ErrorReason == "unknown_error" {
				status.ErrorReason = inferred.Reason
			}
			if status.ErrorMessage == "" {
				status.ErrorMessage = inferred.Message
			}
			if status.ErrorPath == "" {
				status.ErrorPath = inferred.Path
			}
			if status.ErrorChanged == "" {
				status.ErrorChanged = inferred.Changed
			}
		}
	} else {
		clearFolderErrorDetail(folderID)
	}

	return marshalFolderStatus(status)
}

// GetFolderIgnores returns the .stignore lines for a folder as a JSON array.
// Reads the .stignore file directly from disk to avoid model cache staleness.
func GetFolderIgnores(folderID string) string {
	folders := getFolderConfigs()
	if folders == nil {
		return "[]"
	}

	folder, exists := folders[folderID]
	if !exists {
		return "[]"
	}

	ignorePath := filepath.Join(folder.Path, ".stignore")
	raw, err := os.ReadFile(ignorePath)
	switch {
	case err == nil:
	case os.IsNotExist(err):
		// No .stignore yet: legitimately no filters.
		return "[]"
	default:
		// Any other failure must not read as "no filters" — the file may well
		// hold patterns the app then hides (#182). Return an error object the
		// client cannot mistake for a pattern list; strip the path from the
		// OS error so the payload carries no user data.
		return marshalIgnoreReadError(err)
	}

	content := strings.TrimSpace(string(raw))
	if content == "" {
		return "[]"
	}

	// Normalize line endings (Windows peers may use \r\n).
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")

	data, err := json.Marshal(lines)
	if err != nil {
		return "[]"
	}
	return string(data)
}

// marshalIgnoreReadError encodes a failed .stignore read as {"error": ...}
// with the underlying OS error only (no path).
func marshalIgnoreReadError(err error) string {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}
	data, mErr := json.Marshal(struct {
		Error string `json:"error"`
	}{Error: "read .stignore: " + err.Error()})
	if mErr != nil {
		return `{"error":"read .stignore failed"}`
	}
	return string(data)
}

// SetFolderIgnores sets the .stignore lines for a folder.
// ignoresJSON must be a JSON array of strings, e.g. ["*.tmp", ".DS_Store"].
// Returns empty string on success, error message on failure.
func SetFolderIgnores(folderID, ignoresJSON string) string {
	internals := getInternals()
	if internals == nil {
		return "syncthing not running"
	}

	var lines []string
	if err := json.Unmarshal([]byte(ignoresJSON), &lines); err != nil {
		return fmt.Sprintf("invalid JSON: %v", err)
	}

	folder, running, exists := getFolderConfig(folderID)
	if !running {
		return "syncthing not running"
	}
	if !exists {
		return "folder not found"
	}
	if refusal := folderDirectoryRefusal(folder); refusal != "" {
		return refusal
	}

	if err := internals.SetIgnores(folderID, lines); err != nil {
		return ignoresWriteError(err)
	}

	return ""
}

// folderDirectoryRefusal returns "" when the folder's directory exists and
// holds its marker — the state in which the engine syncs it — and a refusal
// otherwise. Every bridge write of a folder's `.stignore` asks it first: the
// engine's write makes the folder's directory when it is missing and writes
// into a directory without the marker, so a vault whose folder was moved or
// deleted would get an empty folder of its name back at its old place. The
// engine then reports the marker missing instead of the path missing, and
// the path reconcile takes the new directory for the vault's own (#371,
// decision 069). VaultSync never makes a folder's directory once the folder
// exists (decision 002).
//
// The rule is the engine's own CheckPath, the marker entry in the directory,
// so a folder the engine syncs is never refused. Every refusal of a missing
// directory or marker contains "marker missing", which the app shows as a
// vault whose folder was moved or deleted; no refusal carries the path. A
// path with a file in place of one of its directories (ENOTDIR) is gone too.
//
// The engine's write asks the same again (patch 006), so a folder taken away
// between this check and the write is not made either, and its refusal reads
// the same (ignoresWriteError); this check also holds if the patch is lost.
func folderDirectoryRefusal(folder config.FolderConfiguration) string {
	err := folder.CheckPath()
	if err == nil {
		return ""
	}
	if refusal := directoryRefusal(err); refusal != "" {
		return refusal
	}
	return fmt.Sprintf("check the folder's directory: %v", withoutPath(err))
}

// directoryRefusal returns the refusal for an error that says a folder's
// directory is gone, holds no marker, or is not the directory the folder
// synced in — the bridge's own check's or the engine's — and "" for any
// other error.
func directoryRefusal(err error) string {
	const missing = "folder directory or marker missing: "
	switch {
	case errors.Is(err, config.ErrPathMissing), errors.Is(err, syscall.ENOTDIR):
		return missing + "the folder's directory is gone"
	case errors.Is(err, config.ErrMarkerMissing):
		return missing + "the directory holds no folder marker"
	case errors.Is(err, config.ErrPathNotDirectory):
		return missing + "the folder's path is not a directory"
	case errors.Is(err, errFolderNotItsDirectory):
		// A copy in the folder's place is no moved or deleted folder, and
		// its filters are not the folder's (#329).
		return folderNotItsDirectory
	default:
		return ""
	}
}

// ignoresWriteError is the text for a write of `.stignore` the engine did
// not make: its refusal of a folder whose directory is gone reads like the
// bridge's own (#371), and no text carries the path.
func ignoresWriteError(err error) string {
	if refusal := directoryRefusal(err); refusal != "" {
		return refusal
	}
	return fmt.Sprintf("set ignores: %v", withoutPath(err))
}

// withoutPath drops the path an *os.PathError carries.
func withoutPath(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}

// EnsureDefaultIgnores merges the given default ignore patterns into a folder's
// `.stignore`, adding only the patterns that are missing and preserving all
// existing custom lines and their order. defaultsJSON must be a JSON array of
// strings.
//
// Unlike a naive read-modify-write across the bridge, this distinguishes "no
// .stignore yet" (safe to create) from "could not read .stignore" (a transient
// error). On any read error other than not-exist it ABORTS without writing, so
// a momentary read failure can never overwrite a populated `.stignore` with
// just the defaults. A missing `.stignore` is safe to create only in the
// folder's own directory: a folder whose directory is gone, or no longer holds
// its marker, is refused before anything is read (#371). Returns empty string
// on success — including the no-op case where every default is already
// present — or an error message on failure.
func EnsureDefaultIgnores(folderID, defaultsJSON string) string {
	var defaults []string
	if err := json.Unmarshal([]byte(defaultsJSON), &defaults); err != nil {
		return fmt.Sprintf("invalid JSON: %v", err)
	}

	internals := getInternals()
	if internals == nil {
		return "syncthing not running"
	}

	folder, running, exists := getFolderConfig(folderID)
	if !running {
		return "syncthing not running"
	}
	if !exists {
		return "folder not found"
	}
	if refusal := folderDirectoryRefusal(folder); refusal != "" {
		return refusal
	}

	// Read existing lines directly from disk with precise error handling:
	// a missing file is fine (treat as empty), any OTHER error aborts.
	var existing []string
	raw, err := os.ReadFile(filepath.Join(folder.Path, ".stignore"))
	switch {
	case err == nil:
		content := strings.ReplaceAll(strings.TrimSpace(string(raw)), "\r\n", "\n")
		if content != "" {
			existing = strings.Split(content, "\n")
		}
	case os.IsNotExist(err):
		// No .stignore yet — safe to create one with the defaults.
	default:
		return fmt.Sprintf("read ignores: %v", err)
	}

	// Append only the defaults that are not already present, preserving order.
	present := make(map[string]struct{}, len(existing))
	for _, line := range existing {
		present[line] = struct{}{}
	}
	merged := existing
	added := false
	for _, def := range defaults {
		if _, ok := present[def]; ok {
			continue
		}
		merged = append(merged, def)
		present[def] = struct{}{}
		added = true
	}
	if !added {
		return ""
	}

	if err := internals.SetIgnores(folderID, merged); err != nil {
		return ignoresWriteError(err)
	}
	return ""
}

// RescanFolder triggers a rescan of all files in the folder.
// Returns empty string on success, error message on failure.
func RescanFolder(folderID string) string {
	internals := getInternals()
	if internals == nil {
		return "syncthing not running"
	}

	if err := internals.ScanFolderSubdirs(folderID, nil); err != nil {
		return fmt.Sprintf("rescan: %v", err)
	}

	return ""
}

func marshalFolderStatus(status FolderStatus) string {
	data, err := json.Marshal(status)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func inferFolderPathErrorDetail(folderID string) (folderErrorDetail, bool) {
	folders := getFolderConfigs()
	if folders == nil {
		return folderErrorDetail{}, false
	}

	folder, exists := folders[folderID]
	if !exists {
		return folderErrorDetail{
			Reason:  "folder_not_found",
			Message: "Folder is not configured in Syncthing.",
			Changed: time.Now().Format("2006-01-02T15:04:05Z07:00"),
		}, true
	}

	now := time.Now().Format("2006-01-02T15:04:05Z07:00")
	info, err := os.Stat(folder.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return folderErrorDetail{
				Reason:  "folder_path_missing",
				Message: fmt.Sprintf("Folder path does not exist: %s", folder.Path),
				Path:    folder.Path,
				Changed: now,
			}, true
		}
		if os.IsPermission(err) {
			return folderErrorDetail{
				Reason:  "permission_denied",
				Message: fmt.Sprintf("Permission denied while accessing folder path: %s", folder.Path),
				Path:    folder.Path,
				Changed: now,
			}, true
		}
		return folderErrorDetail{
			Reason:  "folder_path_unreadable",
			Message: fmt.Sprintf("Could not access folder path %s: %v", folder.Path, err),
			Path:    folder.Path,
			Changed: now,
		}, true
	}

	if !info.IsDir() {
		return folderErrorDetail{
			Reason:  "folder_path_invalid",
			Message: fmt.Sprintf("Folder path is not a directory: %s", folder.Path),
			Path:    folder.Path,
			Changed: now,
		}, true
	}

	if _, err := os.ReadDir(folder.Path); err != nil {
		if os.IsPermission(err) {
			return folderErrorDetail{
				Reason:  "permission_denied",
				Message: fmt.Sprintf("Permission denied while reading folder: %s", folder.Path),
				Path:    folder.Path,
				Changed: now,
			}, true
		}
		return folderErrorDetail{
			Reason:  "folder_path_unreadable",
			Message: fmt.Sprintf("Failed to read folder path %s: %v", folder.Path, err),
			Path:    folder.Path,
			Changed: now,
		}, true
	}

	return folderErrorDetail{}, false
}
