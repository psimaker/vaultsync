package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/psimaker/vaultsync/hub/pairing"
)

// #323: bytes that whatever answers at the Hub's address sends outside an
// answer made Go's transport write a line to the agent's log — the terminal
// of whoever ran vaultsync pair. And the failures the agent sorts come from
// the pairing client, whose errors now carry a cut text: they are sorted as
// before. Decision 062.

// stdLog collects what the standard logger writes while a test runs.
type stdLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *stdLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *stdLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func captureStdLog(t *testing.T) *stdLog {
	t.Helper()
	l := &stdLog{}
	old := log.Writer()
	log.SetOutput(l)
	t.Cleanup(func() { log.SetOutput(old) })
	return l
}

// What came from the address: a terminal sequence, a mark that reverses
// what follows, a line separator, and far more than a line.
var issue323Dirt = "\x1b[2J\u202e" + strings.Repeat("я", 1000) + "\u2028x"

// Each of the Hub's answers came with bytes behind it. They stayed on the
// connection the agent kept for its next request, and went to the log as
// "Unsolicited response received on idle HTTP channel …" — or were read as
// the next answer. Each request now has a connection of its own: the
// pairing goes through, and nothing reaches the log.
func TestIssue323_PairingWritesNoLogLineForBytesBehindTheHubsAnswers(t *testing.T) {
	logged := captureStdLog(t)
	const marker = "bytes-behind-the-answer"
	eng := newFakeEngine(t)
	hub := newFakeHub(t, eng, pairing.VaultInfo{ID: "vs-aaaaaaaaaaaa", Label: "Recipes", Files: 12})
	hub.mu.Lock()
	hub.trailing = "\x1b[2J" + marker + issue323Dirt
	hub.mu.Unlock()
	s, out := testSession(t, eng, hub, pairOptions{}, hub.code, "1", "")
	if err := s.run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if eng.folderCount() != 1 || eng.folders[0].ID != "vs-aaaaaaaaaaaa" {
		t.Errorf("folders: %+v", eng.folders)
	}
	if strings.Contains(logged.String(), marker) {
		t.Errorf("the log: %.400q", logged.String())
	}
}

// rawAnswer listens on loopback and hands each connection to answer, which
// may send anything; the connection is closed once answer returns.
func rawAnswer(t *testing.T, answer func(conn net.Conn, br *bufio.Reader)) string {
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

func readTheRequest(br *bufio.Reader) {
	if req, err := http.ReadRequest(br); err == nil {
		_, _ = io.Copy(io.Discard, req.Body)
	}
}

func sending(raw string) func(net.Conn, *bufio.Reader) {
	return func(conn net.Conn, br *bufio.Reader) {
		readTheRequest(br)
		_, _ = io.WriteString(conn, raw)
	}
}

// The agent sorts a failed request by the errors in its chain: the iPhone's
// words for each kind, the local network hint, and a request that may have
// left (decision 045). Each way a request can fail is sorted as it was,
// through the real client. A handshake's errors stand for a provision's
// here: both come from the same request code.
func TestIssue323_AFailureOfTheRealClientIsSortedAsBefore(t *testing.T) {
	gone, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refused := gone.Addr().String()
	_ = gone.Close()
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	notJSON := "\x1b[2J<html>" + issue323Dirt
	locked := `{"error":"the pairing code was locked after repeated wrong attempts"}`

	didNotAnswer := func(addr string) string {
		return "Your Hub did not answer at " + addr + ". Check that it is running and on the same network as this computer."
	}
	cases := []struct {
		name    string
		addr    string
		local   bool
		wait    time.Duration
		text    func(addr string) string // the whole refusal, when it is the agent's own words
		lead    string                   // else the words it begins with
		unknown bool                     // the outcome of a provision is unknown
	}{
		{"a port nobody listens on", refused, false, 0, didNotAnswer, "", false},
		{"an address outside the local network", "192.0.2.1:8390", true, 0, func(string) string {
			return "Your Hub pairs only on its local network. Connect this computer to the same network as your Hub."
		}, "", false},
		{"a connection closed behind the request", rawAnswer(t, func(conn net.Conn, br *bufio.Reader) { readTheRequest(br) }), false, 0, didNotAnswer, "", true},
		{"an answer that does not come in time", rawAnswer(t, func(_ net.Conn, br *bufio.Reader) { readTheRequest(br); <-hang }), false, 300 * time.Millisecond, didNotAnswer, "", true},
		{"an answer that is no proper HTTP", rawAnswer(t, sending(issue323Dirt+"\r\n\r\n")), false, 0, didNotAnswer, "", true},
		{"an answer that is no JSON", rawAnswer(t, sending(fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(notJSON), notJSON))), false, 0, nil, "Pairing with your Hub failed: invalid character", true},
		{"a refusal of the Hub", rawAnswer(t, sending(fmt.Sprintf("HTTP/1.1 403 Forbidden\r\nContent-Length: %d\r\n\r\n%s", len(locked), locked))), false, 0, func(string) string {
			return "This code is locked after too many wrong tries. Create a new one on your Hub with “vaultsync-hub code”."
		}, "", false},
	}
	for _, c := range cases {
		client := pairing.NewClient(c.addr)
		if c.local {
			client = pairing.NewLocalClient(c.addr)
		}
		ctx, cancel := context.WithCancel(context.Background())
		if c.wait > 0 {
			ctx, cancel = context.WithTimeout(context.Background(), c.wait)
		}
		_, err := client.Handshake(ctx, "TULIP-ANCHOR-42")
		cancel()
		if err == nil {
			t.Fatalf("%s: paired", c.name)
		}
		s := &pairSession{env: pairEnv{goos: "linux"}, hub: pairing.DiscoveredHub{Address: c.addr}, opts: pairOptions{hub: c.addr}}
		got := handshakeText(err, s)
		if c.text != nil && got != c.text(c.addr) || c.text == nil && !strings.HasPrefix(got, c.lead) {
			t.Errorf("%s: the refusal: %.400q", c.name, got)
		}
		if len(got) > 400 || strings.ContainsFunc(got, hidden) {
			t.Errorf("%s: %d bytes, or what a terminal does not show as written: %.400q", c.name, len(got), got)
		}
		if u := provisionOutcomeUnknown(err); u != c.unknown {
			t.Errorf("%s: outcome unknown %v, want %v: %v", c.name, u, c.unknown, err)
		}
		if c.name == "a port nobody listens on" {
			// The Mac's hint for "no route to host" walks the same chain.
			var op *net.OpError
			if !errors.As(err, &op) || op.Op != "dial" {
				t.Errorf("a refused dial lost its dial error: %#v", err)
			}
		}
	}
}
