package join

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psimaker/vaultsync/hub/pairing"
	"github.com/psimaker/vaultsync/hub/syncthing"
)

const (
	testHubID    = "AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA-AAAAAAA"
	testDeviceID = "BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB-BBBBBBB"
)

// fakeDevice is the REST subset a device's Syncthing serves to this package.
type fakeDevice struct {
	mu      sync.Mutex
	folders []syncthing.FolderConfig
	devices []syncthing.DeviceConfig
	pending map[string][]string
	patches []map[string]any
}

func newFakeDevice(t *testing.T) (*fakeDevice, *syncthing.Client) {
	t.Helper()
	f := &fakeDevice{
		devices: []syncthing.DeviceConfig{{DeviceID: testDeviceID, Name: "laptop"}},
		pending: map[string][]string{},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "k" {
			http.Error(w, "Not Authorized", http.StatusForbidden)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch {
		case r.URL.Path == "/rest/config/folders" && r.Method == http.MethodGet:
			write(f.folders)
		case r.URL.Path == "/rest/config/folders" && r.Method == http.MethodPost:
			var fc syncthing.FolderConfig
			if err := json.NewDecoder(r.Body).Decode(&fc); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			f.folders = append(f.folders, fc)
		case r.URL.Path == "/rest/config/devices" && r.Method == http.MethodGet:
			write(f.devices)
		case r.URL.Path == "/rest/config/devices" && r.Method == http.MethodPost:
			var d syncthing.DeviceConfig
			if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			f.devices = append(f.devices, d)
		case strings.HasPrefix(r.URL.Path, "/rest/config/devices/") && r.Method == http.MethodPatch:
			var p map[string]any
			_ = json.NewDecoder(r.Body).Decode(&p)
			f.patches = append(f.patches, p)
		case r.URL.Path == "/rest/cluster/pending/folders":
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
	}))
	t.Cleanup(srv.Close)
	return f, syncthing.NewClient(srv.URL, "k")
}

// recorder notes the progress AcceptShare reports.
type recorder struct{ events []string }

func (r *recorder) AlreadyConfigured(path string) { r.events = append(r.events, "already:"+path) }
func (r *recorder) Waiting()                      { r.events = append(r.events, "waiting") }
func (r *recorder) WaitDone(err error) {
	if err != nil {
		r.events = append(r.events, "wait-failed")
		return
	}
	r.events = append(r.events, "arrived")
}
func (r *recorder) Added(label, path string) { r.events = append(r.events, "added:"+label) }

