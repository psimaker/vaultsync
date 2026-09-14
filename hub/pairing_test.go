package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type pairingFixture struct {
	fake   *fakeSyncthing
	store  *stateStore
	server *pairingServer
	http   *httptest.Server
	code   string
	now    time.Time
}

func newPairingFixture(t *testing.T) *pairingFixture {
	t.Helper()
	fake, st := newFakeSyncthing(t, fakeHubID)
	prov := newTestProvisioner(t, fake, fake.client(st))
	store := newStateStore(filepath.Join(t.TempDir(), "state.json"))
	srv := newPairingServer(store, prov, "Test Hub", "test")
	fx := &pairingFixture{fake: fake, store: store, server: srv, now: time.Now()}
	srv.now = func() time.Time { return fx.now }
	srv.logf = func(string, ...any) {}
	fx.http = httptest.NewServer(srv.handler())
	t.Cleanup(fx.http.Close)
	fx.issueCode(t)
	return fx
}

func (fx *pairingFixture) issueCode(t *testing.T) {
	t.Helper()
	code, err := generateCode()
	if err != nil {
		t.Fatal(err)
	}
	fx.code = code
	if _, err := fx.store.update(func(st *hubState) error {
		st.Pairing = &pairingCodeState{Scalar: passwordScalarForCode(code), CreatedAt: fx.now, ExpiresAt: fx.now.Add(pairingCodeTTL)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func (fx *pairingFixture) client() *pairClient {
	return newPairClient(strings.TrimPrefix(fx.http.URL, "http://"))
}

func TestPairingEndToEndCreatesAndSharesVault(t *testing.T) {
	fx := newPairingFixture(t)
	ctx := context.Background()
	c := fx.client()
	hello, err := c.handshake(ctx, fx.code)
	if err != nil {
		t.Fatal(err)
	}
	if hello.HubDeviceID != fakeHubID || hello.HubName != "Test Hub" || len(hello.Vaults) != 0 {
		t.Fatalf("hello: %+v", hello)
	}
	reply, err := c.provision(ctx, 1, fakeDeviceID, "Laptop", "Notes", true)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Error != "" || reply.Provisioned == nil || reply.Provisioned.Label != "Notes" {
		t.Fatalf("provision reply: %+v", reply)
	}
	folder := fx.fake.folderByLabel("Notes")
	if folder == nil || len(folder.Devices) != 2 || folder.Devices[1].DeviceID != fakeDeviceID {
		t.Fatalf("vault not shared with the device: %+v", folder)
	}
	if !fx.fake.hasDevice(fakeDeviceID) {
		t.Fatal("device not added to Syncthing")
	}
	st, _ := fx.store.load()
	if len(st.Devices) != 1 || st.Devices[0].Name != "Laptop" || st.Devices[0].Vaults[0] != "Notes" {
		t.Fatalf("hub state: %+v", st.Devices)
	}
	// A second device joins the existing vault without creating anything.
	c2 := fx.client()
	if _, err := c2.handshake(ctx, strings.ToLower(strings.ReplaceAll(fx.code, "-", " "))); err == nil {
		t.Fatal("client must only accept the canonical code; normalization is the CLI's job")
	}
	if _, err := c2.handshake(ctx, fx.code); err != nil {
		t.Fatal(err)
	}
	other := strings.ReplaceAll(fakeDeviceID, "B", "C")
	reply, err = c2.provision(ctx, 1, other, "Desktop", "notes", false)
	if err != nil || reply.Error != "" {
		t.Fatalf("join: %+v %v", reply, err)
	}
	if folder := fx.fake.folderByLabel("Notes"); len(folder.Devices) != 3 {
		t.Fatalf("second device not added: %+v", folder.Devices)
	}
	if reply.Vaults[0].Files != 0 || len(reply.Vaults[0].SharedWith) != 2 {
		t.Fatalf("vault list in reply: %+v", reply.Vaults)
	}
}

func TestPairingUnknownVaultWithoutCreateIsReportedNotCreated(t *testing.T) {
	fx := newPairingFixture(t)
	c := fx.client()
	if _, err := c.handshake(context.Background(), fx.code); err != nil {
		t.Fatal(err)
	}
	reply, err := c.provision(context.Background(), 1, fakeDeviceID, "Laptop", "Ghost", false)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Error == "" || reply.Provisioned != nil || len(fx.fake.folders) != 0 {
		t.Fatalf("unknown vault must not be created: %+v", reply)
	}
	if !fx.fake.hasDevice(fakeDeviceID) {
		t.Fatal("device registration must still happen")
	}
}

func TestPairingWrongCodeIsCountedAndLocksAfterLimit(t *testing.T) {
	fx := newPairingFixture(t)
	ctx := context.Background()
	for i := 1; i <= maxCodeFailures; i++ {
		_, err := fx.client().handshake(ctx, "TULIP-ANCHOR-00")
		if !errors.Is(err, errCodeRejected) {
			t.Fatalf("attempt %d: %v", i, err)
		}
		st, _ := fx.store.load()
		if st.Pairing.Failures != i {
			t.Fatalf("attempt %d: failures=%d", i, st.Pairing.Failures)
		}
	}
	// The right code is now refused too: the code is burnt.
	_, err := fx.client().handshake(ctx, fx.code)
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden || !strings.Contains(apiErr.Body, "locked") {
		t.Fatalf("locked code accepted or wrong error: %v", err)
	}
	if fx.fake.hasDevice(fakeDeviceID) || len(fx.fake.folders) != 0 {
		t.Fatal("wrong codes changed Syncthing state")
	}
	fx.issueCode(t)
	if _, err := fx.client().handshake(ctx, fx.code); err != nil {
		t.Fatalf("new code after lockout: %v", err)
	}
}

func TestPairingExpiredCodeIsRefused(t *testing.T) {
	fx := newPairingFixture(t)
	fx.now = fx.now.Add(pairingCodeTTL + time.Minute)
	_, err := fx.client().handshake(context.Background(), fx.code)
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden || !strings.Contains(apiErr.Body, "expired") {
		t.Fatalf("expired code: %v", err)
	}
}

func TestPairingNoCodeIssuedIsRefused(t *testing.T) {
	fx := newPairingFixture(t)
	if _, err := fx.store.update(func(st *hubState) error { st.Pairing = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	_, err := fx.client().handshake(context.Background(), "TULIP-ANCHOR-00")
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("no code: %v", err)
	}
}

func TestPairingReplayAndUnauthenticatedProvisionAreRefused(t *testing.T) {
	fx := newPairingFixture(t)
	ctx := context.Background()
	c := fx.client()
	if _, err := c.handshake(ctx, fx.code); err != nil {
		t.Fatal(err)
	}
	if _, err := c.provision(ctx, 5, fakeDeviceID, "L", "", false); err != nil {
		t.Fatal(err)
	}
	_, err := c.provision(ctx, 5, fakeDeviceID, "L", "Notes", true)
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("replayed seq accepted: %v", err)
	}
	if len(fx.fake.folders) != 0 {
		t.Fatal("replay provisioned a vault")
	}
	// A client that never finished must not be able to provision.
	fresh := fx.client()
	fresh.sess = &pairingSession{id: c.session, key: make([]byte, 32)}
	fresh.session = c.session
	_, err = fresh.provision(ctx, 6, fakeDeviceID, "L", "Notes", true)
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("wrong key accepted: %v", err)
	}
	// finish twice on the same session is refused (session is authenticated).
	var out finishResponse
	err = c.post(ctx, "/v1/pair/finish", finishRequest{Session: c.session, Confirm: b64e(make([]byte, 32))}, &out)
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("second finish: %v", err)
	}
}

func TestPairingRateLimitsStartsPerAddress(t *testing.T) {
	fx := newPairingFixture(t)
	ctx := context.Background()
	var lastErr error
	for i := 0; i <= pairingStartsPerMin; i++ {
		_, lastErr = fx.client().handshake(ctx, "TULIP-ANCHOR-00")
	}
	var apiErr *apiError
	if !errors.As(lastErr, &apiErr) || apiErr.Status != http.StatusTooManyRequests {
		t.Fatalf("no rate limit after %d starts: %v", pairingStartsPerMin+1, lastErr)
	}
	fx.now = fx.now.Add(2 * time.Minute)
	fx.issueCode(t)
	if _, err := fx.client().handshake(ctx, fx.code); err != nil {
		t.Fatalf("rate limit did not expire: %v", err)
	}
}

func TestPairingRejectsMalformedDeviceID(t *testing.T) {
	fx := newPairingFixture(t)
	ctx := context.Background()
	c := fx.client()
	if _, err := c.handshake(ctx, fx.code); err != nil {
		t.Fatal(err)
	}
	_, err := c.provision(ctx, 1, "not-a-device-id", "L", "Notes", true)
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("malformed device id: %v", err)
	}
	if len(fx.fake.devices) != 1 {
		t.Fatal("malformed device id reached Syncthing")
	}
}

func TestBoxesAreDirectionAndSessionBound(t *testing.T) {
	key := make([]byte, 32)
	a := &pairingSession{id: "s1", key: key}
	b := &pairingSession{id: "s2", key: key}
	box, err := sealBox(a, "device->hub", map[string]string{"x": "y"})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]string
	if err := openBox(a, "hub->device", box, &out); err == nil {
		t.Fatal("box reflected to the other direction opened")
	}
	if err := openBox(b, "device->hub", box, &out); err == nil {
		t.Fatal("box replayed into another session opened")
	}
	if err := openBox(a, "device->hub", box, &out); err != nil || out["x"] != "y" {
		t.Fatalf("legit open: %v %v", err, out)
	}
}

