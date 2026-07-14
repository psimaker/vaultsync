package main

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"sync"
	"time"
)

var errDiagnosticsTLSIdentityUnavailable = errors.New("diagnostics TLS identity unavailable")

type diagnosticsCertificateSource struct {
	store *diagnosticsCredentialStore
	now   func() time.Time
	mutex sync.Mutex
	pin   [32]byte
	cert  *tls.Certificate
}

func newDiagnosticsServerTLSConfig(store *diagnosticsCredentialStore, now func() time.Time) (*tls.Config, error) {
	if store == nil {
		return nil, errDiagnosticsTLSIdentityUnavailable
	}
	if now == nil {
		now = time.Now
	}
	source := &diagnosticsCertificateSource{store: store, now: now}
	if _, err := source.currentCertificate(); err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		MaxVersion: tls.VersionTLS13,
		NextProtos: []string{"http/1.1"},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return source.currentCertificate()
		},
	}, nil
}

func (source *diagnosticsCertificateSource) currentCertificate() (*tls.Certificate, error) {
	state, err := source.store.snapshot()
	if err != nil {
		return nil, errDiagnosticsTLSIdentityUnavailable
	}
	privatePKCS8 := diagnosticsTLSHandshakePrivateKey(state, source.now())
	pinBytes, err := diagnosticsTLSPrivateKeyPin(privatePKCS8)
	if err != nil || len(pinBytes) != 32 {
		return nil, errDiagnosticsTLSIdentityUnavailable
	}
	var pin [32]byte
	copy(pin[:], pinBytes)

	source.mutex.Lock()
	defer source.mutex.Unlock()
	if source.cert != nil && source.pin == pin && source.now().Before(source.cert.Leaf.NotAfter.Add(-24*time.Hour)) {
		return source.cert, nil
	}
	certificate, err := diagnosticsSelfSignedCertificate(privatePKCS8, source.now())
	if err != nil {
		return nil, err
	}
	source.pin = pin
	source.cert = certificate
	return source.cert, nil
}

// A TLS-key transition cannot present two different SPKIs in one TLS
// handshake. The listener therefore stays on the current pin until every
// active authorization has durably committed the same proposal, then serves
// the proposed pin while each app performs its exact proposed-state query.
// The old private key remains protected in state until every query confirms.
func diagnosticsTLSHandshakePrivateKey(state diagnosticsCredentialState, now time.Time) []byte {
	for index := range state.Authorizations {
		authorization := &state.Authorizations[index]
		transition := authorization.Transition
		if authorization.State != "active" || transition == nil || transition.Kind != diagnosticsPairingTransitionTLSPin ||
			transition.Stage != "committed" || now.Unix() >= transition.ExpiresAt {
			continue
		}
		if allDiagnosticsAuthorizationsCommittedForTLS(state.Authorizations, transition, now.Unix()) {
			return transition.ProposedTLSPrivate
		}
		break
	}
	return state.Identity.TLSPrivatePKCS8
}

func diagnosticsSelfSignedCertificate(privatePKCS8 []byte, now time.Time) (*tls.Certificate, error) {
	parsed, err := x509.ParsePKCS8PrivateKey(privatePKCS8)
	if err != nil {
		return nil, errDiagnosticsTLSIdentityUnavailable
	}
	privateKey, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || privateKey.Curve == nil || privateKey.Curve.Params().Name != "P-256" {
		return nil, errDiagnosticsTLSIdentityUnavailable
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil || serial.Sign() == 0 {
		return nil, errDiagnosticsTLSIdentityUnavailable
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: "VaultSync Diagnostics",
		},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(30 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	encoded, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, errDiagnosticsTLSIdentityUnavailable
	}
	leaf, err := x509.ParseCertificate(encoded)
	if err != nil {
		return nil, errDiagnosticsTLSIdentityUnavailable
	}
	return &tls.Certificate{
		Certificate: [][]byte{encoded},
		PrivateKey:  privateKey,
		Leaf:        leaf,
	}, nil
}
