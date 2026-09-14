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

const stateVersion = 1

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

// stateStore serialises access to the state file within one process. Across
// processes (`serve` in the container, `code` via docker compose exec) the
// atomic rename keeps every reader consistent; the serve loop re-reads the file
// per pairing attempt, so a code issued while it runs is honoured immediately.
type stateStore struct {
	path string
	mu   sync.Mutex
}

func newStateStore(path string) *stateStore {
	return &stateStore{path: path}
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

func (s *stateStore) save(st *hubState) error {
	st.Version = stateVersion
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
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

// update applies fn under the lock with a fresh load and persists the result.
func (s *stateStore) update(fn func(*hubState) error) (*hubState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
		return nil, errors.New("the pairing code was locked after repeated wrong attempts — run `vaultsync-hub code` for a new one")
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
