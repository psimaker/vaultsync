package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The background service: a launchd LaunchAgent on macOS, a systemd user unit
// on Linux. No root, one file in the user's own service directory, the same
// file on every install (installing twice changes nothing), and uninstalling
// removes exactly that file after stopping the service. The service runs
// `vaultsync run` — from VaultSync for Mac when setup ran from the app
// installed in Applications (appbundle.go), else from the agent's own copy —
// and owns the engine. On a Mac, setup from the app adds a second
// LaunchAgent that starts the menu-bar app at login (decision 053).

const (
	launchdLabel = "eu.vaultsync.agent"
	trayLabel    = "eu.vaultsync.tray"
	systemdUnit  = "vaultsync.service"
)

// runner executes the service manager's commands; tests record them.
type runner interface {
	run(name string, args ...string) (string, error)
}

type execRunner struct{}

func (execRunner) run(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

type service struct {
	goos   string
	home   string
	uid    int
	getenv func(string) string
	lay    layout
	run    runner
	// pause waits between two looks at the service manager (nil: sleep).
	pause func(time.Duration)
	// stat looks at a path (nil: os.Stat); session's look at the user
	// manager's runtime directory goes through it.
	stat func(string) error
}

func (s service) wait(d time.Duration) {
	if s.pause != nil {
		s.pause(d)
		return
	}
	time.Sleep(d)
}

// launchdGone waits until launchd no longer has the job: it removes a
// booted-out job a moment after `launchctl bootout` returns (100–200 ms on
// macOS 27), and a start or bootstrap in that moment finds a job that is
// about to vanish.
func (s service) launchdGone() error { return s.launchdJobGone(launchdLabel) }

func (s service) launchdJobGone(label string) error {
	for i := 0; i < 300; i++ {
		if _, err := s.run.run("launchctl", "print", s.launchdTargetOf(label)); err != nil {
			return nil
		}
		s.wait(100 * time.Millisecond)
	}
	if label == trayLabel {
		return errors.New("launchd still has the menu-bar app's start at login 30 seconds after stopping it")
	}
	return errors.New("launchd still has the background service 30 seconds after stopping it")
}

// waitRunning gives a started service up to 10 seconds to run.
func (s service) waitRunning() bool {
	for i := 0; i < 40; i++ {
		if s.running() {
			return true
		}
		s.wait(250 * time.Millisecond)
	}
	return false
}

func (s service) unitPath() (string, error) {
	switch s.goos {
	case "darwin":
		return filepath.Join(s.home, "Library", "LaunchAgents", launchdLabel+".plist"), nil
	case "linux":
		// Where the user manager looks, which is its XDG_CONFIG_HOME — not
		// necessarily the one of the shell running setup.
		config := s.managerEnv()["XDG_CONFIG_HOME"]
		if config == "" || !filepath.IsAbs(config) {
			config = filepath.Join(s.home, ".config")
		}
		return filepath.Join(config, "systemd", "user", systemdUnit), nil
	}
	return "", fmt.Errorf("no background service on %s yet", s.goos)
}

// unitFile renders the service definition for the agent binary at exe.
func (s service) unitFile(exe string) ([]byte, error) {
	if strings.ContainsAny(exe, "\n\r\x00") {
		return nil, fmt.Errorf("the path %q cannot be written into a service file", exe)
	}
	if strings.ContainsAny(s.lay.Base, "\n\r\x00") {
		return nil, fmt.Errorf("the path %q cannot be written into a service file", s.lay.Base)
	}
	switch s.goos {
	case "darwin":
		return launchdPlist(exe, s.lay.Base, s.lay.LogFile()), nil
	case "linux":
		return systemdUnitFile(exe, s.lay.Base), nil
	}
	return nil, fmt.Errorf("no background service on %s yet", s.goos)
}

func plistEscape(v string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(v))
	return b.String()
}

