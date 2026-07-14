package main

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"path/filepath"
	"testing"
	"time"
)

func TestDiagnosticsServerTLSIsTLS13OnlyAndPinsCredentialSPKI(t *testing.T) {
	deviceDigest := bytes.Repeat([]byte{0x61}, 32)
	store, err := openDiagnosticsCredentialStore(filepath.Join(t.TempDir(), "credentials"), deviceDigest, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	config, err := newDiagnosticsServerTLSConfig(store, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if config.MinVersion != tls.VersionTLS13 || config.MaxVersion != tls.VersionTLS13 ||
		len(config.NextProtos) != 1 || config.NextProtos[0] != "http/1.1" {
		t.Fatalf("TLS boundary = min %x max %x ALPN %v", config.MinVersion, config.MaxVersion, config.NextProtos)
	}
	certificate, err := config.GetCertificate(nil)
	if err != nil || certificate.Leaf == nil {
		t.Fatalf("certificate = %+v, %v", certificate, err)
	}
	state, _ := store.snapshot()
	wantPin, _ := diagnosticsTLSPrivateKeyPin(state.Identity.TLSPrivatePKCS8)
	gotPin, _ := diagnosticsTLSSPKIPin(certificate.Leaf.RawSubjectPublicKeyInfo)
	if !bytes.Equal(wantPin, gotPin[:]) {
		t.Fatalf("certificate SPKI pin = %x, want %x", gotPin, wantPin)
	}
	if len(certificate.Leaf.DNSNames) != 0 || len(certificate.Leaf.IPAddresses) != 0 ||
		certificate.Leaf.Subject.CommonName != "VaultSync Diagnostics" {
		t.Fatalf("certificate leaked endpoint identity: %+v", certificate.Leaf)
	}
}

func TestDiagnosticsTLSRotationServesProposedPinOnlyAfterGlobalCommit(t *testing.T) {
	manager, clock := newDiagnosticsTestPairingManager(t)
	recordID, appPrivate := activateDiagnosticsTestInstallation(
		t, manager, clock, bytes.Repeat([]byte{0x62}, 32), 0x62,
	)
	stateBefore, _ := manager.store.snapshot()
	oldPin, _ := diagnosticsTLSPrivateKeyPin(stateBefore.Identity.TLSPrivatePKCS8)
	proposalBytes, err := manager.beginTLSPinRotation(recordID)
	if err != nil {
		t.Fatal(err)
	}
	proposal, _ := decodeDiagnosticsPairingMessage(proposalBytes)
	proposalDigest, _ := proposal.digest()
	now := uint64(clock.current().Unix())
	confirmationBytes, err := buildDiagnosticsLifecycleContinuation(
		proposal, diagnosticsPairingTLSPinRotationConfirm, 0, nil, appPrivate,
		now, now+300, bytes.Repeat([]byte{0x63}, 32),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.handleLifecycleMessage(confirmationBytes); err != nil {
		t.Fatal(err)
	}
	confirmation, _ := decodeDiagnosticsPairingMessage(confirmationBytes)
	finalize, err := buildDiagnosticsLifecycleContinuation(
		confirmation, diagnosticsPairingLifecycleFinalize, diagnosticsPairingTransitionTLSPin, proposalDigest[:], appPrivate,
		now, now+300, bytes.Repeat([]byte{0x64}, 32),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.handleLifecycleMessage(finalize); err != nil {
		t.Fatal(err)
	}
	committed, _ := manager.store.snapshot()
	if !bytes.Equal(committed.Identity.TLSPrivatePKCS8, stateBefore.Identity.TLSPrivatePKCS8) {
		t.Fatal("TLS identity switched before proposed-pin query")
	}
	proposedPrivate := diagnosticsTLSHandshakePrivateKey(committed, clock.current())
	proposedPin, _ := diagnosticsTLSPrivateKeyPin(proposedPrivate)
	if bytes.Equal(oldPin, proposedPin) {
		t.Fatal("globally committed TLS proposal did not select the proposed handshake pin")
	}
	if err := manager.confirmLifecycleTransition(recordID, proposalDigest[:]); err != nil {
		t.Fatal(err)
	}
	confirmed, _ := manager.store.snapshot()
	confirmedPin, _ := diagnosticsTLSPrivateKeyPin(confirmed.Identity.TLSPrivatePKCS8)
	if !bytes.Equal(confirmedPin, proposedPin) || confirmed.Authorizations[0].Transition != nil {
		t.Fatal("proposed TLS pin was not terminal after capability confirmation")
	}
}
