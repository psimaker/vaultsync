//go:build !unix

package main

// fileOwner reports no owner on platforms without numeric uid/gid semantics
// (Windows); the permission error then falls back to its generic hint.
func fileOwner(string) (uid, gid uint32, ok bool) { return 0, 0, false }
