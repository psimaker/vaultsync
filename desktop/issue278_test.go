package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// A folder in the engine's data that VaultSync may not look into stops the
// removal where it stopped before — said as what it is, with that folder's
// path, not as something from another disk, which sends a person looking
// for a disk that is not there (#278).
func TestIssue278_AFolderVaultSyncMayNotLookIntoIsNamedAsThat(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root looks into what a permission keeps")
	}
	a, link := uninstallable(t)
	stuck := filepath.Join(a.lay.Home, "x")
	writeFile(t, filepath.Join(stuck, "f"), "x")
	if err := os.Chmod(stuck, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stuck, 0o700) })

	got := refusalText(a.uninstall(context.Background(), []string{"--remove-data", "--yes"}))
	if strings.Contains(got, "another disk") || !strings.Contains(got, "VaultSync may not look into "+stuck+" (permission denied)") {
		t.Fatalf("a folder VaultSync may not look into, as main prints it: %q", got)
	}
	if !strings.Contains(got, "run again finishes the removal once that is fixed") {
		t.Fatalf("the refusal no longer says that running the removal again finishes it: %q", got)
	}
	// The stop is where it was: before anything of the engine's data went.
	if _, err := os.Stat(filepath.Join(a.lay.Home, "config.xml")); err != nil {
		t.Fatalf("the engine's data was removed in part before the stop: %v", err)
	}
	keptForAgain(t, a, link)

	if err := os.Chmod(stuck, 0o700); err != nil {
		t.Fatal(err)
	}
	finishedBy(t, a, link)
}

// A folder that can be read but not searched lets VaultSync list its
// entries, not look at them: the folder named is that one — the one whose
// permission to put right — not the entry in it (#278).
func TestIssue278_AFolderWhoseEntriesCannotBeLookedAtNamesThatFolder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root looks into what a permission keeps")
	}
	a, link := uninstallable(t)
	stuck := filepath.Join(a.lay.Home, "x")
	writeFile(t, filepath.Join(stuck, "y", "f"), "x")
	if err := os.Chmod(stuck, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stuck, 0o700) })

	got := refusalText(a.uninstall(context.Background(), []string{"--remove-data", "--yes"}))
	if strings.Contains(got, "another disk") || !strings.Contains(got, "VaultSync may not look into "+stuck+" (permission denied)") {
		t.Fatalf("a folder whose entries cannot be looked at, as main prints it: %q", got)
	}
	if _, err := os.Stat(filepath.Join(a.lay.Home, "config.xml")); err != nil {
		t.Fatalf("the engine's data was removed in part before the stop: %v", err)
	}
	keptForAgain(t, a, link)

	if err := os.Chmod(stuck, 0o700); err != nil {
		t.Fatal(err)
	}
	finishedBy(t, a, link)
}

// uninstallMeetingWalkError runs uninstall --remove-data on a set-up
// computer whose walk of the engine's folder fails with walkErr, and
// returns the refusal as main prints it; it checks that the stop keeps what
// running the removal again needs, and that the run again finishes it.
func uninstallMeetingWalkError(t *testing.T, walkErr error) (got string, lay layout) {
	t.Helper()
	a, link := uninstallable(t)
	saved := onSameFileSystem
	defer func() { onSameFileSystem = saved }()
	onSameFileSystem = func(root *os.Root, name string, base os.FileInfo) error {
		if name == "syncthing" {
			return walkErr
		}
		return saved(root, name, base)
	}
	got = refusalText(a.uninstall(context.Background(), []string{"--remove-data", "--yes"}))
	if !strings.Contains(got, "run again finishes the removal once that is fixed") {
		t.Fatalf("the refusal no longer says that running the removal again finishes it: %q", got)
	}
	keptForAgain(t, a, link)
	onSameFileSystem = saved
	finishedBy(t, a, link)
	return got, a.lay
}

