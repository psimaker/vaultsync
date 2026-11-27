package pairing

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// #323: two texts from whatever answers at the dialled address still reached
// a terminal. What the HTTP client says about an answer that is no proper
// HTTP quotes what came, as long as it came; and Go's transport writes a line
// to the program's log for bytes that reach it outside an answer. Decision
// 062.

// rawHub listens on loopback and hands each connection to answer, which may
// send anything. Once answer returns, the connection is read until the
// client closes it, and closed gets a value: from then on nothing of that
// connection can reach the client's log.
func rawHub(t *testing.T, answer func(conn net.Conn, br *bufio.Reader)) (addr string, closed <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	done := make(chan struct{}, 64)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				br := bufio.NewReader(conn)
				answer(conn, br)
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

// readRequest reads one request, its body included.
func readRequest(br *bufio.Reader) {
	if req, err := http.ReadRequest(br); err == nil {
		_, _ = io.Copy(io.Discard, req.Body)
	}
}

// answerWith is an answer that reads the request and then sends raw.
func answerWith(raw string) func(net.Conn, *bufio.Reader) {
	return func(conn net.Conn, br *bufio.Reader) {
		readRequest(br)
		_, _ = io.WriteString(conn, raw)
	}
}

// wholeAnswer is a proper answer with status and body.
func wholeAnswer(status, body string) string {
	return fmt.Sprintf("HTTP/1.1 %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", status, len(body), body)
}

// waitClosed waits until the client has closed a connection to a rawHub.
func waitClosed(t *testing.T, closed <-chan struct{}) {
	t.Helper()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the client kept the connection open")
	}
}

// logBuffer collects what the standard logger writes during a test. Go's
// transport writes there. No test in this package runs in parallel.
type logBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func captureLog(t *testing.T) *logBuffer {
	t.Helper()
	buf := &logBuffer{}
	old := log.Writer()
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return buf
}

// What came from the dialled address, with a terminal sequence, a mark that
// reverses what follows and a line separator in it, and far longer than an
// error text may be.
var dirt = "\x1b[2J\u202e" + strings.Repeat("я", 1000) + "\u2028x"

// The HTTP client's error for an answer that is no proper HTTP quoted the
// line it could not read, as long as it was: up to the 10 MiB the library
// reads of an answer's head. It is now one clean line of at most 300 bytes
// that begins with the library's own words, and it is sorted as before.
func TestIssue323_AnAnswerThatIsNoProperHTTPIsOneCleanShortLine(t *testing.T) {
	cases := []struct{ name, answer string }{
		{"a status line that is no HTTP", dirt + "\r\n\r\n"},
		{"a status code that is no number", "HTTP/1.1 2" + strings.Repeat("9", 2000) + " OK\r\n\r\n"},
		{"a header with a control character in its name", "HTTP/1.1 200 OK\r\nX-" + dirt + ": y\r\n\r\n"},
		{"a header line without a colon", "HTTP/1.1 200 OK\r\n" + dirt + "\r\n\r\n"},
		{"a status line longer than an answer's head may be", "\x1b[2J" + strings.Repeat("я", 30000) + "\r\n\r\n"},
	}
	for _, c := range cases {
		addr, _ := rawHub(t, answerWith(c.answer))
		_, err := NewClient(addr).Handshake(context.Background(), "TULIP-ANCHOR-42")
		if err == nil {
			t.Fatalf("%s: paired", c.name)
		}
		line := err.Error()
		if len(line) > maxErrorText || !oneCleanLine(line) || !strings.HasSuffix(line, "…") {
			t.Errorf("%s: %d bytes: %.400q", c.name, len(line), line)
		}
		if lead := `Post "http://` + addr + `/v1/pair/start": `; !strings.HasPrefix(line, lead) {
			t.Errorf("%s: the library's own words are not in front: %.400q", c.name, line)
		}
		// Only the text changes: the error still is the library's, and it is
		// sorted as before.
		var urlErr *url.Error
		if !errors.As(err, &urlErr) {
			t.Errorf("%s: the chain lost the library's error: %#v", c.name, err)
		}
		if k := FailureKind(err); k != KindUnreachable {
			t.Errorf("%s: kind %q", c.name, k)
		}
	}
}

