// Package pake implements SPAKE2 (RFC 9382) over edwards25519 for the
// VaultSync Hub pairing handshake.
//
// A short human-typed pairing code is the only secret two parties share. A
// balanced PAKE turns that low-entropy code into a strong session key such that
//
//   - a passive observer on the LAN learns nothing that allows an offline
//     search for the code, and
//   - an active party without the code gets exactly one online guess per
//     handshake, which the Hub counts and throttles.
//
// Both roles are fixed: the device is party A, the Hub is party B. The
// identities are baked into the transcript so a message from a handshake in
// one direction can never be replayed in the other.
//
// The implementation follows RFC 9382 §3 and §4 literally (M/N constants,
// cofactor clearing, transcript layout, confirmation MACs) so a reviewer can
// audit it against the RFC line by line. It is deliberately dependency-light:
// filippo.io/edwards25519 is the group arithmetic the Go standard library
// itself uses.
package pake

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"

	"filippo.io/edwards25519"
)

// Fixed party identities (RFC 9382 §3.3 "A" and "B"). Never reuse across
// protocols: the transcript binds them, so a different protocol must pick its
// own.
const (
	IdentityDevice = "vaultsync-pairing-device"
	IdentityHub    = "vaultsync-pairing-hub"
)

// PointSize is the wire size of a compressed edwards25519 point.
const PointSize = 32

// ConfirmSize is the wire size of a confirmation MAC (HMAC-SHA256).
const ConfirmSize = 32

// KeySize is the size of the derived session key handed to the caller.
const KeySize = 32

var (
	// ErrInvalidPoint is returned when the peer's message is not a canonical
	// encoding of a curve point.
	ErrInvalidPoint = errors.New("pake: peer sent an invalid curve point")
	// ErrLowOrderPoint is returned when the shared secret collapses to the
	// identity — the peer sent a small-subgroup point (RFC 9382 §3.2).
	ErrLowOrderPoint = errors.New("pake: peer sent a low-order point")
	// ErrConfirmMismatch is returned when the peer's confirmation MAC does not
	// verify: wrong code, or an active attacker without the code.
	ErrConfirmMismatch = errors.New("pake: key confirmation failed")
	// ErrStateMisuse is returned when a method is called out of order.
	ErrStateMisuse = errors.New("pake: protocol state misuse")
)

// RFC 9382 §6 constants for edwards25519. Decoded once at init; a typo would
// fail loudly at process start instead of silently weakening the handshake.
var pointM, pointN *edwards25519.Point

func init() {
	pointM = mustPoint("d048032c6ea0b6d697ddc2e86bda85a33adac920f1bf18e1b0c6d166a5cecdaf")
	pointN = mustPoint("d3bfb518f44f3430f29d0c92af503865a1ed3281dc69b35dd868ba85f886c4ab")
}

func mustPoint(h string) *edwards25519.Point {
	b, err := hex.DecodeString(h)
	if err != nil {
		panic("pake: bad constant hex: " + err.Error())
	}
	p, err := new(edwards25519.Point).SetBytes(b)
	if err != nil {
		panic("pake: bad constant point: " + err.Error())
	}
	return p
}

// Role selects which side of the handshake a State plays.
type Role int

const (
	// RoleDevice is party A: it sends the first message.
	RoleDevice Role = iota
	// RoleHub is party B: it answers the first message.
	RoleHub
)

// PasswordScalar derives w = MHF(pw) mod p (RFC 9382 §3.1). A memory-hard
// function is unnecessary here: the code lives 24 hours, the Hub limits online
// guesses, and the PAKE already rules out offline search. SHA-512 output is
// reduced uniformly into the scalar field.
//
// Callers that persist w instead of the code (the Hub does) get exactly the
// same value back from this function for the same code.
func PasswordScalar(code []byte) []byte {
	sum := sha512.Sum512(code)
	s, err := edwards25519.NewScalar().SetUniformBytes(sum[:])
	if err != nil {
		panic("pake: SetUniformBytes rejected 64 bytes: " + err.Error())
	}
	return s.Bytes()
}

// State holds one party's half of a handshake. It is single-use.
type State struct {
	role   Role
	w      *edwards25519.Scalar
	secret *edwards25519.Scalar // x (device) or y (hub)
	own    []byte               // pA or pB, as sent
	peer   []byte               // pB or pA, as received
	ke     []byte               // session key material
	kcOwn  []byte               // confirmation key we MAC with
	kcPeer []byte               // confirmation key we verify with
	done   bool
}

