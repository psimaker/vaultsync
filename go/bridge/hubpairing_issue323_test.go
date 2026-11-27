package bridge

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/psimaker/vaultsync/hub/pairing"
)

// #323: what the pairing client's HTTP library says about an answer that is
// no proper HTTP reached the app's log as long as it came. Decision 062.

// rawPairingAnswer listens on loopback and hands each connection to answer,
// which may send anything; the connection is closed once answer returns.
func rawPairingAnswer(t *testing.T, answer func(conn net.Conn, br *bufio.Reader)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				answer(conn, bufio.NewReader(conn))
			}()
		}
	}()
	return ln.Addr().String()
}

func readPairingRequest(br *bufio.Reader) {
	if req, err := http.ReadRequest(br); err == nil {
		_, _ = io.Copy(io.Discard, req.Body)
	}
}

func sendingRaw(raw string) func(net.Conn, *bufio.Reader) {
	return func(conn net.Conn, br *bufio.Reader) {
		readPairingRequest(br)
		_, _ = io.WriteString(conn, raw)
	}
}

// A terminal sequence, a mark that reverses what follows, a line separator,
// and far more than a line.
var issue323Dirt = "\x1b[2J\u202e" + strings.Repeat("я", 1000) + "\u2028x"

func TestIssue323_AnAnswerThatIsNoProperHTTPReachesTheAppAsOneCleanShortLine(t *testing.T) {
	addr := rawPairingAnswer(t, sendingRaw(issue323Dirt+"\r\n\r\n"))
	flow := HubPairingBegin()
	defer HubPairingEnd(flow)
	e := decodeHubEnvelope(t, HubPairingHandshake(flow, addr, "TULIP-ANCHOR-42"))
	// Sorted as before: the app picks its words by this kind.
	if e.OK || e.Error.Kind != string(pairing.KindUnreachable) {
		t.Fatalf("the kind of failure: %+v", e.Error)
	}
	msg := e.Error.Message
	if !strings.HasPrefix(msg, `Post "http://`+addr+`/v1/pair/start": `) || !strings.HasSuffix(msg, "…") {
		t.Errorf("the message: %.400q", msg)
	}
	if len(msg) > 300 || strings.ContainsFunc(msg, func(r rune) bool {
		return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
	}) {
		t.Errorf("the message is %d bytes, or holds what is not shown as written: %.400q", len(msg), msg)
	}
}

// The bridge sorts a failed provision by the errors in its chain: a request
// that never left is a definite failure, one that may have left leaves the
// outcome unknown. Each way a request can fail is sorted as it was, through
// the real client. A handshake's errors stand for a provision's here: both
// come from the same request code.
func TestIssue323_AFailureOfTheRealClientIsSortedAsBefore(t *testing.T) {
	gone, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refused := gone.Addr().String()
	_ = gone.Close()
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	answer := func(status, body string) string {
		return fmt.Sprintf("HTTP/1.1 %s\r\nContent-Length: %d\r\n\r\n%s", status, len(body), body)
	}
	cases := []struct {
		name          string
		client        *pairing.Client
		wait          time.Duration
		kind, outcome string // FailureKind's, hubProvisionFailureKind's
	}{
		{"a port nobody listens on", pairing.NewLocalClient(refused), 0, "unreachable", "unreachable"},
		{"an address outside the local network", pairing.NewLocalClient("192.0.2.1:8390"), 0, "notLocal", "notLocal"},
		{"a connection closed behind the request", pairing.NewLocalClient(rawPairingAnswer(t, func(_ net.Conn, br *bufio.Reader) { readPairingRequest(br) })), 0, "unreachable", hubKindOutcomeUnknown},
		{"an answer that does not come in time", pairing.NewLocalClient(rawPairingAnswer(t, func(_ net.Conn, br *bufio.Reader) { readPairingRequest(br); <-hang })), 300 * time.Millisecond, "unreachable", hubKindOutcomeUnknown},
		{"an answer that is no proper HTTP", pairing.NewLocalClient(rawPairingAnswer(t, sendingRaw(issue323Dirt+"\r\n\r\n"))), 0, "unreachable", hubKindOutcomeUnknown},
		{"an answer that is no JSON", pairing.NewLocalClient(rawPairingAnswer(t, sendingRaw(answer("200 OK", "\x1b[2J<html>"+issue323Dirt)))), 0, "other", hubKindOutcomeUnknown},
		{"a session the Hub no longer knows", pairing.NewLocalClient(rawPairingAnswer(t, sendingRaw(answer("403 Forbidden", `{"error":"unknown or expired pairing session"}`)))), 0, "sessionExpired", "sessionExpired"},
		{"a server error after the Hub acted", pairing.NewLocalClient(rawPairingAnswer(t, sendingRaw(answer("500 Internal Server Error", `{"error":"encryption failed"}`)))), 0, "other", hubKindOutcomeUnknown},
	}
	for _, c := range cases {
		ctx, cancel := context.WithCancel(context.Background())
		if c.wait > 0 {
			ctx, cancel = context.WithTimeout(context.Background(), c.wait)
		}
		_, err := c.client.Handshake(ctx, "TULIP-ANCHOR-42")
		cancel()
		if err == nil {
			t.Fatalf("%s: paired", c.name)
		}
		if k := string(pairing.FailureKind(err)); k != c.kind {
			t.Errorf("%s: kind %q, want %q: %v", c.name, k, c.kind, err)
		}
		if o := hubProvisionFailureKind(err); o != c.outcome {
			t.Errorf("%s: a provision would be %q, want %q: %v", c.name, o, c.outcome, err)
		}
	}
}
