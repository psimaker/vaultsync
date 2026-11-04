package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// #254 (M4 step 3 part 2, decision 053): on a Mac the background service runs
// from VaultSync for Mac when setup runs from the app installed in
// Applications, macOS lists it under the app, and the menu-bar app starts at
// login — again after a crash, not after Quit.

// shippedInfoPlist is the bundle's Info.plist as make-app.sh writes it.
func shippedInfoPlist(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("macos", "Info.plist"))
	if err != nil {
		t.Fatal(err)
	}
	return bytes.ReplaceAll(data, []byte("@VERSION@"), []byte("0.2.0"))
}

// fakeMacApp puts VaultSync for Mac into dir as make-app.sh lays it out
// (the shipped Info.plist, both programs) and returns the bundle's path.
func fakeMacApp(t *testing.T, dir, name string, plist []byte) string {
	t.Helper()
	app := filepath.Join(dir, name)
	writeFile(t, filepath.Join(app, "Contents", "Info.plist"), string(plist))
	for _, prog := range []string{"vaultsync", "vaultsync-tray"} {
		p := filepath.Join(app, "Contents", "MacOS", prog)
		writeFile(t, p, "#!/bin/sh\n")
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return app
}

// macAppEnv looks at this computer's files, with home as the home folder,
// systemApps standing in for /Applications, and exe as the program.
func macAppEnv(home, systemApps, exe string) appEnv {
	return appEnv{goos: "darwin", home: home,
		executable: func() (string, error) { return exe, nil },
		resolve: func(p string) (string, error) {
			if p == "/Applications" {
				p = systemApps
			}
			return filepath.EvalSymlinks(p)
		},
		readFile: os.ReadFile, lstat: os.Lstat, owned: ownedByMe}
}

func TestIssue254_FindsVaultSyncForMacOnlyWhereItIsInstalled(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tmp, "home")
	systemApps := filepath.Join(tmp, "system-applications")
	userApps := filepath.Join(home, "Applications")
	plist := shippedInfoPlist(t)
	if id, ok := plistString(plist, "CFBundleIdentifier"); !ok || id != appBundleID {
		t.Fatalf("the shipped Info.plist reads %q %v", id, ok)
	}
	agentIn := func(app string) string { return filepath.Join(app, "Contents", "MacOS", "vaultsync") }

	// Installed: in ~/Applications or in /Applications.
	for _, dir := range []string{userApps, systemApps} {
		app := fakeMacApp(t, dir, appBundleName, plist)
		got, ok := findInstalledApp(macAppEnv(home, systemApps, agentIn(app)))
		if !ok || got.Path != app || got.Agent != agentIn(app) || got.Tray != filepath.Join(app, "Contents", "MacOS", "vaultsync-tray") {
			t.Fatalf("installed in %s: %+v %v", dir, got, ok)
		}
	}
	// Reached through a link (the command link setup makes): the bundle's
	// own path, resolved.
	link := filepath.Join(tmp, "link-to-agent")
	if err := os.Symlink(agentIn(filepath.Join(userApps, appBundleName)), link); err != nil {
		t.Fatal(err)
	}
	if got, ok := findInstalledApp(macAppEnv(home, systemApps, link)); !ok || got.Agent != agentIn(filepath.Join(userApps, appBundleName)) {
		t.Fatalf("through a link: %+v %v", got, ok)
	}

	// Not installed: anywhere else, under another name, another identity,
	// an Info.plist that cannot be trusted, a menu-bar app missing or not
	// a plain program, another program of the bundle.
	notFound := func(what string, env appEnv) {
		t.Helper()
		if got, ok := findInstalledApp(env); ok {
			t.Errorf("%s: found %+v", what, got)
		}
	}
	for _, dir := range []string{filepath.Join(home, "Downloads"), filepath.Join(tmp, "AppTranslocation", "0F1E2D3C", "d")} {
		app := fakeMacApp(t, dir, appBundleName, plist)
		notFound("a bundle in "+dir, macAppEnv(home, systemApps, agentIn(app)))
	}
	ios := fakeMacApp(t, filepath.Join(tmp, "other"), "VaultSync.app", plist)
	other := filepath.Join(userApps, "VaultSync.app")
	if err := os.Rename(ios, other); err != nil {
		t.Fatal(err)
	}
	notFound("a bundle named VaultSync.app", macAppEnv(home, systemApps, agentIn(other)))
	for name, info := range map[string]string{
		"another identifier":      strings.Replace(string(plist), appBundleID, "eu.vaultsync.app", 1),
		"a binary Info.plist":     "bplist00\x00\x01",
		"the identifier twice":    strings.Replace(string(plist), "<dict>", "<dict>\n\t<key>CFBundleIdentifier</key>\n\t<string>"+appBundleID+"</string>", 1),
		"an identifier no string": strings.Replace(string(plist), "<string>"+appBundleID+"</string>", "<array><string>"+appBundleID+"</string></array>", 1),
	} {
		dir := filepath.Join(tmp, "case-"+strings.ReplaceAll(name, " ", "-"))
		app := fakeMacApp(t, dir, appBundleName, []byte(info))
		notFound(name, macAppEnv(dir, filepath.Join(tmp, "none"), agentIn(app)))
		// (dir stands in for the home folder: its Applications is not dir
		// itself, so make it so.)
		home2 := filepath.Join(tmp, "home-"+strings.ReplaceAll(name, " ", "-"))
		if err := os.MkdirAll(home2, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(dir, filepath.Join(home2, "Applications")); err != nil {
			t.Fatal(err)
		}
		notFound(name+" in ~/Applications", macAppEnv(home2, filepath.Join(tmp, "none"), agentIn(filepath.Join(home2, "Applications", appBundleName))))
	}
	trayCases := map[string]func(tray string){
		"no menu-bar app": func(tray string) { _ = os.Remove(tray) },
		"a menu-bar app that is a link": func(tray string) {
			_ = os.Remove(tray)
			_ = os.Symlink("/bin/sh", tray)
		},
		"a menu-bar app that cannot run": func(tray string) { _ = os.Chmod(tray, 0o644) },
		"a menu-bar app that is a folder": func(tray string) {
			_ = os.Remove(tray)
			_ = os.Mkdir(tray, 0o755)
		},
	}
	for name, spoil := range trayCases {
		h := filepath.Join(tmp, "tray-"+strings.ReplaceAll(name, " ", "-"))
		app := fakeMacApp(t, filepath.Join(h, "Applications"), appBundleName, plist)
		spoil(filepath.Join(app, "Contents", "MacOS", "vaultsync-tray"))
		notFound(name, macAppEnv(h, filepath.Join(tmp, "none"), agentIn(app)))
	}
	installed := filepath.Join(userApps, appBundleName)
	notFound("another program of the bundle", macAppEnv(home, systemApps, filepath.Join(installed, "Contents", "MacOS", "vaultsync-tray")))
	// A program named vaultsync elsewhere in the bundle is not its agent —
	// also with a menu-bar app beside it and an Info.plist above it, so
	// only the bundle's shape tells.
	for _, dir := range []string{filepath.Join(installed, "Contents", "Resources"), filepath.Join(installed, "Other", "MacOS")} {
		for _, prog := range []string{"vaultsync", "vaultsync-tray"} {
			writeFile(t, filepath.Join(dir, prog), "#!/bin/sh\n")
			if err := os.Chmod(filepath.Join(dir, prog), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		writeFile(t, filepath.Join(filepath.Dir(dir), "Info.plist"), string(plist))
		notFound("a vaultsync in "+dir, macAppEnv(home, systemApps, filepath.Join(dir, "vaultsync")))
	}
	linux := macAppEnv(home, systemApps, agentIn(installed))
	linux.goos = "linux"
	notFound("not a Mac", linux)
	notFound("no look at the program (a test's app)", appEnv{})
	broken := macAppEnv(home, systemApps, agentIn(installed))
	broken.executable = func() (string, error) { return "", errors.New("no executable") }
	notFound("the program's path unknown", broken)

	// The service runs the app's agent when setup runs from the app, the
	// copy otherwise.
	lay, _ := layoutFor("darwin", home, envOf(nil))
	a := &app{goos: "darwin", home: home, lay: lay, appEnv: macAppEnv(home, systemApps, agentIn(installed))}
	if exe, bundle, fromApp, _ := a.serviceProgram(); !fromApp || exe != agentIn(installed) || bundle.Tray == "" {
		t.Fatalf("from the app: %q %+v %v", exe, bundle, fromApp)
	}
	a.appEnv = macAppEnv(home, systemApps, lay.Agent)
	if exe, _, fromApp, _ := a.serviceProgram(); fromApp || exe != lay.Agent {
		t.Fatalf("from the copy: %q %v", exe, fromApp)
	}
}

func TestIssue254_TheServiceNamesVaultSyncForMac(t *testing.T) {
	svc, home := testService(t, "darwin", &fakeRunner{})
	exe := filepath.Join(home, "Applications", appBundleName, "Contents", "MacOS", "vaultsync")
	data, err := svc.unitFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := plistString(data, "AssociatedBundleIdentifiers"); !ok || id != appBundleID {
		t.Fatalf("the service names %q %v:\n%s", id, ok, data)
	}
	if label, ok := plistString(data, "Label"); !ok || label != launchdLabel {
		t.Fatalf("label %q %v", label, ok)
	}
	if !strings.Contains(string(data), "<string>"+exe+"</string>\n\t\t<string>run</string>") {
		t.Fatalf("the program is not the app's agent:\n%s", data)
	}
}

func TestIssue254_MenuBarAppStartsAtLogin(t *testing.T) {
	svc, home := testService(t, "darwin", &fakeRunner{})
	tray := filepath.Join(home, "Applications", appBundleName, "Contents", "MacOS", "vaultsync-tray")
	data := trayPlist(tray, svc.lay.Base, svc.lay.TrayLogFile())
	for key, want := range map[string]string{"Label": trayLabel, "AssociatedBundleIdentifiers": appBundleID, "ProcessType": "Interactive", "StandardErrorPath": svc.lay.TrayLogFile()} {
		if got, ok := plistString(data, key); !ok || got != want {
			t.Errorf("%s: %q %v", key, got, ok)
		}
	}
	text := string(data)
	for _, want := range []string{
		"<string>" + tray + "</string>\n\t\t<string>--state-dir</string>\n\t\t<string>" + svc.lay.Base + "</string>\n\t</array>",
		"<key>RunAtLoad</key>\n\t<true/>",
		// Started again after a crash, not after Quit (exit 0).
		"<key>KeepAlive</key>\n\t<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>",
		"<key>ThrottleInterval</key>\n\t<integer>10</integer>",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the tray's plist lacks %q:\n%s", want, text)
		}
	}
	if filepath.Dir(svc.lay.TrayLogFile()) != svc.lay.Logs || filepath.Base(svc.lay.TrayLogFile()) != "vaultsync-tray.log" {
		t.Fatalf("tray log: %s", svc.lay.TrayLogFile())
	}
	if odd := trayPlist("/Users/a&b/<x>/vaultsync-tray", svc.lay.Base, svc.lay.TrayLogFile()); !strings.Contains(string(odd), "<string>/Users/a&amp;b/&lt;x&gt;/vaultsync-tray</string>") {
		t.Fatalf("escaping:\n%s", odd)
	}
	if err := svc.installTray("/tmp/bad\npath", true); err == nil {
		t.Fatal("a path with a newline must be refused")
	}
	if _, err := os.Stat(svc.trayPlistPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused path left a plist: %v", err)
	}
	if svc.trayPlistPath() != filepath.Join(home, "Library", "LaunchAgents", "eu.vaultsync.tray.plist") {
		t.Fatalf("plist path: %s", svc.trayPlistPath())
	}
	lsvc, _ := testService(t, "linux", &fakeRunner{})
	if err := lsvc.installTray("/opt/vaultsync-tray", true); err == nil {
		t.Fatal("a start at login on Linux is not this LaunchAgent's")
	}
}

func TestIssue254_MenuBarAppStartAtLoginIsIdempotent(t *testing.T) {
	run := &fakeRunner{}
	svc, home := testService(t, "darwin", run)
	tray := filepath.Join(home, "Applications", appBundleName, "Contents", "MacOS", "vaultsync-tray")
	plist := svc.trayPlistPath()
	target := "gui/501/" + trayLabel
	if err := svc.installTray(tray, true); err != nil {
		t.Fatal(err)
	}
	// No enable: the service's undoes a vaultsync stop, the menu-bar app's
	// would only undo the person's own launchctl disable.
	if got := strings.Join(run.calls, "|"); got != "launchctl print-disabled gui/501|launchctl print "+target+"|launchctl bootstrap gui/501 "+plist {
		t.Fatalf("first install ran: %s", got)
	}
	if !run.trayLoaded || run.trayIdle || run.loaded {
		t.Fatalf("the tray job: loaded=%v idle=%v (the agent's job: %v)", run.trayLoaded, run.trayIdle, run.loaded)
	}
	if st, err := os.Stat(svc.lay.Logs); err != nil || !st.IsDir() {
		t.Fatalf("log folder missing: %v", err)
	}

	// Nothing changed: only a look — also after the person quit the app,
	// which stays off until the next login.
	run.trayIdle = true
	run.calls = nil
	if err := svc.installTray(tray, false); err != nil || strings.Join(run.calls, "|") != "launchctl print-disabled gui/501|launchctl print "+target || !run.trayIdle {
		t.Fatalf("second install: %v %v idle=%v", err, run.calls, run.trayIdle)
	}
	// A new app (a new agent came with it): restarted — and started again.
	run.calls = nil
	if err := svc.installTray(tray, true); err != nil || strings.Join(run.calls, "|") != "launchctl print-disabled gui/501|launchctl print "+target+"|launchctl kickstart -k "+target || run.trayIdle {
		t.Fatalf("new app: %v %v idle=%v", err, run.calls, run.trayIdle)
	}
	// The app moved: the job is loaded afresh with the new file.
	run.calls = nil
	moved := filepath.Join("/Applications", appBundleName, "Contents", "MacOS", "vaultsync-tray")
	if err := svc.installTray(moved, false); err != nil ||
		strings.Join(run.calls, "|") != "launchctl print-disabled gui/501|launchctl print "+target+"|launchctl bootout "+target+"|launchctl print "+target+"|launchctl bootstrap gui/501 "+plist {
		t.Fatalf("moved app: %v %v", err, run.calls)
	}
	if data, _ := os.ReadFile(plist); !strings.Contains(string(data), "<string>"+moved+"</string>") {
		t.Fatalf("the plist names the old place:\n%s", data)
	}
	// The agent's job was never touched.
	for _, c := range run.calls {
		if strings.Contains(c, launchdLabel) {
			t.Fatalf("the agent's job was touched: %v", run.calls)
		}
	}

	// The person turned the job off (launchctl disable): setup leaves it
	// off, loaded or not — the file is written for their own enable.
	run.trayOverride = true
	for _, loaded := range []bool{true, false} {
		run.trayLoaded, run.trayIdle = loaded, false
		run.calls = nil
		err := svc.installTray(tray, true)
		if !errors.Is(err, errTrayDisabled) || strings.Join(run.calls, "|") != "launchctl print-disabled gui/501" || run.trayLoaded != loaded {
			t.Fatalf("disabled by the person (loaded %v): %v %v", loaded, err, run.calls)
		}
	}
	if data, _ := os.ReadFile(plist); !strings.Contains(string(data), "<string>"+tray+"</string>") {
		t.Fatalf("the file was not written for a later enable:\n%s", data)
	}
}

func TestIssue254_UninstallRemovesTheMenuBarAppStartAtLogin(t *testing.T) {
	run := &fakeRunner{}
	svc, home := testService(t, "darwin", run)
	tray := filepath.Join(home, "Applications", appBundleName, "Contents", "MacOS", "vaultsync-tray")
	target := "gui/501/" + trayLabel
	if err := svc.installTray(tray, true); err != nil {
		t.Fatal(err)
	}
	run.calls = nil
	if found, err := svc.uninstallTray(); err != nil || !found {
		t.Fatalf("uninstall: %v %v", found, err)
	}
	// Stopped and waited for — the app lets go of tray.lock as it ends —
	// then the file goes.
	if got := strings.Join(run.calls, "|"); got != "launchctl print "+target+"|launchctl bootout "+target+"|launchctl print "+target {
		t.Fatalf("uninstall ran: %s", got)
	}
	if _, err := os.Stat(svc.trayPlistPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the plist stayed: %v", err)
	}
	if found, err := svc.uninstallTray(); err != nil || found {
		t.Fatalf("second uninstall: %v %v", found, err)
	}
	// A job already on its way out answers the bootout with an error: it
	// is gone all the same, and so is the file.
	if err := svc.installTray(tray, true); err != nil {
		t.Fatal(err)
	}
	run.trayBootoutVanishes = true
	if found, err := svc.uninstallTray(); err != nil || !found || run.trayLoaded {
		t.Fatalf("a job on its way out: %v %v loaded=%v", found, err, run.trayLoaded)
	}
	run.trayBootoutVanishes = false
	if _, err := os.Stat(svc.trayPlistPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the plist stayed beside a job on its way out: %v", err)
	}
	// No file and no job — a Mac that never had the menu-bar app — is
	// nothing, without asking a manager that may not answer.
	run.probeFails = "Could not print domain: 1: Operation not permitted"
	run.calls, run.probes = nil, nil
	if found, err := svc.uninstallTray(); err != nil || found || len(run.probes) != 0 {
		t.Fatalf("nothing there: %v %v probes %v", found, err, run.probes)
	}
	run.probeFails = ""
	// A file without a loaded job (never started, or a session ended) is
	// found and removed without a bootout.
	writeFile(t, svc.trayPlistPath(), "<plist/>")
	run.calls = nil
	if found, err := svc.uninstallTray(); err != nil || !found || strings.Contains(strings.Join(run.calls, "|"), "bootout") {
		t.Fatalf("file only: %v %v %v", found, err, run.calls)
	}
	// A manager that could not be asked: the file stays — removing it would
	// leave an app nothing stops until the logout.
	writeFile(t, svc.trayPlistPath(), "<plist/>")
	run.probeFails = "Could not print domain: 1: Operation not permitted"
	if found, err := svc.uninstallTray(); err == nil || found || !strings.Contains(err.Error(), "could not be asked") {
		t.Fatalf("manager unknown: %v %v", found, err)
	}
	if _, err := os.Stat(svc.trayPlistPath()); err != nil {
		t.Fatalf("the plist went although the manager could not be asked: %v", err)
	}
	// Not a Mac: nothing to do, nothing asked.
	lrun := &fakeRunner{}
	lsvc, _ := testService(t, "linux", lrun)
	if found, err := lsvc.uninstallTray(); err != nil || found || len(lrun.calls)+len(lrun.probes) != 0 {
		t.Fatalf("linux: %v %v %v %v", found, err, lrun.calls, lrun.probes)
	}
}

func TestIssue254_CommandLinkFollowsIntoTheApp(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	home := t.TempDir()
	lay, _ := layoutFor("darwin", home, envOf(nil))
	// /Applications is a stand-in: what this Mac has installed there does
	// not count.
	systemApps := filepath.Join(t.TempDir(), "Applications")
	if err := os.MkdirAll(systemApps, 0o755); err != nil {
		t.Fatal(err)
	}
	a := &app{goos: "darwin", home: home, lay: lay, appEnv: macAppEnv(home, systemApps, "")}
	link := commandLink(home)
	appAgent := filepath.Join(home, "Applications", appBundleName, "Contents", "MacOS", "vaultsync")
	readLink := func() string {
		t.Helper()
		target, err := os.Readlink(link)
		if err != nil {
			t.Fatal(err)
		}
		return target
	}
	// A link to the copy follows into the app — in one step, nothing left
	// beside it.
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(lay.Agent, link); err != nil {
		t.Fatal(err)
	}
	if got := a.installCommand(&term{out: &bytes.Buffer{}}, appAgent); got != "~/.local/bin/vaultsync" || readLink() != appAgent {
		t.Fatalf("link to the copy: %q → %s", got, readLink())
	}
	if entries, _ := os.ReadDir(filepath.Dir(link)); len(entries) != 1 {
		t.Fatalf("left beside the link: %v", entries)
	}
	// And back to the copy when setup runs from it.
	a.installCommand(&term{out: &bytes.Buffer{}}, lay.Agent)
	if readLink() != lay.Agent {
		t.Fatalf("back to the copy: %s", readLink())
	}
	// From the app in /Applications to the one in ~/Applications.
	if err := replaceLink(link, filepath.Join("/Applications", appBundleName, "Contents", "MacOS", "vaultsync")); err != nil {
		t.Fatal(err)
	}
	a.installCommand(&term{out: &bytes.Buffer{}}, appAgent)
	if readLink() != appAgent {
		t.Fatalf("from /Applications: %s", readLink())
	}
	// Someone else's link or program stays.
	for name, put := range map[string]func(){
		"a link to another program": func() { _ = os.Symlink("/usr/local/bin/vaultsync", link) },
		"a program":                 func() { writeFile(t, link, "#!/bin/sh\necho mine\n") },
		"a link into another app": func() {
			_ = os.Symlink(filepath.Join(home, "Applications", "VaultSync.app", "Contents", "MacOS", "vaultsync"), link)
		},
	} {
		_ = os.Remove(link)
		put()
		before, _ := os.Lstat(link)
		a.installCommand(&term{out: &bytes.Buffer{}}, appAgent)
		after, err := os.Lstat(link)
		if err != nil || !os.SameFile(before, after) {
			t.Fatalf("%s was replaced: %v", name, err)
		}
	}
}

func TestIssue254_RemoveDataTakesTheLinkIntoTheApp(t *testing.T) {
	home := t.TempDir()
	lay, _ := layoutFor("darwin", home, envOf(nil))
	a := &app{goos: "darwin", home: home, lay: lay}
	link := commandLink(home)
	appAgent := filepath.Join(home, "Applications", appBundleName, "Contents", "MacOS", "vaultsync")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(appAgent, link); err != nil {
		t.Fatal(err)
	}
	// While VaultSync's copy is there, the removal did not get that far.
	writeFile(t, lay.Agent, "agent")
	if _, ok := a.removeCommandLink(); ok {
		t.Fatal("removed the link while VaultSync's files are still there")
	}
	if err := os.Remove(lay.Agent); err != nil {
		t.Fatal(err)
	}
	// The app stays for the person to move to the Trash; the link goes.
	if got, ok := a.removeCommandLink(); !ok || got != link {
		t.Fatalf("the link into the app stayed: %q %v", got, ok)
	}
	if _, err := os.Lstat(link); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("link: %v", err)
	}
}

func TestIssue254_RemoveDataPassesOverOnlyTheTrayJobsLock(t *testing.T) {
	lay, _ := ownedFixture(t)
	held, err := lockFile(lay.TrayLock())
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	// Held while launchd runs the menu-bar app's start at login: that job's
	// app holds it, and the removal stops the job.
	if err := probeOwnLocks(lay, false, true); err != nil {
		t.Fatalf("the tray job's lock: %v", err)
	}
	// Held with no such job running: an app opened by hand — refused.
	if err := probeOwnLocks(lay, false, false); !strings.Contains(refusalText(err), "menu-bar app is running") {
		t.Fatalf("a tray opened by hand: %v", err)
	}
	held()
	// Only tray.lock: the engine's lock held by a terminal run is refused
	// whatever the tray job does.
	run, err := lockFile(lay.Lock)
	if err != nil {
		t.Fatal(err)
	}
	defer run()
	if err := probeOwnLocks(lay, false, true); !strings.Contains(refusalText(err), "VaultSync is busy") {
		t.Fatalf("the engine's lock beside a tray job: %v", err)
	}
}

// uninstall --remove-data on a Mac with the menu-bar app started at login:
// the probe passes over the job's tray.lock, the job goes first and is
// waited for, and VaultSync's files go — while an app opened by hand still
// stops the removal before anything changes.
func TestIssue254_UninstallStopsTheMenuBarAppItStartedAtLogin(t *testing.T) {
	lay, foreign := ownedFixture(t)
	if err := os.Remove(foreign); err != nil {
		t.Fatal(err)
	}
	writeFile(t, lay.TrayLogFile(), "tray log")
	home := filepath.Dir(filepath.Dir(filepath.Dir(lay.Base)))
	run := &fakeRunner{}
	svc := service{goos: "darwin", home: home, uid: 501, getenv: envOf(nil), lay: lay, run: run, pause: func(time.Duration) {}}
	out := &bytes.Buffer{}
	a := &app{goos: "darwin", home: home, getenv: envOf(nil), lay: lay, out: out, svc: svc}
	if err := svc.installTray(filepath.Join(home, "Applications", appBundleName, "Contents", "MacOS", "vaultsync-tray"), true); err != nil {
		t.Fatal(err)
	}

	// An app opened by hand holds the lock while the job's own ended at
	// once (idle): refused before anything is asked or changed.
	run.trayIdle = true
	byHand, err := lockFile(lay.TrayLock())
	if err != nil {
		t.Fatal(err)
	}
	run.calls = nil
	if err := a.uninstall(context.Background(), []string{"--remove-data", "--yes"}); !strings.Contains(refusalText(err), "menu-bar app is running") {
		t.Fatalf("an app opened by hand: %v", err)
	}
	if serviceTouched(run.calls) || !run.trayLoaded {
		t.Fatalf("touched before the refusal: %v", run.calls)
	}
	if _, err := os.Stat(svc.trayPlistPath()); err != nil {
		t.Fatalf("the start at login went before the refusal: %v", err)
	}
	byHand()

	// The job's app holds it — and lets go as launchd lets go of the job, a
	// few looks after the bootout: uninstall waits for that before it looks
	// at the lock again (no grace beyond it here).
	run.trayIdle = false
	jobsApp, err := lockFile(lay.TrayLock())
	if err != nil {
		t.Fatal(err)
	}
	run.onTrayBootout = jobsApp
	run.trayLingerAfterBootout = 3
	orig := trayLockWait
	trayLockWait = 0
	defer func() { trayLockWait = orig }()
	run.calls = nil
	if err := a.uninstall(context.Background(), []string{"--remove-data", "--yes"}); err != nil {
		t.Fatalf("uninstall with the tray job running: %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "✓ The menu-bar icon no longer opens when you log in.") || !strings.Contains(out.String(), "✓ VaultSync's own files are removed.") {
		t.Fatalf("output:\n%s", out)
	}
	if run.trayLoaded {
		t.Fatal("the tray job is still loaded")
	}
	// (The removal took tray.lock, which the job's app held until launchd
	// stopped it: the job went first.)
	for _, gone := range []string{svc.trayPlistPath(), lay.Base, lay.TrayLogFile()} {
		if _, err := os.Lstat(gone); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s stayed: %v", gone, err)
		}
	}
}

// A start at login whose program is gone — the app moved or trashed, setup
// run from VaultSync's own copy — is removed rather than left failing at
// every login; one whose program is there stays.
func TestIssue254_SetupFromTheCopyDropsAStaleStartAtLogin(t *testing.T) {
	run := &fakeRunner{}
	svc, home := testService(t, "darwin", run)
	a := &app{goos: "darwin", home: home, lay: svc.lay, svc: svc}
	tray := filepath.Join(home, "Applications", appBundleName, "Contents", "MacOS", "vaultsync-tray")
	writeFile(t, tray, "#!/bin/sh\n")
	if err := svc.installTray(tray, true); err != nil {
		t.Fatal(err)
	}
	if svc.trayProgram() != tray {
		t.Fatalf("read back: %q", svc.trayProgram())
	}
	if a.dropStaleTray() || !run.trayLoaded {
		t.Fatal("a start at login whose app is there was removed")
	}
	if err := os.RemoveAll(filepath.Join(home, "Applications")); err != nil {
		t.Fatal(err)
	}
	if !a.dropStaleTray() || run.trayLoaded {
		t.Fatal("a start at login whose app is gone stayed")
	}
	if _, err := os.Stat(svc.trayPlistPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("its file stayed: %v", err)
	}
	if a.dropStaleTray() {
		t.Fatal("nothing left to remove, yet something was")
	}
}

// A menu-bar app opened by hand that holds tray.lock in the moment the
// login job's app was starting passes the probe as that job's; once the
// job is gone the lock is looked at again, and the removal stops before
// the background service goes.
func TestIssue254_RemoveDataStopsAtAHandOpenedAppBeforeTheServiceGoes(t *testing.T) {
	lay, foreign := ownedFixture(t)
	if err := os.Remove(foreign); err != nil {
		t.Fatal(err)
	}
	home := filepath.Dir(filepath.Dir(filepath.Dir(lay.Base)))
	run := &fakeRunner{}
	svc := service{goos: "darwin", home: home, uid: 501, getenv: envOf(nil), lay: lay, run: run, pause: func(time.Duration) {}}
	a := &app{goos: "darwin", home: home, getenv: envOf(nil), lay: lay, out: &bytes.Buffer{}, svc: svc}
	if err := svc.installTray(filepath.Join(home, "Applications", appBundleName, "Contents", "MacOS", "vaultsync-tray"), true); err != nil {
		t.Fatal(err)
	}
	unit, _ := svc.unitPath()
	writeFile(t, unit, "<plist/>")
	byHand, err := lockFile(lay.TrayLock())
	if err != nil {
		t.Fatal(err)
	}
	defer byHand()
	orig := trayLockWait
	trayLockWait = 50 * time.Millisecond
	defer func() { trayLockWait = orig }()
	err = a.uninstall(context.Background(), []string{"--remove-data", "--yes"})
	if !strings.Contains(refusalText(err), "menu-bar app is running") || !strings.Contains(refusalText(err), "background service is not") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(unit); err != nil {
		t.Fatalf("the background service went before the refusal: %v", err)
	}
	if _, err := os.Stat(lay.State); err != nil {
		t.Fatalf("VaultSync's files went: %v", err)
	}
	if _, err := os.Stat(svc.trayPlistPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the start at login stayed: %v", err)
	}
}

// macSetupFixture is a Mac where VaultSync's engine is prepared (its binary
// recorded, a config answered by a fake engine) and VaultSync for Mac is in
// ~/Applications; the service manager is a fake.
func macSetupFixture(t *testing.T) (*app, *fakeRunner, string) {
	t.Helper()
	eng := newFakeEngine(t)
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tmp, "home")
	lay, err := layoutFor("darwin", home, envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, lay.Syncthing, "engine")
	sum, err := fileSHA256(lay.Syncthing)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(lay.Home, "config.xml"), "<configuration><gui><address>"+eng.addr+"</address><apikey>engine-key</apikey></gui></configuration>\n")
	_, port, _ := strings.Cut(eng.addr, ":")
	n, _ := strconv.Atoi(port)
	st := agentState{GUIPort: n, Env: map[string]string{}}
	st.Syncthing.Version, st.Syncthing.BinarySHA256 = syncthingVersion, sum
	if err := saveState(lay.State, st); err != nil {
		t.Fatal(err)
	}
	bundle := fakeMacApp(t, filepath.Join(home, "Applications"), appBundleName, shippedInfoPlist(t))
	run := &fakeRunner{}
	svc := service{goos: "darwin", home: home, uid: 501, getenv: envOf(nil), lay: lay, run: run, pause: func(time.Duration) {}}
	a := &app{goos: "darwin", home: home, getenv: envOf(nil), lay: lay, out: &bytes.Buffer{}, svc: svc,
		appEnv: macAppEnv(home, filepath.Join(tmp, "no-system-applications"), filepath.Join(bundle, "Contents", "MacOS", "vaultsync"))}
	return a, run, bundle
}

