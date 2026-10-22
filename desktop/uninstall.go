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

// trayRunningRefusal is the uninstall's word for a held tray.lock.
const trayRunningRefusal = "The VaultSync menu-bar app is running on this computer. Quit it, then run vaultsync uninstall --remove-data again."

// trayQuit refuses while the menu-bar app holds its lock; probed before
// the service goes, so the refusal costs nothing. Where VaultSync's
// folder is missing or no plain folder there is nothing to probe — the
// removal says so itself, and no lock file is made in a link's target.
func trayQuit(lay layout) error {
	if _, err := plainFolder(lay.Base); err != nil {
		return nil
	}
	unlock, err := ownLock(lay.TrayLock(), trayRunningRefusal)
	if err != nil {
		return err
	}
	unlock()
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

// holdsOnlyTrayLock: tray.lock is the one entry in the folder.
func holdsOnlyTrayLock(root *os.Root) bool {
	entries, err := fs.ReadDir(root.FS(), ".")
	return err == nil && len(entries) == 1 && entries[0].Name() == "tray.lock"
}

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
	if *removeData {
		// The menu-bar app must have quit before anything goes: its lock
		// is probed first, so the refusal does not cost the service.
		if err := trayQuit(a.lay); err != nil {
			return err
		}
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
	removed, err := removeOwnData(a.lay, vaults, a.goos)
	if link, ok := a.removeCommandLink(); ok {
		removed = append(removed, link)
	}
	for _, r := range removed {
		t.say("  removed %s", tildePath(a.home, r))
	}
	if err != nil {
		return err
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
	if _, err := os.Stat(lay.Base); err != nil {
		return false
	}
	deadline := time.Now().Add(grace)
	for {
		unlock, err := lockFile(lay.Lock)
		if err == nil {
			unlock()
			return false
		}
		if !errors.Is(err, ErrEngineRunning) || time.Now().After(deadline) {
			return errors.Is(err, ErrEngineRunning)
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
func removeOwnData(lay layout, vaults []string, goos string) ([]string, error) {
	if _, err := os.Lstat(lay.Base); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	// A plain folder before anything is made in it: the locks below create
	// their files, which must not land in whatever a link points to.
	if _, err := plainFolder(lay.Base); err != nil {
		return nil, err
	}
	if st, err := os.Lstat(lay.State); err != nil || !st.Mode().IsRegular() {
		return nil, refuse("%s does not look like VaultSync's own folder (no agent.json), so VaultSync does not remove anything in it.", lay.Base)
	}
	// No engine owner may run: the background service is gone, and the locks
	// tell whether a `vaultsync run`, `pair` or `setup` still does.
	for _, lock := range []string{lay.Lock, filepath.Join(lay.Base, "pair.lock"), filepath.Join(lay.Base, "setup.lock")} {
		unlock, err := ownLock(lock, "VaultSync is still running on this computer (a vaultsync run, pair or setup). Stop it, then run vaultsync uninstall --remove-data again.")
		if err != nil {
			return nil, err
		}
		defer unlock()
	}
	// Nor the menu-bar app: a held tray.lock is one that runs. The lock is
	// kept through the removal; an app that takes the lock after agent.json
	// is gone finds nothing to lock and removes the file it made (the
	// tray's TakeLock), so the folder ends up empty.
	unlockTray, err := ownLock(lay.TrayLock(), trayRunningRefusal)
	if err != nil {
		return nil, err
	}
	defer unlockTray()
	root, base, err := openOwnedDir(lay.Base, vaults, goos)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var removed []string
	for _, name := range ownedEntries {
		entry, err := root.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return removed, err
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			// A link is removed as a link; what it points to stays.
			if err := root.Remove(name); err != nil {
				return removed, err
			}
			removed = append(removed, filepath.Join(lay.Base, name))
			continue
		}
		if err := sameFileSystem(root, name, base); err != nil {
			return removed, refuse("%s holds something from another disk (%v), so VaultSync stopped removing its files there.", filepath.Join(lay.Base, name), err)
		}
		if err := root.RemoveAll(name); err != nil {
			return removed, err
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
	// and nothing else gets a moment. Anything else in it keeps the
	// folder, as does a removal that fails for another reason.
	for try := 0; ; try++ {
		if err := os.Remove(lay.Base); err == nil {
			removed = append(removed, lay.Base)
			break
		}
		if !holdsOnlyTrayLock(root) {
			break
		}
		if try == 20 {
			return removed, fmt.Errorf("%s was not removed: the menu-bar app's tray.lock is still in it — quit the app, then remove the folder by hand", lay.Base)
		}
		time.Sleep(removeRetryWait)
	}
	if lay.Logs != "" {
		if _, err := os.Lstat(lay.Logs); err == nil {
			logs, _, err := openOwnedDir(lay.Logs, vaults, goos)
			if err != nil {
				return removed, err
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
	return removed, nil
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
