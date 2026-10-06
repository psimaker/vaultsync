package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Installing the engine: download the pinned archive, compare its SHA-256
// with the pin compiled into this binary, and only then unpack the one
// syncthing executable and move it into place in a single rename. Any failure
// leaves the previous state untouched — no half-written binary, no temporary
// files.

const (
	maxArchiveBytes = 128 << 20 // the 2.1.6 archives are 11–13 MB
	maxBinaryBytes  = 256 << 20
)

// ErrChecksumMismatch means the downloaded archive is not the pinned one.
var ErrChecksumMismatch = errors.New("the downloaded Syncthing does not match the checksum built into VaultSync")

// ErrNoPin means there is no pinned Syncthing for this operating system and
// processor.
var ErrNoPin = errors.New("no Syncthing build is pinned for this computer")

type installer struct {
	baseURL string // syncthingDownloadBase; a local server in tests
	pin     syncthingPin
	binDir  string // where `syncthing` lands
	client  *http.Client
}

func newInstaller(binDir, goos, goarch string) (installer, error) {
	pin, ok := syncthingPins[goos+"/"+goarch]
	if !ok {
		return installer{}, fmt.Errorf("%w (%s/%s)", ErrNoPin, goos, goarch)
	}
	return installer{baseURL: syncthingDownloadBase, pin: pin, binDir: binDir, client: downloadClient()}, nil
}

// downloadClient follows GitHub's redirect to its download host, never from
// https down to http. The checksum guards the bytes; this guards the request.
func downloadClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
				return errors.New("refusing a redirect from https to " + req.URL.Scheme)
			}
			return nil
		},
	}
}

type installedEngine struct {
	Path   string
	SHA256 string // of the executable, recorded to re-verify it before every start
}

func (in installer) binaryPath() string { return filepath.Join(in.binDir, "syncthing") }

// install downloads, verifies and installs the pinned Syncthing.
func (in installer) install(ctx context.Context) (installedEngine, error) {
	if err := os.MkdirAll(in.binDir, 0o700); err != nil {
		return installedEngine{}, err
	}
	archive, err := os.CreateTemp(in.binDir, ".syncthing-download-*")
	if err != nil {
		return installedEngine{}, err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()

	sum, err := in.download(ctx, archive)
	if err != nil {
		return installedEngine{}, err
	}
	if sum != in.pin.sha256 {
		return installedEngine{}, fmt.Errorf("%w (%s: got %s, pinned %s)", ErrChecksumMismatch, in.pin.archive, sum, in.pin.sha256)
	}
	// Only verified bytes are ever unpacked.
	staged, err := os.CreateTemp(in.binDir, ".syncthing-new-*")
	if err != nil {
		return installedEngine{}, err
	}
	stagedPath := staged.Name()
	defer os.Remove(stagedPath) // a no-op after the rename
	binSum, err := in.extract(archive, staged)
	if cerr := staged.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return installedEngine{}, err
	}
	if err := os.Chmod(stagedPath, 0o755); err != nil {
		return installedEngine{}, err
	}
	if err := os.Rename(stagedPath, in.binaryPath()); err != nil {
		return installedEngine{}, err
	}
	return installedEngine{Path: in.binaryPath(), SHA256: binSum}, nil
}

func (in installer) download(ctx context.Context, dst *os.File) (string, error) {
	url := strings.TrimSuffix(in.baseURL, "/") + "/" + in.pin.archive
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := in.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", in.pin.archive, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: HTTP %d", in.pin.archive, resp.StatusCode)
	}
	if resp.ContentLength > maxArchiveBytes {
		return "", fmt.Errorf("download %s: %d bytes is larger than any pinned archive", in.pin.archive, resp.ContentLength)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(resp.Body, maxArchiveBytes+1))
	if err != nil {
		return "", fmt.Errorf("download %s: %w", in.pin.archive, err)
	}
	if n > maxArchiveBytes {
		return "", fmt.Errorf("download %s: larger than any pinned archive", in.pin.archive)
	}
	if err := dst.Sync(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// extract copies the pinned binary entry — and nothing else — out of the
// verified archive into dst and returns its SHA-256.
func (in installer) extract(archive *os.File, dst *os.File) (string, error) {
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	var src io.Reader
	switch {
	case strings.HasSuffix(in.pin.archive, ".zip"):
		st, err := archive.Stat()
		if err != nil {
			return "", err
		}
		zr, err := zip.NewReader(archive, st.Size())
		if err != nil {
			return "", fmt.Errorf("open %s: %w", in.pin.archive, err)
		}
		for _, f := range zr.File {
			if f.Name != in.pin.binary {
				continue
			}
			if !f.Mode().IsRegular() || f.UncompressedSize64 > maxBinaryBytes {
				return "", fmt.Errorf("%s in %s is not a plain executable", f.Name, in.pin.archive)
			}
			rc, err := f.Open()
			if err != nil {
				return "", err
			}
			defer rc.Close()
			src = rc
			break
		}
	case strings.HasSuffix(in.pin.archive, ".tar.gz"):
		gz, err := gzip.NewReader(archive)
		if err != nil {
			return "", fmt.Errorf("open %s: %w", in.pin.archive, err)
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", fmt.Errorf("read %s: %w", in.pin.archive, err)
			}
			if hdr.Name != in.pin.binary {
				continue
			}
			if hdr.Typeflag != tar.TypeReg || hdr.Size > maxBinaryBytes {
				return "", fmt.Errorf("%s in %s is not a plain executable", hdr.Name, in.pin.archive)
			}
			src = tr
			break
		}
	default:
		return "", fmt.Errorf("unknown archive type %s", in.pin.archive)
	}
	if src == nil {
		return "", fmt.Errorf("%s holds no %s", in.pin.archive, in.pin.binary)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(src, maxBinaryBytes+1))
	if err != nil {
		return "", err
	}
	if n == 0 || n > maxBinaryBytes {
		return "", fmt.Errorf("%s in %s has an implausible size", in.pin.binary, in.pin.archive)
	}
	if err := dst.Sync(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fileSHA256 hashes an installed executable to re-verify it before a start.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, maxBinaryBytes+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
