package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/psimaker/vaultsync/hub/pake"
)

// pairClient is the device side of the pairing protocol. It is transport-only:
// it knows nothing about Syncthing, so the same code can back the CLI, a
// future desktop agent, and (through gomobile) the iOS app.
type pairClient struct {
	baseURL string
	http    *http.Client
	session string
	sess    *pairingSession // reuses the box helpers; key set after finish
}

var errCodeRejected = errors.New("the hub rejected the pairing code")

func newPairClient(hubAddress string) *pairClient {
	return &pairClient{
		baseURL: "http://" + strings.TrimSuffix(hubAddress, "/"),
		http:    &http.Client{Timeout: 20 * time.Second},
	}
}

// handshake runs start+finish with the given canonical code. On success the
// client holds an authenticated session and returns the Hub's first payload
// (its device ID, name and vault list).
func (c *pairClient) handshake(ctx context.Context, code string) (hubPayload, error) {
	dev, err := pake.NewState(pake.RoleDevice, pake.PasswordScalar([]byte(code)))
	if err != nil {
		return hubPayload{}, err
	}
	pa, err := dev.Start()
	if err != nil {
		return hubPayload{}, err
	}
	var started startResponse
	if err := c.post(ctx, "/v1/pair/start", startRequest{PA: b64e(pa)}, &started); err != nil {
		return hubPayload{}, err
	}
	pb, err := b64d(started.PB)
	if err != nil {
		return hubPayload{}, errors.New("hub sent a malformed key exchange")
	}
	confirm, err := dev.Finish(pb)
	if err != nil {
		return hubPayload{}, err
	}
	var finished finishResponse
	err = c.post(ctx, "/v1/pair/finish", finishRequest{Session: started.Session, Confirm: b64e(confirm)}, &finished)
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden {
		return hubPayload{}, errCodeRejected
	}
	if err != nil {
		return hubPayload{}, err
	}
	hubConfirm, err := b64d(finished.Confirm)
	if err != nil {
		return hubPayload{}, errors.New("hub sent a malformed confirmation")
	}
	key, err := dev.Verify(hubConfirm)
	if err != nil {
		return hubPayload{}, fmt.Errorf("%w (the hub could not prove it knows the code)", errCodeRejected)
	}
	c.session = started.Session
	c.sess = &pairingSession{id: started.Session, key: key}
	var reply hubPayload
	if err := openBox(c.sess, "hub->device", finished.Box, &reply); err != nil {
		return hubPayload{}, errors.New("hub reply could not be decrypted")
	}
	if reply.Version != protocolVersion {
		return hubPayload{}, fmt.Errorf("hub speaks pairing protocol %d, this client speaks %d", reply.Version, protocolVersion)
	}
	return reply, nil
}

// provision registers the device on the Hub and, when vault is non-empty,
// asks it to share (or create and share) that vault.
func (c *pairClient) provision(ctx context.Context, seq uint64, deviceID, deviceName, vault string, create bool) (hubPayload, error) {
	if c.sess == nil {
		return hubPayload{}, errors.New("provision before handshake")
	}
	box, err := sealBox(c.sess, "device->hub", provisionPayload{
		Version: protocolVersion, Seq: seq, DeviceID: deviceID, Name: deviceName, Vault: vault, Create: create,
	})
	if err != nil {
		return hubPayload{}, err
	}
	var resp boxResponse
	if err := c.post(ctx, "/v1/pair/provision", provisionRequest{Session: c.session, Box: box}, &resp); err != nil {
		return hubPayload{}, err
	}
	var reply hubPayload
	if err := openBox(c.sess, "hub->device", resp.Box, &reply); err != nil {
		return hubPayload{}, errors.New("hub reply could not be decrypted")
	}
	return reply, nil
}

func (c *pairClient) post(ctx context.Context, path string, in, out any) error {
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
	data, err := io.ReadAll(io.LimitReader(resp.Body, pairingMaxBody))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e errorResponse
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = http.StatusText(resp.StatusCode)
		}
		return &apiError{Status: resp.StatusCode, Body: e.Error}
	}
	return json.Unmarshal(data, out)
}