// setup run from VaultSync for Mac installed in ~/Applications: the service
// runs the app's agent, the menu-bar app opens at login, the command link
// points into the app. Run again from VaultSync's copy after the app went
// to the Trash: the service moves back to the copy, the start at login
// whose app is gone is removed, the link follows.
func TestIssue254_SetupFromTheAppRunsTheServiceFromIt(t *testing.T) {
	a, run, app := macSetupFixture(t)
	t.Setenv("PATH", "/usr/bin:/bin")
	orig := ttyPath
	ttyPath = filepath.Join(t.TempDir(), "no-tty")
	defer func() { ttyPath = orig }()
	setup := func() string {
		t.Helper()
		out := &bytes.Buffer{}
		a.out = out
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		// No Hub answers at this address: setup stops at the pairing, after
		// the service and the command are in place.
		err := a.setup(ctx, []string{"--code", "TULIP-ANCHOR-42", "--hub", "127.0.0.1:1"})
		if !strings.Contains(refusalText(err)+fmt.Sprint(err), "did not answer") {
			t.Fatalf("setup: %v\n%s", err, out)
		}
		return out.String()
	}
	program := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			return ""
		}
		prog, _ := plistFirstString(data, "ProgramArguments")
		return prog
	}
	link := commandLink(a.home)
	agent := filepath.Join(app, "Contents", "MacOS", "vaultsync")

	out := setup()
	unit := mustUnitPath(t, a.svc)
	if program(unit) != agent || program(a.svc.trayPlistPath()) != filepath.Join(app, "Contents", "MacOS", "vaultsync-tray") {
		t.Fatalf("from the app: service %q, menu-bar app %q", program(unit), program(a.svc.trayPlistPath()))
	}
	// The start at login gives the menu-bar app VaultSync's folder and its
	// own log — the very file setup wrote, read back.
	trayData, _ := os.ReadFile(a.svc.trayPlistPath())
	if args, _ := plistArray(trayData, "ProgramArguments"); strings.Join(args, "|") != filepath.Join(app, "Contents", "MacOS", "vaultsync-tray")+"|--state-dir|"+a.lay.Base {
		t.Fatalf("the menu-bar app's arguments: %q", args)
	}
	if logPath, _ := plistString(trayData, "StandardErrorPath"); logPath != a.lay.TrayLogFile() {
		t.Fatalf("the menu-bar app's log: %q", logPath)
	}
	if target, _ := os.Readlink(link); target != agent {
		t.Fatalf("the command link: %q", target)
	}
	if !run.loaded || !run.trayLoaded || !strings.Contains(out, "✓ Background service running") || !strings.Contains(out, "✓ Menu-bar icon set up") {
		t.Fatalf("jobs loaded: service %v, menu-bar app %v\n%s", run.loaded, run.trayLoaded, out)
	}
	if _, err := os.Stat(a.lay.Agent); err != nil {
		t.Fatalf("the copy is not kept current: %v", err)
	}

	// Nothing new: neither job is restarted — a menu-bar app the person quit
	// stays quit.
	run.calls = nil
	setup()
	if restarted := restarts(run.calls); len(restarted) > 0 {
		t.Fatalf("nothing new, yet: %v", restarted)
	}
	// A new build came with the app: both jobs are restarted onto it.
	newBuild(t, app, "second")
	run.calls = nil
	setup()
	for _, want := range []string{"launchctl kickstart -k gui/501/" + launchdLabel, "launchctl kickstart -k gui/501/" + trayLabel} {
		if !slices.Contains(run.calls, want) {
			t.Fatalf("an update did not run %q: %v", want, run.calls)
		}
	}

	// Uninstalled, then set up again with `vaultsync` — which uninstall
	// pointed at the copy — while VaultSync for Mac is still installed and
	// holds the same agent: the service goes back into the app.
	if err := a.uninstall(context.Background(), []string{"--yes"}); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if target, _ := os.Readlink(link); target != a.lay.Agent {
		t.Fatalf("after uninstall the command is %q", target)
	}
	copyBytes, _ := os.ReadFile(a.lay.Agent)
	if err := os.WriteFile(agent, copyBytes, 0o755); err != nil { // the same build
		t.Fatal(err)
	}
	a.appEnv = macAppEnv(a.home, filepath.Join(t.TempDir(), "none"), a.lay.Agent)
	out = setup()
	if program(unit) != agent || program(a.svc.trayPlistPath()) != filepath.Join(app, "Contents", "MacOS", "vaultsync-tray") || !strings.Contains(out, "✓ Menu-bar icon set up") {
		t.Fatalf("setup from the copy beside the same app: service %q, menu-bar app %q\n%s", program(unit), program(a.svc.trayPlistPath()), out)
	}
	if target, _ := os.Readlink(link); target != agent {
		t.Fatalf("the command link from the copy beside the app: %q", target)
	}
	// Another build than the app's — a bare download of another release —
	// never takes the service onto the copy beside the app (decision 053):
	// it stops before anything changes and says how to update.
	writeFile(t, agent, "#!/bin/sh\necho another build\n")
	if err := os.Chmod(agent, 0o755); err != nil {
		t.Fatal(err)
	}
	copyBefore, _ := os.ReadFile(a.lay.Agent)
	run.calls = nil
	a.out = &bytes.Buffer{}
	err := a.setup(context.Background(), []string{"--code", "TULIP-ANCHOR-42", "--hub", "127.0.0.1:1"})
	if !strings.Contains(refusalText(err), "this vaultsync is another build") || !strings.Contains(refusalText(err), "Nothing was changed") {
		t.Fatalf("setup from another build beside the app: %v", err)
	}
	if program(unit) != agent || program(a.svc.trayPlistPath()) != filepath.Join(app, "Contents", "MacOS", "vaultsync-tray") || len(restarts(run.calls)) > 0 {
		t.Fatalf("a refused setup changed the service: %q %v", program(unit), run.calls)
	}
	if copyAfter, _ := os.ReadFile(a.lay.Agent); !bytes.Equal(copyAfter, copyBefore) {
		t.Fatal("a refused setup changed VaultSync's copy")
	}
	// Also without the service: the copy and the command would follow it.
	err = a.setup(context.Background(), []string{"--no-service", "--code", "TULIP-ANCHOR-42", "--hub", "127.0.0.1:1"})
	if !strings.Contains(refusalText(err), "this vaultsync is another build") {
		t.Fatalf("setup --no-service from another build beside the app: %v", err)
	}
	if copyAfter, _ := os.ReadFile(a.lay.Agent); !bytes.Equal(copyAfter, copyBefore) {
		t.Fatal("a refused setup --no-service changed VaultSync's copy")
	}

	// The app goes to the Trash; setup from VaultSync's own copy.
	if err := os.RemoveAll(app); err != nil {
		t.Fatal(err)
	}
	a.appEnv = macAppEnv(a.home, filepath.Join(t.TempDir(), "none"), a.lay.Agent)
	out = setup()
	if program(unit) != a.lay.Agent {
		t.Fatalf("from the copy: service %q", program(unit))
	}
	if _, err := os.Stat(a.svc.trayPlistPath()); !errors.Is(err, os.ErrNotExist) || run.trayLoaded || !strings.Contains(out, "no longer opens at login") {
		t.Fatalf("the start at login of a trashed app stayed: %v loaded=%v\n%s", err, run.trayLoaded, out)
	}
	if target, _ := os.Readlink(link); target != a.lay.Agent {
		t.Fatalf("the command link from the copy: %q", target)
	}
}

