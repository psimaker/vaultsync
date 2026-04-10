// Connection status, events, and configuration queries.
package bridge

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/events"
)

// EventInfo is a simplified event for the bridge.
type EventInfo struct {
	ID       int                    `json:"id"`
	Type     string                 `json:"type"`
	Time     string                 `json:"time"`
	Relevant bool                   `json:"relevant"`
	Data     map[string]interface{} `json:"data"`
}

type folderErrorDetail struct {
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
	Changed string `json:"changed"`
}

// Persistent buffered subscription, created in StartSyncthing.
var stEventSub events.BufferedSubscription

var (
	stFolderErrSub        events.BufferedSubscription
	stFolderErrLastGlobal int
	stFolderErrByFolder   = map[string]folderErrorDetail{}
)

const maxBridgeEventsPerPoll = 120

// GetConnectionsJSON returns a JSON array of all device connections with status.
func GetConnectionsJSON() string {
	mu.Lock()
	defer mu.Unlock()

	if !stRunning || stCfg == nil {
		return "[]"
	}

	devices := stCfg.Devices()
	conns := make([]DeviceInfo, 0, len(devices))
	for _, dev := range devices {
		if dev.DeviceID == stMyID {
			continue
		}
		connected := false
		if stApp != nil && stApp.Internals != nil {
			connected = stApp.Internals.IsConnectedTo(dev.DeviceID)
		}
		conns = append(conns, DeviceInfo{
			DeviceID:  dev.DeviceID.String(),
			Name:      dev.Name,
			Connected: connected,
			Paused:    dev.Paused,
		})
	}

	data, err := json.Marshal(conns)
	if err != nil {
		return "[]"
	}
	return string(data)
}

// GetEventsSince returns a JSON array of events since the given event ID.
// Pass 0 to get all buffered events.
// Blocks up to 500ms waiting for new events — this is intentional to reduce
// busy-polling from the Swift side while keeping latency acceptable for UI updates.
func GetEventsSince(lastID int) string {
	mu.Lock()
	sub := stEventSub
	mu.Unlock()

	if sub == nil {
		return "[]"
	}

	evts := sub.Since(lastID, nil, 500*time.Millisecond)
	if len(evts) == 0 {
		return "[]"
	}
	if len(evts) > maxBridgeEventsPerPoll {
		evts = evts[len(evts)-maxBridgeEventsPerPoll:]
	}

	infos := make([]EventInfo, 0, len(evts))
	for _, ev := range evts {
		infos = append(infos, EventInfo{
			ID:       ev.GlobalID,
			Type:     ev.Type.String(),
			Time:     ev.Time.Format("2006-01-02T15:04:05Z07:00"),
			Relevant: isUserVisibleEventType(ev.Type),
			Data:     bridgeEventData(ev),
		})
	}

	data, err := json.Marshal(infos)
	if err != nil {
		return "[]"
	}
	return string(data)
}

