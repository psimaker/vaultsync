package tray

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// The dialogs, through what the desktop already has: AppleScript's dialogs
// on a Mac (osascript), zenity on Linux. Nothing of our own to draw, and
// the person sees the dialogs they know. Every text is passed as data —
// a quoted AppleScript literal, one argument to zenity — never through a
// shell.

const dialogTitle = "VaultSync"

// runner runs a dialog program and returns what it printed, its exit
// status and the error of running it at all. A test records the calls.
type runner func(ctx context.Context, name string, args []string, stdin string) (stdout, stderr string, status int, err error)

// NewAsker is the dialogs this computer can show: osascript on a Mac,
// zenity on Linux when it is installed — nil when there are none, and the
// menu then says to pair in a terminal.
func NewAsker(goos string, lookPath func(string) (string, error)) Asker {
	switch goos {
	case "darwin":
		return &scriptAsker{run: execRunner}
	case "linux":
		if _, err := lookPath("zenity"); err == nil {
			return &zenityAsker{run: execRunner}
		}
	}
	return nil
}

// --- the texts both share ---------------------------------------------------

const (
	codePrompt    = "Enter the code your Hub printed (like TULIP-ANCHOR-42):"
	hubPrompt     = "Which Hub printed your code? Enter its address (like 192.168.1.20):"
	consentButton = "Start syncing"
)

// menuPrompt is the text above the list of vaults.
func menuPrompt(m Menu) string {
	var b strings.Builder
	b.WriteString("Choose a vault to sync.\n\nA vault from this computer becomes a new vault on your Hub. A vault from your Hub is downloaded into ~/Vaults.")
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
// be told apart from one with the same label; choices maps the numbers.
func menuItems(m Menu) (items []string, choices []Choice) {
	add := func(e MenuEntry, local bool, where string) {
		n := len(items) + 1
		text := fmt.Sprintf("%d) %s — %s", n, e.Label, where)
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

// --- macOS: osascript --------------------------------------------------------

type scriptAsker struct{ run runner }

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
	// focus; shown by System Events, activated, it comes to the front.
	script = "tell application \"System Events\"\nactivate\n" + script + "\nend tell"
	stdout, stderr, status, err := a.run(ctx, "osascript", nil, script)
	if err != nil {
		return "", false, dialogError(what, err)
	}
	if status != 0 {
		if strings.Contains(stderr, "-128") {
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
	items, choices := menuItems(m)
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

type zenityAsker struct{ run runner }

// pangoText escapes what zenity would read as markup.
func pangoText(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
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
	return a.zenity(ctx, "code", "--entry", "--text="+pangoText(codePrompt))
}

func (a *zenityAsker) AskHub(ctx context.Context, refusal string) (string, bool, error) {
	return a.zenity(ctx, "Hub", "--entry", "--text="+pangoText(refusal+"\n\n"+hubPrompt))
}

func (a *zenityAsker) ChooseVault(ctx context.Context, m Menu) (Choice, bool, error) {
	items, choices := menuItems(m)
	if len(items) == 0 {
		err := a.Inform(ctx, "Nothing to choose: "+menuPrompt(m))
		return Choice{}, false, err
	}
	args := append([]string{"--list", "--hide-header", "--width=560", "--height=420", "--column=Vault", "--text=" + pangoText(menuPrompt(m)), "--ok-label=Continue"}, items...)
	line, ok, err := a.zenity(ctx, "vault", args...)
	if err != nil || !ok {
		return Choice{}, false, err
	}
	c, found := chosen(line, choices)
	if !found {
		return Choice{}, false, dialogError("vault", fmt.Errorf("the choice %q is not in the list", line))
	}
	return c, true, nil
}

func (a *zenityAsker) Confirm(ctx context.Context, text string) (bool, error) {
	_, ok, err := a.zenity(ctx, "consent", "--question", "--no-markup", "--default-cancel", "--ok-label="+consentButton, "--cancel-label=Cancel", "--width=480", "--text="+text)
	return ok, err
}

func (a *zenityAsker) Inform(ctx context.Context, text string) error {
	_, _, err := a.zenity(ctx, "message", "--info", "--no-markup", "--width=480", "--text="+text)
	return err
}
