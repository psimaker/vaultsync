package tray

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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

// Line is one line of the menu: Text, and the longer Tooltip when the
// agent's words were cut for the line (Tooltip is "" otherwise).
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
		Quit:   Action{Title: "Quit (syncing continues)", Enabled: true},
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
	if st.Paused {
		v.Glyph = GlyphPaused
		v.Headline = "Paused on this computer"
		v.Pause.Title = "Resume Syncing"
		return v
	}
	v.Glyph, v.Headline, v.Detail = summarize(st)
	return v
}

// summarize is the glyph and the headline for a running, unpaused engine:
// a problem first, then work in progress, then the Hub's absence, then
// "up to date".
func summarize(st Status) (Glyph, string, string) {
	var problem *Vault
	syncing := 0
	allUpToDate := len(st.Vaults) > 0
	for i := range st.Vaults {
		vt := &st.Vaults[i]
		switch {
		case vaultProblem(vt.State):
			if problem == nil {
				problem = vt
			}
		case vaultBusy(vt.State):
			syncing++
		}
		if vt.State != "up to date" {
			allUpToDate = false
		}
	}
	if problem != nil {
		return GlyphAttention, "Attention needed", problem.Label + ": " + cutRemedy(problem.State)
	}
	if syncing == 1 {
		return GlyphSyncing, "Syncing 1 vault", ""
	}
	if syncing > 1 {
		return GlyphSyncing, fmt.Sprintf("Syncing %d vaults", syncing), ""
	}
	if len(st.Hubs) == 0 {
		return GlyphOffline, "No Hub paired yet", "Pair a Vault… pairs this computer with your Hub"
	}
	connected := false
	for _, h := range st.Hubs {
		if strings.HasPrefix(h.State, "connected") {
			connected = true
		}
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
	return GlyphSynced, "Connected", ""
}

// vaultProblem: a state the person must act on (status.go's words).
func vaultProblem(state string) bool {
	return strings.HasPrefix(state, "error") || strings.Contains(state, "could not sync")
}

// vaultBusy: the engine is working on this vault.
func vaultBusy(state string) bool {
	return strings.HasPrefix(state, "syncing") || strings.HasPrefix(state, "scanning") || strings.HasPrefix(state, "uploading") || strings.Contains(state, "left to sync")
}

func hubLines(hubs []Hub) []Line {
	var out []Line
	for _, h := range hubs {
		out = append(out, shortened(h.Name+" — ", h.State))
	}
	return out
}

func vaultLines(vaults []Vault, home string) []VaultLine {
	var out []VaultLine
	for _, vt := range vaults {
		l := shortened(vt.Label+" — ", vt.State)
		where := tildePath(home, vt.Path)
		if l.Tooltip == "" {
			l.Tooltip = where
		} else {
			l.Tooltip = where + "\n" + l.Tooltip
		}
		out = append(out, VaultLine{Line: l, Path: vt.Path})
	}
	return out
}

// shortened is prefix + the state, with the one remedy the menu has its
// own item for cut off the line (Resume Syncing) and kept in the tooltip.
func shortened(prefix, state string) Line {
	short := cutRemedy(state)
	l := Line{Text: prefix + short}
	if short != state {
		l.Tooltip = state
	}
	return l
}

// resumeRemedy is the tail the agent puts on a paused state for the
// terminal; the menu's Resume Syncing item is that remedy.
const resumeRemedy = " — vaultsync resume starts syncing again"

// cutRemedy drops the resume remedy off a state and nothing else: every
// other remedy — a folder to allow in System Settings, a log to read —
// has no item in the menu, and a tooltip is not shown on every desktop.
func cutRemedy(state string) string {
	return strings.TrimSuffix(state, resumeRemedy)
}

// tildePath shows a path below the home folder as ~/…
func tildePath(home, p string) string {
	if home == "" || p == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if rel, err := filepath.Rel(home, p); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		return "~" + string(filepath.Separator) + rel
	}
	return p
}