// launchdPlist is the background service's LaunchAgent. It names VaultSync
// for Mac as its app (AssociatedBundleIdentifiers): macOS lists it under
// Login Items as "VaultSync for Mac" — the app is signed by the same team —
// instead of under the signing certificate's name, the developer's
// (decision 052 (5)). Where no such app is installed the name stays the
// certificate's, as before.
func launchdPlist(exe, stateDir, logFile string) []byte {
	esc := plistEscape
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + launchdLabel + `</string>
	<key>AssociatedBundleIdentifiers</key>
	<string>` + appBundleID + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + esc(exe) + `</string>
		<string>run</string>
		<string>--state-dir</string>
		<string>` + esc(stateDir) + `</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>StandardOutPath</key>
	<string>` + esc(logFile) + `</string>
	<key>StandardErrorPath</key>
	<string>` + esc(logFile) + `</string>
</dict>
</plist>
`)
}

// trayPlist is the LaunchAgent that starts the menu-bar app at login: its
// program in the bundle, the VaultSync folder the service was set up with.
// KeepAlive only on an unsuccessful exit: a crash starts it again after
// ThrottleInterval, Quit (exit 0) leaves it off until the next login — and
// so does a second instance, which ends with 0 for that reason
// (cmd/vaultsync-tray). Interactive: an app the person uses, not a
// background job macOS may slow down.
func trayPlist(exe, stateDir, logFile string) []byte {
	esc := plistEscape
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + trayLabel + `</string>
	<key>AssociatedBundleIdentifiers</key>
	<string>` + appBundleID + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + esc(exe) + `</string>
		<string>--state-dir</string>
		<string>` + esc(stateDir) + `</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>ProcessType</key>
	<string>Interactive</string>
	<key>StandardOutPath</key>
	<string>` + esc(logFile) + `</string>
	<key>StandardErrorPath</key>
	<string>` + esc(logFile) + `</string>
</dict>
</plist>
`)
}

// systemdQuote writes one ExecStart argument: double-quoted with C escapes,
// and % and $ doubled so systemd expands neither specifiers nor variables.
func systemdQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`, `$`, `$$`)
	return `"` + r.Replace(s) + `"`
}

func systemdUnitFile(exe, stateDir string) []byte {
	return []byte(`[Unit]
Description=VaultSync — keeps your Obsidian vaults in sync with your Hub
Documentation=https://github.com/psimaker/vaultsync/blob/main/docs/hub.md

[Service]
ExecStart=` + systemdQuote(exe) + ` run --state-dir ` + systemdQuote(stateDir) + `
Restart=always
RestartSec=10

