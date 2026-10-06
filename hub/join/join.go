// Package join is the device side of joining a VaultSync Hub after pairing:
// choosing the one Hub a code goes to, making the Hub a known device, and
// accepting the Hub's share into a local directory under the merge guard.
//
// The vaultsync-hub `pair` command and the vaultsync desktop agent both use
// it, so a device-side guard exists exactly once. The Hub's own guards
// (provisioning, pairing sessions) stay in the vaultsync-hub command; the two
// path rules they share with the device side — PathsOverlap (decision 001)
// and DirState (decision 007) — live here.
package join

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/psimaker/vaultsync/hub/pairing"
	"github.com/psimaker/vaultsync/hub/syncthing"
)

// Folder settings of a vault accepted on a device: the values the Hub gives
// its own folders (decision 037), so both ends of a vault behave alike.
// TestDeviceFolderSettingsMatchHubDefaults in the vaultsync-hub command keeps
// them in step with the Hub's defaults.
const (
	FolderRescanIntervalS = 3600
	FolderFSWatcherDelayS = 10
	FolderMaxConflicts    = -1
)

// DefaultPendingTimeout bounds the wait for the Hub's share to arrive.
const DefaultPendingTimeout = 90 * time.Second

// PathsOverlap reports whether two cleaned paths are equal or nested — the
// same rule as the iOS app's folderPathOverlapError (decision 001).
func PathsOverlap(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if a == b {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(a, strings.TrimSuffix(b, sep)+sep) || strings.HasPrefix(b, strings.TrimSuffix(a, sep)+sep)
}

// DirState reports whether a directory exists and whether it is empty.
// Syncthing's own marker and version store do not count as content.
func DirState(path string) (exists, empty bool, err error) {
	entries, err := os.ReadDir(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, true, nil
	}
	if err != nil {
		return true, false, err
	}
	for _, e := range entries {
		// Syncthing's own marker does not count as user content.
		if e.Name() == ".stfolder" || e.Name() == ".stversions" {
			continue
		}
		return true, false, nil
	}
	return true, true, nil
}

// EnsureDevice adds the device to Syncthing if it is unknown. An existing
// entry is left untouched except for filling in an empty name. A new entry
// never auto-accepts folders: every share goes through a guarded accept.
func EnsureDevice(ctx context.Context, client *syncthing.Client, deviceID, name string) error {
	devices, err := client.Devices(ctx)
	if err != nil {
		return err
	}
	for _, d := range devices {
		if d.DeviceID == deviceID {
			if d.Name == "" && name != "" {
				return client.PatchDevice(ctx, deviceID, map[string]any{"name": name})
			}
			return nil
		}
	}
	return client.AddDevice(ctx, syncthing.DeviceConfig{
		DeviceID:          deviceID,
		Name:              name,
		Addresses:         []string{"dynamic"},
		Compression:       "metadata",
		Introducer:        false,
		Paused:            false,
		AutoAcceptFolders: false,
	})
}

// --- choosing the Hub -------------------------------------------------------

// ErrNoHub means discovery found no Hub.
var ErrNoHub = errors.New("no Hub answered")

// SeveralHubsError means more than one Hub answered and nobody chose: a code
// is sent only to the Hub the user picked, never tried on each in turn —
// every try is a counted wrong code on someone else's Hub (decision 045).
type SeveralHubsError struct {
	Hubs []pairing.DiscoveredHub
}

func (e *SeveralHubsError) Error() string {
	names := make([]string, 0, len(e.Hubs))
	for _, h := range e.Hubs {
		if h.Name != "" {
			names = append(names, fmt.Sprintf("%s at %s", h.Name, h.Address))
		} else {
			names = append(names, h.Address)
		}
	}
	return fmt.Sprintf("%d Hubs answered (%s) — choose yours with --hub; a code is sent only to the Hub you choose",
		len(e.Hubs), strings.Join(names, ", "))
}

// PickHub returns the one Hub a code may go to: the only answer, or the one
// choose returns when several answered. A nil choose (no terminal to ask on)
// never picks for the user.
func PickHub(hubs []pairing.DiscoveredHub, choose func([]pairing.DiscoveredHub) (int, error)) (pairing.DiscoveredHub, error) {
	switch {
	case len(hubs) == 0:
		return pairing.DiscoveredHub{}, ErrNoHub
	case len(hubs) == 1:
		return hubs[0], nil
	case choose == nil:
		return pairing.DiscoveredHub{}, &SeveralHubsError{Hubs: hubs}
	}
	i, err := choose(hubs)
	if err != nil {
		return pairing.DiscoveredHub{}, err
	}
	if i < 0 || i >= len(hubs) {
		return pairing.DiscoveredHub{}, fmt.Errorf("no Hub number %d", i+1)
	}
	return hubs[i], nil
}

// --- accepting the Hub's share ----------------------------------------------

// Refusal kinds. A refusal leaves everything as it was: no directory is
// created and no folder is added. errors.Is matches them; the message is the
// guard's own text.
var (
	ErrOverlap         = errors.New("the directory overlaps a synced folder")
	ErrMergeRefused    = errors.New("both sides hold files")
	ErrHubCountUnknown = errors.New("the Hub could not confirm that its vault is empty")
)

// ErrShareDidNotArrive means the guards passed but the Hub's share did not
// reach this device in time; nothing was added.
var ErrShareDidNotArrive = errors.New("the Hub's share did not arrive in time — is the Hub reachable from this computer? (Syncthing needs a few seconds to connect; re-run to retry)")

type refusal struct {
	kind error
	msg  string
}

func (r *refusal) Error() string { return r.msg }
func (r *refusal) Unwrap() error { return r.kind }

// Reporter hears what AcceptShare does, so each command can word it its own
// way. TextReporter is the vaultsync-hub `pair` command's wording.
type Reporter interface {
	AlreadyConfigured(path string)
	Waiting()
	WaitDone(err error)
	Added(label, path string)
}

// TextReporter prints the vaultsync-hub `pair` command's progress lines.
type TextReporter struct{ W io.Writer }

func (r TextReporter) AlreadyConfigured(path string) {
	fmt.Fprintf(r.W, "✓ Vault already configured locally at %s\n", path)
}
func (r TextReporter) Waiting() { fmt.Fprint(r.W, "  Waiting for the share to arrive… ") }
func (r TextReporter) WaitDone(err error) {
	if err != nil {
		fmt.Fprintln(r.W)
		return
	}
	fmt.Fprintln(r.W, "ok")
}
func (r TextReporter) Added(label, path string) {
	fmt.Fprintf(r.W, "✓ Vault %q syncs with the Hub at %s\n", label, path)
}

// Local is the device's own Syncthing plus the hooks the guard uses. Nil
// hooks mean the real filesystem and the default wait.
type Local struct {
	Client         *syncthing.Client
	DirState       func(path string) (exists, empty bool, err error)
	Mkdir          func(path string) error
	PendingTimeout time.Duration
	Reporter       Reporter
	// BeforeAdd, when set, runs after the last wait and right before the
	// folder is added: a caller re-checks there whatever may have changed
	// while it waited (the desktop agent repeats all of its checks). An error
	// stops the accept; nothing is added.
	BeforeAdd func(abs string) error
}

func (l Local) dirState(path string) (bool, bool, error) {
	if l.DirState != nil {
		return l.DirState(path)
	}
	return DirState(path)
}

func (l Local) mkdir(path string) error {
	if l.Mkdir != nil {
		return l.Mkdir(path)
	}
	return os.MkdirAll(path, 0o755)
}

func (l Local) reporter() Reporter {
	if l.Reporter != nil {
		return l.Reporter
	}
	return TextReporter{W: io.Discard}
}

// AcceptShare waits for the Hub's share to arrive as a pending folder and
// adds it with the given path. Safety: the path must be empty (or absent)
// unless the vault on the Hub is brand new, in which case existing local
// content becomes the first copy. Two non-empty sides never get merged here.
func AcceptShare(ctx context.Context, local Local, hubID, myID string, v pairing.VaultInfo, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	folders, err := local.Client.Folders(ctx)
	if err != nil {
		return err
	}
	rep := local.reporter()
	for _, f := range folders {
		if f.ID == v.ID {
			rep.AlreadyConfigured(f.Path)
			return nil
		}
		if PathsOverlap(f.Path, abs) {
			return &refusal{ErrOverlap, fmt.Sprintf("%s overlaps the existing Syncthing folder %q — choose another directory", abs, f.Label)}
		}
	}
	exists, empty, err := local.dirState(abs)
	if err != nil {
		return err
	}
	if exists && !empty {
		// Merging two non-empty sides is the one operation VaultSync never
		// performs on its own. An unknown Hub file count (< 0) fails closed.
		if v.Files < 0 {
			return &refusal{ErrHubCountUnknown, fmt.Sprintf("%s already holds files and the Hub could not confirm that its vault is empty — nothing was changed. Use an empty directory, or retry", abs)}
		}
		if v.Files > 0 {
			return &refusal{ErrMergeRefused, fmt.Sprintf("%s already holds files and the Hub's vault is not empty — nothing was changed. Move one of them aside; VaultSync never merges two vaults on its own", abs)}
		}
	}
	if !exists {
		if err := local.mkdir(abs); err != nil {
			return err
		}
	}
	timeout := local.PendingTimeout
	if timeout <= 0 {
		timeout = DefaultPendingTimeout
	}
	rep.Waiting()
	err = WaitForPendingFolder(ctx, local.Client, hubID, v.ID, timeout)
	rep.WaitDone(err)
	if err != nil {
		return err
	}
	if local.BeforeAdd != nil {
		if err := local.BeforeAdd(abs); err != nil {
			return err
		}
	}
	f := syncthing.FolderConfig{
		ID:               v.ID,
		Label:            v.Label,
		Path:             abs,
		Type:             "sendreceive",
		Devices:          []syncthing.FolderDevice{{DeviceID: myID}, {DeviceID: hubID}},
		RescanIntervalS:  FolderRescanIntervalS,
		FSWatcherEnabled: true,
		FSWatcherDelayS:  FolderFSWatcherDelayS,
		IgnorePerms:      true,
		AutoNormalize:    true,
		MaxConflicts:     FolderMaxConflicts,
	}
	if err := local.Client.AddFolder(ctx, f); err != nil {
		return err
	}
	rep.Added(v.Label, abs)
	return nil
}

// WaitForPendingFolder polls until fromDevice offers folderID, or the timeout
// passes (ErrShareDidNotArrive).
func WaitForPendingFolder(ctx context.Context, client *syncthing.Client, fromDevice, folderID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		pending, err := client.PendingFolders(ctx)
		if err == nil {
			if offers, ok := pending[folderID]; ok {
				for _, o := range offers {
					if o == fromDevice {
						return nil
					}
				}
			}
		}
		if time.Now().After(deadline) {
			return ErrShareDidNotArrive
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
