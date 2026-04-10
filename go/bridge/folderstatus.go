// Folder sync status, completion, and .stignore management.
package bridge

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	if !stRunning || stApp == nil {
		return nil
	}
	return stApp.Internals
}

// getFolderConfigs returns configured folders keyed by folder ID.
func getFolderConfigs() map[string]config.FolderConfiguration {
	mu.Lock()
	defer mu.Unlock()
	if !stRunning || stCfg == nil {
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
	if err != nil {
		return "[]"
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

	if err := internals.SetIgnores(folderID, lines); err != nil {
		return fmt.Sprintf("set ignores: %v", err)
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
