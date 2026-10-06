package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// fakeBinary stands in for the syncthing executable inside an archive.
var fakeBinary = []byte("#!/bin/sh\necho syncthing v2.1.6\n")

// realTarGz builds a genuine .tar.gz with the layout of a Syncthing release:
// a versioned top folder with the binary and its companion files.
func realTarGz(t *testing.T, top string, binary []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name string, mode int64, data []byte, typ byte) {
		hdr := &tar.Header{Name: name, Mode: mode, Size: int64(len(data)), Typeflag: typ}
		if typ == tar.TypeDir {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write(data); err != nil {
				t.Fatal(err)
			}
		}
	}
	add(top+"/", 0o755, nil, tar.TypeDir)
	add(top+"/README.txt", 0o644, []byte("Syncthing\n"), tar.TypeReg)
	add(top+"/etc/", 0o755, nil, tar.TypeDir)
	add(top+"/etc/linux-systemd/user/syncthing.service", 0o644, []byte("[Unit]\n"), tar.TypeReg)
	if binary != nil {
		add(top+"/syncthing", 0o755, binary, tar.TypeReg)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func realZip(t *testing.T, top string, binary []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(name string, data []byte, mode os.FileMode) {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	write(top+"/README.txt", []byte("Syncthing\n"), 0o644)
	if binary != nil {
		write(top+"/syncthing", binary, 0o755)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// serveArchive serves exactly one archive under its release name.
func serveArchive(t *testing.T, name string, data []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+name {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// dirEntries lists a directory so a test can prove nothing was left behind.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func testInstaller(t *testing.T, base, archive, sha, binary string) installer {
	return installer{
		baseURL: base,
		pin:     syncthingPin{archive: archive, sha256: sha, binary: binary},
		binDir:  filepath.Join(t.TempDir(), "bin"),
		client:  downloadClient(),
	}
}

func TestIssue175_ChecksumMismatchInstallsNothing(t *testing.T) {
	cases := []struct {
		name    string
		archive string
		build   func(*testing.T, string, []byte) []byte
	}{
		{"linux tar.gz", "syncthing-linux-amd64-v2.1.6.tar.gz", realTarGz},
		{"macOS zip", "syncthing-macos-arm64-v2.1.6.zip", realZip},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			top := strings.TrimSuffix(strings.TrimSuffix(c.archive, ".tar.gz"), ".zip")
			data := c.build(t, top, fakeBinary)
			srv := serveArchive(t, c.archive, data)
			wrong := strings.Repeat("0", 64)
			in := testInstaller(t, srv.URL, c.archive, wrong, top+"/syncthing")

			// A real, well-formed archive whose checksum does not match the
			// pin is refused before anything is unpacked.
			_, err := in.install(context.Background())
			if !errors.Is(err, ErrChecksumMismatch) {
				t.Fatalf("expected a checksum refusal, got %v", err)
			}
			if names := dirEntries(t, in.binDir); len(names) != 0 {
				t.Fatalf("a refused download left files behind: %v", names)
			}

			// The same archive with its true checksum installs.
			in.pin.sha256 = sum(data)
			got, err := in.install(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			installed, err := os.ReadFile(got.Path)
			if err != nil || !bytes.Equal(installed, fakeBinary) || got.SHA256 != sum(fakeBinary) {
				t.Fatalf("installed %q (%v), sha %s", installed, err, got.SHA256)
			}
			if st, _ := os.Stat(got.Path); st.Mode().Perm()&0o100 == 0 {
				t.Fatalf("installed engine is not executable: %v", st.Mode())
			}
			if names := dirEntries(t, in.binDir); len(names) != 1 || names[0] != "syncthing" {
				t.Fatalf("install left temporary files: %v", names)
			}

			// A later refused download leaves the installed engine untouched.
			in.pin.sha256 = wrong
			if _, err := in.install(context.Background()); !errors.Is(err, ErrChecksumMismatch) {
				t.Fatalf("expected a checksum refusal, got %v", err)
			}
			if again, _ := os.ReadFile(got.Path); !bytes.Equal(again, fakeBinary) {
				t.Fatal("a refused download changed the installed engine")
			}
		})
	}
}

// The real pinned archive for this platform, when the E2E job provides it:
// a wrong checksum is refused, the pinned one installs a working Syncthing.
func TestIssue175_RealArchiveChecksumAbort(t *testing.T) {
	path := os.Getenv("VAULTSYNC_DESKTOP_SYNCTHING_ARCHIVE")
	if path == "" {
		t.Skip("set VAULTSYNC_DESKTOP_SYNCTHING_ARCHIVE to the pinned Syncthing archive for this platform")
	}
	pin, err := newInstaller(t.TempDir(), runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != pin.pin.archive {
		t.Fatalf("archive %s is not the pinned %s", filepath.Base(path), pin.pin.archive)
	}
	srv := serveArchive(t, pin.pin.archive, data)
	in := testInstaller(t, srv.URL, pin.pin.archive, strings.Repeat("f", 64), pin.pin.binary)
	if _, err := in.install(context.Background()); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("expected a checksum refusal, got %v", err)
	}
	if names := dirEntries(t, in.binDir); len(names) != 0 {
		t.Fatalf("a refused download left files behind: %v", names)
	}
	in.pin = pin.pin // the pin compiled into this binary
	if _, err := in.install(context.Background()); err != nil {
		t.Fatalf("the pinned archive does not match the pinned checksum: %v", err)
	}
}

func TestIssue175_InstallRefusesWhatIsNotTheEngine(t *testing.T) {
	top := "syncthing-linux-amd64-v2.1.6"
	t.Run("archive without the binary", func(t *testing.T) {
		data := realTarGz(t, top, nil)
		srv := serveArchive(t, top+".tar.gz", data)
		in := testInstaller(t, srv.URL, top+".tar.gz", sum(data), top+"/syncthing")
		if _, err := in.install(context.Background()); err == nil || !strings.Contains(err.Error(), "holds no") {
			t.Fatalf("got %v", err)
		}
		if names := dirEntries(t, in.binDir); len(names) != 0 {
			t.Fatalf("left files behind: %v", names)
		}
	})
	t.Run("HTTP error", func(t *testing.T) {
		srv := serveArchive(t, "other", nil)
		in := testInstaller(t, srv.URL, top+".tar.gz", strings.Repeat("0", 64), top+"/syncthing")
		if _, err := in.install(context.Background()); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("redirect from https to http", func(t *testing.T) {
		plain := serveArchive(t, top+".tar.gz", []byte("x"))
		tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, plain.URL+r.URL.Path, http.StatusFound)
		}))
		defer tlsSrv.Close()
		in := testInstaller(t, tlsSrv.URL, top+".tar.gz", strings.Repeat("0", 64), top+"/syncthing")
		in.client = downloadClient()
		in.client.Transport = tlsSrv.Client().Transport
		if _, err := in.install(context.Background()); err == nil || !strings.Contains(err.Error(), "refusing a redirect") {
			t.Fatalf("got %v", err)
		}
	})
}

var hexRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestIssue175_PinsAreWellFormed(t *testing.T) {
	for _, platform := range []string{"darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64"} {
		p, ok := syncthingPins[platform]
		if !ok {
			t.Errorf("%s has no pinned Syncthing", platform)
			continue
		}
		if !hexRE.MatchString(p.sha256) {
			t.Errorf("%s: checksum %q is not lower-case SHA-256 hex", platform, p.sha256)
		}
		if !strings.Contains(p.archive, syncthingVersion) || !strings.HasSuffix(p.binary, "/syncthing") {
			t.Errorf("%s: %q / %q do not name the pinned version's binary", platform, p.archive, p.binary)
		}
		top := strings.TrimSuffix(strings.TrimSuffix(p.archive, ".tar.gz"), ".zip")
		if p.binary != top+"/syncthing" {
			t.Errorf("%s: binary %q is not inside %q", platform, p.binary, top)
		}
	}
	if _, err := newInstaller(t.TempDir(), "plan9", "386"); !errors.Is(err, ErrNoPin) {
		t.Errorf("an unpinned platform must be refused, got %v", err)
	}
}
