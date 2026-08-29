package bridge

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/syncthing/syncthing/lib/events"
)

func TestIsUserVisibleEventType(t *testing.T) {
	if !isUserVisibleEventType(events.StateChanged) {
		t.Fatal("StateChanged should be user visible")
	}
	if !isUserVisibleEventType(events.ItemFinished) {
		t.Fatal("ItemFinished should be user visible")
	}
	if isUserVisibleEventType(events.DownloadProgress) {
		t.Fatal("DownloadProgress should not be user visible")
	}
}

func TestBridgeEventDataStateChanged(t *testing.T) {
	ev := events.Event{
		Type: events.StateChanged,
		Data: map[string]interface{}{
			"folder":   "vault-a",
			"from":     "idle",
			"to":       "syncing",
			"duration": 12.5,
		},
		Time: time.Now(),
	}

	data := bridgeEventData(ev)
	if got := data["folder"]; got != "vault-a" {
		t.Fatalf("folder = %v, want vault-a", got)
	}
	if got := data["from"]; got != "idle" {
		t.Fatalf("from = %v, want idle", got)
	}
	if got := data["to"]; got != "syncing" {
		t.Fatalf("to = %v, want syncing", got)
	}
	if _, exists := data["duration"]; exists {
		t.Fatalf("duration should not be included: %+v", data)
	}
}

func TestBridgeEventDataFolderErrors(t *testing.T) {
	ev := events.Event{
		Type: events.FolderErrors,
		Data: map[string]interface{}{
			"folder": "vault-b",
			"errors": []map[string]interface{}{
				{
					"path":  "Notes/todo.md",
					"error": "permission denied",
				},
			},
		},
		Time: time.Now(),
	}

	data := bridgeEventData(ev)
	if got := data["folder"]; got != "vault-b" {
		t.Fatalf("folder = %v, want vault-b", got)
	}
	if got := data["message"]; got != "permission denied" {
		t.Fatalf("message = %v, want permission denied", got)
	}
	if got := data["reason"]; got != "permission_denied" {
		t.Fatalf("reason = %v, want permission_denied", got)
	}
	if got := data["path"]; got != "Notes/todo.md" {
		t.Fatalf("path = %v, want Notes/todo.md", got)
	}
}

func TestIssue150Issue167BridgeEventDataConflictRetentionSafetyIsPathFree(t *testing.T) {
	secretPath := "redaction-probe/vault-note.md"
	secretFolder := "vault-safety"
	ev := events.Event{
		Type: events.FolderErrors,
		Data: map[string]interface{}{
			"folder": secretFolder,
			"errors": []map[string]interface{}{
				{"path": "other.md", "error": "permission denied"},
				{"path": secretPath, "error": conflictRetentionSafetyMarker},
			},
		},
		Time: time.Now(),
	}

	data := bridgeEventData(ev)
	if got := data["reason"]; got != conflictRetentionSafetyErrorReason {
		t.Fatalf("reason = %v, want %s", got, conflictRetentionSafetyErrorReason)
	}
	if got := data["message"]; got != conflictRetentionSafetyErrorMessage {
		t.Fatalf("message = %v, want fixed safety message", got)
	}
	if _, ok := data["path"]; ok {
		t.Fatalf("safety event leaked path: %+v", data)
	}
	if _, ok := data["folder"]; ok {
		t.Fatalf("safety event leaked folder: %+v", data)
	}
	if strings.Contains(fmt.Sprint(data), secretPath) || strings.Contains(fmt.Sprint(data), secretFolder) || strings.Contains(fmt.Sprint(data), conflictRetentionSafetyMarker) {
		t.Fatalf("safety event leaked raw detail: %+v", data)
	}
}

