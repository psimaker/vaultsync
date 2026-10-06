package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// The background service: a launchd LaunchAgent on macOS, a systemd user unit
// on Linux. No root, one file in the user's own service directory, the same
// file on every install (installing twice changes nothing), and uninstalling
// removes exactly that file after stopping the service. The service runs
// `vaultsync run` from the agent's own copy, which owns the engine.

const (
	launchdLabel = "eu.vaultsync.agent"
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
}

func (s service) unitPath() (string, error) {
	switch s.goos {
	case "darwin":
		return filepath.Join(s.home, "Library", "LaunchAgents", launchdLabel+".plist"), nil
	case "linux":
		config := s.getenv("XDG_CONFIG_HOME")
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
	switch s.goos {
	case "darwin":
		return launchdPlist(exe, filepath.Join(s.lay.Logs, "vaultsync.log")), nil
	case "linux":
		return systemdUnitFile(exe), nil
	}
	return nil, fmt.Errorf("no background service on %s yet", s.goos)
}

func launchdPlist(exe, logFile string) []byte {
	esc := func(v string) string {
		var b bytes.Buffer
		_ = xml.EscapeText(&b, []byte(v))
		return b.String()
	}
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + launchdLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + esc(exe) + `</string>
		<string>run</string>
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

// systemdQuote writes one ExecStart argument: double-quoted with C escapes,
// and % and $ doubled so systemd expands neither specifiers nor variables.
func systemdQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`, `$`, `$$`)
	return `"` + r.Replace(s) + `"`
}

func systemdUnitFile(exe string) []byte {
	return []byte(`[Unit]
Description=VaultSync — keeps your Obsidian vaults in sync with your Hub
Documentation=https://github.com/psimaker/vaultsync/blob/main/docs/hub.md

[Service]
ExecStart=` + systemdQuote(exe) + ` run
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
	have, _ := os.ReadFile(path)
	fileChanged := !bytes.Equal(have, want)
	if fileChanged {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false, err
		}
		if s.goos == "darwin" {
			if err := os.MkdirAll(s.lay.Logs, 0o700); err != nil {
				return false, err
			}
		}
		if err := writeFileAtomic(path, want, 0o644); err != nil {
			return false, err
		}
	}
	switch s.goos {
	case "darwin":
		return fileChanged || binaryChanged, s.launchdStart(path, fileChanged, binaryChanged)
	case "linux":
		return fileChanged || binaryChanged, s.systemdStart(fileChanged, binaryChanged)
	}
	return false, nil
}

func (s service) launchdTarget() string { return "gui/" + strconv.Itoa(s.uid) + "/" + launchdLabel }

func (s service) launchdStart(plist string, fileChanged, binaryChanged bool) error {
	_, printErr := s.run.run("launchctl", "print", s.launchdTarget())
	loaded := printErr == nil
	if loaded && fileChanged {
		// A loaded job keeps its old definition until it is booted out.
		if out, err := s.run.run("launchctl", "bootout", s.launchdTarget()); err != nil {
			return fmt.Errorf("launchctl bootout: %v: %s", err, out)
		}
		loaded = false
	}
	if !loaded {
		// enable undoes a `vaultsync stop`; launchd refuses to bootstrap a
		// disabled job.
		if out, err := s.run.run("launchctl", "enable", s.launchdTarget()); err != nil {
			return fmt.Errorf("launchctl enable: %v: %s", err, out)
		}
		if out, err := s.run.run("launchctl", "bootstrap", "gui/"+strconv.Itoa(s.uid), plist); err != nil {
			return fmt.Errorf("launchctl bootstrap: %v: %s", err, out)
		}
		return nil
	}
	if binaryChanged {
		if out, err := s.run.run("launchctl", "kickstart", "-k", s.launchdTarget()); err != nil {
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

// uninstall stops the service and removes its file. Not installed is fine.
func (s service) uninstall() error {
	path, err := s.unitPath()
	if err != nil {
		return err
	}
	switch s.goos {
	case "darwin":
		if _, err := s.run.run("launchctl", "print", s.launchdTarget()); err == nil {
			if out, err := s.run.run("launchctl", "bootout", s.launchdTarget()); err != nil {
				return fmt.Errorf("launchctl bootout: %v: %s", err, out)
			}
		}
	case "linux":
		if _, err := os.Stat(path); err == nil {
			if out, err := s.run.run("systemctl", "--user", "disable", "--now", systemdUnit); err != nil {
				return fmt.Errorf("systemctl --user disable --now: %v: %s", err, out)
			}
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if s.goos == "linux" {
		_, _ = s.run.run("systemctl", "--user", "daemon-reload")
		_, _ = s.run.run("systemctl", "--user", "reset-failed", systemdUnit)
	}
	return nil
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
		return s.launchdStart(path, false, false)
	case "linux":
		if out, err := s.run.run("systemctl", "--user", "enable", "--now", systemdUnit); err != nil {
			return fmt.Errorf("systemctl --user enable --now: %v: %s", err, out)
		}
	}
	return nil
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
