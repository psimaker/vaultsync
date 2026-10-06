package main

import (
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner records the service manager's commands; loaded says whether
// `launchctl print` finds the job.
type fakeRunner struct {
	calls  []string
	loaded bool
	fail   map[string]error
	// answers holds the output of a command (by its full text).
	answers map[string]string
}

func (f *fakeRunner) run(name string, args ...string) (string, error) {
	call := name + " " + strings.Join(args, " ")
	if call == "systemctl --user show-environment" {
		// Asked for the manager's environment; not an action worth asserting.
		return f.answers[call], nil
	}
	f.calls = append(f.calls, call)
	for prefix, err := range f.fail {
		if strings.HasPrefix(call, prefix) {
			return "boom", err
		}
	}
	switch {
	case strings.HasPrefix(call, "launchctl print"):
		if !f.loaded {
			return "", errors.New("not loaded")
		}
	case strings.HasPrefix(call, "launchctl bootstrap"):
		f.loaded = true
	case strings.HasPrefix(call, "launchctl bootout"):
		f.loaded = false
	}
	return "", nil
}

func testService(t *testing.T, goos string, run runner) (service, string) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home with space")
	lay, err := layoutFor(goos, home, envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	return service{goos: goos, home: home, uid: 501, getenv: envOf(nil), lay: lay, run: run}, home
}

func TestIssue175_LaunchAgentFile(t *testing.T) {
	svc, home := testService(t, "darwin", &fakeRunner{})
	exe := filepath.Join(home, "Library", "Application Support", "VaultSync", "bin", "vaultsync")
	data, err := svc.unitFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	// A well-formed plist whose program is the agent's own copy, kept alive.
	var plist struct {
		Dict struct {
			Inner []byte `xml:",innerxml"`
		} `xml:"dict"`
	}
	if err := xml.Unmarshal(data, &plist); err != nil {
		t.Fatalf("not XML: %v\n%s", err, data)
	}
	text := string(data)
	for _, want := range []string{
		"<key>Label</key>\n\t<string>eu.vaultsync.agent</string>",
		"<string>" + exe + "</string>\n\t\t<string>run</string>\n\t\t<string>--state-dir</string>\n\t\t<string>" + svc.lay.Base + "</string>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<key>KeepAlive</key>\n\t<true/>",
		"<string>" + filepath.Join(home, "Library", "Logs", "VaultSync", "vaultsync.log") + "</string>",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("plist lacks %q:\n%s", want, text)
		}
	}
	// XML-special characters in a path are escaped, not interpreted.
	odd, err := svc.unitFile("/Users/a&b/<x>/vaultsync")
	if err != nil || !strings.Contains(string(odd), "<string>/Users/a&amp;b/&lt;x&gt;/vaultsync</string>") {
		t.Fatalf("escaping: %v\n%s", err, odd)
	}
	if _, err := svc.unitFile("/tmp/bad\npath"); err == nil {
		t.Fatal("a path with a newline must be refused")
	}
	if p, _ := svc.unitPath(); p != filepath.Join(home, "Library", "LaunchAgents", "eu.vaultsync.agent.plist") {
		t.Fatalf("plist path: %s", p)
	}
}

