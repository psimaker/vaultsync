package tray

import (
	"errors"
	"fmt"

	"github.com/psimaker/vaultsync/desktop/internal/control"
	"github.com/psimaker/vaultsync/desktop/internal/layout"
)

// The view: what the icon and the menu show for what the agent reports.
// Pure — the states are the agent's own words (status.go), the glyph a
// summary of them, and nothing here decides anything.

// Glyph is the icon's shape: one summary of the agent's report.
type Glyph int

const (
	// GlyphOffline: nothing syncs right now — no agent answers, not set
	// up, or no Hub connected. (An engine still starting shows the
	// syncing ring: the agent is at work on it.)
	GlyphOffline Glyph = iota
	GlyphSynced
	GlyphSyncing
	GlyphPaused
	// GlyphAttention: a vault reports an error, files that could not sync,
	// or the agent could not report at all.
	GlyphAttention
)

func (g Glyph) String() string {
	switch g {
	case GlyphSynced:
		return "synced"
	case GlyphSyncing:
		return "syncing"
	case GlyphPaused:
		return "paused"
	case GlyphAttention:
		return "attention"
	}
	return "offline"
}

// Action is a menu item the person can click.
type Action struct {
	Title   string
	Enabled bool
}

// Line is one line of the menu: Text, and a Tooltip where there is more
// to say — a vault's folder (~/…), a Hub's state with the remedy the
// terminal names; "" otherwise.
type Line struct {
	Text    string
	Tooltip string
}

// VaultLine is a vault's line; a click opens Path (absolute, from the
// agent) when it is set.
type VaultLine struct {
	Line
	Path string
}

// View is the menu, top to bottom.
type View struct {
	Glyph    Glyph
	Headline string // the first line, VaultSync's state in a few words
	Detail   string // a second line under it (""): the remedy, or the problem
	Notice   string // what the last action said (""), until the next one
	// Paused: syncing is paused on this computer, and the Pause item
	// offers to resume.
	Paused bool
	// NoVaultLine stands where the vaults would be when there are none
	// and the agent answered (""): "No vault yet".
	NoVaultLine string
	Hubs        []Line
	Vaults      []VaultLine
	Waiting     []Line
	Attempts    []Line
	Pause       Action // "Pause Syncing" or "Resume Syncing"
	Pair        Action
	Log         Action // Title "" on Linux: the journal has no file to open
	Quit        Action
}

// Options are the facts the view needs besides the agent's report.
type Options struct {
	Home    string
	LogPath string // "" hides the Log item
	// Pairing: a pairing started from the menu is running.
	Pairing bool
	// CanAsk: this computer can show the dialogs a pairing needs (osascript
	// on a Mac, zenity on Linux); without them the Pair item says what to
	// do instead.
	CanAsk bool
	Notice string
}

// Render is the view for the agent's report, or for why there is none.
func Render(st Status, err error, o Options) View {
	v := View{
		Notice: o.Notice,
		Pause:  Action{Title: "Pause Syncing"},
		Pair:   Action{Title: "Pair a Vault…"},
		Quit:   Action{Title: "Quit", Enabled: true},
	}
	if o.LogPath != "" {
		v.Log = Action{Title: "Open Log", Enabled: true}
	}
	switch {
	case errors.Is(err, ErrNotSetUp):
		v.Headline = "VaultSync is not set up on this computer yet"
		v.Detail = "Run vaultsync setup in a terminal"
		return v
	case errors.Is(err, ErrNoSocket):
		// The agent may well run and sync on without its socket: not the
		// crossed ring that says nothing syncs.
		v.Glyph = GlyphAttention
		v.Headline = "VaultSync does not answer"
		v.Detail = "Stopped, or running without its control socket (see control-socket-error.txt in VaultSync's folder); vaultsync status in a terminal tells which"
		return v
	case errors.Is(err, ErrNoAgent):
		v.Headline = "VaultSync is not running"
		v.Detail = "Run vaultsync start in a terminal"
		return v
	case errors.Is(err, ErrOldAgent):
		v.Glyph = GlyphAttention
		v.Headline = "VaultSync needs an update on this computer"
		v.Detail = "The agent is older than this menu-bar app; run the setup link or vaultsync setup again"
		if st.Engine == "running" && !st.Paused {
			// Its report said its engine runs unpaused: quitting this app
			// changes nothing. An agent that only answered 404 said nothing.
			v.Quit.Title = "Quit (syncing continues)"
		}
		return v
	case err != nil:
		v.Glyph = GlyphAttention
		v.Headline = "VaultSync could not report"
		v.Detail = err.Error()
		return v
	}
	v.Hubs = hubLines(st.Hubs)
	v.Vaults = vaultLines(st.Vaults, o.Home)
	if len(v.Vaults) == 0 {
		v.NoVaultLine = "No vault yet"
	}
	for _, w := range st.Waiting {
		v.Waiting = append(v.Waiting, Line{Text: w.Label + " — shared by " + w.Hub + "; Pair a Vault… sets it up"})
	}
	for _, a := range st.Attempts {
		v.Attempts = append(v.Attempts, Line{Text: a.Vault + " — unfinished pairing (" + a.At + ")", Tooltip: a.What})
	}
	if st.Engine != "running" {
		v.Glyph = GlyphSyncing
		v.Headline = "Starting the sync engine…"
		return v
	}
	v.Pause.Enabled = true
	switch {
	case o.Pairing:
		v.Pair = Action{Title: "Pairing…"}
	case st.Paused:
		v.Pair = Action{Title: "Pair a Vault… (resume syncing first)"}
	case !o.CanAsk:
		v.Pair = Action{Title: "Pair a Vault… (run vaultsync pair in a terminal)"}
	default:
		v.Pair.Enabled = true
	}
	v.Glyph, v.Headline, v.Detail = summarize(st)
	if st.Paused {
		v.Paused = true
		v.Pause.Title = "Resume Syncing"
		// A problem outranks the pause, as in the agent's own status, which
		// keeps a vault's error visible while syncing is paused.
		if v.Glyph != GlyphAttention {
			v.Glyph, v.Headline, v.Detail = GlyphPaused, "Paused on this computer", ""
		}
	}
	if !st.Paused && (v.Glyph == GlyphSynced || v.Glyph == GlyphSyncing || v.Glyph == GlyphAttention) {
		// Quitting the menu-bar app quits the icon alone — said where the
		// icon says something syncs or may; never under a headline that
		// says nothing does (not running, starting, no Hub, no
		// connection, a pause of any kind).
		v.Quit.Title = "Quit (syncing continues)"
	}
	return v
}

