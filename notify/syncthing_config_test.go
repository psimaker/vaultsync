package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// deviceBeforeGUIConfigXML reproduces the tricky real-world ordering: top-level
// <device> elements carry <address>dynamic</address> and appear BEFORE <gui> in
// document order. A "first <address> in the file" scan would wrongly pick
// "dynamic"; the encoding/xml path <configuration><gui><address> must pick the
// GUI's 127.0.0.1:8384.
const deviceBeforeGUIConfigXML = `<configuration version="37">
    <folder id="default" label="Default" path="/sync">
        <device id="XTR5MC4-3TTKWWM-4KLOCWL-TPKSCR6-IBBUAUX-7PKZ6CS-MKHCF5M-M3NUPQ2"></device>
    </folder>
    <device id="XTR5MC4-3TTKWWM-4KLOCWL-TPKSCR6-IBBUAUX-7PKZ6CS-MKHCF5M-M3NUPQ2" name="server" compression="metadata">
        <address>dynamic</address>
        <paused>false</paused>
    </device>
    <device id="3I5PPP2-GQZXLRG-VJDXONQ-OR7BRNS-DO2L57Z-X6HW6Q6-5ZAW74D-DMBLHA3" name="phone" compression="metadata">
        <address>dynamic</address>
    </device>
    <gui enabled="true" tls="false" debugging="false">
        <address>127.0.0.1:8384</address>
        <apikey>fixture-api-key-12345</apikey>
        <theme>default</theme>
    </gui>
    <options>
        <listenAddress>default</listenAddress>
    </options>
</configuration>`

func writeFixture(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.xml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return p
}

func TestParseSyncthingConfigPicksGUINotDeviceAddress(t *testing.T) {
	cfg, err := parseSyncthingConfig(writeFixture(t, deviceBeforeGUIConfigXML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.GUI.APIKey != "fixture-api-key-12345" {
		t.Fatalf("apikey = %q, want fixture-api-key-12345", cfg.GUI.APIKey)
	}
	if cfg.GUI.Address != "127.0.0.1:8384" {
		t.Fatalf("gui address = %q, want 127.0.0.1:8384 (must NOT be a device's \"dynamic\")", cfg.GUI.Address)
	}
	if cfg.GUI.TLS != "false" {
		t.Fatalf("gui tls = %q, want false", cfg.GUI.TLS)
	}
}

func TestInferSyncthingURL(t *testing.T) {
	cases := []struct {
		name    string
		address string
		tls     bool
		want    string
	}{
		{"loopback", "127.0.0.1:8384", false, "http://127.0.0.1:8384"},
		{"tls", "127.0.0.1:8384", true, "https://127.0.0.1:8384"},
		{"wildcard ipv4", "0.0.0.0:8384", false, "http://127.0.0.1:8384"},
		{"bare port", ":8384", false, "http://127.0.0.1:8384"},
		{"wildcard ipv6", "[::]:8384", false, "http://127.0.0.1:8384"},
		{"empty", "", false, "http://127.0.0.1:8384"},
		{"already url", "https://syncthing.local:8384", false, "https://syncthing.local:8384"},
		{"custom host", "10.0.0.5:9000", false, "http://10.0.0.5:9000"},
		{"unix-socket-fallback", "/run/syncthing.sock", false, "http://127.0.0.1:8384"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := inferSyncthingURL(tc.address, tc.tls); got != tc.want {
				t.Fatalf("inferSyncthingURL(%q, %v) = %q, want %q", tc.address, tc.tls, got, tc.want)
			}
		})
	}
}

func TestDetectSyncthingFromCandidatesSkipsMissingThenFinds(t *testing.T) {
	good := writeFixture(t, deviceBeforeGUIConfigXML)
	det, err := detectSyncthingFromCandidates([]string{
		filepath.Join(t.TempDir(), "does-not-exist.xml"),
		"", // empty entries are skipped
		good,
	})
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if det.APIKey != "fixture-api-key-12345" {
		t.Fatalf("APIKey = %q", det.APIKey)
	}
	if det.APIURL != "http://127.0.0.1:8384" {
		t.Fatalf("APIURL = %q", det.APIURL)
	}
	if det.Source != good {
		t.Fatalf("Source = %q, want %q", det.Source, good)
	}
}

func TestDetectSyncthingFromCandidatesNoKeyIsAClearError(t *testing.T) {
	noKey := writeFixture(t, `<configuration version="37">
    <gui enabled="true" tls="false">
        <address>127.0.0.1:8384</address>
        <apikey></apikey>
    </gui>
</configuration>`)
	_, err := detectSyncthingFromCandidates([]string{noKey})
	if err == nil {
		t.Fatal("expected an error when the GUI API key is empty")
	}
	if !strings.Contains(err.Error(), "API key") {
		t.Fatalf("error %q should explain the missing API key", err)
	}
}

func TestParseSyncthingConfigMalformedXML(t *testing.T) {
	p := writeFixture(t, `<configuration><gui><apikey>k</apikey></gui`) // unclosed
	_, err := parseSyncthingConfig(p)
	if err == nil {
		t.Fatal("expected a parse error for malformed XML")
	}
	if !strings.Contains(err.Error(), "parse XML") {
		t.Fatalf("error should indicate XML parsing failure, got: %v", err)
	}
}

func TestDetectSyncthingFromCandidatesPermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses file permission checks")
	}
	p := writeFixture(t, deviceBeforeGUIConfigXML)
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o600) }) // let TempDir cleanup remove it

	_, err := detectSyncthingFromCandidates([]string{p})
	if err == nil {
		t.Fatal("expected a permission error for an unreadable config")
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("error should wrap os.ErrPermission, got: %v", err)
	}
	uid, gid, ok := fileOwner(p)
	if !ok {
		t.Fatalf("fileOwner should resolve the fixture owner on unix")
	}
	wantHint := fmt.Sprintf("-u %d:%d", uid, gid)
	if !strings.Contains(err.Error(), wantHint) {
		t.Fatalf("error should carry the exact %q fix, got: %v", wantHint, err)
	}
}

