package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIssue175_OnDiskOverlap(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) string {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	work := mk("Work")
	notes := mk("Vaults/Notes")
	other := mk("Vaults/Other")
	link := filepath.Join(root, "NotesLink")
	if err := os.Symlink(notes, link); err != nil {
		t.Fatal(err)
	}
	configured := []string{work, notes}

	cases := []struct {
		name      string
		candidate string
		goos      string
		want      string
	}{
		{"same folder", notes, "linux", notes},
		{"inside a synced folder", filepath.Join(work, "Sub", "New"), "linux", work},
		{"contains a synced folder", filepath.Join(root, "Vaults"), "linux", notes},
		{"a symlink to a synced folder", link, "linux", notes},
		{"a new folder below that symlink", filepath.Join(link, "New"), "linux", notes},
		{"another spelling on a case-insensitive system", filepath.Join(root, "vaults", "NOTES"), "darwin", notes},
		{"a sibling", other, "linux", ""},
		{"a sibling with a common prefix", filepath.Join(root, "Vaults", "Notes2"), "linux", ""},
	}
	for _, c := range cases {
		got, ok := onDiskOverlap(c.candidate, configured, c.goos)
		if c.want == "" {
			if ok {
				t.Errorf("%s: %s overlaps %s", c.name, c.candidate, got)
			}
			continue
		}
		if !ok || got != c.want {
			t.Errorf("%s: %s → %q (%v), want %s", c.name, c.candidate, got, ok, c.want)
		}
	}
}

// When the file system itself folds case (macOS, Windows), a second spelling
// is the same directory — os.SameFile sees it even where strings differ.
func TestIssue175_OnDiskOverlapUsesFileIdentity(t *testing.T) {
	root := t.TempDir()
	notes := filepath.Join(root, "Notes")
	if err := os.Mkdir(notes, 0o755); err != nil {
		t.Fatal(err)
	}
	upper := filepath.Join(root, "NOTES")
	if _, err := os.Stat(upper); err != nil {
		t.Skip("this file system is case-sensitive")
	}
	if !sameOrNested(filepath.Join(upper, "New", "Deeper"), notes) {
		t.Fatal("a folder below another spelling of a synced folder must count as inside it")
	}
}

func TestIssue175_UserSyncthingIsReadOnlyAndFailsClosed(t *testing.T) {
	home := t.TempDir()
	env := envOf(nil)
	if _, ok := findUserSyncthing("linux", home, env); ok {
		t.Fatal("no config, no Syncthing")
	}
	cfg := filepath.Join(home, ".local", "state", "syncthing", "config.xml")
	writeFile(t, cfg, `<configuration version="52">
    <folder id="abcd-1234" label="Sync" path="~/Sync" type="sendreceive"></folder>
    <folder id="efgh-5678" label="Notes" path="/srv/notes" type="sendreceive"></folder>
    <gui><apikey>not-for-vaultsync</apikey></gui>
</configuration>`)
	before, _ := os.Stat(cfg)
	us, ok := findUserSyncthing("linux", home, env)
	if !ok || us.Unreadable != nil || us.ConfigPath != cfg {
		t.Fatalf("found=%v %+v", ok, us)
	}
	if len(us.Folders) != 2 || us.Folders[0] != filepath.Join(home, "Sync") || us.Folders[1] != "/srv/notes" {
		t.Fatalf("folders (with ~ expanded): %v", us.Folders)
	}
	after, _ := os.Stat(cfg)
	if !after.ModTime().Equal(before.ModTime()) || after.Mode() != before.Mode() {
		t.Fatal("the user's Syncthing config was touched")
	}

	// A config that cannot be parsed still means "a Syncthing is here", and
	// the agent cannot rule out an overlap.
	writeFile(t, cfg, "<configuration><folder path=")
	us, ok = findUserSyncthing("linux", home, env)
	if !ok || us.Unreadable == nil {
		t.Fatalf("an unparsable config must be reported: found=%v %+v", ok, us)
	}

	mac := t.TempDir()
	writeFile(t, filepath.Join(mac, "Library", "Application Support", "Syncthing", "config.xml"), `<configuration></configuration>`)
	if us, ok := findUserSyncthing("darwin", mac, env); !ok || us.Unreadable != nil || len(us.Folders) != 0 {
		t.Fatalf("macOS: found=%v %+v", ok, us)
	}
}

func TestIssue175_LayoutFollowsTheSystem(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "Users", "me")
	mac, err := layoutFor("darwin", home, envOf(nil))
	if err != nil || mac.Base != filepath.Join(home, "Library", "Application Support", "VaultSync") ||
		mac.Logs != filepath.Join(home, "Library", "Logs", "VaultSync") || mac.Agent != filepath.Join(mac.Base, "bin", "vaultsync") ||
		mac.Syncthing != filepath.Join(mac.Base, "bin", "syncthing") || mac.Home != filepath.Join(mac.Base, "syncthing") {
		t.Fatalf("macOS layout: %+v %v", mac, err)
	}
	linux, err := layoutFor("linux", home, envOf(nil))
	if err != nil || linux.Base != filepath.Join(home, ".local", "state", "vaultsync") || linux.Logs != "" {
		t.Fatalf("Linux layout: %+v %v", linux, err)
	}
	state := filepath.Join(string(filepath.Separator), "state")
	if l, _ := layoutFor("linux", home, envOf(map[string]string{"XDG_STATE_HOME": state})); l.Base != filepath.Join(state, "vaultsync") {
		t.Fatalf("XDG_STATE_HOME: %s", l.Base)
	}
	if l, _ := layoutFor("linux", home, envOf(map[string]string{"XDG_STATE_HOME": "rel"})); l.Base != linux.Base {
		t.Fatalf("a relative XDG_STATE_HOME must be ignored: %s", l.Base)
	}
	if _, err := layoutFor("windows", home, envOf(nil)); err == nil {
		t.Fatal("Windows comes later")
	}
	if _, err := layoutFor("linux", "relative/home", envOf(nil)); err == nil {
		t.Fatal("a relative home must be refused")
	}
}

func TestIssue175_AgentStateIsPrivateAndAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "VaultSync", "agent.json")
	st, err := loadState(path)
	if err != nil || st.Version != agentStateVersion || st.GUIPort != 0 {
		t.Fatalf("missing state: %+v %v", st, err)
	}
	st.GUIPort = 41234
	st.Syncthing.Version = syncthingVersion
	if err := saveState(path, st); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("agent.json mode: %v %v", info.Mode(), err)
	}
	back, err := loadState(path)
	if err != nil || back.GUIPort != 41234 || back.Syncthing.Version != syncthingVersion {
		t.Fatalf("round trip: %+v %v", back, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temporary files left: %v", entries)
	}
	writeFile(t, path, `{"version": 99}`)
	if _, err := loadState(path); err == nil {
		t.Fatal("a newer state format must not be read as this one")
	}
}

// Codex review of #212, round 2: a Syncthing config that cannot be reached
// because a folder above it is closed is not "no Syncthing".
func TestIssue175_UserSyncthingBehindAClosedFolderFailsClosed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads any folder")
	}
	home := t.TempDir()
	cfg := filepath.Join(home, ".local", "state", "syncthing", "config.xml")
	writeFile(t, cfg, `<configuration></configuration>`)
	closed := filepath.Join(home, ".local", "state")
	if err := os.Chmod(closed, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(closed, 0o755)
	us, ok := findUserSyncthing("linux", home, envOf(nil))
	if !ok || us.Unreadable == nil {
		t.Fatalf("found=%v %+v", ok, us)
	}
}
