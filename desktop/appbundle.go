package main

import (
	"bytes"
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
	for _, dir := range applicationsDirs(env) {
		if dir == filepath.Dir(app) {
			inApplications = true
		}
	}
	if !inApplications {
		return macApp{}, false
	}
	return checkApp(env, app)
}

// applicationsDirs are /Applications and ~/Applications as they resolve.
func applicationsDirs(env appEnv) []string {
	var dirs []string
	for _, dir := range []string{"/Applications", filepath.Join(env.home, "Applications")} {
		if resolved, err := env.resolve(dir); err == nil {
			dirs = append(dirs, resolved)
		}
	}
	return dirs
}

// checkApp: app is VaultSync for Mac — eu.vaultsync.desktop at the top of
// its Info.plist, its agent and the menu-bar app plain executable files.
func checkApp(env appEnv, app string) (macApp, bool) {
	data, err := env.readFile(filepath.Join(app, "Contents", "Info.plist"))
	if err != nil {
		return macApp{}, false
	}
	if id, ok := plistString(data, "CFBundleIdentifier"); !ok || id != appBundleID {
		return macApp{}, false
	}
	agent := filepath.Join(app, "Contents", "MacOS", "vaultsync")
	tray := filepath.Join(app, "Contents", "MacOS", "vaultsync-tray")
	for _, prog := range []string{agent, tray} {
		if fi, err := env.lstat(prog); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
			return macApp{}, false
		}
	}
	return macApp{Path: app, Agent: agent, Tray: tray}, true
}

// installedAppHolding finds VaultSync for Mac installed in /Applications or
// ~/Applications whose agent is byte for byte the program at agent — the
// same build as VaultSync's copy, say.
func installedAppHolding(env appEnv, agent string) (macApp, bool) {
	if env.goos != "darwin" || env.readFile == nil {
		return macApp{}, false
	}
	want, err := env.readFile(agent)
	if err != nil {
		return macApp{}, false
	}
	for _, dir := range applicationsDirs(env) {
		bundle, ok := checkApp(env, filepath.Join(dir, appBundleName))
		if !ok {
			continue
		}
		if have, err := env.readFile(bundle.Agent); err == nil && bytes.Equal(have, want) {
			return bundle, true
		}
	}
	return macApp{}, false
}

// plistFirstString reads the first string of the array under key in the
// top-level dictionary of an XML property list — a LaunchAgent's program,
// in ProgramArguments.
func plistFirstString(data []byte, key string) (string, bool) {
	strs, ok := plistArray(data, key)
	if !ok || len(strs) == 0 {
		return "", false
	}
	return strs[0], true
}

// plistArray reads the strings of the array under key in the top-level
// dictionary of an XML property list.
func plistArray(data []byte, key string) ([]string, bool) {
	var plist struct {
		Dict struct {
			Items []struct {
				XMLName xml.Name
				Value   string   `xml:",chardata"`
				Strings []string `xml:"string"`
			} `xml:",any"`
		} `xml:"dict"`
	}
	if err := xml.Unmarshal(data, &plist); err != nil {
		return nil, false
	}
	items := plist.Dict.Items
	for i := 0; i+1 < len(items); i++ {
		if items[i].XMLName.Local == "key" && items[i].Value == key {
			if next := items[i+1]; next.XMLName.Local == "array" {
				return next.Strings, true
			}
			return nil, false
		}
	}
	return nil, false
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
