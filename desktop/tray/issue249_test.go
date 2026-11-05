package tray

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// #249: the menu's pairing dialogs on a Linux computer with kdialog and no
// zenity, and every text shown as written. What each program does with a text
// was tried on zenity 3.42, 3.44, 4.0.1 and 4.1.90 and kdialog 21.12, 22.12,
// 23.08 and 25.04; these tests hold the arguments that came of it.

// recorder is a runner that says what it is told to and keeps the arguments.
type recorder struct {
	name   string
	args   []string
	stdin  string
	stdout string
	stderr string
	status int
	err    error
}

func (r *recorder) run(_ context.Context, name string, args []string, stdin string) (string, string, int, error) {
	r.name, r.args, r.stdin = name, args, stdin
	return r.stdout, r.stderr, r.status, r.err
}

const awkward = `R&D <b>x</b> a_b &amp; c:\temp\new`

func TestIssue249_ZenityEscapesOnlyForTheDialogThatReadsIt(t *testing.T) {
	ctx := context.Background()
	text := func(args []string) string {
		t.Helper()
		for _, a := range args {
			if s, ok := strings.CutPrefix(a, "--text="); ok {
				return s
			}
		}
		t.Fatalf("no text in %q", args)
		return ""
	}
	r := &recorder{}
	z := &zenityAsker{run: r.run, home: "/home/me"}
	// An entry: no markup, _ is a shortcut, backslash sequences are read.
	if _, _, err := z.AskHub(ctx, awkward); err != nil {
		t.Fatal(err)
	}
	want := `R&D <b>x</b> a__b &amp; c:\\temp\\new` + "\n\n" + hubPrompt
	if got := text(r.args); got != want {
		t.Fatalf("entry:\n got %q\nwant %q", got, want)
	}
	// A list: Pango markup, backslash sequences are read; its rows are shown
	// as written.
	m := Menu{Hub: []MenuEntry{{Label: awkward, Vault: "vs-1", Default: "/home/me/Vaults/x"}}, Note: awkward}
	r.stdout = "\n"
	if _, _, err := z.ChooseVault(ctx, m); err != nil {
		t.Fatal(err)
	}
	if got := text(r.args); !strings.HasSuffix(got, `R&amp;D &lt;b&gt;x&lt;/b&gt; a_b &amp;amp; c:\\temp\\new`) {
		t.Fatalf("list: %q", got)
	}
	if got := r.args[len(r.args)-1]; !strings.HasPrefix(got, "1) "+awkward+" — on your Hub") {
		t.Fatalf("row: %q", got)
	}
	// --no-markup: as written, a backslash included.
	r.stdout = ""
	if _, err := z.Confirm(ctx, awkward); err != nil {
		t.Fatal(err)
	}
	if got := text(r.args); got != awkward {
		t.Fatalf("question: %q", got)
	}
	if err := z.Inform(ctx, awkward); err != nil {
		t.Fatal(err)
	}
	if got := text(r.args); got != awkward {
		t.Fatalf("message: %q", got)
	}
}

// Return alone chooses nothing where a list opens with a row selected.
func TestIssue249_ReturnInAListChoosesNoVault(t *testing.T) {
	ctx := context.Background()
	m := Menu{Local: []MenuEntry{{Label: "Work", Path: "/w"}}}
	z := &zenityAsker{run: (&recorder{stdout: noChoiceRow + "\n"}).run}
	if c, ok, err := z.ChooseVault(ctx, m); ok || err != nil || c != (Choice{}) {
		t.Fatalf("zenity: %+v %v %v", c, ok, err)
	}
	k := &kdialogAsker{run: (&recorder{stdout: kdialogNoChoice + "\n"}).run}
	if c, ok, err := k.ChooseVault(ctx, m); ok || err != nil || c != (Choice{}) {
		t.Fatalf("kdialog: %+v %v %v", c, ok, err)
	}
}

