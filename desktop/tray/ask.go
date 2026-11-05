package tray

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/psimaker/vaultsync/desktop/internal/layout"
)

// The dialogs, through what the desktop already has: AppleScript's dialogs
// on a Mac (osascript), zenity or KDE's kdialog on Linux. Nothing of our own
// to draw, and the person sees the dialogs they know. Every text is passed
// as data — a quoted AppleScript literal, one argument to zenity or kdialog
// — never through a shell.

const dialogTitle = "VaultSync"

// runner runs a dialog program and returns what it printed, its exit
// status and the error of running it at all. A test records the calls.
type runner func(ctx context.Context, name string, args []string, stdin string) (stdout, stderr string, status int, err error)

// NewAsker is the dialogs this computer can show: osascript on a Mac, on
// Linux zenity, else kdialog — nil when there are none, and the menu then
// says to pair in a terminal.
func NewAsker(goos, home string, lookPath func(string) (string, error)) Asker {
	switch goos {
	case "darwin":
		return &scriptAsker{run: execRunner, home: home}
	case "linux":
		if _, err := lookPath("zenity"); err == nil {
			return &zenityAsker{run: execRunner, home: home}
		}
		if _, err := lookPath("kdialog"); err == nil {
			return &kdialogAsker{run: execRunner, home: home}
		}
	}
	return nil
}

// --- the texts both share ---------------------------------------------------

const (
	codePrompt    = "Enter the code your Hub printed (like TULIP-ANCHOR-42):"
	hubPrompt     = "Which Hub printed your code? Enter its address (like 192.168.1.20):"
	consentButton = "Start syncing"
	// noChoiceRow comes first in a list on Linux: zenity 4 and kdialog open
	// a list with its first row selected, so Return alone would choose that
	// vault. Here it chooses nothing, as a cancel.
	noChoiceRow = "Choose a vault below"
)

// menuPrompt is the text above the list of vaults.
func menuPrompt(m Menu) string {
	var b strings.Builder
	b.WriteString("Choose a vault to sync.\n\nA vault from this computer becomes a new vault on your Hub. A vault from your Hub is downloaded into the folder shown.")
	if m.Note != "" {
		b.WriteString("\n\n" + m.Note)
	}
	if len(m.Already) > 0 {
		b.WriteString("\n\nAlready syncing: " + joinEntries(m.Already))
	}
	if len(m.Unavailable) > 0 {
		b.WriteString("\n\nUnavailable: " + joinEntries(m.Unavailable))
	}
	return b.String()
}

func joinEntries(es []MenuEntry) string {
	var parts []string
	for _, e := range es {
		if e.Note != "" {
			parts = append(parts, e.Label+" ("+e.Note+")")
		} else {
			parts = append(parts, e.Label)
		}
	}
	return strings.Join(parts, "; ")
}

// menuItems lists the choosable entries, numbered so the chosen line can
// be told apart from one with the same label; choices maps the numbers. A
// vault from the Hub names the folder it would go to — the agent's
// default, shortened with home.
func menuItems(m Menu, home string) (items []string, choices []Choice) {
	add := func(e MenuEntry, local bool, where string) {
		n := len(items) + 1
		text := fmt.Sprintf("%d) %s — %s", n, e.Label, where)
		if !local && e.Default != "" {
			text += " → " + layout.TildePath(home, e.Default)
		}
		if e.Note != "" {
			text += ", " + e.Note
		}
		items = append(items, text)
		choices = append(choices, Choice{Local: local, Entry: e})
	}
	for _, e := range m.Local {
		add(e, true, "on this computer")
	}
	for _, e := range m.Hub {
		add(e, false, "on your Hub")
	}
	return items, choices
}

// chosen finds the choice a chosen line names by its number.
func chosen(line string, choices []Choice) (Choice, bool) {
	n, _, found := strings.Cut(strings.TrimSpace(line), ")")
	i, err := strconv.Atoi(n)
	if !found || err != nil || i < 1 || i > len(choices) {
		return Choice{}, false
	}
	return choices[i-1], true
}

// lineWidth is the width a text on Linux is broken at. kdialog's list and
// entry, zenity's entry and zenity 3's question do not wrap a long line: the
// window grows wider than the screen, its buttons out of reach.
const lineWidth = 72

