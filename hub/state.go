package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The Hub keeps a tiny state file next to — not inside — Syncthing's config:
// the active pairing code (as its password scalar, never the code itself), the
// devices it paired, and nothing else. Syncthing's config.xml stays the single
// source of truth for devices and folders; this file only records what the Hub
// did so `status` can explain it.
//
// Writes are atomic (temp file + rename) and 0600: the pairing scalar is a
// short-lived secret.
//
// Two processes write the file: `serve` (a failed attempt's count, a recorded
// pairing) and commands such as `vaultsync-hub code`. Every read-modify-write
// therefore holds one lock that all of them share, from its read to its
// rename (lockStateFile), so no change rests on a stale read and none is lost
// (#490). Readers need no lock: a rename always shows a whole file. A write
// keeps the file's owner, so a command run as root does not shut the server
// out of its own state.

const stateVersion = 1

// maxCodeFailures wrong codes lock the active pairing code (decision 036).
const maxCodeFailures = 5

type pairingCodeState struct {
	Scalar    []byte    `json:"scalar"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Failures  int       `json:"failures"`
}

type pairedDevice struct {
	DeviceID string    `json:"deviceID"`
	Name     string    `json:"name"`
	PairedAt time.Time `json:"pairedAt"`
	Vaults   []string  `json:"vaults"`
}

type hubState struct {
	Version int               `json:"version"`
	Pairing *pairingCodeState `json:"pairing,omitempty"`
	Devices []pairedDevice    `json:"devices"`
}

// errCodeLocked: the active code took maxCodeFailures wrong attempts.
var errCodeLocked = errors.New("the pairing code was locked after repeated wrong attempts — run `vaultsync-hub code` for a new one")

// stateStore serialises access to the state file within one process. Across
// processes (`serve` in the container, `code` via docker compose exec) the
// atomic rename keeps every reader consistent; the serve loop re-reads the file
// per pairing attempt, so a code issued while it runs is honoured immediately.
type stateStore struct {
	path string
	mu   sync.Mutex
	// owner decides who a write leaves the file to (stateFileOwner) and
	// chown hands the new file to them; package seams for tests.
	owner func(statePath string) (fileOwner, error)
	chown func(f *os.File, uid, gid int) error
}

func newStateStore(path string) *stateStore {
	return &stateStore{
		path:  path,
		owner: stateFileOwner,
		chown: func(f *os.File, uid, gid int) error { return f.Chown(uid, gid) },
	}
}

// fileOwner is who a state write leaves the file to.
type fileOwner struct {
	uid, gid int
	change   bool // hand the new file to uid:gid
}

// ownerOf is the owner of a file or folder, as decideStateOwner reads it.
type ownerOf struct{ uid, gid int }

// decideStateOwner keeps the state file's owner across a write (decision
// 097). The server reads and writes the file as the service's user; a command
// run as root would otherwise leave a root-owned 0600 file the server cannot
// read, and every pairing would fail. Root keeps the existing file's owner, or
// gives a new file its folder's owner; any other user whose write would change
// the owner is refused. file is nil when there is no state file yet, dir when
// its folder's owner is not known.
func decideStateOwner(statePath string, euid int, file, dir *ownerOf) (fileOwner, error) {
	switch {
	case file != nil && file.uid == euid:
		return fileOwner{}, nil
	case file != nil && euid == 0:
		return fileOwner{uid: file.uid, gid: file.gid, change: true}, nil
	case file != nil:
		return fileOwner{}, fmt.Errorf("the Hub's state file %s belongs to another user (uid %d): run vaultsync-hub as that user — `docker compose exec hub …` does; nothing was changed", statePath, file.uid)
	case euid != 0 || dir == nil || dir.uid == 0:
		return fileOwner{}, nil
	default:
		return fileOwner{uid: dir.uid, gid: dir.gid, change: true}, nil
	}
}

func (s *stateStore) load() (*hubState, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return &hubState{Version: stateVersion}, nil
	}
	if err != nil {
		return nil, err
	}
	var st hubState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("state file %s is not valid JSON: %w", s.path, err)
	}
	if st.Version != stateVersion {
		return nil, fmt.Errorf("state file %s has version %d, this hub understands %d", s.path, st.Version, stateVersion)
	}
	return &st, nil
}

// save writes the whole file: a temp file beside it, then a rename. Callers
// hold the state lock (update).
func (s *stateStore) save(st *hubState) error {
	st.Version = stateVersion
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	decide, chown := s.owner, s.chown
	if decide == nil {
		decide = stateFileOwner
	}
	if chown == nil {
		chown = func(f *os.File, uid, gid int) error { return f.Chown(uid, gid) }
	}
	owner, err := decide(s.path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".state-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if owner.change {
		if err := chown(tmp, owner.uid, owner.gid); err != nil {
			tmp.Close()
			cleanup()
			return fmt.Errorf("keep the owner of %s: %w", s.path, err)
		}
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// update applies fn with a fresh load and persists the result, holding the
// in-process mutex and the lock every process shares (lockStateFile) from the
// read to the rename. A write that cannot get the lock fails; it never goes
// on without it.
func (s *stateStore) update(fn func(*hubState) error) (*hubState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := lockStateFile(s.path)
	if err != nil {
		return nil, err
	}
	defer unlock()
	st, err := s.load()
	if err != nil {
		return nil, err
	}
	if err := fn(st); err != nil {
		return nil, err
	}
	if err := s.save(st); err != nil {
		return nil, err
	}
	return st, nil
}

// activePairing returns the pairing code state if one exists and is still
// usable (not expired, not locked out by failures).
func (st *hubState) activePairing(now time.Time) (*pairingCodeState, error) {
	if st.Pairing == nil {
		return nil, errors.New("no pairing code has been issued — run `vaultsync-hub code`")
	}
	if now.After(st.Pairing.ExpiresAt) {
		return nil, errors.New("the pairing code has expired — run `vaultsync-hub code` for a new one")
	}
	if st.Pairing.Failures >= maxCodeFailures {
		return nil, errCodeLocked
	}
	return st.Pairing, nil
}

func (st *hubState) recordDevice(d pairedDevice) {
	for i := range st.Devices {
		if st.Devices[i].DeviceID == d.DeviceID {
			if d.Name != "" {
				st.Devices[i].Name = d.Name
			}
			st.Devices[i].Vaults = mergeStrings(st.Devices[i].Vaults, d.Vaults)
			return
		}
	}
	st.Devices = append(st.Devices, d)
}

func mergeStrings(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string{}, a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
