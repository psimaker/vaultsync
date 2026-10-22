//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/psimaker/vaultsync/desktop/internal/lock"
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
	for _, f := range []string{"bin/vaultsync", "bin/syncthing", "syncthing/config.xml", "syncthing/index-v2/db.sqlite", "pairing.json", "last-error.txt", ".agent.json.123", "agent.sock", "control-socket.log", "control-socket-error.txt", "state.lock", "tray.lock"} {
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
	removed, _, err := removeOwnData(lay, nil, "darwin")
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"bin", "syncthing", "agent.json", "pairing.json", "last-error.txt", ".agent.json.123", "agent.sock", "control-socket.log", "control-socket-error.txt", "state.lock", "tray.lock"} {
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
	if _, _, err := removeOwnData(lay, nil, "darwin"); err != nil {
		t.Fatal(err)
	}
	assertUntouched(t, vault)
}

func TestIssue175_RemoveDataRefusesWhenOwnershipIsInDoubt(t *testing.T) {
	t.Run("the folder is a link: nothing is made through it", func(t *testing.T) {
		// Nothing is made through the link either: no lock file lands in
		// what it points to.
		lay, _ := ownedFixture(t)
		target := lay.Base + "-real"
		if err := os.Rename(lay.Base, target); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, lay.Base); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"agent.lock", "pair.lock", "setup.lock", "tray.lock"} {
			_ = os.Remove(filepath.Join(target, name))
		}
		if _, _, err := removeOwnData(lay, nil, "darwin"); !strings.Contains(refusalText(err), "not a plain folder") {
			t.Fatalf("got %v", err)
		}
		if _, err := os.Stat(filepath.Join(target, "bin", "syncthing")); err != nil {
			t.Fatal("removal followed the link")
		}
		if err := probeOwnLocks(lay, false); !strings.Contains(refusalText(err), "not a plain folder") || !strings.Contains(refusalText(err), "without --remove-data still removes the background service") {
			t.Fatalf("the probe through a link: %v", err)
		}
		// The look for a running engine reads through the link as it is,
		// making nothing: a terminal's vaultsync run there is seen.
		if running, err := engineOwnerRunning(lay, 0); running || err != nil {
			t.Fatalf("no lock file, yet a running engine: %v %v", running, err)
		}
		hold, err := lockFile(filepath.Join(target, "agent.lock"))
		if err != nil {
			t.Fatal(err)
		}
		if running, err := engineOwnerRunning(lay, 0); !running || err != nil {
			t.Fatalf("a run behind the link is not seen: %v %v", running, err)
		}
		hold()
		if err := os.Remove(filepath.Join(target, "agent.lock")); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"agent.lock", "pair.lock", "setup.lock", "tray.lock"} {
			if _, err := os.Lstat(filepath.Join(target, name)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s was made through the link", name)
			}
		}
	})
	t.Run("no agent.json", func(t *testing.T) {
		lay, _ := ownedFixture(t)
		if err := os.Remove(lay.State); err != nil {
			t.Fatal(err)
		}
		// The probe before the service goes names a holder first — a
		// setup in progress holds setup.lock before it writes agent.json,
		// and must not be handed the folder to remove — and only then
		// refuses a folder that is not VaultSync's, with the removal's
		// words and the way out.
		holdSetup, err := lockFile(filepath.Join(lay.Base, "setup.lock"))
		if err != nil {
			t.Fatal(err)
		}
		if err := probeOwnLocks(lay, false); !strings.Contains(refusalText(err), "VaultSync is busy") || strings.Contains(refusalText(err), "remove by hand") {
			t.Fatalf("the probe during a setup: %v", err)
		}
		holdSetup()
		hold, err := lockFile(lay.TrayLock())
		if err != nil {
			t.Fatal(err)
		}
		if err := probeOwnLocks(lay, false); !strings.Contains(refusalText(err), "menu-bar app is running") {
			t.Fatalf("the probe with the app running: %v", err)
		}
		hold()
		if err := probeOwnLocks(lay, false); !strings.Contains(refusalText(err), "does not look like VaultSync's own folder") || !strings.Contains(refusalText(err), "without --remove-data still removes the background service") {
			t.Fatalf("the probe on a folder without agent.json: %v", err)
		}
		// With the service running, a held setup.lock may be its own
		// start: the words name the service and the way out, not "Stop
		// it". A link at the engine's lock is an error now, not after
		// the service is gone.
		holdSetup, err = lockFile(filepath.Join(lay.Base, "setup.lock"))
		if err != nil {
			t.Fatal(err)
		}
		if err := probeOwnLocks(lay, true); !strings.Contains(refusalText(err), "background service") || !strings.Contains(refusalText(err), "vaultsync uninstall;") {
			t.Fatalf("the probe during the service's start: %v", err)
		}
		holdSetup()
		if err := os.Symlink(filepath.Join(lay.Base, "elsewhere"), lay.Lock); err != nil {
			t.Fatal(err)
		}
		if err := probeOwnLocks(lay, true); err == nil || errors.As(err, new(*refusal)) {
			t.Fatalf("a link at the engine's lock while the service runs: %v", err)
		}
		// A service at its start holds setup.lock and is about to take
		// agent.lock: found by setup.lock first, agent.lock is not looked
		// at — the link there would be an error otherwise.
		holdSetup, err = lockFile(filepath.Join(lay.Base, "setup.lock"))
		if err != nil {
			t.Fatal(err)
		}
		if err := probeOwnLocks(lay, true); !strings.Contains(refusalText(err), "VaultSync is busy") {
			t.Fatalf("setup.lock before agent.lock: %v", err)
		}
		holdSetup()
		if err := os.Remove(lay.Lock); err != nil {
			t.Fatal(err)
		}
		if _, _, err := removeOwnData(lay, nil, "darwin"); !strings.Contains(refusalText(err), "does not look like VaultSync's own folder") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a vault overlaps it", func(t *testing.T) {
		lay, _ := ownedFixture(t)
		_, _, err := removeOwnData(lay, []string{filepath.Dir(lay.Base)}, "darwin")
		// A refusal that says nothing was removed leaves nothing behind:
		// the locks are taken after the folder is checked.
		for _, name := range []string{"agent.lock", "pair.lock", "setup.lock"} {
			if _, err := os.Lstat(filepath.Join(lay.Base, name)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s was made before the refusal", name)
			}
		}
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
		if _, _, err := removeOwnData(lay, nil, "darwin"); !strings.Contains(refusalText(err), "still running") {
			t.Fatalf("got %v", err)
		}
		unlock()
	})
	t.Run("the menu-bar app runs", func(t *testing.T) {
		lay, _ := ownedFixture(t)
		unlock, err := lockFile(lay.TrayLock())
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := removeOwnData(lay, nil, "darwin"); !strings.Contains(refusalText(err), "menu-bar app is running") {
			t.Fatalf("got %v", err)
		}
		unlock()
	})
	t.Run("an owner starting mid-removal meets a held lock", func(t *testing.T) {
		// The lock files go last, still held: a vaultsync setup or pair,
		// or the menu-bar app, that starts while the folder is taken apart
		// finds its lock held and stops — nothing of it lands in the
		// half-removed folder. And lock files the removal itself made
		// (only tray.lock was there) are not reported as removed.
		lay, foreign := ownedFixture(t)
		if err := os.Remove(foreign); err != nil {
			t.Fatal(err)
		}
		ownEntriesRemovedHook = func(l layout) {
			if _, err := lockFile(filepath.Join(l.Base, "setup.lock")); !errors.Is(err, ErrEngineRunning) {
				t.Errorf("a setup starting mid-removal: %v", err)
			}
			if _, err := lock.Take(l.TrayLock()); !errors.Is(err, lock.ErrHeld) {
				t.Errorf("the menu-bar app starting mid-removal: %v", err)
			}
		}
		defer func() { ownEntriesRemovedHook = nil }()
		removed, kept, err := removeOwnData(lay, nil, "darwin")
		if err != nil || kept != nil || !slices.Contains(removed, lay.Base) {
			t.Fatalf("got %v %v %v", removed, kept, err)
		}
		for _, name := range []string{"agent.lock", "pair.lock", "setup.lock"} {
			if slices.Contains(removed, filepath.Join(lay.Base, name)) {
				t.Fatalf("%s, made by the removal itself, reported removed", name)
			}
		}
		if !slices.Contains(removed, lay.TrayLock()) {
			t.Fatalf("the tray.lock that was there is not reported: %v", removed)
		}
	})
	t.Run("an interrupted write's leftover of any own file goes", func(t *testing.T) {
		lay, foreign := ownedFixture(t)
		if err := os.Remove(foreign); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{".last-error.txt.123", ".control-socket-error.txt.456", ".agent.json.789"} {
			writeFile(t, filepath.Join(lay.Base, name), "x")
		}
		_, kept, err := removeOwnData(lay, nil, "darwin")
		if err != nil || kept != nil {
			t.Fatalf("got %v %v", kept, err)
		}
		if _, err := os.Stat(lay.Base); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the folder stayed: %v", err)
		}
		// A file that merely starts like one — someone's .syncthing.backup,
		// .bin.old — is not VaultSync's: it stays, and keeps the folder.
		lay, foreign = ownedFixture(t)
		if err := os.Remove(foreign); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{".syncthing.backup", ".bin.old", ".agent.json.notes"} {
			writeFile(t, filepath.Join(lay.Base, name), "keep me")
		}
		_, kept, err = removeOwnData(lay, nil, "darwin")
		if err != nil || kept == nil || kept.failed || !strings.Contains(kept.why, "did not make") {
			t.Fatalf("got %v %v", kept, err)
		}
		for _, name := range []string{".syncthing.backup", ".bin.old", ".agent.json.notes"} {
			if _, err := os.Stat(filepath.Join(lay.Base, name)); err != nil {
				t.Fatalf("%s was removed: %v", name, err)
			}
		}
	})
	t.Run("a lock file is a link", func(t *testing.T) {
		// A link in a lock's place is not followed: nothing outside the
		// folder is opened, locked or made — by the removal or the probe.
		lay, _ := ownedFixture(t)
		outside := filepath.Join(filepath.Dir(lay.Base), "notes.md")
		if err := os.Symlink(outside, filepath.Join(lay.Base, "pair.lock")); err != nil {
			t.Fatal(err)
		}
		if _, _, err := removeOwnData(lay, nil, "darwin"); err == nil || errors.As(err, new(*refusal)) {
			t.Fatalf("got %v", err)
		}
		if err := probeOwnLocks(lay, false); err == nil || errors.As(err, new(*refusal)) {
			t.Fatalf("the probe: %v", err)
		}
		// A link at the engine's lock (the removal above made the file;
		// gone again): whether a run still syncs cannot be told — an
		// error, not "not running".
		if err := os.Remove(lay.Lock); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, lay.Lock); err != nil {
			t.Fatal(err)
		}
		if running, err := engineOwnerRunning(lay, 0); err == nil || running {
			t.Fatalf("a lock that cannot be looked at: %v %v", running, err)
		}
		if _, err := os.Lstat(outside); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("the link's target was made")
		}
		if _, err := os.Stat(filepath.Join(lay.Base, "bin", "syncthing")); err != nil {
			t.Fatal("files were removed despite the error")
		}
	})
	t.Run("an engine lock cannot be taken", func(t *testing.T) {
		// The same for the engine's locks: a directory at pair.lock is
		// not a running vaultsync pair, the error comes through.
		lay, _ := ownedFixture(t)
		if err := os.Mkdir(filepath.Join(lay.Base, "pair.lock"), 0o700); err != nil {
			t.Fatal(err)
		}
		_, _, err := removeOwnData(lay, nil, "darwin")
		if err == nil || errors.As(err, new(*refusal)) || strings.Contains(err.Error(), "still running") {
			t.Fatalf("got %v", err)
		}
		if _, err := os.Stat(filepath.Join(lay.Base, "bin", "syncthing")); err != nil {
			t.Fatal("files were removed despite the error")
		}
	})
	t.Run("the tray lock cannot be taken", func(t *testing.T) {
		// A lock that fails for another reason than a holder is said as
		// that failure, not as the menu-bar app running: a directory in
		// the lock's place cannot be opened as a lock file.
		lay, _ := ownedFixture(t)
		if err := os.Remove(lay.TrayLock()); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(lay.TrayLock(), 0o700); err != nil {
			t.Fatal(err)
		}
		_, _, err := removeOwnData(lay, nil, "darwin")
		if err == nil || errors.As(err, new(*refusal)) || strings.Contains(err.Error(), "menu-bar app") {
			t.Fatalf("got %v", err)
		}
		if _, err := os.Stat(filepath.Join(lay.Base, "bin", "syncthing")); err != nil {
			t.Fatal("files were removed despite the error")
		}
	})
	t.Run("nothing installed", func(t *testing.T) {
		lay, err := layoutFor("linux", t.TempDir(), envOf(nil))
		if err != nil {
			t.Fatal(err)
		}
		if removed, _, err := removeOwnData(lay, nil, "linux"); err != nil || len(removed) != 0 {
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

// --remove-data removes nothing of VaultSync's own when a list of synced
// folders cannot be read: it could not rule out a vault inside.
func TestIssue175_RemoveDataFailsClosedOnUnreadableLists(t *testing.T) {
	cases := map[string]string{
		".local/state/syncthing/config.xml": "<configuration><folder path=",
		".config/obsidian/obsidian.json":    `{"vaults":`,
	}
	for file, content := range cases {
		t.Run(file, func(t *testing.T) {
			tmp, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			home := filepath.Join(tmp, "home")
			lay, err := layoutFor("linux", home, envOf(nil))
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(lay.Base, "bin", "syncthing"), "x")
			if err := saveState(lay.State, agentState{GUIPort: 1}); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(home, file), content)
			run := &fakeRunner{}
			var out strings.Builder
			a := &app{goos: "linux", home: home, getenv: envOf(nil), lay: lay, out: &out,
				svc: service{goos: "linux", home: home, uid: 1000, getenv: envOf(nil), lay: lay, run: run}}
			err = a.uninstall(context.Background(), []string{"--remove-data", "--yes"})
			if !strings.Contains(refusalText(err), "cannot rule out") {
				t.Fatalf("got %v\n%s", err, out.String())
			}
			if _, err := os.Stat(filepath.Join(lay.Base, "bin", "syncthing")); err != nil {
				t.Fatal("VaultSync's files were removed despite the refusal")
			}
		})
	}
}

// setup links the command as ~/.local/bin/vaultsync; once --remove-data has
// removed the copy it points to, the link goes too. A `vaultsync` there that
// setup did not make stays, and so does the link while the copy is kept.
func TestIssue175_RemoveDataRemovesTheCommandLink(t *testing.T) {
	uninstall := func(t *testing.T, args ...string) (layout, string) {
		t.Helper()
		tmp, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		home := filepath.Join(tmp, "home")
		lay, err := layoutFor("linux", home, envOf(nil))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, lay.Agent, "agent")
		if err := saveState(lay.State, agentState{GUIPort: 1}); err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		a := &app{goos: "linux", home: home, getenv: envOf(nil), lay: lay, out: &out,
			svc: service{goos: "linux", home: home, uid: 1000, getenv: envOf(nil), lay: lay, run: &fakeRunner{}}}
		a.installCommand(&term{out: &out})
		link := filepath.Join(home, ".local", "bin", "vaultsync")
		if target, err := os.Readlink(link); err != nil || target != lay.Agent {
			t.Fatalf("setup made no link: %q %v", target, err)
		}
		if err := a.uninstall(context.Background(), append(args, "--yes")); err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
		return lay, link
	}

	t.Run("removed with the data", func(t *testing.T) {
		_, link := uninstall(t, "--remove-data")
		if _, err := os.Lstat(link); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("the command link survived the copy it points to")
		}
	})
	t.Run("kept with the copy", func(t *testing.T) {
		lay, link := uninstall(t)
		if target, err := os.Readlink(link); err != nil || target != lay.Agent {
			t.Fatalf("the link went although the copy stays: %v", err)
		}
	})
	t.Run("someone else's command stays", func(t *testing.T) {
		tmp, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		home := filepath.Join(tmp, "home")
		lay, err := layoutFor("linux", home, envOf(nil))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, lay.Agent, "agent")
		if err := saveState(lay.State, agentState{GUIPort: 1}); err != nil {
			t.Fatal(err)
		}
		other := filepath.Join(home, ".local", "bin", "vaultsync")
		writeFile(t, other, "someone else's vaultsync")
		var out strings.Builder
		a := &app{goos: "linux", home: home, getenv: envOf(nil), lay: lay, out: &out,
			svc: service{goos: "linux", home: home, uid: 1000, getenv: envOf(nil), lay: lay, run: &fakeRunner{}}}
		if err := a.uninstall(context.Background(), []string{"--remove-data", "--yes"}); err != nil {
			t.Fatalf("%v\n%s", err, out.String())
		}
		if data, err := os.ReadFile(other); err != nil || string(data) != "someone else's vaultsync" {
			t.Fatalf("a command setup did not make was touched: %v", err)
		}
	})
	t.Run("only a link to the removed copy goes", func(t *testing.T) {
		tmp, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		home := filepath.Join(tmp, "home")
		lay, err := layoutFor("linux", home, envOf(nil))
		if err != nil {
			t.Fatal(err)
		}
		a := &app{home: home, lay: lay}
		link := commandLink(home)
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		// A link to another program that is gone as well stays.
		if err := os.Symlink(filepath.Join(tmp, "elsewhere", "vaultsync"), link); err != nil {
			t.Fatal(err)
		}
		if _, ok := a.removeCommandLink(); ok {
			t.Fatal("removed a link to another program")
		}
		// VaultSync's link stays while its copy is still there.
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(lay.Agent, link); err != nil {
			t.Fatal(err)
		}
		writeFile(t, lay.Agent, "agent")
		if _, ok := a.removeCommandLink(); ok {
			t.Fatal("removed a link that still works")
		}
		if err := os.Remove(lay.Agent); err != nil {
			t.Fatal(err)
		}
		if got, ok := a.removeCommandLink(); !ok || got != link {
			t.Fatalf("the dangling link stayed: %q %v", got, ok)
		}
	})
}