// wrapLines breaks each line of s to at most width characters: at a space,
// or in the middle of a longer word such as a path.
func wrapLines(s string, width int) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		r := []rune(line)
		for len(r) > width {
			cut := width
			for i := width; i > 0; i-- {
				if r[i] == ' ' {
					cut = i
					break
				}
			}
			out = append(out, string(r[:cut]))
			r = r[cut:]
			if r[0] == ' ' {
				r = r[1:]
			}
		}
		out = append(out, string(r))
	}
	return strings.Join(out, "\n")
}

// --- macOS: osascript --------------------------------------------------------

type scriptAsker struct {
	run  runner
	home string
}

// appleScriptString quotes s as an AppleScript string literal.
func appleScriptString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\r", `\r`, "\n", `\n`, "\t", `\t`)
	return `"` + r.Replace(s) + `"`
}

// appleScriptList quotes a list of strings.
func appleScriptList(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = appleScriptString(s)
	}
	return "{" + strings.Join(q, ", ") + "}"
}

// osascript runs script and tells a cancel (AppleScript's -128) from a
// failure.
func (a *scriptAsker) osascript(ctx context.Context, what, script string) (out string, ok bool, err error) {
	// A background process's dialog opens behind the active app, without
	// focus; activated first, the script's own process comes to the front
	// with it — and no Apple event goes to another app, so no Automation
	// consent is asked for.
	script = "activate\n" + script
	stdout, stderr, status, err := a.run(ctx, "osascript", nil, script)
	if err != nil {
		return "", false, dialogError(what, err)
	}
	if status != 0 {
		// AppleScript's "User canceled. (-128)" — the token in brackets,
		// not any number that happens to contain it.
		if strings.Contains(stderr, "(-128)") {
			return "", false, nil
		}
		return "", false, dialogError(what, fmt.Errorf("osascript exited with status %d: %s", status, strings.TrimSpace(stderr)))
	}
	return strings.TrimRight(stdout, "\n"), true, nil
}

func (a *scriptAsker) AskCode(ctx context.Context) (string, bool, error) {
	return a.osascript(ctx, "code", `set r to display dialog `+appleScriptString(codePrompt)+` with title `+appleScriptString(dialogTitle)+` default answer "" buttons {"Cancel", "Continue"} default button "Continue"
return text returned of r`)
}

func (a *scriptAsker) AskHub(ctx context.Context, refusal string) (string, bool, error) {
	return a.osascript(ctx, "Hub", `set r to display dialog `+appleScriptString(refusal+"\n\n"+hubPrompt)+` with title `+appleScriptString(dialogTitle)+` default answer "" buttons {"Cancel", "Continue"} default button "Continue"
return text returned of r`)
}

func (a *scriptAsker) ChooseVault(ctx context.Context, m Menu) (Choice, bool, error) {
	items, choices := menuItems(m, a.home)
	if len(items) == 0 {
		err := a.Inform(ctx, "Nothing to choose: "+menuPrompt(m))
		return Choice{}, false, err
	}
	line, ok, err := a.osascript(ctx, "vault", `set c to choose from list `+appleScriptList(items)+` with title `+appleScriptString(dialogTitle)+` with prompt `+appleScriptString(menuPrompt(m))+` OK button name "Continue" cancel button name "Cancel"
if c is false then error number -128
return item 1 of c`)
	if err != nil || !ok {
		return Choice{}, false, err
	}
	c, found := chosen(line, choices)
	if !found {
		return Choice{}, false, dialogError("vault", fmt.Errorf("the choice %q is not in the list", line))
	}
	return c, true, nil
}

func (a *scriptAsker) Confirm(ctx context.Context, text string) (bool, error) {
	_, ok, err := a.osascript(ctx, "consent", `set r to display dialog `+appleScriptString(text)+` with title `+appleScriptString(dialogTitle)+` buttons {"Cancel", `+appleScriptString(consentButton)+`} default button "Cancel" with icon caution
if button returned of r is not `+appleScriptString(consentButton)+` then error number -128`)
	return ok, err
}

