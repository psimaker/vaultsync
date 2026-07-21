// Regression tests for the fail-closed device identity guarantee (#135).
//
// Reported scenario: during a crash loop the app burned five device
// identities in one day — each engine start after a failed certificate load
// silently generated a new key pair, because upstream's
// LoadOrGenerateCertificate falls back to generation on any load error.
// These tests pin the replacement behavior: generate only on a confirmed
// first launch, otherwise fail closed and never touch the existing files.
package bridge

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/syncthing/syncthing/lib/protocol"
)

func identityPaths(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
}

func TestFirstLaunchGeneratesIdentityOnce(t *testing.T) {
	certPath, keyPath := identityPaths(t)

	first, err := loadOrCreateIdentity(certPath, keyPath)
	if err != nil {
		t.Fatalf("first launch: %v", err)
	}
	second, err := loadOrCreateIdentity(certPath, keyPath)
	if err != nil {
		t.Fatalf("second launch: %v", err)
	}

	firstID := protocol.NewDeviceID(first.Certificate[0])
	secondID := protocol.NewDeviceID(second.Certificate[0])
	if firstID != secondID {
		t.Fatalf("device ID changed across restarts: %s -> %s", firstID, secondID)
	}
}

func TestCorruptCertificateFailsClosedWithoutRegenerating(t *testing.T) {
	certPath, keyPath := identityPaths(t)
	if _, err := loadOrCreateIdentity(certPath, keyPath); err != nil {
		t.Fatalf("seed identity: %v", err)
	}

	corrupt := []byte("not a certificate")
	if err := os.WriteFile(certPath, corrupt, 0o600); err != nil {
		t.Fatalf("corrupt cert: %v", err)
	}
	keyBefore, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}

	if _, err := loadOrCreateIdentity(certPath, keyPath); err == nil {
		t.Fatal("corrupt certificate must fail closed, got a certificate")
	}

	certAfter, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read cert after: %v", err)
	}
	keyAfter, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key after: %v", err)
	}
	if !bytes.Equal(certAfter, corrupt) || !bytes.Equal(keyAfter, keyBefore) {
		t.Fatal("failed load must not rewrite identity files")
	}
}

func TestPartialIdentityFailsClosedWithoutRegenerating(t *testing.T) {
	for _, missing := range []string{"cert", "key"} {
		t.Run("missing_"+missing, func(t *testing.T) {
			certPath, keyPath := identityPaths(t)
			if _, err := loadOrCreateIdentity(certPath, keyPath); err != nil {
				t.Fatalf("seed identity: %v", err)
			}

			removed := certPath
			survivor := keyPath
			if missing == "key" {
				removed = keyPath
				survivor = certPath
			}
			if err := os.Remove(removed); err != nil {
				t.Fatalf("remove %s: %v", missing, err)
			}
			survivorBefore, err := os.ReadFile(survivor)
			if err != nil {
				t.Fatalf("read survivor: %v", err)
			}

			if _, err := loadOrCreateIdentity(certPath, keyPath); err == nil {
				t.Fatal("partial identity must fail closed, got a certificate")
			}
			if _, err := os.Stat(removed); !os.IsNotExist(err) {
				t.Fatal("failed load must not recreate the missing identity file")
			}
			survivorAfter, err := os.ReadFile(survivor)
			if err != nil {
				t.Fatalf("read survivor after: %v", err)
			}
			if !bytes.Equal(survivorBefore, survivorAfter) {
				t.Fatal("failed load must not rewrite the surviving identity file")
			}
		})
	}
}

func TestUnreadableIdentityFailsClosedWithoutRegenerating(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not restrict root")
	}
	certPath, keyPath := identityPaths(t)
	if _, err := loadOrCreateIdentity(certPath, keyPath); err != nil {
		t.Fatalf("seed identity: %v", err)
	}

	// Simulate the iOS file-protection failure mode: the files exist but a
	// stat/read is denied. os.Stat still succeeds on a mode-0 file, so deny
	// directory search permission instead, which fails the stat itself.
	dir := filepath.Dir(certPath)
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if _, err := loadOrCreateIdentity(certPath, keyPath); err == nil {
		t.Fatal("unreadable identity must fail closed, got a certificate")
	}

	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("restore dir mode: %v", err)
	}
	reloaded, err := loadOrCreateIdentity(certPath, keyPath)
	if err != nil {
		t.Fatalf("reload after restoring access: %v", err)
	}
	if len(reloaded.Certificate) == 0 {
		t.Fatal("expected the original identity to survive the outage")
	}
}
