package tray

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

// #291: what the dialogs on Linux say when they cannot be shown. What each
// program writes was measured on zenity 3.42.1, 3.44.0, 4.0.1, 4.1.90 and
// 4.2.2 and kdialog 21.12.3, 22.12.3, 23.08.5 (Qt 5.15), 25.04.0 (Qt 6.8.2)
// and 26.08.3 (Qt 6.12.1), with no display, with a display that is not there,
// in the C locale, and cancelled with Escape on a display; the texts below are
// what they wrote.

// What zenity writes when it finds no display, with status 1 — the status of
// a cancel. GTK 3 (zenity 3.x) and GTK 4 (zenity 4.x) word it apart.
var zenityNoDisplay = map[string]string{
	"zenity 3.42.1":             "\n(zenity:19): Gtk-WARNING **: 12:52:58.776: cannot open display: \n",
	"zenity 3.44.0":             "\n(zenity:20): Gtk-WARNING **: 12:53:03.095: cannot open display: \n",
	"zenity 3.44.0, DISPLAY=:5": "\n(zenity:79): Gtk-WARNING **: 12:57:29.704: cannot open display: :5\n",
	"zenity 4.0.1":              "\n(zenity:19): Gtk-WARNING **: 12:52:58.754: Failed to open display\n",
	"zenity 4.1.90":             "\n(zenity:20): Gtk-WARNING **: 12:57:08.954: Failed to open display\n",
	"zenity 4.2.2":              "\n(zenity:19): Gtk-WARNING **: 12:52:45.454: Failed to open display\n",
}

// What zenity writes when its window was shown and cancelled with Escape, also
// with status 1: zenity 3 nothing, zenity 4 what the graphics stack and GLib
// warn about in a session without them.
var zenityCancelled = map[string]string{
	"zenity 3.x":    "",
	"zenity 4.0.1":  "libEGL warning: DRI3 error: Could not get DRI3 device\nlibEGL warning: Ensure your X server supports DRI3 to get accelerated rendering\n\n(zenity:452): Gtk-WARNING **: 12:53:02.914: Unable to acquire session bus: Failed to execute child process “dbus-launch” (No such file or directory)\n",
	"zenity 4.1.90": "\n(zenity:447): Gtk-WARNING **: 12:57:12.836: Unable to acquire session bus: Failed to execute child process “dbus-launch” (No such file or directory)\n",
	"zenity 4.2.2":  "MESA-EGL: warning: DRI3 error: Could not get DRI3 device\nMESA-EGL: warning: Ensure your X server supports DRI3 to get accelerated rendering\n\n(zenity:437): Gtk-WARNING **: 12:52:49.513: Unable to acquire session bus: Cannot spawn a message bus without a machine-id: Unable to load /var/lib/dbus/machine-id or /etc/machine-id: Failed to open file “/var/lib/dbus/machine-id”: No such file or directory\n",
}

// What kdialog writes when it finds no display; it then aborts, which the
// runner reports as status -1. Qt 6 in a locale that is not UTF-8 puts its
// locale warning first, and the line that names the display is the fifth.
var kdialogNoDisplay = map[string]string{
	"kdialog 22.12.3, Qt 5.15":             "qt.qpa.xcb: could not connect to display \nqt.qpa.plugin: Could not load the Qt platform plugin \"xcb\" in \"\" even though it was found.\nThis application failed to start because no Qt platform plugin could be initialized. Reinstalling the application may fix this problem.\n\nAvailable platform plugins are: eglfs, linuxfb, minimal, minimalegl, offscreen, vnc, xcb.\n",
	"kdialog 22.12.3, Qt 5.15, DISPLAY=:5": "qt.qpa.xcb: could not connect to display :5\nqt.qpa.plugin: Could not load the Qt platform plugin \"xcb\" in \"\" even though it was found.\nThis application failed to start because no Qt platform plugin could be initialized. Reinstalling the application may fix this problem.\n\nAvailable platform plugins are: eglfs, linuxfb, minimal, minimalegl, offscreen, vnc, xcb.\n",
	"kdialog 26.08.3, Qt 6.12, C locale":   "Detected locale \"C\" with character encoding \"ANSI_X3.4-1968\", which is not UTF-8.\nQt depends on a UTF-8 locale, and has switched to \"C.UTF-8\" instead.\nIf this causes problems, reconfigure your locale. See the locale(1) manual\nfor more information.\nqt.qpa.xcb: could not connect to display \nqt.qpa.plugin: From 6.5.0, xcb-cursor0 or libxcb-cursor0 is needed to load the Qt xcb platform plugin.\nqt.qpa.plugin: Could not load the Qt platform plugin \"xcb\" in \"\" even though it was found.\nThis application failed to start because no Qt platform plugin could be initialized. Reinstalling the application may fix this problem.\n\nAvailable platform plugins are: eglfs, linuxfb, minimal, minimalegl, offscreen, vkkhrdisplay, vnc, wayland-brcm, wayland-egl, wayland, xcb.\n",
}

