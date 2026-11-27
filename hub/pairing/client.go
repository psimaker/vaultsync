package pairing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/psimaker/vaultsync/hub/pake"
)

// Client is the device side of the pairing protocol. It is transport-only: it
// knows nothing about Syncthing. Not safe for concurrent use.
type Client struct {
	baseURL string
	http    *http.Client
	session string
	key     []byte // session key, set only after both confirmations verified
}

// ErrCodeRejected means the key confirmation failed: the Hub counted a wrong
// code, or the other side could not prove that it knows the code.
var ErrCodeRejected = errors.New("the hub rejected the pairing code")

// ErrHubNotAuthenticated means the Hub accepted this device's proof but could
// not prove its own: whatever answered does not know the code. A real Hub
// counts nothing here — this is an impostor or a broken Hub, not a typo.
var ErrHubNotAuthenticated = errors.New("the hub could not prove that it knows the code")

// ErrIncompatible means the Hub speaks another protocol version.
var ErrIncompatible = errors.New("the hub speaks another pairing protocol version")

// APIError is a non-200 answer from the Hub's pairing service. Body is the
// answer's error text as errorText leaves it: one clean line of at most
// maxErrorText bytes. When the answer brought no text, or none that can be
// shown, Body is the status's own text, which is empty for a status that has
// none.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("hub pairing: HTTP %d: %s", e.Status, e.Body)
}

// NewClient returns a client for the pairing service at hubAddress
// (host:port). The Hub never redirects, so the client never follows one.
func NewClient(hubAddress string) *Client {
	return newClient(hubAddress, &net.Dialer{Timeout: 10 * time.Second})
}

// NewLocalClient is NewClient for an address from outside the device (a
// scanned QR code, an opened link, a discovery answer): besides
// LocalHubAddress at parse time, every connection it opens is checked again
// at the socket — the peer must be a private, loopback or link-local IP — so
// no later step can carry the handshake off the local network.
func NewLocalClient(hubAddress string) *Client {
	return newClient(hubAddress, &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip := net.ParseIP(host); ip == nil || !isLocalIP(ip) {
				return ErrNotLocal
			}
			return nil
		},
	})
}

// maxAnswerHead bounds what the client reads of an answer's head, its status
// line and headers, in bytes. A Hub's own head is under 200 bytes; a reverse
// proxy in front of one can add a few KiB. Go's default is 10 MiB, which it
// reads and holds before it gives up (#323).
const maxAnswerHead = 64 << 10

func newClient(hubAddress string, dialer *net.Dialer) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // a LAN peer, never through a proxy
	transport.MaxResponseHeaderBytes = maxAnswerHead
	// Go's transport writes a line to the program's log for bytes that reach
	// it while it waits for no answer: bytes behind an answer on a connection
	// it keeps for the next request, and bytes that arrive before it has
	// counted the request it is about to send. Whatever answers at the
	// address can send both. So each request has a connection of its own,
	// and a connection is read only once the request is written on it: the
	// transport counts a request before it writes it (#323, decision 062).
	transport.DisableKeepAlives = true
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			// As it is: callers tell a refused dial, and an address outside
			// the local network, by this error.
			return nil, err
		}
		return newAskedFirst(conn), nil
	}
	return &Client{
		baseURL: "http://" + strings.TrimSuffix(hubAddress, "/"),
		http: &http.Client{
			Timeout:   20 * time.Second,
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("the hub answered with a redirect")
			},
		},
	}
}

// SessionID is the Hub's ID for the authenticated session; empty before a
// successful Handshake. It travels in the clear and is no secret.
func (c *Client) SessionID() string { return c.session }