func TestIssue249_KdialogArguments(t *testing.T) {
	ctx := context.Background()
	const open, closing = `<qt><p align="left" style="white-space:pre-wrap">`, `</p></qt>`
	r := &recorder{stdout: "TULIP-ANCHOR-42\n"}
	k := &kdialogAsker{run: r.run, home: "/home/me"}
	code, ok, err := k.AskCode(ctx)
	if err != nil || !ok || code != "TULIP-ANCHOR-42" {
		t.Fatalf("code: %q %v %v", code, ok, err)
	}
	if r.name != "kdialog" || r.stdin != "" || !reflect.DeepEqual(r.args, []string{"--title", "VaultSync", "--inputbox", open + codePrompt + closing, ""}) {
		t.Fatalf("code args: %s %q", r.name, r.args)
	}
	// An entry's label doubles & for rich text and for the shortcut.
	if _, _, err := k.AskHub(ctx, awkward); err != nil {
		t.Fatal(err)
	}
	label := `R&amp;&amp;D &lt;b&gt;x&lt;/b&gt; a_b &amp;&amp;amp; c:\\temp\\new<br/><br/>`
	if got := r.args[3]; !strings.HasPrefix(got, open+label) || !strings.HasSuffix(got, closing) {
		t.Fatalf("entry label: %q", got)
	}
	// A message and a list's label: & once, and each row as written with its
	// number for a tag.
	if err := k.Inform(ctx, awkward+"\nnext"); err != nil {
		t.Fatal(err)
	}
	escaped := `R&amp;D &lt;b&gt;x&lt;/b&gt; a_b &amp;amp; c:\\temp\\new`
	if !reflect.DeepEqual(r.args, []string{"--title", "VaultSync", "--msgbox", open + escaped + "<br/>next" + closing}) {
		t.Fatalf("message args: %q", r.args)
	}
	m := Menu{Local: []MenuEntry{{Label: awkward, Path: "/w"}}, Hub: []MenuEntry{{Label: "Notes", Vault: "vs-1", Default: "/home/me/Vaults/Notes"}}}
	r.stdout = "2)\n"
	c, ok, err := k.ChooseVault(ctx, m)
	if err != nil || !ok || c.Entry.Vault != "vs-1" {
		t.Fatalf("choose: %+v %v %v", c, ok, err)
	}
	if got := r.args[4:]; !reflect.DeepEqual(got, []string{kdialogNoChoice, noChoiceRow, "1)", "1) " + awkward + " — on this computer", "2)", "2) Notes — on your Hub → ~/Vaults/Notes"}) {
		t.Fatalf("rows: %q", got)
	}
	// Nothing to choose says so; a tag not in the list is a failure.
	if _, ok, err := k.ChooseVault(ctx, Menu{}); ok || err != nil || r.args[2] != "--msgbox" {
		t.Fatalf("nothing to choose: %v %v %q", ok, err, r.args)
	}
	r.stdout = "9)\n"
	if _, _, err := k.ChooseVault(ctx, m); err == nil {
		t.Fatal("a row that is not in the list")
	}
}

// Only the consent row's tag from a kdialog that ended well is a yes. Return
// alone gives the Cancel row; a bad option exits with 1, which a yes-or-no
// question with swapped labels would have read as yes.
func TestIssue249_KdialogConsentTakesTheConsentRowOnly(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		stdout string
		status int
		err    error
		yes    bool
		failed bool
	}{
		"the consent row":       {stdout: "start\n", yes: true},
		"Return, the first row": {stdout: "cancel\n"},
		"nothing printed":       {},
		"something else":        {stdout: "startx\n"},
		"yes in a sentence":     {stdout: "not start\n"},
		"Escape or Cancel":      {status: 1},
		"a bad option":          {stdout: "start\n", status: 1},
		"Escape elsewhere":      {stdout: "start\n", status: 2},
		"a crash":               {stdout: "start\n", status: 134, failed: true},
		"it cannot run":         {stdout: "start\n", err: errors.New("no such file"), failed: true},
	} {
		r := &recorder{stdout: c.stdout, status: c.status, err: c.err}
		yes, err := (&kdialogAsker{run: r.run}).Confirm(ctx, "R&D already contains files.")
		if yes != c.yes || (err != nil) != c.failed {
			t.Errorf("%s: yes %v, %v", name, yes, err)
		}
		// Cancel is the first row, the one selected when the list opens.
		if got := r.args[4:]; !reflect.DeepEqual(got, []string{kdialogCancel, "Cancel", kdialogConsent, consentButton}) {
			t.Errorf("%s: rows %q", name, got)
		}
		if !strings.Contains(r.args[3], "R&amp;D already contains files.") {
			t.Errorf("%s: text %q", name, r.args[3])
		}
	}
}