// newBuild puts a build named mark into the app — the setup link's update.
func newBuild(t *testing.T, app, mark string) {
	t.Helper()
	for _, prog := range []string{"vaultsync", "vaultsync-tray"} {
		p := filepath.Join(app, "Contents", "MacOS", prog)
		writeFile(t, p, "#!/bin/sh\necho "+prog+" "+mark+"\n")
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// restarts are the calls among calls that restart or reload a job.
func restarts(calls []string) []string {
	var out []string
	for _, c := range calls {
		if strings.Contains(c, "kickstart") || strings.Contains(c, "bootout") || strings.Contains(c, "bootstrap") {
			out = append(out, c)
		}
	}
	return out
}

// The setup link put a new VaultSync for Mac in place, and the setup it ran
// stopped before it restarted anything — after it refreshed VaultSync's
// copy, waiting in vain for a pairing in progress. The next setup still
// restarts the service and the menu-bar app onto the new build: whether
// they run it is agent.json's record of what they were started with, not
// the copy (it already matches the new build). Once they run it, setup
// restarts nothing.
func TestIssue254_ASetupThatStoppedEarlyStillRestartsOntoTheNewBuild(t *testing.T) {
	a, run, app := macSetupFixture(t)
	t.Setenv("PATH", "/usr/bin:/bin")
	orig := ttyPath
	ttyPath = filepath.Join(t.TempDir(), "no-tty")
	defer func() { ttyPath = orig }()
	setup := func(timeout time.Duration) error {
		t.Helper()
		a.out = &bytes.Buffer{}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		return a.setup(ctx, []string{"--code", "TULIP-ANCHOR-42", "--hub", "127.0.0.1:1"})
	}
	if err := setup(time.Minute); !strings.Contains(refusalText(err)+fmt.Sprint(err), "did not answer") {
		t.Fatalf("setup: %v\n%s", err, a.out)
	}
	st, err := loadState(a.lay.State)
	if err != nil || st.Started.Service == "" || st.Started.Tray == "" {
		t.Fatalf("what the jobs were started with is not recorded: %+v %v", st.Started, err)
	}

	newBuild(t, app, "second")
	writeFile(t, a.lay.Agent, "the build before")
	pairing, err := lockFile(filepath.Join(a.lay.Base, "pair.lock"))
	if err != nil {
		t.Fatal(err)
	}
	run.calls = nil
	err = setup(1500 * time.Millisecond)
	pairing()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the setup did not stop at the pairing in progress: %v\n%s", err, a.out)
	}
	if have, _ := os.ReadFile(a.lay.Agent); !bytes.Equal(have, ownBytes(t)) {
		t.Fatal("the stopped setup did not refresh the copy before the pairing step")
	}
	if restarted := restarts(run.calls); len(restarted) > 0 {
		t.Fatalf("the stopped setup restarted: %v", restarted)
	}

	run.calls = nil
	if err := setup(time.Minute); !strings.Contains(refusalText(err)+fmt.Sprint(err), "did not answer") {
		t.Fatalf("setup after the stop: %v\n%s", err, a.out)
	}
	for _, want := range []string{"launchctl kickstart -k gui/501/" + launchdLabel, "launchctl kickstart -k gui/501/" + trayLabel} {
		if !slices.Contains(run.calls, want) {
			t.Fatalf("the setup after the stop did not run %q — the old build runs on: %v", want, run.calls)
		}
	}
	run.calls = nil
	if err := setup(time.Minute); !strings.Contains(refusalText(err)+fmt.Sprint(err), "did not answer") {
		t.Fatalf("setup once more: %v", err)
	}
	if restarted := restarts(run.calls); len(restarted) > 0 {
		t.Fatalf("both run the new build, yet: %v", restarted)
	}
}

// A service whose program — VaultSync for Mac — is gone: status says so
// while it still runs and once it cannot start, with the way back and the
// way out; start refuses with the same words instead of loading a job
// launchd cannot start.
func TestIssue254_StatusAndStartSayWhenTheAppIsGone(t *testing.T) {
	run := &fakeRunner{}
	a := installedApp(t, "darwin", run)
	gone := filepath.Join(a.home, "Applications", appBundleName, "Contents", "MacOS", "vaultsync")
	data, err := a.svc.unitFile(gone)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, mustUnitPath(t, a.svc), string(data))
	if prog, ok := a.svc.programGone(); !ok || prog != gone {
		t.Fatalf("programGone: %q %v", prog, ok)
	}
	way := "it runs ~/Applications/VaultSync for Mac.app/Contents/MacOS/vaultsync, which is no longer there"
	out := statusText(t, a)
	if !strings.Contains(out, "Background service: cannot start — "+way) || !strings.Contains(out, shellPath(a.home, a.lay.Agent)+" uninstall") || !strings.Contains(out, shellPath(a.home, a.lay.Agent)+" setup") {
		t.Fatalf("status with the app gone:\n%s", out)
	}
	run.loaded = true
	if out := statusText(t, a); !strings.Contains(out, "Background service: running, but it will not start again — "+way) {
		t.Fatalf("status with the app gone while the service still runs:\n%s", out)
	}
	run.loaded = false
	run.calls = nil
	if err := a.startService(); !strings.Contains(refusalText(err), way) || strings.Contains(refusalText(err), "'~") {
		t.Fatalf("start: %v", err)
	}
	for _, c := range run.calls {
		if strings.Contains(c, "bootstrap") || strings.Contains(c, "enable") {
			t.Fatalf("start loaded a job that cannot start: %v", run.calls)
		}
	}
	// The app back in its place: nothing to say.
	writeFile(t, gone, "#!/bin/sh\n")
	if _, ok := a.svc.programGone(); ok {
		t.Fatal("the program is there again")
	}
	if out := statusText(t, a); strings.Contains(out, "no longer there") {
		t.Fatalf("status with the app back:\n%s", out)
	}
}

