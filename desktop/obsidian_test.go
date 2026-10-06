package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestIssue175_ObsidianRegistryLocations(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "me")
	got := obsidianRegistries("darwin", home, envOf(nil))
	if len(got) != 1 || got[0] != filepath.Join(home, "Library", "Application Support", "obsidian", "obsidian.json") {
		t.Errorf("macOS: %v", got)
	}

	got = obsidianRegistries("linux", home, envOf(nil))
	want := []string{
		filepath.Join(home, ".config", "obsidian", "obsidian.json"),
		filepath.Join(home, ".var", "app", "md.obsidian.Obsidian", "config", "obsidian", "obsidian.json"),
		filepath.Join(home, "snap", "obsidian", "current", ".config", "obsidian", "obsidian.json"),
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("Linux (native, Flatpak, Snap):\n got %v\nwant %v", got, want)
	}
	xdg := filepath.Join(string(filepath.Separator), "cfg")
	if got := obsidianRegistries("linux", home, envOf(map[string]string{"XDG_CONFIG_HOME": xdg})); got[0] != filepath.Join(xdg, "obsidian", "obsidian.json") {
		t.Errorf("XDG_CONFIG_HOME ignored: %v", got)
	}
	if got := obsidianRegistries("linux", home, envOf(map[string]string{"XDG_CONFIG_HOME": "relative"})); got[0] != want[0] {
		t.Errorf("a relative XDG_CONFIG_HOME must be ignored: %v", got)
	}

	appData := filepath.Join(string(filepath.Separator), "Users", "me", "AppData", "Roaming")
	if got := obsidianRegistries("windows", home, envOf(map[string]string{"APPDATA": appData})); len(got) != 1 || got[0] != filepath.Join(appData, "obsidian", "obsidian.json") {
		t.Errorf("Windows: %v", got)
	}
}

func TestIssue175_ObsidianRegistryParsing(t *testing.T) {
	dir := t.TempDir()
	// The layouts Obsidian writes on each system; Windows paths are kept
	// verbatim (the parser never reinterprets them).
	macOS := filepath.Join(dir, "mac.json")
	writeFile(t, macOS, `{"vaults":{"1a2b3c4d5e6f7a8b":{"path":"/Users/me/Vaults/Notes","ts":1759730000000,"open":true},
		"0f0f0f0f0f0f0f0f":{"path":"/Users/me/Work","ts":1759000000000}},"frame":"hidden"}`)
	windows := filepath.Join(dir, "win.json")
	writeFile(t, windows, `{"vaults":{"aaaaaaaaaaaaaaaa":{"path":"C:\\Users\\me\\Documents\\Notes","ts":1759800000000}},"updateDisabled":true}`)
	empty := filepath.Join(dir, "empty.json")
	writeFile(t, empty, `{"vaults":{"bbbbbbbbbbbbbbbb":{"path":"  ","ts":1}}}`)
	broken := filepath.Join(dir, "broken.json")
	writeFile(t, broken, `{"vaults":`)

	vaults, errs := knownVaults([]string{macOS, filepath.Join(dir, "missing.json"), windows, empty, broken})
	if len(errs) != 1 {
		t.Fatalf("exactly the damaged registry is reported, got %v", errs)
	}
	var paths []string
	for _, v := range vaults {
		paths = append(paths, v.Path)
	}
	want := `C:\Users\me\Documents\Notes|/Users/me/Vaults/Notes|/Users/me/Work`
	if strings.Join(paths, "|") != want {
		t.Fatalf("newest-opened first:\n got %s\nwant %s", strings.Join(paths, "|"), want)
	}
	if !vaults[1].Opened.Equal(time.UnixMilli(1759730000000)) {
		t.Fatalf("ts is milliseconds: %v", vaults[1].Opened)
	}
}

// A vault both the native app and the Flatpak know shows once, with the
// newer opening.
func TestIssue175_ObsidianRegistriesMergeByPath(t *testing.T) {
	home := t.TempDir()
	regs := obsidianRegistries("linux", home, envOf(nil))
	writeFile(t, regs[0], `{"vaults":{"a":{"path":"/home/me/Notes","ts":1000}}}`)
	writeFile(t, regs[1], `{"vaults":{"b":{"path":"/home/me/Notes","ts":5000},"c":{"path":"/home/me/Flat","ts":3000}}}`)
	writeFile(t, regs[2], `{"vaults":{"d":{"path":"/home/me/Snap","ts":4000}}}`)
	vaults, errs := knownVaults(regs)
	if len(errs) != 0 || len(vaults) != 3 {
		t.Fatalf("vaults=%v errs=%v", vaults, errs)
	}
	if vaults[0].Path != "/home/me/Notes" || vaults[0].Opened.UnixMilli() != 5000 || vaults[1].Path != "/home/me/Snap" || vaults[2].Path != "/home/me/Flat" {
		t.Fatalf("merge: %+v", vaults)
	}
}

func TestIssue175_FallbackScanFindsVaultsAndStaysBounded(t *testing.T) {
	home := t.TempDir()
	mk := func(rel ...string) string {
		p := filepath.Join(append([]string{home}, rel...)...)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	notes := filepath.Dir(mk("Vaults", "Notes", ".obsidian"))
	mk("Vaults", "Notes", "Inner", ".obsidian") // inside a vault: never a second vault
	work := filepath.Dir(mk("Work", ".obsidian"))
	mk(".hidden", "Secret", ".obsidian") // hidden folders are skipped
	mk("Library", "Mobile Documents", "Journal", ".obsidian")
	mk("a", "b", "c", "d", "Deep", ".obsidian") // below the depth limit
	cloud := mk("Dropbox")
	mk("Dropbox", "Shared", ".obsidian")

	skip := func(p string) bool { return p == cloud }
	got := scanForVaults([]string{home}, skip, scanLimits{depth: 3, maxDirs: 1000})
	var paths []string
	for _, v := range got {
		if !v.FromScan {
			t.Errorf("%s not marked as found by the scan", v.Path)
		}
		paths = append(paths, v.Path)
	}
	if strings.Join(paths, "|") != notes+"|"+work {
		t.Fatalf("scan found %v, want %s and %s", paths, notes, work)
	}

	// The directory budget ends the walk early instead of reading everything.
	if got := scanForVaults([]string{home}, skip, scanLimits{depth: 3, maxDirs: 1}); len(got) != 0 {
		t.Fatalf("budget of one directory still found %v", got)
	}
}
