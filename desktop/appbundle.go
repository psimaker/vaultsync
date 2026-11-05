package main

import (
	"bytes"
	"context"
	"debug/macho"
	"encoding/binary"
	"encoding/xml"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
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
	owned      func(string) bool // what is at the path belongs to this account
}

func liveAppEnv(goos, home string) appEnv {
	return appEnv{goos: goos, home: home, executable: os.Executable, resolve: filepath.EvalSymlinks, readFile: os.ReadFile, lstat: os.Lstat, owned: ownedByMe}
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
	app, why := appOfThisProgram(env)
	return app, app.Path != "" && why == ""
}

// appOfThisProgram is findInstalledApp with the reason a program inside a
// VaultSync for Mac bundle does not count — said by setup, which then runs
// the service from VaultSync's own copy (decision 053). Not in such a
// bundle at all: no app, no reason.
func appOfThisProgram(env appEnv) (macApp, string) {
	if env.goos != "darwin" || env.executable == nil {
		// Not a Mac — or an app with no look at its program (a test's).
		return macApp{}, ""
	}
	self, err := env.executable()
	if err != nil {
		return macApp{}, ""
	}
	if self, err = env.resolve(self); err != nil || !isAppAgent(self) {
		return macApp{}, ""
	}
	app := filepath.Dir(filepath.Dir(filepath.Dir(self)))
	inApplications := false
	for _, dir := range applicationsDirs(env) {
		if dir == filepath.Dir(app) {
			inApplications = true
		}
	}
	if !inApplications {
		return macApp{Path: app}, "it is not in Applications or ~/Applications"
	}
	got, why := checkApp(env, app)
	if why != "" {
		return macApp{Path: app}, why
	}
	return got, ""
}

// isAppAgent: prog is Contents/MacOS/vaultsync of a bundle named VaultSync
// for Mac.app — by its path alone.
func isAppAgent(prog string) bool {
	macOS := filepath.Dir(prog)
	contents := filepath.Dir(macOS)
	return filepath.Base(prog) == "vaultsync" && filepath.Base(macOS) == "MacOS" && filepath.Base(contents) == "Contents" && filepath.Base(filepath.Dir(contents)) == appBundleName
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

// checkApp: app is VaultSync for Mac — folders, not links to them, down to
// Contents/MacOS, eu.vaultsync.desktop at the top of its Info.plist, its
// agent and the menu-bar app plain executable files — and this account's:
// the bundle's owner, as the setup link decides it too. Another account's
// VaultSync for Mac (or one root installed) is theirs to update or move to
// the Trash, and this account's service must not end with it; one locked
// in the Finder is still its owner's, and the service runs from it. It
// says why app is not VaultSync for Mac ("" when it is).
func checkApp(env appEnv, app string) (macApp, string) {
	if env.owned == nil {
		return macApp{}, "it could not be looked at"
	}
	for _, dir := range []string{app, filepath.Join(app, "Contents"), filepath.Join(app, "Contents", "MacOS")} {
		if fi, err := env.lstat(dir); err != nil || !fi.IsDir() {
			return macApp{}, "it is not a folder of its own (a link, or part of it is)"
		}
	}
	if !env.owned(app) {
		return macApp{}, "it belongs to another account"
	}
	data, err := env.readFile(filepath.Join(app, "Contents", "Info.plist"))
	if err != nil {
		return macApp{}, "its Info.plist cannot be read"
	}
	if id, ok := plistString(data, "CFBundleIdentifier"); !ok || id != appBundleID {
		return macApp{}, "its Info.plist does not name " + appBundleID
	}
	agent := filepath.Join(app, "Contents", "MacOS", "vaultsync")
	tray := filepath.Join(app, "Contents", "MacOS", "vaultsync-tray")
	for _, prog := range []string{agent, tray} {
		if fi, err := env.lstat(prog); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
			return macApp{}, "its " + filepath.Base(prog) + " is missing or no program"
		}
	}
	return macApp{Path: app, Agent: agent, Tray: tray}, ""
}