// An unreadable config *directory* (0700, foreign owner) denies the stat of
// config.xml itself; the owner hint must then come from the directory, which
// Syncthing keeps under the same user as the file.
func TestPermissionDeniedHintFallsBackToDirectoryOwner(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses file permission checks")
	}
	p := writeFixture(t, deviceBeforeGUIConfigXML)
	dir := filepath.Dir(p)
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) // let TempDir cleanup remove it

	_, err := detectSyncthingFromCandidates([]string{p})
	if err == nil {
		t.Fatal("expected a permission error for a config behind an unreadable directory")
	}
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("error should wrap os.ErrPermission, got: %v", err)
	}
	uid, gid, ok := fileOwner(dir)
	if !ok {
		t.Fatalf("fileOwner should resolve the directory owner on unix")
	}
	wantHint := fmt.Sprintf("-u %d:%d", uid, gid)
	if !strings.Contains(err.Error(), wantHint) {
		t.Fatalf("error should carry the directory-derived %q fix, got: %v", wantHint, err)
	}
}

func TestDetectSyncthingContinuesPastUnreadableCandidate(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses file permission checks")
	}
	bad := writeFixture(t, deviceBeforeGUIConfigXML)
	if err := os.Chmod(bad, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(bad, 0o600) })
	good := writeFixture(t, deviceBeforeGUIConfigXML)

	det, err := detectSyncthingFromCandidates([]string{bad, good})
	if err != nil {
		t.Fatalf("detection must fall through an unreadable candidate to a readable one: %v", err)
	}
	if det.Source != good {
		t.Fatalf("Source = %q, want the readable candidate %q", det.Source, good)
	}
}

