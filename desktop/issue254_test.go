package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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
	if got := strings.Join(run.calls, "|"); got != "launchctl print "+target+"|launchctl print-disabled gui/501|launchctl bootstrap gui/501 "+plist {
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
	if err := svc.installTray(tray, false); err != nil || strings.Join(run.calls, "|") != "launchctl print "+target+"|launchctl print-disabled gui/501" || !run.trayIdle {
		t.Fatalf("second install: %v %v idle=%v", err, run.calls, run.trayIdle)
	}
	// A new app (a new agent came with it): restarted — and started again.
	run.calls = nil
	if err := svc.installTray(tray, true); err != nil || strings.Join(run.calls, "|") != "launchctl print "+target+"|launchctl print-disabled gui/501|launchctl kickstart -k "+target || run.trayIdle {
		t.Fatalf("new app: %v %v idle=%v", err, run.calls, run.trayIdle)
	}
	// The app moved: the job is loaded afresh with the new file.
	run.calls = nil
	moved := filepath.Join("/Applications", appBundleName, "Contents", "MacOS", "vaultsync-tray")
	if err := svc.installTray(moved, false); err != nil ||
		strings.Join(run.calls, "|") != "launchctl print "+target+"|launchctl print-disabled gui/501|launchctl bootout "+target+"|launchctl print "+target+"|launchctl bootstrap gui/501 "+plist {
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
		if !errors.Is(err, errTrayDisabled) || strings.Join(run.calls, "|") != "launchctl print "+target+"|launchctl print-disabled gui/501" || run.trayLoaded != loaded {
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
	a := &app{goos: "darwin", home: home, lay: lay}
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
