//go:build linux

package main

import "testing"

func TestIssue175_MountinfoEscapes(t *testing.T) {
	if got := unescapeMountinfo(`/home/me/My\040Vault\134x`); got != `/home/me/My Vault\x` {
		t.Fatalf("got %q", got)
	}
	mounts, err := mountPoints()
	if err != nil || len(mounts) == 0 {
		t.Fatalf("the mount table: %v %v", mounts, err)
	}
}