// zenity's answer, with status 255, to a --text that holds a character outside
// ASCII in a locale that is not UTF-8 (every zenity measured).
const zenityNotUTF8 = "This option is not available. Please see --help for all possible usages.\n"

// eachZenityDialog opens each of zenity's dialogs once and returns whether it
// was taken for a cancel and the error.
func eachZenityDialog(t *testing.T, run runner) map[string]struct {
	ok  bool
	err error
} {
	t.Helper()
	ctx := context.Background()
	z := &zenityAsker{run: run, home: "/home/me"}
	m := Menu{Hub: []MenuEntry{{Label: "Notes", Vault: "vs-1", Default: "/home/me/Vaults/Notes"}}}
	got := map[string]struct {
		ok  bool
		err error
	}{}
	_, ok, err := z.AskCode(ctx)
	got["code"] = struct {
		ok  bool
		err error
	}{ok, err}
	_, ok, err = z.AskHub(ctx, "No Hub answered.")
	got["Hub address"] = struct {
		ok  bool
		err error
	}{ok, err}
	_, ok, err = z.ChooseHub(ctx, twoHubs)
	got["Hub list"] = struct {
		ok  bool
		err error
	}{ok, err}
	_, ok, err = z.ChooseVault(ctx, m)
	got["vault list"] = struct {
		ok  bool
		err error
	}{ok, err}
	ok, err = z.Confirm(ctx, "R&D already contains files.")
	got["consent"] = struct {
		ok  bool
		err error
	}{ok, err}
	err = z.Inform(ctx, "x")
	got["message"] = struct {
		ok  bool
		err error
	}{false, err}
	return got
}

// zenity finds no display and exits with 1, the status of a cancel: Pair a
// Vault… then did nothing and said nothing. The words GTK writes are the only
// thing that tells it from a cancel, and every dialog now fails on them, with
// the program's own line and the terminal to pair in.
func TestIssue291_ZenityWithoutADisplayIsNoCancel(t *testing.T) {
	for version, stderr := range zenityNoDisplay {
		r := &recorder{status: 1, stderr: stderr}
		for dialog, c := range eachZenityDialog(t, r.run) {
			if c.ok || c.err == nil {
				t.Errorf("%s, %s: taken for a cancel (%v %v)", version, dialog, c.ok, c.err)
				continue
			}
			msg := c.err.Error()
			if !strings.Contains(msg, "zenity found no display") || !strings.Contains(msg, "vaultsync pair in a terminal") {
				t.Errorf("%s, %s: %q", version, dialog, msg)
			}
			if !strings.Contains(msg, "open display") || strings.Contains(msg, "Gtk-WARNING") || strings.Contains(msg, "\n") {
				t.Errorf("%s, %s: GTK's line, without GLib's head, expected: %q", version, dialog, msg)
			}
		}
	}
}

// A cancel stays a cancel: what zenity writes beside a window it showed names
// no display it could not open.
func TestIssue291_ACancelStaysACancel(t *testing.T) {
	for version, stderr := range zenityCancelled {
		r := &recorder{status: 1, stderr: stderr}
		for dialog, c := range eachZenityDialog(t, r.run) {
			if c.ok || c.err != nil {
				t.Errorf("%s, %s: a cancel was taken for %v %v", version, dialog, c.ok, c.err)
			}
		}
	}
}

