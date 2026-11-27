package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/psimaker/vaultsync/hub/pairing"
)

// #490, decision 097: who a write leaves the state file to, decided from the
// effective user, the file's owner and its folder's owner. Tested here for
// every case; the real run as root is TestIssue490_AsRootAWriteKeepsTheStateFilesOwner.
func TestIssue490_TheStateFileKeepsItsOwner(t *testing.T) {
	const path = "/srv/vaultsync/hub/state.json"
	service := &ownerOf{uid: 1000, gid: 1000}
	root := &ownerOf{uid: 0, gid: 0}
	cases := []struct {
		name      string
		euid      int
		file, dir *ownerOf
		want      fileOwner
		refused   bool
	}{
		{"the service's user writes its own file", 1000, service, service, fileOwner{}, false},
		{"root writes the service's file and keeps its owner", 0, &ownerOf{uid: 1000, gid: 1001}, service, fileOwner{uid: 1000, gid: 1001, change: true}, false},
		{"root writes its own file", 0, root, service, fileOwner{}, false},
		{"another user may not change the owner", 2000, service, service, fileOwner{}, true},
		{"the service's user may not take root's file", 1000, root, service, fileOwner{}, true},
		{"root makes a new file for the folder's owner", 0, nil, &ownerOf{uid: 1000, gid: 1002}, fileOwner{uid: 1000, gid: 1002, change: true}, false},
		{"root makes a new file in root's folder", 0, nil, root, fileOwner{}, false},
		{"root makes a new file where the folder's owner is unknown", 0, nil, nil, fileOwner{}, false},
		{"the service's user makes a new file", 1000, nil, root, fileOwner{}, false},
	}
	for _, c := range cases {
		got, err := decideStateOwner(path, c.euid, c.file, c.dir)
		switch {
		case c.refused && (err == nil || !strings.Contains(err.Error(), "belongs to another user") || !strings.Contains(err.Error(), "docker compose exec hub")):
			t.Errorf("%s: want a refusal, got %+v %v", c.name, got, err)
		case !c.refused && (err != nil || got != c.want):
			t.Errorf("%s: got %+v %v, want %+v", c.name, got, err, c.want)
		}
	}
}

// save hands the new file to the decided owner before the rename, and a
// refusal or a failed hand-over writes nothing.
func TestIssue490_AWriteHandsTheFileToItsOwnerBeforeTheRename(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := newStateStore(path)
	var handed []string
	store.owner = func(string) (fileOwner, error) { return fileOwner{uid: 1000, gid: 1001, change: true}, nil }
	store.chown = func(f *os.File, uid, gid int) error {
		if f.Name() == path {
			t.Errorf("the owner was set on the state file itself, not on the new one")
		}
		if _, err := os.Stat(path); err == nil {
			t.Errorf("the new file was renamed before it was handed over")
		}
		handed = append(handed, filepath.Base(f.Name()))
		return nil
	}
	if _, err := store.update(func(st *hubState) error { st.Devices = append(st.Devices, pairedDevice{DeviceID: "D"}); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(handed) != 1 || !strings.HasPrefix(handed[0], ".state-") {
		t.Fatalf("handed over: %v", handed)
	}
	before, _ := os.ReadFile(path)

	for name, o := range map[string]removeOwnerCase{
		"a refusal":          {decide: func(string) (fileOwner, error) { return fileOwner{}, errors.New("belongs to another user") }},
		"a failed hand-over": {decide: func(string) (fileOwner, error) { return fileOwner{uid: 1, gid: 1, change: true}, nil }, chownErr: errors.New("operation not permitted")},
	} {
		store.owner = o.decide
		store.chown = func(*os.File, int, int) error { return o.chownErr }
		if _, err := store.update(func(st *hubState) error { st.Devices = nil; return nil }); err == nil {
			t.Errorf("%s: the write went through", name)
		}
		if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
			t.Errorf("%s changed the file", name)
		}
		entries, _ := os.ReadDir(filepath.Dir(path))
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".state-") {
				t.Errorf("%s left %s behind", name, e.Name())
			}
		}
	}
}

type removeOwnerCase struct {
	decide   func(string) (fileOwner, error)
	chownErr error
}

