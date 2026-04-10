package bridge

import (
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