// The log folder is checked like the rest —
// a link there is never followed into a vault.
func TestIssue175_RemoveDataNeverFollowsTheLogFolder(t *testing.T) {
	lay, _ := ownedFixture(t)
	vault := filepath.Join(filepath.Dir(lay.Base), "..", "Vault")
	mkVault(t, vault)
	writeFile(t, filepath.Join(vault, "vaultsync.log"), "a note that happens to have this name")
	if err := os.RemoveAll(lay.Logs); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(vault, lay.Logs); err != nil {
		t.Fatal(err)
	}
	_, _, err := removeOwnData(lay, nil, "darwin")
	if !strings.Contains(refusalText(err), "not a plain folder") {
		t.Fatalf("got %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(vault, "vaultsync.log")); err != nil || !strings.HasPrefix(string(data), "a note") {
		t.Fatal("a file in the vault behind the link was removed")
	}
}

// A bind mount keeps its file system's device number; the mount table shows
// it, and nothing is removed around it.
func TestIssue175_RemoveDataStopsAtMounts(t *testing.T) {
	lay, _ := ownedFixture(t)
	orig := mountPoints
	defer func() { mountPoints = orig }()
	mountPoints = func() ([]string, error) {
		return []string{"/", filepath.Join(lay.Base, "syncthing", "index-v2")}, nil
	}
	_, _, err := removeOwnData(lay, nil, "darwin")
	if !strings.Contains(refusalText(err), "mounted at or inside") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(lay.Base, "syncthing", "config.xml")); err != nil {
		t.Fatal("files were removed around a mount")
	}
	mountPoints = func() ([]string, error) { return nil, errors.New("no /proc") }
	if _, _, err := removeOwnData(lay, nil, "darwin"); !strings.Contains(refusalText(err), "could not list this computer's mounts") {
		t.Fatalf("an unknown mount table must stop the removal, got %v", err)
	}
}