// A path shown as a command to type works when typed: shortened to ~ under
// the home folder (as printed paths are), the rest escaped — a ~ in quotes
// would not expand — and quoted outside it. A real shell gives back the
// very path.
func TestIssue254_APathToTypeWorksWhenTyped(t *testing.T) {
	home := t.TempDir()
	for _, p := range []string{
		filepath.Join(home, "Library", "Application Support", "VaultSync", "bin", "vaultsync"),
		filepath.Join(home, "odd $name's (copy) & more", "vaultsync"),
		filepath.Join(home, "plain", "vaultsync"),
		filepath.Join(t.TempDir(), "outside home", "vaultsync"),
	} {
		word := shellPath(home, p)
		if strings.Contains(word, "'~") {
			t.Fatalf("a quoted ~ does not expand: %s", word)
		}
		cmd := exec.Command("/bin/sh", "-c", "printf '%s' "+word)
		cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
		out, err := cmd.Output()
		if err != nil || string(out) != p {
			t.Fatalf("%s typed into sh gave %q (%v), want %q", word, out, err, p)
		}
	}
	if got := shellPath(home, filepath.Join(home, "Library", "Application Support", "x")); got != `~/Library/Application\ Support/x` {
		t.Fatalf("the documented spelling: %s", got)
	}
}