func (a *scriptAsker) Inform(ctx context.Context, text string) error {
	_, _, err := a.osascript(ctx, "message", `display dialog `+appleScriptString(text)+` with title `+appleScriptString(dialogTitle)+` buttons {"OK"} default button "OK"`)
	return err
}

// --- Linux: zenity -----------------------------------------------------------

type zenityAsker struct {
	run  runner
	home string
}

// How zenity shows --text, the same in 3.42, 3.44, 4.0 and 4.1, tried with
// each: --entry reads no markup, takes _ for a keyboard shortcut and reads
// backslash sequences; --list reads Pango markup and backslash sequences;
// --question and --info with --no-markup, and a list's rows, show it as
// written. Escaping for the wrong one shows the escape: a vault called R&D
// read R&amp;D above the entry.

// zenityEntryText is text for --entry.
func zenityEntryText(s string) string {
	return strings.NewReplacer(`\`, `\\`, "_", "__").Replace(wrapLines(s, lineWidth))
}

// zenityListText is text for --list.
func zenityListText(s string) string {
	return strings.NewReplacer(`\`, `\\`, "&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(wrapLines(s, lineWidth))
}

// zenity runs zenity with args and tells a cancel (status 1) from a
// failure.
func (a *zenityAsker) zenity(ctx context.Context, what string, args ...string) (out string, ok bool, err error) {
	stdout, stderr, status, err := a.run(ctx, "zenity", append([]string{"--title=" + dialogTitle}, args...), "")
	if err != nil {
		return "", false, dialogError(what, err)
	}
	switch status {
	case 0:
		return strings.TrimRight(stdout, "\n"), true, nil
	case 1:
		return "", false, nil
	}
	return "", false, dialogError(what, fmt.Errorf("zenity exited with status %d: %s", status, strings.TrimSpace(stderr)))
}

func (a *zenityAsker) AskCode(ctx context.Context) (string, bool, error) {
	return a.zenity(ctx, "code", "--entry", "--text="+zenityEntryText(codePrompt))
}

func (a *zenityAsker) AskHub(ctx context.Context, refusal string) (string, bool, error) {
	return a.zenity(ctx, "Hub", "--entry", "--text="+zenityEntryText(refusal+"\n\n"+hubPrompt))
}

func (a *zenityAsker) ChooseVault(ctx context.Context, m Menu) (Choice, bool, error) {
	items, choices := menuItems(m, a.home)
	if len(items) == 0 {
		err := a.Inform(ctx, "Nothing to choose: "+menuPrompt(m))
		return Choice{}, false, err
	}
	args := append([]string{"--list", "--hide-header", "--width=560", "--height=420", "--column=Vault", "--text=" + zenityListText(menuPrompt(m)), "--ok-label=Continue", noChoiceRow}, items...)
	line, ok, err := a.zenity(ctx, "vault", args...)
	if err != nil || !ok {
		return Choice{}, false, err
	}
	if t := strings.TrimSpace(line); t == "" || t == noChoiceRow {
		// Continue without a row: nothing chosen, as a cancel.
		return Choice{}, false, nil
	}
	c, found := chosen(line, choices)
	if !found {
		return Choice{}, false, dialogError("vault", fmt.Errorf("the choice %q is not in the list", line))
	}
	return c, true, nil
}

func (a *zenityAsker) Confirm(ctx context.Context, text string) (bool, error) {
	_, ok, err := a.zenity(ctx, "consent", "--question", "--no-markup", "--default-cancel", "--ok-label="+consentButton, "--cancel-label=Cancel", "--width=480", "--text="+wrapLines(text, lineWidth))
	return ok, err
}

func (a *zenityAsker) Inform(ctx context.Context, text string) error {
	_, _, err := a.zenity(ctx, "message", "--info", "--no-markup", "--width=480", "--text="+wrapLines(text, lineWidth))
	return err
}

// --- Linux: kdialog ----------------------------------------------------------

type kdialogAsker struct {
	run  runner
	home string
}

// kdialog reads a text that looks like markup as Qt rich text, whatever
// asked for it: a folder called <b>x</b> would show bold, and <!-- would hide
// what follows it in a question about that folder. So every text goes as
// rich text with all of it escaped. It reads \\ and \n itself as well.
var (
	kdialogEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `\`, `\\`, "\n", "<br/>")
	// An entry's label takes & for a keyboard shortcut, rich text or not.
	kdialogEntryEscape = strings.NewReplacer("&", "&amp;&amp;", "<", "&lt;", ">", "&gt;", `\`, `\\`, "\n", "<br/>")
)

// kdialogRich is s as rich text, its spaces kept.
func kdialogRich(escaped string) string {
	return `<qt><p align="left" style="white-space:pre-wrap">` + escaped + `</p></qt>`
}

// kdialog runs kdialog with args and tells a cancel from a failure: status 1
// is Cancel or Escape in an entry or a list, 2 is Escape in a message box.
// kdialog also exits with 1 on an option it does not know, so no status alone
// ever stands for a yes.
func (a *kdialogAsker) kdialog(ctx context.Context, what string, args ...string) (out string, ok bool, err error) {
	stdout, stderr, status, err := a.run(ctx, "kdialog", append([]string{"--title", dialogTitle}, args...), "")
	if err != nil {
		return "", false, dialogError(what, err)
	}
	switch status {
	case 0:
		return strings.TrimRight(stdout, "\n"), true, nil
	case 1, 2:
		return "", false, nil
	}
	return "", false, dialogError(what, fmt.Errorf("kdialog exited with status %d: %s", status, strings.TrimSpace(stderr)))
}

func (a *kdialogAsker) entry(ctx context.Context, what, text string) (string, bool, error) {
	return a.kdialog(ctx, what, "--inputbox", kdialogRich(kdialogEntryEscape.Replace(wrapLines(text, lineWidth))), "")
}

func (a *kdialogAsker) AskCode(ctx context.Context) (string, bool, error) {
	return a.entry(ctx, "code", codePrompt)
}

func (a *kdialogAsker) AskHub(ctx context.Context, refusal string) (string, bool, error) {
	return a.entry(ctx, "Hub", refusal+"\n\n"+hubPrompt)
}

// The tags kdialog prints for a row of a list.
const (
	kdialogNoChoice = "none"
	kdialogCancel   = "cancel"
	kdialogConsent  = "start"
)

// list shows text above rows given as tag, label, tag, label…; a row's label
// is shown as written.
func (a *kdialogAsker) list(ctx context.Context, what, text string, rows ...string) (string, bool, error) {
	args := append([]string{"--menu", kdialogRich(kdialogEscape.Replace(wrapLines(text, lineWidth)))}, rows...)
	return a.kdialog(ctx, what, args...)
}

func (a *kdialogAsker) ChooseVault(ctx context.Context, m Menu) (Choice, bool, error) {
	items, choices := menuItems(m, a.home)
	if len(items) == 0 {
		err := a.Inform(ctx, "Nothing to choose: "+menuPrompt(m))
		return Choice{}, false, err
	}
	rows := []string{kdialogNoChoice, noChoiceRow}
	for i, item := range items {
		rows = append(rows, strconv.Itoa(i+1)+")", item)
	}
	line, ok, err := a.list(ctx, "vault", menuPrompt(m), rows...)
	if err != nil || !ok {
		return Choice{}, false, err
	}
	if t := strings.TrimSpace(line); t == "" || t == kdialogNoChoice {
		return Choice{}, false, nil
	}
	c, found := chosen(line, choices)
	if !found {
		return Choice{}, false, dialogError("vault", fmt.Errorf("the choice %q is not in the list", line))
	}
	return c, true, nil
}

// Confirm asks through a list, with Cancel as the row that is selected. A
// yes-or-no question cannot be asked: kdialog's takes Return as yes with no
// way to change that, and with the labels swapped its "no" status, 1, is also
// what a failure exits with. Here only the consent row's tag is a yes, and
// kdialog gives what was printed only for a program that ended well.
func (a *kdialogAsker) Confirm(ctx context.Context, text string) (bool, error) {
	out, _, err := a.list(ctx, "consent", text+"\n\nChoose “"+consentButton+"”, then OK.", kdialogCancel, "Cancel", kdialogConsent, consentButton)
	return out == kdialogConsent, err
}

func (a *kdialogAsker) Inform(ctx context.Context, text string) error {
	_, _, err := a.kdialog(ctx, "message", "--msgbox", kdialogRich(kdialogEscape.Replace(text)))
	return err
}
