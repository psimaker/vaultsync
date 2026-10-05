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
	"strings"
	"syscall"
	"time"

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
// Hub's error text.
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

func newClient(hubAddress string, dialer *net.Dialer) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil // a LAN peer, never through a proxy
	transport.DialContext = dialer.DialContext
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
	return reply, nil
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
	return reply, nil
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
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e ErrorResponse
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = http.StatusText(resp.StatusCode)
		}
		return &APIError{Status: resp.StatusCode, Body: e.Error}
	}
	return json.Unmarshal(data, out)
}

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