// uninstall without --remove-data keeps the vaultsync command — so the
// link setup pointed into the app goes back to VaultSync's copy, which
// stays, before the app may go to the Trash.
func TestIssue254_UninstallPointsTheCommandBackAtTheCopy(t *testing.T) {
	run := &fakeRunner{}
	a := installedApp(t, "darwin", run)
	writeFile(t, a.lay.Agent, "agent")
	if err := os.Chmod(a.lay.Agent, 0o755); err != nil {
		t.Fatal(err)
	}
	link := commandLink(a.home)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(a.home, "Applications", appBundleName, "Contents", "MacOS", "vaultsync"), link); err != nil {
		t.Fatal(err)
	}
	if err := a.uninstall(context.Background(), []string{"--yes"}); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, a.out)
	}
	if target, err := os.Readlink(link); err != nil || target != a.lay.Agent {
		t.Fatalf("the command link: %q %v", target, err)
	}
	// --remove-data that stops after the service is gone (Obsidian's list
	// cannot be read) leaves the command on the copy as well.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(a.home, "Applications", appBundleName, "Contents", "MacOS", "vaultsync"), link); err != nil {
		t.Fatal(err)
	}
	writeFile(t, mustUnitPath(t, a.svc), "installed\n")
	registry := filepath.Join(a.home, "Library", "Application Support", "obsidian", "obsidian.json")
	if err := os.MkdirAll(registry, 0o700); err != nil { // a folder where the file should be
		t.Fatal(err)
	}
	if err := a.uninstall(context.Background(), []string{"--remove-data", "--yes"}); !strings.Contains(refusalText(err), "could not read Obsidian's list of vaults") {
		t.Fatalf("--remove-data with an unreadable list: %v", err)
	}
	if target, _ := os.Readlink(link); target != a.lay.Agent {
		t.Fatalf("--remove-data that stopped left the command at %q", target)
	}
	if err := os.RemoveAll(filepath.Dir(registry)); err != nil {
		t.Fatal(err)
	}
	// Someone else's link stays as it is.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/usr/local/bin/vaultsync", link); err != nil {
		t.Fatal(err)
	}
	writeFile(t, mustUnitPath(t, a.svc), "installed\n")
	if err := a.uninstall(context.Background(), []string{"--yes"}); err != nil {
		t.Fatal(err)
	}
	if target, _ := os.Readlink(link); target != "/usr/local/bin/vaultsync" {
		t.Fatalf("someone else's link was changed: %q", target)
	}
}

// The command link counts as setup's only for VaultSync for Mac's agent: an
// app of that name with another identifier is someone else's, and its link
// stays; an app gone to the Trash leaves a link that is still setup's.
func TestIssue254_TheCommandIntoAnotherAppIsNotOurs(t *testing.T) {
	home := t.TempDir()
	lay, _ := layoutFor("darwin", home, envOf(nil))
	a := &app{goos: "darwin", home: home, lay: lay}
	apps := filepath.Join(home, "Applications")
	target := filepath.Join(apps, appBundleName, "Contents", "MacOS", "vaultsync")
	if !a.ownCommandTarget(target) {
		t.Fatal("a link into an app that is gone is setup's")
	}
	other := fakeMacApp(t, apps, appBundleName, bytes.Replace(shippedInfoPlist(t), []byte(appBundleID), []byte("com.example.other"), 1))
	if a.ownCommandTarget(target) {
		t.Fatalf("a link into %s, another app of the name, counts as setup's", other)
	}
	if err := os.RemoveAll(other); err != nil {
		t.Fatal(err)
	}
	fakeMacApp(t, apps, appBundleName, shippedInfoPlist(t))
	if !a.ownCommandTarget(target) {
		t.Fatal("a link into VaultSync for Mac is setup's")
	}
}

// --remove-data's refusal after the start at login went says what happened:
// the start at login removed only when there was one.
func TestIssue254_TheRefusalSaysWhatWasRemoved(t *testing.T) {
	if got := refusalText(trayStillRunningRefusal(true)); !strings.Contains(got, "start at login is already removed") {
		t.Fatalf("with a start at login: %s", got)
	}
	if got := refusalText(trayStillRunningRefusal(false)); strings.Contains(got, "start at login") || !strings.Contains(got, "background service is not removed") {
		t.Fatalf("without one: %s", got)
	}
	// A refusal once the service went names the start at login too.
	run := &fakeRunner{}
	a := installedApp(t, "darwin", run)
	if err := a.svc.installTray(filepath.Join(a.home, "Applications", appBundleName, "Contents", "MacOS", "vaultsync-tray"), true); err != nil {
		t.Fatal(err)
	}
	writeFile(t, mustUnitPath(t, a.svc), "installed\n")
	registry := filepath.Join(a.home, "Library", "Application Support", "obsidian", "obsidian.json")
	if err := os.MkdirAll(registry, 0o700); err != nil { // a folder where the file should be
		t.Fatal(err)
	}
	got := refusalText(a.uninstall(context.Background(), []string{"--remove-data", "--yes"}))
	if !strings.Contains(got, "Nothing was removed besides the background service and the menu-bar icon's start at login.") {
		t.Fatalf("the refusal after the start at login went: %s", got)
	}
	if removedSoFar(false) != "the background service" {
		t.Fatalf("without a start at login: %s", removedSoFar(false))
	}
}

// The menu-bar app's start at login that cannot be removed does not keep a
// plain uninstall from stopping the sync: the service goes, and the start
// at login left is said. --remove-data, which needs the app the job ran
// gone first, stops before the service instead.
func TestIssue254_AStartAtLoginThatStaysStopsUninstallAndSaysSo(t *testing.T) {
	run := &fakeRunner{fail: map[string]error{"launchctl bootout gui/501/" + trayLabel: exitErr(5)}}
	a := installedApp(t, "darwin", run)
	if err := a.svc.installTray(filepath.Join(a.home, "Applications", appBundleName, "Contents", "MacOS", "vaultsync-tray"), true); err != nil {
		t.Fatal(err)
	}
	unit := mustUnitPath(t, a.svc)
	writeFile(t, unit, "installed\n")
	run.loaded = true
	if err := a.uninstall(context.Background(), []string{"--yes"}); err != nil {
		t.Fatalf("plain uninstall: %v", err)
	}
	if out := a.out.(*bytes.Buffer).String(); !strings.Contains(out, "start at login could not be removed") || !strings.Contains(out, "✓ Background service removed") {
		t.Fatalf("plain uninstall said:\n%s", out)
	}
	if _, err := os.Stat(unit); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plain uninstall kept the service: %v", err)
	}
	writeFile(t, unit, "installed\n")
	run.loaded = true
	run.calls = nil
	got := refusalText(a.uninstall(context.Background(), []string{"--remove-data", "--yes"}))
	if !strings.Contains(got, "start at login could not be removed") || !strings.Contains(got, "The background service is not removed") {
		t.Fatalf("got %q", got)
	}
	for _, c := range run.calls {
		if strings.Contains(c, launchdLabel) && !strings.HasPrefix(c, "launchctl print") {
			t.Fatalf("the service was touched: %v", run.calls)
		}
	}
	if !run.loaded {
		t.Fatal("the service is no longer loaded")
	}
	if _, err := os.Stat(unit); err != nil {
		t.Fatalf("the service's file went: %v", err)
	}
}

