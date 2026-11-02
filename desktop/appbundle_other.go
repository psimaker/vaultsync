//go:build !unix

package main

// writableByMe: VaultSync for Mac is a Mac's; nowhere else does the
// service run from an app.
func writableByMe(string) bool { return false }