// countBytesRead makes c count every byte its connections read.
func countBytesRead(c *Client) *atomic.Int64 {
	var n atomic.Int64
	tr := c.http.Transport.(*http.Transport)
	dial := tr.DialContext
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return countingConn{Conn: conn, n: &n}, nil
	}
	return &n
}

type countingConn struct {
	net.Conn
	n *atomic.Int64
}

func (c countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// The client read up to 10 MiB of an answer's head before it gave up, and
// held it. A Hub's head is under 200 bytes, and a reverse proxy in front of
// one adds a few KiB; the client now reads at most 64 KiB of one.
func TestIssue323_AnAnswersHeadIsReadOnlyToTheBound(t *testing.T) {
	addr, _ := rawHub(t, func(conn net.Conn, br *bufio.Reader) {
		readRequest(br)
		_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\n")
		line := []byte("X-More: " + strings.Repeat("a", 100) + "\r\n")
		for range 200000 {
			if _, err := conn.Write(line); err != nil {
				return
			}
		}
	})
	c := NewClient(addr)
	read := countBytesRead(c)
	_, err := c.Handshake(context.Background(), "TULIP-ANCHOR-42")
	if err == nil {
		t.Fatal("a head that does not end paired")
	}
	if n := read.Load(); n > 64<<10 {
		t.Errorf("the client read %d bytes of a head that does not end", n)
	}
	if len(err.Error()) > maxErrorText || !oneCleanLine(err.Error()) {
		t.Errorf("the error: %.400q", err.Error())
	}
	if k := FailureKind(err); k != KindUnreachable {
		t.Errorf("kind %q", k)
	}
}

// Bytes behind a whole answer stayed on the connection the client kept open
// for its next request, and Go's transport wrote them to the program's log:
// "Unsolicited response received on idle HTTP channel …", in the terminal of
// whoever paired. The client now keeps no connection open between requests,
// and the bytes go with it.
func TestIssue323_BytesBehindAnAnswerReachNoLog(t *testing.T) {
	logged := captureLog(t)
	const marker = "bytes-behind-the-answer"
	addr, closed := rawHub(t, answerWith(wholeAnswer("200 OK", `{"session":"s","pb":"!"}`)+"\x1b[2J"+marker+dirt))
	_, err := NewClient(addr).Handshake(context.Background(), "TULIP-ANCHOR-42")
	// The answer itself is read as before: a key exchange that is no key.
	if err == nil || err.Error() != "hub sent a malformed key exchange" || FailureKind(err) != KindOther {
		t.Fatalf("the answer: %v", err)
	}
	waitClosed(t, closed)
	if strings.Contains(logged.String(), marker) {
		t.Errorf("the log: %.400q", logged.String())
	}
}

// What a service at that port sends before it is asked — a greeting, as an
// SSH server sends one — reached Go's transport before it counted the request
// as sent, and went to the log in the same words. The client now reads from a
// connection only once it has written its request on it, so the greeting is
// read as the answer, which it is not. The moment between the connection and
// the request is held open here; on a fast machine the greeting arrives in
// it unaided, now and then.
func TestIssue323_BytesBeforeTheRequestReachNoLog(t *testing.T) {
	logged := captureLog(t)
	const marker = "a-greeting-before-the-request"
	addr, closed := rawHub(t, func(conn net.Conn, _ *bufio.Reader) {
		_, _ = io.WriteString(conn, "SSH-2.0-"+marker+"\x1b[2J"+dirt+"\r\n")
	})
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) { time.Sleep(20 * time.Millisecond) },
	})
	_, err := NewClient(addr).Handshake(ctx, "TULIP-ANCHOR-42")
	if err == nil || FailureKind(err) != KindUnreachable {
		t.Fatalf("the greeting: %v (kind %q)", err, FailureKind(err))
	}
	if len(err.Error()) > maxErrorText || !oneCleanLine(err.Error()) {
		t.Errorf("the error: %.400q", err.Error())
	}
	waitClosed(t, closed)
	if strings.Contains(logged.String(), marker) {
		t.Errorf("the log: %.400q", logged.String())
	}
}

