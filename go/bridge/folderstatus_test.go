package bridge

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/model"
)

func TestDiagnosticsUploadPathAvailableIsExactAndReadOnly(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	folderPath := filepath.Join(configDir, "diagnostics-folder")
	addSendOnlyFolderForTesting(t, "diagnostics-folder", "Diagnostics Folder", folderPath)
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
	if errMsg := addFolderForTesting("healthy-folder", "Healthy Folder", folderPath); errMsg != "" {
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
		{"syncing: " + conflictRetentionSafetyMarker, "unknown_error"},
		{"something else", "unknown_error"},
	}

	for _, tc := range cases {
		if got := classifyFolderErrorReason(tc.input); got != tc.want {
			t.Fatalf("classifyFolderErrorReason(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestIssue150Issue167ConflictRetentionSafetyStatusIsStableAndPathFree(t *testing.T) {
	secretPath := "redaction-probe/vault-note.md"
	status := FolderStatus{
		State:         "idle",
		StateChanged:  "2026-08-28T16:00:00+02:00",
		CompletionPct: 91,
		NeedBytes:     123,
		NeedFiles:     1,
	}
	errors := []model.FileError{
		{Path: "other.md", Err: "permission denied"},
		{Path: secretPath, Err: conflictRetentionSafetyMarker},
	}

	if !applyConflictRetentionSafetyStatus(&status, errors) {
		t.Fatal("safety marker behind another error was not detected")
	}
	first := status
	if !applyConflictRetentionSafetyStatus(&status, errors) {
		t.Fatal("second safety overlay did not detect the same marker")
	}
	if status != first {
		t.Fatalf("repeated safety overlay changed status: first=%+v second=%+v", first, status)
	}
	if status.State != "error" || status.ErrorReason != conflictRetentionSafetyErrorReason {
		t.Fatalf("unexpected safety status: %+v", status)
	}
	if status.ErrorMessage != conflictRetentionSafetyErrorMessage || status.ErrorPath != "" {
		t.Fatalf("safety status exposed non-fixed detail: %+v", status)
	}
	if status.ErrorChanged != status.StateChanged {
		t.Fatalf("errorChanged = %q, want stable stateChanged %q", status.ErrorChanged, status.StateChanged)
	}
	if status.NeedBytes != 123 || status.NeedFiles != 1 || status.CompletionPct != 91 {
		t.Fatalf("safety overlay changed completion evidence: %+v", status)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secretPath) || strings.Contains(string(raw), conflictRetentionSafetyMarker) {
		t.Fatalf("safety status leaked raw detail: %s", raw)
	}
}

func TestIssue150FolderStateSafetyErrorIsNormalizedBeforeEarlyReturn(t *testing.T) {
	secretPath := "redaction-probe/vault-note.md"
	status := FolderStatus{
		StateChanged: "2026-08-28T16:00:00+02:00",
		ErrorPath:    secretPath,
	}
	stateErr := errors.New(conflictRetentionSafetyMarker)

	if !applyFolderSafetyEvidence(&status, stateErr, nil, errors.New("runner unavailable"), false) {
		t.Fatal("state safety error did not produce a terminal status")
	}
	if status.State != "error" || status.ErrorReason != conflictRetentionSafetyErrorReason {
		t.Fatalf("unexpected safety status: %+v", status)
	}
	if status.ErrorMessage != conflictRetentionSafetyErrorMessage || status.ErrorPath != "" {
		t.Fatalf("state safety error was not normalized: %+v", status)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secretPath) || strings.Contains(string(raw), conflictRetentionSafetyMarker) {
		t.Fatalf("state safety status leaked raw detail: %s", raw)
	}
}

func TestIssue150FolderErrorSafetySentinelOverridesSendOnlyEvidencePolicy(t *testing.T) {
	secretPath := "redaction-probe/vault-note.md"
	status := FolderStatus{
		State:        "idle",
		StateChanged: "2026-08-28T16:00:00+02:00",
		ErrorMessage: secretPath,
		ErrorPath:    secretPath,
	}
	folderErrors := []model.FileError{{Path: secretPath, Err: conflictRetentionSafetyMarker}}

	if !applyFolderSafetyEvidence(&status, nil, folderErrors, errors.New("folder errors unavailable"), false) {
		t.Fatal("exact folder-error sentinel did not override send-only evidence policy")
	}
	if status.State != "error" || status.ErrorReason != conflictRetentionSafetyErrorReason {
		t.Fatalf("unexpected safety status: %+v", status)
	}
	if status.ErrorMessage != conflictRetentionSafetyErrorMessage || status.ErrorPath != "" {
		t.Fatalf("folder-error safety status exposed non-fixed detail: %+v", status)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secretPath) || strings.Contains(string(raw), conflictRetentionSafetyMarker) {
		t.Fatalf("folder-error safety status leaked raw detail: %s", raw)
	}
}

func TestIssue150FolderErrorEvidenceUnavailableFailsClosedAndPathFree(t *testing.T) {
	secretPath := "redaction-probe/vault-note.md"
	status := FolderStatus{
		State:         "idle",
		StateChanged:  "2026-08-28T16:00:00+02:00",
		ErrorMessage:  secretPath,
		ErrorPath:     secretPath,
		CompletionPct: 100,
	}

	if !applyFolderSafetyEvidence(&status, nil, nil, errors.New("read "+secretPath), true) {
		t.Fatal("missing folder-error evidence did not produce a terminal status")
	}
	if status.State != "error" || status.ErrorReason != folderErrorEvidenceUnavailableReason {
		t.Fatalf("unexpected evidence-unavailable status: %+v", status)
	}
	if status.ErrorMessage != folderErrorEvidenceUnavailableMessage || status.ErrorPath != "" {
		t.Fatalf("evidence-unavailable status was not normalized: %+v", status)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secretPath) {
		t.Fatalf("evidence-unavailable status leaked raw detail: %s", raw)
	}
}

func TestIssue150FolderCompletionEvidenceUnavailableFailsClosedAndPathFree(t *testing.T) {
	secretPath := "redaction-probe/index.db"
	status := FolderStatus{
		State:         "idle",
		StateChanged:  "2026-08-28T16:00:00+02:00",
		CompletionPct: 100,
	}

	if applyFolderCompletionEvidence(&status, model.FolderCompletion{}, errors.New("read "+secretPath), true) {
		t.Fatal("missing completion evidence was accepted")
	}
	if status.State != "error" || status.ErrorReason != folderCompletionEvidenceUnavailableReason {
		t.Fatalf("unexpected completion-evidence status: %+v", status)
	}
	if status.ErrorMessage != folderCompletionEvidenceUnavailableMessage || status.ErrorPath != "" {
		t.Fatalf("completion-evidence status was not normalized: %+v", status)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secretPath) {
		t.Fatalf("completion-evidence status leaked raw detail: %s", raw)
	}
}

func TestIssue150SendOnlyOrdinaryFolderErrorsPreserveStateDiagnostics(t *testing.T) {
	tests := []struct {
		name       string
		stateError string
		wantReason string
	}{
		{name: "paused", stateError: "folder is paused", wantReason: "unknown_error"},
		{name: "not running", stateError: "folder not running", wantReason: "unknown_error"},
		{name: "permission", stateError: "permission denied", wantReason: "permission_denied"},
		{name: "marker", stateError: "folder marker missing", wantReason: "unknown_error"},
		{name: "disk", stateError: "no space left on device", wantReason: "disk_full"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stateErr := errors.New(test.stateError)
			status := FolderStatus{
				State:        "idle",
				StateChanged: "2026-08-28T16:00:00+02:00",
			}
			before := status

			if applyFolderSafetyEvidence(&status, stateErr, nil, errors.New("folder errors unavailable"), false) {
				t.Fatalf("ordinary send-only evidence became terminal: %+v", status)
			}
			if status != before {
				t.Fatalf("ordinary send-only evidence changed status: before=%+v after=%+v", before, status)
			}

			status.State = "error"
			status.ErrorReason = classifyFolderErrorReason(stateErr.Error())
			status.ErrorMessage = stateErr.Error()
			if status.ErrorReason != test.wantReason || status.ErrorMessage != test.stateError {
				t.Fatalf("normal state diagnosis was not preserved: %+v", status)
			}
			if status.ErrorReason == folderErrorEvidenceUnavailableReason || status.ErrorReason == conflictRetentionSafetyErrorReason {
				t.Fatalf("ordinary send-only diagnosis was recast as #150 evidence: %+v", status)
			}
		})
	}
}

func TestIssue150SendOnlyCompletionUnavailablePreservesExistingStatus(t *testing.T) {
	status := FolderStatus{
		State:           "idle",
		StateChanged:    "2026-08-28T16:00:00+02:00",
		ErrorReason:     "permission_denied",
		ErrorMessage:    "permission denied",
		ErrorChanged:    "2026-08-28T15:59:00+02:00",
		LocalBytes:      123,
		LocalFiles:      4,
		CompletionPct:   17,
		InProgressBytes: 5,
	}
	before := status

	if !applyFolderCompletionEvidence(&status, model.FolderCompletion{}, errors.New("folder not running"), false) {
		t.Fatalf("ordinary send-only completion error became terminal: %+v", status)
	}
	if status != before {
		t.Fatalf("ordinary send-only completion error changed status: before=%+v after=%+v", before, status)
	}
}

func TestIssue150ReceiveCapableAndUnknownFolderStatusEvidenceRemainFailClosed(t *testing.T) {
	tests := []struct {
		name       string
		folderType config.FolderType
		exists     bool
		want       bool
	}{
		{name: "send only", folderType: config.FolderTypeSendOnly, exists: true, want: false},
		{name: "send receive", folderType: config.FolderTypeSendReceive, exists: true, want: true},
		{name: "receive only", folderType: config.FolderTypeReceiveOnly, exists: true, want: true},
		{name: "receive encrypted", folderType: config.FolderTypeReceiveEncrypted, exists: true, want: true},
		{name: "unknown type", folderType: config.FolderType(127), exists: true, want: true},
		{name: "unknown folder", folderType: config.FolderTypeSendOnly, exists: false, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := folderTypeRequiresCompleteSafetyEvidence(test.folderType, test.exists); got != test.want {
				t.Fatalf("folderTypeRequiresCompleteSafetyEvidence(%v, %v) = %v, want %v", test.folderType, test.exists, got, test.want)
			}
		})
	}
}

func TestIssue150ExactSafetySentinelOverridesSendOnlyCompletionAndStaysPathFree(t *testing.T) {
	secretPath := "redaction-probe/index.db"
	status := FolderStatus{
		State:        "idle",
		StateChanged: "2026-08-28T16:00:00+02:00",
		ErrorMessage: secretPath,
		ErrorPath:    secretPath,
	}

	if applyFolderCompletionEvidence(&status, model.FolderCompletion{}, errors.New(conflictRetentionSafetyMarker), false) {
		t.Fatal("exact safety sentinel was accepted for a send-only completion error")
	}
	if status.State != "error" || status.ErrorReason != conflictRetentionSafetyErrorReason {
		t.Fatalf("unexpected safety status: %+v", status)
	}
	if status.ErrorMessage != conflictRetentionSafetyErrorMessage || status.ErrorPath != "" {
		t.Fatalf("send-only safety status exposed non-fixed detail: %+v", status)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secretPath) || strings.Contains(string(raw), conflictRetentionSafetyMarker) {
		t.Fatalf("send-only safety status leaked raw detail: %s", raw)
	}
}

func TestIssue150CompleteFolderEvidencePopulatesSettlementFields(t *testing.T) {
	status := FolderStatus{State: "idle"}
	completion := model.FolderCompletion{
		CompletionPct: 87,
		NeedBytes:     123,
		NeedItems:     4,
		GlobalBytes:   456,
		GlobalItems:   7,
	}

	if !applyFolderCompletionEvidence(&status, completion, nil, true) {
		t.Fatal("valid completion evidence was rejected")
	}
	if status.State != "idle" || status.CompletionPct != 87 || status.NeedBytes != 123 || status.NeedFiles != 4 || status.GlobalBytes != 456 || status.GlobalFiles != 7 {
		t.Fatalf("completion evidence was not copied exactly: %+v", status)
	}
}

func TestIssue150SafetyMarkerRequiresExactErrorValue(t *testing.T) {
	for _, message := range []string{
		"prefix " + conflictRetentionSafetyMarker,
		conflictRetentionSafetyMarker + " suffix",
		"redaction-probe/" + conflictRetentionSafetyMarker + ".md",
	} {
		if isConflictRetentionSafetyError(message) {
			t.Fatalf("non-exact marker value was accepted: %q", message)
		}
		if got := classifyFolderErrorReason(message); got == conflictRetentionSafetyErrorReason {
			t.Fatalf("non-exact marker classified as safety stop: %q", message)
		}
	}
	if !isConflictRetentionSafetyError("  " + conflictRetentionSafetyMarker + "\n") {
		t.Fatal("whitespace-wrapped exact marker was rejected")
	}
}