func TestIssue175_SystemdUserUnitFile(t *testing.T) {
	svc, home := testService(t, "linux", &fakeRunner{})
	exe := filepath.Join(home, ".local", "state", "vaultsync", "bin", "vaultsync")
	data, err := svc.unitFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"ExecStart=\"" + exe + "\" run --state-dir \"" + svc.lay.Base + "\"\n",
		"Restart=always\n",
		"RestartSec=10\n",
		"WantedBy=default.target\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("unit lacks %q:\n%s", want, text)
		}
	}
	// systemd would expand % specifiers and $ variables in ExecStart.
	if got := systemdQuote(`/home/100%/$HOME/a"b\c`); got != `"/home/100%%/$$HOME/a\"b\\c"` {
		t.Fatalf("systemdQuote: %s", got)
	}
	if p, _ := svc.unitPath(); p != filepath.Join(home, ".config", "systemd", "user", "vaultsync.service") {
		t.Fatalf("unit path: %s", p)
	}
	// The unit goes where the user manager looks: its XDG_CONFIG_HOME, not
	// the shell's (Codex review of #212).
	xdg := filepath.Join(t.TempDir(), "cfg")
	svc.getenv = envOf(map[string]string{"XDG_CONFIG_HOME": filepath.Join(t.TempDir(), "shell-only")})
	svc.run = &fakeRunner{answers: map[string]string{"systemctl --user show-environment": "HOME=/home/me\nXDG_CONFIG_HOME=" + xdg}}
	if p, _ := svc.unitPath(); p != filepath.Join(xdg, "systemd", "user", "vaultsync.service") {
		t.Fatalf("unit path from the manager's XDG_CONFIG_HOME: %s", p)
	}
	svc.run = &fakeRunner{}
	if p, _ := svc.unitPath(); p != filepath.Join(home, ".config", "systemd", "user", "vaultsync.service") {
		t.Fatalf("unit path without a manager XDG_CONFIG_HOME: %s", p)
	}
}

func TestIssue175_LaunchAgentInstallIsIdempotent(t *testing.T) {
	run := &fakeRunner{}
	svc, _ := testService(t, "darwin", run)
	exe := svc.lay.Agent
	changed, err := svc.install(exe, true)
	if err != nil || !changed {
		t.Fatalf("first install: changed=%v err=%v", changed, err)
	}
	if strings.Join(run.calls, "|") != "launchctl print gui/501/eu.vaultsync.agent|launchctl enable gui/501/eu.vaultsync.agent|launchctl bootstrap gui/501 "+mustUnitPath(t, svc) {
		t.Fatalf("first install ran: %v", run.calls)
	}
	if st, err := os.Stat(svc.lay.Logs); err != nil || !st.IsDir() {
		t.Fatalf("log directory missing: %v", err)
	}

	run.calls = nil
	changed, err = svc.install(exe, false)
	if err != nil || changed || len(run.calls) != 1 || !strings.HasPrefix(run.calls[0], "launchctl print") {
		t.Fatalf("second install must only look: changed=%v err=%v calls=%v", changed, err, run.calls)
	}

	// A new agent binary restarts the job; a changed plist reloads it.
	run.calls = nil
	if _, err := svc.install(exe, true); err != nil || run.calls[len(run.calls)-1] != "launchctl kickstart -k gui/501/eu.vaultsync.agent" {
		t.Fatalf("binary change: %v %v", err, run.calls)
	}
	run.calls = nil
	if _, err := svc.install(exe+"-moved", false); err != nil ||
		strings.Join(run.calls, "|") != "launchctl print gui/501/eu.vaultsync.agent|launchctl bootout gui/501/eu.vaultsync.agent|launchctl enable gui/501/eu.vaultsync.agent|launchctl bootstrap gui/501 "+mustUnitPath(t, svc) {
		t.Fatalf("plist change: %v %v", err, run.calls)
	}

	// stop pauses the job across restarts; start brings it back.
	run.calls = nil
	if err := svc.stop(); err != nil || strings.Join(run.calls, "|") != "launchctl disable gui/501/eu.vaultsync.agent|launchctl print gui/501/eu.vaultsync.agent|launchctl bootout gui/501/eu.vaultsync.agent" {
		t.Fatalf("stop: %v %v", err, run.calls)
	}
	run.calls = nil
	if err := svc.start(); err != nil || strings.Join(run.calls, "|") != "launchctl print gui/501/eu.vaultsync.agent|launchctl enable gui/501/eu.vaultsync.agent|launchctl bootstrap gui/501 "+mustUnitPath(t, svc) {
		t.Fatalf("start: %v %v", err, run.calls)
	}

	// Uninstall stops the job and removes exactly the plist; twice is fine.
	run.calls = nil
	if err := svc.uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mustUnitPath(t, svc)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("plist still there")
	}
	if strings.Join(run.calls, "|") != "launchctl print gui/501/eu.vaultsync.agent|launchctl bootout gui/501/eu.vaultsync.agent" {
		t.Fatalf("uninstall ran: %v", run.calls)
	}
	if err := svc.uninstall(); err != nil {
		t.Fatalf("second uninstall: %v", err)
	}
}