[Install]
WantedBy=default.target
`)
}

// install writes the service file and (re)starts the service. Unchanged
// file and binary: nothing is restarted. Returns whether anything changed.
func (s service) install(exe string, binaryChanged bool) (bool, error) {
	path, err := s.unitPath()
	if err != nil {
		return false, err
	}
	want, err := s.unitFile(exe)
	if err != nil {
		return false, err
	}
	fileChanged, err := s.writeServiceFile(path, want)
	if err != nil {
		return false, err
	}
	switch s.goos {
	case "darwin":
		return fileChanged || binaryChanged, s.launchdStart(path, fileChanged, binaryChanged)
	case "linux":
		return fileChanged || binaryChanged, s.systemdStart(fileChanged, binaryChanged)
	}
	return false, nil
}

// writeServiceFile writes a service file unless it already reads want, and
// says whether it wrote it. On a Mac the log folder the file names is made
// first: launchd does not make it.
func (s service) writeServiceFile(path string, want []byte) (bool, error) {
	if have, _ := os.ReadFile(path); bytes.Equal(have, want) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if s.goos == "darwin" {
		if err := os.MkdirAll(s.lay.Logs, 0o700); err != nil {
			return false, err
		}
	}
	return true, writeFileAtomic(path, want, 0o644)
}

// trayPlistPath is the file of the LaunchAgent that starts the menu-bar app.
func (s service) trayPlistPath() string {
	return filepath.Join(s.home, "Library", "LaunchAgents", trayLabel+".plist")
}

// errTrayDisabled: the person turned the menu-bar app's start at login off
// (launchctl disable); setup leaves it so (safety rule 5).
var errTrayDisabled = errors.New("the menu-bar icon's start at login is turned off for this account")

// installTray writes the LaunchAgent that starts the menu-bar app at login
// and starts it the way install starts the service: a new or changed file
// loads it afresh, a new program (binaryChanged: setup installed another
// agent, and the app came with it) restarts it, and nothing changed leaves
// it as it is — off, too, when the person quit it. Unlike the service's,
// a job the person disabled is not enabled again: errTrayDisabled. macOS
// only.
func (s service) installTray(exe string, binaryChanged bool) error {
	if s.goos != "darwin" {
		return fmt.Errorf("no menu-bar app start at login on %s", s.goos)
	}
	for _, p := range []string{exe, s.lay.Base} {
		if strings.ContainsAny(p, "\n\r\x00") {
			return fmt.Errorf("the path %q cannot be written into a service file", p)
		}
	}
	path := s.trayPlistPath()
	fileChanged, err := s.writeServiceFile(path, trayPlist(exe, s.lay.Base, s.lay.TrayLogFile()))
	if err != nil {
		return err
	}
	return s.launchdStartJob(trayLabel, path, fileChanged, binaryChanged)
}

// launchdDisabled: launchd keeps label disabled for this account — the
// person's `launchctl disable`, which outlasts logins and which bootstrap
// refuses. print-disabled says "disabled" (macOS 13 on) or "true".
func (s service) launchdDisabled(label string) bool {
	out, err := s.run.run("launchctl", "print-disabled", "gui/"+strconv.Itoa(s.uid))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), `"`+label+`" =>`); ok {
			v = strings.TrimSpace(v)
			return v == "disabled" || v == "true"
		}
	}
	return false
}

// programGone reads the program the background service's LaunchAgent names
// and says whether it is VaultSync for Mac's agent and gone: the app the
// service runs from (decision 053) moved out of Applications or put in the
// Trash — launchd then cannot start the service. macOS only; a file that
// cannot be read, names nothing, or names VaultSync's own copy is not this
// case.
func (s service) programGone() (prog string, gone bool) {
	if s.goos != "darwin" {
		return "", false
	}
	path, err := s.unitPath()
	if err != nil {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	prog, ok := plistFirstString(data, "ProgramArguments")
	if !ok || !strings.Contains(prog, string(filepath.Separator)+appBundleName+string(filepath.Separator)) {
		return "", false
	}
	_, err = os.Lstat(prog)
	return prog, errors.Is(err, fs.ErrNotExist)
}

// trayProgram is the program the menu-bar app's start at login names, read
// back from the file this agent wrote; "" when there is no such file or it
// names none.
func (s service) trayProgram() string {
	data, err := os.ReadFile(s.trayPlistPath())
	if err != nil {
		return ""
	}
	prog, _ := plistFirstString(data, "ProgramArguments")
	return prog
}

// uninstallTray stops the menu-bar app's start at login and removes its
// file; it says whether there was one. It waits until launchd let go of the
// job — the app lets go of tray.lock as it ends, and a removal of
// VaultSync's files takes that lock next. Like uninstall, it keeps the file
// when the service manager could not be asked about the job.
func (s service) uninstallTray() (found bool, err error) {
	if s.goos != "darwin" {
		return false, nil
	}
	_, statErr := os.Lstat(s.trayPlistPath())
	hasFile := statErr == nil
	if _, err := s.run.run("launchctl", "print", s.launchdTargetOf(trayLabel)); err == nil {
		found = true
		// A job already on its way out (a logout, a setup loading it
		// afresh) answers the bootout with an error: whether launchd let
		// go of it is what counts.
		_, _ = s.run.run("launchctl", "bootout", s.launchdTargetOf(trayLabel))
		if err := s.launchdJobGone(trayLabel); err != nil {
			return found, err
		}
	} else if hasFile {
		// Only a file left to remove makes the session worth asking about:
		// on a Mac that never had the menu-bar app there is nothing here.
		if state, why := s.session(); state == sessionUnknown {
			return false, fmt.Errorf("the service manager could not be asked whether the menu-bar app runs (%s); run vaultsync uninstall from your Mac's own session", why)
		}
	}
	switch err := os.Remove(s.trayPlistPath()); {
	case err == nil:
		found = true
	case !errors.Is(err, os.ErrNotExist):
		return found, err
	}
	return found, nil
}

// trayRunning asks launchd whether the menu-bar app's start at login runs
// the app right now — then that job holds tray.lock (a second instance
// beside it ends at once), and uninstallTray stops it.
func (s service) trayRunning() bool {
	if s.goos != "darwin" {
		return false
	}
	out, err := s.run.run("launchctl", "print", s.launchdTargetOf(trayLabel))
	return err == nil && strings.Contains(out, "state = running")
}

func (s service) launchdTarget() string { return s.launchdTargetOf(launchdLabel) }

func (s service) launchdTargetOf(label string) string {
	return "gui/" + strconv.Itoa(s.uid) + "/" + label
}

func (s service) launchdStart(plist string, fileChanged, binaryChanged bool) error {
	return s.launchdStartJob(launchdLabel, plist, fileChanged, binaryChanged)
}

func (s service) launchdStartJob(label, plist string, fileChanged, binaryChanged bool) error {
	target := s.launchdTargetOf(label)
	_, printErr := s.run.run("launchctl", "print", target)
	loaded := printErr == nil
	if label == trayLabel && s.launchdDisabled(label) {
		// The person turned it off: left as it is, loaded or not; the
		// file written for it waits for their launchctl enable.
		return errTrayDisabled
	}
	if loaded && fileChanged {
		// A loaded job keeps its old definition until it is booted out.
		if out, err := s.run.run("launchctl", "bootout", target); err != nil {
			return fmt.Errorf("launchctl bootout: %v: %s", err, out)
		}
		if err := s.launchdJobGone(label); err != nil {
			return err
		}
		loaded = false
	}
	if !loaded {
		// enable undoes a `vaultsync stop` — the service's only; launchd
		// refuses to bootstrap a disabled job, and the menu-bar app's is
		// disabled only by the person.
		if label != trayLabel {
			if out, err := s.run.run("launchctl", "enable", target); err != nil {
				return fmt.Errorf("launchctl enable: %v: %s", err, out)
			}
		}
		if out, err := s.run.run("launchctl", "bootstrap", "gui/"+strconv.Itoa(s.uid), plist); err != nil {
			return fmt.Errorf("launchctl bootstrap: %v: %s", err, out)
		}
		return nil
	}
	if binaryChanged {
		if out, err := s.run.run("launchctl", "kickstart", "-k", target); err != nil {
			return fmt.Errorf("launchctl kickstart: %v: %s", err, out)
		}
	}
	return nil
}

func (s service) systemdStart(fileChanged, binaryChanged bool) error {
	if fileChanged {
		if out, err := s.run.run("systemctl", "--user", "daemon-reload"); err != nil {
			return fmt.Errorf("systemctl --user daemon-reload: %v: %s", err, out)
		}
	}
	if out, err := s.run.run("systemctl", "--user", "enable", systemdUnit); err != nil {
		return fmt.Errorf("systemctl --user enable: %v: %s", err, out)
	}
	verb := "start" // a no-op for a running unit
	if fileChanged || binaryChanged {
		verb = "restart"
	}
	if out, err := s.run.run("systemctl", "--user", verb, systemdUnit); err != nil {
		return fmt.Errorf("systemctl --user %s: %v: %s", verb, err, out)
	}
	return nil
}

// uninstall stops the service and removes its file, and says whether it
// found one — a loaded job on a Mac; on Linux a unit file, or a unit the
// manager still runs without its file — whose own run may hold the
// engine's lock a moment longer. Not installed is fine.
func (s service) uninstall() (found bool, err error) {
	path, err := s.unitPath()
	if err != nil {
		return false, err
	}
	switch s.goos {
	case "darwin":
		if _, err := s.run.run("launchctl", "print", s.launchdTarget()); err == nil {
			found = true
			if out, err := s.run.run("launchctl", "bootout", s.launchdTarget()); err != nil {
				return found, fmt.Errorf("launchctl bootout: %v: %s", err, out)
			}
		} else if state, why := s.session(); state == sessionUnknown {
			// Not "no job" but "could not ask": removing the file now would
			// leave a job running until the logout, with nothing to stop
			// it by. No session at all is a job nothing runs.
			return false, fmt.Errorf("the service manager could not be asked whether the service runs (%s); run vaultsync uninstall from your Mac's own session", why)
		}
	case "linux":
		// A unit the manager runs without its file (removed by hand, no
		// daemon-reload; a config home that moved) is stopped all the
		// same — by stop, which needs no file; disable (the wants link)
		// then as it may.
		switch _, err := os.Stat(path); {
		case err == nil:
			found = true
			if out, err := s.run.run("systemctl", "--user", "disable", "--now", systemdUnit); err != nil {
				return found, fmt.Errorf("systemctl --user disable --now: %v: %s", err, out)
			}
		case s.unitAlive():
			found = true
			if out, err := s.run.run("systemctl", "--user", "stop", systemdUnit); err != nil {
				return found, fmt.Errorf("systemctl --user stop: %v: %s", err, out)
			}
			_, _ = s.run.run("systemctl", "--user", "disable", systemdUnit)
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return found, err
	}
	if s.goos == "linux" {
		_, _ = s.run.run("systemctl", "--user", "daemon-reload")
		_, _ = s.run.run("systemctl", "--user", "reset-failed", systemdUnit)
	}
	return found, nil
}

// stop pauses the service until start (or setup) — also across a restart of
// the computer. Nothing else changes.
func (s service) stop() error {
	switch s.goos {
	case "darwin":
		if out, err := s.run.run("launchctl", "disable", s.launchdTarget()); err != nil {
			return fmt.Errorf("launchctl disable: %v: %s", err, out)
		}
		if _, err := s.run.run("launchctl", "print", s.launchdTarget()); err == nil {
			if out, err := s.run.run("launchctl", "bootout", s.launchdTarget()); err != nil {
				return fmt.Errorf("launchctl bootout: %v: %s", err, out)
			}
			// Return only once the job is gone, so a start right after this
			// one finds it stopped.
			if err := s.launchdGone(); err != nil {
				return err
			}
		}
	case "linux":
		if out, err := s.run.run("systemctl", "--user", "disable", "--now", systemdUnit); err != nil {
			return fmt.Errorf("systemctl --user disable --now: %v: %s", err, out)
		}
	default:
		return fmt.Errorf("no background service on %s yet", s.goos)
	}
	return nil
}

// start resumes a stopped service.
func (s service) start() error {
	path, err := s.unitPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return errors.New("the background service is not installed — run vaultsync setup")
	}
	switch s.goos {
	case "darwin":
		// enable first: it undoes a stop also when launchd still holds the
		// job, which would otherwise stay disabled — now and at every login.
		if out, err := s.run.run("launchctl", "enable", s.launchdTarget()); err != nil {
			if state, _ := s.session(); state == sessionAbsent {
				return s.waitsForLogin()
			}
			return fmt.Errorf("launchctl enable: %v: %s", err, out)
		}
		if _, err := s.run.run("launchctl", "print", s.launchdTarget()); err == nil {
			// Still loaded: idle, disabled while loaded, or on its way out
			// after a stop that ran out of time — launchd shows all of them
			// alike, so the job is loaded afresh. Its bootout may fail when
			// it is already going; waiting for it to be gone settles both.
			_, _ = s.run.run("launchctl", "bootout", s.launchdTarget())
			if err := s.launchdGone(); err != nil {
				return err
			}
		}
		if out, err := s.run.run("launchctl", "bootstrap", "gui/"+strconv.Itoa(s.uid), path); err != nil {
			return fmt.Errorf("launchctl bootstrap: %v: %s", err, out)
		}
	case "linux":
		if out, err := s.run.run("systemctl", "--user", "enable", "--now", systemdUnit); err != nil {
			if state, _ := s.session(); state == sessionAbsent {
				return s.waitsForLogin()
			}
			return fmt.Errorf("systemctl --user enable --now: %v: %s", err, out)
		}
	}
	return nil
}

// sessionState is what the service manager says about this user's session —
// the one the service runs in.
type sessionState int

const (
	sessionPresent sessionState = iota // the service can run, start and stop
	sessionAbsent                      // no session: nothing runs it, nothing starts it
	sessionUnknown                     // the manager could not be asked from here
)

// session asks the service manager for this user's session. launchd keeps a
// user's gui domain only while they are logged in at the screen, and the
// systemd user manager runs only while the user has a session, unless
// lingering is on: without one the service is neither running nor startable,
// but it was not stopped either (#232). Absent is claimed only on evidence
// from a manager that answered — launchd's own "no domain" answers, or on
// Linux the user manager's runtime directory /run/user/<uid> missing (it
// goes with the last session). Anything else is unknown, with the words
// kept: a command that did not run at all, a shell without the session's
// environment, a permission.
func (s service) session() (sessionState, string) {
	switch s.goos {
	case "darwin":
		out, err := s.run.run("launchctl", "print", "gui/"+strconv.Itoa(s.uid))
		switch {
		case err == nil:
			return sessionPresent, ""
		case strings.Contains(out, "Could not find domain") || strings.Contains(out, "Domain does not support specified action"):
			return sessionAbsent, ""
		}
		return sessionUnknown, firstLine(out, err)
	case "linux":
		out, err := s.run.run("systemctl", "--user", "show-environment")
		if err == nil {
			return sessionPresent, ""
		}
		if !exited(err) {
			return sessionUnknown, firstLine(out, err)
		}
		switch serr := s.statPath(filepath.Join("/run", "user", strconv.Itoa(s.uid))); {
		case errors.Is(serr, fs.ErrNotExist):
			return sessionAbsent, ""
		case serr != nil:
			return sessionUnknown, serr.Error()
		}
		return sessionUnknown, firstLine(out, err)
	}
	return sessionPresent, ""
}

func (s service) statPath(path string) error {
	if s.stat != nil {
		return s.stat(path)
	}
	_, err := os.Stat(path)
	return err
}

// exited reports whether a service manager command ran and ended with an
// exit status — as opposed to never running (not found, not executable),
// which says nothing about the session. launchd needs no such check: its
// words are the evidence, and a command that never ran has none.
func exited(err error) bool {
	var ee interface{ ExitCode() int }
	return errors.As(err, &ee)
}

// firstLine is the manager's first line of output, else the error.
func firstLine(out string, err error) string {
	if line, _, _ := strings.Cut(strings.TrimSpace(out), "\n"); line != "" {
		return line
	}
	return err.Error()
}

// waitsForLoginNote is what status says about a service without a session.
// A service stopped with vaultsync stop stays disabled across the login, so
// the note does not promise that it starts on its own.
func (s service) waitsForLoginNote() string {
	if s.goos == "linux" {
		return "waits for your login — it runs while you are logged in (loginctl enable-linger keeps it running after you log out). Stopped it with vaultsync stop? Then run vaultsync start once you are logged in."
	}
	return "waits for your login — it runs in your desktop session. Stopped it with vaultsync stop? Then run vaultsync start once you are logged in."
}

// waitsForLogin is start's answer without a session: the service manager's
// error would send the user nowhere. The enable that failed may have been
// the one undoing a vaultsync stop, so start is to be run again after the
// login.
func (s service) waitsForLogin() error {
	if s.goos == "linux" {
		return refuse("The background service runs while you are logged in, and no session of yours is running right now. Log in, then run vaultsync start again — and run loginctl enable-linger once to keep it running after you log out.")
	}
	return refuse("The background service runs in your desktop session, and you are not logged in at the screen right now. Log in on this Mac, then run vaultsync start again.")
}

// unitAlive: the Linux manager has the unit in any live state — active,
// activating (an auto-restart on its way), deactivating, reloading —
// as opposed to inactive, failed or unknown. Such a unit is stopped at
// the removal whether or not its file is there.
func (s service) unitAlive() bool {
	out, _ := s.run.run("systemctl", "--user", "is-active", systemdUnit)
	switch strings.TrimSpace(out) {
	case "active", "activating", "deactivating", "reloading", "refreshing":
		return true
	}
	return false
}

// running asks the service manager whether the service is up.
func (s service) running() bool {
	switch s.goos {
	case "darwin":
		out, err := s.run.run("launchctl", "print", s.launchdTarget())
		return err == nil && strings.Contains(out, "state = running")
	case "linux":
		out, err := s.run.run("systemctl", "--user", "is-active", systemdUnit)
		return err == nil && strings.TrimSpace(out) == "active"
	}
	return false
}

// managerEnv is the systemd user manager's environment (empty off Linux or
// when it does not answer).
func (s service) managerEnv() map[string]string {
	env := map[string]string{}
	if s.goos != "linux" {
		return env
	}
	out, err := s.run.run("systemctl", "--user", "show-environment")
	if err != nil {
		return env
	}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			env[k] = unquoteShellValue(v)
		}
	}
	return env
}

// unquoteShellValue reads one value as systemctl show-environment prints it
// (shell_maybe_quote): bare, $'…' with C escapes, or "…" with backslash
// escapes — without ever running a shell.
func unquoteShellValue(v string) string {
	switch {
	case strings.HasPrefix(v, "$'") && strings.HasSuffix(v, "'") && len(v) >= 3:
		return cUnescape(v[2 : len(v)-1])
	case strings.HasPrefix(v, "\"") && strings.HasSuffix(v, "\"") && len(v) >= 2:
		var b strings.Builder
		in := v[1 : len(v)-1]
		for i := 0; i < len(in); i++ {
			if in[i] == '\\' && i+1 < len(in) && strings.IndexByte("\"\\$`\n", in[i+1]) >= 0 {
				i++
			}
			b.WriteByte(in[i])
		}
		return b.String()
	}
	return v
}