func TestIssue249_KdialogStatuses(t *testing.T) {
	ctx := context.Background()
	for status, c := range map[int]struct {
		ok     bool
		failed bool
	}{0: {ok: true}, 1: {}, 2: {}, 3: {failed: true}, 134: {failed: true}} {
		k := &kdialogAsker{run: (&recorder{stdout: "x\n", status: status}).run}
		_, ok, err := k.AskCode(ctx)
		if ok != c.ok || (err != nil) != c.failed {
			t.Errorf("status %d: %v %v", status, ok, err)
		}
		if err := k.Inform(ctx, "x"); (err != nil) != c.failed {
			t.Errorf("status %d, message: %v", status, err)
		}
	}
}

// Every dialog on Linux that does not wrap a long line gets one broken to fit;
// a window wider than the screen has its buttons out of reach.
func TestIssue249_EveryDialogGetsItsTextBroken(t *testing.T) {
	ctx := context.Background()
	long := strings.Repeat("word ", 40)
	m := Menu{Note: long, Local: []MenuEntry{{Label: "Work", Path: "/w"}}}
	for name, ask := range map[string]func(Asker) error{
		"hub":     func(a Asker) error { _, _, err := a.AskHub(ctx, long); return err },
		"vault":   func(a Asker) error { _, _, err := a.ChooseVault(ctx, m); return err },
		"consent": func(a Asker) error { _, err := a.Confirm(ctx, long); return err },
	} {
		for program, a := range map[string]func(*recorder) Asker{
			"zenity":  func(r *recorder) Asker { return &zenityAsker{run: r.run} },
			"kdialog": func(r *recorder) Asker { return &kdialogAsker{run: r.run} },
		} {
			r := &recorder{}
			if err := ask(a(r)); err != nil {
				t.Fatalf("%s %s: %v", program, name, err)
			}
			var text string
			if program == "kdialog" {
				text = r.args[3]
			}
			for _, arg := range r.args {
				if s, ok := strings.CutPrefix(arg, "--text="); ok {
					text = s
				}
			}
			for _, line := range strings.Split(strings.ReplaceAll(text, "<br/>", "\n"), "\n") {
				line = strings.TrimSuffix(strings.TrimPrefix(line, `<qt><p align="left" style="white-space:pre-wrap">`), `</p></qt>`)
				if n := len([]rune(line)); n > lineWidth {
					t.Errorf("%s %s: a line of %d: %q", program, name, n, line)
				}
			}
		}
	}
	// zenity's message too; kdialog's message box wraps its own.
	r := &recorder{}
	if err := (&zenityAsker{run: r.run}).Inform(ctx, long); err != nil {
		t.Fatal(err)
	}
	if got := r.args[len(r.args)-1]; !strings.Contains(got, "\n") {
		t.Errorf("zenity message: %q", got)
	}
	r = &recorder{}
	if err := (&kdialogAsker{run: r.run}).Inform(ctx, long); err != nil {
		t.Fatal(err)
	}
	if got := r.args[3]; strings.Contains(got, "<br/>") {
		t.Errorf("kdialog message: %q", got)
	}
}

func TestIssue249_LabelsAreBrokenToFitAScreen(t *testing.T) {
	for name, c := range map[string]struct {
		in, want string
	}{
		"short":          {"one two", "one two"},
		"at a space":     {"one two three", "one two\nthree"},
		"exactly width":  {"one twothr", "one twothr"},
		"a long word":    {"abcdefghijklmnopqrst", "abcdefghij\nklmnopqrst"},
		"word then path": {"a abcdefghijklmnop", "a\nabcdefghij\nklmnop"},
		"lines kept":     {"one\n\ntwo", "one\n\ntwo"},
		"spaces kept":    {"a  b", "a  b"},
		"letters":        {"äöüäöüäöüä äöü", "äöüäöüäöüä\näöü"},
	} {
		if got := wrapLines(c.in, 10); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
	// Nothing of a long text goes missing but the spaces it broke at.
	long := "Your Hub has a vault with quite a long name /home/me/Documents/a-rather-long-folder-name-to-see-how-the-text-wraps/notes and more"
	got := wrapLines(long, lineWidth)
	for _, line := range strings.Split(got, "\n") {
		if n := len([]rune(line)); n > lineWidth {
			t.Errorf("line of %d: %q", n, line)
		}
	}
	strip := func(s string) string { return strings.NewReplacer(" ", "", "\n", "").Replace(s) }
	if strip(got) != strip(long) {
		t.Errorf("text changed: %q", got)
	}
}
