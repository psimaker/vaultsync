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
	"time"
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
	// pause waits between two looks at the service manager (nil: sleep).
	pause func(time.Duration)
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
func (s service) launchdGone() error {
	for i := 0; i < 300; i++ {
		if _, err := s.run.run("launchctl", "print", s.launchdTarget()); err != nil {
			return nil
		}
		s.wait(100 * time.Millisecond)
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
		return launchdPlist(exe, s.lay.Base, filepath.Join(s.lay.Logs, "vaultsync.log")), nil
	case "linux":
		return systemdUnitFile(exe, s.lay.Base), nil
	}
	return nil, fmt.Errorf("no background service on %s yet", s.goos)
}

func launchdPlist(exe, stateDir, logFile string) []byte {
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
		if err := s.launchdGone(); err != nil {
			return err
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