// The way back into the app (setup from VaultSync's copy beside the same
// build) takes only this account's VaultSync for Mac — one it can replace,
// as the setup link places it — and never a link to a bundle elsewhere.
// Another account's app in /Applications is theirs to update or trash.
func TestIssue254_TheWayBackIntoTheAppTakesOnlyThisAccountsApp(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tmp, "home")
	systemApps := filepath.Join(tmp, "system-applications")
	lay, _ := layoutFor("darwin", home, envOf(nil))
	writeFile(t, lay.Agent, "the agent")
	same, _ := os.ReadFile(lay.Agent)
	holding := func(app string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", "vaultsync"), same, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := macAppEnv(home, systemApps, lay.Agent)

	// A link in ~/Applications to the same build kept elsewhere: not taken.
	elsewhere := fakeMacApp(t, filepath.Join(tmp, "elsewhere"), appBundleName, shippedInfoPlist(t))
	holding(elsewhere)
	if err := os.MkdirAll(filepath.Join(home, "Applications"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(home, "Applications", appBundleName)); err != nil {
		t.Fatal(err)
	}
	if got, ok := installedAppHolding(env, lay.Agent); ok {
		t.Fatalf("a link to a bundle was taken: %+v", got)
	}
	if err := os.Remove(filepath.Join(home, "Applications", appBundleName)); err != nil {
		t.Fatal(err)
	}

	// Another account's app in /Applications holds the same build — owned
	// by them (a test cannot chown, so the look at the owner says so) — and
	// this account has its own in ~/Applications: the service goes into its
	// own.
	theirs := fakeMacApp(t, systemApps, appBundleName, shippedInfoPlist(t))
	holding(theirs)
	mine := fakeMacApp(t, filepath.Join(home, "Applications"), appBundleName, shippedInfoPlist(t))
	holding(mine)
	theirsOwned := func(p string) bool { return p != theirs && ownedByMe(p) }
	env.owned = theirsOwned
	if got, ok := installedAppHolding(env, lay.Agent); !ok || got.Path != mine {
		t.Fatalf("beside another account's app: %+v %v", got, ok)
	}
	// Only theirs: the copy.
	if err := os.RemoveAll(mine); err != nil {
		t.Fatal(err)
	}
	if got, ok := installedAppHolding(env, lay.Agent); ok {
		t.Fatalf("another account's app was taken: %+v", got)
	}
	// Nor is it the app setup runs from.
	fromTheirs := macAppEnv(home, systemApps, filepath.Join(theirs, "Contents", "MacOS", "vaultsync"))
	fromTheirs.owned = theirsOwned
	if got, ok := findInstalledApp(fromTheirs); ok {
		t.Fatalf("setup from another account's app runs the service from it: %+v", got)
	}
}

// The command link into VaultSync for Mac in ~/Applications, itself a link to
// a disk that is gone now, is still setup's: uninstall points it back at the
// copy instead of leaving a command that leads nowhere.
func TestIssue254_TheCommandIntoAnAppsFolderThatIsGoneIsOurs(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tmp, "home")
	disk := filepath.Join(tmp, "disk", "Apps")
	if err := os.MkdirAll(disk, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(disk, filepath.Join(home, "Applications")); err != nil {
		t.Fatal(err)
	}
	lay, _ := layoutFor("darwin", home, envOf(nil))
	a := &app{goos: "darwin", home: home, lay: lay, appEnv: macAppEnv(home, filepath.Join(tmp, "none"), lay.Agent)}
	// Setup linked the agent by its resolved path.
	target := filepath.Join(disk, appBundleName, "Contents", "MacOS", "vaultsync")
	fakeMacApp(t, disk, appBundleName, shippedInfoPlist(t))
	if !a.ownCommandTarget(target) {
		t.Fatal("a link into the app on that disk is setup's")
	}
	if err := os.RemoveAll(filepath.Dir(disk)); err != nil { // the disk is gone
		t.Fatal(err)
	}
	if !a.ownCommandTarget(target) {
		t.Fatal("once the disk is gone, the link into the app on it is no longer setup's")
	}
	if a.ownCommandTarget(filepath.Join(tmp, "other", appBundleName, "Contents", "MacOS", "vaultsync")) {
		t.Fatal("a link into an app elsewhere counts as setup's")
	}
}

// A bootout that fails while the job stays is said in launchctl's words.
func TestIssue254_AFailedBootoutIsSaid(t *testing.T) {
	run := &fakeRunner{fail: map[string]error{"launchctl bootout gui/501/" + trayLabel: exitErr(5)}}
	svc, home := testService(t, "darwin", run)
	if err := svc.installTray(filepath.Join(home, "Applications", appBundleName, "Contents", "MacOS", "vaultsync-tray"), true); err != nil {
		t.Fatal(err)
	}
	_, err := svc.uninstallTray()
	if err == nil || !strings.Contains(err.Error(), "launchctl bootout of the menu-bar app's start at login") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("got %v", err)
	}
	if _, serr := os.Stat(svc.trayPlistPath()); serr != nil {
		t.Fatalf("the file went although the job stayed: %v", serr)
	}
}

// The menu-bar app's log goes with --remove-data, like the service's.
func TestIssue254_RemoveDataTakesTheMenuBarAppsLog(t *testing.T) {
	lay, _ := ownedFixture(t)
	writeFile(t, lay.TrayLogFile(), "tray log")
	removed, _, err := removeOwnData(lay, nil, "darwin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lay.TrayLogFile()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the menu-bar app's log stayed: %v", err)
	}
	found := false
	for _, r := range removed {
		found = found || r == lay.TrayLogFile()
	}
	if !found {
		t.Fatalf("not said as removed: %v", removed)
	}
}

// ownBytes is this test program — what installAgentCopy copies.
func ownBytes(t *testing.T) []byte {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// setupFrom runs setup the way the #254 setup tests do — no terminal, a Hub
// that does not answer — and returns its output and error.
func setupFrom(t *testing.T, a *app, args ...string) (string, error) {
	t.Helper()
	t.Setenv("PATH", "/usr/bin:/bin")
	orig := ttyPath
	ttyPath = filepath.Join(t.TempDir(), "no-tty")
	defer func() { ttyPath = orig }()
	out := &bytes.Buffer{}
	a.out = out
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err := a.setup(ctx, append(args, "--code", "TULIP-ANCHOR-42", "--hub", "127.0.0.1:1"))
	return out.String(), err
}

// An /Applications only an administrator may change does not make the app
// in it someone else's: a standard account that put VaultSync for Mac there
// by hand (Finder asks for an administrator's password) owns the bundle,
// and the service runs from it — trashing it then stops syncing, as the
// docs say. The bundle's own folders decide.
func TestIssue254_AnAppInAnApplicationsOnlyAnAdminMayChangeCounts(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("needs an account that cannot write everywhere")
	}
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tmp, "home")
	systemApps := filepath.Join(tmp, "system-applications")
	app := fakeMacApp(t, systemApps, appBundleName, shippedInfoPlist(t))
	if err := os.Chmod(systemApps, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(systemApps, 0o755) })
	agent := filepath.Join(app, "Contents", "MacOS", "vaultsync")
	if got, ok := findInstalledApp(macAppEnv(home, systemApps, agent)); !ok || got.Path != app {
		t.Fatalf("the app in an /Applications this account may not change: %+v %v", got, ok)
	}
	lay, _ := layoutFor("darwin", home, envOf(nil))
	same, _ := os.ReadFile(agent)
	writeFile(t, lay.Agent, string(same))
	if got, ok := installedAppHolding(macAppEnv(home, systemApps, lay.Agent), lay.Agent); !ok || got.Path != app {
		t.Fatalf("the way back into it: %+v %v", got, ok)
	}
}