// GetConfigJSON returns the current Syncthing configuration as JSON.
func GetConfigJSON() string {
	mu.Lock()
	defer mu.Unlock()

	if !stRunning || stCfg == nil {
		return "{}"
	}

	cfg := stCfg.RawCopy()
	data, err := json.Marshal(cfg)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// SetDiscoveryEnabled toggles local and global discovery.
// Returns empty string on success, error message on failure.
func SetDiscoveryEnabled(local bool, global bool) string {
	mu.Lock()
	defer mu.Unlock()

	if !stRunning || stCfg == nil {
		return "syncthing not running"
	}

	waiter, err := stCfg.Modify(func(cfg *config.Configuration) {
		cfg.Options.LocalAnnEnabled = local
		cfg.Options.GlobalAnnEnabled = global
	})
	if err != nil {
		return fmt.Sprintf("modify config: %v", err)
	}
	waiter.Wait()

	return ""
}

func asMap(v interface{}) map[string]interface{} {
	if m, ok := v.(map[string]interface{}); ok {
		return m
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		log.Printf("bridge: asMap unmarshal failed: %v", err)
		return nil
	}
	return m
}

func isUserVisibleEventType(eventType events.EventType) bool {
	switch eventType {
	case events.StateChanged, events.ItemFinished, events.DeviceConnected, events.DeviceDisconnected, events.FolderErrors:
		return true
	default:
		return false
	}
}

func bridgeEventData(ev events.Event) map[string]interface{} {
	data := asMap(ev.Data)
	if data == nil {
		return map[string]interface{}{}
	}

	out := map[string]interface{}{}

	switch ev.Type {
	case events.StateChanged:
		setStringField(out, "folder", data["folder"])
		setStringField(out, "from", data["from"])
		setStringField(out, "to", data["to"])
		if errMsg, ok := stringFromAny(data["error"]); ok && strings.TrimSpace(errMsg) != "" {
			out["error"] = errMsg
		}
	case events.ItemFinished:
		setStringField(out, "folder", data["folder"])
		setStringField(out, "item", data["item"])
		setStringField(out, "type", data["type"])
		setStringField(out, "action", data["action"])
		if errMsg, ok := stringFromAny(data["error"]); ok && strings.TrimSpace(errMsg) != "" {
			out["error"] = errMsg
		}
	case events.DeviceConnected:
		setStringField(out, "id", data["id"])
		setStringField(out, "deviceName", data["deviceName"])
		setStringField(out, "addr", data["addr"])
	case events.DeviceDisconnected:
		setStringField(out, "id", data["id"])
		if errMsg, ok := stringFromAny(data["error"]); ok && strings.TrimSpace(errMsg) != "" {
			out["error"] = errMsg
		}
	case events.FolderErrors:
		setStringField(out, "folder", data["folder"])
		entries := parseFolderErrorEntries(data["errors"])
		if len(entries) > 0 {
			first := entries[0]
			if first.Error != "" {
				out["message"] = first.Error
				out["reason"] = classifyFolderErrorReason(first.Error)
			}
			if first.Path != "" {
				out["path"] = first.Path
			}
		}
	}

	return out
}

func setStringField(out map[string]interface{}, key string, raw interface{}) {
	if val, ok := stringFromAny(raw); ok {
		out[key] = val
	}
}

func stringFromAny(raw interface{}) (string, bool) {
	switch v := raw.(type) {
	case nil:
		return "", false
	case string:
		return v, true
	case fmt.Stringer:
		return v.String(), true
	case bool:
		if v {
			return "true", true
		}
		return "false", true
	case float64:
		return fmt.Sprintf("%v", v), true
	case float32:
		return fmt.Sprintf("%v", v), true
	case int:
		return fmt.Sprintf("%d", v), true
	case int8:
		return fmt.Sprintf("%d", v), true
	case int16:
		return fmt.Sprintf("%d", v), true
	case int32:
		return fmt.Sprintf("%d", v), true
	case int64:
		return fmt.Sprintf("%d", v), true
	case uint:
		return fmt.Sprintf("%d", v), true
	case uint8:
		return fmt.Sprintf("%d", v), true
	case uint16:
		return fmt.Sprintf("%d", v), true
	case uint32:
		return fmt.Sprintf("%d", v), true
	case uint64:
		return fmt.Sprintf("%d", v), true
	default:
		return "", false
	}
}

func getFolderErrorDetail(folderID string) (folderErrorDetail, bool) {
	mu.Lock()
	defer mu.Unlock()

	if !stRunning {
		return folderErrorDetail{}, false
	}
	refreshFolderErrorCacheLocked()
	detail, ok := stFolderErrByFolder[folderID]
	return detail, ok
}

func clearFolderErrorDetail(folderID string) {
	mu.Lock()
	defer mu.Unlock()
	delete(stFolderErrByFolder, folderID)
}

func refreshFolderErrorCacheLocked() {
	if stEventSub == nil {
		stFolderErrSub = nil
		stFolderErrLastGlobal = 0
		stFolderErrByFolder = map[string]folderErrorDetail{}
		return
	}

	if stFolderErrSub == nil || stFolderErrSub != stEventSub {
		stFolderErrSub = stEventSub
		stFolderErrLastGlobal = 0
		stFolderErrByFolder = map[string]folderErrorDetail{}
	}

	evts := stEventSub.Since(stFolderErrLastGlobal, nil, 0)
	if len(evts) == 0 {
		return
	}

	for _, ev := range evts {
		if ev.GlobalID > stFolderErrLastGlobal {
			stFolderErrLastGlobal = ev.GlobalID
		}
		if ev.Type != events.FolderErrors {
			continue
		}

		folderID, detail, ok := folderErrorFromEvent(ev)
		if !ok {
			continue
		}
		stFolderErrByFolder[folderID] = detail
	}
}

func folderErrorFromEvent(ev events.Event) (string, folderErrorDetail, bool) {
	data := asMap(ev.Data)
	if data == nil {
		return "", folderErrorDetail{}, false
	}

	folderID, ok := data["folder"].(string)
	if !ok || folderID == "" {
		return "", folderErrorDetail{}, false
	}

	entries := parseFolderErrorEntries(data["errors"])
	if len(entries) == 0 {
		return "", folderErrorDetail{}, false
	}

	first := entries[0]
	if first.Error == "" {
		return "", folderErrorDetail{}, false
	}

	return folderID, folderErrorDetail{
		Reason:  classifyFolderErrorReason(first.Error),
		Message: first.Error,
		Path:    first.Path,
		Changed: ev.Time.Format("2006-01-02T15:04:05Z07:00"),
	}, true
}

type folderErrorEntry struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

func parseFolderErrorEntries(raw interface{}) []folderErrorEntry {
	if raw == nil {
		return nil
	}

	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}

	var entries []folderErrorEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil
	}
	return entries
}

func classifyFolderErrorReason(message string) string {
	msg := strings.ToLower(message)
	switch {
	case strings.Contains(msg, "permission denied"),
		strings.Contains(msg, "operation not permitted"),
		strings.Contains(msg, "access denied"):
		return "permission_denied"
	case strings.Contains(msg, "no such file"),
		strings.Contains(msg, "does not exist"),
		strings.Contains(msg, "not found"):
		return "folder_path_missing"
	case strings.Contains(msg, "not a directory"),
		strings.Contains(msg, "invalid path"):
		return "folder_path_invalid"
	case strings.Contains(msg, "no space left"):
		return "disk_full"
	case strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "timeout"),
		strings.Contains(msg, "unreachable"),
		strings.Contains(msg, "network"):
		return "network_error"
	default:
		return "unknown_error"
	}
}