// From the menu: Pair a Vault… without a display ends with the reason in the
// menu's notice, where it ended with nothing.
func TestIssue291_PairingWithoutADisplaySaysSo(t *testing.T) {
	agent := newFakeAgent(t)
	r := &recorder{status: 1, stderr: zenityNoDisplay["zenity 4.2.2"]}
	line, err := Pair(context.Background(), NewClient(agent.socket), &zenityAsker{run: r.run})
	if err == nil || line != "" {
		t.Fatalf("%q %v", line, err)
	}
	if notice := pairingOutcome(line, err); notice != "the code dialog could not be shown: zenity found no display to open it on (Failed to open display); run vaultsync pair in a terminal" {
		t.Fatalf("notice: %q", notice)
	}
	agent.set(func(f *fakeAgent) {
		if len(f.pairs) != 0 {
			t.Fatalf("the agent was asked: %+v", f.pairs)
		}
	})
}

// kdialog without a display writes five to ten lines, and the one that names
// the display is the first or the fifth. A failure says that line, not all of
// it: the rest is Qt's advice to reinstall and its list of plugins.
func TestIssue291_AFailureKeepsTheLineThatNamesTheDisplay(t *testing.T) {
	for version, stderr := range kdialogNoDisplay {
		r := &recorder{status: -1, stderr: stderr}
		_, ok, err := (&kdialogAsker{run: r.run}).AskCode(context.Background())
		if ok || err == nil {
			t.Fatalf("%s: %v %v", version, ok, err)
		}
		msg := err.Error()
		want := "could not connect to display"
		if strings.Contains(version, "DISPLAY=:5") {
			want = "could not connect to display :5"
		}
		if !strings.Contains(msg, "kdialog found no display") || !strings.Contains(msg, want) {
			t.Errorf("%s: %q", version, msg)
		}
		for _, rest := range []string{"Reinstalling", "Available platform plugins", "Detected locale", "qt.qpa", "\n"} {
			if strings.Contains(msg, rest) {
				t.Errorf("%s: %q holds %q", version, msg, rest)
			}
		}
	}
}

// A failure that names no display is still said, but not without end: the
// menu shows it on one line.
func TestIssue291_AFailureIsBounded(t *testing.T) {
	long := strings.Repeat("qt.qpa.plugin: something went wrong in a plugin\n", 40)
	for name, a := range map[string]Asker{
		"zenity":  &zenityAsker{run: (&recorder{status: 255, stderr: long}).run},
		"kdialog": &kdialogAsker{run: (&recorder{status: -1, stderr: long}).run},
		"mac":     &scriptAsker{run: (&recorder{status: 1, stderr: long}).run},
	} {
		_, _, err := a.AskCode(context.Background())
		if err == nil {
			t.Fatalf("%s: no error", name)
		}
		msg := err.Error()
		if n := utf8.RuneCountInString(msg); n > 360 || strings.Contains(msg, "\n") || !strings.HasSuffix(msg, "…") || !strings.Contains(msg, "something went wrong") {
			t.Errorf("%s: %d characters: %q", name, n, msg)
		}
	}
	// What fits is said as it was written, on one line.
	r := &recorder{status: 255, stderr: zenityNotUTF8}
	if _, _, err := (&zenityAsker{run: r.run}).AskCode(context.Background()); err == nil || err.Error() != "the code dialog could not be shown: zenity exited with status 255: This option is not available. Please see --help for all possible usages." {
		t.Errorf("%v", err)
	}
	// A program that ended without a word says only its status.
	r = &recorder{status: -1}
	if _, _, err := (&kdialogAsker{run: r.run}).AskCode(context.Background()); err == nil || err.Error() != "the code dialog could not be shown: kdialog exited with status -1" {
		t.Errorf("%v", err)
	}
}

// vaultMenuAgent asks for a vault with menu, whatever it is sent.
func vaultMenuAgent(t *testing.T, menu Menu) *fakeAgent {
	t.Helper()
	agent := newFakeAgent(t)
	agent.set(func(f *fakeAgent) {
		f.pair = func(PairRequest) (PairResult, int) {
			return PairResult{Output: "✓ Connected to “Office Hub”\n", Refusal: "Pass --vault NAME and --path FOLDER (with --create for a new vault on your Hub) — there is no terminal to ask on.", Needs: "vault", Menu: &menu}, 200
		}
	})
	return agent
}

