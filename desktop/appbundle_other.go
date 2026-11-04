//go:build !unix

package main

// ownedByMe: VaultSync for Mac is a Mac's; nowhere else does the service
// run from an app.
func ownedByMe(string) bool { return false }
