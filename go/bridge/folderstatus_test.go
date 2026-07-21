package bridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiagnosticsUploadPathAvailableIsExactAndReadOnly(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	folderPath := filepath.Join(configDir, "diagnostics-folder")
	if errMsg := AddFolder("diagnostics-folder", "Diagnostics Folder", folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}
	installation := strings.Repeat("a", 52)
	operation := strings.Repeat("b", 52)
	operationsPath := filepath.Join(
		folderPath,
		diagnosticsNamespaceRoot,
		"installations",
		installation,
		"operations",
	)
	if err := os.MkdirAll(operationsPath, 0o700); err != nil {
		t.Fatal(err)
	}

	if !DiagnosticsUploadPathAvailable("diagnostics-folder", installation, operation) {
		t.Fatal("exact empty operation slot should be available")
	}
	if DiagnosticsUploadPathAvailable("diagnostics-folder", "../escape", operation) {
		t.Fatal("non-canonical component was accepted")
	}

	if errMsg := SetFolderIgnores("diagnostics-folder", `["VaultSync Diagnostics"]`); errMsg != "" {
		t.Fatalf("SetFolderIgnores failed: %s", errMsg)
	}
	if DiagnosticsUploadPathAvailable("diagnostics-folder", installation, operation) {
		t.Fatal("ignored diagnostics root was accepted")
	}
	if errMsg := SetFolderIgnores("diagnostics-folder", `[]`); errMsg != "" {
		t.Fatalf("clear ignores failed: %s", errMsg)
	}

	requestPath := filepath.Join(operationsPath, operation+".request.cbor")
	if err := os.WriteFile(requestPath, []byte("collision"), 0o600); err != nil {
		t.Fatal(err)
	}
	if DiagnosticsUploadPathAvailable("diagnostics-folder", installation, operation) {
		t.Fatal("existing operation artifact was accepted")
	}
	if !DiagnosticsUploadPathAllowed("diagnostics-folder", installation, operation) {
		t.Fatal("existing signed-artifact slot should remain ignore/access allowed")
	}
}

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