const everyVaultSyncs = "Every vault on your Hub already syncs here."

// #381 (and #291): with nothing to choose, the pairing ends on the menu's own
// words, in the notice as in the message: "Every vault on your Hub already
// syncs here." where the agent's answer names a vault of the Hub that syncs
// here, not "Nothing to choose: Choose a vault to sync." A message zenity
// cannot show — a name outside ASCII in a session without a UTF-8 locale —
// leaves the notice as it is (#291).
func TestIssue381_NothingToChooseIsSaidInTheMenusOwnWords(t *testing.T) {
	menu := Menu{
		Already:     []MenuEntry{{Label: "Café", Vault: "vs-cafe", Note: "~/Vaults/Café"}, {Label: "Work", Path: "/home/me/Vaults/Work", Note: "~/Vaults/Work"}},
		Unavailable: []MenuEntry{{Label: "Cloudy", Note: "~/iCloud/Cloudy — iCloud Drive"}},
	}
	for name, c := range map[string]struct {
		a        func(*recorder) Asker
		failFrom int
	}{
		"zenity":                     {a: func(r *recorder) Asker { return &zenityAsker{run: r.run, home: "/home/me"} }},
		"zenity, the message fails":  {a: func(r *recorder) Asker { return &zenityAsker{run: r.run, home: "/home/me"} }, failFrom: 2},
		"kdialog":                    {a: func(r *recorder) Asker { return &kdialogAsker{run: r.run, home: "/home/me"} }},
		"kdialog, the message fails": {a: func(r *recorder) Asker { return &kdialogAsker{run: r.run, home: "/home/me"} }, failFrom: 2},
		"mac":                        {a: func(r *recorder) Asker { return &scriptAsker{run: r.run, home: "/home/me"} }},
		"mac, the message fails":     {a: func(r *recorder) Asker { return &scriptAsker{run: r.run, home: "/home/me"} }, failFrom: 2},
	} {
		agent := vaultMenuAgent(t, menu)
		r := &recorder{stdouts: []string{"TULIP-ANCHOR-42\n", ""}, failFrom: c.failFrom}
		line, err := Pair(context.Background(), NewClient(agent.socket), c.a(r))
		if err != nil || line != everyVaultSyncs {
			t.Errorf("%s: %q %v", name, line, err)
		}
		if r.calls != 2 {
			t.Errorf("%s: %d dialogs", name, r.calls)
		}
		shown := strings.Join(r.args, " ") + r.stdin
		if c.failFrom == 0 && (!strings.Contains(shown, everyVaultSyncs) || !strings.Contains(shown, "Café") || !strings.Contains(shown, "Cloudy")) {
			t.Errorf("%s: the message: %q %q", name, r.args, r.stdin)
		}
		if strings.Contains(shown, "Nothing to choose") || strings.Contains(shown, "--list") || strings.Contains(shown, "--menu") || strings.Contains(shown, "choose from list") {
			t.Errorf("%s: %q %q", name, r.args, r.stdin)
		}
	}
}

const hubHasNone = "Your Hub has no vault yet, and VaultSync found none on this computer to start one from; run vaultsync pair in a terminal to choose another folder."

// A Hub with no vault yet, and none found on this computer: the notice says
// that, and where a vault can come from. Every vault of the Hub that is set up
// here is listed under Already, so with none there the Hub has none.
func TestIssue381_NothingOnEitherSideIsSaid(t *testing.T) {
	agent := vaultMenuAgent(t, Menu{})
	r := &recorder{stdouts: []string{"TULIP-ANCHOR-42\n", ""}}
	line, err := Pair(context.Background(), NewClient(agent.socket), &zenityAsker{run: r.run})
	if err != nil || line != hubHasNone {
		t.Fatalf("%q %v", line, err)
	}
}