// summarize is the glyph and the headline for a running engine, from the
// kinds the agent gives its states: a problem first, then work in
// progress, then a vault paused on its own, then the Hub's absence, then
// "up to date".
func summarize(st Status) (Glyph, string, string) {
	var problem *Vault
	syncing, paused, waiting, unknown := 0, 0, 0, 0
	allUpToDate := len(st.Vaults) > 0
	for i := range st.Vaults {
		vt := &st.Vaults[i]
		switch vt.Kind {
		case control.KindProblem:
			if problem == nil {
				problem = vt
			}
		case control.KindBusy:
			syncing++
		case control.KindPaused:
			paused++
		case control.KindWaiting:
			waiting++
		case control.KindUnknown:
			unknown++
		}
		if vt.Kind != control.KindOK {
			allUpToDate = false
		}
	}
	if problem != nil {
		return GlyphAttention, "Attention needed", problem.Label + ": " + problem.State
	}
	if syncing == 1 {
		return GlyphSyncing, "Syncing 1 vault", ""
	}
	if syncing > 1 {
		return GlyphSyncing, fmt.Sprintf("Syncing %d vaults", syncing), ""
	}
	if paused == 1 {
		return GlyphPaused, "1 vault paused", ""
	}
	if paused > 1 {
		return GlyphPaused, fmt.Sprintf("%d vaults paused", paused), ""
	}
	if len(st.Hubs) == 0 {
		return GlyphOffline, "No Hub paired yet", "Pair a Vault… pairs this computer with your Hub"
	}
	connected, pausedHubs := false, 0
	for _, h := range st.Hubs {
		switch h.Kind {
		case control.KindOK:
			connected = true
		case control.KindPaused:
			pausedHubs++
		}
	}
	if !connected && pausedHubs > 0 {
		// Some Hubs paused, the rest not connected (every Hub paused is
		// st.Paused, said before this): the pause is the news. The menu's
		// own item would pause the rest, so the remedy is the terminal's.
		return GlyphPaused, fmt.Sprintf("%d of %d Hubs paused", pausedHubs, len(st.Hubs)), "vaultsync resume in a terminal resumes it"
	}
	if !connected {
		return GlyphOffline, "Waiting for your Hub", "Not connected right now; syncing resumes when it is"
	}
	if len(st.Vaults) == 0 {
		return GlyphSynced, "Connected — no vault yet", "Pair a Vault… adds one"
	}
	if allUpToDate {
		return GlyphSynced, "Up to date", ""
	}
	// Connected, nothing busy or paused, and still not everything up to
	// date: vaults the Hub has yet to take, or to connect — work ahead,
	// not a check mark.
	if waiting == 1 {
		return GlyphSyncing, "1 vault waiting for your Hub", ""
	}
	if waiting > 1 {
		return GlyphSyncing, fmt.Sprintf("%d vaults waiting for your Hub", waiting), ""
	}
	// No report on a vault this time (the engine did not answer for it):
	// a passing state, not a problem to flip the icon over.
	if unknown == 1 {
		return GlyphSyncing, "1 vault without a status right now", ""
	}
	if unknown > 1 {
		return GlyphSyncing, fmt.Sprintf("%d vaults without a status right now", unknown), ""
	}
	return GlyphSyncing, "Connected — not everything is up to date yet", ""
}

// hubLines: the Hub's state as the agent words it; a remedy the agent
// gives for the terminal goes to the tooltip — the menu has its own item
// for the one remedy there is (Resume Syncing).
func hubLines(hubs []Hub) []Line {
	var out []Line
	for _, h := range hubs {
		l := Line{Text: h.Name + " — " + h.State}
		if h.Remedy != "" {
			l.Tooltip = h.State + " — " + h.Remedy
		}
		out = append(out, l)
	}
	return out
}

func vaultLines(vaults []Vault, home string) []VaultLine {
	var out []VaultLine
	for _, vt := range vaults {
		l := Line{Text: vt.Label + " — " + vt.State, Tooltip: layout.TildePath(home, vt.Path)}
		out = append(out, VaultLine{Line: l, Path: vt.Path})
	}
	return out
}
