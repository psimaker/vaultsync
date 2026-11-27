package tray

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// #479 and #480: the dialog programs run with what their environment needs
// beside the menu-bar app's own — a UTF-8 locale for zenity where the session
// has none, Qt's messages on standard error for kdialog.

func env(vars map[string]string) func(string) string {
	return func(name string) string { return vars[name] }
}

// zenity refuses every --text with a character outside ASCII in a session
// without a UTF-8 locale (status 255, "This option is not available", every
// version from 3.42.1 to 4.2.2). It gets LC_ALL=C.UTF-8 there; a session
// whose locale is UTF-8 keeps it.
func TestIssue479_ZenityGetsAUTF8LocaleWhereTheSessionHasNone(t *testing.T) {
	utf8 := []string{"LC_ALL=C.UTF-8"}
	for name, c := range map[string]struct {
		vars map[string]string
		want []string
	}{
		"no locale variable":               {nil, utf8},
		"LANG=C":                           {map[string]string{"LANG": "C"}, utf8},
		"LANG=POSIX":                       {map[string]string{"LANG": "POSIX"}, utf8},
		"a locale without a codeset":       {map[string]string{"LANG": "en_US"}, utf8},
		"Latin-1":                          {map[string]string{"LANG": "de_DE.ISO-8859-1"}, utf8},
		"LC_ALL=C over a UTF-8 LANG":       {map[string]string{"LC_ALL": "C", "LANG": "en_US.UTF-8"}, utf8},
		"LC_CTYPE=C over a UTF-8 LANG":     {map[string]string{"LC_CTYPE": "C", "LANG": "en_US.UTF-8"}, utf8},
		"LANG=de_DE.UTF-8":                 {map[string]string{"LANG": "de_DE.UTF-8"}, nil},
		"LANG=C.utf8":                      {map[string]string{"LANG": "C.utf8"}, nil},
		"a modifier":                       {map[string]string{"LANG": "sr_RS.UTF-8@latin"}, nil},
		"LC_CTYPE UTF-8 over LANG=C":       {map[string]string{"LC_CTYPE": "C.UTF-8", "LANG": "C"}, nil},
		"LC_ALL UTF-8 over a Latin-1 LANG": {map[string]string{"LC_ALL": "en_US.UTF-8", "LANG": "de_DE.ISO-8859-1"}, nil},
	} {
		if got := zenityEnv(env(c.vars)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %q", name, got)
		}
	}
}

// extraFor builds the asker newAsker gives for the session vars on goos with
// programs, and returns the extra environment its runner was made with.
func extraFor(t *testing.T, goos string, vars map[string]string, programs ...string) (Asker, []string) {
	t.Helper()
	has := func(n string) (string, error) {
		for _, p := range programs {
			if p == n {
				return "/usr/bin/" + n, nil
			}
		}
		return "", errNotHere
	}
	var extra []string
	made := 0
	a := newAsker(goos, "/home/me", has, env(vars), func(e []string) runner {
		extra, made = e, made+1
		return (&recorder{}).run
	})
	if made != 1 {
		t.Fatalf("%s %v: %d runners made", goos, programs, made)
	}
	return a, extra
}

var errNotHere = errors.New("not here")

func TestIssue479_TheZenityAskerRunsWithThatLocale(t *testing.T) {
	a, extra := extraFor(t, "linux", map[string]string{"LANG": "C"}, "zenity", "kdialog")
	if _, ok := a.(*zenityAsker); !ok || !reflect.DeepEqual(extra, []string{"LC_ALL=C.UTF-8"}) {
		t.Fatalf("%T %q", a, extra)
	}
	if _, extra = extraFor(t, "linux", map[string]string{"LANG": "de_DE.UTF-8"}, "zenity"); extra != nil {
		t.Fatalf("a UTF-8 session: %q", extra)
	}
	// osascript is no business of this.
	if a, extra = extraFor(t, "darwin", map[string]string{"LANG": "C"}); extra != nil {
		t.Fatalf("a Mac: %T %q", a, extra)
	}
}

// Qt 6 built to log to the journal writes its messages there when kdialog has
// no controlling terminal, as under a menu-bar app the desktop started: a
// kdialog that found no display aborted with nothing on standard error, and
// the menu said only its status. kdialog runs with QT_FORCE_STDERR_LOGGING=1,
// in every locale.
func TestIssue480_KdialogRunsWithQtsMessagesOnStandardError(t *testing.T) {
	for _, vars := range []map[string]string{nil, {"LANG": "C"}, {"LANG": "de_DE.UTF-8"}} {
		a, extra := extraFor(t, "linux", vars, "kdialog")
		if _, ok := a.(*kdialogAsker); !ok || !reflect.DeepEqual(extra, []string{"QT_FORCE_STDERR_LOGGING=1"}) {
			t.Fatalf("%v: %T %q", vars, a, extra)
		}
	}
	// What Qt then writes (Arch, Qt 6.12, no terminal, the variable set) is
	// said by the line that names the display.
	r := &recorder{status: -1, stderr: kdialogNoDisplay["kdialog 26.08.3, Qt 6.12, C locale"]}
	if _, _, err := (&kdialogAsker{run: r.run}).AskCode(context.Background()); err == nil || !strings.Contains(err.Error(), "kdialog found no display to open it on (could not connect to display)") {
		t.Fatalf("%v", err)
	}
}

// The runner puts the extra entries into the program's environment after this
// app's own, so that they win over the same names; without any it changes
// nothing.
func TestIssue479_TheRunnerPassesItsEnvironment(t *testing.T) {
	t.Setenv("VS_ISSUE479_KEPT", "kept")
	t.Setenv("VS_ISSUE479_OVER", "before")
	out, _, status, err := envRunner([]string{"VS_ISSUE479_OVER=after", "VS_ISSUE479_NEW=new"})(context.Background(), "env", nil, "")
	if err != nil || status != 0 {
		t.Fatalf("%d %v", status, err)
	}
	lines := strings.Split(out, "\n")
	has := func(l string) bool {
		for _, x := range lines {
			if x == l {
				return true
			}
		}
		return false
	}
	if !has("VS_ISSUE479_KEPT=kept") || !has("VS_ISSUE479_OVER=after") || has("VS_ISSUE479_OVER=before") || !has("VS_ISSUE479_NEW=new") {
		t.Fatalf("the environment:\n%s", out)
	}
	out, _, _, _ = envRunner(nil)(context.Background(), "env", nil, "")
	if !strings.Contains(out, "VS_ISSUE479_OVER=before\n") {
		t.Fatalf("without extra entries:\n%s", out)
	}
}
