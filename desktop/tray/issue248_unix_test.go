//go:build unix

package tray

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// vaultsync start runs in a process group of its own. launchd signals what is
// left in a login job's group when the job ends; a quit must not reach a
// start half-way, or the job it took down stays down.
func TestIssue248_AStartIsNotInTheMenusProcessGroup(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "pgid")
	program := filepath.Join(dir, "vaultsync")
	script := "#!/bin/sh\nps -o pgid= -p $$ > '" + out + "'\necho ok\n"
	if err := os.WriteFile(program, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	said, err := Start(context.Background(), program)
	if err != nil || said != "ok" {
		t.Fatalf("said %q, %v", said, err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	pgid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("its process group: %q", b)
	}
	if pgid == syscall.Getpgrp() {
		t.Fatalf("the start ran in this program's process group %d", pgid)
	}
}