// Handshake runs start+finish with the given canonical code (see
// NormalizeCode — only canonical bytes may enter the PAKE). On success the
// client holds an authenticated session and returns the Hub's first payload
// (its device ID, name and vault list).
func (c *Client) Handshake(ctx context.Context, code string) (HubPayload, error) {
	dev, err := pake.NewState(pake.RoleDevice, pake.PasswordScalar([]byte(code)))
	if err != nil {
		return HubPayload{}, err
	}
	pa, err := dev.Start()
	if err != nil {
		return HubPayload{}, err
	}
	var started StartResponse
	if err := c.post(ctx, "/v1/pair/start", StartRequest{PA: B64Encode(pa)}, &started); err != nil {
		return HubPayload{}, err
	}
	pb, err := B64Decode(started.PB)
	if err != nil {
		return HubPayload{}, errors.New("hub sent a malformed key exchange")
	}
	confirm, err := dev.Finish(pb)
	if err != nil {
		return HubPayload{}, err
	}
	var finished FinishResponse
	err = c.post(ctx, "/v1/pair/finish", FinishRequest{Session: started.Session, Confirm: B64Encode(confirm)}, &finished)
	var apiErr *APIError
	// Only a failed key confirmation is a wrong code (the Hub counted it); a
	// 403 for a session the Hub no longer knows is not.
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden && strings.Contains(apiErr.Body, "rejected") {
		return HubPayload{}, ErrCodeRejected
	}
	if err != nil {
		return HubPayload{}, err
	}
	hubConfirm, err := B64Decode(finished.Confirm)
	if err != nil {
		return HubPayload{}, errors.New("hub sent a malformed confirmation")
	}
	key, err := dev.Verify(hubConfirm)
	if err != nil {
		return HubPayload{}, ErrHubNotAuthenticated
	}
	var reply HubPayload
	if err := OpenBox(started.Session, key, DirectionHubToDevice, finished.Box, &reply); err != nil {
		return HubPayload{}, errors.New("hub reply could not be decrypted")
	}
	if reply.Version != ProtocolVersion {
		return HubPayload{}, fmt.Errorf("%w: hub %d, this client %d", ErrIncompatible, reply.Version, ProtocolVersion)
	}
	c.session = started.Session
	c.key = key
	return cleaned(reply), nil
}

// Provision registers the device on the Hub and, when vault is non-empty,
// asks it to share (or create and share) that vault. seq must grow with every
// call on the same session; the Hub refuses a replayed one.
func (c *Client) Provision(ctx context.Context, seq uint64, deviceID, deviceName, vault string, create bool) (HubPayload, error) {
	if c.key == nil {
		return HubPayload{}, errors.New("provision before handshake")
	}
	box, err := SealBox(c.session, c.key, DirectionDeviceToHub, ProvisionPayload{
		Version: ProtocolVersion, Seq: seq, DeviceID: deviceID, Name: deviceName, Vault: vault, Create: create,
	})
	if err != nil {
		return HubPayload{}, err
	}
	var resp BoxResponse
	if err := c.post(ctx, "/v1/pair/provision", ProvisionRequest{Session: c.session, Box: box}, &resp); err != nil {
		return HubPayload{}, err
	}
	var reply HubPayload
	if err := OpenBox(c.session, c.key, DirectionHubToDevice, resp.Box, &reply); err != nil {
		return HubPayload{}, errors.New("hub reply could not be decrypted")
	}
	return cleaned(reply), nil
}

// cleaned returns a Hub's answer with the Hub's name (decision 061) and the
// error it reports (decision 062) fit to show. They are cleaned here, for
// every program that shows or stores them; a vault's label is not (#320). An
// error with nothing left to show stays an error: callers tell a refusal
// from a share by whether Error is empty.
func cleaned(reply HubPayload) HubPayload {
	reply.HubName = SanitizeName(reply.HubName)
	if reply.Error != "" {
		if reply.Error = errorText(reply.Error); reply.Error == "" {
			reply.Error = unreadableError
		}
	}
	return reply
}

func (c *Client) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return shortened(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody))
	if err != nil {
		return shortened(err)
	}
	if resp.StatusCode != http.StatusOK {
		var e ErrorResponse
		_ = json.Unmarshal(data, &e)
		// Cleaned here, for every program that prints the text or reads the
		// kind of a refusal from it.
		text := errorText(e.Error)
		if text == "" {
			text = http.StatusText(resp.StatusCode)
		}
		return &APIError{Status: resp.StatusCode, Body: text}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return shortened(err)
	}
	return nil
}

// answerError is an error about an answer — what Go's HTTP client says of
// one that is no proper HTTP or broke off, or a body that is no JSON — with
// its text as errorText leaves it. The library quotes what came from the
// dialled address in it, as long as it came (#323, decision 062). Unwrap
// keeps the error itself: callers sort a failure by the errors in its chain,
// a refused dial, an address outside the local network, no route to host, a
// deadline.
type answerError struct {
	err  error
	text string
}