// The callers sort a failure by the errors in its chain: FailureKind here,
// the desktop agent and the iPhone's bridge by a refused dial and an address
// outside the local network, the Mac's hint by "no route to host". Each way
// a request can fail is sorted as it was, through the real client, and
// every text is one clean line.
func TestIssue323_EveryFailureIsSortedAsBefore(t *testing.T) {
	gone, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	refusedAddr := gone.Addr().String()
	_ = gone.Close()

	closedAddr, _ := rawHub(t, func(conn net.Conn, br *bufio.Reader) { readRequest(br); _ = conn.Close() })
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	silentAddr, _ := rawHub(t, func(_ net.Conn, br *bufio.Reader) { readRequest(br); <-hang })
	malformedAddr, _ := rawHub(t, answerWith(dirt+"\r\n\r\n"))
	notJSONAddr, _ := rawHub(t, answerWith(wholeAnswer("200 OK", "\x1b[2J<html>"+dirt)))
	lockedAddr, _ := rawHub(t, answerWith(wholeAnswer("403 Forbidden", `{"error":"the pairing code was locked"}`)))

	isDial := func(err error) bool {
		var op *net.OpError
		return errors.As(err, &op) && op.Op == "dial"
	}
	cases := []struct {
		name   string
		client *Client
		wait   time.Duration
		kind   Kind
		chain  func(error) bool
	}{
		{"a port nobody listens on", NewClient(refusedAddr), 0, KindUnreachable, isDial},
		{"an address outside the local network", NewLocalClient("192.0.2.1:8390"), 0, KindNotLocal,
			func(err error) bool { return errors.Is(err, ErrNotLocal) }},
		{"a connection closed behind the request", NewClient(closedAddr), 0, KindUnreachable,
			func(err error) bool { var u *url.Error; return errors.As(err, &u) && !isDial(err) }},
		{"an answer that does not come in time", NewClient(silentAddr), 300 * time.Millisecond, KindUnreachable,
			func(err error) bool { return errors.Is(err, context.DeadlineExceeded) && !isDial(err) }},
		{"an answer that is no proper HTTP", NewClient(malformedAddr), 0, KindUnreachable,
			func(err error) bool { var u *url.Error; return errors.As(err, &u) && !isDial(err) }},
		{"an answer that is no JSON", NewClient(notJSONAddr), 0, KindOther,
			func(err error) bool { var u *url.Error; return !errors.As(err, &u) }},
		{"a refusal of the Hub", NewClient(lockedAddr), 0, KindCodeLocked,
			func(err error) bool { var a *APIError; return errors.As(err, &a) && a.Status == http.StatusForbidden }},
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
		if k := FailureKind(err); k != c.kind {
			t.Errorf("%s: kind %q, want %q: %v", c.name, k, c.kind, err)
		}
		if !c.chain(err) {
			t.Errorf("%s: the chain changed: %#v", c.name, err)
		}
		var apiErr *APIError
		if !oneCleanLine(err.Error()) || !errors.As(err, &apiErr) && len(err.Error()) > maxErrorText {
			t.Errorf("%s: %d bytes: %.400q", c.name, len(err.Error()), err.Error())
		}
	}
}

// The text is cut and the chain stays: the Mac's hint for "no route to host"
// reads the system call's error under the library's. An error whose text
// has nothing to show is still an error, in a fixed phrase.
func TestIssue323_ACutTextKeepsTheErrorsUnderIt(t *testing.T) {
	unreachable := &url.Error{Op: "Post", URL: "http://192.168.8.70:8390/v1/pair/start", Err: &net.OpError{
		Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.EHOSTUNREACH),
	}}
	err := shortened(unreachable)
	var op *net.OpError
	var netErr net.Error
	if !errors.Is(err, syscall.EHOSTUNREACH) || !errors.As(err, &op) || op.Op != "dial" || !errors.As(err, &netErr) {
		t.Errorf("the chain: %#v", err)
	}
	if err.Error() != unreachable.Error() {
		t.Errorf("a clean, short text changed: %q", err.Error())
	}
	nothing := errors.New("\x1b\u202e\r\n")
	if err := shortened(nothing); err.Error() != "the hub's answer could not be read" || !errors.Is(err, nothing) {
		t.Errorf("nothing to show: %q", err.Error())
	}
}

