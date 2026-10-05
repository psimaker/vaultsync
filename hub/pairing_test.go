package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/psimaker/vaultsync/hub/pairing"
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
	code, err := pairing.GenerateCode()
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

func (fx *pairingFixture) client() *pairing.Client {
	return pairing.NewClient(strings.TrimPrefix(fx.http.URL, "http://"))
}

// post sends one raw protocol request, for the cases the client refuses to
// produce (a forged session, a second finish).
func (fx *pairingFixture) post(t *testing.T, path string, in any) (int, string) {
	t.Helper()
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(fx.http.URL+path, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var e errorResponse
	_ = json.NewDecoder(resp.Body).Decode(&e)
	return resp.StatusCode, e.Error
}

func TestPairingEndToEndCreatesAndSharesVault(t *testing.T) {
	fx := newPairingFixture(t)
	ctx := context.Background()
	c := fx.client()
	hello, err := c.Handshake(ctx, fx.code)
	if err != nil {
		t.Fatal(err)
	}
	if hello.HubDeviceID != fakeHubID || hello.HubName != "Test Hub" || len(hello.Vaults) != 0 {
		t.Fatalf("hello: %+v", hello)
	}
	reply, err := c.Provision(ctx, 1, fakeDeviceID, "Laptop", "Notes", true)
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
	if _, err := c2.Handshake(ctx, strings.ToLower(strings.ReplaceAll(fx.code, "-", " "))); err == nil {
		t.Fatal("client must only accept the canonical code; normalization is the CLI's job")
	}
	if _, err := c2.Handshake(ctx, fx.code); err != nil {
		t.Fatal(err)
	}
	other := strings.ReplaceAll(fakeDeviceID, "B", "C")
	reply, err = c2.Provision(ctx, 1, other, "Desktop", "notes", false)
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
	if _, err := c.Handshake(context.Background(), fx.code); err != nil {
		t.Fatal(err)
	}
	reply, err := c.Provision(context.Background(), 1, fakeDeviceID, "Laptop", "Ghost", false)
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
		_, err := fx.client().Handshake(ctx, "TULIP-ANCHOR-00")
		if !errors.Is(err, pairing.ErrCodeRejected) {
			t.Fatalf("attempt %d: %v", i, err)
		}
		st, _ := fx.store.load()
		if st.Pairing.Failures != i {
			t.Fatalf("attempt %d: failures=%d", i, st.Pairing.Failures)
		}
	}
	// The right code is now refused too: the code is burnt.
	_, err := fx.client().Handshake(ctx, fx.code)
	var apiErr *pairing.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden || !strings.Contains(apiErr.Body, "locked") {
		t.Fatalf("locked code accepted or wrong error: %v", err)
	}
	if fx.fake.hasDevice(fakeDeviceID) || len(fx.fake.folders) != 0 {
		t.Fatal("wrong codes changed Syncthing state")
	}
	fx.issueCode(t)
	if _, err := fx.client().Handshake(ctx, fx.code); err != nil {
		t.Fatalf("new code after lockout: %v", err)
	}
}

func TestPairingExpiredCodeIsRefused(t *testing.T) {
	fx := newPairingFixture(t)
	fx.now = fx.now.Add(pairingCodeTTL + time.Minute)
	_, err := fx.client().Handshake(context.Background(), fx.code)
	var apiErr *pairing.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden || !strings.Contains(apiErr.Body, "expired") {
		t.Fatalf("expired code: %v", err)
	}
}