func TestLooksLikeDeviceID(t *testing.T) {
	if !looksLikeDeviceID(fakeHubID) {
		t.Fatal("canonical id rejected")
	}
	for _, bad := range []string{"", fakeHubID[:55], strings.ToLower(fakeHubID), strings.ReplaceAll(fakeHubID, "A", "1"), fakeHubID + "-AAAAAAA"} {
		if looksLikeDeviceID(bad) {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestProvisionedVaultReportsHubFileCount(t *testing.T) {
	fx := newPairingFixture(t)
	ctx := context.Background()
	c := fx.client()
	if _, err := c.handshake(ctx, fx.code); err != nil {
		t.Fatal(err)
	}
	reply, err := c.provision(ctx, 1, fakeDeviceID, "Laptop", "Notes", true)
	if err != nil || reply.Provisioned == nil {
		t.Fatalf("%+v %v", reply, err)
	}
	if reply.Provisioned.Files != 0 {
		t.Fatalf("fresh vault must report 0 files, got %d", reply.Provisioned.Files)
	}
	fx.fake.mu.Lock()
	fx.fake.dbFiles[reply.Provisioned.ID] = 42
	fx.fake.mu.Unlock()
	reply, err = c.provision(ctx, 2, fakeDeviceID, "Laptop", "Notes", false)
	if err != nil || reply.Provisioned == nil {
		t.Fatalf("%+v %v", reply, err)
	}
	if reply.Provisioned.Files != 42 {
		t.Fatalf("existing vault must report the Hub's file count, got %d", reply.Provisioned.Files)
	}
}

func TestPairingRefusesNonPrivateSourceAddresses(t *testing.T) {
	fx := newPairingFixture(t)
	h := fx.server.handler()
	for _, tc := range []struct {
		remote string
		want   int
	}{
		{"203.0.113.9:4444", http.StatusForbidden},
		{"[2001:db8::1]:4444", http.StatusForbidden},
		{"192.168.1.20:4444", http.StatusOK},
		{"10.0.0.7:4444", http.StatusOK},
		{"[fe80::1]:4444", http.StatusOK},
		{"127.0.0.1:4444", http.StatusOK},
		{"garbage", http.StatusForbidden},
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/hub", nil)
		req.RemoteAddr = tc.remote
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("remote %s: got %d, want %d", tc.remote, rec.Code, tc.want)
		}
	}
}
