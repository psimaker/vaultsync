package main

import (
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRunner records the service manager's commands; loaded says whether
// `launchctl print` finds the job.
type fakeRunner struct {
	calls  []string
	loaded bool
	fail   map[string]error
	// answers holds the output of a command (by its full text).
	answers map[string]string
	// Like launchd: a disabled job cannot be bootstrapped; a booted-out job
	// still answers lingerAfterBootout prints before it is gone; an idle job
	// is loaded without running; neverRuns keeps every job idle.
	disabled           bool
	idle, neverRuns    bool
	lingerAfterBootout int
	lingering          int
	// noSession: the user has no session for the service to run in — launchd
	// has no gui domain for them, the systemd user manager is not running
	// (#232). Every launchctl / systemctl call fails the way the real ones do.
	noSession bool
	// probeFails, when set, is what the session probe (the domain print,
	// show-environment) answers while failing for another reason than a
	// missing session.
	probeFails string
}

// launchdUID reads the uid out of a gui/<uid>[/label] target.
func launchdUID(args []string) string {
	for _, a := range args {
		if strings.HasPrefix(a, "gui/") {
			uid, _, _ := strings.Cut(strings.TrimPrefix(a, "gui/"), "/")
			return uid
		}
	}
	return "?"
}

func (f *fakeRunner) run(name string, args ...string) (string, error) {
	call := name + " " + strings.Join(args, " ")
	domainProbe := strings.HasPrefix(call, "launchctl print gui/") && strings.Count(call, "/") == 1
	if call == "systemctl --user show-environment" {
		// Asked for the manager's environment; not an action worth asserting.
		switch {
		case f.noSession:
			return "Failed to connect to bus: No medium found", errors.New("exit status 1")
		case f.probeFails != "":
			return f.probeFails, errors.New("exit status 1")
		}
		return f.answers[call], nil
	}
	f.calls = append(f.calls, call)
	if f.noSession {
		if name == "launchctl" {
			return "Bad request.\nCould not find domain for user gui: " + launchdUID(args), errors.New("exit status 112")
		}
		return "Failed to connect to bus: No medium found", errors.New("exit status 1")
	}
	for prefix, err := range f.fail {
		if strings.HasPrefix(call, prefix) {
			return "boom", err
		}
	}
	switch {
	case domainProbe && f.probeFails != "":
		return f.probeFails, errors.New("exit status 1")
	case domainProbe:
		// The domain itself: there as long as the user has a desktop session.
		return "gui/" + launchdUID(args) + " = {\n", nil
	case strings.HasPrefix(call, "launchctl print"):
		if f.lingering > 0 {
			f.lingering--
			return "\tstate = running\n", nil // still on its way out
		}
		if !f.loaded {
			return "", errors.New("not loaded")
		}
		if f.idle || f.neverRuns {
			return "\tstate = not running\n", nil
		}
		return "\tstate = running\n", nil
	case strings.HasPrefix(call, "launchctl bootstrap"):
		if f.disabled {
			return "Bootstrap failed: 5: Input/output error", errors.New("exit status 5")
		}
		f.loaded, f.idle = true, false
	case strings.HasPrefix(call, "launchctl bootout"):
		switch {
		case f.loaded:
			f.loaded = false
			f.lingering = f.lingerAfterBootout
		case f.lingering > 0:
			// already on its way out; the removal goes on
		default:
			return "Boot-out failed: 3: No such process", errors.New("exit status 3")
		}
	case strings.HasPrefix(call, "launchctl disable"):
		f.disabled = true
	case strings.HasPrefix(call, "launchctl enable"):
		f.disabled = false
	case strings.HasPrefix(call, "launchctl kickstart"):
		if !f.loaded || f.disabled {
			return "", errors.New("cannot kickstart")
		}
		f.idle = false
	}
	// A configured answer (systemctl is-active, for one) stands for the
	// rest; anything else succeeds silently.
	return f.answers[call], nil
}

func testService(t *testing.T, goos string, run runner) (service, string) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home with space")
	lay, err := layoutFor(goos, home, envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	svc := service{goos: goos, home: home, uid: 501, getenv: envOf(nil), lay: lay, run: run, pause: func(time.Duration) {}}
	if f, ok := run.(*fakeRunner); ok {
		// The user manager's runtime directory is there exactly while the
		// manager runs.
		svc.exists = func(string) bool { return !f.noSession }
	}
	return svc, home
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
	// the shell's.
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
		strings.Join(run.calls, "|") != "launchctl print gui/501/eu.vaultsync.agent|launchctl bootout gui/501/eu.vaultsync.agent|launchctl print gui/501/eu.vaultsync.agent|launchctl enable gui/501/eu.vaultsync.agent|launchctl bootstrap gui/501 "+mustUnitPath(t, svc) {
		t.Fatalf("plist change: %v %v", err, run.calls)
	}

	// stop pauses the job across restarts and returns once launchd let go of
	// it; start enables it first, then brings it back.
	run.calls = nil
	if err := svc.stop(); err != nil || strings.Join(run.calls, "|") != "launchctl disable gui/501/eu.vaultsync.agent|launchctl print gui/501/eu.vaultsync.agent|launchctl bootout gui/501/eu.vaultsync.agent|launchctl print gui/501/eu.vaultsync.agent" {
		t.Fatalf("stop: %v %v", err, run.calls)
	}
	run.calls = nil
	if err := svc.start(); err != nil || strings.Join(run.calls, "|") != "launchctl enable gui/501/eu.vaultsync.agent|launchctl print gui/501/eu.vaultsync.agent|launchctl bootstrap gui/501 "+mustUnitPath(t, svc) {
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

// `systemctl --user show-environment` quotes values
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
