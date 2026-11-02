//go:build unix

package main

import "syscall"

// writableByMe: this account may change what is at p — access(2) with
// W_OK, which is what the setup link's test -w asks.
func writableByMe(p string) bool {
	const wOK = 2 // W_OK
	return syscall.Access(p, wOK) == nil
}