// Setup run from a VaultSync for Mac that does not count says so — and why
// — instead of putting the service on VaultSync's own copy in silence: the
// person must know that the app's trip to the Trash will not stop it.
func TestIssue254_SetupSaysWhyTheServiceRunsTheCopy(t *testing.T) {
	a, _, app := macSetupFixture(t)
	downloads := filepath.Join(a.home, "Downloads")
	if err := os.MkdirAll(downloads, 0o755); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(downloads, appBundleName)
	if err := os.Rename(app, moved); err != nil {
		t.Fatal(err)
	}
	a.appEnv = macAppEnv(a.home, filepath.Join(t.TempDir(), "none"), filepath.Join(moved, "Contents", "MacOS", "vaultsync"))
	out, err := setupFrom(t, a)
	if !strings.Contains(refusalText(err)+fmt.Sprint(err), "did not answer") {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	if !strings.Contains(out, "It runs VaultSync's own copy, not VaultSync for Mac in ~/Downloads: it is not in Applications or ~/Applications.") {
		t.Fatalf("the copy was chosen without a word:\n%s", out)
	}
	if prog, _ := plistFirstString(mustRead(t, mustUnitPath(t, a.svc)), "ProgramArguments"); prog != a.lay.Agent {
		t.Fatalf("the service runs %q", prog)
	}
	// Installed, but another account's: said too.
	if err := os.Rename(moved, app); err != nil {
		t.Fatal(err)
	}
	a.appEnv = macAppEnv(a.home, filepath.Join(t.TempDir(), "none"), filepath.Join(app, "Contents", "MacOS", "vaultsync"))
	a.appEnv.owned = func(p string) bool { return p != app && ownedByMe(p) }
	out, _ = setupFrom(t, a)
	if !strings.Contains(out, "It runs VaultSync's own copy, not VaultSync for Mac in ~/Applications: it belongs to another account.") {
		t.Fatalf("another account's app was passed over without a word:\n%s", out)
	}
	// Locked in the Finder, the app is still its owner's: the service runs
	// from it, and nothing is said.
	if os.Geteuid() != 0 {
		a.appEnv.owned = ownedByMe
		locked := []string{filepath.Join(app, "Contents", "MacOS"), filepath.Join(app, "Contents"), app}
		for _, d := range locked {
			if err := os.Chmod(d, 0o555); err != nil {
				t.Fatal(err)
			}
		}
		out, _ = setupFrom(t, a)
		for _, d := range locked {
			_ = os.Chmod(d, 0o755)
		}
		if prog, _ := plistFirstString(mustRead(t, mustUnitPath(t, a.svc)), "ProgramArguments"); prog != filepath.Join(app, "Contents", "MacOS", "vaultsync") || strings.Contains(out, "It runs VaultSync's own copy") {
			t.Fatalf("a locked app of this account's was passed over: %q\n%s", prog, out)
		}
	}
	// Run from the copy, with no app to speak of: nothing to say.
	a.appEnv = macAppEnv(a.home, filepath.Join(t.TempDir(), "none"), a.lay.Agent)
	if out, _ := setupFrom(t, a); strings.Contains(out, "It runs VaultSync's own copy") {
		t.Fatalf("setup from the copy explained itself:\n%s", out)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// A bundle whose Contents (or MacOS) is a link to elsewhere is not
// VaultSync for Mac: the way back does not take it.
func TestIssue254_ABundleWithALinkInsideIsNotTheApp(t *testing.T) {
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(tmp, "home")
	lay, _ := layoutFor("darwin", home, envOf(nil))
	elsewhere := fakeMacApp(t, filepath.Join(tmp, "elsewhere"), appBundleName, shippedInfoPlist(t))
	app := filepath.Join(home, "Applications", appBundleName)
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(elsewhere, "Contents"), filepath.Join(app, "Contents")); err != nil {
		t.Fatal(err)
	}
	same, _ := os.ReadFile(filepath.Join(elsewhere, "Contents", "MacOS", "vaultsync"))
	writeFile(t, lay.Agent, string(same))
	if got, ok := installedAppHolding(macAppEnv(home, filepath.Join(tmp, "none"), lay.Agent), lay.Agent); ok {
		t.Fatalf("a bundle with its Contents elsewhere was taken: %+v", got)
	}
	if _, why := checkApp(macAppEnv(home, filepath.Join(tmp, "none"), lay.Agent), app); !strings.Contains(why, "link") {
		t.Fatalf("the reason: %q", why)
	}
}

// uninstall run from the app — the `vaultsync` command setup pointed into
// it — brings VaultSync's copy to the app's build before it points the
// command back at the copy: an update dragged over in the Finder had no
// setup to do it, and the way back must not run an older build. Run from
// anywhere else, it leaves the copy as it is.
func TestIssue254_UninstallFromTheAppBringsTheCopyToItsBuild(t *testing.T) {
	a, _, app := macSetupFixture(t)
	writeFile(t, mustUnitPath(t, a.svc), "installed\n")
	writeFile(t, a.lay.Agent, "the build before")
	if err := os.Chmod(a.lay.Agent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := a.uninstall(context.Background(), []string{"--yes"}); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if have, _ := os.ReadFile(a.lay.Agent); !bytes.Equal(have, ownBytes(t)) {
		t.Fatal("uninstall from the app left the copy at another build than the app's")
	}
	writeFile(t, mustUnitPath(t, a.svc), "installed\n")
	writeFile(t, a.lay.Agent, "the build before")
	a.appEnv = macAppEnv(a.home, filepath.Join(t.TempDir(), "none"), a.lay.Agent)
	if err := a.uninstall(context.Background(), []string{"--yes"}); err != nil {
		t.Fatalf("uninstall from the copy: %v", err)
	}
	if have, _ := os.ReadFile(a.lay.Agent); string(have) != "the build before" {
		t.Fatalf("uninstall from the copy changed it (app %s)", app)
	}
}

// A setup that fails before its engine is in place leaves the copy alone:
// on Linux, or a Mac without the app, the copy is the program the service
// runs, and a build whose engine never arrived would not start at the next
// login (main replaced the copy only after the engine, too).
func TestIssue254_ASetupThatFailsBeforeTheEngineLeavesTheCopy(t *testing.T) {
	a, _, _ := macSetupFixture(t)
	a.appEnv = appEnv{}
	writeFile(t, a.lay.Agent, "the build that runs")
	// agent.json from a newer VaultSync: loadState refuses it.
	writeFile(t, a.lay.State, `{"version": 2}`)
	if _, err := setupFrom(t, a); err == nil || !strings.Contains(err.Error(), "another VaultSync version") {
		t.Fatalf("setup with a newer agent.json: %v", err)
	}
	if have, _ := os.ReadFile(a.lay.Agent); string(have) != "the build that runs" {
		t.Fatal("a setup that stopped before the engine replaced the copy")
	}
}

// setup --no-service from the copy also removes a start at login whose app
// is gone: it does not depend on the service.
func TestIssue254_SetupWithoutTheServiceDropsAStaleStartAtLogin(t *testing.T) {
	a, run, app := macSetupFixture(t)
	if err := a.svc.installTray(filepath.Join(app, "Contents", "MacOS", "vaultsync-tray"), true); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(app); err != nil {
		t.Fatal(err)
	}
	a.appEnv = macAppEnv(a.home, filepath.Join(t.TempDir(), "none"), a.lay.Agent)
	out, _ := setupFrom(t, a, "--no-service")
	if _, err := os.Stat(a.svc.trayPlistPath()); !errors.Is(err, os.ErrNotExist) || run.trayLoaded || !strings.Contains(out, "no longer opens at login") {
		t.Fatalf("the start at login of a trashed app stayed: %v loaded=%v\n%s", err, run.trayLoaded, out)
	}
}

// What setup notes about the jobs it started never makes VaultSync's folder
// or agent.json: an uninstall --remove-data that finished meanwhile stays
// finished.
func TestIssue254_TheNoteOfWhatStartedMakesNothing(t *testing.T) {
	home := t.TempDir()
	lay, _ := layoutFor("darwin", home, envOf(nil))
	if err := updateStateIfSetUp(lay.State, func(s *agentState) { s.Started.Tray = "x" }); err == nil {
		t.Fatal("a note without agent.json was written")
	}
	if _, err := os.Lstat(lay.Base); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("VaultSync's folder came back: %v", err)
	}
	if err := saveState(lay.State, agentState{GUIPort: 1}); err != nil {
		t.Fatal(err)
	}
	if err := updateStateIfSetUp(lay.State, func(s *agentState) { s.Started.Tray = "x" }); err != nil {
		t.Fatal(err)
	}
	if st, _ := loadState(lay.State); st.Started.Tray != "x" || st.GUIPort != 1 {
		t.Fatalf("the note: %+v", st)
	}
}

// Only the service that runs VaultSync for Mac's agent names the app: one on
// VaultSync's own copy is not the app's — its trip to the Trash does not
// stop it — and must not be listed as "VaultSync for Mac".
func TestIssue254_OnlyTheAppsServiceNamesTheApp(t *testing.T) {
	svc, home := testService(t, "darwin", &fakeRunner{})
	data, err := svc.unitFile(svc.lay.Agent)
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := plistString(data, "AssociatedBundleIdentifiers"); ok {
		t.Fatalf("the copy's service names %q:\n%s", id, data)
	}
	data, err = svc.unitFile(filepath.Join(home, "Applications", appBundleName, "Contents", "MacOS", "vaultsync"))
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := plistString(data, "AssociatedBundleIdentifiers"); !ok || id != appBundleID {
		t.Fatalf("the app's service names %q %v", id, ok)
	}
}

// The bare agent of a release and the one in VaultSync for Mac are signed
// apart, so their bytes differ (the signatures' time stamps) while their
// code directory — what the cdhash is the hash of — is the same: one build.
// Another code directory is another build.
func TestIssue254_TheBareAgentOfTheAppsBuildIsTheSameBuild(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("needs a signed Mach-O program: the test binary on a Mac")
	}
	self, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	cd, ok := codeDirectory(self)
	if !ok {
		t.Skip("this test binary carries no code signature")
	}
	signedApart := append(bytes.Clone(self), 0) // other bytes, the same code directory
	if !sameBuild(self, signedApart) {
		t.Fatal("one build signed apart counted as another")
	}
	at := bytes.Index(self, cd)
	another := bytes.Clone(self)
	another[at+len(cd)-1] ^= 0xff // a page hash of other code
	if sameBuild(self, another) {
		t.Fatal("another code directory counted as the same build")
	}
	if sameBuild([]byte("#!/bin/sh\necho a\n"), []byte("#!/bin/sh\necho b\n")) || !sameBuild([]byte("x"), []byte("x")) {
		t.Fatal("programs without a signature are one build only byte for byte")
	}
}

// A run started through the vaultsync command — the link setup points into
// the app — is the app's run, and watched like one.
func TestIssue254_ARunThroughTheCommandIsWatched(t *testing.T) {
	agent := "/Applications/" + appBundleName + "/Contents/MacOS/vaultsync"
	env := appEnv{goos: "darwin",
		executable: func() (string, error) { return "/Users/me/.local/bin/vaultsync", nil },
		resolve: func(p string) (string, error) {
			if p == "/Users/me/.local/bin/vaultsync" {
				return agent, nil
			}
			return p, nil
		},
		lstat: func(p string) (fs.FileInfo, error) {
			if p != agent {
				t.Errorf("watched %s", p)
			}
			return nil, fs.ErrNotExist
		}}
	every, grace := appCheckEvery, appGoneGrace
	appCheckEvery, appGoneGrace = time.Millisecond, time.Millisecond
	defer func() { appCheckEvery, appGoneGrace = every, grace }()
	watched, gone, stop := watchOwnApp(context.Background(), env)
	defer stop()
	select {
	case <-watched.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a run through the command was not watched")
	}
	if !gone() {
		t.Fatal("not said to be the app's departure")
	}
}

// The look at the app fails closed: anything but the program there — no
// access, a file where the bundle was, a folder — counts as missing.
func TestIssue254_TheWatchFailsClosed(t *testing.T) {
	dir, err := os.Lstat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, look := range map[string]func(string) (fs.FileInfo, error){
		"no access":           func(string) (fs.FileInfo, error) { return nil, fs.ErrPermission },
		"a file in the way":   func(string) (fs.FileInfo, error) { return nil, syscall.ENOTDIR },
		"a folder in its way": func(string) (fs.FileInfo, error) { return dir, nil },
	} {
		watched, gone, stop := whileProgramStays(context.Background(), "/Applications/"+appBundleName+"/Contents/MacOS/vaultsync", look, time.Millisecond, time.Millisecond)
		select {
		case <-watched.Done():
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: the watch went on", name)
		}
		if !gone() {
			t.Fatalf("%s: not said to be the app's departure", name)
		}
		stop()
	}
	if !programMissing(syscall.ENOTDIR) || !programMissing(fs.ErrNotExist) || programMissing(fs.ErrPermission) {
		t.Fatal("programMissing: a file in the way and nothing there are missing; no access is not")
	}
}

// A definition launchd still runs although its file names another program
// — the setup that rewrote the file could not reload it (its bootout
// failed) — is loaded afresh by the next setup, not merely restarted: a
// kickstart would run the old program on and record the new one.
func TestIssue254_AStaleLoadedDefinitionIsLoadedAfresh(t *testing.T) {
	run := &fakeRunner{}
	svc, home := testService(t, "darwin", run)
	copyProg := svc.lay.Agent
	appProg := filepath.Join(home, "Applications", appBundleName, "Contents", "MacOS", "vaultsync")
	if _, err := svc.install(copyProg, false); err != nil {
		t.Fatal(err)
	}
	run.fail = map[string]error{"launchctl bootout gui/501/" + launchdLabel: exitErr(5)}
	if _, err := svc.install(appProg, true); err == nil {
		t.Fatal("a failed bootout must stop the move")
	}
	if run.loadedProgram != copyProg {
		t.Fatalf("launchd's definition: %q", run.loadedProgram)
	}
	run.fail = nil
	run.calls = nil
	if _, err := svc.install(appProg, true); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(run.calls, "launchctl bootout gui/501/"+launchdLabel) || run.loadedProgram != appProg {
		t.Fatalf("the stale definition was not loaded afresh: %q %v", run.loadedProgram, run.calls)
	}
}

// uninstall run from the app where VaultSync is not set up makes no copy —
// and no folder for it.
func TestIssue254_UninstallWhereNothingIsSetUpMakesNoCopy(t *testing.T) {
	a, _, _ := macSetupFixture(t)
	if err := os.RemoveAll(a.lay.Base); err != nil {
		t.Fatal(err)
	}
	_ = a.uninstall(context.Background(), []string{"--yes"})
	if _, err := os.Lstat(a.lay.Base); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("uninstall made VaultSync's folder: %v", err)
	}
}

