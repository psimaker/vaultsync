package tray

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

// #255: macOS asks once whether VaultSync for Mac may open a folder in
// Documents, Desktop or Downloads. The menu must neither give up on that
// in silence nor let a refused look launch what it meant to reveal.

// lookAs answers every look at a path with dir and err.
func lookAs(dir bool, err error) func(string) (bool, error) {
	return func(string) (bool, error) { return dir, err }
}

// A folder whose name has an extension is revealed, never opened (open
// would launch it as an app) — also when macOS did not let the menu look
// at it: only a look that found a file, or nothing at all, opens it.
func TestIssue255_ARefusedLookRevealsInsteadOfLaunching(t *testing.T) {
	denied := &fs.PathError{Op: "stat", Path: "/Users/me/Documents/Journal.app", Err: syscall.EPERM}
	for _, c := range []struct {
		what string
		look func(string) (bool, error)
		args []string
	}{
		{"a folder", lookAs(true, nil), []string{"-R", "/Users/me/Documents/Journal.app"}},
		{"a look macOS refused", lookAs(false, denied), []string{"-R", "/Users/me/Documents/Journal.app"}},
		{"a file", lookAs(false, nil), []string{"/Users/me/Documents/Journal.app"}},
		{"nothing there", lookAs(false, fs.ErrNotExist), []string{"/Users/me/Documents/Journal.app"}},
	} {
		name, args, err := openCommand("darwin", "/Users/me/Documents/Journal.app", c.look)
		if err != nil || name != "open" || !reflect.DeepEqual(args, c.args) {
			t.Errorf("%s: %s %v %v", c.what, name, args, err)
		}
	}
}

// open on a Mac hands the folder to Finder and returns at once; one that
// still runs after half a minute waits for macOS's question. That is said
// — never taken for done — with what to do; on Linux a file manager that
// stays in front is still what it did.
func TestIssue255_AnOpenThatWaitsForMacOSIsSaid(t *testing.T) {
	ctx := context.Background()
	waited := func(context.Context, string, []string, string) (string, string, int, error) {
		return "", "", 0, context.DeadlineExceeded
	}
	readable := func(string) error { return nil }
	err := openWith("darwin", waited, lookAs(true, nil), readable)(ctx, "/Users/me/Documents/Notes")
	if err == nil || !strings.Contains(err.Error(), "VaultSync for Mac") || !strings.Contains(err.Error(), "click it again") {
		t.Fatalf("an open that waited on a Mac: %v", err)
	}
	if err := openWith("linux", waited, lookAs(true, nil), readable)(ctx, "/home/me/Notes"); err != nil {
		t.Fatalf("a file manager that stays in front on Linux: %v", err)
	}
	// An open that fails where macOS refused the app the folder names the
	// setting. Under that refusal a look at the folder (stat) passes; its
	// contents are refused with EPERM — that is what is asked. A plain
	// permission (EACCES), or a folder that can be read, keeps open's own
	// words: the Files & Folders setting cannot help there.
	failed := func(context.Context, string, []string, string) (string, string, int, error) {
		return "", "LSOpenURLsWithRole() failed with error -54 for the file /Users/me/Documents/Notes.\n", 1, nil
	}
	refused := func(p string) error { return &fs.PathError{Op: "open", Path: p, Err: syscall.EPERM} }
	err = openWith("darwin", failed, lookAs(true, nil), refused)(ctx, "/Users/me/Documents/Notes")
	if err == nil || !strings.Contains(err.Error(), "Privacy & Security → Files & Folders") || !strings.Contains(err.Error(), "“VaultSync for Mac”") {
		t.Fatalf("an open macOS refused: %v", err)
	}
	unixDenied := func(p string) error { return &fs.PathError{Op: "open", Path: p, Err: syscall.EACCES} }
	for name, read := range map[string]func(string) error{"a plain permission": unixDenied, "a folder that can be read": readable} {
		if err := openWith("darwin", failed, lookAs(true, nil), read)(ctx, "/Users/me/Documents/Notes"); err == nil || !strings.Contains(err.Error(), "error -54") || strings.Contains(err.Error(), "Files & Folders") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// The read itself: a folder's first name, a file's first byte — and
	// nothing there is said as such.
	dir := t.TempDir()
	if err := readContents(dir); err != nil {
		t.Fatalf("an empty folder: %v", err)
	}
	file := dir + "/log"
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := readContents(file); err != nil {
		t.Fatalf("a file: %v", err)
	}
	if err := readContents(dir + "/none"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("nothing there: %v", err)
	}
}
