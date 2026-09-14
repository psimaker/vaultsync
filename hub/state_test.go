package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestStateStoreRoundTripAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	store := newStateStore(path)
	st, err := store.load()
	if err != nil || st.Pairing != nil || len(st.Devices) != 0 {
		t.Fatalf("fresh load: %+v, %v", st, err)
	}
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	if _, err := store.update(func(st *hubState) error {
		st.Pairing = &pairingCodeState{Scalar: []byte{1, 2, 3}, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
		st.recordDevice(pairedDevice{DeviceID: fakeDeviceID, Name: "laptop", PairedAt: now, Vaults: []string{"Notes"}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("state file mode %v, want 0600", info.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
	again, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	if again.Pairing == nil || string(again.Pairing.Scalar) != "\x01\x02\x03" || len(again.Devices) != 1 {
		t.Fatalf("round trip lost data: %+v", again)
	}
}

func TestActivePairingExpiryAndLockout(t *testing.T) {
	now := time.Now()
	st := &hubState{}
	if _, err := st.activePairing(now); err == nil {
		t.Fatal("no code must not be active")
	}
	st.Pairing = &pairingCodeState{ExpiresAt: now.Add(time.Minute)}
	if _, err := st.activePairing(now); err != nil {
		t.Fatalf("fresh code inactive: %v", err)
	}
	if _, err := st.activePairing(now.Add(2 * time.Minute)); err == nil {
		t.Fatal("expired code must not be active")
	}
	st.Pairing.Failures = maxCodeFailures
	if _, err := st.activePairing(now); err == nil {
		t.Fatal("locked code must not be active")
	}
}

func TestRecordDeviceMergesVaults(t *testing.T) {
	st := &hubState{}
	st.recordDevice(pairedDevice{DeviceID: fakeDeviceID, Name: "a", Vaults: []string{"X"}})
	st.recordDevice(pairedDevice{DeviceID: fakeDeviceID, Name: "", Vaults: []string{"Y", "X"}})
	if len(st.Devices) != 1 || st.Devices[0].Name != "a" || len(st.Devices[0].Vaults) != 2 {
		t.Fatalf("merge: %+v", st.Devices)
	}
}

func TestStateStoreRejectsUnknownVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"version": 99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newStateStore(path).load(); err == nil {
		t.Fatal("future version must be refused, not silently rewritten")
	}
}