func TestPairingNoCodeIssuedIsRefused(t *testing.T) {
	fx := newPairingFixture(t)
	if _, err := fx.store.update(func(st *hubState) error { st.Pairing = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	_, err := fx.client().Handshake(context.Background(), "TULIP-ANCHOR-00")
	var apiErr *pairing.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("no code: %v", err)
	}
}

func TestPairingReplayAndUnauthenticatedProvisionAreRefused(t *testing.T) {
	fx := newPairingFixture(t)
	ctx := context.Background()
	c := fx.client()
	if _, err := c.Handshake(ctx, fx.code); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Provision(ctx, 5, fakeDeviceID, "L", "", false); err != nil {
		t.Fatal(err)
	}
	_, err := c.Provision(ctx, 5, fakeDeviceID, "L", "Notes", true)
	var apiErr *pairing.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("replayed seq accepted: %v", err)
	}
	if len(fx.fake.folders) != 0 {
		t.Fatal("replay provisioned a vault")
	}
	// A client that never finished must not be able to provision: a box
	// sealed under a guessed key does not open on the Hub.
	forged, err := pairing.SealBox(c.SessionID(), make([]byte, 32), pairing.DirectionDeviceToHub, provisionPayload{
		Version: protocolVersion, Seq: 6, DeviceID: fakeDeviceID, Name: "L", Vault: "Notes", Create: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := fx.post(t, "/v1/pair/provision", provisionRequest{Session: c.SessionID(), Box: forged}); status != http.StatusBadRequest {
		t.Fatalf("wrong key accepted: HTTP %d", status)
	}
	// finish twice on the same session is refused (session is authenticated).
	if status, _ := fx.post(t, "/v1/pair/finish", finishRequest{Session: c.SessionID(), Confirm: b64e(make([]byte, 32))}); status != http.StatusForbidden {
		t.Fatalf("second finish: HTTP %d", status)
	}
	if len(fx.fake.folders) != 0 {
		t.Fatal("a refused request provisioned a vault")
	}
}

func TestPairingRateLimitsStartsPerAddress(t *testing.T) {
	fx := newPairingFixture(t)
	ctx := context.Background()
	var lastErr error
	for i := 0; i <= pairingStartsPerMin; i++ {
		_, lastErr = fx.client().Handshake(ctx, "TULIP-ANCHOR-00")
	}
	var apiErr *pairing.APIError
	if !errors.As(lastErr, &apiErr) || apiErr.Status != http.StatusTooManyRequests {
		t.Fatalf("no rate limit after %d starts: %v", pairingStartsPerMin+1, lastErr)
	}
	fx.now = fx.now.Add(2 * time.Minute)
	fx.issueCode(t)
	if _, err := fx.client().Handshake(ctx, fx.code); err != nil {
		t.Fatalf("rate limit did not expire: %v", err)
	}
}

func TestPairingRejectsMalformedDeviceID(t *testing.T) {
	fx := newPairingFixture(t)
	ctx := context.Background()
	c := fx.client()
	if _, err := c.Handshake(ctx, fx.code); err != nil {
		t.Fatal(err)
	}
	_, err := c.Provision(ctx, 1, "not-a-device-id", "L", "Notes", true)
	var apiErr *pairing.APIError
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
	if _, err := c.Handshake(ctx, fx.code); err != nil {
		t.Fatal(err)
	}
	reply, err := c.Provision(ctx, 1, fakeDeviceID, "Laptop", "Notes", true)
	if err != nil || reply.Provisioned == nil {
		t.Fatalf("%+v %v", reply, err)
	}
	if reply.Provisioned.Files != 0 {
		t.Fatalf("fresh vault must report 0 files, got %d", reply.Provisioned.Files)
	}
	fx.fake.mu.Lock()
	fx.fake.dbFiles[reply.Provisioned.ID] = 42
	fx.fake.mu.Unlock()
	reply, err = c.Provision(ctx, 2, fakeDeviceID, "Laptop", "Notes", false)
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

// The iOS app tells a wrong, expired or locked code apart through
// pairing.FailureKind, which reads the server's refusal texts. This pins every
// phrase it relies on to the server's real answer (#174): a reworded refusal
// fails here instead of turning into a generic error on the iPhone.
func TestIssue174_FailureKindsMatchServerTexts(t *testing.T) {
	ctx := context.Background()
	kind := func(err error) pairing.Kind { return pairing.FailureKind(err) }

	fx := newPairingFixture(t)
	if k := kind(func() error { _, err := fx.client().Handshake(ctx, "TULIP-ANCHOR-00"); return err }()); k != pairing.KindCodeRejected {
		t.Fatalf("wrong code: %q", k)
	}

	expired := newPairingFixture(t)
	expired.now = expired.now.Add(pairingCodeTTL + time.Minute)
	if k := kind(func() error { _, err := expired.client().Handshake(ctx, expired.code); return err }()); k != pairing.KindCodeExpired {
		t.Fatalf("expired code: %q", k)
	}

	locked := newPairingFixture(t)
	for range maxCodeFailures {
		_, _ = locked.client().Handshake(ctx, "TULIP-ANCHOR-00")
	}
	if k := kind(func() error { _, err := locked.client().Handshake(ctx, locked.code); return err }()); k != pairing.KindCodeLocked {
		t.Fatalf("locked code: %q", k)
	}

	none := newPairingFixture(t)
	if _, err := none.store.update(func(st *hubState) error { st.Pairing = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	if k := kind(func() error { _, err := none.client().Handshake(ctx, "TULIP-ANCHOR-00"); return err }()); k != pairing.KindNoCode {
		t.Fatalf("no code: %q", k)
	}

	limited := newPairingFixture(t)
	var lastErr error
	for i := 0; i <= pairingStartsPerMin; i++ {
		_, lastErr = limited.client().Handshake(ctx, "TULIP-ANCHOR-00")
	}
	if k := kind(lastErr); k != pairing.KindRateLimited {
		t.Fatalf("rate limited: %q", k)
	}

	busy := newPairingFixture(t)
	busy.server.mu.Lock()
	for i := range pairingMaxSessions {
		busy.server.sessions[strconv.Itoa(i)] = &pairingSession{id: strconv.Itoa(i), expires: busy.now.Add(time.Hour)}
	}
	busy.server.mu.Unlock()
	if k := kind(func() error { _, err := busy.client().Handshake(ctx, busy.code); return err }()); k != pairing.KindBusy {
		t.Fatalf("busy: %q", k)
	}

	// The Internet-facing refusal comes from the handler wrapper; replay its
	// exact answer through the client's error type.
	req := httptest.NewRequest(http.MethodPost, "/v1/pair/start", strings.NewReader("{}"))
	req.RemoteAddr = "203.0.113.9:4444"
	rec := httptest.NewRecorder()
	fx.server.handler().ServeHTTP(rec, req)
	var e errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	if k := kind(&pairing.APIError{Status: rec.Code, Body: e.Error}); k != pairing.KindNotLocal {
		t.Fatalf("not local (%d %q): %q", rec.Code, e.Error, k)
	}

	// The Hub forgets a session after pairingSessionTTL: the user lingered on
	// the vault choice. That is not a wrong code and must not read as one.
	slow := newPairingFixture(t)
	sc := slow.client()
	if _, err := sc.Handshake(ctx, slow.code); err != nil {
		t.Fatal(err)
	}
	slow.now = slow.now.Add(pairingSessionTTL + time.Second)
	if k := kind(func() error { _, err := sc.Provision(ctx, 1, fakeDeviceID, "L", "Notes", false); return err }()); k != pairing.KindSessionExpired {
		t.Fatalf("expired session at provision: %q", k)
	}
	if status, body := slow.post(t, "/v1/pair/finish", finishRequest{Session: sc.SessionID(), Confirm: b64e(make([]byte, 32))}); status != http.StatusForbidden ||
		kind(&pairing.APIError{Status: status, Body: body}) != pairing.KindSessionExpired {
		t.Fatalf("expired session at finish: HTTP %d %q", status, body)
	}

	closed := httptest.NewServer(http.NotFoundHandler())
	addr := strings.TrimPrefix(closed.URL, "http://")
	closed.Close()
	if k := kind(func() error { _, err := pairing.NewClient(addr).Handshake(ctx, fx.code); return err }()); k != pairing.KindUnreachable {
		t.Fatalf("unreachable: %q", k)
	}
}