// A copy that cannot be brought to the app's build keeps the vaultsync
// command on the app, and uninstall says so: the way back must not run an
// older build.
func TestIssue254_UninstallKeepsTheCommandOnTheAppWhenTheCopyCannotFollow(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	a, _, app := macSetupFixture(t)
	writeFile(t, mustUnitPath(t, a.svc), "installed\n")
	writeFile(t, a.lay.Agent, "the build before")
	link := commandLink(a.home)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	agent := filepath.Join(app, "Contents", "MacOS", "vaultsync")
	if err := os.Symlink(agent, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(a.lay.Bin, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(a.lay.Bin, 0o755) })
	if err := a.uninstall(context.Background(), []string{"--yes"}); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if out := a.out.(*bytes.Buffer).String(); !strings.Contains(out, "could not be brought to this build") {
		t.Fatalf("not said:\n%s", out)
	}
	if target, _ := os.Readlink(link); target != agent {
		t.Fatalf("the command went to the older copy: %q", target)
	}
}

// setup --no-service run from the app also removes a start at login whose
// program is gone (the app moved from one Applications folder to the other).
func TestIssue254_SetupWithoutTheServiceFromTheAppDropsAStaleStartAtLogin(t *testing.T) {
	a, run, _ := macSetupFixture(t)
	gone := filepath.Join(t.TempDir(), "Applications", appBundleName, "Contents", "MacOS", "vaultsync-tray")
	if err := a.svc.installTray(gone, true); err != nil {
		t.Fatal(err)
	}
	out, _ := setupFrom(t, a, "--no-service")
	if _, err := os.Stat(a.svc.trayPlistPath()); !errors.Is(err, os.ErrNotExist) || run.trayLoaded || !strings.Contains(out, "no longer opens at login") {
		t.Fatalf("a start at login whose program is gone stayed: %v\n%s", err, out)
	}
}

// The vaultsync command follows the service before the engine is waited
// for: a setup from the copy after the app went to the Trash that stops
// there leaves no command into the Trash.
func TestIssue254_TheCommandFollowsBeforeTheEngineIsWaitedFor(t *testing.T) {
	a, _, app := macSetupFixture(t)
	link := commandLink(a.home)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(app, "Contents", "MacOS", "vaultsync"), link); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(app); err != nil {
		t.Fatal(err)
	}
	a.appEnv = macAppEnv(a.home, filepath.Join(t.TempDir(), "none"), a.lay.Agent)
	// An engine that does not answer: its port free, nothing listening.
	port, err := freeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	if err := updateState(a.lay.State, func(s *agentState) { s.GUIPort = port }); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(a.lay.Home, "config.xml"), "<configuration><gui><address>127.0.0.1:"+strconv.Itoa(port)+"</address><apikey>engine-key</apikey></gui></configuration>\n")
	t.Setenv("PATH", "/usr/bin:/bin")
	orig := ttyPath
	ttyPath = filepath.Join(t.TempDir(), "no-tty")
	defer func() { ttyPath = orig }()
	a.out = &bytes.Buffer{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := a.setup(ctx, []string{"--code", "TULIP-ANCHOR-42", "--hub", "127.0.0.1:1"}); err == nil {
		t.Fatal("setup with an engine that does not answer must fail")
	}
	if target, _ := os.Readlink(link); target != a.lay.Agent {
		t.Fatalf("the command still leads into the Trash: %q", target)
	}
}

// The menu-bar app opens at login only for a service that runs from the
// app: when setup puts the service on the copy — beside a VaultSync for Mac
// that no longer counts (another account's now), whose menu-bar app is
// still there — the start at login goes too, and setup says so; one that
// cannot be removed is said as well.
func TestIssue254_TheStartAtLoginGoesWhenTheServiceRunsTheCopy(t *testing.T) {
	a, run, app := macSetupFixture(t)
	if out, err := setupFrom(t, a); !strings.Contains(refusalText(err)+fmt.Sprint(err), "did not answer") || !run.trayLoaded {
		t.Fatalf("setup from the app: %v\n%s", err, out)
	}
	a.appEnv = macAppEnv(a.home, filepath.Join(t.TempDir(), "none"), a.lay.Agent)
	a.appEnv.owned = func(p string) bool { return p != app && ownedByMe(p) } // now another account's
	out, _ := setupFrom(t, a)
	if prog, _ := plistFirstString(mustRead(t, mustUnitPath(t, a.svc)), "ProgramArguments"); prog != a.lay.Agent {
		t.Fatalf("the service runs %q", prog)
	}
	if _, err := os.Stat(a.svc.trayPlistPath()); !errors.Is(err, os.ErrNotExist) || run.trayLoaded || !strings.Contains(out, "does not run from VaultSync for Mac") {
		t.Fatalf("the start at login stayed beside a service on the copy: %v loaded=%v\n%s", err, run.trayLoaded, out)
	}
	// A start at login that cannot be removed is said, not passed over.
	if err := a.svc.installTray(filepath.Join(app, "Contents", "MacOS", "vaultsync-tray"), true); err != nil {
		t.Fatal(err)
	}
	run.fail = map[string]error{"launchctl bootout gui/501/" + trayLabel: exitErr(5)}
	out, _ = setupFrom(t, a)
	run.fail = nil
	if !strings.Contains(out, "start at login could not be removed") {
		t.Fatalf("a start at login left beside a service on the copy was not said:\n%s", out)
	}
}

// The look at the owner is a real one: what this account made is its own,
// the root folder (root's) is not.
func TestIssue254_OwnershipIsTheOwners(t *testing.T) {
	if !ownedByMe(t.TempDir()) {
		t.Fatal("a folder this account made is not its own")
	}
	if os.Geteuid() != 0 && ownedByMe("/") {
		t.Fatal("the root folder counted as this account's")
	}
	if ownedByMe(filepath.Join(t.TempDir(), "nothing here")) {
		t.Fatal("nothing at all counted as this account's")
	}
}

// A service on VaultSync's own copy beside a VaultSync for Mac that counts —
// an older vaultsync (desktop-v0.1.0 knows no app) set it up so, or the app
// came after the setup — is said by status, with the way into the app: the
// app's trip to the Trash would not stop it. Not said when the service runs
// from the app, nor without an app.
func TestIssue254_StatusSaysWhenTheServiceRunsTheCopyBesideTheApp(t *testing.T) {
	run := &fakeRunner{loaded: true}
	a := installedApp(t, "darwin", run)
	app := fakeMacApp(t, filepath.Join(a.home, "Applications"), appBundleName, shippedInfoPlist(t))
	a.appEnv = macAppEnv(a.home, filepath.Join(t.TempDir(), "none"), a.lay.Agent)
	writeFile(t, a.lay.Agent, "agent")
	data, err := a.svc.unitFile(a.lay.Agent)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, mustUnitPath(t, a.svc), string(data))
	resolved, err := filepath.EvalSymlinks(app) // as the look at Applications finds it
	if err != nil {
		t.Fatal(err)
	}
	want := "It runs VaultSync's own copy, not VaultSync for Mac in " + tildePath(a.home, filepath.Dir(resolved)) + " — moving the app to the Trash will not stop it."
	if out := statusText(t, a); !strings.Contains(out, want) || !strings.Contains(out, shellPath(a.home, filepath.Join(resolved, "Contents", "MacOS", "vaultsync"))+" setup.") {
		t.Fatalf("status beside the app:\n%s", out)
	}
	data, err = a.svc.unitFile(filepath.Join(app, "Contents", "MacOS", "vaultsync"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, mustUnitPath(t, a.svc), string(data))
	if out := statusText(t, a); strings.Contains(out, "It runs VaultSync's own copy") {
		t.Fatalf("status with the service in the app:\n%s", out)
	}
	data, _ = a.svc.unitFile(a.lay.Agent)
	writeFile(t, mustUnitPath(t, a.svc), string(data))
	if err := os.RemoveAll(app); err != nil {
		t.Fatal(err)
	}
	if out := statusText(t, a); strings.Contains(out, "It runs VaultSync's own copy") {
		t.Fatalf("status without an app:\n%s", out)
	}
}

// status and start say that a parked service runs but does not sync, and
// the way out — with the app installed (sync from the app) or gone again
// (go on without it, or use it again) — never "running again".
func TestIssue254_StatusAndStartSayAParkedServiceDoesNotSync(t *testing.T) {
	run := &fakeRunner{loaded: true}
	a := installedApp(t, "darwin", run)
	app := fakeMacApp(t, filepath.Join(a.home, "Applications"), appBundleName, shippedInfoPlist(t))
	a.appEnv = macAppEnv(a.home, filepath.Join(t.TempDir(), "none"), a.lay.Agent)
	data, err := a.svc.unitFile(a.lay.Agent)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, mustUnitPath(t, a.svc), string(data))
	if err := updateState(a.lay.State, func(s *agentState) { s.ParkedBesideApp = app }); err != nil {
		t.Fatal(err)
	}
	if out := statusText(t, a); !strings.Contains(out, "It does not sync: it runs VaultSync's own copy, and VaultSync for Mac is installed in") || strings.Contains(out, "moving the app to the Trash will not stop it") {
		t.Fatalf("status of a parked service beside the app:\n%s", out)
	}
	run.loaded = false
	if err := a.startService(); !strings.Contains(refusalText(err), "runs, but it does not sync") {
		t.Fatalf("start of a parked service: %v", err)
	}
	if err := os.RemoveAll(app); err != nil {
		t.Fatal(err)
	}
	if out := statusText(t, a); !strings.Contains(out, "It does not sync: it stopped when it found VaultSync for Mac installed. Run "+shellPath(a.home, a.lay.Agent)+" setup to go on without the app") {
		t.Fatalf("status of a parked service without the app:\n%s", out)
	}
}

// A setup sets the service up anew and ends a park: the service now runs
// from the app, or the person chose the copy with no app.
func TestIssue254_SetupEndsAPark(t *testing.T) {
	a, _, _ := macSetupFixture(t)
	if err := updateState(a.lay.State, func(s *agentState) { s.ParkedBesideApp = "/Applications/" + appBundleName }); err != nil {
		t.Fatal(err)
	}
	if out, err := setupFrom(t, a); !strings.Contains(refusalText(err)+fmt.Sprint(err), "did not answer") {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	if st, _ := loadState(a.lay.State); st.ParkedBesideApp != "" {
		t.Fatalf("setup left the park: %q", st.ParkedBesideApp)
	}
}
