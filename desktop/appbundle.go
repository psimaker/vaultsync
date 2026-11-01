package main

import (
	"encoding/xml"
	"io/fs"
	"os"
	"path/filepath"
)

// VaultSync for Mac (decisions 052, 053): when this program runs from the
// app bundle installed in Applications, the background service runs it from
// there and the menu-bar app beside it starts at login.

const (
	appBundleName = "VaultSync for Mac.app"
	appBundleID   = "eu.vaultsync.desktop"
)

// macApp is VaultSync for Mac around this program.
type macApp struct {
	Path  string // …/VaultSync for Mac.app
	Agent string // Contents/MacOS/vaultsync — this program
	Tray  string // Contents/MacOS/vaultsync-tray — the menu-bar app
}

// appEnv is what findInstalledApp looks at; tests replace it.
type appEnv struct {
	goos       string
	home       string
	executable func() (string, error)
	resolve    func(string) (string, error) // filepath.EvalSymlinks
	readFile   func(string) ([]byte, error)
	lstat      func(string) (fs.FileInfo, error)
}

func liveAppEnv(goos, home string) appEnv {
	return appEnv{goos: goos, home: home, executable: os.Executable, resolve: filepath.EvalSymlinks, readFile: os.ReadFile, lstat: os.Lstat}
}

// findInstalledApp reports the app bundle this program runs from, when that
// bundle is installed: on a Mac, this program is Contents/MacOS/vaultsync of
// a bundle named VaultSync for Mac.app directly in /Applications or
// ~/Applications, whose Info.plist names eu.vaultsync.desktop, with the
// menu-bar app as a plain executable file beside it. Anywhere else — a bare
// download, a bundle in Downloads or on a disk image, or one macOS runs
// from a randomized, read-only place (App Translocation) — the service
// keeps running VaultSync's own copy: a LaunchAgent pointing there would
// lose its program by the next restart.
func findInstalledApp(env appEnv) (macApp, bool) {
	if env.goos != "darwin" || env.executable == nil {
		// Not a Mac — or an app with no look at its program (a test's).
		return macApp{}, false
	}
	self, err := env.executable()
	if err != nil {
		return macApp{}, false
	}
	if self, err = env.resolve(self); err != nil {
		return macApp{}, false
	}
	macOS := filepath.Dir(self)
	contents := filepath.Dir(macOS)
	app := filepath.Dir(contents)
	if filepath.Base(self) != "vaultsync" || filepath.Base(macOS) != "MacOS" || filepath.Base(contents) != "Contents" || filepath.Base(app) != appBundleName {
		return macApp{}, false
	}
	inApplications := false
	for _, dir := range []string{"/Applications", filepath.Join(env.home, "Applications")} {
		if resolved, err := env.resolve(dir); err == nil && resolved == filepath.Dir(app) {
			inApplications = true
		}
	}
	if !inApplications {
		return macApp{}, false
	}
	data, err := env.readFile(filepath.Join(contents, "Info.plist"))
	if err != nil {
		return macApp{}, false
	}
	if id, ok := plistString(data, "CFBundleIdentifier"); !ok || id != appBundleID {
		return macApp{}, false
	}
	tray := filepath.Join(macOS, "vaultsync-tray")
	if fi, err := env.lstat(tray); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return macApp{}, false
	}
	return macApp{Path: app, Agent: self, Tray: tray}, true
}

// plistString reads one string value from the top-level dictionary of an
// XML property list. A binary plist, a key given twice, or a value that is
// not a string reads as nothing: the caller then does not trust the file.
func plistString(data []byte, key string) (string, bool) {
	var plist struct {
		Dict struct {
			Items []struct {
				XMLName xml.Name
				Value   string `xml:",chardata"`
			} `xml:",any"`
		} `xml:"dict"`
	}
	if err := xml.Unmarshal(data, &plist); err != nil {
		return "", false
	}
	items := plist.Dict.Items
	value, found := "", false
	for i := 0; i+1 < len(items); i++ {
		if items[i].XMLName.Local != "key" || items[i].Value != key {
			continue
		}
		if found || items[i+1].XMLName.Local != "string" {
			return "", false
		}
		value, found = items[i+1].Value, true
	}
	return value, found
}
