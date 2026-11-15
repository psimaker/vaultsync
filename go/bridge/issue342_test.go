package bridge

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/syncthing/syncthing/lib/config"
)

// The engine hands out the pending shares as a map, and Go ranges over a map
// in a different order each time (#342). The app shows the list as it comes
// and runs its automatic accept pass whenever the list changes, so with two
// pending shares the rows could swap places at every poll — a tap aimed at
// one share landing on the other — and every swap ran the pass again. The
// devices that offer one share came in map order too, and the label of the
// share was the first label among them, so it could change between polls.
//
// The bridge now answers in the order of the IDs, which never change: the
// folders by folder ID, the devices of each by device ID, and the label is
// the first one in that device order. The app sorts the folders by name for
// display (Issue342PendingShareOrderTests on the Swift side).

const issue342Shared = "issue342-shared"

// issue342Row is what a pending share row is built from, without the times
// of the offers: those are the engine's own and may move when a peer sends
// its folder list again.
type issue342Row struct {
	ID      string
	Label   string
	Devices []string // "<device ID> <name>"
}

func issue342Rows(t *testing.T) []issue342Row {
	t.Helper()
	var pending []PendingFolderInfo
	if err := json.Unmarshal([]byte(GetPendingFoldersJSON()), &pending); err != nil {
		t.Fatalf("GetPendingFoldersJSON() unmarshal: %v", err)
	}
	rows := make([]issue342Row, 0, len(pending))
	for _, p := range pending {
		row := issue342Row{ID: p.ID, Label: p.Label}
		for _, d := range p.OfferedBy {
			row.Devices = append(row.Devices, d.DeviceID+" "+d.Name)
		}
		rows = append(rows, row)
	}
	return rows
}

// Two real peers offer four shares; one of them is offered by both, under a
// different label from each. Every call returns the shares, and the devices
// of each, in one order — the order of their IDs — and the shared folder
// keeps the label of the device with the lower ID, also once the devices are
// renamed.
func TestIssue342_PendingSharesComeInOneOrderOnEveryCall(t *testing.T) {
	bin := peerBinary(t)
	peerA := startPeer(t, bin, "issue342-peer-a")
	peerB := startPeer(t, bin, "issue342-peer-b")

	configDir := testConfigDir(t)
	if errMsg := StartSyncthing(configDir); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	captureBridgeLog(t, filepath.Join(configDir, "bridge-engine.log"))
	bridgeListen := "tcp://127.0.0.1:" + strconv.Itoa(freePort(t))
	mu.Lock()
	err := commitConfigLocked(func(cfg *config.Configuration) {
		cfg.Options.RawListenAddresses = []string{bridgeListen}
		cfg.Options.GlobalAnnEnabled = false
		cfg.Options.LocalAnnEnabled = false
		cfg.Options.RelaysEnabled = false
		cfg.Options.NATEnabled = false
	})
	mu.Unlock()
	if err != nil {
		t.Fatalf("pin bridge to loopback: %v", err)
	}
	bridgeID := DeviceID()

	// The device with the higher ID is named "alpha", the other "beta": an
	// order by name, with or without regard to case, would list them the
	// other way round than the order by device ID.
	first, second := peerA, peerB
	if first.id < second.id {
		first, second = second, first
	}
	names := map[*peerInstance]string{first: "alpha", second: "beta"}
	for _, peer := range []*peerInstance{peerA, peerB} {
		if errMsg := AddDevice(peer.id, names[peer]); errMsg != "" {
			t.Fatalf("AddDevice(%s) = %q", names[peer], errMsg)
		}
		mu.Lock()
		err := commitConfigLocked(func(cfg *config.Configuration) {
			for i := range cfg.Devices {
				if cfg.Devices[i].DeviceID.String() == peer.id {
					cfg.Devices[i].Addresses = []string{peer.listen}
				}
			}
		})
		mu.Unlock()
		if err != nil {
			t.Fatalf("set address of %s: %v", names[peer], err)
		}
		peer.mustDo(t, http.MethodPost, "/rest/config/devices", map[string]any{
			"deviceID":  bridgeID,
			"name":      "Bridge",
			"addresses": []string{bridgeListen},
		}, nil)
	}

	// By label the folders would sort otherwise than by folder ID.
	offer := func(peer *peerInstance, folderID, label string) {
		t.Helper()
		path := filepath.Join(peer.home, folderID)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		peer.mustDo(t, http.MethodPost, "/rest/config/folders", map[string]any{
			"id":               folderID,
			"label":            label,
			"path":             path,
			"type":             "sendreceive",
			"rescanIntervalS":  3600,
			"fsWatcherEnabled": false,
			"devices":          []map[string]any{{"deviceID": peer.id}, {"deviceID": bridgeID}},
		}, nil)
	}
	offer(first, "issue342-c", "banana")
	offer(first, "issue342-a", "Cherry")
	offer(first, "issue342-b", "date")
	offer(first, issue342Shared, "Shared as alpha names it")
	offer(second, issue342Shared, "Shared as beta names it")

	waitFor(t, "both peers connected", 60*time.Second, func() bool {
		return bridgeConnected(peerA.id) && bridgeConnected(peerB.id)
	})
	waitFor(t, "every offer pending", 60*time.Second, func() bool {
		rows := issue342Rows(t)
		if len(rows) != 4 {
			return false
		}
		for _, row := range rows {
			if row.ID == issue342Shared && len(row.Devices) != 2 {
				return false
			}
		}
		return true
	})

	want := func(firstName, secondName string) []issue342Row {
		return []issue342Row{
			{ID: "issue342-a", Label: "Cherry", Devices: []string{first.id + " " + firstName}},
			{ID: "issue342-b", Label: "date", Devices: []string{first.id + " " + firstName}},
			{ID: "issue342-c", Label: "banana", Devices: []string{first.id + " " + firstName}},
			{ID: issue342Shared, Label: "Shared as beta names it", Devices: []string{second.id + " " + secondName, first.id + " " + firstName}},
		}
	}
	const calls = 100
	seen := map[string]int{}
	var orders []string
	for call := 0; call < calls; call++ {
		got := fmt.Sprintf("%+v", issue342Rows(t))
		if seen[got] == 0 {
			orders = append(orders, got)
		}
		seen[got]++
	}
	if len(orders) != 1 {
		for _, order := range orders {
			t.Logf("%d of %d calls:\n%s", seen[order], calls, order)
		}
		t.Fatalf("%d calls returned the pending shares in %d different orders, want one", calls, len(orders))
	}
	if got, want := issue342Rows(t), want("alpha", "beta"); !reflect.DeepEqual(got, want) {
		t.Fatalf("every call returned the pending shares as\n%+v\nwant\n%+v", got, want)
	}

	// A device's name can change — the user edits it, or the engine fills it
	// in when the device connects — and the label, which names the folder a
	// share is accepted into, must not change with it. Renamed so that an
	// order by name would now put the other device first, the list keeps its
	// order and the shared folder its label.
	if errMsg := RenameDevice(second.id, "zulu"); errMsg != "" {
		t.Fatalf("RenameDevice(beta) = %q", errMsg)
	}
	waitFor(t, "the new name in the pending shares", 30*time.Second, func() bool {
		rows := issue342Rows(t)
		return len(rows) == 4 && reflect.DeepEqual(rows[3].Devices, want("alpha", "zulu")[3].Devices)
	})
	if got, want := issue342Rows(t), want("alpha", "zulu"); !reflect.DeepEqual(got, want) {
		t.Fatalf("after the rename the pending shares came as\n%+v\nwant\n%+v", got, want)
	}
}