// failWrites makes every write of the fixture's state fail, as a full disk
// or a lock not got in time would; restore undoes it.
func failWrites(fx *pairingFixture) (restore func()) {
	saved := fx.store.owner
	fx.store.owner = func(string) (fileOwner, error) { return fileOwner{}, errors.New("no space left on device") }
	return func() { fx.store.owner = saved }
}

func fileFailures(t *testing.T, fx *pairingFixture) int {
	t.Helper()
	st, err := fx.store.load()
	if err != nil {
		t.Fatal(err)
	}
	return st.Pairing.Failures
}

// A failed attempt is counted also when the Hub's state cannot be written.
func TestIssue490_AFailedAttemptCountsWhenTheStateCannotBeWritten(t *testing.T) {
	fx := newPairingFixture(t)
	ctx := context.Background()
	restore := failWrites(fx)
	for i := 1; i <= maxCodeFailures; i++ {
		if _, err := fx.client().Handshake(ctx, "TULIP-ANCHOR-00"); !errors.Is(err, pairing.ErrCodeRejected) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if n := fileFailures(t, fx); n != 0 {
		t.Fatalf("the file took %d counts while its writes failed", n)
	}
	var apiErr *pairing.APIError
	if _, err := fx.client().Handshake(ctx, fx.code); !errors.As(err, &apiErr) || !strings.Contains(apiErr.Body, "locked") {
		t.Fatalf("the right code after %d uncounted wrong ones: %v", maxCodeFailures, err)
	}
	// Writes work again: the next start brings the file level.
	restore()
	if _, err := fx.client().Handshake(ctx, fx.code); !errors.As(err, &apiErr) || !strings.Contains(apiErr.Body, "locked") {
		t.Fatalf("the right code once writes work: %v", err)
	}
	if n := fileFailures(t, fx); n != maxCodeFailures {
		t.Fatalf("the file holds %d failed attempts, want %d", n, maxCodeFailures)
	}
	// A new code starts afresh.
	fx.issueCode(t)
	if _, err := fx.client().Handshake(ctx, fx.code); err != nil {
		t.Fatalf("a new code: %v", err)
	}
}

func TestIssue490_TheFileCatchesUpWithTheCountOnItsNextWrite(t *testing.T) {
	fx := newPairingFixture(t)
	ctx := context.Background()
	restore := failWrites(fx)
	for i := 0; i < 2; i++ {
		_, _ = fx.client().Handshake(ctx, "TULIP-ANCHOR-00")
	}
	restore()
	if _, err := fx.client().Handshake(ctx, "TULIP-ANCHOR-00"); !errors.Is(err, pairing.ErrCodeRejected) {
		t.Fatal(err)
	}
	if n := fileFailures(t, fx); n != 3 {
		t.Fatalf("the file holds %d failed attempts after 2 unwritten and 1 written, want 3", n)
	}
}

// A pairing whose record cannot be written is refused and shares nothing.
func TestIssue490_APairingWhoseRecordCannotBeWrittenSharesNothing(t *testing.T) {
	for _, create := range []bool{false, true} {
		fx := newPairingFixture(t)
		ctx := context.Background()
		if !create {
			if _, err := fx.server.prov.createVault(ctx, "Notes", false); err != nil {
				t.Fatal(err)
			}
		}
		client := fx.client()
		if _, err := client.Handshake(ctx, fx.code); err != nil {
			t.Fatal(err)
		}
		restore := failWrites(fx)
		reply, err := client.Provision(ctx, 1, fakeDeviceID, "Laptop", "Notes", create)
		restore()
		if err != nil || reply.Provisioned != nil || !strings.Contains(reply.Error, "could not record the pairing") {
			t.Fatalf("create=%v: %+v %v", create, reply, err)
		}
		folder := fx.fake.folderByLabel("Notes")
		if folder == nil {
			t.Fatalf("create=%v: no vault", create)
		}
		for _, d := range folder.Devices {
			if d.DeviceID == fakeDeviceID {
				t.Fatalf("create=%v: the vault was shared although the pairing was refused", create)
			}
		}
		if st, _ := fx.store.load(); len(st.Devices) != 0 {
			t.Fatalf("create=%v: records %+v", create, st.Devices)
		}
	}
}
