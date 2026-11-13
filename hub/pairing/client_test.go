package pairing

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/psimaker/vaultsync/hub/pake"
)

// A link-supplied address is checked again at the socket: a Hub address
// outside the local network never sees a single packet (#174).
func TestIssue174_LocalClientRefusesNonLocalPeersAtDial(t *testing.T) {
	start := time.Now()
	_, err := NewLocalClient("192.0.2.1:8390").Handshake(context.Background(), "TULIP-ANCHOR-42")
	if !errors.Is(err, ErrNotLocal) || FailureKind(err) != KindNotLocal {
		t.Fatalf("non-local dial: %v (kind %q)", err, FailureKind(err))
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("refusal waited for the network instead of failing at the socket")
	}
}

// The Hub never redirects; a client that followed one would carry the
// handshake to wherever an answer points.
func TestIssue174_ClientRefusesRedirects(t *testing.T) {
	var elsewhere atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	defer target.Close()
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer hub.Close()
	for _, c := range []*Client{
		NewClient(strings.TrimPrefix(hub.URL, "http://")),
		NewLocalClient(strings.TrimPrefix(hub.URL, "http://")),
	} {
		_, err := c.Handshake(context.Background(), "TULIP-ANCHOR-42")
		if err == nil || !strings.Contains(err.Error(), "redirect") {
			t.Fatalf("redirect followed or wrong error: %v", err)
		}
	}
	if elsewhere.Load() != 0 {
		t.Fatal("a redirect reached its target")
	}
}

func TestFailureKindOfUnknownAndNil(t *testing.T) {
	if FailureKind(nil) != KindNone {
		t.Fatal("nil error has a kind")
	}
	if FailureKind(errors.New("boom")) != KindOther {
		t.Fatal("plain error is not other")
	}
	if FailureKind(&APIError{Status: http.StatusTooManyRequests, Body: "too many pairing attempts from this address — wait a minute"}) != KindRateLimited {
		t.Fatal("429 is not rate limited")
	}
	if FailureKind(&APIError{Status: http.StatusInternalServerError, Body: "hub state unavailable"}) != KindOther {
		t.Fatal("500 is not other")
	}
}

// #319: the text of an error answer comes from whatever answered at the
// address that was dialled — before the key exchange that is anyone — and the
// programs that pair print it. Decision 062.

// errorAnswer runs a handshake against a service that answers every request
// with status and text, and returns what the client makes of it.
func errorAnswer(t *testing.T, status int, text string) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Error: text})
	}))
	defer srv.Close()
	_, err := NewClient(strings.TrimPrefix(srv.URL, "http://")).Handshake(context.Background(), "TULIP-ANCHOR-42")
	if err == nil {
		t.Fatalf("an answer with status %d paired", status)
	}
	return err
}

// oneCleanLine reports what a terminal would not show as written: a control
// character, a format character, a line or paragraph separator, or bytes that
// are no text.
func oneCleanLine(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool {
		return r == utf8.RuneError || unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
	})
}

// The text was handed on as it came: a terminal sequence, a mark that
// reverses what follows, a line separator and a line break reached the
// terminal of whoever typed the code. It now arrives as one line without
// them, and what stood on another line or behind a tab is a word of its own.
func TestIssue319_AnErrorAnswerIsOneCleanLine(t *testing.T) {
	err := errorAnswer(t, http.StatusForbidden,
		"\x1b[2J\u202ethe pairing code was locked\u2028after repeated\r\nwrong\tattempts\u0085\u009b31m \x07")
	const want = "[2Jthe pairing code was locked after repeated wrong attempts 31m"
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden || apiErr.Body != want {
		t.Fatalf("the error text: %+q", err)
	}
	if err.Error() != "hub pairing: HTTP 403: "+want {
		t.Errorf("the line a program prints: %+q", err.Error())
	}
	// The words that tell one refusal from another are still read out of it.
	if k := FailureKind(err); k != KindCodeLocked {
		t.Errorf("the kind of failure: %q", k)
	}
}