// NewState starts a handshake for the given role from the password scalar
// produced by PasswordScalar.
func NewState(role Role, passwordScalar []byte) (*State, error) {
	w, err := edwards25519.NewScalar().SetCanonicalBytes(passwordScalar)
	if err != nil {
		return nil, errors.New("pake: password scalar is not canonical")
	}
	var seed [64]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return nil, err
	}
	secret, err := edwards25519.NewScalar().SetUniformBytes(seed[:])
	if err != nil {
		return nil, err
	}
	return &State{role: role, w: w, secret: secret}, nil
}

// Start returns this party's public message (pA for the device, pB for the
// Hub). It may be called once.
func (s *State) Start() ([]byte, error) {
	if s.own != nil {
		return nil, ErrStateMisuse
	}
	// X = x·P, pA = w·M + X   (device)
	// Y = y·P, pB = w·N + Y   (hub)
	pub := new(edwards25519.Point).ScalarBaseMult(s.secret)
	blind := new(edwards25519.Point).ScalarMult(s.w, s.roleConst(s.role))
	msg := new(edwards25519.Point).Add(blind, pub)
	s.own = msg.Bytes()
	return s.own, nil
}

// Finish consumes the peer's public message and derives the keys. It returns
// this party's confirmation MAC, which the caller sends to the peer. The
// session key is only released by Verify after the peer's MAC checks out.
func (s *State) Finish(peerMsg []byte) (confirm []byte, err error) {
	if s.own == nil || s.peer != nil {
		return nil, ErrStateMisuse
	}
	if len(peerMsg) != PointSize {
		return nil, ErrInvalidPoint
	}
	peerPoint, err := new(edwards25519.Point).SetBytes(peerMsg)
	if err != nil {
		return nil, ErrInvalidPoint
	}
	// Strip the peer's blinding: K = h·secret·(peer − w·N) resp. (peer − w·M).
	unblind := new(edwards25519.Point).ScalarMult(s.w, s.roleConst(s.peerRole()))
	stripped := new(edwards25519.Point).Subtract(peerPoint, unblind)
	k := new(edwards25519.Point).ScalarMult(s.secret, stripped)
	k.MultByCofactor(k)
	if k.Equal(edwards25519.NewIdentityPoint()) == 1 {
		return nil, ErrLowOrderPoint
	}
	s.peer = append([]byte(nil), peerMsg...)

	// Transcript TT (RFC 9382 §3.3). pA always precedes pB regardless of role.
	var pA, pB []byte
	if s.role == RoleDevice {
		pA, pB = s.own, s.peer
	} else {
		pA, pB = s.peer, s.own
	}
	tt := make([]byte, 0, 256)
	tt = appendLV(tt, []byte(IdentityDevice))
	tt = appendLV(tt, []byte(IdentityHub))
	tt = appendLV(tt, pA)
	tt = appendLV(tt, pB)
	tt = appendLV(tt, k.Bytes())
	tt = appendLV(tt, s.w.Bytes())

	sum := sha256.Sum256(tt)
	ke, ka := sum[:16], sum[16:]
	kc, err := hkdf.Expand(sha256.New, ka, "ConfirmationKeys", 32)
	if err != nil {
		return nil, err
	}
	kcA, kcB := kc[:16], kc[16:]

	s.ke = ke
	if s.role == RoleDevice {
		s.kcOwn, s.kcPeer = kcA, kcB
		confirm = mac(kcA, pB) // cA = MAC(KcA, pB)
	} else {
		s.kcOwn, s.kcPeer = kcB, kcA
		confirm = mac(kcB, pA) // cB = MAC(KcB, pA)
	}
	return confirm, nil
}

// Verify checks the peer's confirmation MAC and, on success, returns the
// 32-byte session key. A failed verification poisons the state: no key is
// ever released from it.
func (s *State) Verify(peerConfirm []byte) ([]byte, error) {
	if s.ke == nil || s.done {
		return nil, ErrStateMisuse
	}
	s.done = true
	expected := mac(s.kcPeer, s.own) // peer MACs the message *we* sent
	if subtle.ConstantTimeCompare(expected, peerConfirm) != 1 {
		s.ke = nil
		return nil, ErrConfirmMismatch
	}
	key, err := hkdf.Expand(sha256.New, s.ke, "vaultsync-pairing-session-key", KeySize)
	if err != nil {
		return nil, err
	}
	return key, nil
}

func (s *State) peerRole() Role {
	if s.role == RoleDevice {
		return RoleHub
	}
	return RoleDevice
}

func (s *State) roleConst(r Role) *edwards25519.Point {
	if r == RoleDevice {
		return pointM
	}
	return pointN
}

func appendLV(dst, v []byte) []byte {
	dst = binary.LittleEndian.AppendUint64(dst, uint64(len(v)))
	return append(dst, v...)
}

func mac(key, msg []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return h.Sum(nil)
}