func TestIssue175_SystemdInstallIsIdempotent(t *testing.T) {
	run := &fakeRunner{}
	svc, _ := testService(t, "linux", run)
	exe := svc.lay.Agent
	if _, err := svc.install(exe, true); err != nil {
		t.Fatal(err)
	}
	want := "systemctl --user daemon-reload|systemctl --user enable vaultsync.service|systemctl --user restart vaultsync.service"
	if strings.Join(run.calls, "|") != want {
		t.Fatalf("first install ran: %v", run.calls)
	}
	run.calls = nil
	changed, err := svc.install(exe, false)
	if err != nil || changed || strings.Join(run.calls, "|") != "systemctl --user enable vaultsync.service|systemctl --user start vaultsync.service" {
		t.Fatalf("second install: changed=%v err=%v calls=%v", changed, err, run.calls)
	}
	run.calls = nil
	if err := svc.stop(); err != nil || strings.Join(run.calls, "|") != "systemctl --user disable --now vaultsync.service" {
		t.Fatalf("stop: %v %v", err, run.calls)
	}
	run.calls = nil
	if err := svc.start(); err != nil || strings.Join(run.calls, "|") != "systemctl --user enable --now vaultsync.service" {
		t.Fatalf("start: %v %v", err, run.calls)
	}
	run.calls = nil
	if err := svc.uninstall(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(run.calls, "|") != "systemctl --user disable --now vaultsync.service|systemctl --user daemon-reload|systemctl --user reset-failed vaultsync.service" {
		t.Fatalf("uninstall ran: %v", run.calls)
	}
	if svc.installed() {
		t.Fatal("unit still installed")
	}
	// A failing service manager is reported, not ignored.
	run.fail = map[string]error{"systemctl --user enable": errors.New("Failed to connect to bus")}
	if _, err := svc.install(exe, false); err == nil || !strings.Contains(err.Error(), "enable") {
		t.Fatalf("expected the enable failure, got %v", err)
	}
}

func mustUnitPath(t *testing.T, s service) string {
	t.Helper()
	p, err := s.unitPath()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Codex review of #212, round 2: systemctl show-environment quotes values
// (shell_maybe_quote); a config folder with a space must still be found.
func TestIssue175_ManagerEnvironmentIsUnquoted(t *testing.T) {
	for raw, want := range map[string]string{
		`/home/me/.config`:          `/home/me/.config`,
		`$'/home/me/my config'`:     `/home/me/my config`,
		`$'/home/me/it\'s\x41\101'`: `/home/me/it'sAA`,
		`"/home/me/a \"b\" \$c"`:    `/home/me/a "b" $c`,
	} {
		if got := unquoteShellValue(raw); got != want {
			t.Errorf("unquoteShellValue(%s) = %q, want %q", raw, got, want)
		}
	}
	svc, home := testService(t, "linux", &fakeRunner{answers: map[string]string{
		"systemctl --user show-environment": "LANG=C.UTF-8\nXDG_CONFIG_HOME=$'" + filepath.Join("/srv", "my config") + "'\nHOME=" + "/home/me",
	}})
	_ = home
	if p, _ := svc.unitPath(); p != filepath.Join("/srv", "my config", "systemd", "user", "vaultsync.service") {
		t.Fatalf("unit path: %s", p)
	}
}
