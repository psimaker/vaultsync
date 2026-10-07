package main

import (
	"context"
	"testing"
	"time"

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

// The Hub reports a vault's file count only once it knows it (#214):
// unknownFiles — which every consumer fails closed on — while the folder
// scans, syncs or has never completed a scan, and the global count, what the
// vault holds or still expects, once it has settled.
func TestIssue214_CountIsReportedOnlyForASettledVault(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name          string
		state         string
		local, global int64
		neverScanned  bool
		want          int64
	}{
		{"settled and empty", "idle", 0, 0, false, 0},
		{"settled with files", "idle", 5, 5, false, 5},
		{"announced by a device, not downloaded yet", "idle", 0, 7, false, 7},
		{"first scan still running", "scanning", 0, 0, true, unknownFiles},
		{"never scanned", "idle", 0, 0, true, unknownFiles},
		{"rescan running", "scanning", 5, 5, false, unknownFiles},
		{"syncing", "syncing", 3, 9, false, unknownFiles},
		{"error", "error", 5, 5, false, unknownFiles},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake, st := newFakeSyncthing(t, fakeHubID)
			prov := newTestProvisioner(t, fake, fake.client(st))
			// Adopted: the Hub knows nothing about the directory's content.
			f, err := prov.createVault(ctx, "Old Notes", true)
			if err != nil {
				t.Fatal(err)
			}
			fake.mu.Lock()
			fake.dbFiles[f.ID], fake.dbGlobal[f.ID] = c.local, c.global
			fake.dbState[f.ID], fake.neverScanned[f.ID] = c.state, c.neverScanned
			fake.mu.Unlock()
			vaults, err := prov.listVaults(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(vaults) != 1 || vaults[0].ID != f.ID || vaults[0].Files != c.want {
				t.Fatalf("got %+v, want files=%d", vaults, c.want)
			}
			if n := prov.settledFiles(ctx, f.ID); n != c.want {
				t.Fatalf("provision reply would say %d, want %d", n, c.want)
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
	if n := prov.settledFiles(ctx, f.ID); n != unknownFiles {
		t.Fatalf("provision reply would say %d", n)
	}
}

// A vault the Hub just created holds nothing of its own, so until its first
// scan completes it counts what other devices announced: 0 right after the
// create — which keeps the agent's new-vault evidence (decision 046) working
// — and a device's files as soon as they are announced. After the first
// scan the general rule applies: a rescan reads as unknown again.
func TestIssue214_AVaultTheHubJustCreatedCountsWhatDevicesAnnounced(t *testing.T) {
	fx := newPairingFixture(t)
	fx.fake.mu.Lock()
	fx.fake.defaultState = "scanning"
	fx.fake.defaultUnscanned = true
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
	// Another device's index arrives before the scan is through.
	fx.fake.mu.Lock()
	fx.fake.dbGlobal[id] = 3
	fx.fake.mu.Unlock()
	if reply, err = c.Provision(ctx, 2, fakeDeviceID, "Laptop", "", false); err != nil {
		t.Fatal(err)
	}
	if catalogued(reply.Vaults, id) != 3 {
		t.Fatalf("announced files must count: %+v", reply.Vaults)
	}
	// The first scan completes: the general rule takes over …
	fx.fake.mu.Lock()
	fx.fake.defaultState, fx.fake.defaultUnscanned = "", false
	fx.fake.dbGlobal[id] = 0
	fx.fake.mu.Unlock()
	if reply, err = c.Provision(ctx, 3, fakeDeviceID, "Laptop", "", false); err != nil {
		t.Fatal(err)
	}
	if catalogued(reply.Vaults, id) != 0 {
		t.Fatalf("settled and empty: %+v", reply.Vaults)
	}
	// … and a rescan after it is unknown, created here or not.
	fx.fake.mu.Lock()
	fx.fake.dbState[id] = "scanning"
	fx.fake.mu.Unlock()
	if reply, err = c.Provision(ctx, 4, fakeDeviceID, "Laptop", "", false); err != nil {
		t.Fatal(err)
	}
	if catalogued(reply.Vaults, id) != unknownFiles {
		t.Fatalf("rescan must read as unknown: %+v", reply.Vaults)
	}
}

// The created-vault exception is bounded: an adopted directory is never
// trusted, and a created vault is not trusted forever without a scan.
func TestIssue214_OnlyAFreshCreateIsTrustedWithoutAScan(t *testing.T) {
	ctx := context.Background()
	fake, st := newFakeSyncthing(t, fakeHubID)
	prov := newTestProvisioner(t, fake, fake.client(st))
	fake.mu.Lock()
	fake.defaultUnscanned = true
	fake.mu.Unlock()
	adopted, err := prov.createVault(ctx, "Adopted", true)
	if err != nil {
		t.Fatal(err)
	}
	created, err := prov.createVault(ctx, "Created", false)
	if err != nil {
		t.Fatal(err)
	}
	vaults, err := prov.listVaults(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if catalogued(vaults, adopted.ID) != unknownFiles || catalogued(vaults, created.ID) != 0 {
		t.Fatalf("got %+v", vaults)
	}
	prov.mu.Lock()
	prov.createdEmpty[created.ID] = time.Now().Add(-createdEmptyFor - time.Minute)
	prov.mu.Unlock()
	if vaults, err = prov.listVaults(ctx); err != nil {
		t.Fatal(err)
	}
	if catalogued(vaults, created.ID) != unknownFiles {
		t.Fatalf("an old create without a scan must read as unknown: %+v", vaults)
	}
}

// Sharing a vault restarts its folder, which rescans: the provision reply
// waits for the folder to settle instead of answering "unknown" to a device
// that could have joined; without the wait that moment reads as unknown.
func TestIssue214_ProvisionWaitsForTheSharedFolderToSettle(t *testing.T) {
	fx := newPairingFixture(t)
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
	fx.server.prov.settleWait = 2 * time.Second
	fx.fake.mu.Lock()
	fx.fake.dbFiles[id] = 4
	fx.fake.scanningFirst[id] = 3
	fx.fake.mu.Unlock()
	if reply, err = c.Provision(ctx, 2, fakeDeviceID, "Laptop", "Notes", false); err != nil || reply.Provisioned == nil {
		t.Fatalf("%+v %v", reply, err)
	}
	if reply.Provisioned.Files != 4 {
		t.Fatalf("must wait for the folder to settle, got %d", reply.Provisioned.Files)
	}
	fx.server.prov.settleWait = 0
	fx.fake.mu.Lock()
	fx.fake.scanningFirst[id] = 1
	fx.fake.mu.Unlock()
	if reply, err = c.Provision(ctx, 3, fakeDeviceID, "Laptop", "Notes", false); err != nil || reply.Provisioned == nil {
		t.Fatalf("%+v %v", reply, err)
	}
	if reply.Provisioned.Files != unknownFiles {
		t.Fatalf("without the wait a scanning folder is unknown, got %d", reply.Provisioned.Files)
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
