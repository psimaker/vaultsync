package tray

// The agent's answers on the control socket (control.go, decision 049), as
// the menu-bar app reads them. The field names are the contract; the
// agent's own test decodes its answers into these types.

// Status is GET /v1/status.
type Status struct {
	Version   string `json:"version"`   // the agent's
	Syncthing string `json:"syncthing"` // the engine's
	// Engine is "running" when the engine answers the agent, else
	// "starting": the agent is up and bringing it up.
	Engine string `json:"engine"`
	// Paused: syncing is paused on this computer (vaultsync pause).
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
	Hubs   int  `json:"hubs"` // Hubs this engine knows; 0 left nothing to pause
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
// decision the flags did not make — Needs names the flag: code, hub,
// vault, create or yes — and, once the Hub was reached, the menu the
// terminal would show.
type PairResult struct {
	OK      bool   `json:"ok"`
	Output  string `json:"output"`
	Refusal string `json:"refusal"`
	Needs   string `json:"needs"`
	// Consent, with Needs yes, is the question the terminal would ask.
	Consent string `json:"consent"`
	Menu    *Menu  `json:"menu"`
}

type Menu struct {
	Local       []MenuEntry `json:"local"`
	Hub         []MenuEntry `json:"hub"`
	Already     []MenuEntry `json:"already"`
	Unavailable []MenuEntry `json:"unavailable"`
	Note        string      `json:"note"`
}

type MenuEntry struct {
	Label string `json:"label"`
	Path  string `json:"path"`  // a folder on this computer
	Vault string `json:"vault"` // a vault on the Hub, by its ID
	Note  string `json:"note"`
	// Default is where a vault on the Hub goes on this computer: the
	// folder the terminal proposes, the agent's word.
	Default string `json:"default"`
}
