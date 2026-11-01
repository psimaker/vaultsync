package tray

import (
	"context"
	"errors"
	"io/fs"
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
	err := openWith("darwin", waited, lookAs(true, nil))(ctx, "/Users/me/Documents/Notes")
	if err == nil || !strings.Contains(err.Error(), "VaultSync for Mac") || !strings.Contains(err.Error(), "click it again") {
		t.Fatalf("an open that waited on a Mac: %v", err)
	}
	if err := openWith("linux", waited, lookAs(true, nil))(ctx, "/home/me/Notes"); err != nil {
		t.Fatalf("a file manager that stays in front on Linux: %v", err)
	}
	// An open that fails where macOS refused the menu its look names the
	// setting; one that fails for another reason keeps its own words.
	failed := func(context.Context, string, []string, string) (string, string, int, error) {
		return "", "LSOpenURLsWithRole() failed with error -54 for the file /Users/me/Documents/Notes.\n", 1, nil
	}
	denied := &fs.PathError{Op: "stat", Path: "/Users/me/Documents/Notes", Err: syscall.EPERM}
	err = openWith("darwin", failed, lookAs(false, denied))(ctx, "/Users/me/Documents/Notes")
	if err == nil || !strings.Contains(err.Error(), "Privacy & Security → Files & Folders") || !strings.Contains(err.Error(), "“VaultSync for Mac”") {
		t.Fatalf("a refused open: %v", err)
	}
	if err := openWith("darwin", failed, lookAs(true, nil))(ctx, "/Users/me/Documents/Notes"); err == nil || !strings.Contains(err.Error(), "error -54") || strings.Contains(err.Error(), "Files & Folders") {
		t.Fatalf("a failure macOS's refusal does not explain: %v", err)
	}
	if !errors.Is(denied, fs.ErrPermission) {
		t.Fatal("EPERM must read as a permission error")
	}
}