// A look that fails for any other reason stops the removal too, named as it
// is: only a mount the walk found is something from another disk (#278).
func TestIssue278_AnotherErrorIsNamedAsItIs(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		folder func(lay layout) string
		why    string
	}{
		{
			name:   "a folder that cannot be read",
			err:    &lookError{folder: "syncthing/x", err: &fs.PathError{Op: "openat", Path: "syncthing/x", Err: syscall.EIO}},
			folder: func(lay layout) string { return filepath.Join(lay.Home, "x") },
			why:    syscall.EIO.Error(),
		},
		{
			name:   "an error without a folder",
			err:    errors.New("the walk ended"),
			folder: func(lay layout) string { return lay.Home },
			why:    "the walk ended",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, lay := uninstallMeetingWalkError(t, c.err)
			want := "VaultSync could not look into " + c.folder(lay) + " (" + c.why + "), so it stopped removing its files there."
			if strings.Contains(got, "another disk") || !strings.Contains(got, want) {
				t.Fatalf("%s, as main prints it: %q", c.name, got)
			}
		})
	}
}

// A look the folder's permissions refuse (EACCES) says to give your user
// access to it; one the system refuses (EPERM: macOS's privacy protection,
// a sandbox), which no change of the folder's permissions undoes, says only
// that — no advice that would be false (#278).
func TestIssue278_APermissionIsWordedByWhatRefusesTheLook(t *testing.T) {
	cases := []struct {
		name, want string
		errno      syscall.Errno
		advice     bool
	}{
		{"the folder's permissions", "VaultSync may not look into %s (%s), so it stopped removing its files there; give your user access to that folder.", syscall.EACCES, true},
		{"the system", "The system does not let VaultSync look into %s (%s), so it stopped removing its files there.", syscall.EPERM, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			walkErr := &lookError{folder: "syncthing/x", err: &fs.PathError{Op: "openat", Path: "syncthing/x", Err: c.errno}}
			got, lay := uninstallMeetingWalkError(t, walkErr)
			want := fmt.Sprintf(c.want, filepath.Join(lay.Home, "x"), c.errno.Error())
			if strings.Contains(got, "another disk") || !strings.Contains(got, want) {
				t.Fatalf("%s, as main prints it: %q", c.name, got)
			}
			if hasAdvice := strings.Contains(got, "give your user access"); hasAdvice != c.advice {
				t.Fatalf("%s: the advice to give your user access is there: %v, want %v: %q", c.name, hasAdvice, c.advice, got)
			}
		})
	}
}

// An entry gone meanwhile holds nothing from another disk: the walk passes
// over it and does not stop the removal — RemoveAll takes a missing entry
// as removed (#278).
func TestIssue278_WhatWentMeanwhileDoesNotStopTheRemoval(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	base, err := root.Stat(".")
	if err != nil {
		t.Fatal(err)
	}
	if err := sameFileSystem(root, "gone", base); err != nil {
		t.Fatalf("the walk stopped at an entry that is not there: %v", err)
	}
}

// Inside the walk too — a folder whose listing or an entry whose look finds
// it gone in the instant between the listing above and this look — what
// went is passed over, and every other failure is kept with the folder it
// concerns. A real race cannot be made to happen in a test: this is the one
// helper every failed look of the walk goes through (#278).
func TestIssue278_ALookBelowTheRootPassesOverOnlyWhatWent(t *testing.T) {
	gone := &fs.PathError{Op: "openat", Path: "syncthing/x/y", Err: syscall.ENOENT}
	if err := lookFailed("syncthing/x/y", gone); err != nil {
		t.Fatalf("a folder gone in the middle of the walk stopped it: %v", err)
	}
	if err := lookFailed("syncthing/x", &fs.PathError{Op: "fstatat", Path: "syncthing/x/y", Err: syscall.ENOENT}); err != nil {
		t.Fatalf("an entry gone in the middle of the walk stopped it: %v", err)
	}
	for _, errno := range []syscall.Errno{syscall.EACCES, syscall.EPERM, syscall.EIO, syscall.ENOTDIR, syscall.ELOOP} {
		inner := &fs.PathError{Op: "openat", Path: "/an/absolute/spelling/syncthing/x", Err: errno}
		err := lookFailed("syncthing/x", inner)
		var le *lookError
		if !errors.As(err, &le) || le.folder != "syncthing/x" || !errors.Is(err, errno) {
			t.Errorf("%v below the root: %#v, want a lookError naming syncthing/x", errno, err)
		}
	}
}
