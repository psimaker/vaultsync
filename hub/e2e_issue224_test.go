package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/psimaker/vaultsync/hub/pairing"
)

// #224 against real Syncthing: what the Hub's engine does when it forgets a
// device. The removal here is the engine's own DELETE /rest/config/devices/ID,
// the one change a Hub command would make; these tests pin down what follows
// from it on the Hub and on the device that was removed.
//
// VAULTSYNC_HUB_SYNCTHING_BIN runs the Hub; VAULTSYNC_HUB_SYNCTHING_PEER_BIN,
// when set, runs the devices (a Hub on Syncthing 1.x with devices on 2.x is
// what an existing Hub sees, decision 068).

func e2eIssue224Bins(t *testing.T) (hubBin, peerBin string) {
	t.Helper()
	hubBin = os.Getenv("VAULTSYNC_HUB_SYNCTHING_BIN")
	if hubBin == "" {
		t.Skip("set VAULTSYNC_HUB_SYNCTHING_BIN to a syncthing binary to run the real-Syncthing E2E")
	}
	peerBin = os.Getenv("VAULTSYNC_HUB_SYNCTHING_PEER_BIN")
	if peerBin == "" {
		peerBin = hubBin
	}
	return hubBin, peerBin
}

type e2eHub struct {
	st      *stInstance
	vaults  string
	store   *stateStore
	pairing *httptest.Server
}

func startE2EHub(t *testing.T, ctx context.Context, bin string) *e2eHub {
	t.Helper()
	st := startSyncthing(t, bin, "hub")
	if _, err := applyHubDefaults(ctx, st.client, "E2E Hub", true); err != nil {
		t.Fatal(err)
	}
	// The Hub defaults switch discovery, relays and NAT on; this test stays on
	// loopback (#215).
	if err := st.client.PatchOptions(ctx, map[string]any{"globalAnnounceEnabled": false, "localAnnounceEnabled": false, "relaysEnabled": false, "natEnabled": false}); err != nil {
		t.Fatal(err)
	}
	vaults := filepath.Join(st.home, "vaults")
	store := newStateStore(filepath.Join(st.home, "hub-state.json"))
	srv := newPairingServer(store, newProvisioner(st.client, vaults, vaults), "E2E Hub", "e2e")
	srv.logf = t.Logf
	h := httptest.NewServer(srv.handler())
	t.Cleanup(h.Close)
	return &e2eHub{st: st, vaults: vaults, store: store, pairing: h}
}