// The agent lists under Already every vault in Obsidian's list that already
// syncs, whichever Hub it came from, without a vault of the Hub's ID: a vault
// synced with another Hub, or one this Hub no longer has. From such entries
// alone the menu cannot tell whether this Hub has a vault at all, so it makes
// no claim about the Hub's vaults, and keeps the one way on: a vault from
// another folder, in a terminal.
func TestIssue381_VaultsFoundHereSayNothingAboutTheHubsVaults(t *testing.T) {
	agent := vaultMenuAgent(t, Menu{Already: []MenuEntry{{Label: "Work", Path: "/home/me/Vaults/Work", Note: "~/Vaults/Work"}}})
	r := &recorder{stdouts: []string{"TULIP-ANCHOR-42\n", ""}}
	line, err := Pair(context.Background(), NewClient(agent.socket), &zenityAsker{run: r.run})
	if err != nil || line != "No vault to choose: the vaults found here already sync, and your Hub offers no other; run vaultsync pair in a terminal to start one from another folder." {
		t.Fatalf("%q %v", line, err)
	}
	if msg := strings.Join(r.args, " "); !strings.Contains(msg, wrapLines(line, lineWidth)) || !strings.Contains(msg, "Already syncing: Work (~/Vaults/Work)") {
		t.Fatalf("the message: %q", r.args)
	}
}

// The program's line is shown as it came, cleaned as every text from outside
// is in this menu (what hides goes, line breaks with it), and the cut never
// falls inside it: what comes before it does not count, and a display name too
// long for the notice is left out rather than cut.
func TestIssue291_TheDisplayLineIsCleanedAndNeverCut(t *testing.T) {
	ctx := context.Background()
	noise := strings.Repeat("Detected locale \"C\" with character encoding \"ANSI_X3.4-1968\", which is not UTF-8.\n", 6)
	r := &recorder{status: -1, stderr: noise + "qt.qpa.xcb: could not connect to display :5\u202e\x07\r\nAvailable platform plugins are: xcb.\n"}
	_, _, err := (&kdialogAsker{run: r.run}).AskCode(ctx)
	if err == nil || err.Error() != "the code dialog could not be shown: kdialog found no display to open it on (could not connect to display :5); run vaultsync pair in a terminal" {
		t.Fatalf("%q", err)
	}
	r = &recorder{status: 1, stderr: "\n(zenity:19): Gtk-WARNING **: 12:52:58.776: cannot open display: " + strings.Repeat("x", 300) + "\n"}
	_, _, err = (&zenityAsker{run: r.run}).AskCode(ctx)
	if err == nil || err.Error() != "the code dialog could not be shown: zenity found no display to open it on (cannot open display); run vaultsync pair in a terminal" {
		t.Fatalf("%q", err)
	}
	// Any other failure is cleaned the same way: white space that breaks a
	// line (a tab, a line or paragraph separator) is one space, as in a Hub's
	// error text (decision 062), so two words stay two; a terminal's escape
	// sequence goes whole, not only its first character.
	r = &recorder{status: 255, stderr: "error:\tbad\x07 option\u2028see\x1b[1;31m --help\u2029done\x1b]0;title\x07!\n\x1b[0m\n"}
	_, _, err = (&zenityAsker{run: r.run}).AskCode(ctx)
	if err == nil || err.Error() != "the code dialog could not be shown: zenity exited with status 255: error: bad option see --help done!" {
		t.Fatalf("%q", err)
	}
}

// kdialog follows the rule zenity does: a status that is a cancel is one only
// without the toolkit's own line that it found no display. kdialog aborts
// without one on every version measured; a build that exits with 1 or 2
// instead would otherwise do nothing and say nothing again.
func TestIssue291_KdialogFollowsTheSameRule(t *testing.T) {
	ctx := context.Background()
	for _, status := range []int{1, 2} {
		r := &recorder{status: status, stderr: kdialogNoDisplay["kdialog 22.12.3, Qt 5.15"]}
		if _, ok, err := (&kdialogAsker{run: r.run}).AskCode(ctx); ok || err == nil || !strings.Contains(err.Error(), "kdialog found no display") {
			t.Errorf("status %d with the display line: %v %v", status, ok, err)
		}
		r = &recorder{status: status, stderr: qtLocaleLines}
		if _, ok, err := (&kdialogAsker{run: r.run}).AskCode(ctx); ok || err != nil {
			t.Errorf("status %d, a cancel: %v %v", status, ok, err)
		}
	}
}