func TestIssue150Issue167BridgeItemFinishedSafetyOmitsItemPath(t *testing.T) {
	secretPath := "redaction-probe/vault-note.md"
	secretFolder := "vault-safety"
	ev := events.Event{
		Type: events.ItemFinished,
		Data: map[string]interface{}{
			"folder": secretFolder,
			"item":   secretPath,
			"type":   "file",
			"action": "update",
			"error":  conflictRetentionSafetyMarker,
		},
		Time: time.Now(),
	}

	data := bridgeEventData(ev)
	if got := data["reason"]; got != conflictRetentionSafetyErrorReason {
		t.Fatalf("reason = %v, want %s", got, conflictRetentionSafetyErrorReason)
	}
	if _, ok := data["item"]; ok {
		t.Fatalf("safety item event leaked item path: %+v", data)
	}
	if _, ok := data["folder"]; ok {
		t.Fatalf("safety item event leaked folder: %+v", data)
	}
	if strings.Contains(fmt.Sprint(data), secretPath) || strings.Contains(fmt.Sprint(data), secretFolder) || strings.Contains(fmt.Sprint(data), conflictRetentionSafetyMarker) {
		t.Fatalf("safety item event leaked raw detail: %+v", data)
	}
}

func TestIssue150BridgeStateChangedSafetyCannotReportSuccessOrIdentifiers(t *testing.T) {
	secretFolder := "redaction-probe-folder"
	ev := events.Event{
		Type: events.StateChanged,
		Data: map[string]interface{}{
			"folder": secretFolder,
			"from":   "syncing",
			"to":     "idle",
			"error":  conflictRetentionSafetyMarker,
		},
		Time: time.Now(),
	}

	data := bridgeEventData(ev)
	if got := data["reason"]; got != conflictRetentionSafetyErrorReason {
		t.Fatalf("reason = %v, want %s", got, conflictRetentionSafetyErrorReason)
	}
	for _, key := range []string{"folder", "from", "to", "item", "path", "id", "deviceName"} {
		if _, ok := data[key]; ok {
			t.Fatalf("safety state event retained %q: %+v", key, data)
		}
	}
	if strings.Contains(fmt.Sprint(data), secretFolder) || strings.Contains(fmt.Sprint(data), conflictRetentionSafetyMarker) {
		t.Fatalf("safety state event leaked raw detail: %+v", data)
	}
}

func TestBridgeEventDataItemFinishedSkipsEmptyError(t *testing.T) {
	ev := events.Event{
		Type: events.ItemFinished,
		Data: map[string]interface{}{
			"folder": "vault-c",
			"item":   "daily.md",
			"type":   "file",
			"action": "update",
			"error":  "",
		},
		Time: time.Now(),
	}

	data := bridgeEventData(ev)
	if got := data["item"]; got != "daily.md" {
		t.Fatalf("item = %v, want daily.md", got)
	}
	if _, exists := data["error"]; exists {
		t.Fatalf("error should be omitted when empty: %+v", data)
	}
}

func TestMakeEventInfoExportsSubscriptionCursor(t *testing.T) {
	eventTime := time.Date(2027, time.January, 15, 8, 0, 0, 123456789, time.UTC)
	ev := events.Event{
		SubscriptionID: 7,
		GlobalID:       42,
		Type:           events.ItemFinished,
		Time:           eventTime,
		Data: map[string]interface{}{
			"folder": "vault-c",
			"item":   "daily.md",
			"type":   "file",
			"action": "update",
			"error":  "",
		},
	}

	info := makeEventInfo(ev)
	if info.ID != ev.SubscriptionID {
		t.Fatalf("event cursor = %d, want subscription ID %d", info.ID, ev.SubscriptionID)
	}
	if info.ID == ev.GlobalID {
		t.Fatalf("event cursor unexpectedly used global ID %d", ev.GlobalID)
	}
	if info.Time != eventTime.Format(time.RFC3339Nano) {
		t.Fatalf("event time = %q, want nanosecond RFC3339 %q", info.Time, eventTime.Format(time.RFC3339Nano))
	}
}
