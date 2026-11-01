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
	"slices"
	"strconv"
	"strings"
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
		readFile: os.ReadFile, lstat: os.Lstat}
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
	if exe, bundle, fromApp := a.serviceProgram(); !fromApp || exe != agentIn(installed) || bundle.Tray == "" {
		t.Fatalf("from the app: %q %+v %v", exe, bundle, fromApp)
	}
	a.appEnv = macAppEnv(home, systemApps, lay.Agent)
	if exe, _, fromApp := a.serviceProgram(); fromApp || exe != lay.Agent {
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

	// The job's app holds it — and lets go as launchd stops it.
	run.trayIdle = false
	jobsApp, err := lockFile(lay.TrayLock())
	if err != nil {
		t.Fatal(err)
	}
	run.onTrayBootout = jobsApp
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

	// A new agent came with the app (the copy differs from the program that
	// runs setup): both jobs are restarted.
	writeFile(t, a.lay.Agent, "an older agent")
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
	// Another agent than the app's decides for itself: the copy.
	writeFile(t, agent, "#!/bin/sh\necho another build\n")
	if err := os.Chmod(agent, 0o755); err != nil {
		t.Fatal(err)
	}
	setup()
	if program(unit) != a.lay.Agent {
		t.Fatalf("setup from another build beside the app: service %q", program(unit))
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