// pair issues a new code on the Hub and pairs dev with it as name: the Hub
// shares vault (creating it when create is set) with dev, and dev accepts the
// share into dir — or keeps the folder it has when dir is empty.
func (h *e2eHub) pair(t *testing.T, ctx context.Context, dev *stInstance, name, vault string, create bool, dir string) vaultInfo {
	t.Helper()
	code, err := pairing.GenerateCode()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := h.store.update(func(st *hubState) error {
		st.Pairing = &pairingCodeState{Scalar: passwordScalarForCode(code), CreatedAt: now, ExpiresAt: now.Add(pairingCodeTTL)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	client := pairing.NewClient(strings.TrimPrefix(h.pairing.URL, "http://"))
	hello, err := client.Handshake(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	localProv := newProvisioner(dev.client, "", "")
	if err := localProv.ensureDevice(ctx, hello.HubDeviceID, hello.HubName); err != nil {
		t.Fatal(err)
	}
	if err := dev.client.PatchDevice(ctx, h.st.id, map[string]any{"addresses": []string{h.st.listen}}); err != nil {
		t.Fatal(err)
	}
	reply, err := client.Provision(ctx, 1, dev.id, name, vault, create)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Error != "" || reply.Provisioned == nil {
		t.Fatalf("provision: %+v", reply)
	}
	if dir != "" {
		if err := acceptShareLocally(ctx, dev.client, localProv, h.st.id, dev.id, *reply.Provisioned, dir); err != nil {
			t.Fatal(err)
		}
	}
	return *reply.Provisioned
}

// rest sends one request to an instance's API and returns status and body.
func rest(t *testing.T, ctx context.Context, inst *stInstance, method, path string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, "http://"+inst.gui+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-API-Key", inst.apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// rescanAndSettle scans a folder and waits until it is idle and needs nothing.
func rescanAndSettle(t *testing.T, ctx context.Context, inst *stInstance, folderID string) {
	t.Helper()
	if code, body := rest(t, ctx, inst, http.MethodPost, "/rest/db/scan?folder="+url.QueryEscape(folderID)); code != http.StatusOK {
		t.Fatalf("scan: HTTP %d %s", code, body)
	}
	waitUntil(t, "folder "+folderID+" to settle", 60*time.Second, func() bool {
		var st struct {
			State     string `json:"state"`
			NeedTotal int64  `json:"needTotalItems"`
		}
		return inst.client.Get(ctx, "/rest/db/status?folder="+url.QueryEscape(folderID), &st) == nil && st.State == "idle" && st.NeedTotal == 0
	})
}

// tree lists every entry under root — each file with its size, SHA-256 and
// modification time, each directory and link — so two looks show any change.
func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			out[rel] = "link " + target
		case d.IsDir():
			out[rel] = "dir"
		default:
			info, err := d.Info()
			if err != nil {
				return err
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[rel] = fmt.Sprintf("file %d %x %s", len(data), sha256.Sum256(data), info.ModTime().UTC().Format(time.RFC3339Nano))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameTree(t *testing.T, what string, before, after map[string]string) {
	t.Helper()
	if reflect.DeepEqual(before, after) {
		return
	}
	var diff []string
	for k, v := range before {
		if after[k] != v {
			diff = append(diff, fmt.Sprintf("  %s: %q → %q", k, v, after[k]))
		}
	}
	for k, v := range after {
		if _, ok := before[k]; !ok {
			diff = append(diff, fmt.Sprintf("  %s: new %q", k, v))
		}
	}
	sort.Strings(diff)
	t.Fatalf("%s changed:\n%s", what, strings.Join(diff, "\n"))
}

// folderWithoutDevices is a folder's raw configuration without its device
// list — everything a removal of a device must leave as it was.
func folderWithoutDevices(t *testing.T, ctx context.Context, inst *stInstance, folderID string) map[string]any {
	t.Helper()
	raw, err := inst.client.FolderRaw(ctx, folderID)
	if err != nil {
		t.Fatal(err)
	}
	delete(raw, "devices")
	return raw
}

func folderDeviceIDs(t *testing.T, ctx context.Context, inst *stInstance, folderID string) []string {
	t.Helper()
	folders, err := inst.client.Folders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range folders {
		if f.ID == folderID {
			var ids []string
			for _, d := range f.Devices {
				ids = append(ids, d.DeviceID)
			}
			sort.Strings(ids)
			return ids
		}
	}
	t.Fatalf("folder %s is gone", folderID)
	return nil
}

func hasDeviceEntry(t *testing.T, ctx context.Context, inst *stInstance, id string) bool {
	t.Helper()
	devices, err := inst.client.Devices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range devices {
		if d.DeviceID == id {
			return true
		}
	}
	return false
}

func connectedTo(ctx context.Context, inst *stInstance, id string) bool {
	c, err := inst.client.ConnectedDevices(ctx)
	return err == nil && c[id]
}

func fileIs(path, want string) bool {
	data, err := os.ReadFile(path)
	return err == nil && string(data) == want
}

func sortedIDs(ids ...string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}

// The issue's own case, then the removal: the computer "hermes" pairs, is
// wiped (`vaultsync uninstall --remove-data`: identity and database go, the
// vault's files stay) and set up again, and the Hub lists two devices of one
// name. Removing the old identity drops it from the vault's device list in the
// same change, and leaves the vault's settings and every file on the Hub as
// they were; the new identity syncs on.
func TestE2EIssue224_TheOldIdentityGoesAndTheVaultStaysAsItWas(t *testing.T) {
	hubBin, peerBin := e2eIssue224Bins(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	hub := startE2EHub(t, ctx, hubBin)

	oldDev := startSyncthing(t, peerBin, "hermes-old")
	oldDir := filepath.Join(t.TempDir(), "hermes-before", "Notes")
	vault := hub.pair(t, ctx, oldDev, "hermes", "Notes", true, oldDir)
	hubDir := filepath.Join(hub.vaults, "notes")
	waitUntil(t, "hermes to connect", 60*time.Second, func() bool { return connectedTo(ctx, hub.st, oldDev.id) })
	if err := os.WriteFile(filepath.Join(oldDir, "from-hermes.md"), []byte("# hermes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "hermes's note on the Hub", 90*time.Second, func() bool { return fileIs(filepath.Join(hubDir, "from-hermes.md"), "# hermes\n") })

	// uninstall --remove-data: the engine stops, its identity and database go.
	oldDev.stop()
	if err := os.RemoveAll(oldDev.home); err != nil {
		t.Fatal(err)
	}
	newDev := startSyncthing(t, peerBin, "hermes-new")
	newDir := filepath.Join(t.TempDir(), "hermes-after", "Notes")
	hub.pair(t, ctx, newDev, "hermes", "Notes", false, newDir)
	waitUntil(t, "the new hermes to connect", 60*time.Second, func() bool { return connectedTo(ctx, hub.st, newDev.id) })
	waitUntil(t, "the vault on the new hermes", 90*time.Second, func() bool { return fileIs(filepath.Join(newDir, "from-hermes.md"), "# hermes\n") })

	// What the issue reports: two devices of one name, both on the vault.
	if got, want := folderDeviceIDs(t, ctx, hub.st, vault.ID), sortedIDs(hub.st.id, oldDev.id, newDev.id); !reflect.DeepEqual(got, want) {
		t.Fatalf("vault shared with %v, want %v", got, want)
	}
	cfg := statusConfig(t, "http://"+hub.st.gui, hub.st.apiKey, hub.store.path)
	out, err := captureStdout(t, func() error { return cmdStatus(ctx, cfg) })
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	t.Logf("vaultsync-hub status with both identities:\n%s", out)
	rows := deviceRows(out, "hermes")
	if len(rows) != 2 {
		t.Fatalf("status lists %d rows for hermes, want 2:\n%s", len(rows), out)
	}
	// Each row begins with its device's short ID, and the engine's statistics
	// say when the old identity was last seen (#224, decision 094).
	for _, row := range rows {
		switch {
		case strings.HasPrefix(row, "  "+shortDeviceID(oldDev.id)+"  hermes "):
			if !strings.Contains(row, " offline, last seen 20") {
				t.Errorf("the old identity's row: %q", row)
			}
		case strings.HasPrefix(row, "  "+shortDeviceID(newDev.id)+"  hermes "):
			if !strings.HasSuffix(row, " connected") {
				t.Errorf("the new identity's row: %q", row)
			}
		default:
			t.Errorf("a row that begins with neither identity's short ID: %q", row)
		}
	}

	// The removal: the engine forgets the old identity.
	rescanAndSettle(t, ctx, hub.st, vault.ID)
	filesBefore := tree(t, hubDir)
	settingsBefore := folderWithoutDevices(t, ctx, hub.st, vault.ID)
	if code, body := rest(t, ctx, hub.st, http.MethodDelete, "/rest/config/devices/"+oldDev.id); code != http.StatusOK {
		t.Fatalf("remove the old identity: HTTP %d %s", code, body)
	}
	if hasDeviceEntry(t, ctx, hub.st, oldDev.id) {
		t.Fatal("the old identity is still a device of the Hub")
	}
	if got, want := folderDeviceIDs(t, ctx, hub.st, vault.ID), sortedIDs(hub.st.id, newDev.id); !reflect.DeepEqual(got, want) {
		t.Fatalf("after the removal the vault is shared with %v, want %v", got, want)
	}
	if settingsAfter := folderWithoutDevices(t, ctx, hub.st, vault.ID); !reflect.DeepEqual(settingsBefore, settingsAfter) {
		t.Fatalf("the vault's settings changed:\nbefore %v\nafter  %v", settingsBefore, settingsAfter)
	}
	rescanAndSettle(t, ctx, hub.st, vault.ID)
	sameTree(t, "the vault on the Hub", filesBefore, tree(t, hubDir))

	// The new identity syncs on, both ways.
	if !connectedTo(ctx, hub.st, newDev.id) {
		waitUntil(t, "the new hermes to reconnect", 60*time.Second, func() bool { return connectedTo(ctx, hub.st, newDev.id) })
	}
	if err := os.WriteFile(filepath.Join(newDir, "after-removal.md"), []byte("# after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hubDir, "hub-after-removal.md"), []byte("# hub after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the new note on the Hub", 90*time.Second, func() bool { return fileIs(filepath.Join(hubDir, "after-removal.md"), "# after\n") })
	waitUntil(t, "the Hub's note on the new hermes", 90*time.Second, func() bool { return fileIs(filepath.Join(newDir, "hub-after-removal.md"), "# hub after\n") })
	if !fileIs(filepath.Join(hubDir, "from-hermes.md"), "# hermes\n") || !fileIs(filepath.Join(newDir, "from-hermes.md"), "# hermes\n") {
		t.Fatal("the old identity's note is gone")
	}
}

// The removed device still runs — the worst case, removed while connected.
// The Hub closes the connection and sends it nothing: the device's folder, its
// settings and its files stay as they were, its retries land in the Hub's
// pending devices, and nothing crosses either way. The vault it was the only
// device of stays on the Hub with every file. Only a new pairing takes it
// back, and then what changed on either side in between syncs as after any
// absence: a deletion made on the device reaches the Hub, which keeps the
// deleted note in .stversions.
func TestE2EIssue224_ARemovedDeviceKeepsItsFilesAndComesBackOnlyByPairing(t *testing.T) {
	hubBin, peerBin := e2eIssue224Bins(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	hub := startE2EHub(t, ctx, hubBin)

	dev := startSyncthing(t, peerBin, "laptop")
	// Retry the Hub every few seconds, not every minute.
	if err := dev.client.PatchOptions(ctx, map[string]any{"reconnectionIntervalS": 3}); err != nil {
		t.Fatal(err)
	}
	devDir := filepath.Join(t.TempDir(), "laptop", "Solo")
	vault := hub.pair(t, ctx, dev, "laptop", "Solo", true, devDir)
	hubDir := filepath.Join(hub.vaults, "solo")
	waitUntil(t, "the laptop to connect", 60*time.Second, func() bool { return connectedTo(ctx, hub.st, dev.id) })
	if err := os.WriteFile(filepath.Join(devDir, "from-laptop.md"), []byte("# laptop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hubDir, "from-hub.md"), []byte("# hub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the laptop's note on the Hub", 90*time.Second, func() bool { return fileIs(filepath.Join(hubDir, "from-laptop.md"), "# laptop\n") })
	waitUntil(t, "the Hub's note on the laptop", 90*time.Second, func() bool { return fileIs(filepath.Join(devDir, "from-hub.md"), "# hub\n") })
	rescanAndSettle(t, ctx, dev, vault.ID)
	rescanAndSettle(t, ctx, hub.st, vault.ID)

	devFiles := tree(t, devDir)
	devFolder, err := dev.client.FolderRaw(ctx, vault.ID)
	if err != nil {
		t.Fatal(err)
	}
	devDevices, err := dev.client.Devices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hubFiles := tree(t, hubDir)
	hubSettings := folderWithoutDevices(t, ctx, hub.st, vault.ID)

	// Removed while connected.
	if !connectedTo(ctx, hub.st, dev.id) {
		t.Fatal("the laptop must be connected for this case")
	}
	if code, body := rest(t, ctx, hub.st, http.MethodDelete, "/rest/config/devices/"+dev.id); code != http.StatusOK {
		t.Fatalf("remove the laptop: HTTP %d %s", code, body)
	}
	waitUntil(t, "the Hub to drop the laptop's connection", 30*time.Second, func() bool { return !connectedTo(ctx, hub.st, dev.id) })
	waitUntil(t, "the laptop's retry to land in the Hub's pending devices", 90*time.Second, func() bool {
		var pending map[string]any
		return hub.st.client.Get(ctx, "/rest/cluster/pending/devices", &pending) == nil && pending[dev.id] != nil
	})
	if hasDeviceEntry(t, ctx, hub.st, dev.id) {
		t.Fatal("a retry of the removed laptop made it a device of the Hub again")
	}

	// Nothing changed on the laptop: its files, its folder, its devices.
	rescanAndSettle(t, ctx, dev, vault.ID)
	sameTree(t, "the vault on the removed laptop", devFiles, tree(t, devDir))
	if after, err := dev.client.FolderRaw(ctx, vault.ID); err != nil || !reflect.DeepEqual(devFolder, after) {
		t.Fatalf("the laptop's folder changed (%v):\nbefore %v\nafter  %v", err, devFolder, after)
	}
	if after, err := dev.client.Devices(ctx); err != nil || !reflect.DeepEqual(devDevices, after) {
		t.Fatalf("the laptop's devices changed (%v):\nbefore %+v\nafter  %+v", err, devDevices, after)
	}
	// The vault the laptop was the only device of stays on the Hub, as it was.
	if got, want := folderDeviceIDs(t, ctx, hub.st, vault.ID), []string{hub.st.id}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the vault is shared with %v, want the Hub alone", got)
	}
	if after := folderWithoutDevices(t, ctx, hub.st, vault.ID); !reflect.DeepEqual(hubSettings, after) {
		t.Fatalf("the vault's settings changed:\nbefore %v\nafter  %v", hubSettings, after)
	}
	rescanAndSettle(t, ctx, hub.st, vault.ID)
	sameTree(t, "the vault on the Hub", hubFiles, tree(t, hubDir))

	// While it is removed, nothing crosses in either direction.
	if err := os.WriteFile(filepath.Join(devDir, "while-removed.md"), []byte("# laptop alone\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(devDir, "from-hub.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hubDir, "hub-while-removed.md"), []byte("# hub alone\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rescanAndSettle(t, ctx, dev, vault.ID)
	rescanAndSettle(t, ctx, hub.st, vault.ID)
	time.Sleep(15 * time.Second) // several of the laptop's retries
	if _, err := os.Stat(filepath.Join(hubDir, "while-removed.md")); !os.IsNotExist(err) {
		t.Fatalf("a note reached the Hub from the removed laptop (%v)", err)
	}
	if !fileIs(filepath.Join(hubDir, "from-hub.md"), "# hub\n") {
		t.Fatal("a deletion on the removed laptop reached the Hub")
	}
	if _, err := os.Stat(filepath.Join(devDir, "hub-while-removed.md")); !os.IsNotExist(err) {
		t.Fatalf("a note reached the removed laptop from the Hub (%v)", err)
	}

	// Only a new pairing takes it back; then the absence syncs out.
	hub.pair(t, ctx, dev, "laptop", "Solo", false, "")
	waitUntil(t, "the laptop to connect again", 90*time.Second, func() bool { return connectedTo(ctx, hub.st, dev.id) })
	waitUntil(t, "the laptop's note from the absence on the Hub", 90*time.Second, func() bool {
		return fileIs(filepath.Join(hubDir, "while-removed.md"), "# laptop alone\n")
	})
	waitUntil(t, "the Hub's note from the absence on the laptop", 90*time.Second, func() bool {
		return fileIs(filepath.Join(devDir, "hub-while-removed.md"), "# hub alone\n")
	})
	waitUntil(t, "the laptop's deletion on the Hub", 90*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(hubDir, "from-hub.md"))
		return os.IsNotExist(err)
	})
	versions, _ := filepath.Glob(filepath.Join(hubDir, ".stversions", "from-hub~*.md"))
	if len(versions) != 1 || !fileIs(versions[0], "# hub\n") {
		t.Fatalf("the Hub kept no version of the deleted note: %v", versions)
	}
	if !fileIs(filepath.Join(hubDir, "from-laptop.md"), "# laptop\n") || !fileIs(filepath.Join(devDir, "from-laptop.md"), "# laptop\n") {
		t.Fatal("a note that nobody touched changed")
	}
}