// The text could be as long as an answer may be, 64 KiB. It is cut to 300
// bytes, between two characters, and says that it was cut.
func TestIssue319_AnErrorAnswerIsShort(t *testing.T) {
	for _, head := range []string{"unknown or expired pairing session ", "unknown or expired pairing session: "} {
		err := errorAnswer(t, http.StatusForbidden, head+strings.Repeat("я", 30000))
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("%q: %v", head, err)
		}
		body := apiErr.Body
		if len(body) > 300 || len(body) < 290 || !oneCleanLine(body) {
			t.Errorf("%q: %d bytes kept: %+q", head, len(body), body)
		}
		if !strings.HasPrefix(body, head+"яяя") || !strings.HasSuffix(body, "я…") {
			t.Errorf("%q: what was kept: %+q", head, body)
		}
		if k := FailureKind(err); k != KindSessionExpired {
			t.Errorf("%q: the kind of failure: %q", head, k)
		}
	}

	// A text of exactly the length that is kept is not cut, and one byte
	// more is.
	whole := strings.Repeat("a", 300)
	var apiErr *APIError
	if err := errorAnswer(t, http.StatusBadRequest, whole); !errors.As(err, &apiErr) || apiErr.Body != whole {
		t.Errorf("a text of 300 bytes: %+q", err)
	}
	if err := errorAnswer(t, http.StatusBadRequest, whole+"a"); !errors.As(err, &apiErr) || apiErr.Body != strings.Repeat("a", 297)+"…" {
		t.Errorf("a text of 301 bytes: %+q", err)
	}
	// Words are not cut apart from the mark by a space left over.
	spaced := strings.Repeat("a", 296) + " " + strings.Repeat("b", 10)
	if err := errorAnswer(t, http.StatusBadRequest, spaced); !errors.As(err, &apiErr) || apiErr.Body != strings.Repeat("a", 296)+"…" {
		t.Errorf("a cut behind a space: %+q", err)
	}
}

// An answer whose text holds nothing that can be shown is still an error:
// it reads as the status does, like an answer without a text.
func TestIssue319_AnErrorAnswerWithNothingToShowIsItsStatus(t *testing.T) {
	for _, text := range []string{"", "\x1b\u202e\u2028 \t\u200b\r\n", "\u200d \u200c"} {
		err := errorAnswer(t, http.StatusForbidden, text)
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Body != "Forbidden" {
			t.Errorf("%+q: %+q", text, err)
		}
	}
}

// What a Hub words itself arrives as it was sent, and every phrase the
// programs tell a refusal by is found in it — also when something follows
// that is cleaned away or cut off. (The vaultsync-hub command pins these
// texts to its server's real answers.)
func TestIssue319_ARefusalIsStillToldByItsWords(t *testing.T) {
	cases := []struct {
		status int
		text   string
		kind   Kind
	}{
		{http.StatusForbidden, "unknown or expired pairing session", KindSessionExpired},
		{http.StatusForbidden, "the pairing code has expired — run `vaultsync-hub code` for a new one", KindCodeExpired},
		{http.StatusForbidden, "the pairing code was locked after repeated wrong attempts — run `vaultsync-hub code` for a new one", KindCodeLocked},
		{http.StatusForbidden, "no pairing code has been issued — run `vaultsync-hub code`", KindNoCode},
		{http.StatusForbidden, "pairing is only available from the local network", KindNotLocal},
		{http.StatusForbidden, "pairing code rejected", KindCodeRejected},
		{http.StatusTooManyRequests, "too many pairing attempts from this address — wait a minute", KindRateLimited},
		{http.StatusTooManyRequests, "the hub is busy pairing — try again shortly", KindBusy},
		{http.StatusInternalServerError, "hub state unavailable", KindOther},
	}
	for _, c := range cases {
		err := errorAnswer(t, c.status, c.text)
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Body != c.text {
			t.Errorf("%q arrived as %+q", c.text, err)
		}
		if k := FailureKind(err); k != c.kind {
			t.Errorf("%q: kind %q, want %q", c.text, k, c.kind)
		}
		err = errorAnswer(t, c.status, c.text+"\u2028\x1b[1m"+strings.Repeat(" more", 2000))
		if k := FailureKind(err); k != c.kind {
			t.Errorf("%q with a tail: kind %q, want %q", c.text, k, c.kind)
		}
	}
}

