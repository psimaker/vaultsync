package tray

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The pairing conversation: the person gives the code, the agent runs
// vaultsync pair's flag flow (decision 049) and refuses with the flag it
// still needs — the menu to choose a vault from, the consent for a folder
// that already holds files — and the menu-bar app asks for exactly that
// and asks the agent again with the same code. The agent decides
// everything; here nothing is checked, only asked and passed on.

// pairRequestTimeout is as long as the agent itself allows one pairing
// request to take (its socket's write timeout).
const pairRequestTimeout = 15 * time.Minute

// Asker shows the dialogs a pairing needs. ok is false when the person
// cancelled; an error is a dialog that could not be shown.
type Asker interface {
	AskCode(ctx context.Context) (code string, ok bool, err error)
	// AskHub asks which Hub printed the code — by its address, as the
	// terminal takes it — when several answered; refusal is the agent's
	// words, which list them.
	AskHub(ctx context.Context, refusal string) (addr string, ok bool, err error)
	ChooseVault(ctx context.Context, m Menu) (c Choice, ok bool, err error)
	// Confirm asks the consent the agent asked for, with No as the default.
	Confirm(ctx context.Context, text string) (ok bool, err error)
	Inform(ctx context.Context, text string) error
}

// Choice is one entry of the menu: a vault on this computer (Local) or on
// the Hub.
type Choice struct {
	Local bool
	Entry MenuEntry
}

// Pair runs the conversation and returns what it ended with for the
// menu's notice: the agent's last line when a vault was set up, the
// refusal otherwise, "" when the person cancelled. A dialog that could
// not be shown, or an agent that could not be asked, is the error.
func Pair(ctx context.Context, c *Client, ask Asker) (string, error) {
	code, ok, err := ask.AskCode(ctx)
	if err != nil || !ok {
		return "", err
	}
	req := PairRequest{Code: strings.TrimSpace(code)}
	if req.Code == "" {
		return refused(ctx, ask, "Enter the code your Hub printed to pair a vault — Pair a Vault… asks for it again.")
	}
	// Each flag is asked for once, so the conversation is bounded by the
	// kinds of flags there are. Each request has the agent's own limit; the
	// time the person takes over a dialog is not counted against it.
	asked := map[string]bool{}
	for {
		rctx, cancel := context.WithTimeout(ctx, pairRequestTimeout)
		res, err := c.Pair(rctx, req)
		cancel()
		if ctx.Err() != nil {
			// Quit, or the system ending the app: no dialog is opened on a
			// context that is done, whatever the agent answered.
			return "", ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			// The agent did not answer this request in its time; the
			// controller says so in the menu.
			return "", err
		}
		if err != nil {
			_ = ask.Inform(ctx, err.Error())
			return "", err
		}
		if res.OK {
			line := resultLine(res.Output)
			if line == "" {
				line = "The vault is set up."
			}
			_ = ask.Inform(ctx, line)
			return line, nil
		}
		// Each flag is asked for once: the agent asking for it again means
		// the answer did not get through, and asking the person again
		// would not change that — said in the menu's words, since the
		// refusal's name a flag no dialog has.
		if asked[res.Needs] {
			return refused(ctx, ask, "VaultSync asked for the same answer again; run vaultsync pair in a terminal. It said: "+res.Refusal)
		}
		if res.Needs == "" {
			return refused(ctx, ask, res.Refusal)
		}
		asked[res.Needs] = true
		switch res.Needs {
		case "hub":
			addr, ok, err := ask.AskHub(ctx, res.Refusal)
			if err != nil || !ok {
				return "", err
			}
			req.Hub = strings.TrimSpace(addr)
			if req.Hub == "" {
				return refused(ctx, ask, "Enter your Hub's address to pair with it — Pair a Vault… asks for it again.")
			}
		case "vault":
			if res.Menu == nil {
				return refused(ctx, ask, res.Refusal)
			}
			choice, ok, err := ask.ChooseVault(ctx, *res.Menu)
			if err != nil || !ok {
				return "", err
			}
			// A vault from this computer goes out as --create; a name the
			// Hub already has is the agent's refusal (it never turns
			// --create into a download), shown like any other. A vault
			// from the Hub goes where the agent says the terminal would
			// put it.
			if !choice.Local && choice.Entry.Default == "" {
				return refused(ctx, ask, "VaultSync did not say where to put "+choice.Entry.Label+" on this computer; run vaultsync pair in a terminal.")
			}
			req.Vault, req.Create, req.Path = requestFor(choice)
		case "yes":
			// The consent is the agent's own words — the terminal's lines,
			// its sync-plugin warnings included; yes goes out only after
			// the person agreed to them, for the very vault and folder the
			// refusal was about (the request is unchanged otherwise).
			if res.Consent == "" || res.Folder == "" {
				// A refusal that asks for consent without the question to
				// agree to, or without naming the folder it is about, is
				// shown as it is — no dialog puts a flag's sentence to a
				// button, and no yes goes out for a folder not named.
				return refused(ctx, ask, res.Refusal)
			}
			ok, err := ask.Confirm(ctx, res.Consent)
			if err != nil {
				return "", err
			}
			if !ok {
				_ = ask.Inform(ctx, "Nothing was changed.")
				return "", nil
			}
			// The yes names the folder the question was about, so a folder
			// replaced meanwhile is refused by the agent.
			req.Yes, req.Folder = true, res.Folder
		case "create":
			// The menu never asks to create what the Hub has, so this is
			// a Hub vault chosen from the list and gone from the Hub since:
			// said so, not as the flag the terminal would take.
			return refused(ctx, ask, "Your Hub no longer has the vault you chose. Open Pair a Vault… again and choose anew.")
		default:
			// code (the agent did not take the one given), or a flag this
			// app does not know: the terminal is the place for it.
			return refused(ctx, ask, res.Refusal)
		}
	}
}

// refused shows the refusal and ends the conversation with it.
func refused(ctx context.Context, ask Asker, refusal string) (string, error) {
	if refusal == "" {
		refusal = "VaultSync refused the pairing without saying why. Run vaultsync pair in a terminal."
	}
	_ = ask.Inform(ctx, refusal)
	return refusal, nil
}

// requestFor turns a choice into the flags the terminal would take: a
// vault on this computer starts as a new vault on the Hub under the name
// the agent proposed for it (--vault NAME --create --path FOLDER); a
// vault on the Hub is downloaded into the folder the agent named as the
// terminal's default.
func requestFor(c Choice) (vault string, create bool, path string) {
	if c.Local {
		name := c.Entry.Name
		if name == "" {
			name = c.Entry.Label
		}
		return name, true, c.Entry.Path
	}
	return c.Entry.Vault, false, c.Entry.Default
}

// resultLine is the line the agent's output ends on: its last "✓" line —
// the agent's own mark for what was done, which may be followed by a hint
// (where to open the vault) — else the last non-empty line.
func resultLine(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	last := ""
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "✓") {
			return l
		}
		if last == "" {
			last = l
		}
	}
	return last
}

// dialogError says which dialog could not be shown.
func dialogError(what string, err error) error {
	return fmt.Errorf("the %s dialog could not be shown: %w", what, err)
}
