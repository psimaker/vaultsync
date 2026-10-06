package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Pairing attempts are recorded before the Hub is asked and updated as they
// go, so `vaultsync status` can say what happened even when an answer was
// lost or the engine is stopped. A record never authorizes anything: a later
// pairing runs every check again.

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

func attemptsPath(lay layout) string { return filepath.Join(lay.Base, "pairing.json") }

func loadAttempts(lay layout) []pairAttempt {
	data, err := os.ReadFile(attemptsPath(lay))
	if err != nil {
		return nil
	}
	var out []pairAttempt
	if json.Unmarshal(data, &out) != nil {
		return nil
	}
	return out
}

// recordAttempt stores the attempt's current state; written only under the
// pair lock. A failure to record never stops the pairing.
func (s *pairSession) recordAttempt(att pairAttempt, state string) {
	att.State = state
	list := loadAttempts(s.env.lay)
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
	if len(list) > attemptsKept {
		list = list[len(list)-attemptsKept:]
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	_ = writeFileAtomic(attemptsPath(s.env.lay), append(data, '\n'), 0o600)
}
