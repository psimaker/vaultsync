package control

// What the agent answers on the socket (decision 049) — the one definition
// the agent encodes and the terminal and the menu-bar app decode.

// Status is GET /v1/status.
type Status struct {
	Version   string `json:"version"`   // the agent's
	Syncthing string `json:"syncthing"` // the engine's, without the v
	// Engine is "running" when the engine answers, else "starting": the
	// agent is up and bringing it up.
	Engine string `json:"engine"`
	Report
}

// Report is what is known about syncing right now: the lines vaultsync
// status prints below the engine line, and the facts behind them.
type Report struct {
	// Paused: syncing is paused on this computer — every Hub this engine
	// knows is paused in it (vaultsync pause).
	Paused   bool      `json:"paused"`
	Hubs     []Hub     `json:"hubs"`
	Vaults   []Vault   `json:"vaults"`
	Waiting  []Waiting `json:"waiting"`
	Attempts []Attempt `json:"attempts"`
	// Text is the report as vaultsync status prints it.
	Text string `json:"text"`
}

type Hub struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

type Vault struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Path  string `json:"path"`
	State string `json:"state"`
}

// Waiting is a vault the Hub shares that is not set up here yet.
type Waiting struct {
	Label string `json:"label"`
	Hub   string `json:"hub"`
}

// Attempt is a pairing that did not finish.
type Attempt struct {
	Vault string `json:"vault"`
	What  string `json:"what"`
	At    string `json:"at"`
}

// PauseResult answers POST /v1/pause and /v1/resume.
type PauseResult struct {
	Paused bool `json:"paused"`
	Hubs   int  `json:"hubs"` // Hubs this engine knows; 0 leaves nothing to pause
}

// PairRequest is POST /v1/pair: the flags of vaultsync pair.
type PairRequest struct {
	Code   string `json:"code"`
	Hub    string `json:"hub"`
	Vault  string `json:"vault"`
	Create bool   `json:"create"`
	Path   string `json:"path"`
	Yes    bool   `json:"yes"`
	Name   string `json:"name"`
}

// PairResult: what vaultsync pair printed, its refusal when it needs a
// decision the flags did not make, and — once the Hub was reached — the
// menu the terminal would show, so the caller can choose and ask again.
type PairResult struct {
	OK      bool   `json:"ok"`
	Output  string `json:"output"`
	Refusal string `json:"refusal,omitempty"`
	// Needs is the flag the refusal asks for — code, hub, vault, create or
	// yes — when it asks for one, so a caller asks the person for exactly
	// that and never reads the refusal's words for it.
	Needs string `json:"needs,omitempty"`
	// Consent, with needs yes, is the question the terminal would ask —
	// the folder's sync-plugin warnings and what syncing it means, line by
	// line — for the caller's own dialog.
	Consent string `json:"consent,omitempty"`
	Menu    *Menu  `json:"menu,omitempty"`
}

type Menu struct {
	Local       []MenuEntry `json:"local"`
	Hub         []MenuEntry `json:"hub"`
	Already     []MenuEntry `json:"already"`
	Unavailable []MenuEntry `json:"unavailable"`
	Note        string      `json:"note,omitempty"`
}

type MenuEntry struct {
	Label string `json:"label"`
	Path  string `json:"path,omitempty"`  // a folder on this computer
	Vault string `json:"vault,omitempty"` // a vault on the Hub, by its ID
	Note  string `json:"note,omitempty"`
	// Default is where a vault on the Hub would go on this computer — the
	// folder the terminal proposes — so a caller downloads it to the same
	// place the terminal would.
	Default string `json:"default,omitempty"`
}