// The order without an engine: whichever order the offers and the folders
// arrive in, the same list comes out, ordered by ID — never by a name or a
// label, which can change.
func TestIssue342_TheOrderDoesNotDependOnTheOrderOffersArriveIn(t *testing.T) {
	dev := func(id, name string) PendingDeviceInfo { return PendingDeviceInfo{DeviceID: id, Name: name} }
	offers := []pendingOffer{
		{device: dev("DEV-C", "mac"), label: "Notes from the mac"},
		{device: dev("DEV-A", ""), label: ""},
		{device: dev("DEV-B", "Zulu Hub"), label: "Notes on the Hub"},
		{device: dev("DEV-D", "Mac"), label: "Notes from the Mac"},
	}
	wantFolder := PendingFolderInfo{
		ID: "notes",
		// DEV-A comes first but gives no label; DEV-B is next, although its
		// name would sort last.
		Label:     "Notes on the Hub",
		OfferedBy: []PendingDeviceInfo{dev("DEV-A", ""), dev("DEV-B", "Zulu Hub"), dev("DEV-C", "mac"), dev("DEV-D", "Mac")},
	}
	for _, perm := range issue342Permutations(offers) {
		if got := pendingFolderInfo("notes", perm); !reflect.DeepEqual(got, wantFolder) {
			t.Fatalf("pendingFolderInfo(%+v) = %+v, want %+v", perm, got, wantFolder)
		}
	}

	folders := []PendingFolderInfo{
		{ID: "id-2", Label: "notes"},
		{ID: "id-1", Label: "notes"},
		{ID: "id-3", Label: "Notes"},
		{ID: "archive-id", Label: ""},
		{ID: "id-0", Label: "Zettel"},
	}
	wantIDs := []string{"archive-id", "id-0", "id-1", "id-2", "id-3"}
	for _, perm := range issue342Permutations(folders) {
		in := fmt.Sprintf("%+v", perm)
		sortPendingFolders(perm)
		got := make([]string, 0, len(perm))
		for _, f := range perm {
			got = append(got, f.ID)
		}
		if !reflect.DeepEqual(got, wantIDs) {
			t.Fatalf("sortPendingFolders(%s) put the IDs as %v, want %v", in, got, wantIDs)
		}
	}
}

// issue342Permutations returns every order of items, each in a slice of its
// own.
func issue342Permutations[T any](items []T) [][]T {
	if len(items) <= 1 {
		return [][]T{append([]T(nil), items...)}
	}
	var out [][]T
	for i := range items {
		rest := make([]T, 0, len(items)-1)
		rest = append(rest, items[:i]...)
		rest = append(rest, items[i+1:]...)
		for _, p := range issue342Permutations(rest) {
			out = append(out, append([]T{items[i]}, p...))
		}
	}
	return out
}
