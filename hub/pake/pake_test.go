package pake

import (
	"bytes"
	"errors"
	"testing"

	"filippo.io/edwards25519"
)

func runHandshake(t *testing.T, deviceCode, hubCode string) ([]byte, []byte, error, error) {
	t.Helper()
	dev, err := NewState(RoleDevice, PasswordScalar([]byte(deviceCode)))
	if err != nil {
		t.Fatal(err)
	}
	hub, err := NewState(RoleHub, PasswordScalar([]byte(hubCode)))
	if err != nil {
		t.Fatal(err)
	}
	pA, err := dev.Start()
	if err != nil {
		t.Fatal(err)
	}
	pB, err := hub.Start()
	if err != nil {
		t.Fatal(err)
	}
	cA, err := dev.Finish(pB)
	if err != nil {
		t.Fatal(err)
	}
	cB, err := hub.Finish(pA)
	if err != nil {
		t.Fatal(err)
	}
	keyHub, errHub := hub.Verify(cA)
	keyDev, errDev := dev.Verify(cB)
	return keyDev, keyHub, errDev, errHub
}

func TestHandshakeSameCodeAgreesOnKey(t *testing.T) {
	kd, kh, ed, eh := runHandshake(t, "TULPE-ANKER-93", "TULPE-ANKER-93")
	if ed != nil || eh != nil {
		t.Fatalf("verify errors: device=%v hub=%v", ed, eh)
	}
	if len(kd) != KeySize || !bytes.Equal(kd, kh) {
		t.Fatalf("keys differ or wrong size: %x vs %x", kd, kh)
	}
}

func TestHandshakeDifferentCodesFailsOnBothSides(t *testing.T) {
	kd, kh, ed, eh := runHandshake(t, "TULPE-ANKER-93", "TULPE-ANKER-94")
	if !errors.Is(ed, ErrConfirmMismatch) || !errors.Is(eh, ErrConfirmMismatch) {
		t.Fatalf("expected confirm mismatch on both sides, got device=%v hub=%v", ed, eh)
	}
	if kd != nil || kh != nil {
		t.Fatal("a key leaked from a failed verification")
	}
}

func TestSessionKeysAreFreshPerHandshake(t *testing.T) {
	k1, _, _, _ := runHandshake(t, "A-B-1", "A-B-1")
	k2, _, _, _ := runHandshake(t, "A-B-1", "A-B-1")
	if bytes.Equal(k1, k2) {
		t.Fatal("two handshakes with the same code produced the same key")
	}
}

func TestLowOrderPeerPointIsRejected(t *testing.T) {
	dev, _ := NewState(RoleDevice, PasswordScalar([]byte("x")))
	if _, err := dev.Start(); err != nil {
		t.Fatal(err)
	}
	// The identity is the canonical low-order point; after unblinding and
	// cofactor clearing the shared secret must collapse and be rejected.
	// Craft pB = w·N + identity so unblinding yields exactly the identity.
	w, _ := edwards25519.NewScalar().SetCanonicalBytes(PasswordScalar([]byte("x")))
	pB := new(edwards25519.Point).ScalarMult(w, pointN).Bytes()
	if _, err := dev.Finish(pB); !errors.Is(err, ErrLowOrderPoint) {
		t.Fatalf("expected ErrLowOrderPoint, got %v", err)
	}
}

func TestInvalidPeerEncodingIsRejected(t *testing.T) {
	dev, _ := NewState(RoleDevice, PasswordScalar([]byte("x")))
	_, _ = dev.Start()
	if _, err := dev.Finish(make([]byte, 31)); !errors.Is(err, ErrInvalidPoint) {
		t.Fatalf("short message: %v", err)
	}
	// Find an encoding that is not on the curve (roughly half of all y values).
	var bad []byte
	for i := 1; i < 256 && bad == nil; i++ {
		candidate := make([]byte, 32)
		candidate[0] = byte(i)
		if _, err := new(edwards25519.Point).SetBytes(candidate); err != nil {
			bad = candidate
		}
	}
	if bad == nil {
		t.Fatal("no off-curve encoding found in the probe range")
	}
	if _, err := dev.Finish(bad); !errors.Is(err, ErrInvalidPoint) {
		t.Fatalf("off-curve message: %v", err)
	}
}

func TestStateIsSingleUse(t *testing.T) {
	dev, _ := NewState(RoleDevice, PasswordScalar([]byte("x")))
	if _, err := dev.Finish(make([]byte, 32)); !errors.Is(err, ErrStateMisuse) {
		t.Fatalf("Finish before Start: %v", err)
	}
	if _, err := dev.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := dev.Start(); !errors.Is(err, ErrStateMisuse) {
		t.Fatalf("second Start: %v", err)
	}
	if _, err := dev.Verify(make([]byte, 32)); !errors.Is(err, ErrStateMisuse) {
		t.Fatalf("Verify before Finish: %v", err)
	}
}

func TestPasswordScalarIsDeterministicAndCanonical(t *testing.T) {
	a := PasswordScalar([]byte("TULPE-ANKER-93"))
	b := PasswordScalar([]byte("TULPE-ANKER-93"))
	if !bytes.Equal(a, b) {
		t.Fatal("not deterministic")
	}
	if _, err := edwards25519.NewScalar().SetCanonicalBytes(a); err != nil {
		t.Fatalf("not canonical: %v", err)
	}
	if bytes.Equal(a, PasswordScalar([]byte("tulpe-anker-93"))) {
		t.Fatal("case must matter at this layer; normalization is the caller's job")
	}
}

func TestRoleSwapCannotBeReplayed(t *testing.T) {
	// Two devices with the same code must not be able to complete a handshake
	// with each other: the transcript fixes A/B identities and constants.
	a, _ := NewState(RoleDevice, PasswordScalar([]byte("c")))
	b, _ := NewState(RoleDevice, PasswordScalar([]byte("c")))
	pa, _ := a.Start()
	pb, _ := b.Start()
	ca, err := a.Finish(pb)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := b.Finish(pa)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Verify(cb); err == nil {
		t.Fatal("device/device handshake must not verify")
	}
	if _, err := b.Verify(ca); err == nil {
		t.Fatal("device/device handshake must not verify")
	}
}