// VaultSync's folder below a linked folder
// (a custom XDG_STATE_HOME pointing elsewhere) — the mount table names the
// real path, and the removal still stops there.
func TestIssue175_RemoveDataSeesMountsBehindALinkedParent(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	realState := filepath.Join(tmp, "real-state")
	if err := os.MkdirAll(realState, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(tmp, "alias-state")
	if err := os.Symlink(realState, alias); err != nil {
		t.Fatal(err)
	}
	lay, err := layoutFor("linux", filepath.Join(tmp, "home"), envOf(map[string]string{"XDG_STATE_HOME": alias}))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(lay.Base, "syncthing", "data", "note.md"), "mounted vault content")
	if err := saveState(lay.State, agentState{GUIPort: 1}); err != nil {
		t.Fatal(err)
	}
	orig := mountPoints
	defer func() { mountPoints = orig }()
	mountPoints = func() ([]string, error) {
		return []string{"/", filepath.Join(realState, "vaultsync", "syncthing", "data")}, nil
	}
	_, _, err = removeOwnData(lay, nil, "linux")
	if !strings.Contains(refusalText(err), "mounted at or inside") {
		t.Fatalf("got %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(lay.Base, "syncthing", "data", "note.md")); err != nil || string(data) != "mounted vault content" {
		t.Fatal("content behind the mount was removed")
	}
}

// The menu-bar app may make a fresh tray.lock between the entries' removal
// and the folder's (its look at agent.json came first); it removes that
// file itself as soon as it sees agent.json gone, and the uninstall waits
// that moment for the folder. Anything else keeps the folder, at once.
func TestIssue176_RemoveDataWaitsForTheTraysOwnFile(t *testing.T) {
	t.Run("tray.lock made and removed in between", func(t *testing.T) {
		lay, foreign := ownedFixture(t)
		if err := os.Remove(foreign); err != nil {
			t.Fatal(err)
		}
		lockFilesRemovedHook = func(l layout) {
			if l.Base != lay.Base {
				t.Errorf("hook for %q", l.Base)
			}
			writeFile(t, l.TrayLock(), "x")
			go func() {
				time.Sleep(150 * time.Millisecond)
				_ = os.Remove(l.TrayLock())
			}()
		}
		defer func() { lockFilesRemovedHook = nil }()
		removed, _, err := removeOwnData(lay, nil, "darwin")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(lay.Base); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the folder stayed: %v", err)
		}
		found := false
		for _, r := range removed {
			if r == lay.Base {
				found = true
			}
		}
		if !found {
			t.Fatalf("the folder is not reported as removed: %v", removed)
		}
	})
	t.Run("something else in the folder keeps it, without a wait", func(t *testing.T) {
		lay, foreign := ownedFixture(t)
		if err := os.Remove(foreign); err != nil {
			t.Fatal(err)
		}
		ownEntriesRemovedHook = func(l layout) {
			writeFile(t, l.TrayLock(), "x")
			writeFile(t, filepath.Join(l.Base, "notes.md"), "keep me")
		}
		defer func() { ownEntriesRemovedHook = nil }()
		// One wait would be ten seconds: a run well under that took none.
		orig := removeRetryWait
		removeRetryWait = 10 * time.Second
		defer func() { removeRetryWait = orig }()
		start := time.Now()
		_, kept, err := removeOwnData(lay, nil, "darwin")
		if err != nil || kept == nil || kept.failed || !strings.Contains(kept.why, "did not make") {
			t.Fatalf("someone's file keeps the folder, and is said as a note: %v %v", kept, err)
		}
		if took := time.Since(start); took > 3*time.Second {
			t.Fatalf("waited %v for a folder that is not VaultSync's alone", took)
		}
		if _, err := os.Stat(filepath.Join(lay.Base, "notes.md")); err != nil {
			t.Fatalf("someone's file: %v", err)
		}
	})
	t.Run("an empty folder that cannot go is not waited for", func(t *testing.T) {
		// The folder's parent made unwritable: the removal fails for a
		// reason no wait would change, and the folder is empty — no
		// tray.lock to wait for, so no wait.
		if os.Getuid() == 0 {
			t.Skip("root removes through any mode")
		}
		lay, foreign := ownedFixture(t)
		if err := os.Remove(foreign); err != nil {
			t.Fatal(err)
		}
		parent := filepath.Dir(lay.Base)
		ownEntriesRemovedHook = func(l layout) {
			if err := os.Chmod(parent, 0o500); err != nil {
				t.Fatal(err)
			}
		}
		defer func() { ownEntriesRemovedHook = nil }()
		defer func() { _ = os.Chmod(parent, 0o700) }()
		orig := removeRetryWait
		removeRetryWait = 10 * time.Second
		defer func() { removeRetryWait = orig }()
		start := time.Now()
		_, kept, err := removeOwnData(lay, nil, "darwin")
		if err != nil || kept == nil || !kept.failed || !strings.Contains(kept.why, "could not be removed (") {
			t.Fatalf("an empty folder nobody can remove is said, as a failure: %v %v", kept, err)
		}
		if took := time.Since(start); took > 3*time.Second {
			t.Fatalf("waited %v for an empty folder nobody can remove", took)
		}
		if _, err := os.Stat(lay.Base); err != nil {
			t.Fatalf("the folder: %v", err)
		}
	})
	t.Run("emptied between a try and the look: once more, now", func(t *testing.T) {
		// The app removes its file right after a try failed on it: the
		// look finds the folder empty, and the next try — without a wait
		// — takes it.
		lay, foreign := ownedFixture(t)
		if err := os.Remove(foreign); err != nil {
			t.Fatal(err)
		}
		lockFilesRemovedHook = func(l layout) { writeFile(t, l.TrayLock(), "x") }
		removeTryHook = func() { _ = os.Remove(lay.TrayLock()) }
		defer func() { lockFilesRemovedHook, removeTryHook = nil, nil }()
		orig := removeRetryWait
		removeRetryWait = 10 * time.Second
		defer func() { removeRetryWait = orig }()
		start := time.Now()
		removed, _, err := removeOwnData(lay, nil, "darwin")
		if err != nil {
			t.Fatal(err)
		}
		if took := time.Since(start); took > 3*time.Second {
			t.Fatalf("waited %v for a folder emptied in between", took)
		}
		if _, err := os.Stat(lay.Base); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the folder stayed: %v", err)
		}
		if !slices.Contains(removed, lay.Base) {
			t.Fatalf("the folder is not reported as removed: %v", removed)
		}
	})
	t.Run("a tray.lock nobody holds is VaultSync's own leftover, and goes", func(t *testing.T) {
		// An app that made its file and crashed before removing it: the
		// file is unheld, VaultSync's own entry, and goes with the folder
		// — reported removed; the lock file the removal itself made is
		// not (the fixture's own tray.lock is taken away first).
		lay, foreign := ownedFixture(t)
		if err := os.Remove(foreign); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(lay.TrayLock()); err != nil {
			t.Fatal(err)
		}
		lockFilesRemovedHook = func(l layout) { writeFile(t, l.TrayLock(), "x") }
		defer func() { lockFilesRemovedHook = nil }()
		orig := removeRetryWait
		removeRetryWait = time.Millisecond
		defer func() { removeRetryWait = orig }()
		removed, kept, err := removeOwnData(lay, nil, "darwin")
		trayLocks := 0
		for _, r := range removed {
			if r == lay.TrayLock() {
				trayLocks++
			}
		}
		if err != nil || kept != nil || !slices.Contains(removed, lay.Base) || trayLocks != 1 {
			t.Fatalf("got %v %v %v", removed, kept, err)
		}
		if _, err := os.Stat(lay.Base); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the folder stayed: %v", err)
		}
	})
	t.Run("tray.lock that stays is said", func(t *testing.T) {
		// Held by an app through the whole wait (one about to remove it,
		// but not in time): the folder stays, as a failure.
		lay, foreign := ownedFixture(t)
		if err := os.Remove(foreign); err != nil {
			t.Fatal(err)
		}
		var held *lock.Handle
		lockFilesRemovedHook = func(l layout) {
			h, err := lock.Take(l.TrayLock())
			if err != nil {
				t.Fatal(err)
			}
			held = h
		}
		defer func() {
			lockFilesRemovedHook = nil
			if held != nil {
				held.Release()
			}
		}()
		orig := removeRetryWait
		removeRetryWait = time.Millisecond
		defer func() { removeRetryWait = orig }()
		removed, kept, err := removeOwnData(lay, nil, "darwin")
		if err != nil || kept == nil || !kept.failed || !strings.Contains(kept.why, "a tray.lock the menu-bar app holds stayed") {
			t.Fatalf("got %v %v", kept, err)
		}
		for _, r := range removed {
			if r == lay.Base {
				t.Fatal("the folder reported as removed")
			}
		}
		if _, err := os.Stat(lay.TrayLock()); err != nil {
			t.Fatalf("the file: %v", err)
		}
		// The log goes all the same: a second run cannot (agent.json is
		// gone), and the person is told to remove one folder, not two.
		if _, err := os.Stat(filepath.Join(lay.Logs, "vaultsync.log")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the log stayed: %v", err)
		}
	})
	t.Run("tray.lock that stays is said beside a log folder that is refused", func(t *testing.T) {
		// Both are said: the kept folder and the log folder that is a
		// link — neither message swallows the other.
		lay, foreign := ownedFixture(t)
		if err := os.Remove(foreign); err != nil {
			t.Fatal(err)
		}
		real := lay.Logs + "-real"
		if err := os.Rename(lay.Logs, real); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, lay.Logs); err != nil {
			t.Fatal(err)
		}
		var held *lock.Handle
		lockFilesRemovedHook = func(l layout) {
			h, err := lock.Take(l.TrayLock())
			if err != nil {
				t.Fatal(err)
			}
			held = h
		}
		defer func() {
			lockFilesRemovedHook = nil
			if held != nil {
				held.Release()
			}
		}()
		orig := removeRetryWait
		removeRetryWait = time.Millisecond
		defer func() { removeRetryWait = orig }()
		_, kept, err := removeOwnData(lay, nil, "darwin")
		if kept == nil || !strings.Contains(kept.why, "a tray.lock the menu-bar app holds stayed") || !strings.Contains(refusalText(err), "not a plain folder") {
			t.Fatalf("got %v %v", kept, err)
		}
	})
}

// With the menu-bar app running, uninstall --remove-data refuses before
// the background service goes: the refusal costs nothing, and the second
// run after quitting the app is the first one that changes anything.
func TestIssue176_UninstallRefusesBeforeTheServiceGoesWhileTheTrayRuns(t *testing.T) {
	lay, foreign := ownedFixture(t)
	unlock, err := lockFile(lay.TrayLock())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	run := &fakeRunner{}
	a := &app{goos: "linux", home: filepath.Dir(lay.Base), getenv: envOf(nil), lay: lay, out: &bytes.Buffer{},
		svc: service{goos: "linux", home: filepath.Dir(lay.Base), getenv: envOf(nil), lay: lay, run: run}}
	// The service's unit file is the witness that the service stayed.
	unit, err := a.svc.unitPath()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, unit, "[Unit]\n")
	serviceStayed := func(what string) {
		t.Helper()
		if serviceTouched(run.calls) {
			t.Fatalf("%s: the service was touched before the refusal: %v", what, run.calls)
		}
		if _, err := os.Stat(unit); err != nil {
			t.Fatalf("%s: the service's unit file went: %v", what, err)
		}
	}
	err = a.uninstall(context.Background(), []string{"--remove-data", "--yes"})
	if !strings.Contains(refusalText(err), "menu-bar app is running") {
		t.Fatalf("got %v", err)
	}
	serviceStayed("the app running")
	if _, err := os.Stat(filepath.Join(lay.Base, "bin", "syncthing")); err != nil {
		t.Fatal("files were removed despite the refusal")
	}
	// Before any question, too: without --yes and without a terminal the
	// answer would be "pass --yes" — the probe comes first. (The terminal
	// is pointed at nothing, so a go test in a Terminal asks nobody.)
	origTTY := ttyPath
	ttyPath = filepath.Join(t.TempDir(), "no-tty")
	defer func() { ttyPath = origTTY }()
	if err := a.uninstall(context.Background(), []string{"--remove-data"}); !strings.Contains(refusalText(err), "menu-bar app is running") {
		t.Fatalf("asked before the probe: %v", err)
	}
	unlock()

	// A vaultsync run in a terminal — no service is installed here, so a
	// held engine lock is one — is probed before the service goes, too.
	holdRun, err := lockFile(lay.Lock)
	if err != nil {
		t.Fatal(err)
	}
	err = a.uninstall(context.Background(), []string{"--remove-data", "--yes"})
	if !strings.Contains(refusalText(err), "VaultSync is busy") {
		t.Fatalf("a run in a terminal: %v", err)
	}
	serviceStayed("a run in a terminal")
	// Without --remove-data, and with no service installed, the run is
	// found at once, not after a wait meant for a service's own run.
	if err := os.Remove(unit); err != nil {
		t.Fatal(err)
	}
	out := a.out.(*bytes.Buffer)
	out.Reset()
	start := time.Now()
	if err := a.uninstall(context.Background(), []string{"--yes"}); err != nil {
		t.Fatalf("uninstall beside a terminal run: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second || !strings.Contains(out.String(), "still syncs") {
		t.Fatalf("a terminal run beside no service: %v\n%s", took, out.String())
	}
	holdRun()
	// With a service installed — even one the manager does not call
	// running at that instant — its own run may let go of the lock a
	// moment after the removal: that moment is waited for, and a lock
	// released within it is no terminal run.
	writeFile(t, unit, "[Unit]\n")
	origGrace := engineOwnerGrace
	engineOwnerGrace = 2 * time.Second
	defer func() { engineOwnerGrace = origGrace }()
	holdRun, err = lockFile(lay.Lock)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		holdRun()
	}()
	out.Reset()
	if err := a.uninstall(context.Background(), []string{"--yes"}); err != nil {
		t.Fatalf("uninstall beside the service's own run letting go: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "has stopped syncing") || strings.Contains(got, "still syncs") {
		t.Fatalf("the service's own run taken for a terminal run:\n%s", got)
	}
	// The service went with that; it is back for the cases to come.
	writeFile(t, unit, "[Unit]\n")
	run.calls = nil

	// A vaultsync pair or setup in progress is probed the same way — and
	// the probe makes no file: the lock files that were not there before
	// are not there after the refusal.
	for _, name := range []string{"agent.lock", "pair.lock", "tray.lock"} {
		if err := os.Remove(filepath.Join(lay.Base, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	holdSetup, err := lockFile(filepath.Join(lay.Base, "setup.lock"))
	if err != nil {
		t.Fatal(err)
	}
	err = a.uninstall(context.Background(), []string{"--remove-data", "--yes"})
	if !strings.Contains(refusalText(err), "VaultSync is busy") {
		t.Fatalf("a setup in progress: %v", err)
	}
	serviceStayed("a setup in progress")
	holdSetup()
	for _, name := range []string{"agent.lock", "pair.lock", "tray.lock"} {
		if _, err := os.Lstat(filepath.Join(lay.Base, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the probe made %s", name)
		}
	}

	// A link in the folder's place is refused before the service goes,
	// and no lock file lands in its target.
	target := lay.Base + "-real"
	if err := os.Rename(lay.Base, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, lay.Base); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"agent.lock", "pair.lock", "setup.lock", "tray.lock"} {
		_ = os.Remove(filepath.Join(target, name))
	}
	err = a.uninstall(context.Background(), []string{"--remove-data", "--yes"})
	if !strings.Contains(refusalText(err), "not a plain folder") {
		t.Fatalf("a link: %v", err)
	}
	serviceStayed("a link")
	for _, name := range []string{"agent.lock", "pair.lock", "setup.lock", "tray.lock"} {
		if _, err := os.Lstat(filepath.Join(target, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s was made through the link", name)
		}
	}
	if err := os.Remove(lay.Base); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target, lay.Base); err != nil {
		t.Fatal(err)
	}

	// Without --remove-data the app may run on: the service goes, the
	// folder stays, nothing is probed.
	hold, err := lockFile(lay.TrayLock())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.uninstall(context.Background(), []string{"--yes"}); err != nil {
		t.Fatalf("uninstall without --remove-data: %v", err)
	}
	hold()

	// The service went with that plain uninstall.
	if _, err := os.Stat(unit); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the service's unit file stayed: %v", err)
	}
	// A folder kept for someone's file in it is said, beside what went.
	out.Reset()
	if err := a.uninstall(context.Background(), []string{"--remove-data", "--yes"}); err != nil {
		t.Fatalf("uninstall --remove-data: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "  kept ") || !strings.Contains(got, "did not make") || !strings.Contains(got, "its folder stays") {
		t.Fatalf("the kept folder is not said:\n%s", got)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("someone's file: %v", err)
	}

	// A folder kept because its removal failed is an error, so a script
	// sees it — not a note under a check mark.
	if os.Getuid() != 0 {
		if err := os.Remove(foreign); err != nil {
			t.Fatal(err)
		}
		if err := saveState(lay.State, agentState{GUIPort: 1}); err != nil {
			t.Fatal(err)
		}
		parent := filepath.Dir(lay.Base)
		ownEntriesRemovedHook = func(l layout) {
			if err := os.Chmod(parent, 0o500); err != nil {
				t.Fatal(err)
			}
		}
		defer func() { ownEntriesRemovedHook = nil }()
		defer func() { _ = os.Chmod(parent, 0o700) }()
		out.Reset()
		err = a.uninstall(context.Background(), []string{"--remove-data", "--yes"})
		if err == nil || errors.As(err, new(*refusal)) || !strings.Contains(err.Error(), "was not removed") || strings.Contains(out.String(), "✓ VaultSync's own files are removed") {
			t.Fatalf("a failed removal as an error: %v\n%s", err, out.String())
		}
		if err := os.Chmod(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		ownEntriesRemovedHook = nil
	}

	// A kept folder beside a log folder that is refused: the failure is
	// said as a line, so the log's refusal does not hide it.
	lay2, foreign2 := ownedFixture(t)
	if err := os.Remove(foreign2); err != nil {
		t.Fatal(err)
	}
	real := lay2.Logs + "-real"
	if err := os.Rename(lay2.Logs, real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, lay2.Logs); err != nil {
		t.Fatal(err)
	}
	var held *lock.Handle
	lockFilesRemovedHook = func(l layout) {
		h, err := lock.Take(l.TrayLock())
		if err != nil {
			t.Fatal(err)
		}
		held = h
	}
	defer func() {
		lockFilesRemovedHook = nil
		if held != nil {
			held.Release()
		}
	}()
	orig := removeRetryWait
	removeRetryWait = time.Millisecond
	defer func() { removeRetryWait = orig }()
	a2 := &app{goos: "linux", home: filepath.Dir(lay2.Base), getenv: envOf(nil), lay: lay2, out: &bytes.Buffer{},
		svc: service{goos: "linux", home: filepath.Dir(lay2.Base), getenv: envOf(nil), lay: lay2, run: &fakeRunner{}}}
	err = a2.uninstall(context.Background(), []string{"--remove-data", "--yes"})
	if got := a2.out.(*bytes.Buffer).String(); !strings.Contains(refusalText(err), "not a plain folder") || !strings.Contains(got, "  kept ") || !strings.Contains(got, "holds stayed") {
		t.Fatalf("the kept folder beside the log's refusal: %v\n%s", err, got)
	}
}

// serviceTouched: a call that changes the service — reading its state
// (is-active, launchctl print) is none.
func serviceTouched(calls []string) bool {
	for _, c := range calls {
		if !strings.Contains(c, "is-active") && !strings.Contains(c, "launchctl print") {
			return true
		}
	}
	return false
}