// installedApps are the VaultSync for Mac bundles installed in /Applications
// and ~/Applications that count (checkApp), in that order.
func installedApps(env appEnv) []macApp {
	if env.goos != "darwin" || env.readFile == nil {
		return nil
	}
	var apps []macApp
	for _, dir := range applicationsDirs(env) {
		if bundle, why := checkApp(env, filepath.Join(dir, appBundleName)); why == "" {
			apps = append(apps, bundle)
		}
	}
	return apps
}

// installedAppHolding finds VaultSync for Mac installed in /Applications or
// ~/Applications — this account's, no link (checkApp) — whose agent is the
// build of the program at agent (sameBuild): VaultSync's copy, or the bare
// agent of the same release.
func installedAppHolding(env appEnv, agent string) (macApp, bool) {
	want, err := readIf(env, agent)
	if err != nil {
		return macApp{}, false
	}
	for _, bundle := range installedApps(env) {
		if have, err := env.readFile(bundle.Agent); err == nil && sameBuild(have, want) {
			return bundle, true
		}
	}
	return macApp{}, false
}

func readIf(env appEnv, path string) ([]byte, error) {
	if env.readFile == nil {
		return nil, fs.ErrNotExist
	}
	return env.readFile(path)
}

// sameBuild: two programs are one build — byte for byte, or with the same
// code directory, the part of a signature its cdhash is the hash of. The
// bare agent of a release and the one in VaultSync for Mac are signed
// apart, and their signatures' other bytes (the time stamps) differ.
func sameBuild(a, b []byte) bool {
	if bytes.Equal(a, b) {
		return true
	}
	ca, ok := codeDirectory(a)
	if !ok {
		return false
	}
	cb, ok := codeDirectory(b)
	return ok && bytes.Equal(ca, cb)
}

// codeDirectory is the code directory of a signed, single-architecture
// Mach-O program: LC_CODE_SIGNATURE's super blob, its slot 0.
func codeDirectory(data []byte) ([]byte, bool) {
	f, err := macho.NewFile(bytes.NewReader(data))
	if err != nil {
		return nil, false
	}
	defer f.Close()
	const (
		lcCodeSignature    = 0x1d
		superBlobMagic     = 0xfade0cc0
		codeDirectoryType  = 0
		codeDirectoryMagic = 0xfade0c02
	)
	for _, load := range f.Loads {
		raw := load.Raw()
		if len(raw) < 16 || f.ByteOrder.Uint32(raw) != lcCodeSignature {
			continue
		}
		off, size := uint64(f.ByteOrder.Uint32(raw[8:])), uint64(f.ByteOrder.Uint32(raw[12:]))
		if off+size > uint64(len(data)) || size < 12 {
			return nil, false
		}
		blob := data[off : off+size]
		if binary.BigEndian.Uint32(blob) != superBlobMagic {
			return nil, false
		}
		count := uint64(binary.BigEndian.Uint32(blob[8:]))
		for i := uint64(0); i < count; i++ {
			entry := 12 + 8*i
			if entry+8 > uint64(len(blob)) {
				return nil, false
			}
			if binary.BigEndian.Uint32(blob[entry:]) != codeDirectoryType {
				continue
			}
			at := uint64(binary.BigEndian.Uint32(blob[entry+4:]))
			if at+8 > uint64(len(blob)) || binary.BigEndian.Uint32(blob[at:]) != codeDirectoryMagic {
				return nil, false
			}
			end := at + uint64(binary.BigEndian.Uint32(blob[at+4:]))
			if end > uint64(len(blob)) {
				return nil, false
			}
			return blob[at:end], true
		}
		return nil, false
	}
	return nil, false
}

// appCheckEvery is how often a service that runs from VaultSync for Mac
// looks whether the app is still there; appGoneGrace is how long a program
// found missing gets to come back before that counts — the setup link's
// update leaves the app's place empty for an instant between its two
// renames.
var (
	appCheckEvery = 5 * time.Second
	appGoneGrace  = 2 * time.Second
)

