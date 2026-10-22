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
	"regexp"
	"strings"
	"syscall"
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

// The uninstall's words for a held lock: the engine's owners once the
// service is gone, the menu-bar app, and — before the service is gone —
// the engine's lock, which the service on its way down may still hold.
const (
	ownerRunningRefusal = "VaultSync is still running on this computer (a vaultsync run, pair or setup). Stop it, then run vaultsync uninstall --remove-data again."
	trayRunningRefusal  = "The VaultSync menu-bar app is running on this computer. Quit it, then run vaultsync uninstall --remove-data again."
	busyRefusal         = "VaultSync is busy on this computer — the background service (starting, pairing, on its way down, or one VaultSync could not ask about), or a vaultsync run, pair or setup in a terminal. Wait a moment, stop what runs in a terminal, or remove the service first with vaultsync uninstall; then run vaultsync uninstall --remove-data again."
	notOwnFolderRefusal = "%s does not look like VaultSync's own folder (no agent.json), so VaultSync does not remove anything in it."
	// serviceRemedy follows a refusal the probe raises before the service
	// is touched: the way to the service's removal, and the folder's.
	serviceRemedy = " vaultsync uninstall without --remove-data still removes the background service; the folder is yours to remove by hand."
)

// withServiceRemedy adds the way out to a refusal the probe raises.
func withServiceRemedy(err error) error {
	var r *refusal
	if errors.As(err, &r) {
		return &refusal{msg: r.msg + serviceRemedy}
	}
	return err
}

