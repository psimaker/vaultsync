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
)

// Uninstalling stops the background service and removes its file; vaults
// always stay. --remove-data also removes VaultSync's own directory — but
// only the entries VaultSync puts there, only through a handle anchored at
// that directory (a symlink can never lead the removal elsewhere), only on
// one file system (never into something mounted inside), and only after
// every engine owner has stopped.

// ownedEntries are the names VaultSync creates in its directory.
var ownedEntries = []string{"bin", "syncthing", "agent.json", "pairing.json", "agent.lock", "pair.lock"}

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
	if err := a.svc.uninstall(); err != nil {
		return err
	}
	t.say("✓ Background service removed. VaultSync has stopped syncing on this computer. Your Hub keeps its copy and may still list this computer.")
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
	known, _ := knownVaults(obsidianRegistries(a.goos, a.home, a.getenv))
	for _, v := range known {
		vaults = append(vaults, v.Path)
	}
	removed, err := removeOwnData(a.lay, vaults, a.goos)
	for _, r := range removed {
		t.say("  removed %s", tildePath(a.home, r))
	}
	if err != nil {
		return err
	}
	t.say("✓ VaultSync's own files are removed. Your vault files were not touched.")
	return nil
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
	info, err := os.Lstat(lay.Base)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, refuse("%s is not a plain folder (a link?), so VaultSync does not remove anything in it.", lay.Base)
	}
	if st, err := os.Lstat(lay.State); err != nil || !st.Mode().IsRegular() {
		return nil, refuse("%s does not look like VaultSync's own folder (no agent.json), so VaultSync does not remove anything in it.", lay.Base)
	}
	// No engine owner may run: the background service is gone, and the locks
	// tell whether a `vaultsync run` or `vaultsync pair` still does.
	for _, lock := range []string{lay.Lock, filepath.Join(lay.Base, "pair.lock")} {
		unlock, err := lockFile(lock)
		if err != nil {
			return nil, refuse("VaultSync is still running on this computer (a vaultsync run or vaultsync pair). Stop it, then run vaultsync uninstall --remove-data again.")
		}
		defer unlock()
	}
	if v, ok := onDiskOverlap(lay.Base, vaults, goos); ok {
		return nil, refuse("%s overlaps the vault %s, so VaultSync does not remove anything in it.", lay.Base, v)
	}
	root, err := os.OpenRoot(lay.Base)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	base, err := root.Stat(".")
	if err != nil {
		return nil, err
	}
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
	if err := os.Remove(lay.Base); err == nil {
		removed = append(removed, lay.Base)
	}
	if lay.Logs != "" {
		if logs, err := os.OpenRoot(lay.Logs); err == nil {
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