func (e *answerError) Error() string { return e.text }
func (e *answerError) Unwrap() error { return e.err }

// unreadableAnswer stands for an error about an answer whose text holds
// nothing that can be shown.
const unreadableAnswer = "the hub's answer could not be read"

func shortened(err error) error {
	text := errorText(err.Error())
	if text == "" {
		text = unreadableAnswer
	}
	return &answerError{err: err, text: text}
}

// askedFirst is a connection that is read only once something has been
// written on it, so nothing the other side sends before the request reaches
// Go's transport (newClient). A read that waits for the first write returns
// when the connection is closed, which happens before the read is let go, or
// when its read deadline passes: the transport closes every connection it
// does not use, and a deadline is kept as it is set.
type askedFirst struct {
	net.Conn
	once  sync.Once
	asked chan struct{} // closed by the first write or by Close

	mu       sync.Mutex
	deadline time.Time
	moved    chan struct{} // closed when the read deadline changes
}

func newAskedFirst(conn net.Conn) *askedFirst {
	return &askedFirst{Conn: conn, asked: make(chan struct{}), moved: make(chan struct{})}
}

func (c *askedFirst) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.asked) })
	return c.Conn.Write(p)
}

// Close closes the connection before it lets a waiting read go on, so that
// read finds the connection closed and takes nothing the other side sent.
// Released first, it could read a greeting before the close, and whether
// that reached the log would hang on how the transport orders its own close.
func (c *askedFirst) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.asked) })
	return err
}

func (c *askedFirst) Read(p []byte) (int, error) {
	if err := c.waitForRequest(); err != nil {
		return 0, err
	}
	return c.Conn.Read(p)
}

// waitForRequest returns nil once the request is written or the connection
// is closed, and the timeout the connection's own read returns once its read
// deadline passes first. The timeout is made here: the connection's own
// timer for the same deadline may not have run yet, and its read would then
// take what the other side sent.
func (c *askedFirst) waitForRequest() error {
	for {
		c.mu.Lock()
		deadline, moved := c.deadline, c.moved
		c.mu.Unlock()
		if done, err := c.waitOnce(deadline, moved); done {
			return err
		}
	}
}

// waitOnce waits for the request, the deadline or a new deadline; done is
// false for a new one.
func (c *askedFirst) waitOnce(deadline time.Time, moved <-chan struct{}) (done bool, err error) {
	var passed <-chan time.Time
	if !deadline.IsZero() {
		timer := time.NewTimer(time.Until(deadline))
		defer timer.Stop()
		passed = timer.C
	}
	select {
	case <-c.asked:
		return true, nil
	case <-passed:
		timeout := &net.OpError{Op: "read", Net: "tcp", Addr: c.RemoteAddr(), Err: os.ErrDeadlineExceeded}
		// A connection without a local address must not take down the
		// transport's reading goroutine.
		if local := c.LocalAddr(); local != nil {
			timeout.Net, timeout.Source = local.Network(), local
		}
		return true, timeout
	case <-moved:
		return false, nil
	}
}

func (c *askedFirst) SetDeadline(t time.Time) error {
	err := c.Conn.SetDeadline(t)
	c.setReadDeadline(t)
	return err
}

func (c *askedFirst) SetReadDeadline(t time.Time) error {
	err := c.Conn.SetReadDeadline(t)
	c.setReadDeadline(t)
	return err
}

func (c *askedFirst) setReadDeadline(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deadline = t
	close(c.moved)
	c.moved = make(chan struct{})
}

// maxErrorText bounds an error text the client hands out, in bytes. What a
// Hub words itself stays under it in the setup it ships with: a refusal is
// at most 100 bytes, and the Hub's own words in front of an answer of its
// Syncthing, which it cuts at 200 bytes, are about 260. A text that quotes a
// long path or name can pass it and loses its end (#324). The words a
// refusal is told by have to stand within these bytes: Handshake and
// FailureKind read the text as it is cut.
const maxErrorText = 300

// unreadableError stands for an error in a Hub's encrypted answer that holds
// nothing that can be shown.
const unreadableError = "the hub reported an error without readable text"

