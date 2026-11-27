package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/psimaker/vaultsync/hub/pairing"
	"github.com/psimaker/vaultsync/hub/pake"
)

// #490, decision 097: who the state file and its lock file belong to, and
// what a writer does about it, decided from the effective user and the
// owners on disk. Tested here for every case; the real runs as root are
// TestIssue490_AsRoot… in issue490_test.go.
func TestIssue490_BothFilesBelongToTheServicesUser(t *testing.T) {
	service := &ownerOf{uid: 1000, gid: 1000}
	root := &ownerOf{uid: 0, gid: 0}
	cases := []struct {
		name      string
		euid      int
		file, dir *ownerOf
		want      ownerOf
	}{
		{"an existing file keeps its owner", 1000, &ownerOf{uid: 1000, gid: 1001}, service, ownerOf{uid: 1000, gid: 1001}},
		{"also when root writes it", 0, &ownerOf{uid: 1000, gid: 1001}, root, ownerOf{uid: 1000, gid: 1001}},
		{"a file left to root goes back to its folder's owner", 0, root, &ownerOf{uid: 1000, gid: 1002}, ownerOf{uid: 1000, gid: 1002}},
		{"also when the service's user finds it", 1000, root, &ownerOf{uid: 1000, gid: 1002}, ownerOf{uid: 1000, gid: 1002}},
		{"root's file in root's folder stays root's", 0, root, root, *root},
		{"root's file where the folder's owner is unknown stays root's", 0, root, nil, *root},
		{"root makes a new file for the folder's owner", 0, nil, &ownerOf{uid: 1000, gid: 1002}, ownerOf{uid: 1000, gid: 1002}},
		{"root makes a new file in root's folder for itself", 0, nil, root, ownerOf{uid: 0, gid: -1}},
		{"another user makes a new file for itself", 1000, nil, root, ownerOf{uid: 1000, gid: -1}},
	}
	for _, c := range cases {
		if got := stateTarget(c.euid, c.file, c.dir); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestIssue490_AWriterHandsOverOrIsRefused(t *testing.T) {
	const path = "/srv/vaultsync/hub/state.json.lock"
	service := ownerOf{uid: 1000, gid: 1000}
	cases := []struct {
		name       string
		euid       int
		have, want ownerOf
		got        fileOwner
		refused    bool
	}{
		{"the service's user and its own file", 1000, service, service, fileOwner{}, false},
		{"a group of its own is no reason to refuse", 1000, ownerOf{uid: 1000, gid: 1001}, service, fileOwner{}, false},
		{"root hands its new file to the service's user", 0, ownerOf{uid: 0, gid: 0}, service, fileOwner{uid: 1000, gid: 1000, change: true}, false},
		{"root puts a stray owner right", 0, ownerOf{uid: 2000, gid: 2000}, service, fileOwner{uid: 1000, gid: 1000, change: true}, false},
		{"root and a file that is right", 0, service, service, fileOwner{}, false},
		{"root keeps the writer's group where none is wanted", 0, ownerOf{uid: 0, gid: 0}, ownerOf{uid: 1000, gid: -1}, fileOwner{uid: 1000, gid: 0, change: true}, false},
		{"the service's user and root's lock", 1000, ownerOf{uid: 0, gid: 0}, service, fileOwner{}, true},
		{"another user writing the service's file", 2000, ownerOf{uid: 2000, gid: 2000}, service, fileOwner{}, true},
	}
	for _, c := range cases {
		got, err := decideOwner(path, c.euid, c.have, c.want)
		switch {
		case c.refused && (err == nil || !strings.Contains(err.Error(), "nothing was changed") ||
			!strings.Contains(err.Error(), "sudo chown --reference=hub hub/state.json hub/state.json.lock") ||
			!strings.Contains(err.Error(), "docker compose run --rm --no-deps -u 0 hub code")):
			t.Errorf("%s: want a refusal that names the way back, got %+v %v", c.name, got, err)
		case !c.refused && (err != nil || got != c.got):
			t.Errorf("%s: got %+v %v, want %+v", c.name, got, err, c.got)
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

// A pairing whose record cannot be written is refused before the device is
// registered or a vault created: it leaves nothing behind, and says so.
func TestIssue490_APairingWhoseRecordCannotBeWrittenLeavesNothing(t *testing.T) {
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
		if err != nil || reply.Provisioned != nil || !strings.Contains(reply.Error, "could not record the pairing") || !strings.Contains(reply.Error, "nothing was changed") {
			t.Fatalf("create=%v: %+v %v", create, reply, err)
		}
		if fx.fake.hasDevice(fakeDeviceID) {
			t.Fatalf("create=%v: the device was registered", create)
		}
		folder := fx.fake.folderByLabel("Notes")
		if create && folder != nil {
			t.Fatalf("create=%v: a vault was created: %+v", create, folder)
		}
		if !create && len(folder.Devices) != 1 {
			t.Fatalf("create=%v: the vault is shared with %+v", create, folder.Devices)
		}
		if st, _ := fx.store.load(); len(st.Devices) != 0 {
			t.Fatalf("create=%v: records %+v", create, st.Devices)
		}
	}
}

// A step after the record that fails says what exists by then.
func TestIssue490_AStepAfterTheRecordSaysWhatExists(t *testing.T) {
	fx := newPairingFixture(t)
	ctx := context.Background()
	client := fx.client()
	if _, err := client.Handshake(ctx, fx.code); err != nil {
		t.Fatal(err)
	}
	fx.fake.failDeviceAdd = true
	reply, err := client.Provision(ctx, 1, fakeDeviceID, "Laptop", "Notes", true)
	if err != nil || reply.Provisioned != nil || !strings.HasPrefix(reply.Error, pairing.RegistrationRefusedPrefix) ||
		!strings.Contains(reply.Error, "the Hub recorded this pairing; nothing else was changed") {
		t.Fatalf("a registration that fails after the record: %+v %v", reply, err)
	}
	if fx.fake.folderByLabel("Notes") != nil {
		t.Fatal("a vault was created for a device the Hub could not register")
	}
}

// --- the count of wrong attempts -------------------------------------------

// openSession starts a pairing session for code and returns it with the
// confirmation to finish it with.
func (fx *pairingFixture) openSession(t *testing.T, code string) (id string, confirm []byte) {
	t.Helper()
	dev, err := pake.NewState(pake.RoleDevice, pake.PasswordScalar([]byte(code)))
	if err != nil {
		t.Fatal(err)
	}
	pa, err := dev.Start()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(pairing.StartRequest{PA: pairing.B64Encode(pa)})
	resp, err := http.Post(fx.http.URL+"/v1/pair/start", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var started pairing.StartResponse
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&started) != nil {
		t.Fatalf("start: HTTP %d", resp.StatusCode)
	}
	pb, err := pairing.B64Decode(started.PB)
	if err != nil {
		t.Fatal(err)
	}
	if confirm, err = dev.Finish(pb); err != nil {
		t.Fatal(err)
	}
	return started.Session, confirm
}

// finishSession sends a session's finish; safe from any goroutine.
func (fx *pairingFixture) finishSession(id string, confirm []byte) (int, string, error) {
	body, _ := json.Marshal(pairing.FinishRequest{Session: id, Confirm: pairing.B64Encode(confirm)})
	resp, err := http.Post(fx.http.URL+"/v1/pair/finish", "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	var e errorResponse
	_ = json.NewDecoder(resp.Body).Decode(&e)
	return resp.StatusCode, e.Error, nil
}

// The limit on wrong codes is held at every attempt: attempts finished at
// once count one step each, and exactly maxCodeFailures of them are taken
// before the code locks — with the state file written or not.
func TestIssue490_TheLimitIsHeldAtEveryAttempt(t *testing.T) {
	for _, writable := range []bool{true, false} {
		fx := newPairingFixture(t)
		const sessions = 10
		ids := make([]string, sessions)
		confirms := make([][]byte, sessions)
		for i := range ids {
			ids[i], confirms[i] = fx.openSession(t, "TULIP-ANCHOR-00")
		}
		if !writable {
			defer failWrites(fx)()
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		rejected, locked := 0, 0
		for i := range ids {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				status, msg, err := fx.finishSession(ids[i], confirms[i])
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err != nil || status != http.StatusForbidden:
					t.Errorf("finish %d: HTTP %d %q %v", i, status, msg, err)
				case strings.Contains(msg, "rejected"):
					rejected++
				case strings.Contains(msg, "locked"):
					locked++
				default:
					t.Errorf("finish %d: %q", i, msg)
				}
			}(i)
		}
		wg.Wait()
		if rejected != maxCodeFailures || locked != sessions-maxCodeFailures {
			t.Fatalf("writable=%v: %d attempts tried and %d refused as locked, want %d and %d", writable, rejected, locked, maxCodeFailures, sessions-maxCodeFailures)
		}
		if n := fx.server.failures[string(passwordScalarForCode(fx.code))]; n != maxCodeFailures {
			t.Fatalf("writable=%v: counted %d", writable, n)
		}
		want := maxCodeFailures
		if !writable {
			want = 0
		}
		if n := fileFailures(t, fx); n != want {
			t.Fatalf("writable=%v: the state file holds %d failed attempts, want %d", writable, n, want)
		}
	}
}

// Wrong attempts counted at once give the exact count, one step each.
func TestIssue490_TheCountIsExactUnderConcurrentWrongAttempts(t *testing.T) {
	fx := newPairingFixture(t)
	ids := make([]string, 3)
	confirms := make([][]byte, 3)
	for i := range ids {
		ids[i], confirms[i] = fx.openSession(t, "TULIP-ANCHOR-00")
	}
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, _ = fx.finishSession(ids[i], confirms[i])
		}(i)
	}
	wg.Wait()
	if n := fileFailures(t, fx); n != 3 {
		t.Fatalf("three wrong attempts at once were counted as %d", n)
	}
	// Two more: the fifth locks the code, not an earlier one.
	if _, err := fx.client().Handshake(context.Background(), "TULIP-ANCHOR-00"); !errors.Is(err, pairing.ErrCodeRejected) {
		t.Fatalf("the fourth attempt: %v", err)
	}
	if _, err := fx.client().Handshake(context.Background(), "TULIP-ANCHOR-00"); !errors.Is(err, pairing.ErrCodeRejected) {
		t.Fatalf("the fifth attempt: %v", err)
	}
	var apiErr *pairing.APIError
	if _, err := fx.client().Handshake(context.Background(), fx.code); !errors.As(err, &apiErr) || !strings.Contains(apiErr.Body, "locked") {
		t.Fatalf("after five: %v", err)
	}
}

// A session from an older code counts against that code only: it neither
// clears nor changes the count of the code that is open now.
func TestIssue490_AnOlderCodesSessionLeavesTheCurrentCountAlone(t *testing.T) {
	fx := newPairingFixture(t)
	oldID, oldConfirm := fx.openSession(t, "TULIP-ANCHOR-00")
	fx.issueCode(t)
	defer failWrites(fx)()
	for i := 1; i <= maxCodeFailures-1; i++ {
		if _, err := fx.client().Handshake(context.Background(), "TULIP-ANCHOR-00"); !errors.Is(err, pairing.ErrCodeRejected) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if status, msg, err := fx.finishSession(oldID, oldConfirm); err != nil || status != http.StatusForbidden {
		t.Fatalf("the older code's session: HTTP %d %q %v", status, msg, err)
	}
	// The open code's fifth attempt is still the one that locks it.
	if _, err := fx.client().Handshake(context.Background(), "TULIP-ANCHOR-00"); !errors.Is(err, pairing.ErrCodeRejected) {
		t.Fatalf("the fifth attempt on the open code: %v", err)
	}
	var apiErr *pairing.APIError
	if _, err := fx.client().Handshake(context.Background(), fx.code); !errors.As(err, &apiErr) || !strings.Contains(apiErr.Body, "locked") {
		t.Fatalf("after five on the open code: %v", err)
	}
}
