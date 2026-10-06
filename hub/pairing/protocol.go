// Package pairing is the device side of the VaultSync Hub pairing protocol v1
// and the parts of the protocol both sides share: the wire types, the
// encrypted boxes, the pairing-code word list, LAN discovery and the QR link.
//
// It is transport-only and knows nothing about Syncthing, so the same code
// backs the `vaultsync-hub pair` CLI and — through gomobile, in the bridge of
// the iOS app — VaultSync on iPhone. SPAKE2 itself lives in the sibling
// package pake; nothing here reimplements it. The Hub's server half (session
// bookkeeping, rate limits, failure counting, provisioning guards) stays in
// the vaultsync-hub command, which uses these types for its wire format.
//
// Protocol v1 — three requests over plain HTTP on the LAN (decision 036):
//
//	POST /v1/pair/start      {pa}                → {session, pb}
//	POST /v1/pair/finish     {session, confirm}  → {confirm, box}
//	POST /v1/pair/provision  {session, box}      → {box}
package pairing

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
)

const (
	// ProtocolVersion is carried inside every encrypted payload; both sides
	// refuse a payload of another version.
	ProtocolVersion = 1
	// MaxBody bounds every request and response body of the protocol.
	MaxBody = 64 << 10
	// DefaultPort is the Hub's pairing port (TCP) and discovery port (UDP).
	DefaultPort = 8390
)

// Box directions. Every box is bound to its session and its direction, so a
// box can be neither replayed into another session nor reflected back to its
// sender.
const (
	DirectionHubToDevice = "hub->device"
	DirectionDeviceToHub = "device->hub"
)

type StartRequest struct {
	PA string `json:"pa"`
}

type StartResponse struct {
	Session string `json:"session"`
	PB      string `json:"pb"`
}

type FinishRequest struct {
	Session string `json:"session"`
	Confirm string `json:"confirm"`
}

type FinishResponse struct {
	Confirm string `json:"confirm"`
	Box     string `json:"box"`
}

type ProvisionRequest struct {
	Session string `json:"session"`
	Box     string `json:"box"`
}

type BoxResponse struct {
	Box string `json:"box"`
}

// HubPayload is the Hub's encrypted answer to finish and to every provision.
type HubPayload struct {
	Version     int         `json:"version"`
	HubDeviceID string      `json:"hubDeviceID"`
	HubName     string      `json:"hubName"`
	Vaults      []VaultInfo `json:"vaults"`
	Provisioned *VaultInfo  `json:"provisioned,omitempty"`
	Error       string      `json:"error,omitempty"`
}

// ProvisionPayload identifies the device and asks the Hub to share (or create
// and share) one vault with it. An empty Vault only registers the device.
type ProvisionPayload struct {
	Version  int    `json:"version"`
	Seq      uint64 `json:"seq"`
	DeviceID string `json:"deviceID"`
	Name     string `json:"name"`
	Vault    string `json:"vault"`
	Create   bool   `json:"create"`
}

// VaultInfo is what pairing and `status` report about one folder on the Hub.
// Files is the Hub's local file count; -1 means unknown, and a device must
// treat unknown as "not empty" (fail closed).
type VaultInfo struct {
	ID         string   `json:"id"`
	Label      string   `json:"label"`
	Files      int64    `json:"files"`
	SharedWith []string `json:"sharedWith"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

// RegistrationRefusedPrefix starts the Error of a provision reply when the Hub
// could not register the device at all. Any other Error in a reply comes
// after the Hub acted (reading its own state for the reply failed) and does
// not undo the registration or the share.
const RegistrationRefusedPrefix = "could not register the device"

// --- boxes ------------------------------------------------------------------

func aead(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// SealBox encrypts v as JSON with AES-256-GCM under the session key. The
// session ID and the direction are the associated data.
func SealBox(sessionID string, key []byte, direction string, v any) (string, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	g, err := aead(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ad := []byte(sessionID + "|" + direction)
	return B64Encode(append(nonce, g.Seal(nil, nonce, plain, ad)...)), nil
}

// OpenBox decrypts a box sealed by SealBox for the same session and direction
// and decodes its JSON into v.
func OpenBox(sessionID string, key []byte, direction string, box string, v any) error {
	raw, err := B64Decode(box)
	if err != nil {
		return err
	}
	g, err := aead(key)
	if err != nil {
		return err
	}
	if len(raw) < g.NonceSize() {
		return errors.New("box too short")
	}
	ad := []byte(sessionID + "|" + direction)
	plain, err := g.Open(nil, raw[:g.NonceSize()], raw[g.NonceSize():], ad)
	if err != nil {
		return err
	}
	return json.Unmarshal(plain, v)
}

// B64Encode is the protocol's byte encoding (standard base64, padded).
func B64Encode(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// B64Decode reverses B64Encode.
func B64Decode(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }
