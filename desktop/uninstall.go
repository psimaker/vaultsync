package main

import (
	"context"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/psimaker/vaultsync/desktop/internal/lock"
)

// Uninstalling stops the background service and removes its file; vaults
// always stay. --remove-data also removes VaultSync's own directory — but
// only the entries VaultSync puts there, only through a handle anchored at
// that directory (a symlink can never lead the removal elsewhere), only on
// one file system (never into something mounted inside), and only after
// every engine owner has stopped.

// plainFolder: dir is a directory and not a link to one — the one kind of
// place VaultSync removes anything from or makes a lock file in.
func plainFolder(dir string) (os.FileInfo, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, refuse("%s is not a plain folder (a link?), so VaultSync does not remove anything in it.", dir)
	}
	return info, nil
}

// The uninstall's words for a held lock: the engine's owners, the
// menu-bar app.
const (
	ownerRunningRefusal = "VaultSync is still running on this computer (a vaultsync run, pair or setup). Stop it, then run vaultsync uninstall --remove-data again."
	trayRunningRefusal  = "The VaultSync menu-bar app is running on this computer. Quit it, then run vaultsync uninstall --remove-data again."
)

// ownLockEntry is one of VaultSync's own locks: where it is, what the
// uninstall says when it is held, and whether the background service is
// its holder while it runs.
type ownLockEntry struct {
	path, held string
	service    bool
}

// ownLocks is the one list of VaultSync's own locks, for the probe before
// the service goes and for the removal after it.
func ownLocks(lay layout) []ownLockEntry {
	return []ownLockEntry{
		{lay.Lock, ownerRunningRefusal, true},
		{filepath.Join(lay.Base, "pair.lock"), ownerRunningRefusal, false},
		{filepath.Join(lay.Base, "setup.lock"), ownerRunningRefusal, false},
		{lay.TrayLock(), trayRunningRefusal, false},
	}
}

// ownFolder: whether VaultSync's folder is there, and whether it is its
// own — a plain folder with a regular agent.json in it. A link or a file
// in the folder's place is the error. The probe before the service goes
// and the removal after it read this one gate.
func ownFolder(lay layout) (exists, own bool, err error) {
	if _, err := plainFolder(lay.Base); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, false, nil
		}
		return true, false, err
	}
	st, err := os.Lstat(lay.State)
	return true, err == nil && st.Mode().IsRegular(), nil
}

// probeOwnLocks refuses while one of VaultSync's own locks is held —
// probed before the service goes and before any question, so the refusal
// costs nothing, and making no file (lock.Probe). The engine's lock is
// probed only while the service does not run: a running service holds
// it until it is removed; otherwise a holder is a vaultsync run in a
// terminal. A missing folder or one without agent.json is nothing to
// probe — the removal says so itself; a link in the folder's place is
// refused here.
func probeOwnLocks(lay layout, engineToo bool) error {
	if _, own, err := ownFolder(lay); err != nil || !own {
		return err
	}
	for _, l := range ownLocks(lay) {
		if l.service && !engineToo {
			continue
		}
		held, err := lock.Probe(l.path)
		if err != nil {
			return err
		}
		if held {
			return refuse("%s", l.held)
		}
	}
	return nil
}

// ownLock takes one of VaultSync's locks for the removal: held, the
// refusal names who holds it; any other failure to take it is said as
// it is — a directory in the lock's place is not a running VaultSync.
func ownLock(path, held string) (func(), error) {
	unlock, err := lockFile(path)
	if errors.Is(err, ErrEngineRunning) {
		return nil, refuse("%s", held)
	}
	return unlock, err
}

// ownEntriesRemovedHook runs once VaultSync's entries are removed and
// before its folder is; a test puts something in the folder there.
var ownEntriesRemovedHook func(lay layout)

// removeRetryWait is the pause between tries at removing the folder
// while the menu-bar app's tray.lock is the one thing in it.
var removeRetryWait = 50 * time.Millisecond

// removeTryHook runs after a try at removing the folder failed; a test
// takes the menu-bar app's file away there.
var removeTryHook func()

// ownedEntries are the names VaultSync creates in its directory.
var ownedEntries = []string{"bin", "syncthing", "agent.json", "pairing.json", "last-error.txt", "agent.lock", "pair.lock", "setup.lock", "agent.sock", "control-socket.log", "control-socket-error.txt", "state.lock", "tray.lock"}

func (a *app) uninstall(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	removeData := flags.Bool("remove-data", false, "")
	yes := flags.Bool("yes", false, "")
	if err := parseFlags(flags, args); err != nil {
		return err
	}
	t, closeTerm := openTerm(a.out)
	defer closeTerm()
	confirm := func(q string) (bool, error) {
		if *yes {
			return true, nil
		}
		ok, err := t.confirm(q)
		if errors.Is(err, errNoTerminal) {
			return false, refuse("Pass --yes to confirm — there is no terminal to ask on.")
		}
		return ok, err
	}
	vaults := configuredVaultPaths(a.engine())
	if *removeData {
		// Whoever holds one of VaultSync's own locks — a pair or setup in
		// progress, the menu-bar app, a vaultsync run in a terminal while
		// the service does not run — must be gone before anything is asked
		// or changed: probed first, so the refusal costs nothing.
		if err := probeOwnLocks(a.lay, !a.svc.running()); err != nil {
			return err
		}
	}
	t.blank()
	t.say("This stops VaultSync on this computer and removes its background service.")
	if len(vaults) > 0 {
		var shown []string
		for _, v := range vaults {
			shown = append(shown, tildePath(a.home, v))
		}
		t.say("Your vaults stay where they are: %s.", strings.Join(shown, ", "))
	}
	ok, err := confirm("Remove the background service?")
	if err != nil {
		return err
	}
	if !ok {
		t.say("Nothing was changed.")
		return nil
	}
	if err := a.svc.uninstall(); err != nil {
		return err
	}
	if engineOwnerRunning(a.lay, 25*time.Second) {
		t.say("✓ Background service removed. A vaultsync run started in a terminal still syncs on this computer — stop it (Ctrl-C in that terminal) to stop syncing.")
	} else {
		t.say("✓ Background service removed. VaultSync has stopped syncing on this computer. Your Hub keeps its copy and may still list this computer.")
	}
	if !*removeData {
		t.say("The vaultsync command, its sync engine, settings and pairing identity stay in %s, so running the setup again continues where you left off.", tildePath(a.home, a.lay.Base))
		return nil
	}
	t.blank()
	t.say("This also removes VaultSync's settings, its pairing identity and the sync database on this computer. Your vault files stay.")
	t.say("This computer will have to pair again, and folders that hold files will not reconnect by themselves.")
	ok, err = confirm("Remove them?")
	if err != nil {
		return err
	}
	if !ok {
		t.say("Kept %s.", tildePath(a.home, a.lay.Base))
		return nil
	}
	// Every folder anything syncs on this computer must lie outside what is
	// removed — and when a list cannot be read, nothing is removed.
	known, errs := knownVaults(obsidianRegistries(a.goos, a.home, a.getenv))
	if len(errs) > 0 {
		return refuse("VaultSync could not read Obsidian's list of vaults (%v), so it cannot rule out that a vault lies inside its own folder. Nothing was removed besides the background service.", errs[0])
	}
	for _, v := range known {
		vaults = append(vaults, v.Path)
	}
	if us, ok := a.userSyncthing(); ok {
		if us.Unreadable != nil {
			return refuse("VaultSync could not read the settings of the Syncthing on this computer (%s), so it cannot rule out that it syncs VaultSync's own folder. Nothing was removed besides the background service.", tildePath(a.home, us.ConfigPath))
		}
		vaults = append(vaults, us.Folders...)
	}
	removed, kept, err := removeOwnData(a.lay, vaults, a.goos)
	if link, ok := a.removeCommandLink(); ok {
		removed = append(removed, link)
	}
	for _, r := range removed {
		t.say("  removed %s", tildePath(a.home, r))
	}
	if kept != nil {
		t.say("  kept %s — %s", tildePath(a.home, kept.path), kept.why)
	}
	if err != nil {
		return err
	}
	if kept != nil {
		t.say("✓ VaultSync's own files are removed, but its folder stays (see above). Your vault files were not touched.")
		return nil
	}
	t.say("✓ VaultSync's own files are removed. Your vault files were not touched.")
	return nil
}

// removeCommandLink removes the command link setup made, but only while it
// still points at VaultSync's own copy and that copy is gone: a `vaultsync`
// someone else put there, or a link that still works, stays.
func (a *app) removeCommandLink() (string, bool) {
	link := commandLink(a.home)
	if target, err := os.Readlink(link); err != nil || target != a.lay.Agent {
		return "", false
	}
	if _, err := os.Lstat(a.lay.Agent); !errors.Is(err, fs.ErrNotExist) {
		return "", false
	}
	if err := os.Remove(link); err != nil {
		return "", false
	}
	return link, true
}

// engineOwnerRunning reports whether some process still owns the engine once
// the stopped service had time to let go of it.
func engineOwnerRunning(lay layout, grace time.Duration) bool {
	// A look that makes nothing (lock.Probe): a missing lock file is no
	// running engine, and a folder reached through a link is read as it
	// is, with no lock file made in the link's target.
	deadline := time.Now().Add(grace)
	for {
		held, err := lock.Probe(lay.Lock)
		if err != nil || !held {
			return false
		}
		if time.Now().After(deadline) {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// configuredVaultPaths reads the engine's folders from its config file.
func configuredVaultPaths(eng engine) []string {
	f, err := os.Open(eng.configPath())
	if err != nil {
		return nil
	}
	defer f.Close()
	var cfg struct {
		Folders []struct {
			Path string `xml:"path,attr"`
		} `xml:"folder"`
	}
	if xml.NewDecoder(io.LimitReader(f, 16<<20)).Decode(&cfg) != nil {
		return nil
	}
	var out []string
	for _, fo := range cfg.Folders {
		out = append(out, fo.Path)
	}
	return out
}

// removeOwnData removes VaultSync's own entries from lay.Base (and its log
// file), refusing whenever ownership or the boundary is in doubt.
// keptFolder: VaultSync's folder stayed after its own entries went, and
// why — said to the person, beside what was removed.
type keptFolder struct {
	path, why string
}

func removeOwnData(lay layout, vaults []string, goos string) ([]string, *keptFolder, error) {
	// A plain folder before anything is made in it: the locks below create
	// their files, which must not land in whatever a link points to.
	exists, own, err := ownFolder(lay)
	if err != nil {
		return nil, nil, err
	}
	if !exists {
		return nil, nil, nil
	}
	if !own {
		return nil, nil, refuse("%s does not look like VaultSync's own folder (no agent.json), so VaultSync does not remove anything in it.", lay.Base)
	}
	// No owner may run: the background service is gone, and the locks tell
	// whether a `vaultsync run`, `pair` or `setup` still does — or the
	// menu-bar app. Each is held through the removal; an app that takes
	// its lock after agent.json is gone finds nothing to lock and removes
	// the file it made (the tray's TakeLock), so the folder ends up empty.
	for _, l := range ownLocks(lay) {
		unlock, err := ownLock(l.path, l.held)
		if err != nil {
			return nil, nil, err
		}
		defer unlock()
	}
	root, base, err := openOwnedDir(lay.Base, vaults, goos)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	var removed []string
	for _, name := range ownedEntries {
		entry, err := root.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return removed, nil, err
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			// A link is removed as a link; what it points to stays.
			if err := root.Remove(name); err != nil {
				return removed, nil, err
			}
			removed = append(removed, filepath.Join(lay.Base, name))
			continue
		}
		if err := sameFileSystem(root, name, base); err != nil {
			return removed, nil, refuse("%s holds something from another disk (%v), so VaultSync stopped removing its files there.", filepath.Join(lay.Base, name), err)
		}
		if err := root.RemoveAll(name); err != nil {
			return removed, nil, err
		}
		removed = append(removed, filepath.Join(lay.Base, name))
	}
	// Temporary files an interrupted write may have left.
	if entries, err := fs.ReadDir(root.FS(), "."); err == nil {
		for _, e := range entries {
			if !e.IsDir() && (strings.HasPrefix(e.Name(), ".agent.json.") || strings.HasPrefix(e.Name(), ".pairing.json.")) {
				_ = root.Remove(e.Name())
			}
		}
	}
	if ownEntriesRemovedHook != nil {
		ownEntriesRemovedHook(lay)
	}
	// The menu-bar app may have made a fresh tray.lock just now (its look
	// at agent.json came before the removal); it removes that file itself
	// the moment it sees agent.json gone — so a folder holding that file
	// and nothing else gets a moment, and one emptied in between gets
	// another try at once. Anything else in it keeps the folder, as does
	// a removal that fails for another reason.
	var kept *keptFolder
	for try := 0; try <= 20; try++ {
		err := os.Remove(lay.Base)
		if err == nil {
			removed = append(removed, lay.Base)
			break
		}
		if removeTryHook != nil {
			removeTryHook()
		}
		entries, readErr := fs.ReadDir(root.FS(), ".")
		if readErr != nil {
			kept = &keptFolder{lay.Base, fmt.Sprintf("it could not be read (%v)", readErr)}
			break
		}
		if len(entries) > 1 || (len(entries) == 1 && entries[0].Name() != "tray.lock") {
			// Someone's file: the folder stays, as it always has — said now.
			kept = &keptFolder{lay.Base, "it holds files VaultSync did not make"}
			break
		}
		if try == 20 {
			// Nobody holds that file — this removal held every lock — so
			// it is a leftover of an app that did not get to remove it.
			if len(entries) == 0 {
				var pe *os.PathError
				if errors.As(err, &pe) {
					err = pe.Err
				}
				kept = &keptFolder{lay.Base, fmt.Sprintf("it could not be removed (%v); remove the folder by hand", err)}
			} else {
				kept = &keptFolder{lay.Base, "a tray.lock the menu-bar app left in it stayed; remove the folder by hand"}
			}
			break
		}
		if len(entries) == 0 {
			continue
		}
		time.Sleep(removeRetryWait)
	}
	if lay.Logs != "" {
		if _, err := os.Lstat(lay.Logs); err == nil {
			logs, _, err := openOwnedDir(lay.Logs, vaults, goos)
			if err != nil {
				return removed, kept, err
			}
			if logs.Remove("vaultsync.log") == nil {
				removed = append(removed, filepath.Join(lay.Logs, "vaultsync.log"))
			}
			logs.Close()
			if os.Remove(lay.Logs) == nil {
				removed = append(removed, lay.Logs)
			}
		}
	}
	return removed, kept, nil
}

// openOwnedDir opens one of VaultSync's own folders for removal — only when
// it is a plain folder (not a link), no vault overlaps it, nothing is mounted
// inside it, and the folder opened is the one that was checked.
func openOwnedDir(dir string, vaults []string, goos string) (*os.Root, os.FileInfo, error) {
	info, err := plainFolder(dir)
	if err != nil {
		return nil, nil, err
	}
	if v, ok := onDiskOverlap(dir, vaults, goos); ok {
		return nil, nil, refuse("%s overlaps the vault %s, so VaultSync does not remove anything in it.", dir, v)
	}
	mounts, err := mountPoints()
	if err != nil {
		return nil, nil, refuse("VaultSync could not list this computer's mounts (%v), so it cannot rule out a disk mounted inside %s. Nothing was removed there.", err, dir)
	}
	// The mount table names resolved paths: compare with the folder as given
	// and as it resolves (a linked folder above it, such as a custom
	// XDG_STATE_HOME, would hide a mount from the first spelling).
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, nil, err
	}
	for _, m := range mounts {
		m = filepath.Clean(m)
		for _, d := range []string{filepath.Clean(dir), filepath.Clean(resolved)} {
			if m == d || strings.HasPrefix(m, d+string(filepath.Separator)) {
				return nil, nil, refuse("Something is mounted at or inside %s (%s), so VaultSync does not remove anything there.", dir, m)
			}
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(opened, info) {
		root.Close()
		return nil, nil, refuse("%s changed while VaultSync was checking it, so nothing was removed there.", dir)
	}
	return root, opened, nil
}

// sameFileSystem walks name inside root and refuses an entry on another
// device: RemoveAll would otherwise descend into whatever is mounted there.
func sameFileSystem(root *os.Root, name string, base os.FileInfo) error {
	return fs.WalkDir(root.FS(), name, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		info, err := root.Lstat(p)
		if err != nil {
			return err
		}
		if !sameDevice(info, base) {
			return fmt.Errorf("%s is a mount point", p)
		}
		return nil
	})
}