// Qt's four lines about a locale that is not UTF-8, as Qt 6 writes them
// before anything else.
const qtLocaleLines = "Detected locale \"C\" with character encoding \"ANSI_X3.4-1968\", which is not UTF-8.\nQt depends on a UTF-8 locale, and has switched to \"C.UTF-8\" instead.\nIf this causes problems, reconfigure your locale. See the locale(1) manual\nfor more information.\n"

// In a locale that is not UTF-8 Qt's warning about it comes first, 247
// characters on one line: cut at 200 it would be all a failure said. It is
// left out, and the cause after it is said — here a plugin Qt 6.5 and newer
// needs for X11, a failure with a display.
func TestIssue291_QtsLocaleWarningDoesNotTakeTheCausesPlace(t *testing.T) {
	stderr := qtLocaleLines + "qt.qpa.plugin: From 6.5.0, xcb-cursor0 or libxcb-cursor0 is needed to load the Qt xcb platform plugin.\nqt.qpa.plugin: Could not load the Qt platform plugin \"xcb\" in \"\" even though it was found.\nThis application failed to start because no Qt platform plugin could be initialized. Reinstalling the application may fix this problem.\n"
	r := &recorder{status: -1, stderr: stderr}
	_, _, err := (&kdialogAsker{run: r.run}).AskCode(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "the code dialog could not be shown: kdialog exited with status -1: qt.qpa.plugin: From 6.5.0, xcb-cursor0 or libxcb-cursor0 is needed") || strings.Contains(err.Error(), "locale") {
		t.Fatalf("%q", err)
	}
	// Qt's other wording, where it could not switch to a UTF-8 locale.
	r = &recorder{status: -1, stderr: strings.Replace(qtLocaleLines, "and has switched to \"C.UTF-8\" instead.", "but has failed to switch to one.", 1) + "kdialog: the cause\n"}
	if _, _, err := (&kdialogAsker{run: r.run}).AskCode(context.Background()); err == nil || err.Error() != "the code dialog could not be shown: kdialog exited with status -1: kdialog: the cause" {
		t.Fatalf("%q", err)
	}
}

// Only the toolkit's own line counts: the same words in a warning of another
// kind, or a line without GLib's head, leave a cancel a cancel.
func TestIssue291_OnlyTheToolkitsOwnLineCounts(t *testing.T) {
	for name, stderr := range map[string]string{
		"in another warning":  "(zenity:5): Gtk-WARNING **: 12:00:00.000: Failed to set text 'cannot open display' from markup\n",
		"without GLib's head": "Notes: cannot open display\n",
		"as a plugin's name":  "Available platform plugins are: vkkhrdisplay, xcb.\n",
	} {
		r := &recorder{status: 1, stderr: stderr}
		if _, ok, err := (&zenityAsker{run: r.run}).AskCode(context.Background()); ok || err != nil {
			t.Errorf("%s: %v %v", name, ok, err)
		}
	}
}

// Vaults found here that cannot sync do not make the Hub's empty list read as
// "every vault syncs": the message lists them under the line that says why
// there is nothing to choose.
func TestIssue381_NothingToChooseWithOnlyVaultsThatCannotSync(t *testing.T) {
	agent := vaultMenuAgent(t, Menu{Unavailable: []MenuEntry{{Label: "Cloudy", Note: "~/iCloud/Cloudy — iCloud Drive"}}, Note: "Obsidian’s list of vaults could not be read; vaults it lists may be missing here."})
	r := &recorder{stdouts: []string{"TULIP-ANCHOR-42\n", ""}}
	line, err := Pair(context.Background(), NewClient(agent.socket), &kdialogAsker{run: r.run})
	if err != nil || line != hubHasNone {
		t.Fatalf("%q %v", line, err)
	}
	if msg := r.args[len(r.args)-1]; !strings.Contains(msg, "Unavailable: Cloudy") || !strings.Contains(msg, "could not be read") || !strings.HasPrefix(msg, `<qt><p align="left" style="white-space:pre-wrap">Your Hub has no vault yet`) {
		t.Fatalf("the message: %q", msg)
	}
}