// askedFirstPair dials a listener that greets at once and returns the
// client's end, wrapped as the client wraps it, and the greeting.
func askedFirstPair(t *testing.T) (*askedFirst, string) {
	t.Helper()
	const greeting = "a greeting before the request"
	addr, _ := rawHub(t, func(conn net.Conn, _ *bufio.Reader) { _, _ = io.WriteString(conn, greeting) })
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return newAskedFirst(conn), greeting
}

// readAsync reads once from c in the background.
func readAsync(c net.Conn) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := c.Read(make([]byte, 64))
		done <- err
	}()
	return done
}

// A read that waits for the request returns when the connection is closed or
// its read deadline passes, so a connection that is never written on leaves
// no reader behind; past its deadline the read is the connection's own
// timeout, and it has read nothing.
func TestIssue323_AReadBeforeTheRequestEndsWithItsConnection(t *testing.T) {
	c, _ := askedFirstPair(t)
	done := readAsync(c)
	waitForReaders(t, 1)
	select {
	case err := <-done:
		t.Fatalf("a read before the request returned: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	_ = c.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a read on a closed connection read")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a read before the request outlived Close")
	}

	for _, set := range []func(net.Conn, time.Time) error{net.Conn.SetReadDeadline, net.Conn.SetDeadline} {
		c, greeting := askedFirstPair(t)
		done := readAsync(c)
		// A deadline set while the read waits, and then moved nearer.
		waitForReaders(t, 1)
		_ = set(c, time.Now().Add(time.Hour))
		time.Sleep(20 * time.Millisecond)
		_ = set(c, time.Now().Add(50*time.Millisecond))
		select {
		case err := <-done:
			var netErr net.Error
			if !errors.As(err, &netErr) || !netErr.Timeout() {
				t.Errorf("past its deadline: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a read before the request outlived its deadline")
		}
		// Nothing was read: the greeting is read once the request is written.
		_ = set(c, time.Time{})
		if _, err := c.Write([]byte("request")); err != nil {
			t.Fatal(err)
		}
		_ = set(c, time.Now().Add(5*time.Second))
		got, err := io.ReadAll(io.LimitReader(c, int64(len(greeting))))
		if err != nil || string(got) != greeting {
			t.Errorf("after the request: %q, %v", got, err)
		}
	}
}

// readersLeft counts the goroutines that wait in a read for a request.
func readersLeft() int {
	buf := make([]byte, 1<<20)
	return strings.Count(string(buf[:runtime.Stack(buf, true)]), "(*askedFirst).waitOnce")
}

// waitForReaders waits until at least n reads wait for a request.
func waitForReaders(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for readersLeft() < n {
		if time.Now().After(deadline) {
			t.Fatal("the read did not start waiting")
		}
		time.Sleep(time.Millisecond)
	}
}

// A connection the transport dials for a request that is gone by the time
// the dial returns is never written on. The transport closes it, and its
// reader ends.
func TestIssue323_AConnectionNeverUsedLeavesNoReader(t *testing.T) {
	addr, closed := rawHub(t, func(net.Conn, *bufio.Reader) {})
	c := NewClient(addr)
	tr := c.http.Transport.(*http.Transport)
	dial := tr.DialContext
	gone := make(chan struct{})
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		<-gone // the request ended while the dial was under way
		return conn, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := c.Handshake(ctx, "TULIP-ANCHOR-42")
	close(gone)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the request: %v", err)
	}
	waitClosed(t, closed)
	deadline := time.Now().Add(5 * time.Second)
	for readersLeft() > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d readers wait for a request that never comes", readersLeft())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
