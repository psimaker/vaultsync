package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/psimaker/vaultsync/hub/pairing"
)

// #323: `vaultsync-hub pair` printed what the HTTP client said about an
// answer that is no proper HTTP as long as it came, and Go's transport wrote
// bytes that came behind an answer to the command's log, in the same
// terminal. Decision 062.

// rawService answers every request to it with raw, which may be anything,
// and reads on until the client closes the connection; closed then gets a
// value.
func rawService(t *testing.T, raw string) (addr string, closed <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	done := make(chan struct{}, 16)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				br := bufio.NewReader(conn)
				if req, err := http.ReadRequest(br); err == nil {
					_, _ = io.Copy(io.Discard, req.Body)
				}
				_, _ = io.WriteString(conn, raw)
				_, _ = io.Copy(io.Discard, br)
				select {
				case done <- struct{}{}:
				default:
				}
			}()
		}
	}()
	return ln.Addr().String(), done
}

// lockedLog collects what the standard logger writes while a test runs.
type lockedLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// What came from the address: a terminal sequence, a mark that reverses
// what follows, a line separator, and far more than a line.
var issue323Dirt = "\x1b[2J\u202e" + strings.Repeat("я", 1000) + "\u2028x"

func TestIssue323_PairPrintsAnAnswerThatIsNoProperHTTPAsOneCleanShortLine(t *testing.T) {
	addr, _ := rawService(t, issue323Dirt+"\r\n\r\n")
	_, _, err := handshakeChosenHub(context.Background(), []discoveredHub{{Address: addr}}, "TULIP-ANCHOR-42", nil)
	if err == nil {
		t.Fatal("an answer that is no HTTP paired")
	}
	line, lead := err.Error(), "hub at "+addr+" did not answer properly: "
	if !strings.HasPrefix(line, lead+`Post "http://`+addr+`/v1/pair/start": `) || !strings.HasSuffix(line, "…") {
		t.Errorf("the line: %.400q", line)
	}
	if len(line) > len(lead)+300 || strings.ContainsFunc(line, hidden) {
		t.Errorf("the line is %d bytes behind its own words, or holds what a terminal does not show as written: %.400q", len(line)-len(lead), line)
	}
}

func TestIssue323_PairWritesNoLogLineForBytesBehindAnAnswer(t *testing.T) {
	logged := &lockedLog{}
	old := log.Writer()
	log.SetOutput(logged)
	t.Cleanup(func() { log.SetOutput(old) })

	const marker = "bytes-behind-the-answer"
	body := `{"session":"s","pb":"!"}`
	addr, closed := rawService(t, fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body), body)+"\x1b[2J"+marker+issue323Dirt)
	_, _, err := handshakeChosenHub(context.Background(), []discoveredHub{{Address: addr}}, "TULIP-ANCHOR-42", nil)
	if err == nil || !strings.HasSuffix(err.Error(), "hub sent a malformed key exchange") {
		t.Fatalf("the answer: %v", err)
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the client kept the connection open")
	}
	if strings.Contains(logged.String(), marker) {
		t.Errorf("the log: %.400q", logged.String())
	}
}

// The client reads at most 64 KiB of an answer's head. A reverse proxy in
// front of a Hub adds headers of its own: a head of 40 KiB pairs as it did,
// a code and a registration, each on a connection of its own.
func TestIssue323_AHubBehindAProxyWithALargeHeadPairs(t *testing.T) {
	fx := newPairingFixture(t)
	handler := fx.server.handler()
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := range 40 {
			w.Header().Set(fmt.Sprintf("X-Proxy-%02d", i), strings.Repeat("p", 1000))
		}
		handler.ServeHTTP(w, r)
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	ctx := context.Background()
	c := pairing.NewClient(strings.TrimPrefix(srv.URL, "http://"))
	hello, err := c.Handshake(ctx, fx.code)
	if err != nil || hello.HubDeviceID != fakeHubID {
		t.Fatalf("the handshake: %+v, %v", hello, err)
	}
	reply, err := c.Provision(ctx, 1, fakeDeviceID, "Laptop", "", false)
	if err != nil || reply.Error != "" {
		t.Fatalf("the registration: %+v, %v", reply, err)
	}
	// start, finish, provision
	if n := conns.Load(); n != 3 {
		t.Errorf("%d connections for three requests", n)
	}
}
