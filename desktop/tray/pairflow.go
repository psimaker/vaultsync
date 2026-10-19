package tray

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The pairing conversation: the person gives the code, the agent runs
// vaultsync pair's flag flow (decision 049) and refuses with the flag it
// still needs — the menu to choose a vault from, the consent for a folder
// that already holds files — and the menu-bar app asks for exactly that
// and asks the agent again with the same code. The agent decides
// everything; here nothing is checked, only asked and passed on.

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

// maxPairRounds bounds the conversation: the agent asks for each flag at
// most once, so more rounds than kinds of flags means something is off.
const maxPairRounds = 6

// Pair runs the conversation and returns what it ended with for the
// menu's notice: the agent's last line when a vault was set up, the
// refusal otherwise, "" when the person cancelled. A dialog that could
// not be shown, or an agent that could not be asked, is the error.
func Pair(ctx context.Context, c *Client, ask Asker, home string) (string, error) {
	code, ok, err := ask.AskCode(ctx)
	if err != nil || !ok {
		return "", err
	}
	req := PairRequest{Code: strings.TrimSpace(code)}
	if req.Code == "" {
		return "", nil
	}
	asked := map[string]bool{}
	for round := 0; round < maxPairRounds; round++ {
		res, err := c.Pair(ctx, req)
		if err != nil {
			_ = ask.Inform(ctx, err.Error())
			return "", err
		}
		if res.OK {
			line := lastLine(res.Output)
			if line == "" {
				line = "The vault is set up."
			}
			_ = ask.Inform(ctx, line)
			return line, nil
		}
		// Each flag is asked for once: the agent asking for it again means
		// the answer did not get through, and asking the person again
		// would not change that.
		if res.Needs == "" || asked[res.Needs] {
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
		case "vault":
			if res.Menu == nil {
				return refused(ctx, ask, res.Refusal)
			}
			choice, ok, err := ask.ChooseVault(ctx, *res.Menu)
			if err != nil || !ok {
				return "", err
			}
			if taken := sameNameOnHub(choice, *res.Menu); taken != "" {
				// The terminal asks for another name here; the menu has no
				// name to offer, and --create with a name the Hub already
				// has would resolve to the Hub's vault instead of starting
				// a new one (that is how a repeated setup finds its own
				// vault, #228) — an empty folder would then receive the
				// Hub's vault the person did not choose.
				return refused(ctx, ask, "Your Hub already has a vault named "+taken+". To download it, choose it under On your Hub; to start a new vault from this folder under another name, run vaultsync pair in a terminal.")
			}
			req.Vault, req.Create, req.Path = requestFor(choice, home)
		case "yes":
			// The consent is the agent's own words; yes goes out only after
			// the person agreed to them, for the very vault and folder the
			// refusal was about (the request is unchanged otherwise).
			ok, err := ask.Confirm(ctx, consentText(res.Refusal))
			if err != nil {
				return "", err
			}
			if !ok {
				_ = ask.Inform(ctx, "Nothing was changed.")
				return "", nil
			}
			req.Yes = true
		default:
			// code (the agent did not take the one given), create (the
			// Hub has no such vault), or a flag this app does not know:
			// the terminal is the place for it.
			return refused(ctx, ask, res.Refusal)
		}
	}
	return refused(ctx, ask, "The pairing did not finish: VaultSync kept asking for more. Run vaultsync pair in a terminal.")
}

// refused shows the agent's refusal and ends the conversation with it.
func refused(ctx context.Context, ask Asker, refusal string) (string, error) {
	if refusal == "" {
		refusal = "VaultSync refused the pairing without saying why. Run vaultsync pair in a terminal."
	}
	_ = ask.Inform(ctx, refusal)
	return refusal, nil
}

// sameNameOnHub is the Hub vault's name when a vault chosen on this
// computer bears a name the Hub already lists (as the agent matches names:
// without regard to case), else "".
func sameNameOnHub(c Choice, m Menu) string {
	if !c.Local {
		return ""
	}
	name := strings.TrimSpace(c.Entry.Label)
	for _, h := range m.Hub {
		if strings.EqualFold(strings.TrimSpace(h.Label), name) {
			return "“" + h.Label + "”"
		}
	}
	return ""
}

// requestFor turns a choice into the flags the terminal would take: a
// vault on this computer starts as a new vault on the Hub under its
// folder's name (--vault NAME --create --path FOLDER); a vault on the
// Hub is downloaded into ~/Vaults/<name>, the terminal's default.
func requestFor(c Choice, home string) (vault string, create bool, path string) {
	if c.Local {
		return c.Entry.Label, true, c.Entry.Path
	}
	return c.Entry.Vault, false, filepath.Join(home, "Vaults", folderName(c.Entry.Label))
}

// consentText is the agent's refusal as the question to agree to: without
// the flag it names (the button is the answer here), and with the note
// the terminal's own consent gives.
func consentText(refusal string) string {
	t := strings.TrimSuffix(refusal, " Pass --yes to agree.")
	return t + "\n\nIf Obsidian Sync or another service also syncs this folder, turn that off for it first."
}

// lastLine is the last non-empty line of the agent's output.
func lastLine(out string) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// maxFolderNameRunes mirrors the agent's limit on a vault's name.
const maxFolderNameRunes = 64

// folderName turns a vault label into one folder name the way the agent
// does for the terminal's default (safeVaultName): no separators, control
// characters or leading dots, at most 64 characters.
func folderName(label string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(label) {
		switch {
		case r == '/' || r == '\\' || r == ':' || unicode.IsControl(r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	name := strings.Join(strings.Fields(b.String()), " ")
	name = strings.TrimLeft(name, ". ")
	for utf8.RuneCountInString(name) > maxFolderNameRunes {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	if name == "" {
		return "Vault"
	}
	return name
}

// errNoDialogs: this computer has no way to show the dialogs.
var errNoDialogs = errors.New("no dialog program found")

// dialogError says which dialog could not be shown.
func dialogError(what string, err error) error {
	return fmt.Errorf("the %s dialog could not be shown: %w", what, err)
}