// exchangeHub is a pairing service that knows code. It answers finish with
// finishError when that is set, and seals what reply returns into every
// other answer.
func exchangeHub(t *testing.T, code, finishError string, reply func() HubPayload) *Client {
	t.Helper()
	const session = "session-1"
	var (
		mu         sync.Mutex
		state      *pake.State
		hubConfirm []byte
		key        []byte
	)
	fail := func(w http.ResponseWriter, status int, text string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(ErrorResponse{Error: text})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/pair/start", func(w http.ResponseWriter, r *http.Request) {
		var req StartRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		pa, _ := B64Decode(req.PA)
		mu.Lock()
		defer mu.Unlock()
		st, err := pake.NewState(pake.RoleHub, pake.PasswordScalar([]byte(code)))
		if err != nil {
			t.Error(err)
			return
		}
		pb, _ := st.Start()
		confirm, err := st.Finish(pa)
		if err != nil {
			fail(w, http.StatusBadRequest, "malformed key exchange")
			return
		}
		state, hubConfirm = st, confirm
		_ = json.NewEncoder(w).Encode(StartResponse{Session: session, PB: B64Encode(pb)})
	})
	mux.HandleFunc("POST /v1/pair/finish", func(w http.ResponseWriter, r *http.Request) {
		if finishError != "" {
			fail(w, http.StatusForbidden, finishError)
			return
		}
		var req FinishRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		confirm, _ := B64Decode(req.Confirm)
		mu.Lock()
		defer mu.Unlock()
		k, err := state.Verify(confirm)
		if err != nil {
			fail(w, http.StatusForbidden, "pairing code rejected")
			return
		}
		key = k
		box, _ := SealBox(session, key, DirectionHubToDevice, reply())
		_ = json.NewEncoder(w).Encode(FinishResponse{Confirm: B64Encode(hubConfirm), Box: box})
	})
	mux.HandleFunc("POST /v1/pair/provision", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		box, _ := SealBox(session, key, DirectionHubToDevice, reply())
		_ = json.NewEncoder(w).Encode(BoxResponse{Box: box})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return NewClient(strings.TrimPrefix(srv.URL, "http://"))
}

// Only a failed key confirmation is a wrong code, and it is told by one word
// of the answer. That word is found in a text that brings more than it.
func TestIssue319_ARejectedCodeIsStillAWrongCode(t *testing.T) {
	c := exchangeHub(t, "TULIP-ANCHOR-42", "\x1b[1mpairing code\u2028rejected\u202e"+strings.Repeat(" more", 2000), nil)
	if _, err := c.Handshake(context.Background(), "TULIP-ANCHOR-42"); !errors.Is(err, ErrCodeRejected) || FailureKind(err) != KindCodeRejected {
		t.Fatalf("a rejected code: %+q", err)
	}
}

// A Hub that knows the code reports what went wrong on its side inside its
// encrypted answer — what its Syncthing said, for one. The same programs
// print that text, so it is cleaned and cut the same way, and it keeps the
// words a refused registration is told by.
func TestIssue319_TheErrorInAHubsReplyIsOneCleanShortLine(t *testing.T) {
	ctx := context.Background()
	const code = "TULIP-ANCHOR-42"
	var reply atomic.Pointer[HubPayload]
	report := func(text string) {
		reply.Store(&HubPayload{Version: ProtocolVersion, HubDeviceID: "hub", HubName: "Home", Error: text})
	}
	c := exchangeHub(t, code, "", func() HubPayload { return *reply.Load() })

	report("hub could not read its own device ID:\r\n\x1b[2J\u202esyncthing API: HTTP 500")
	hello, err := c.Handshake(ctx, code)
	if err != nil || hello.Error != "hub could not read its own device ID: [2Jsyncthing API: HTTP 500" {
		t.Fatalf("the first answer's error: %+q, %v", hello.Error, err)
	}

	// As long as an encrypted answer can be: its box has to fit in MaxBody.
	report(RegistrationRefusedPrefix + ": syncthing API: HTTP 500:\u2028\x1b[2J" + strings.Repeat("я", 10000))
	got, err := c.Provision(ctx, 1, "device", "Laptop", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Error) > 300 || !oneCleanLine(got.Error) || !strings.HasSuffix(got.Error, "я…") {
		t.Errorf("a later answer's error, %d bytes: %+q", len(got.Error), got.Error)
	}
	if !strings.HasPrefix(got.Error, RegistrationRefusedPrefix+": syncthing API: HTTP 500: [2Jяя") {
		t.Errorf("a refused registration is no longer told by its first words: %+q", got.Error)
	}

	// An error with nothing to show in it is still an error: the programs
	// tell a refusal from a share by whether this text is empty.
	report("\u202e\x1b \u200b")
	if got, err = c.Provision(ctx, 2, "device", "Laptop", "Notes", false); err != nil || got.Error != "the hub reported an error without readable text" {
		t.Errorf("an error with nothing to show: %+q, %v", got.Error, err)
	}
	// And no error stays none.
	report("")
	if got, err = c.Provision(ctx, 3, "device", "Laptop", "Notes", false); err != nil || got.Error != "" {
		t.Errorf("no error: %+q, %v", got.Error, err)
	}
}
