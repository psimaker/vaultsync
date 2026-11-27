package main

import (
	"context"
	"errors"
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
			a, link := uninstallable(t)
			saved := onSameFileSystem
			defer func() { onSameFileSystem = saved }()
			onSameFileSystem = func(root *os.Root, name string, base os.FileInfo) error {
				if name == "syncthing" {
					return c.err
				}
				return saved(root, name, base)
			}
			got := refusalText(a.uninstall(context.Background(), []string{"--remove-data", "--yes"}))
			want := "VaultSync could not look into " + c.folder(a.lay) + " (" + c.why + "), so it stopped removing its files there."
			if strings.Contains(got, "another disk") || !strings.Contains(got, want) {
				t.Fatalf("%s, as main prints it: %q", c.name, got)
			}
			if !strings.Contains(got, "run again finishes the removal once that is fixed") {
				t.Fatalf("the refusal no longer says that running the removal again finishes it: %q", got)
			}
			keptForAgain(t, a, link)
			onSameFileSystem = saved
			finishedBy(t, a, link)
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
