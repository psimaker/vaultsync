package main

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	diagnosticsKeyIDDomain              = "eu.vaultsync.key-id/ed25519/v1\x00"
	diagnosticsDeviceBindingDomain      = "eu.vaultsync.binding/syncthing-device/v1\x00"
	diagnosticsFolderBindingDomain      = "eu.vaultsync.binding/syncthing-folder/v1\x00"
	diagnosticsPairingHMACDomain        = "eu.vaultsync.helper-pairing/v1/bootstrap-hmac\x00"
	diagnosticsPairingFingerprintDomain = "eu.vaultsync.helper-pairing/v1/transcript-fingerprint\x00"
)

var errDiagnosticsTLSPinMismatch = errors.New("diagnostics TLS identity unavailable")

func diagnosticsDomainSHA256(domain string, body []byte) [32]byte {
	hash := sha256.New()
	_, _ = hash.Write([]byte(domain))
	_, _ = hash.Write(body)
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return digest
}

func diagnosticsKeyID(publicKey []byte) [32]byte {
	return diagnosticsDomainSHA256(diagnosticsKeyIDDomain, publicKey)
}

func diagnosticsDeviceIDDigest(rawDeviceID []byte) ([32]byte, error) {
	if len(rawDeviceID) != 32 {
		return [32]byte{}, errDiagnosticsPairingInvalid
	}
	return diagnosticsDomainSHA256(diagnosticsDeviceBindingDomain, rawDeviceID), nil
}

func diagnosticsFolderIDDigest(folderID string) ([32]byte, error) {
	if folderID == "" || len(folderID) > 255 || !utf8.ValidString(folderID) {
		return [32]byte{}, errDiagnosticsPairingInvalid
	}
	bytes := []byte(folderID)
	body := make([]byte, 4, 4+len(bytes))
	binary.BigEndian.PutUint32(body, uint32(len(bytes)))
	body = append(body, bytes...)
	return diagnosticsDomainSHA256(diagnosticsFolderBindingDomain, body), nil
}

func diagnosticsTLSSPKIPin(spkiDER []byte) ([32]byte, error) {
	if len(spkiDER) == 0 {
		return [32]byte{}, errDiagnosticsTLSPinMismatch
	}
	if _, err := x509.ParsePKIXPublicKey(spkiDER); err != nil {
		return [32]byte{}, errDiagnosticsTLSPinMismatch
	}
	return sha256.Sum256(spkiDER), nil
}

func diagnosticsPairingBootstrapHMAC(secret []byte, appRequest diagnosticsCBORValue) ([32]byte, error) {
	if len(secret) != 32 || appRequest.kind != diagnosticsCBORMap {
		return [32]byte{}, errDiagnosticsPairingInvalid
	}
	body, err := encodeDiagnosticsCBOR(diagnosticsCBORWithoutLabels(appRequest, 21, 255))
	if err != nil {
		return [32]byte{}, errDiagnosticsPairingInvalid
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(diagnosticsPairingHMACDomain))
	_, _ = mac.Write(body)
	var result [32]byte
	copy(result[:], mac.Sum(nil))
	return result, nil
}

func verifyDiagnosticsPairingBootstrapHMAC(secret []byte, request diagnosticsPairingMessage) bool {
	if request.messageType != diagnosticsPairingAppRequest {
		return false
	}
	want, err := diagnosticsPairingBootstrapHMAC(secret, request.value)
	if err != nil {
		return false
	}
	got, ok := request.bytesField(21, 32)
	return ok && subtle.ConstantTimeCompare(want[:], got) == 1
}

func diagnosticsPairingFingerprint(appRequestDigest, helperAcceptDigest []byte) (string, error) {
	if len(appRequestDigest) != 32 || len(helperAcceptDigest) != 32 {
		return "", errDiagnosticsPairingInvalid
	}
	body := make([]byte, 0, 64)
	body = append(body, appRequestDigest...)
	body = append(body, helperAcceptDigest...)
	digest := diagnosticsDomainSHA256(diagnosticsPairingFingerprintDomain, body)
	return strings.ToUpper(hex.EncodeToString(digest[:6])), nil
}

func encodeDiagnosticsPairingQR(value diagnosticsCBORValue) (string, error) {
	encoded, err := encodeDiagnosticsCBOR(value)
	if err != nil {
		return "", errDiagnosticsPairingInvalid
	}
	message, err := decodeDiagnosticsPairingMessage(encoded)
	if err != nil || message.messageType != diagnosticsPairingQR {
		return "", errDiagnosticsPairingInvalid
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeDiagnosticsPairingQR(encoded string) (diagnosticsPairingMessage, error) {
	if encoded == "" || len(encoded) > base64.RawURLEncoding.EncodedLen(diagnosticsMaximumMessageBytes) || strings.Contains(encoded, "=") {
		return diagnosticsPairingMessage{}, errDiagnosticsPairingInvalid
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(data) != encoded {
		return diagnosticsPairingMessage{}, errDiagnosticsPairingInvalid
	}
	message, err := decodeDiagnosticsPairingMessage(data)
	if err != nil || message.messageType != diagnosticsPairingQR {
		return diagnosticsPairingMessage{}, errDiagnosticsPairingInvalid
	}
	return message, nil
}

func newDiagnosticsSigningIdentity(random io.Reader) (seed, publicKey, keyID []byte, err error) {
	if random == nil {
		random = rand.Reader
	}
	public, private, err := ed25519.GenerateKey(random)
	if err != nil {
		return nil, nil, nil, err
	}
	seed = append([]byte(nil), private.Seed()...)
	publicKey = append([]byte(nil), public...)
	id := diagnosticsKeyID(publicKey)
	keyID = append([]byte(nil), id[:]...)
	return seed, publicKey, keyID, nil
}

func newDiagnosticsTLSIdentity(random io.Reader) (privatePKCS8, spkiDER, pin []byte, err error) {
	if random == nil {
		random = rand.Reader
	}
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), random)
	if err != nil {
		return nil, nil, nil, err
	}
	privatePKCS8, err = x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, nil, nil, err
	}
	spkiDER, err = x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, nil, nil, err
	}
	digest, err := diagnosticsTLSSPKIPin(spkiDER)
	if err != nil {
		return nil, nil, nil, err
	}
	return privatePKCS8, spkiDER, append([]byte(nil), digest[:]...), nil
}

func diagnosticsSigningPrivateKey(seed []byte) (ed25519.PrivateKey, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, errDiagnosticsPairingInvalid
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// diagnosticsPinnedTLSConfig is intentionally not connected to an HTTP client.
// A later transport milestone may use it only with the fixed local pairing path.
// InsecureSkipVerify disables CA/hostname trust because the QR-delivered SPKI
// pin is the sole endpoint identity; VerifyConnection enforces that pin.
func diagnosticsPinnedTLSConfig(pin []byte) (*tls.Config, error) {
	if len(pin) != 32 {
		return nil, errDiagnosticsTLSPinMismatch
	}
	pinned := append([]byte(nil), pin...)
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // See VerifyConnection above; never TOFU.
		VerifyConnection: func(state tls.ConnectionState) error {
			if state.Version != tls.VersionTLS13 || len(state.PeerCertificates) == 0 {
				return errDiagnosticsTLSPinMismatch
			}
			spki := state.PeerCertificates[0].RawSubjectPublicKeyInfo
			digest := sha256.Sum256(spki)
			if subtle.ConstantTimeCompare(digest[:], pinned) != 1 {
				return errDiagnosticsTLSPinMismatch
			}
			return nil
		},
	}, nil
}