// cUnescape resolves the C escapes of a $'…' string.
func cUnescape(in string) string {
	var b strings.Builder
	for i := 0; i < len(in); i++ {
		if in[i] != '\\' || i+1 >= len(in) {
			b.WriteByte(in[i])
			continue
		}
		i++
		switch c := in[i]; c {
		case 'a':
			b.WriteByte('\a')
		case 'b':
			b.WriteByte('\b')
		case 'e', 'E':
			b.WriteByte(0x1b)
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte('\v')
		case 'x':
			j := i + 1
			for j < len(in) && j < i+3 && strings.IndexByte("0123456789abcdefABCDEF", in[j]) >= 0 {
				j++
			}
			if n, err := strconv.ParseUint(in[i+1:j], 16, 8); err == nil && j > i+1 {
				b.WriteByte(byte(n))
				i = j - 1
			} else {
				b.WriteByte('x')
			}
		case '0', '1', '2', '3', '4', '5', '6', '7':
			j := i
			for j < len(in) && j < i+3 && in[j] >= '0' && in[j] <= '7' {
				j++
			}
			n, _ := strconv.ParseUint(in[i:j], 8, 8)
			b.WriteByte(byte(n))
			i = j - 1
		default: // \\ \' \" \? and anything else stand for themselves
			b.WriteByte(c)
		}
	}
	return b.String()
}

// userManagerAvailable checks, before anything is installed, that systemd
// runs a manager for this user (WSL, containers and some minimal systems
// have none).
func (s service) userManagerAvailable() error {
	if s.goos != "linux" {
		return nil
	}
	if out, err := s.run.run("systemctl", "--user", "show-environment"); err != nil {
		return fmt.Errorf("%v: %s", err, out)
	}
	return nil
}

// installed reports whether the service file exists.
func (s service) installed() bool {
	path, err := s.unitPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}