// errorText makes the text of an error answer fit to print, and to read the
// kind of a refusal from: one line, without what SanitizeName drops from a
// name, at most maxErrorText bytes. The text comes from whatever answered at
// the dialled address — before the key exchange that is anyone — or from a
// Hub's encrypted answer, where it passes on what the Hub's Syncthing said
// (#319, decision 062). A run of white space with a tab, a line break or a
// line separator in it becomes one space: what stood on two lines stays two
// words. Other white space stays as it came, because a Hub quotes names in
// its texts; at either end it goes. A text that is cut ends in "…". A text
// with nothing to show in it is the empty string.
func errorText(text string) string {
	// Split before the dropping: hides takes a line break out from between
	// two words. Each piece is trimmed after it, as a name is.
	var kept []string
	for _, piece := range strings.FieldsFunc(text, breaksLine) {
		if piece = strings.TrimSpace(withoutHidden(piece)); piece != "" {
			kept = append(kept, piece)
		}
	}
	out := strings.Join(kept, " ")
	const mark = "…"
	cut := len(out) > maxErrorText
	if cut {
		out = strings.TrimRightFunc(cutTo(out, maxErrorText-len(mark)), unicode.IsSpace)
	}
	// Judged after the cut, as for a name: what is left may draw nothing.
	if drawsNothing(out) {
		return ""
	}
	if cut {
		out += mark
	}
	return out
}

// breaksLine reports white space that ends a line or jumps within one: a
// tab, a line feed, a vertical tab, a form feed, a carriage return, NEL
// (U+0085) and the line and paragraph separators — the white space hides
// would take out.
func breaksLine(r rune) bool { return unicode.IsSpace(r) && hides(r) }

// Kind sorts a pairing failure into what a user interface has to say about
// it. The Hub's refusals carry no machine-readable code, so the HTTP status
// plus a stable phrase of its message decide; TestFailureKindsMatchServerTexts
// in the vaultsync-hub command pins every phrase to the server's real text.
type Kind string

const (
	KindNone           Kind = ""
	KindCodeRejected   Kind = "codeRejected"         // wrong code — the Hub counted it
	KindAuthFailed     Kind = "authenticationFailed" // the Hub could not prove the code
	KindCodeExpired    Kind = "codeExpired"
	KindCodeLocked     Kind = "codeLocked" // too many wrong codes
	KindNoCode         Kind = "noCode"     // no code issued on the Hub
	KindSessionExpired Kind = "sessionExpired"
	KindRateLimited    Kind = "rateLimited"
	KindBusy           Kind = "busy"
	KindNotLocal       Kind = "notLocal" // not a local-network address
	KindUnreachable    Kind = "unreachable"
	KindIncompatible   Kind = "incompatible"
	KindOther          Kind = "other"
)

// FailureKind classifies an error returned by Handshake or Provision.
func FailureKind(err error) Kind {
	if err == nil {
		return KindNone
	}
	if errors.Is(err, ErrCodeRejected) {
		return KindCodeRejected
	}
	if errors.Is(err, ErrHubNotAuthenticated) {
		return KindAuthFailed
	}
	if errors.Is(err, ErrIncompatible) {
		return KindIncompatible
	}
	if errors.Is(err, ErrNotLocal) {
		return KindNotLocal
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		body := strings.ToLower(apiErr.Body)
		switch apiErr.Status {
		case http.StatusForbidden:
			switch {
			case strings.Contains(body, "pairing session"):
				return KindSessionExpired
			case strings.Contains(body, "code has expired"):
				return KindCodeExpired
			case strings.Contains(body, "locked"):
				return KindCodeLocked
			case strings.Contains(body, "no pairing code"):
				return KindNoCode
			case strings.Contains(body, "local network"):
				return KindNotLocal
			case strings.Contains(body, "rejected"):
				return KindCodeRejected
			}
		case http.StatusTooManyRequests:
			if strings.Contains(body, "busy") {
				return KindBusy
			}
			return KindRateLimited
		}
		return KindOther
	}
	var netErr net.Error
	var opErr *net.OpError
	if errors.As(err, &opErr) || errors.As(err, &netErr) || errors.Is(err, context.DeadlineExceeded) {
		return KindUnreachable
	}
	return KindOther
}
