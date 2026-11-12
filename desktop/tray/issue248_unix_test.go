//go:build unix

package tray

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
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

// A start has its time limit: one that hangs does not leave "Starting
// VaultSync…" standing for good.
func TestIssue248_AStartThatHangsIsEnded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	began := time.Now()
	_, _, _, err := startRunner(ctx, "sleep", []string{"30"}, "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("its end: %v", err)
	}
	if took := time.Since(began); took > 10*time.Second {
		t.Fatalf("it took %v", took)
	}
}
