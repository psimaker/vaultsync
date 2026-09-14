package main

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Notes":                 "notes",
		"  My  Vault ":          "my-vault",
		"../../etc":             "etc",
		"Notizen für Ärzte":     "notizen-fuer-aerzte",
		"Straße_2":              "strasse-2",
		"日本語":                   "",
		"":                      "",
		"..":                    "",
		"a/b\\c":                "abc",
		"Work (2024).Backup":    "work-2024-backup",
		"-----":                 "",
		"Vault.Name.With.Dots":  "vault-name-with-dots",
		"UPPER lower MiXeD 123": "upper-lower-mixed-123",
	}
	for in, want := range cases {
		got, err := slugify(in)
		if want == "" {
			if err == nil {
				t.Errorf("slugify(%q) = %q, want error", in, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("slugify(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	long := make([]byte, maxVaultNameLength+1)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := slugify(string(long)); !errors.Is(err, errVaultNameTooLong) {
		t.Errorf("long name: %v", err)
	}
}

func TestPathsOverlap(t *testing.T) {
	root := filepath.Join("/srv", "vaults")
	if !pathsOverlap(filepath.Join(root, "a"), filepath.Join(root, "a")) {
		t.Error("equal paths must overlap")
	}
	if !pathsOverlap(root, filepath.Join(root, "a")) || !pathsOverlap(filepath.Join(root, "a"), root) {
		t.Error("nested paths must overlap in both directions")
	}
	if pathsOverlap(filepath.Join(root, "a"), filepath.Join(root, "ab")) {
		t.Error("sibling with common prefix must not overlap")
	}
	if pathsOverlap(filepath.Join(root, "a"), filepath.Join(root, "b")) {
		t.Error("siblings must not overlap")
	}
}

func newTestProvisioner(t *testing.T, f *fakeSyncthing, client *SyncthingClient) *provisioner {
	t.Helper()
	p := newProvisioner(client, "/var/syncthing/vaults", "/var/syncthing/vaults")
	dirs := map[string]bool{} // path → non-empty
	p.dirState = func(path string) (bool, bool, error) {
		nonEmpty, ok := dirs[path]
		return ok, !nonEmpty, nil
	}
	p.mkdir = func(path string) error { dirs[path] = false; return nil }
	t.Cleanup(func() { _ = f })
	// expose for tests that need to pre-populate a directory
	testDirs[p] = dirs
	return p
}

var testDirs = map[*provisioner]map[string]bool{}

func TestCreateVaultHappyPath(t *testing.T) {
	f, srv := newFakeSyncthing(t, fakeHubID)
	p := newTestProvisioner(t, f, f.client(srv))
	folder, err := p.createVault(context.Background(), "My Notes", false)
	if err != nil {
		t.Fatal(err)
	}
	if folder.Path != filepath.Join("/var/syncthing/vaults", "my-notes") || folder.Label != "My Notes" {
		t.Fatalf("unexpected folder %+v", folder)
	}
	stored := f.folder(folder.ID)
	if stored == nil {
		t.Fatal("folder not stored in Syncthing")
	}
	if stored.MaxConflicts != -1 || stored.Versioning.Type != "staggered" || stored.Type != "sendreceive" || !stored.IgnorePerms {
		t.Fatalf("hub defaults missing: %+v", stored)
	}
	if len(stored.Devices) != 1 || stored.Devices[0].DeviceID != fakeHubID {
		t.Fatalf("new vault must be shared with nobody but the hub: %+v", stored.Devices)
	}
	if _, ok := testDirs[p][folder.Path]; !ok {
		t.Fatal("directory was not created")
	}
}

func TestCreateVaultRefusesDuplicateLabel(t *testing.T) {
	f, srv := newFakeSyncthing(t, fakeHubID)
	p := newTestProvisioner(t, f, f.client(srv))
	if _, err := p.createVault(context.Background(), "Notes", false); err != nil {
		t.Fatal(err)
	}
	if _, err := p.createVault(context.Background(), "notes", false); !errors.Is(err, errVaultExists) {
		t.Fatalf("case-insensitive duplicate accepted: %v", err)
	}
}

func TestCreateVaultRefusesOverlap(t *testing.T) {
	f, srv := newFakeSyncthing(t, fakeHubID)
	p := newTestProvisioner(t, f, f.client(srv))
	// An operator-added folder that sits at the vaults root itself.
	f.folders = append(f.folders, folderConfig{ID: "manual", Label: "Everything", Path: "/var/syncthing/vaults"})
	if _, err := p.createVault(context.Background(), "Notes", false); !errors.Is(err, errPathOverlap) {
		t.Fatalf("nested under an existing folder accepted: %v", err)
	}
}

func TestCreateVaultRefusesNonEmptyDirectoryWithoutAdopt(t *testing.T) {
	f, srv := newFakeSyncthing(t, fakeHubID)
	p := newTestProvisioner(t, f, f.client(srv))
	testDirs[p][filepath.Join("/var/syncthing/vaults", "notes")] = true
	if _, err := p.createVault(context.Background(), "Notes", false); !errors.Is(err, errPathNotEmpty) {
		t.Fatalf("non-empty dir accepted: %v", err)
	}
	if len(f.folders) != 0 {
		t.Fatal("a folder was created despite the refusal")
	}
	if _, err := p.createVault(context.Background(), "Notes", true); err != nil {
		t.Fatalf("adopt refused: %v", err)
	}
}

func TestEnsureDeviceAndShareAreIdempotent(t *testing.T) {
	f, srv := newFakeSyncthing(t, fakeHubID)
	p := newTestProvisioner(t, f, f.client(srv))
	ctx := context.Background()
	for range 2 {
		if err := p.ensureDevice(ctx, fakeDeviceID, "laptop"); err != nil {
			t.Fatal(err)
		}
	}
	if !f.hasDevice(fakeDeviceID) || len(f.devices) != 2 {
		t.Fatalf("device list: %+v", f.devices)
	}
	folder, err := p.createVault(ctx, "Notes", false)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := p.shareVault(ctx, folder.ID, fakeDeviceID); err != nil {
			t.Fatal(err)
		}
	}
	stored := f.folder(folder.ID)
	if len(stored.Devices) != 2 {
		t.Fatalf("share not idempotent: %+v", stored.Devices)
	}
	if stored.MaxConflicts != -1 || stored.Versioning.Type != "staggered" || stored.Path != folder.Path {
		t.Fatalf("sharing must not alter hub defaults or the path: %+v", stored)
	}
	if err := p.shareVault(ctx, "nope", fakeDeviceID); err == nil {
		t.Fatal("sharing an unknown vault must fail")
	}
}

func TestApplyHubDefaultsRespectsExplicitUsageReportingChoice(t *testing.T) {
	f, srv := newFakeSyncthing(t, fakeHubID)
	f.options["urAccepted"] = float64(3)
	id, err := applyHubDefaults(context.Background(), f.client(srv), "My Hub", true)
	if err != nil || id != fakeHubID {
		t.Fatal(id, err)
	}
	if f.options["urAccepted"] != float64(3) {
		t.Fatalf("explicit urAccepted overwritten: %v", f.options["urAccepted"])
	}
	if f.options["startBrowser"] != false {
		t.Fatal("startBrowser not disabled")
	}
	if f.devices[0].Name != "My Hub" {
		t.Fatalf("device not renamed: %+v", f.devices[0])
	}
	if f.folderDefaults["maxConflicts"] != float64(-1) {
		t.Fatalf("folder defaults not applied: %v", f.folderDefaults)
	}
	f.options["urAccepted"] = float64(0)
	if _, err := applyHubDefaults(context.Background(), f.client(srv), "My Hub", false); err != nil {
		t.Fatal(err)
	}
	if f.options["urAccepted"] != float64(-1) {
		t.Fatal("undecided usage reporting must be declined")
	}
}

func TestDetectSyncthingParsesGUIBlock(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.xml")
	xml := `<configuration version="37">
  <device id="` + fakeHubID + `" name="x"><address>dynamic</address></device>
  <gui enabled="true" tls="false"><address>0.0.0.0:8384</address><apikey>secret-key</apikey></gui>
</configuration>`
	if err := writeFile(cfg, xml); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYNCTHING_CONFIG", cfg)
	det, err := detectSyncthing("")
	if err != nil {
		t.Fatal(err)
	}
	if det.APIKey != "secret-key" || det.APIURL != "http://127.0.0.1:8384" {
		t.Fatalf("detected %+v", det)
	}
	det, err = detectSyncthing("http://syncthing:8384")
	if err != nil || det.APIURL != "http://syncthing:8384" {
		t.Fatalf("override ignored: %+v %v", det, err)
	}
}

func TestAutoDetectedTLSGUITrustsSyncthingCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "k" {
			http.Error(w, "nope", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"ping":"pong"}`))
	}))
	defer srv.Close()
	dir := t.TempDir()
	certPath := filepath.Join(dir, "https-cert.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NewSyncthingClient(srv.URL, "k").Ping(context.Background()); err == nil {
		t.Fatal("system roots must not trust Syncthing's self-signed certificate")
	}
	trusted, err := newSyncthingClientTLS(srv.URL, "k", certPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := trusted.Ping(context.Background()); err != nil {
		t.Fatalf("pinned certificate rejected: %v", err)
	}
	if _, err := newSyncthingClientTLS(srv.URL, "k", filepath.Join(dir, "missing.pem")); err == nil {
		t.Fatal("missing certificate file must be an error, not silent insecurity")
	}
	// Detection reports the certificate only for TLS GUIs without an override.
	cfg := filepath.Join(dir, "config.xml")
	if err := writeFile(cfg, `<configuration><gui tls="true"><address>127.0.0.1:8384</address><apikey>k</apikey></gui></configuration>`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYNCTHING_CONFIG", cfg)
	det, err := detectSyncthing("")
	if err != nil || det.CertPath != certPath || det.APIURL != "https://127.0.0.1:8384" {
		t.Fatalf("detected %+v %v", det, err)
	}
	det, _ = detectSyncthing("http://syncthing:8384")
	if det.CertPath != "" {
		t.Fatal("an explicit override must keep normal certificate verification")
	}
}
