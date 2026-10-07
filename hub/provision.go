package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/psimaker/vaultsync/hub/join"
	"github.com/psimaker/vaultsync/hub/pairing"
)

// Provisioning is the only place the Hub changes Syncthing's configuration,
// and it does so under the same invariants the iOS app enforces on its side:
//
//   - two folders never overlap on disk (equal or nested paths);
//   - a folder is never created over a directory that already holds content
//     unless the operator explicitly adopts it (`vault adopt`);
//   - an existing folder's path is never changed, and nothing is ever deleted.
//
// New vaults always land under one fixed root (vaultsRoot/<slug>), which makes
// overlap with anything outside that root impossible by construction. Folder
// IDs are random and never derived from user input; the label carries the
// human name, and the iOS app maps a shared folder to <root>/<label>.

const (
	folderIDPrefix     = "vs-"
	maxVaultNameLength = 64
	// Hub folders keep every conflict copy and version: disk on a NAS is the
	// cheap resource, a user's lost edit is not.
	hubMaxConflicts     = -1
	hubRescanIntervalS  = 3600
	hubFSWatcherDelayS  = 10
	hubVersioningMaxAge = 30 * 24 * 60 * 60 // seconds; staggered versioning keeps 30 days
	hubVersioningClean  = 3600
)

var (
	errVaultNameEmpty   = errors.New("vault name is empty")
	errVaultNameTooLong = fmt.Errorf("vault name is longer than %d characters", maxVaultNameLength)
	errVaultNameInvalid = errors.New("vault name has no letters or digits")
	errVaultExists      = errors.New("a vault with this name already exists")
	errPathOverlap      = errors.New("path overlaps an existing vault")
	errPathNotEmpty     = errors.New("directory already holds files — adopt it explicitly with `vaultsync-hub vault adopt`")
)

// provisioner performs guarded configuration changes on the Hub's Syncthing.
type provisioner struct {
	client *SyncthingClient
	// vaultsRoot is the vaults directory as Syncthing sees it (goes into the
	// folder config).
	vaultsRoot string
	// localRoot is the same directory as this process sees it (used for
	// emptiness checks and mkdir). Identical in the shipped compose stack.
	localRoot string
	// dirState reports whether a directory exists and whether it is empty; a
	// package var so tests run without a filesystem.
	dirState func(path string) (exists, empty bool, err error)
	mkdir    func(path string) error
}

func newProvisioner(client *SyncthingClient, vaultsRoot, localRoot string) *provisioner {
	return &provisioner{
		client:     client,
		vaultsRoot: filepath.Clean(vaultsRoot),
		localRoot:  filepath.Clean(localRoot),
		dirState:   dirStateFS,
		mkdir:      func(p string) error { return os.MkdirAll(p, 0o755) },
	}
}

// dirStateFS is the shared emptiness rule (join.DirState): Syncthing's own
// marker and version store do not count as content.
func dirStateFS(path string) (bool, bool, error) { return join.DirState(path) }

// vaultInfo is what pairing and `status` report about one folder.
type vaultInfo = pairing.VaultInfo

// slugify maps a human vault name onto a safe directory name: ASCII letters
// and digits, lower case, dashes between words. Everything else — including
// path separators, dots and unicode punctuation — is dropped, so a name can
// never escape vaultsRoot.
func slugify(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errVaultNameEmpty
	}
	if len([]rune(name)) > maxVaultNameLength {
		return "", errVaultNameTooLong
	}
	var b strings.Builder
	lastDash := true
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case unicode.IsSpace(r), r == '-', r == '_', r == '.':
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		default:
			// Fold the most common umlauts so "Notizen für Ärzte" stays readable.
			switch r {
			case 'ä':
				b.WriteString("ae")
			case 'ö':
				b.WriteString("oe")
			case 'ü':
				b.WriteString("ue")
			case 'ß':
				b.WriteString("ss")
			default:
				continue
			}
			lastDash = false
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "", errVaultNameInvalid
	}
	return slug, nil
}

// pathsOverlap reports whether two cleaned paths are equal or nested — the same
// rule as the iOS app's folderPathOverlapError (decision 001), shared with the
// device side as join.PathsOverlap.
func pathsOverlap(a, b string) bool { return join.PathsOverlap(a, b) }

func newFolderID() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return folderIDPrefix + hex.EncodeToString(b[:]), nil
}

// unknownFiles is what a vault reports while the Hub does not know what it
// holds. The desktop agent and `vaultsync-hub pair` fail closed on it — a
// folder that holds files is never combined with such a vault — and the
// CLIs show no number; the iPhone shows a count only when it is positive.
const unknownFiles int64 = -1

// vaultFiles reports what a vault holds, or unknownFiles (#214). A positive
// count is what Syncthing's index knows locally or globally — files on the
// Hub, or announced by another device and not downloaded yet — and is safe
// either way: a device never combines its files with them. Zero is the one
// count that lets a device make its files the vault's first copy, so it is
// proven, not assumed: the index knows of nothing anywhere, the vault's
// directory is empty on disk right now (the index lags a copy made on the
// Hub itself by a scan), and the folder is running — paused and stopped
// folders answer with empty counters. Decision 047.
func (p *provisioner) vaultFiles(ctx context.Context, f folderConfig) int64 {
	st, err := p.client.DBStatus(ctx, f.ID)
	if err != nil {
		return unknownFiles
	}
	if n := max(st.LocalFiles, st.GlobalFiles); n > 0 {
		return n
	}
	local, ok := p.localPath(f.Path)
	if !ok {
		return unknownFiles
	}
	exists, empty, err := p.dirState(local)
	if err != nil || !exists || !empty {
		return unknownFiles
	}
	switch st.State {
	case "idle", "scanning", "scan-waiting":
		return 0
	}
	return unknownFiles
}

