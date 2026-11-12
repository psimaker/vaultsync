package tray

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// #249: the menu's pairing dialogs on a Linux computer with kdialog and no
// zenity, and every text shown as written. What each program does with a text
// was tried on zenity 3.42, 3.44, 4.0.1 and 4.1.90 and kdialog 21.12, 22.12,
// 23.08 and 25.04; these tests hold the arguments that came of it.

// recorder is a runner that says what it is told to and keeps the arguments.
// stdouts, when set, are the answers in turn, and calls counts the runs.
type recorder struct {
	name    string
	args    []string
	stdin   string
	stdout  string
	stdouts []string
	stderr  string
	status  int
	err     error
	calls   int
}

func (r *recorder) run(_ context.Context, name string, args []string, stdin string) (string, string, int, error) {
	r.name, r.args, r.stdin = name, args, stdin
	r.calls++
	if len(r.stdouts) > 0 {
		r.stdout, r.stdouts = r.stdouts[0], r.stdouts[1:]
	}
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
	r.stdout = linuxTag(0) + "\n"
	if _, _, err := z.ChooseVault(ctx, m); err != nil {
		t.Fatal(err)
	}
	if got := text(r.args); !strings.HasSuffix(got, `R&amp;D &lt;b&gt;x&lt;/b&gt; a_b &amp;amp; c:\\temp\\new`) {
		t.Fatalf("list: %q", got)
	}
	if got := r.args[len(r.args)-2]; !strings.HasPrefix(got, awkward+" — on your Hub") {
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

// Return alone chooses nothing where a list opens with a row selected, and the
// list is shown again: the person would otherwise type the code again.
func TestIssue249_ReturnInAListChoosesNoVaultAndAsksAgain(t *testing.T) {
	ctx := context.Background()
	m := Menu{Local: []MenuEntry{{Label: "Work", Path: "/w"}, {Label: "Notes", Path: "/n"}}}
	for name, a := range map[string]func(*recorder) Asker{
		"zenity":  func(r *recorder) Asker { return &zenityAsker{run: r.run} },
		"kdialog": func(r *recorder) Asker { return &kdialogAsker{run: r.run} },
	} {
		r := &recorder{stdouts: []string{noChoiceTag + "\n", "\n", linuxTag(1) + "\n"}}
		c, ok, err := a(r).ChooseVault(ctx, m)
		if !ok || err != nil || c.Entry.Path != "/n" || r.calls != 3 {
			t.Errorf("%s, chosen on the third time: %+v %v %v, %d times", name, c, ok, err, r.calls)
		}
		// Never chosen: the list shows rowTries times, then a message says
		// that nothing was chosen. Before, the pairing ended without a word.
		r = &recorder{stdout: noChoiceTag + "\n"}
		c, ok, err = a(r).ChooseVault(ctx, m)
		if ok || err != nil || c != (Choice{}) || r.calls != rowTries+1 {
			t.Errorf("%s, never chosen: %+v %v %v, %d runs", name, c, ok, err, r.calls)
		}
		if said := strings.Join(r.args, " "); !strings.Contains(said, "No vault was chosen") || strings.Contains(said, "--list") || strings.Contains(said, "--menu") {
			t.Errorf("%s, after the last showing: %q", name, r.args)
		}
		r = &recorder{status: 1}
		if _, ok, err := a(r).ChooseVault(ctx, m); ok || err != nil || r.calls != 1 {
			t.Errorf("%s, a cancel: %v %v, %d times", name, ok, err, r.calls)
		}
	}
}

// A row shows no number to type: zenity 4 filters on a typed key. What is read
// back is a hidden column, printed alone, and it is the second one: zenity 3
// looks for what a person types in the first column, and with the tags there
// a typed 192.168.1.21 chose the first row. Its tag is made of a character
// no keyboard types: zenity 4.1 looks in the hidden column too, and a typed 2
// chose the row once tagged linuxTag(1).
func TestIssue249_ARowShowsNoNumberAndAHiddenTagIsReadBack(t *testing.T) {
	ctx := context.Background()
	m := Menu{Local: []MenuEntry{{Label: "Work", Path: "/w"}}, Hub: []MenuEntry{{Label: "Work", Vault: "vs-1"}}}
	r := &recorder{stdout: linuxTag(1) + "\n"}
	c, ok, err := (&zenityAsker{run: r.run}).ChooseVault(ctx, m)
	if err != nil || !ok || c.Entry.Vault != "vs-1" {
		t.Fatalf("%+v %v %v", c, ok, err)
	}
	joined := strings.Join(r.args, "\x00")
	if !strings.Contains(joined, strings.Join([]string{"--column=Choice", "--column=Row", "--hide-column=2", "--print-column=2"}, "\x00")) {
		t.Fatalf("the columns: %q", r.args)
	}
	if got := r.args[len(r.args)-6:]; !reflect.DeepEqual(got, []string{noVaultRow, noChoiceTag, "Work — on this computer", linuxTag(0), "Work — on your Hub", linuxTag(1)}) {
		t.Fatalf("rows: %q", got)
	}
	// On a Mac the row's words come back, so its number stays in them.
	r = &recorder{stdout: "2) Work — on your Hub\n"}
	c, ok, err = (&scriptAsker{run: r.run}).ChooseVault(ctx, m)
	if err != nil || !ok || c.Entry.Vault != "vs-1" {
		t.Fatalf("mac: %+v %v %v", c, ok, err)
	}
	if !strings.Contains(r.stdin, `{"1) Work — on this computer", "2) Work — on your Hub"}`) {
		t.Fatalf("mac rows: %s", r.stdin)
	}
}

// With nothing to choose, each program says so, and asks nothing.
func TestIssue249_NothingToChooseIsSaid(t *testing.T) {
	ctx := context.Background()
	for name, a := range map[string]func(*recorder) Asker{
		"zenity":  func(r *recorder) Asker { return &zenityAsker{run: r.run} },
		"kdialog": func(r *recorder) Asker { return &kdialogAsker{run: r.run} },
		"mac":     func(r *recorder) Asker { return &scriptAsker{run: r.run} },
	} {
		r := &recorder{}
		if _, ok, err := a(r).ChooseVault(ctx, Menu{}); ok || err != nil || r.calls != 1 {
			t.Errorf("%s: %v %v, %d times", name, ok, err, r.calls)
		}
		if !strings.Contains(strings.Join(r.args, " ")+r.stdin, "Nothing to choose") {
			t.Errorf("%s: %q %q", name, r.args, r.stdin)
		}
		if strings.Contains(strings.Join(r.args, " ")+r.stdin, "choose from list") || strings.Contains(strings.Join(r.args, " "), "--list") || strings.Contains(strings.Join(r.args, " "), "--menu") {
			t.Errorf("%s offered a list: %q %q", name, r.args, r.stdin)
		}
	}
}

// A long prompt takes the list's room on zenity 4: the window grows with it.
func TestIssue249_TheListGrowsWithItsPrompt(t *testing.T) {
	if got := zenityListHeight("one\ntwo\nthree\nfour"); got != 420 {
		t.Errorf("a short prompt: %d", got)
	}
	if got := zenityListHeight(strings.Repeat("line\n", 9) + "line"); got != 420+17*6 {
		t.Errorf("ten lines: %d", got)
	}
	if got := zenityListHeight(strings.Repeat("line\n", 200)); got != 760 {
		t.Errorf("no end of lines: %d", got)
	}
	r := &recorder{stdout: linuxTag(0) + "\n"}
	long := Menu{Note: strings.Repeat("word ", 200), Local: []MenuEntry{{Label: "Work", Path: "/w"}}}
	if _, _, err := (&zenityAsker{run: r.run}).ChooseVault(context.Background(), long); err != nil {
		t.Fatal(err)
	}
	// By the lines of the text as zenity is given it, broken to fit, not as
	// the agent wrote them: the note above is one line of a thousand characters.
	var text, height string
	for _, a := range r.args {
		if v, ok := strings.CutPrefix(a, "--text="); ok {
			text = v
		}
		if v, ok := strings.CutPrefix(a, "--height="); ok {
			height = v
		}
	}
	lines := strings.Count(text, "\n") + 1
	if lines < 10 || height != strconv.Itoa(420+17*(lines-4)) {
		t.Fatalf("%d lines of text, height %s: %q", lines, height, r.args)
	}
}

// zenity's windows are wide enough for a line of lineWidth characters: at 480
// zenity broke each line once more, and a message read as stubs.
func TestIssue249_ZenityWindowsFitALine(t *testing.T) {
	ctx := context.Background()
	r := &recorder{stdout: linuxTag(0) + "\n"}
	z := &zenityAsker{run: r.run}
	shows := map[string]func() error{
		"message": func() error { return z.Inform(ctx, "x") },
		"consent": func() error { _, err := z.Confirm(ctx, "x"); return err },
		"list": func() error {
			_, _, err := z.ChooseVault(ctx, Menu{Local: []MenuEntry{{Label: "Work", Path: "/w"}}})
			return err
		},
	}
	for name, show := range shows {
		if err := show(); err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(r.args, "--width=560") {
			t.Errorf("the %s: %q", name, r.args)
		}
	}
}

// A row's words are an argument of their own, and zenity and kdialog read one
// that begins with "-" as an option: for a vault called -archive the list did
// not open, and a name could spell an option that is taken. Such a row gets a
// space before it; the others stay as written.
func TestIssue249_ARowThatBeginsWithADashIsNoOption(t *testing.T) {
	ctx := context.Background()
	m := Menu{
		Local: []MenuEntry{{Label: "-archive", Path: "/a"}, {Label: "Work", Path: "/w"}},
		Hub:   []MenuEntry{{Label: "--gtk-module=/home/me/Vaults/Notes/x.so:", Vault: "vs-1", Default: "/home/me/Vaults/x"}},
	}
	for name, c := range map[string]struct {
		asker Asker
		rows  func([]string) []string
	}{
		"zenity":  {&zenityAsker{}, func(args []string) []string { return args[slices.Index(args, "--ok-label=Continue")+1:] }},
		"kdialog": {&kdialogAsker{}, func(args []string) []string { return args[4:] }},
	} {
		r := &recorder{stdout: linuxTag(2) + "\n"}
		switch a := c.asker.(type) {
		case *zenityAsker:
			a.run, a.home = r.run, "/home/me"
		case *kdialogAsker:
			a.run, a.home = r.run, "/home/me"
		}
		choice, ok, err := c.asker.ChooseVault(ctx, m)
		if err != nil || !ok || choice.Entry.Vault != "vs-1" {
			t.Fatalf("%s: %+v %v %v", name, choice, ok, err)
		}
		rows := c.rows(r.args)
		if len(rows) != 8 {
			t.Fatalf("%s rows: %q", name, rows)
		}
		for _, row := range rows {
			if strings.HasPrefix(row, "-") {
				t.Errorf("%s would read %q as an option", name, row)
			}
		}
		joined := strings.Join(rows, "\x00")
		for _, want := range []string{" -archive — on this computer", "Work — on this computer", " --gtk-module=/home/me/Vaults/Notes/x.so: — on your Hub → ~/Vaults/x"} {
			if !slices.Contains(rows, want) {
				t.Errorf("%s: no row %q in %q", name, want, joined)
			}
		}
	}
}

// A failure says all that the program wrote: Qt's first line may be a warning
// that also heads a run that works, with the cause further down.
func TestIssue249_AFailureSaysAllThatWasWritten(t *testing.T) {
	r := &recorder{status: -1, stderr: "Detected locale \"C\" with character encoding \"ANSI_X3.4-1968\", which is not UTF-8.\nqt.qpa.xcb: could not connect to display\n"}
	_, _, err := (&kdialogAsker{run: r.run}).AskCode(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Detected locale") || !strings.Contains(err.Error(), "could not connect to display") {
		t.Fatalf("%v", err)
	}
}

// zenity's only cancel is status 1: anything else but 0 is a failure.
func TestIssue249_ZenityStatuses(t *testing.T) {
	for status, c := range map[int]struct{ ok, failed bool }{0: {ok: true}, 1: {}, 2: {failed: true}, 5: {failed: true}} {
		r := &recorder{stdout: "x\n", status: status}
		_, ok, err := (&zenityAsker{run: r.run}).AskCode(context.Background())
		if ok != c.ok || (err != nil) != c.failed {
			t.Errorf("status %d: %v %v", status, ok, err)
		}
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
	r.stdout = linuxTag(1) + "\n"
	c, ok, err := k.ChooseVault(ctx, m)
	if err != nil || !ok || c.Entry.Vault != "vs-1" {
		t.Fatalf("choose: %+v %v %v", c, ok, err)
	}
	if got := r.args[4:]; !reflect.DeepEqual(got, []string{noChoiceTag, noVaultRow, linuxTag(0), awkward + " — on this computer", linuxTag(1), "Notes — on your Hub → ~/Vaults/Notes"}) {
		t.Fatalf("rows: %q", got)
	}
	// Nothing to choose says so; a tag not in the list is a failure.
	if _, ok, err := k.ChooseVault(ctx, Menu{}); ok || err != nil || r.args[2] != "--msgbox" {
		t.Fatalf("nothing to choose: %v %v %q", ok, err, r.args)
	}
	r.stdout = linuxTag(8) + "\n"
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
