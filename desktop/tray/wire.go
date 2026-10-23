package tray

import "github.com/psimaker/vaultsync/desktop/internal/control"

// The agent's answers on the control socket are the shared package's
// types (internal/control/wire.go), the agent's own definition.
type (
	Status      = control.Status
	Report      = control.Report
	Hub         = control.Hub
	Vault       = control.Vault
	Waiting     = control.Waiting
	Attempt     = control.Attempt
	PauseResult = control.PauseResult
	PairRequest = control.PairRequest
	PairResult  = control.PairResult
	Menu        = control.Menu
	MenuEntry   = control.MenuEntry
)