func TestPathsOverlap(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "srv", "vaults")
	cases := []struct {
		a, b string
		want bool
	}{
		{filepath.Join(root, "a"), filepath.Join(root, "a"), true},
		{filepath.Join(root, "a") + string(filepath.Separator), filepath.Join(root, "a"), true},
		{root, filepath.Join(root, "a"), true},
		{filepath.Join(root, "a"), root, true},
		{filepath.Join(root, "a"), filepath.Join(root, "ab"), false},
		{filepath.Join(root, "a"), filepath.Join(root, "b"), false},
		{filepath.Join(root, "a", "..", "b"), filepath.Join(root, "b", "c"), true},
	}
	for _, c := range cases {
		if got := PathsOverlap(c.a, c.b); got != c.want {
			t.Errorf("PathsOverlap(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestDirStateIgnoresOnlySyncthingsOwnEntries(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	if exists, empty, err := DirState(missing); err != nil || exists || !empty {
		t.Fatalf("missing: %v %v %v", exists, empty, err)
	}
	vault := filepath.Join(dir, "vault")
	for _, d := range []string{".stfolder", ".stversions"} {
		if err := os.MkdirAll(filepath.Join(vault, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if exists, empty, err := DirState(vault); err != nil || !exists || !empty {
		t.Fatalf("marker only: %v %v %v", exists, empty, err)
	}
	// An Obsidian settings folder is content: the guard is stricter than the
	// app's "nothing beyond .obsidian" rule, never looser.
	if err := os.MkdirAll(filepath.Join(vault, ".obsidian"), 0o755); err != nil {
		t.Fatal(err)
	}
	if exists, empty, err := DirState(vault); err != nil || !exists || empty {
		t.Fatalf(".obsidian must count as content: %v %v %v", exists, empty, err)
	}
}

func TestPickHubNeverPicksForTheUser(t *testing.T) {
	a := pairing.DiscoveredHub{Address: "192.168.1.20:8390", Name: "Home Hub"}
	b := pairing.DiscoveredHub{Address: "192.168.1.30:8390"}
	if _, err := PickHub(nil, nil); !errors.Is(err, ErrNoHub) {
		t.Fatalf("no answer: %v", err)
	}
	if got, err := PickHub([]pairing.DiscoveredHub{a}, nil); err != nil || got != a {
		t.Fatalf("one answer: %v %v", got, err)
	}
	_, err := PickHub([]pairing.DiscoveredHub{a, b}, nil)
	var several *SeveralHubsError
	if !errors.As(err, &several) || len(several.Hubs) != 2 {
		t.Fatalf("several answers without a chooser must refuse, got %v", err)
	}
	for _, want := range []string{"Home Hub at 192.168.1.20:8390", "192.168.1.30:8390", "--hub"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err, want)
		}
	}
	got, err := PickHub([]pairing.DiscoveredHub{a, b}, func(h []pairing.DiscoveredHub) (int, error) { return 1, nil })
	if err != nil || got != b {
		t.Fatalf("chosen: %v %v", got, err)
	}
	if _, err := PickHub([]pairing.DiscoveredHub{a, b}, func([]pairing.DiscoveredHub) (int, error) { return 2, nil }); err == nil {
		t.Fatal("an out-of-range choice must fail")
	}
	stop := errors.New("cancelled")
	if _, err := PickHub([]pairing.DiscoveredHub{a, b}, func([]pairing.DiscoveredHub) (int, error) { return 0, stop }); !errors.Is(err, stop) {
		t.Fatalf("chooser error: %v", err)
	}
}

func TestEnsureDeviceNeverAutoAccepts(t *testing.T) {
	ctx := context.Background()
	fake, client := newFakeDevice(t)
	if err := EnsureDevice(ctx, client, testHubID, "Home Hub"); err != nil {
		t.Fatal(err)
	}
	if len(fake.devices) != 2 {
		t.Fatalf("devices: %+v", fake.devices)
	}
	hub := fake.devices[1]
	if hub.DeviceID != testHubID || hub.Name != "Home Hub" || hub.AutoAcceptFolders || hub.Introducer || len(hub.Addresses) != 1 || hub.Addresses[0] != "dynamic" {
		t.Fatalf("hub device: %+v", hub)
	}
	// Idempotent; a known device keeps its name.
	if err := EnsureDevice(ctx, client, testHubID, "Other Name"); err != nil || len(fake.devices) != 2 || len(fake.patches) != 0 {
		t.Fatalf("second call: %v %+v %+v", err, fake.devices, fake.patches)
	}
	fake.devices[1].Name = ""
	if err := EnsureDevice(ctx, client, testHubID, "Home Hub"); err != nil || len(fake.patches) != 1 || fake.patches[0]["name"] != "Home Hub" {
		t.Fatalf("empty name not filled: %v %+v", err, fake.patches)
	}
}

func TestAcceptShareGuards(t *testing.T) {
	ctx := context.Background()
	vault := func(files int64) pairing.VaultInfo {
		return pairing.VaultInfo{ID: "vs-aaaaaaaaaaaa", Label: "Notes", Files: files}
	}
	setup := func(t *testing.T, nonEmpty bool) (*fakeDevice, Local, *recorder, string, *[]string) {
		fake, client := newFakeDevice(t)
		fake.pending["vs-aaaaaaaaaaaa"] = []string{testHubID}
		dir := filepath.Join(t.TempDir(), "Notes")
		var made []string
		rec := &recorder{}
		local := Local{
			Client: client,
			DirState: func(p string) (bool, bool, error) {
				if p != dir {
					t.Fatalf("guard looked at %s, not %s", p, dir)
				}
				return nonEmpty, !nonEmpty, nil
			},
			Mkdir:          func(p string) error { made = append(made, p); return nil },
			PendingTimeout: time.Second,
			Reporter:       rec,
		}
		return fake, local, rec, dir, &made
	}

	refusals := []struct {
		name  string
		files int64
		kind  error
		text  string
	}{
		{"both sides hold files", 12, ErrMergeRefused, "already holds files and the Hub's vault is not empty — nothing was changed. Move one of them aside; VaultSync never merges two vaults on its own"},
		{"unknown Hub count fails closed", -1, ErrHubCountUnknown, "already holds files and the Hub could not confirm that its vault is empty — nothing was changed. Use an empty directory, or retry"},
	}
	for _, c := range refusals {
		t.Run(c.name, func(t *testing.T) {
			fake, local, rec, dir, made := setup(t, true)
			err := AcceptShare(ctx, local, testHubID, testDeviceID, vault(c.files), dir)
			if !errors.Is(err, c.kind) || err.Error() != dir+" "+c.text {
				t.Fatalf("got %v", err)
			}
			if len(fake.folders) != 0 || len(*made) != 0 || len(rec.events) != 0 {
				t.Fatalf("a refusal changed something: folders=%v mkdir=%v events=%v", fake.folders, *made, rec.events)
			}
		})
	}

	t.Run("overlap with a configured folder", func(t *testing.T) {
		fake, local, _, dir, made := setup(t, false)
		fake.folders = []syncthing.FolderConfig{{ID: "other", Label: "Everything", Path: filepath.Dir(dir)}}
		err := AcceptShare(ctx, local, testHubID, testDeviceID, vault(0), dir)
		if !errors.Is(err, ErrOverlap) || err.Error() != dir+` overlaps the existing Syncthing folder "Everything" — choose another directory` {
			t.Fatalf("got %v", err)
		}
		if len(fake.folders) != 1 || len(*made) != 0 {
			t.Fatal("overlap refusal changed something")
		}
	})

	t.Run("brand-new Hub vault takes the local files as its first copy", func(t *testing.T) {
		fake, local, rec, dir, made := setup(t, true)
		if err := AcceptShare(ctx, local, testHubID, testDeviceID, vault(0), dir); err != nil {
			t.Fatal(err)
		}
		if len(*made) != 0 {
			t.Fatal("existing directory was created again")
		}
		f := fake.folders[0]
		if f.Path != dir || f.Type != "sendreceive" || f.MaxConflicts != FolderMaxConflicts || len(f.Devices) != 2 ||
			f.Devices[0].DeviceID != testDeviceID || f.Devices[1].DeviceID != testHubID {
			t.Fatalf("folder: %+v", f)
		}
		if strings.Join(rec.events, ",") != "waiting,arrived,added:Notes" {
			t.Fatalf("events: %v", rec.events)
		}
	})

	t.Run("empty target accepts a populated vault and is created", func(t *testing.T) {
		fake, local, _, dir, made := setup(t, false)
		if err := AcceptShare(ctx, local, testHubID, testDeviceID, vault(500), dir); err != nil {
			t.Fatal(err)
		}
		if len(fake.folders) != 1 || len(*made) != 1 || (*made)[0] != dir {
			t.Fatalf("folders=%v mkdir=%v", fake.folders, *made)
		}
	})

	t.Run("a configured vault is never re-pointed", func(t *testing.T) {
		fake, local, rec, dir, _ := setup(t, false)
		fake.folders = []syncthing.FolderConfig{{ID: "vs-aaaaaaaaaaaa", Label: "Notes", Path: "/elsewhere/Notes"}}
		if err := AcceptShare(ctx, local, testHubID, testDeviceID, vault(3), dir); err != nil {
			t.Fatal(err)
		}
		if len(fake.folders) != 1 || fake.folders[0].Path != "/elsewhere/Notes" || rec.events[0] != "already:/elsewhere/Notes" {
			t.Fatalf("folders=%v events=%v", fake.folders, rec.events)
		}
	})

	t.Run("a caller's last check before adding can stop the accept", func(t *testing.T) {
		fake, local, rec, dir, _ := setup(t, false)
		stop := errors.New("the folder changed while waiting")
		var checked string
		local.BeforeAdd = func(abs string) error { checked = abs; return stop }
		if err := AcceptShare(ctx, local, testHubID, testDeviceID, vault(3), dir); !errors.Is(err, stop) {
			t.Fatalf("got %v", err)
		}
		if checked != dir || len(fake.folders) != 0 {
			t.Fatalf("checked=%q folders=%v", checked, fake.folders)
		}
		// It runs after the wait, never before it.
		if strings.Join(rec.events, ",") != "waiting,arrived" {
			t.Fatalf("events: %v", rec.events)
		}
	})

	t.Run("a share that never arrives adds nothing", func(t *testing.T) {
		fake, local, rec, dir, _ := setup(t, false)
		delete(fake.pending, "vs-aaaaaaaaaaaa")
		err := AcceptShare(ctx, local, testHubID, testDeviceID, vault(3), dir)
		if !errors.Is(err, ErrShareDidNotArrive) || len(fake.folders) != 0 {
			t.Fatalf("err=%v folders=%v", err, fake.folders)
		}
		if strings.Join(rec.events, ",") != "waiting,wait-failed" {
			t.Fatalf("events: %v", rec.events)
		}
	})
}
