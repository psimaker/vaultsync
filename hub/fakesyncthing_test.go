package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeSyncthing is an in-memory stand-in for the REST subset the Hub uses.
// It enforces the API key, keeps devices/folders/options like the real thing
// (as far as the Hub can observe) and records every mutation for assertions.
type fakeSyncthing struct {
	t      *testing.T
	apiKey string
	myID   string

	mu             sync.Mutex
	devices        []deviceConfig
	folders        []folderConfig
	options        map[string]any
	folderDefaults map[string]any
	dbFiles        map[string]int64
	connected      map[string]bool
	pending        map[string][]string // folder → offered by
	devicePatches  []map[string]any
	optionPatches  []map[string]any
}

const (
	fakeHubID    = "AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA"
	fakeDeviceID = "BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB"
)

func newFakeSyncthing(t *testing.T, myID string) (*fakeSyncthing, *httptest.Server) {
	t.Helper()
	f := &fakeSyncthing{
		t:              t,
		apiKey:         "test-key",
		myID:           myID,
		devices:        []deviceConfig{{DeviceID: myID, Name: "host"}},
		options:        map[string]any{"urAccepted": float64(0), "startBrowser": true},
		folderDefaults: map[string]any{},
		dbFiles:        map[string]int64{},
		connected:      map[string]bool{},
		pending:        map[string][]string{},
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeSyncthing) client(srv *httptest.Server) *SyncthingClient {
	return NewSyncthingClient(srv.URL, f.apiKey)
}

func (f *fakeSyncthing) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-API-Key") != f.apiKey {
		http.Error(w, "Not Authorized", http.StatusForbidden)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	write := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	decode := func(v any) bool {
		if err := json.NewDecoder(r.Body).Decode(v); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return false
		}
		return true
	}
	path := r.URL.Path
	switch {
	case path == "/rest/system/ping":
		write(map[string]string{"ping": "pong"})
	case path == "/rest/system/status":
		write(map[string]string{"myID": f.myID})
	case path == "/rest/system/connections":
		conns := map[string]any{}
		for id, c := range f.connected {
			conns[id] = map[string]any{"connected": c}
		}
		write(map[string]any{"connections": conns})
	case path == "/rest/config/devices" && r.Method == http.MethodGet:
		write(f.devices)
	case path == "/rest/config/devices" && r.Method == http.MethodPost:
		var d deviceConfig
		if !decode(&d) {
			return
		}
		for _, e := range f.devices {
			if e.DeviceID == d.DeviceID {
				http.Error(w, "device already exists", http.StatusBadRequest)
				return
			}
		}
		f.devices = append(f.devices, d)
	case strings.HasPrefix(path, "/rest/config/devices/") && r.Method == http.MethodPatch:
		id := strings.TrimPrefix(path, "/rest/config/devices/")
		var patch map[string]any
		if !decode(&patch) {
			return
		}
		for i := range f.devices {
			if f.devices[i].DeviceID == id {
				if n, ok := patch["name"].(string); ok {
					f.devices[i].Name = n
				}
				f.devicePatches = append(f.devicePatches, patch)
				return
			}
		}
		http.NotFound(w, r)
	case path == "/rest/config/folders" && r.Method == http.MethodGet:
		write(f.folders)
	case path == "/rest/config/folders" && r.Method == http.MethodPost:
		var fc folderConfig
		if !decode(&fc) {
			return
		}
		for _, e := range f.folders {
			if e.ID == fc.ID {
				http.Error(w, "folder already exists", http.StatusBadRequest)
				return
			}
		}
		f.folders = append(f.folders, fc)
	case strings.HasPrefix(path, "/rest/config/folders/") && r.Method == http.MethodGet:
		id := strings.TrimPrefix(path, "/rest/config/folders/")
		for _, fc := range f.folders {
			if fc.ID == id {
				write(fc)
				return
			}
		}
		http.NotFound(w, r)
	case strings.HasPrefix(path, "/rest/config/folders/") && r.Method == http.MethodPut:
		id := strings.TrimPrefix(path, "/rest/config/folders/")
		var fc folderConfig
		if !decode(&fc) {
			return
		}
		for i := range f.folders {
			if f.folders[i].ID == id {
				if f.folders[i].Path != fc.Path {
					f.t.Errorf("hub attempted to change the path of folder %s", id)
				}
				f.folders[i] = fc
				return
			}
		}
		http.NotFound(w, r)
	case strings.HasPrefix(path, "/rest/config/folders/") && r.Method == http.MethodPatch:
		// Real Syncthing resets every default-tagged field before applying a
		// PATCH (FolderConfiguration.UnmarshalJSON → structutil.SetDefaults),
		// so maxConflicts silently becomes 10 again. Emulate that so a test
		// catches any future use of PATCH on folders.
		id := strings.TrimPrefix(path, "/rest/config/folders/")
		for i := range f.folders {
			if f.folders[i].ID == id {
				base := f.folders[i]
				base.MaxConflicts = 10
				base.RescanIntervalS = 3600
				base.FSWatcherDelayS = 10
				if !decode(&base) {
					return
				}
				f.folders[i] = base
				return
			}
		}
		http.NotFound(w, r)
	case path == "/rest/config/options" && r.Method == http.MethodGet:
		write(f.options)
	case path == "/rest/config/options" && r.Method == http.MethodPatch:
		var patch map[string]any
		if !decode(&patch) {
			return
		}
		for k, v := range patch {
			f.options[k] = v
		}
		f.optionPatches = append(f.optionPatches, patch)
	case path == "/rest/config/defaults/folder" && r.Method == http.MethodPatch:
		var patch map[string]any
		if !decode(&patch) {
			return
		}
		for k, v := range patch {
			f.folderDefaults[k] = v
		}
	case path == "/rest/db/status":
		id := r.URL.Query().Get("folder")
		write(map[string]any{"localFiles": f.dbFiles[id], "state": "idle"})
	case path == "/rest/cluster/pending/folders":
		out := map[string]any{}
		for id, devs := range f.pending {
			offered := map[string]any{}
			for _, d := range devs {
				offered[d] = map[string]any{"label": id}
			}
			out[id] = map[string]any{"offeredBy": offered}
		}
		write(out)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeSyncthing) folder(id string) *folderConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.folders {
		if f.folders[i].ID == id {
			c := f.folders[i]
			return &c
		}
	}
	return nil
}

func (f *fakeSyncthing) folderByLabel(label string) *folderConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.folders {
		if f.folders[i].Label == label {
			c := f.folders[i]
			return &c
		}
	}
	return nil
}

func (f *fakeSyncthing) hasDevice(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.devices {
		if d.DeviceID == id {
			return true
		}
	}
	return false
}