// localPath is a vault's directory as this process sees it: the vaults root
// is one directory, mounted at vaultsRoot for Syncthing and at localRoot
// here. A folder elsewhere cannot be looked at.
func (p *provisioner) localPath(syncthingPath string) (string, bool) {
	rel, err := filepath.Rel(p.vaultsRoot, filepath.Clean(syncthingPath))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.Join(p.localRoot, rel), true
}

// listVaults returns every configured folder with its file count (or
// unknownFiles) and peers.
func (p *provisioner) listVaults(ctx context.Context) ([]vaultInfo, error) {
	folders, err := p.client.Folders(ctx)
	if err != nil {
		return nil, err
	}
	myID, err := p.client.MyID(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]vaultInfo, 0, len(folders))
	for _, f := range folders {
		info := vaultInfo{ID: f.ID, Label: f.Label, Files: p.vaultFiles(ctx, f)}
		if info.Label == "" {
			info.Label = f.ID
		}
		for _, d := range f.Devices {
			if d.DeviceID != myID {
				info.SharedWith = append(info.SharedWith, d.DeviceID)
			}
		}
		out = append(out, info)
	}
	return out, nil
}

// findVaultByLabel returns the folder whose label (or id) matches name,
// case-insensitively, or nil.
func findVaultByLabel(folders []folderConfig, name string) *folderConfig {
	name = strings.TrimSpace(name)
	for i := range folders {
		if strings.EqualFold(folders[i].Label, name) || folders[i].ID == name {
			return &folders[i]
		}
	}
	return nil
}

// createVault adds a new folder under vaultsRoot. adopt allows an existing
// non-empty directory at the computed path to become the vault (operator
// consent, CLI only — never reachable from the pairing endpoint).
func (p *provisioner) createVault(ctx context.Context, name string, adopt bool) (folderConfig, error) {
	slug, err := slugify(name)
	if err != nil {
		return folderConfig{}, err
	}
	folders, err := p.client.Folders(ctx)
	if err != nil {
		return folderConfig{}, err
	}
	if findVaultByLabel(folders, name) != nil {
		return folderConfig{}, errVaultExists
	}
	syncthingPath := filepath.Join(p.vaultsRoot, slug)
	localPath := filepath.Join(p.localRoot, slug)
	for _, f := range folders {
		if pathsOverlap(f.Path, syncthingPath) {
			return folderConfig{}, fmt.Errorf("%w (%s)", errPathOverlap, f.Label)
		}
	}
	exists, empty, err := p.dirState(localPath)
	if err != nil {
		return folderConfig{}, err
	}
	if exists && !empty && !adopt {
		return folderConfig{}, errPathNotEmpty
	}
	if !exists {
		if err := p.mkdir(localPath); err != nil {
			return folderConfig{}, fmt.Errorf("create vault directory: %w", err)
		}
	}
	myID, err := p.client.MyID(ctx)
	if err != nil {
		return folderConfig{}, err
	}
	id, err := newFolderID()
	if err != nil {
		return folderConfig{}, err
	}
	f := folderConfig{
		ID:               id,
		Label:            strings.TrimSpace(name),
		Path:             syncthingPath,
		Type:             "sendreceive",
		Devices:          []folderDevice{{DeviceID: myID}},
		RescanIntervalS:  hubRescanIntervalS,
		FSWatcherEnabled: true,
		FSWatcherDelayS:  hubFSWatcherDelayS,
		IgnorePerms:      true,
		AutoNormalize:    true,
		MaxConflicts:     hubMaxConflicts,
		Versioning:       hubVersioning(),
	}
	if err := p.client.AddFolder(ctx, f); err != nil {
		return folderConfig{}, err
	}
	return f, nil
}

func hubVersioning() versioningConfig {
	return versioningConfig{
		Type: "staggered",
		Params: map[string]string{
			"maxAge":        fmt.Sprint(hubVersioningMaxAge),
			"cleanInterval": fmt.Sprint(hubVersioningClean),
		},
		CleanupIntervalS: hubVersioningClean,
		FSType:           "basic",
	}
}

// ensureDevice adds the device to Syncthing if it is unknown. An existing
// entry is left untouched except for filling in an empty name
// (join.EnsureDevice, which the device side uses for the Hub).
func (p *provisioner) ensureDevice(ctx context.Context, deviceID, name string) error {
	return join.EnsureDevice(ctx, p.client, deviceID, name)
}

// shareVault adds deviceID to the folder's device list. Idempotent; the
// folder's path and every other setting stay exactly as they are — the update
// is a read-modify-write of the raw folder object (see FolderRaw for why a
// PATCH would corrupt hub defaults).
func (p *provisioner) shareVault(ctx context.Context, folderID, deviceID string) error {
	raw, err := p.client.FolderRaw(ctx, folderID)
	if err != nil {
		var apiErr *apiError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return fmt.Errorf("vault %s not found", folderID)
		}
		return err
	}
	devices, _ := raw["devices"].([]any)
	for _, d := range devices {
		if m, ok := d.(map[string]any); ok && m["deviceID"] == deviceID {
			return nil
		}
	}
	raw["devices"] = append(devices, map[string]any{"deviceID": deviceID})
	if path, _ := raw["path"].(string); path == "" {
		return fmt.Errorf("vault %s has no path; refusing to rewrite it", folderID)
	}
	return p.client.PutFolderRaw(ctx, folderID, raw)
}
