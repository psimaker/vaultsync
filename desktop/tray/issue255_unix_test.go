//go:build unix

package tray

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The read behind the Files & Folders note on real files (#255): a pipe in
// a vault's place is not read (opening it would wait for a writer), and a
// folder this account may not read (EACCES, not macOS's EPERM) keeps open's
// own words.
func TestIssue255_TheReadOnRealFiles(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- readContents(fifo) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a pipe: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the read waited on a pipe")
	}

	if os.Geteuid() == 0 {
		t.Skip("root reads any folder")
	}
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(locked, 0o700)
	failed := func(context.Context, string, []string, string) (string, string, int, error) {
		return "", "_LSOpenURLsWithCompletionHandler() failed with error -54 for the file " + locked + ".\n", 1, nil
	}
	err := openWith("darwin", failed, lookAs(true, nil), readContents)(context.Background(), locked)
	if err == nil || !strings.Contains(err.Error(), "error -54") || strings.Contains(err.Error(), "Files & Folders") {
		t.Fatalf("a folder this account may not read: %v", err)
	}
}
