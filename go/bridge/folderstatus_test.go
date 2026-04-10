package bridge

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestGetFolderStatusJSONMissingFolderHasErrorDetails(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	raw := GetFolderStatusJSON("missing-folder-id")
	var status FolderStatus
	if err := json.Unmarshal([]byte(raw), &status); err != nil {
		t.Fatalf("GetFolderStatusJSON unmarshal failed: %v (raw=%s)", err, raw)
	}

	if status.State != "error" {
		t.Fatalf("state = %q, want error (raw=%s)", status.State, raw)
	}
	if status.ErrorReason == "" {
		t.Fatalf("errorReason is empty (raw=%s)", raw)
	}
	if status.ErrorMessage == "" {
		t.Fatalf("errorMessage is empty (raw=%s)", raw)
	}
}

func TestGetFolderStatusJSONHealthyFolderKeepsErrorFieldsEmpty(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	folderPath := filepath.Join(configDir, "healthy-folder")
	if errMsg := AddFolder("healthy-folder", "Healthy Folder", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}

	raw := GetFolderStatusJSON("healthy-folder")
	var status FolderStatus
	if err := json.Unmarshal([]byte(raw), &status); err != nil {
		t.Fatalf("GetFolderStatusJSON unmarshal failed: %v (raw=%s)", err, raw)
	}

	if status.State != "error" {
		if status.ErrorReason != "" || status.ErrorMessage != "" || status.ErrorPath != "" || status.ErrorChanged != "" {
			t.Fatalf("unexpected error detail on healthy status: %+v (raw=%s)", status, raw)
		}
	}
}

func TestClassifyFolderErrorReason(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"permission denied", "permission_denied"},
		{"no such file or directory", "folder_path_missing"},
		{"not a directory", "folder_path_invalid"},
		{"no space left on device", "disk_full"},
		{"connection refused", "network_error"},
		{"something else", "unknown_error"},
	}

	for _, tc := range cases {
		if got := classifyFolderErrorReason(tc.input); got != tc.want {
			t.Fatalf("classifyFolderErrorReason(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
