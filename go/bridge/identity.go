// Device identity loading with a fail-closed guarantee (#135).
package bridge

import (
	"crypto/tls"
	"errors"
	"fmt"
	"os"

	"github.com/syncthing/syncthing/lib/syncthing"
)

// loadOrCreateIdentity loads the TLS certificate that anchors this device's
// Syncthing identity, generating one only on a confirmed first launch (both
// files verifiably absent).
//
// It deliberately replaces upstream's LoadOrGenerateCertificate, which
// regenerates a fresh key pair whenever loading fails. On iOS a transient
// read failure (file protection before first unlock, corruption after a hard
// crash) would then silently mint a new device ID — invalidating every peer
// pairing and surfacing server-side as an endless stream of new pending
// devices (#135). A device identity must never change implicitly: on any
// ambiguous on-disk state we fail closed and let the engine start fail
// visibly instead.
func loadOrCreateIdentity(certPath, keyPath string) (tls.Certificate, error) {
	certExists, err := identityFileExists(certPath)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("device certificate unreadable, refusing to regenerate identity: %w", err)
	}
	keyExists, err := identityFileExists(keyPath)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("device key unreadable, refusing to regenerate identity: %w", err)
	}

	switch {
	case certExists && keyExists:
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("device identity exists but failed to load, refusing to regenerate: %w", err)
		}
		return cert, nil
	case certExists || keyExists:
		return tls.Certificate{}, errors.New("partial device identity on disk (one of certificate/key missing), refusing to regenerate")
	default:
		return syncthing.GenerateCertificate(certPath, keyPath)
	}
}

// identityFileExists reports whether the file verifiably exists or verifiably
// does not. Any other stat outcome (e.g. permission or I/O errors) returns an
// error so the caller fails closed rather than treating an unreadable
// identity as absent and generating a replacement.
func identityFileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}