// errnoOf: the error without its path — for a line that shortens paths
// itself, or names none.
func errnoOf(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// ownLockEntry is one of VaultSync's own locks: where it is, what the
// removal says when it is held (the service is gone by then), what the
// probe before the service goes says (the service may hold it: at its
// start it holds setup.lock, during a socket pairing pair.lock), and
// whether a running service holds it for as long as it runs.
type ownLockEntry struct {
	path, held, probed string
	service            bool
}

// ownLocks is the one list of VaultSync's own locks, for the probe before
// the service goes and for the removal after it. The engine's lock comes
// last: a service at its start holds setup.lock and takes agent.lock
// once, without waiting — the probe's instant hold on agent.lock in that
// moment would make it exit as "already running"; found by setup.lock
// first, agent.lock is never touched then.
func ownLocks(lay layout) []ownLockEntry {
	return []ownLockEntry{
		{filepath.Join(lay.Base, "setup.lock"), ownerRunningRefusal, busyRefusal, false},
		{filepath.Join(lay.Base, "pair.lock"), ownerRunningRefusal, busyRefusal, false},
		{lay.TrayLock(), trayRunningRefusal, trayRunningRefusal, false},
		{lay.Lock, ownerRunningRefusal, busyRefusal, true},
	}
}

// isOwnLock: name is one of the lock files, which go last in the removal.
func isOwnLock(lay layout, name string) bool {
	for _, l := range ownLocks(lay) {
		if filepath.Base(l.path) == name {
			return true
		}
	}
	return false
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
// costs nothing, and making no file (lock.Probe). The locks are looked
// at before the folder is judged VaultSync's own: a setup in progress
// holds setup.lock before it writes agent.json, and is named as such
// rather than the folder handed to the person to remove. A running
// service holds the engine's lock for as long as it runs, so that one's
// held verdict is passed over then; its error (a link, a directory in
// the lock's place) is not. A missing folder is nothing to probe; a link
// in the folder's place, or a folder that is not VaultSync's, is refused
// with the removal's words.
func probeOwnLocks(lay layout, serviceRunning bool) error {
	exists, own, err := ownFolder(lay)
	if err != nil || !exists {
		return withServiceRemedy(err)
	}
	for _, l := range ownLocks(lay) {
		isHeld, err := lock.Probe(l.path)
		if err != nil {
			return err
		}
		if isHeld && !(l.service && serviceRunning) {
			return refuse("%s", l.probed)
		}
	}
	if !own {
		return refuse(notOwnFolderRefusal+serviceRemedy, lay.Base)
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
// before its lock files are; lockFilesRemovedHook once those are gone
// too, before the folder is. A test puts something in the folder there,
// or tries a lock.
var (
	ownEntriesRemovedHook func(lay layout)
	lockFilesRemovedHook  func(lay layout)
)

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
		// or changed: probed first, so the refusal costs nothing. Whether
		// the service runs (a read of the service manager) decides whose
		// the engine's lock is.
		if err := probeOwnLocks(a.lay, a.svc.running()); err != nil {
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
	// Read right before the removal, not minutes earlier at a prompt: a
	// service that is installed, or that the manager calls running, may
	// hold the engine's lock a moment after its removal (one restarting
	// after a crash is "activating" one instant and holding it the next).
	serviceWas := a.svc.installed() || a.svc.running()
	if err := a.svc.uninstall(); err != nil {
		return err
	}
	// The service's own run lets go of the lock a moment after the removal;
	// a terminal run holds it for good, and without a service it is the
	// one holder there can be — known at once.
	grace := time.Duration(0)
	if serviceWas {
		grace = engineOwnerGrace
	}
	switch running, err := engineOwnerRunning(a.lay, grace); {
	case err != nil:
		t.say("✓ Background service removed. VaultSync could not tell whether a vaultsync run started in a terminal still syncs on this computer (its lock file: %v); vaultsync status in that terminal tells.", errnoOf(err))
	case running:
		t.say("✓ Background service removed. A vaultsync run started in a terminal still syncs on this computer — stop it (Ctrl-C in that terminal) to stop syncing.")
	default:
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
	if kept != nil && (!kept.failed || err != nil) {
		// A note; or a failure the error returned below would hide.
		t.say("  kept %s — %s", tildePath(a.home, kept.path), kept.why)
	}
	if err != nil {
		return err
	}
	if kept != nil && kept.failed {
		// The folder could not be removed: an error, so a script sees it.
		return fmt.Errorf("%s was not removed: %s", tildePath(a.home, kept.path), kept.why)
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
func engineOwnerRunning(lay layout, grace time.Duration) (running bool, err error) {
	// A look that makes nothing (lock.Probe): a missing lock file is no
	// running engine, and a folder reached through a link is read as it
	// is, with no lock file made in the link's target. A lock that cannot
	// be looked at is an answer nobody has — said as that, not as "not
	// running".
	deadline := time.Now().Add(grace)
	for {
		held, err := lock.Probe(lay.Lock)
		if err != nil {
			return false, err
		}
		if !held {
			return false, nil
		}
		if time.Now().After(deadline) {
			return true, nil
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
// engineOwnerGrace is how long the service's own run may hold the
// engine's lock after the service's removal before a holder counts as a
// terminal run.
var engineOwnerGrace = 25 * time.Second

// ownTempFile: the name an interrupted writeFileAtomic of one of the
// files VaultSync writes in its folder leaves behind — ".<name>.<digits>"
// (os.CreateTemp), for exactly those names. Anything else that merely
// starts like one (.syncthing.backup) is not VaultSync's to remove.
var ownTempFile = regexp.MustCompile(`^\.(agent\.json|pairing\.json|last-error\.txt|control-socket-error\.txt)\.[0-9]+$`).MatchString

// keptFolder: VaultSync's folder stayed after its own entries went, and
// why — said to the person beside what was removed; failed when the
// reason is a failure of the removal (an error, not a note), as opposed
// to files in it that VaultSync did not make.
type keptFolder struct {
	path, why string
	failed    bool
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
		return nil, nil, refuse(notOwnFolderRefusal, lay.Base)
	}
	// The folder is checked and opened before any lock file is made in it:
	// a refusal that says nothing was removed leaves nothing behind.
	root, base, err := openOwnedDir(lay.Base, vaults, goos)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	// No owner may run: the background service is gone, and the locks tell
	// whether a `vaultsync run`, `pair` or `setup` still does — or the
	// menu-bar app. Each is held through the whole removal and its file
	// goes last, so a run, pair, setup or menu-bar app starting meanwhile
	// meets a held lock and stops; the app's second look then finds
	// agent.json gone and holds nothing. The lock files the taking makes
	// (none was there) are not reported as removed.
	preexisting := map[string]bool{}
	for _, l := range ownLocks(lay) {
		if _, err := os.Lstat(l.path); err == nil {
			preexisting[l.path] = true
		}
		unlock, err := ownLock(l.path, l.held)
		if err != nil {
			return nil, nil, err
		}
		defer unlock()
	}
	var removed []string
	for _, name := range ownedEntries {
		if isOwnLock(lay, name) {
			continue
		}
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
	// Temporary files an interrupted write may have left: writeFileAtomic
	// writes ".<name>.<random>" beside any of the own entries.
	if entries, err := fs.ReadDir(root.FS(), "."); err == nil {
		for _, e := range entries {
			if !e.IsDir() && ownTempFile(e.Name()) {
				_ = root.Remove(e.Name())
			}
		}
	}
	if ownEntriesRemovedHook != nil {
		ownEntriesRemovedHook(lay)
	}
	// The lock files last, still held.
	for _, l := range ownLocks(lay) {
		if err := root.Remove(filepath.Base(l.path)); err == nil && preexisting[l.path] {
			removed = append(removed, l.path)
		}
	}
	if lockFilesRemovedHook != nil {
		lockFilesRemovedHook(lay)
	}
	// The menu-bar app may have made a fresh tray.lock just now (its look
	// at agent.json came before the removal); it removes that file itself
	// the moment it sees agent.json gone — so a folder holding that file
	// and nothing else gets a moment, and one emptied in between gets
	// another try at once. Anything else in it keeps the folder, as does
	// a removal that fails for another reason.
	var kept *keptFolder
	for try := 0; ; try++ {
		err := os.Remove(lay.Base)
		if err == nil {
			removed = append(removed, lay.Base)
			break
		}
		if removeTryHook != nil {
			removeTryHook()
		}
		if !errors.Is(err, syscall.ENOTEMPTY) && !errors.Is(err, syscall.EEXIST) {
			// Not its content: a reason no wait changes, said as it is.
			kept = &keptFolder{lay.Base, fmt.Sprintf("it could not be removed (%v); remove the folder by hand", errnoOf(err)), true}
			break
		}
		entries, readErr := fs.ReadDir(root.FS(), ".")
		if readErr != nil {
			kept = &keptFolder{lay.Base, fmt.Sprintf("it could not be read (%v)", errnoOf(readErr)), true}
			break
		}
		if len(entries) > 1 || (len(entries) == 1 && entries[0].Name() != "tray.lock") {
			// Someone's file: the folder stays, as it always has — said now.
			kept = &keptFolder{lay.Base, "it holds files VaultSync did not make", false}
			break
		}
		if len(entries) == 0 {
			// Emptied between the try and the look (the app removed its
			// file): once more, now — a bounded number of times.
			if try <= 20 {
				continue
			}
			kept = &keptFolder{lay.Base, fmt.Sprintf("it could not be removed (%v); remove the folder by hand", errnoOf(err)), true}
			break
		}
		if try >= 20 {
			// The file is VaultSync's own entry. Unheld — this removal
			// held every lock, so no app is about to remove it — it is a
			// leftover of an app that did not get to, and goes like the
			// rest of the inventory; held, an app is about to remove it.
			trayLock := filepath.Join(lay.Base, "tray.lock")
			held, perr := lock.Probe(trayLock)
			switch {
			case perr != nil:
				kept = &keptFolder{lay.Base, fmt.Sprintf("its tray.lock could not be looked at (%v); remove the folder by hand", errnoOf(perr)), true}
			case held:
				kept = &keptFolder{lay.Base, "a tray.lock the menu-bar app holds stayed; quit the app, then remove the folder by hand", true}
			default:
				// Gone in between (the app removed it after all): the
				// folder is tried as it is.
				if rerr := root.Remove("tray.lock"); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
					kept = &keptFolder{lay.Base, fmt.Sprintf("its tray.lock could not be removed (%v); remove the folder by hand", errnoOf(rerr)), true}
					break
				} else if rerr == nil {
					removed = append(removed, trayLock)
				}
				if rerr := os.Remove(lay.Base); rerr != nil {
					kept = &keptFolder{lay.Base, fmt.Sprintf("it could not be removed (%v); remove the folder by hand", errnoOf(rerr)), true}
					break
				}
				removed = append(removed, lay.Base)
			}
			break
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