func TestDetectSyncthingFromCandidatesNotFound(t *testing.T) {
	_, err := detectSyncthingFromCandidates([]string{filepath.Join(t.TempDir(), "absent.xml")})
	if err == nil {
		t.Fatal("expected an error when no config.xml exists")
	}
	if !strings.Contains(err.Error(), "no Syncthing config.xml found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPlatformCandidatesIncludeOfficialImagePath(t *testing.T) {
	got := platformSyncthingConfigCandidates()
	idx := func(want string) int {
		for i, p := range got {
			if p == want {
				return i
			}
		}
		return -1
	}
	official := idx("/var/syncthing/config/config.xml") // STHOMEDIR layout
	legacy := idx("/var/syncthing/config.xml")
	lsio := idx("/config/config.xml")
	if official < 0 {
		t.Fatalf("candidates must include the official image path /var/syncthing/config/config.xml; got %v", got)
	}
	if lsio < 0 {
		t.Fatalf("candidates must include the linuxserver image path /config/config.xml; got %v", got)
	}
	if legacy >= 0 && official > legacy {
		t.Fatalf("the correct STHOMEDIR path must be probed before the legacy /var/syncthing/config.xml; got %v", got)
	}
}

func TestSyncthingConfigEnvOverrideIsFirst(t *testing.T) {
	t.Setenv("SYNCTHING_CONFIG", "/custom/path/config.xml")
	got := platformSyncthingConfigCandidates()
	if len(got) == 0 || got[0] != "/custom/path/config.xml" {
		t.Fatalf("SYNCTHING_CONFIG must be the first candidate; got %v", got)
	}
}

func TestDetectSyncthingFromCandidatesEmptyListIsAClearError(t *testing.T) {
	_, err := detectSyncthingFromCandidates(nil)
	if err == nil {
		t.Fatal("expected an error with no candidates")
	}
	if !strings.Contains(err.Error(), "no Syncthing config.xml location") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- NAS host layouts (#86) --------------------------------------------------

func TestPlatformCandidatesIncludeNASHostLayouts_Issue86(t *testing.T) {
	got := platformSyncthingConfigCandidates()
	idx := func(want string) int {
		for i, p := range got {
			if p == want {
				return i
			}
		}
		return -1
	}
	// The one-step installer runs on the NAS *host*: the package/appdata
	// layouts — not the container-internal paths — hold config.xml there.
	statics := []string{
		"/var/packages/syncthing/var/config.xml",        // Synology DSM 7 package
		"/var/packages/syncthing/target/var/config.xml", // Synology target -> @appstore
		"/mnt/user/appdata/syncthing/config.xml",        // Unraid linuxserver template
	}
	for _, want := range statics {
		if idx(want) < 0 {
			t.Fatalf("candidates must include the NAS host path %s (#86); got %v", want, got)
		}
	}
	// Appended AFTER every pre-#86 candidate on purpose: no existing setup may
	// change which config it resolves.
	if last := idx("/etc/syncthing/config.xml"); last < 0 || idx(statics[0]) < last {
		t.Fatalf("NAS host paths must come after the pre-existing probe list; got %v", got)
	}
}

func TestNASGlobCandidatesExpand_Issue86(t *testing.T) {
	orig := nasGlobFn
	defer func() { nasGlobFn = orig }()
	nasGlobFn = func(pattern string) ([]string, error) {
		switch pattern {
		case "/volume*/@appdata/syncthing/config.xml":
			return []string{"/volume1/@appdata/syncthing/config.xml"}, nil
		case "/share/*/.qpkg/*yncthing*/var/config.xml":
			return []string{"/share/CACHEDEV1_DATA/.qpkg/QSyncthing/var/config.xml"}, nil
		}
		return nil, nil
	}
	got := platformSyncthingConfigCandidates()
	for _, want := range []string{
		"/volume1/@appdata/syncthing/config.xml",
		"/share/CACHEDEV1_DATA/.qpkg/QSyncthing/var/config.xml",
	} {
		found := false
		for _, p := range got {
			if p == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("glob-derived NAS candidate %s missing (#86); got %v", want, got)
		}
	}
}
