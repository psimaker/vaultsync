package main

import (
	"context"
	"testing"

	"github.com/psimaker/vaultsync/hub/pairing"
)

func catalogued(vaults []pairing.VaultInfo, id string) int64 {
	for _, v := range vaults {
		if v.ID == id {
			return v.Files
		}
	}
	return -99
}

// Zero — the one count that lets a device make its files a vault's first
// copy — is proven, not assumed (#214): the index knows of nothing, the
// directory on the Hub is empty right now, the folder is running. Positive
// counts are what the index knows locally or globally.
func TestIssue214_ZeroIsProvenNotAssumed(t *testing.T) {
	ctx := context.Background()
	const (
		dirEmpty = iota
		dirFiles
		dirMissing
	)
	cases := []struct {
		name          string
		state         string
		local, global int64
		dir           int
		want          int64
	}{
		{"empty and idle", "idle", 0, 0, dirEmpty, 0},
		{"empty, first scan running", "scanning", 0, 0, dirEmpty, 0},
		{"empty, waiting for its scan", "scan-waiting", 0, 0, dirEmpty, 0},
		{"files on the Hub", "idle", 5, 5, dirFiles, 5},
		{"announced by a device, not downloaded yet", "idle", 0, 7, dirEmpty, 7},
		{"local content the global index does not show", "idle", 1, 0, dirFiles, 1},
		{"copied onto the Hub, not scanned yet", "scanning", 0, 0, dirFiles, unknownFiles},
		{"copied onto the Hub after the last scan", "idle", 0, 0, dirFiles, unknownFiles},
		{"paused or not running", "", 0, 0, dirEmpty, unknownFiles},
		{"error", "error", 0, 0, dirEmpty, unknownFiles},
		{"syncing with nothing known", "syncing", 0, 0, dirEmpty, unknownFiles},
		{"directory gone", "idle", 0, 0, dirMissing, unknownFiles},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake, st := newFakeSyncthing(t, fakeHubID)
			prov := newTestProvisioner(t, fake, fake.client(st))
			f, err := prov.createVault(ctx, "Notes", false)
			if err != nil {
				t.Fatal(err)
			}
			switch c.dir {
			case dirFiles:
				testDirs[prov][f.Path] = true
			case dirMissing:
				delete(testDirs[prov], f.Path)
			}
			fake.mu.Lock()
			fake.dbFiles[f.ID], fake.dbGlobal[f.ID], fake.dbState[f.ID] = c.local, c.global, c.state
			fake.mu.Unlock()
			vaults, err := prov.listVaults(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(vaults) != 1 || vaults[0].ID != f.ID || vaults[0].Files != c.want {
				t.Fatalf("got %+v, want files=%d", vaults, c.want)
			}
		})
	}
}

// A status the Hub cannot read is unknown, not an empty vault (#214): the
// catalogue used to leave the count at 0.
func TestIssue214_UnreadableStatusIsUnknownNotEmpty(t *testing.T) {
	ctx := context.Background()
	fake, st := newFakeSyncthing(t, fakeHubID)
	prov := newTestProvisioner(t, fake, fake.client(st))
	f, err := prov.createVault(ctx, "Notes", false)
	if err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.dbStatusFail[f.ID] = true
	fake.mu.Unlock()
	vaults, err := prov.listVaults(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if catalogued(vaults, f.ID) != unknownFiles {
		t.Fatalf("catalogue: %+v", vaults)
	}
}

// A folder outside the vaults root cannot be looked at: its emptiness is
// never certified.
func TestIssue214_AVaultOutsideTheVaultsRootIsUnknown(t *testing.T) {
	ctx := context.Background()
	fake, st := newFakeSyncthing(t, fakeHubID)
	prov := newTestProvisioner(t, fake, fake.client(st))
	fake.mu.Lock()
	fake.folders = append(fake.folders, folderConfig{ID: "vs-elsewhere00", Label: "Elsewhere", Path: "/srv/other/elsewhere"})
	fake.mu.Unlock()
	testDirs[prov]["/srv/other/elsewhere"] = false
	vaults, err := prov.listVaults(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if catalogued(vaults, "vs-elsewhere00") != unknownFiles {
		t.Fatalf("catalogue: %+v", vaults)
	}
}

// The vault a pairing creates is empty on disk from the first moment, so it
// counts 0 while its first scan runs — the agent's new-vault evidence
// (decision 046) asks right after the create — and a device's files as soon
// as they are announced; files copied onto the Hub make it unknown until
// a scan has reported them.
func TestIssue214_AVaultTheHubJustCreatedCountsZeroWhileItScans(t *testing.T) {
	fx := newPairingFixture(t)
	fx.fake.mu.Lock()
	fx.fake.defaultState = "scanning"
	fx.fake.mu.Unlock()
	ctx := context.Background()
	c := fx.client()
	if _, err := c.Handshake(ctx, fx.code); err != nil {
		t.Fatal(err)
	}
	reply, err := c.Provision(ctx, 1, fakeDeviceID, "Laptop", "Notes", true)
	if err != nil || reply.Provisioned == nil {
		t.Fatalf("%+v %v", reply, err)
	}
	id := reply.Provisioned.ID
	if reply.Provisioned.Files != 0 || catalogued(reply.Vaults, id) != 0 {
		t.Fatalf("a vault created in this request must count 0 while its first scan runs: %+v %+v", reply.Provisioned, reply.Vaults)
	}
	fx.fake.mu.Lock()
	fx.fake.dbGlobal[id] = 3
	fx.fake.mu.Unlock()
	if reply, err = c.Provision(ctx, 2, fakeDeviceID, "Laptop", "", false); err != nil {
		t.Fatal(err)
	}
	if catalogued(reply.Vaults, id) != 3 {
		t.Fatalf("announced files must count: %+v", reply.Vaults)
	}
	fx.fake.mu.Lock()
	fx.fake.dbGlobal[id] = 0
	fx.fake.mu.Unlock()
	var path string
	for p := range testDirs[fx.server.prov] {
		path = p
	}
	testDirs[fx.server.prov][path] = true // copied onto the Hub, not scanned yet
	if reply, err = c.Provision(ctx, 3, fakeDeviceID, "Laptop", "", false); err != nil {
		t.Fatal(err)
	}
	if catalogued(reply.Vaults, id) != unknownFiles {
		t.Fatalf("files on disk the index has not seen must read as unknown: %+v", reply.Vaults)
	}
}

// "1 file", not "1 files" (#223); no number while the Hub does not know.
func TestIssue223_FileCountWording(t *testing.T) {
	for n, want := range map[int64]string{unknownFiles: "counting…", 0: "0 files", 1: "1 file", 2: "2 files"} {
		if got := fileCount(n); got != want {
			t.Errorf("fileCount(%d) = %q, want %q", n, got, want)
		}
	}
}