// whileProgramStays returns a context that ends once prog — the agent in
// VaultSync for Mac the service runs — is gone: the app moved out of
// Applications or put in the Trash, which keeps a running program running
// until the next login. The person who did that expects VaultSync to stop,
// and a service that went on would carry what they delete next to every
// device (decision 053). It looks at once, then every every; missing twice,
// grace apart, is gone. ended says what ended it, if the watch did; stop
// ends the look.
func whileProgramStays(ctx context.Context, prog string, lstat func(string) (fs.FileInfo, error), every, grace time.Duration) (watched context.Context, ended func() programEnd, stop func()) {
	watched, cancel := context.WithCancel(ctx)
	var end atomic.Int32
	// Anything but the program there counts as missing — not there, a file
	// or a link where the bundle was, no access to it: the watch fails
	// closed, and launchd starts the service again once it can run.
	missing := func() bool {
		fi, err := lstat(prog)
		return err != nil || !fi.Mode().IsRegular()
	}
	// Another program at the path — the setup link put another VaultSync
	// for Mac in place, or one was dragged over it — ends the run too, not
	// as the app's departure: launchd starts the service again from what is
	// there now (the run of an older build parks until then, decision 054).
	started, startErr := lstat(prog)
	replaced := func() bool {
		fi, err := lstat(prog)
		return startErr == nil && started != nil && err == nil && fi != nil && fi.Mode().IsRegular() && !os.SameFile(started, fi)
	}
	go func() {
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			if replaced() {
				end.Store(int32(programReplaced))
				cancel()
				return
			}
			if missing() {
				select {
				case <-watched.Done():
					return
				case <-time.After(grace):
				}
				if missing() {
					end.Store(int32(programGone))
					cancel()
					return
				}
			}
			select {
			case <-watched.Done():
				return
			case <-tick.C:
			}
		}
	}()
	return watched, func() programEnd { return programEnd(end.Load()) }, cancel
}

// programEnd is what ended a watched run: not the watch, the program gone
// (the app moved or put in the Trash), or another program in its place.
type programEnd int32

const (
	programHeld programEnd = iota
	programGone
	programReplaced
)

// watchOwnApp: a service started from VaultSync for Mac — the program's
// path, as it was started, is Contents/MacOS/vaultsync of a bundle of that
// name, whatever else is true of the bundle — runs while that program stays
// (whileProgramStays). Fails closed: no look at the bundle can keep it from
// being watched. Any other program is not watched.
func watchOwnApp(ctx context.Context, env appEnv) (watched context.Context, ended func() programEnd, stop func()) {
	if env.lstat != nil {
		if self, ok := ownAppAgent(env); ok {
			return whileProgramStays(ctx, self, env.lstat, appCheckEvery, appGoneGrace)
		}
	}
	return ctx, func() programEnd { return programHeld }, func() {}
}

// ownAppAgent: on a Mac, this program is VaultSync for Mac's agent — by the
// path it was started with, or the one a link it was started through leads
// to (the vaultsync command setup points into the app) — and that path.
func ownAppAgent(env appEnv) (string, bool) {
	if env.goos != "darwin" || env.executable == nil {
		return "", false
	}
	self, err := env.executable()
	if err != nil {
		return "", false
	}
	if env.resolve != nil {
		if resolved, err := env.resolve(self); err == nil && isAppAgent(resolved) {
			self = resolved
		}
	}
	return self, isAppAgent(self)
}

// whileNoAppCounts returns a context that ends once a VaultSync for Mac
// that counts is installed (installedApps), looked at every every — for a
// run of any other program, which does not sync beside the app (decision
// 053 (8)). came says which app ended it; stop ends the look.
func whileNoAppCounts(ctx context.Context, env appEnv, every time.Duration) (watched context.Context, came func() (macApp, bool), stop func()) {
	watched, cancel := context.WithCancel(ctx)
	var found atomic.Pointer[macApp]
	go func() {
		tick := time.NewTicker(every)
		defer tick.Stop()
		for {
			select {
			case <-watched.Done():
				return
			case <-tick.C:
				if apps := installedApps(env); len(apps) > 0 {
					found.Store(&apps[0])
					cancel()
					return
				}
			}
		}
	}()
	return watched, func() (macApp, bool) {
		if app := found.Load(); app != nil {
			return *app, true
		}
		return macApp{}, false
	}, cancel
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
