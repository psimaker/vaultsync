// Package lock is the one file lock both binaries use: the agent for its
// engine, setup, pairing and state locks, the menu-bar app for its one
// instance per user — one flock, one reading of "held", one check that
// the file is still the one at its place.
package lock
