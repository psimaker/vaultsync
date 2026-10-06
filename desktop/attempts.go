package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Pairing attempts are recorded before the Hub is asked and updated as they
// go, so `vaultsync status` can say what happened even when an answer was
// lost or the engine is stopped. The record before sending must succeed —
// nothing is asked of the Hub that could not be recorded. A record never
// authorizes anything: a later pairing runs every check again.

type pairAttempt struct {
	Hub     string `json:"hub"`
	HubID   string `json:"hubID"`
	Vault   string `json:"vault"`
	VaultID string `json:"vaultID,omitempty"`
	Path    string `json:"path"`
	// State: sending → shared → accepted, or unknown (the answer was lost)
	// or refused (nothing was set up here).
	State string    `json:"state"`
	At    time.Time `json:"at"`
}

// unresolved attempts are never dropped to make room.
func (a pairAttempt) unresolved() bool {
	return a.State == "sending" || a.State == "unknown" || a.State == "shared"
}

func attemptsPath(lay layout) string { return filepath.Join(lay.Base, "pairing.json") }

// loadAttempts reads the journal; a missing one is empty, a damaged one an
// error (it may hold an unresolved attempt).
func loadAttempts(lay layout) ([]pairAttempt, error) {
	data, err := os.ReadFile(attemptsPath(lay))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []pairAttempt
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("VaultSync's pairing journal %s is damaged (%v) — move it aside, then try again", attemptsPath(lay), err)
	}
	return out, nil
}

// recordAttempt stores the attempt's current state; written only under the
// pair lock.
func (s *pairSession) recordAttempt(att pairAttempt, state string) error {
	att.State = state
	list, err := loadAttempts(s.env.lay)
	if err != nil {
		return err
	}
	replaced := false
	for i := range list {
		if list[i].At.Equal(att.At) && list[i].Vault == att.Vault && list[i].HubID == att.HubID {
			list[i] = att
			replaced = true
		}
	}
	if !replaced {
		list = append(list, att)
	}
	list = trimAttempts(list)
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(attemptsPath(s.env.lay), append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("could not record the pairing in %s (%v); nothing was sent to your Hub", attemptsPath(s.env.lay), err)
	}
	return nil
}

// trimAttempts keeps every unresolved attempt and the newest finished ones,
// up to attemptsKept in all where it can.
func trimAttempts(list []pairAttempt) []pairAttempt {
	if len(list) <= attemptsKept {
		return list
	}
	drop := len(list) - attemptsKept
	out := make([]pairAttempt, 0, len(list))
	for _, a := range list { // oldest first
		if drop > 0 && !a.unresolved() {
			drop--
			continue
		}
		out = append(out, a)
	}
	return out
}
