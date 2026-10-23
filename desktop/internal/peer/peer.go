// Package peer tells whose a unix socket connection is — the one fact the
// control socket's two ends (the agent, the terminal, the menu-bar app)
// authenticate on: the kernel's word on the account at the other end. Both
// binaries import it, so there is one implementation per operating system.
package peer
