package bridge

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/syncthing/syncthing/lib/protocol"
)

// The engine hands out the configured folders and devices as maps, and Go
// ranges over a map in a different order each time (#345). The bridge built
// the folder list and the device list in that order, so with two vaults or
// two devices the app's rows could swap places between two polls, and every
// new order was a change to whatever reads the lists. The same cause as the
// pending shares of #342.
//
// The bridge now answers in the order of the IDs, which never change: the
// folders by folder ID, the devices by device ID — also in the connection
// list, which reads the same map. A folder's own device list
// is a slice in the configuration and keeps the order it has there (the
// engine sorts it by device ID). The app sorts the lists by name for display
// (Issue345ListOrderTests on the Swift side).

// Four folders and four devices, labelled and named so that an order by name
// would differ from the order by ID: every call returns them in one order,
// the order of their IDs.
func TestIssue345_FoldersAndDevicesComeInOneOrderOnEveryCall(t *testing.T) {
	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	captureBridgeLog(t, filepath.Join(configDir, "bridge-engine.log"))

	// The device with the highest ID gets the name that sorts first.
	var deviceIDs []string
	for i := range 4 {
		deviceIDs = append(deviceIDs, protocol.NewDeviceID([]byte(fmt.Sprintf("issue345-device-%d", i))).String())
	}
	byID := slices.Clone(deviceIDs)
	slices.Sort(byID)
	names := map[string]string{byID[3]: "alpha", byID[2]: "Beta", byID[1]: "charlie", byID[0]: "Delta"}
	for _, id := range deviceIDs {
		if errMsg := AddDevice(id, names[id]); errMsg != "" {
			t.Fatalf("AddDevice(%s) = %q", names[id], errMsg)
		}
	}

	root := t.TempDir()
	folders := []struct{ id, label string }{
		{"issue345-c", "apple"},
		{"issue345-a", "Date"},
		{"issue345-d", "banana"},
		{"issue345-b", "Cherry"},
	}
	for _, f := range folders {
		if errMsg := AddFolder(f.id, f.label, filepath.Join(root, f.id)); errMsg != "" {
			t.Fatalf("AddFolder(%s) = %q", f.id, errMsg)
		}
	}
	// A folder shared with two devices lists them in the order of its
	// configuration.
	for _, id := range []string{byID[2], byID[0]} {
		if errMsg := ShareFolderWithDevice("issue345-a", id); errMsg != "" {
			t.Fatalf("ShareFolderWithDevice(issue345-a, %s) = %q", names[id], errMsg)
		}
	}

	const calls = 100
	for _, list := range []struct {
		name string
		get  func() string
	}{
		{"GetFoldersJSON", GetFoldersJSON},
		{"GetDevicesJSON", GetDevicesJSON},
		{"GetConnectionsJSON", GetConnectionsJSON},
	} {
		seen := map[string]int{}
		var orders []string
		for range calls {
			got := list.get()
			if seen[got] == 0 {
				orders = append(orders, got)
			}
			seen[got]++
		}
		if len(orders) != 1 {
			for _, order := range orders {
				t.Logf("%d of %d calls: %s", seen[order], calls, order)
			}
			t.Errorf("%d calls of %s returned the list in %d different orders, want one", calls, list.name, len(orders))
		}
	}

	mu.Lock()
	var configured []string
	for _, d := range stCfg.Folders()["issue345-a"].Devices {
		if d.DeviceID != stMyID {
			configured = append(configured, d.DeviceID.String())
		}
	}
	mu.Unlock()
	if len(configured) != 2 {
		t.Fatalf("issue345-a is shared with %v in the configuration, want two devices", configured)
	}

	var gotFolders []FolderInfo
	if err := json.Unmarshal([]byte(GetFoldersJSON()), &gotFolders); err != nil {
		t.Fatalf("GetFoldersJSON() unmarshal: %v", err)
	}
	var folderIDs []string
	for _, f := range gotFolders {
		folderIDs = append(folderIDs, f.ID)
		if f.ID == "issue345-a" && !reflect.DeepEqual(f.DeviceIDs, configured) {
			t.Errorf("issue345-a lists its devices as %v, want the order of its configuration, %v", f.DeviceIDs, configured)
		}
	}
	if want := []string{"issue345-a", "issue345-b", "issue345-c", "issue345-d"}; !reflect.DeepEqual(folderIDs, want) {
		t.Errorf("GetFoldersJSON() lists the folders as %v, want the order of their IDs, %v", folderIDs, want)
	}

	for _, list := range []struct {
		name string
		get  func() string
	}{
		{"GetDevicesJSON", GetDevicesJSON},
		{"GetConnectionsJSON", GetConnectionsJSON},
	} {
		var gotDevices []DeviceInfo
		if err := json.Unmarshal([]byte(list.get()), &gotDevices); err != nil {
			t.Fatalf("%s() unmarshal: %v", list.name, err)
		}
		var gotDeviceIDs []string
		for _, d := range gotDevices {
			gotDeviceIDs = append(gotDeviceIDs, d.DeviceID)
		}
		if !reflect.DeepEqual(gotDeviceIDs, byID) {
			t.Errorf("%s() lists the devices as %v, want the order of their IDs, %v", list.name, gotDeviceIDs, byID)
		}
	}
}

// The order without an engine: whichever order the engine's maps hand the
// folders and devices out in, the same list comes out, ordered by ID — never
// by a label or a name, which can change.
func TestIssue345_TheOrderDoesNotDependOnTheOrderTheEngineHandsOut(t *testing.T) {
	folders := []FolderInfo{
		{ID: "id-2", Label: "notes"},
		{ID: "id-1", Label: "notes"},
		{ID: "id-3", Label: "Notes"},
		{ID: "archive-id", Label: ""},
		{ID: "id-0", Label: "Zettel"},
	}
	wantFolderIDs := []string{"archive-id", "id-0", "id-1", "id-2", "id-3"}
	for _, perm := range issue342Permutations(folders) {
		in := fmt.Sprintf("%+v", perm)
		sortFolderInfos(perm)
		var got []string
		for _, f := range perm {
			got = append(got, f.ID)
		}
		if !reflect.DeepEqual(got, wantFolderIDs) {
			t.Fatalf("sortFolderInfos(%s) put the IDs as %v, want %v", in, got, wantFolderIDs)
		}
	}

	// The device IDs as text, the form the app receives: "5…" comes before
	// "D…" here, although the engine's own order of the same IDs, by their
	// bytes, puts it after.
	devices := []DeviceInfo{
		{DeviceID: "DFBYY7O", Name: "alpha"},
		{DeviceID: "5IKJ74I", Name: "Zulu Hub"},
		{DeviceID: "CGKBN4G", Name: ""},
		{DeviceID: "K45H57R", Name: "mac"},
	}
	wantDeviceIDs := []string{"5IKJ74I", "CGKBN4G", "DFBYY7O", "K45H57R"}
	for _, perm := range issue342Permutations(devices) {
		in := fmt.Sprintf("%+v", perm)
		sortDeviceInfos(perm)
		var got []string
		for _, d := range perm {
			got = append(got, d.DeviceID)
		}
		if !reflect.DeepEqual(got, wantDeviceIDs) {
			t.Fatalf("sortDeviceInfos(%s) put the IDs as %v, want %v", in, got, wantDeviceIDs)
		}
	}
}
