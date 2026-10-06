package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cloudFixture is a fresh home with the given folders and files.
func cloudFixture(t *testing.T, goos string, dirs []string, files map[string]string) cloudEnv {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	for _, d := range append(dirs, "") {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for rel, content := range files {
		writeFile(t, filepath.Join(home, rel), strings.ReplaceAll(content, "$HOME", home))
	}
	return cloudEnv{
		goos: goos, home: home, getenv: envOf(nil),
		volumes: filepath.Join(root, "Volumes"), runtimeDir: filepath.Join(root, "run"),
	}
}

func blocked(t *testing.T, env cloudEnv, rel string) (string, bool) {
	t.Helper()
	r, ok := cloudBlock(filepath.Join(env.home, rel), cloudRoots(env), env.goos)
	return r.Provider, ok
}

func TestIssue175_CloudFoldersAreBlockedOnMacOS(t *testing.T) {
	env := cloudFixture(t, "darwin", []string{
		"Library/Mobile Documents/iCloud~md~obsidian/Documents/Journal",
		"Library/CloudStorage/OneDrive-Personal/Notes",
		"Library/CloudStorage/Dropbox/Notes",
		"Library/CloudStorage/GoogleDrive-me@example.com/My Drive/Notes",
		"Library/CloudStorage/Box-Box/Notes",
		"Dropbox (Personal)/Notes",
		"Nextcloud/Notes",
		"Sync/Elsewhere/Notes",
		"Vaults/Notes",
		"Documents/Notes",
	}, map[string]string{
		// The sandboxed Nextcloud client keeps its settings in its container.
		"Library/Containers/com.nextcloud.desktopclient/Data/Library/Preferences/Nextcloud/nextcloud.cfg": "[Accounts]\n0\\Folders\\1\\localPath=$HOME/Sync/Elsewhere/\n0\\Folders\\1\\virtualFilesMode=off\n",
	})
	for rel, provider := range map[string]string{
		"Library/Mobile Documents/iCloud~md~obsidian/Documents/Journal":  "iCloud Drive",
		"Library/CloudStorage/OneDrive-Personal/Notes":                   "OneDrive",
		"Library/CloudStorage/Dropbox/Notes":                             "Dropbox",
		"Library/CloudStorage/GoogleDrive-me@example.com/My Drive/Notes": "Google Drive",
		"Library/CloudStorage/Box-Box/Notes":                             "a cloud drive",
		"Dropbox (Personal)/Notes":                                       "Dropbox",
		"Nextcloud/Notes":                                                "Nextcloud",
		"Sync/Elsewhere/Notes":                                           "Nextcloud",    // a configured folder, not a known name
		"library/mobile documents/iCloud~md~obsidian/Documents/Journal":  "iCloud Drive", // macOS ignores case
	} {
		got, ok := blocked(t, env, rel)
		if !ok || got != provider {
			t.Errorf("%s: blocked=%v provider=%q, want %q", rel, ok, got, provider)
		}
	}
	for _, rel := range []string{"Vaults/Notes", "Documents/Notes", "Vaults/New Folder"} {
		if p, ok := blocked(t, env, rel); ok {
			t.Errorf("%s is not a cloud folder, but was blocked as %s", rel, p)
		}
	}
	// A vault that contains a cloud folder would sync its placeholders too.
	if _, ok := blocked(t, env, ""); !ok {
		t.Error("the home folder contains cloud folders and must be blocked")
	}
}

// With iCloud's "Desktop & Documents Folders", ~/Documents and ~/Desktop are
// iCloud Drive; iCloud Drive then holds a Documents entry.
func TestIssue175_ICloudDesktopAndDocuments(t *testing.T) {
	env := cloudFixture(t, "darwin", []string{"Documents/Notes", "Desktop/Notes", "Vaults/Notes"}, nil)
	if _, ok := blocked(t, env, "Documents/Notes"); ok {
		t.Fatal("without the iCloud setting ~/Documents is a local folder")
	}
	icloud := filepath.Join(env.home, "Library", "Mobile Documents", "com~apple~CloudDocs")
	if err := os.MkdirAll(icloud, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(env.home, "Documents"), filepath.Join(icloud, "Documents")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"Documents/Notes", "Desktop/Notes"} {
		if p, ok := blocked(t, env, rel); !ok || p != "iCloud Drive" {
			t.Errorf("%s: blocked=%v %q", rel, ok, p)
		}
	}
	if _, ok := blocked(t, env, "Vaults/Notes"); ok {
		t.Error("~/Vaults is not iCloud Drive")
	}
}

func TestIssue175_CloudFoldersAreBlockedOnLinux(t *testing.T) {
	env := cloudFixture(t, "linux", []string{
		"Dropbox/Notes", "OneDrive/Notes", "Nextcloud2/Notes", "Cloud/Business/Notes", "Cloud/One/Notes",
		"Cloud/Flat/Notes", "Vaults/Notes",
	}, map[string]string{
		".dropbox/info.json":      `{"personal":{"path":"$HOME/Dropbox"},"business":{"path":"$HOME/Cloud/Business","is_team":true}}`,
		".config/onedrive/config": "# onedrive client\nsync_dir = \"~/Cloud/One\"\n",
		".var/app/com.nextcloud.desktopclient.nextcloud/config/Nextcloud/nextcloud.cfg": "0\\Folders\\2\\localPath=$HOME/Cloud/Flat/\n",
	})
	if err := os.MkdirAll(filepath.Join(env.runtimeDir, "gvfs", "google-drive:host=example.com,user=me", "Notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, provider := range map[string]string{
		"Dropbox/Notes":        "Dropbox",
		"OneDrive/Notes":       "OneDrive",
		"Nextcloud2/Notes":     "Nextcloud",
		"Cloud/Business/Notes": "Dropbox",
		"Cloud/One/Notes":      "OneDrive",
		"Cloud/Flat/Notes":     "Nextcloud",
	} {
		if got, ok := blocked(t, env, rel); !ok || got != provider {
			t.Errorf("%s: blocked=%v provider=%q, want %q", rel, ok, got, provider)
		}
	}
	gdrive := filepath.Join(env.runtimeDir, "gvfs", "google-drive:host=example.com,user=me", "Notes")
	if r, ok := cloudBlock(gdrive, cloudRoots(env), "linux"); !ok || !strings.Contains(r.Provider, "Google Drive") {
		t.Errorf("GNOME's Google Drive mount: blocked=%v %q", ok, r.Provider)
	}
	if p, ok := blocked(t, env, "Vaults/Notes"); ok {
		t.Errorf("~/Vaults/Notes blocked as %s", p)
	}
	// Linux file systems are case-sensitive: ~/dropbox is not ~/Dropbox.
	if p, ok := blocked(t, env, "dropbox/Notes"); ok {
		t.Errorf("~/dropbox blocked as %s", p)
	}
}

// A symlink into a cloud folder is the cloud folder.
func TestIssue175_CloudBlockFollowsSymlinks(t *testing.T) {
	env := cloudFixture(t, "linux", []string{"Dropbox/Notes"}, nil)
	link := filepath.Join(env.home, "Notes")
	if err := os.Symlink(filepath.Join(env.home, "Dropbox", "Notes"), link); err != nil {
		t.Fatal(err)
	}
	if p, ok := blocked(t, env, "Notes"); !ok || p != "Dropbox" {
		t.Fatalf("symlink into Dropbox: blocked=%v %q", ok, p)
	}
	// A folder that does not exist yet, below the link, lands in Dropbox too.
	if p, ok := blocked(t, env, "Notes/New"); !ok || p != "Dropbox" {
		t.Fatalf("new folder below the link: blocked=%v %q", ok, p)
	}
}

func TestIssue175_CloudFoldersOnWindowsLayout(t *testing.T) {
	root := t.TempDir()
	oneDrive := filepath.Join(root, "OneDrive - Contoso")
	env := cloudEnv{goos: "windows", home: filepath.Join(root, "home"), getenv: envOf(map[string]string{"OneDriveCommercial": oneDrive})}
	if err := os.MkdirAll(env.home, 0o755); err != nil {
		t.Fatal(err)
	}
	if r, ok := cloudBlock(filepath.Join(oneDrive, "Notes"), cloudRoots(env), "windows"); !ok || r.Provider != "OneDrive" {
		t.Fatalf("OneDrive from the environment: blocked=%v %q", ok, r.Provider)
	}
}

// Codex review of #212, major 7: Qt quotes and escapes a nextcloud.cfg value
// with commas, semicolons, quotes and the like; the quoted form must block
// the real folder.
func TestIssue175_NextcloudQuotedPathsAreDecoded(t *testing.T) {
	for raw, want := range map[string]string{
		`/home/me/Nextcloud/`:           `/home/me/Nextcloud/`,
		`"/home/me/Cloud, Work/"`:       `/home/me/Cloud, Work/`,
		`"/home/me/a \"quoted\" name/"`: `/home/me/a "quoted" name/`,
		`"/home/me/back\\slash/"`:       `/home/me/back\slash/`,
		`"/home/me/\x4e\x43/"`:          `/home/me/NC/`,
	} {
		if got := qtINIValue(raw); got != want {
			t.Errorf("qtINIValue(%s) = %q, want %q", raw, got, want)
		}
	}
	env := cloudFixture(t, "linux", []string{"Cloud, Work/Notes", "Cloud/Notes"}, map[string]string{
		".config/Nextcloud/nextcloud.cfg": "[Accounts]\n0\\Folders\\3\\localPath=\"$HOME/Cloud, Work/\"\n0\\Folders\\4\\localPath=@Invalid()\n",
	})
	if p, ok := blocked(t, env, "Cloud, Work/Notes"); !ok || p != "Nextcloud" {
		t.Fatalf("a quoted Nextcloud folder: blocked=%v %q", ok, p)
	}
	if p, ok := blocked(t, env, "Cloud/Notes"); ok {
		t.Fatalf("the quotes' remains matched another folder: %q", p)
	}
}
