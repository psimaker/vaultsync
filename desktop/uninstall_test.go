//go:build unix

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ownedFixture is VaultSync's own folder as setup leaves it, plus a file
// someone else put there.
func ownedFixture(t *testing.T) (layout, string) {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tmp, "home")
	lay, err := layoutFor("darwin", home, envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"bin/vaultsync", "bin/syncthing", "syncthing/config.xml", "syncthing/index-v2/db.sqlite", "pairing.json", ".agent.json.123"} {
		writeFile(t, filepath.Join(lay.Base, f), "x")
	}
	if err := saveState(lay.State, agentState{GUIPort: 1}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(lay.Logs, "vaultsync.log"), "log")
	foreign := filepath.Join(lay.Base, "notes-someone-kept-here.md")
	writeFile(t, foreign, "keep me")
	return lay, foreign
}

func TestIssue175_RemoveDataRemovesOnlyOwnEntries(t *testing.T) {
	lay, foreign := ownedFixture(t)
	removed, err := removeOwnData(lay, nil, "darwin")
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"bin", "syncthing", "agent.json", "pairing.json", ".agent.json.123"} {
		if _, err := os.Lstat(filepath.Join(lay.Base, gone)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived", gone)
		}
	}
	if data, err := os.ReadFile(foreign); err != nil || string(data) != "keep me" {
		t.Fatalf("a file VaultSync did not create was touched: %v", err)
	}
	if _, err := os.Stat(lay.Base); err != nil {
		t.Fatal("a folder that still holds someone's file was removed")
	}
	if _, err := os.Stat(filepath.Join(lay.Logs, "vaultsync.log")); !errors.Is(err, os.ErrNotExist) {
		t.Error("the log file survived")
	}
	if len(removed) == 0 {
		t.Fatal("nothing reported as removed")
	}
}

// A symlink inside VaultSync's folder is removed as a link; what it points
// to stays.
func TestIssue175_RemoveDataNeverFollowsLinks(t *testing.T) {
	lay, _ := ownedFixture(t)
	vault := filepath.Join(filepath.Dir(lay.Base), "..", "Vault")
	mkVault(t, vault)
	if err := os.RemoveAll(filepath.Join(lay.Base, "syncthing")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(vault, filepath.Join(lay.Base, "syncthing")); err != nil {
		t.Fatal(err)
	}
	if _, err := removeOwnData(lay, nil, "darwin"); err != nil {
		t.Fatal(err)
	}
	assertUntouched(t, vault)
}

func TestIssue175_RemoveDataRefusesWhenOwnershipIsInDoubt(t *testing.T) {
	t.Run("the folder is a link", func(t *testing.T) {
		lay, _ := ownedFixture(t)
		real := lay.Base + "-real"
		if err := os.Rename(lay.Base, real); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, lay.Base); err != nil {
			t.Fatal(err)
		}
		_, err := removeOwnData(lay, nil, "darwin")
		if !strings.Contains(refusalText(err), "not a plain folder") {
			t.Fatalf("got %v", err)
		}
		if _, err := os.Stat(filepath.Join(real, "bin", "syncthing")); err != nil {
			t.Fatal("removal followed the link")
		}
	})
	t.Run("no agent.json", func(t *testing.T) {
		lay, _ := ownedFixture(t)
		if err := os.Remove(lay.State); err != nil {
			t.Fatal(err)
		}
		if _, err := removeOwnData(lay, nil, "darwin"); !strings.Contains(refusalText(err), "does not look like VaultSync's own folder") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a vault overlaps it", func(t *testing.T) {
		lay, _ := ownedFixture(t)
		_, err := removeOwnData(lay, []string{filepath.Dir(lay.Base)}, "darwin")
		if !strings.Contains(refusalText(err), "overlaps the vault") {
			t.Fatalf("got %v", err)
		}
		if _, err := os.Stat(filepath.Join(lay.Base, "bin", "syncthing")); err != nil {
			t.Fatal("files were removed despite the refusal")
		}
	})
	t.Run("the engine still runs", func(t *testing.T) {
		lay, _ := ownedFixture(t)
		unlock, err := lockFile(lay.Lock)
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		if _, err := removeOwnData(lay, nil, "darwin"); !strings.Contains(refusalText(err), "still running") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("nothing installed", func(t *testing.T) {
		lay, err := layoutFor("linux", t.TempDir(), envOf(nil))
		if err != nil {
			t.Fatal(err)
		}
		if removed, err := removeOwnData(lay, nil, "linux"); err != nil || len(removed) != 0 {
			t.Fatalf("got %v %v", removed, err)
		}
		if _, err := os.Stat(lay.Base); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("removing nothing created the folder")
		}
	})
}

func TestIssue175_AgentCopyIsIdempotent(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "bin", "vaultsync")
	changed, err := installAgentCopy(dst)
	if err != nil || !changed {
		t.Fatalf("first copy: %v %v", changed, err)
	}
	st, err := os.Stat(dst)
	if err != nil || st.Mode().Perm()&0o100 == 0 {
		t.Fatalf("copy is not executable: %v %v", st, err)
	}
	if changed, err := installAgentCopy(dst); err != nil || changed {
		t.Fatalf("second copy changed something: %v %v", changed, err)
	}
}
