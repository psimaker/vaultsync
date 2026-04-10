package bridge

import (
	"encoding/json"
	"testing"

	"github.com/syncthing/syncthing/lib/events"
)

func TestEventInfoUnmarshalMissingPhase3Fields(t *testing.T) {
	oldSchemaJSON := `[{"id":7,"type":"StateChanged","time":"2026-04-10T11:22:33Z"}]`

	var decoded []EventInfo
	if err := json.Unmarshal([]byte(oldSchemaJSON), &decoded); err != nil {
		t.Fatalf("unmarshal old schema event payload: %v", err)
	}

	if len(decoded) != 1 {
		t.Fatalf("decoded %d events, want 1", len(decoded))
	}
	if decoded[0].ID != 7 {
		t.Fatalf("event ID = %d, want 7", decoded[0].ID)
	}
	if decoded[0].Type != "StateChanged" {
		t.Fatalf("event Type = %q, want StateChanged", decoded[0].Type)
	}
	if decoded[0].Relevant {
		t.Fatalf("event Relevant = true, want false zero-value when field is missing")
	}
	if decoded[0].Data != nil {
		t.Fatalf("event Data = %+v, want nil when field is missing", decoded[0].Data)
	}
}

func TestLegacyEventConsumerIgnoresPhase3Fields(t *testing.T) {
	newSchemaJSON := `[{"id":99,"type":"ItemFinished","time":"2026-04-10T11:22:33Z","relevant":true,"data":{"folder":"vault-a","item":"Daily.md"}}]`

	type legacyEventInfo struct {
		ID   int    `json:"id"`
		Type string `json:"type"`
		Time string `json:"time"`
	}

	var decoded []legacyEventInfo
	if err := json.Unmarshal([]byte(newSchemaJSON), &decoded); err != nil {
		t.Fatalf("legacy unmarshal of new schema event payload failed: %v", err)
	}

	if len(decoded) != 1 {
		t.Fatalf("decoded %d legacy events, want 1", len(decoded))
	}
	if decoded[0].ID != 99 || decoded[0].Type != "ItemFinished" || decoded[0].Time == "" {
		t.Fatalf("unexpected legacy event decode: %+v", decoded[0])
	}
}

func TestBridgeEventDataFolderErrorsHandlesMissingEntries(t *testing.T) {
	ev := events.Event{
		Type: events.FolderErrors,
		Data: map[string]interface{}{
			"folder": "vault-missing-errors",
		},
	}

	data := bridgeEventData(ev)
	if got := data["folder"]; got != "vault-missing-errors" {
		t.Fatalf("folder = %v, want vault-missing-errors", got)
	}
	if _, exists := data["reason"]; exists {
		t.Fatalf("reason should be absent when errors entries are missing: %+v", data)
	}
	if _, exists := data["message"]; exists {
		t.Fatalf("message should be absent when errors entries are missing: %+v", data)
	}
}

func TestFolderStatusUnmarshalMissingPhase1Fields(t *testing.T) {
	oldSchemaJSON := `{"state":"idle","stateChanged":"2026-04-10T11:22:33Z","completionPct":100,"globalBytes":12,"globalFiles":2,"localBytes":12,"localFiles":2,"needBytes":0,"needFiles":0,"inProgressBytes":0}`

	var decoded FolderStatus
	if err := json.Unmarshal([]byte(oldSchemaJSON), &decoded); err != nil {
		t.Fatalf("unmarshal old schema folder status payload: %v", err)
	}

	if decoded.State != "idle" {
		t.Fatalf("state = %q, want idle", decoded.State)
	}
	if decoded.ErrorReason != "" || decoded.ErrorMessage != "" || decoded.ErrorPath != "" || decoded.ErrorChanged != "" {
		t.Fatalf("new phase1 fields should stay empty for old payloads: %+v", decoded)
	}
}

func TestLegacyFolderStatusConsumerIgnoresPhase1Fields(t *testing.T) {
	type legacyFolderStatus struct {
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
	}

	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	defer StopSyncthing()

	raw := GetFolderStatusJSON("missing-folder-id")
	var decoded legacyFolderStatus
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("legacy unmarshal of new schema folder status failed: %v (raw=%s)", err, raw)
	}

	if decoded.State != "error" {
		t.Fatalf("state = %q, want error (raw=%s)", decoded.State, raw)
	}
}
