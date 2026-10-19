// Package tray is the menu-bar app's logic without the menu bar: what the
// icon and the menu show for what the agent reports (view.go), the
// client that asks the agent on its control socket (client.go), the loop
// that keeps the two together and turns a click into a request
// (controller.go), and the pairing conversation (pairflow.go). It never
// touches the sync engine and decides nothing: every state is the agent's
// own, every action is one the terminal has too (decisions 048, 049).
// The one package that draws anything is cmd/vaultsync-tray.
package tray
