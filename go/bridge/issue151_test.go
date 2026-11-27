package bridge

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The conflict list, the comparison view's read and the actions on a
// conflict work on regular files inside the vault's own folder (#151): an
// entry that is not a regular file is not listed and not acted on.

const (
	issue151ConflictName = "doc.sync-conflict-20260406-100000-DEF5678.md"
	issue151OriginalName = "doc.md"
	issue151KeepBothName = "doc.conflict-DEF5678.md"
)

// issue151Engine starts the engine once for a test; each case adds a folder
// of its own with issue151Folder.
func issue151Engine(t *testing.T) string {
	t.Helper()
	base := testConfigDir(t)
	if errMsg := StartSyncthing(base); errMsg != "" {
		t.Fatalf("StartSyncthing() failed: %s", errMsg)
	}
	return base
}

func issue151Folder(t *testing.T, base, folderID string) string {
	t.Helper()
	folderPath := filepath.Join(base, folderID)
	if errMsg := AddFolder(folderID, folderID, folderPath); errMsg != "" {
		t.Fatalf("AddFolder failed: %s", errMsg)
	}
	return folderPath
}

func issue151Write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir for %q: %v", filepath.Base(path), err)
	}
	issue144WriteAndReadBack(t, path, []byte(data))
}

func issue151Link(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("create link %q: %v", filepath.Base(link), err)
	}
}

func issue151AssertLink(t *testing.T, link, wantTarget string) {
	t.Helper()
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat %q: %v", filepath.Base(link), err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%q mode = %v, want a link", filepath.Base(link), info.Mode())
	}
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink %q: %v", filepath.Base(link), err)
	}
	if got != wantTarget {
		t.Errorf("%q leads to %q, want %q", filepath.Base(link), got, wantTarget)
	}
}

func issue151Conflicts(t *testing.T, folderID string) []ConflictFile {
	t.Helper()
	var conflicts []ConflictFile
	if err := json.Unmarshal([]byte(GetConflictFilesJSON(folderID)), &conflicts); err != nil {
		t.Fatalf("GetConflictFilesJSON unmarshal: %v", err)
	}
	return conflicts
}

func issue151Listed(conflicts []ConflictFile, conflictPath string) bool {
	for _, c := range conflicts {
		if c.ConflictPath == conflictPath {
			return true
		}
	}
	return false
}

type issue151RemoveResult struct {
	Removed int    `json:"removed"`
	Error   string `json:"error"`
}

func issue151RemoveCopies(t *testing.T, folderID, originalPath string) issue151RemoveResult {
	t.Helper()
	var result issue151RemoveResult
	raw := RemoveConflictFilesForOriginal(folderID, originalPath)
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("RemoveConflictFilesForOriginal unmarshal: %v (raw %q)", err, raw)
	}
	return result
}

func TestIssue151_TheConflictListHoldsOnlyRegularFiles(t *testing.T) {
	base := issue151Engine(t)
	const folderID = "issue151list"
	folder := issue151Folder(t, base, folderID)

	issue151Write(t, filepath.Join(folder, "Notes", "target.md"), "issue-151-target")
	issue151Write(t, filepath.Join(folder, "regular.md"), "issue-151-original")
	issue151Write(t, filepath.Join(folder, "regular.sync-conflict-20260406-100000-AAA1111.md"), "issue-151-regular-copy")
	issue151Link(t, filepath.Join("Notes", "target.md"), filepath.Join(folder, "linked.sync-conflict-20260406-100000-BBB2222.md"))
	if err := os.Mkdir(filepath.Join(folder, "dir.sync-conflict-20260406-100000-CCC3333.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Only directories are entered.
	issue151Write(t, filepath.Join(folder, "Real", "nested.sync-conflict-20260406-100000-DDD4444.md"), "issue-151-nested-copy")
	issue151Link(t, "Real", filepath.Join(folder, "Linked"))

	conflicts := issue151Conflicts(t, folderID)
	if !issue151Listed(conflicts, "regular.sync-conflict-20260406-100000-AAA1111.md") {
		t.Errorf("the regular conflict copy is not listed: %+v", conflicts)
	}
	if !issue151Listed(conflicts, filepath.Join("Real", "nested.sync-conflict-20260406-100000-DDD4444.md")) {
		t.Errorf("the regular conflict copy in a subdirectory is not listed: %+v", conflicts)
	}
	for _, notListed := range []string{
		"linked.sync-conflict-20260406-100000-BBB2222.md",
		"dir.sync-conflict-20260406-100000-CCC3333.md",
		filepath.Join("Linked", "nested.sync-conflict-20260406-100000-DDD4444.md"),
	} {
		if issue151Listed(conflicts, notListed) {
			t.Errorf("%q is listed, want only regular files: %+v", notListed, conflicts)
		}
	}
	if len(conflicts) != 2 {
		t.Errorf("listed %d conflicts, want 2: %+v", len(conflicts), conflicts)
	}
}

func TestIssue151_AConflictReadReadsOnlyARegularFile(t *testing.T) {
	base := issue151Engine(t)
	const folderID = "issue151read"
	folder := issue151Folder(t, base, folderID)

	issue151Write(t, filepath.Join(folder, "Notes", "target.md"), "issue-151-target")
	issue151Write(t, filepath.Join(folder, "regular.md"), "issue-151-regular")
	issue151Link(t, filepath.Join("Notes", "target.md"), filepath.Join(folder, "linked.md"))
	issue151Link(t, "Notes", filepath.Join(folder, "LinkedDir"))

	if got := ReadFileContent(folderID, "regular.md"); got != "issue-151-regular" {
		t.Errorf("ReadFileContent(regular file) = %q, want its content", got)
	}
	if got := ReadFileContent(folderID, filepath.Join("Notes", "target.md")); got != "issue-151-target" {
		t.Errorf("ReadFileContent(regular file in a subdirectory) = %q, want its content", got)
	}
	for _, relPath := range []string{"linked.md", filepath.Join("LinkedDir", "target.md"), "Notes", "."} {
		if got := ReadFileContent(folderID, relPath); got != "error:not a regular file" {
			t.Errorf("ReadFileContent(%q) = %q, want the refusal of an entry that is not a regular file", relPath, got)
		}
	}
	if got := ReadFileContent(folderID, "missing.md"); !strings.HasPrefix(got, "error:") || strings.Contains(got, folder) || strings.Contains(got, "missing.md") {
		t.Errorf("ReadFileContent(missing file) = %q, want a refusal without path or name", got)
	}
	issue144AssertFileBytes(t, filepath.Join(folder, "Notes", "target.md"), []byte("issue-151-target"))
}

func TestIssue151_AConflictActionActsOnlyOnARegularFile(t *testing.T) {
	base := issue151Engine(t)

	// setup makes a folder with a regular original, and an entry under the
	// conflict copy's name that is not a regular file.
	setup := func(t *testing.T, folderID string) (folder, target string) {
		t.Helper()
		folder = issue151Folder(t, base, folderID)
		target = filepath.Join(folder, "Notes", "target.md")
		issue151Write(t, target, "issue-151-target")
		issue151Write(t, filepath.Join(folder, issue151OriginalName), "issue-151-original")
		issue151Link(t, filepath.Join("Notes", "target.md"), filepath.Join(folder, issue151ConflictName))
		return folder, target
	}
	assertUntouched := func(t *testing.T, folder, target string) {
		t.Helper()
		issue144AssertFileBytes(t, filepath.Join(folder, issue151OriginalName), []byte("issue-151-original"))
		issue151AssertLink(t, filepath.Join(folder, issue151ConflictName), filepath.Join("Notes", "target.md"))
		issue144AssertFileBytes(t, target, []byte("issue-151-target"))
		issue144AssertPathMissing(t, filepath.Join(folder, issue151KeepBothName))
		issue143AssertNoOperationTemps(t, folder)
	}

	t.Run("keep the other version", func(t *testing.T) {
		const folderID = "issue151keepother"
		folder, target := setup(t, folderID)
		if errMsg := ResolveConflict(folderID, issue151ConflictName, true); errMsg != conflictNotRegularError {
			t.Errorf("ResolveConflict(keepConflict: true) = %q, want %q", errMsg, conflictNotRegularError)
		}
		assertUntouched(t, folder, target)
	})

	t.Run("keep this version", func(t *testing.T) {
		const folderID = "issue151keepthis"
		folder, target := setup(t, folderID)
		if errMsg := ResolveConflict(folderID, issue151ConflictName, false); errMsg != conflictNotRegularError {
			t.Errorf("ResolveConflict(keepConflict: false) = %q, want %q", errMsg, conflictNotRegularError)
		}
		assertUntouched(t, folder, target)
	})

	t.Run("keep both", func(t *testing.T) {
		const folderID = "issue151keepboth"
		folder, target := setup(t, folderID)
		if errMsg := KeepBothConflict(folderID, issue151ConflictName); errMsg != conflictNotRegularError {
			t.Errorf("KeepBothConflict = %q, want %q", errMsg, conflictNotRegularError)
		}
		assertUntouched(t, folder, target)
	})

	t.Run("remove the copies of an original", func(t *testing.T) {
		const folderID = "issue151removecopies"
		folder, target := setup(t, folderID)
		regularCopy := filepath.Join(folder, "doc.sync-conflict-20260407-100000-EEE5555.md")
		issue151Write(t, regularCopy, "issue-151-regular-copy")

		result := issue151RemoveCopies(t, folderID, issue151OriginalName)
		if result.Error != "" || result.Removed != 1 {
			t.Errorf("RemoveConflictFilesForOriginal = %+v, want the one regular copy removed", result)
		}
		issue144AssertPathMissing(t, regularCopy)
		assertUntouched(t, folder, target)
	})

	t.Run("keep the other version over an original that is not a regular file", func(t *testing.T) {
		const folderID = "issue151originallink"
		folder := issue151Folder(t, base, folderID)
		target := filepath.Join(folder, "Notes", "target.md")
		issue151Write(t, target, "issue-151-target")
		issue151Link(t, filepath.Join("Notes", "target.md"), filepath.Join(folder, issue151OriginalName))
		issue151Write(t, filepath.Join(folder, issue151ConflictName), "issue-151-copy")

		if errMsg := ResolveConflict(folderID, issue151ConflictName, true); errMsg != originalNotRegularError {
			t.Errorf("ResolveConflict(keepConflict: true) = %q, want %q", errMsg, originalNotRegularError)
		}
		issue151AssertLink(t, filepath.Join(folder, issue151OriginalName), filepath.Join("Notes", "target.md"))
		issue144AssertFileBytes(t, target, []byte("issue-151-target"))
		issue144AssertFileBytes(t, filepath.Join(folder, issue151ConflictName), []byte("issue-151-copy"))
		issue143AssertNoOperationTemps(t, folder)
	})
}

// The read and every action work only in directories of the folder: a path
// whose directory is not a directory is refused, and nothing changes.
func TestIssue151_AConflictActionActsOnlyInADirectoryOfTheFolder(t *testing.T) {
	base := issue151Engine(t)

	// setup lists a conflict copy in Sub; then Sub is not a directory.
	setup := func(t *testing.T, folderID string) (folder string) {
		t.Helper()
		folder = issue151Folder(t, base, folderID)
		issue151Write(t, filepath.Join(folder, "Sub", issue151OriginalName), "issue-151-sub-original")
		issue151Write(t, filepath.Join(folder, "Sub", issue151ConflictName), "issue-151-sub-copy")
		issue151Write(t, filepath.Join(folder, "Other", issue151OriginalName), "issue-151-other-original")
		issue151Write(t, filepath.Join(folder, "Other", issue151ConflictName), "issue-151-other-copy")

		if !issue151Listed(issue151Conflicts(t, folderID), filepath.Join("Sub", issue151ConflictName)) {
			t.Fatal("the conflict copy in Sub is not listed")
		}
		if err := os.Rename(filepath.Join(folder, "Sub"), filepath.Join(folder, "Moved")); err != nil {
			t.Fatal(err)
		}
		issue151Link(t, "Other", filepath.Join(folder, "Sub"))
		return folder
	}
	assertUntouched := func(t *testing.T, folder string) {
		t.Helper()
		issue144AssertFileBytes(t, filepath.Join(folder, "Other", issue151OriginalName), []byte("issue-151-other-original"))
		issue144AssertFileBytes(t, filepath.Join(folder, "Other", issue151ConflictName), []byte("issue-151-other-copy"))
		issue144AssertPathMissing(t, filepath.Join(folder, "Other", issue151KeepBothName))
		issue143AssertNoOperationTemps(t, filepath.Join(folder, "Other"))
		issue144AssertFileBytes(t, filepath.Join(folder, "Moved", issue151OriginalName), []byte("issue-151-sub-original"))
		issue144AssertFileBytes(t, filepath.Join(folder, "Moved", issue151ConflictName), []byte("issue-151-sub-copy"))
		issue151AssertLink(t, filepath.Join(folder, "Sub"), "Other")
	}
	conflictPath := filepath.Join("Sub", issue151ConflictName)

	t.Run("read", func(t *testing.T) {
		const folderID = "issue151dirread"
		folder := setup(t, folderID)
		for _, relPath := range []string{conflictPath, filepath.Join("Sub", issue151OriginalName)} {
			if got := ReadFileContent(folderID, relPath); got != "error:not a regular file" {
				t.Errorf("ReadFileContent(%q) = %q, want the refusal of an entry that is not a regular file", relPath, got)
			}
		}
		assertUntouched(t, folder)
	})

	t.Run("keep the other version", func(t *testing.T) {
		const folderID = "issue151dirkeepother"
		folder := setup(t, folderID)
		if errMsg := ResolveConflict(folderID, conflictPath, true); errMsg != conflictNotRegularError {
			t.Errorf("ResolveConflict(keepConflict: true) = %q, want %q", errMsg, conflictNotRegularError)
		}
		assertUntouched(t, folder)
	})

	t.Run("keep this version", func(t *testing.T) {
		const folderID = "issue151dirkeepthis"
		folder := setup(t, folderID)
		if errMsg := ResolveConflict(folderID, conflictPath, false); errMsg != conflictNotRegularError {
			t.Errorf("ResolveConflict(keepConflict: false) = %q, want %q", errMsg, conflictNotRegularError)
		}
		assertUntouched(t, folder)
	})

	t.Run("keep both", func(t *testing.T) {
		const folderID = "issue151dirkeepboth"
		folder := setup(t, folderID)
		if errMsg := KeepBothConflict(folderID, conflictPath); errMsg != conflictNotRegularError {
			t.Errorf("KeepBothConflict = %q, want %q", errMsg, conflictNotRegularError)
		}
		assertUntouched(t, folder)
	})

	t.Run("remove the copies of an original", func(t *testing.T) {
		const folderID = "issue151dirremovecopies"
		folder := setup(t, folderID)
		result := issue151RemoveCopies(t, folderID, filepath.Join("Sub", issue151OriginalName))
		if result.Removed != 0 || result.Error != "the original's directory is not a directory" {
			t.Errorf("RemoveConflictFilesForOriginal = %+v, want a refusal with nothing removed", result)
		}
		assertUntouched(t, folder)
	})
}

// issue151ChangeAfterCheck calls change with the entry's path once the
// conflict functions have checked name for the first time.
func issue151ChangeAfterCheck(t *testing.T, name string, change func(path string)) {
	t.Helper()
	check := lstatInFolder
	t.Cleanup(func() { lstatInFolder = check })
	changed := false
	lstatInFolder = func(dir *os.Root, entry string) (os.FileInfo, error) {
		info, err := check(dir, entry)
		if entry == name && !changed {
			changed = true
			change(filepath.Join(dir.Name(), entry))
		}
		return info, err
	}
}

// issue151MoveAndLink renames the entry at path to moved, in its directory,
// and puts a link to target in its place.
func issue151MoveAndLink(t *testing.T, target, moved string) func(path string) {
	return func(path string) {
		if err := os.Rename(path, filepath.Join(filepath.Dir(path), moved)); err != nil {
			t.Errorf("rename %q: %v", filepath.Base(path), err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Errorf("link %q: %v", filepath.Base(path), err)
		}
	}
}

// issue151MoveAndMkdir renames the entry at path to moved, in its directory,
// and puts an empty directory in its place.
func issue151MoveAndMkdir(t *testing.T, moved string) func(path string) {
	return func(path string) {
		if err := os.Rename(path, filepath.Join(filepath.Dir(path), moved)); err != nil {
			t.Errorf("rename %q: %v", filepath.Base(path), err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Errorf("mkdir %q: %v", filepath.Base(path), err)
		}
	}
}

// The entry opened is the entry checked: the file read must be the one
// checked, and so must every directory on the way. A removal removes a file
// and never a directory.
func TestIssue151_TheEntryOpenedIsTheEntryChecked(t *testing.T) {
	base := issue151Engine(t)

	t.Run("file", func(t *testing.T) {
		const folderID = "issue151changedfile"
		folder := issue151Folder(t, base, folderID)
		issue151Write(t, filepath.Join(folder, "Notes", "target.md"), "issue-151-target")
		issue151Write(t, filepath.Join(folder, issue151ConflictName), "issue-151-copy")
		issue151ChangeAfterCheck(t, issue151ConflictName, issue151MoveAndLink(t, filepath.Join("Notes", "target.md"), "moved.md"))

		if got := ReadFileContent(folderID, issue151ConflictName); got != "error:not a regular file" {
			t.Errorf("ReadFileContent = %q, want the refusal of an entry that is not a regular file", got)
		}
		issue144AssertFileBytes(t, filepath.Join(folder, "Notes", "target.md"), []byte("issue-151-target"))
		issue144AssertFileBytes(t, filepath.Join(folder, "moved.md"), []byte("issue-151-copy"))
	})

	t.Run("keep the other version", func(t *testing.T) {
		const folderID = "issue151changedkeepother"
		folder := issue151Folder(t, base, folderID)
		issue151Write(t, filepath.Join(folder, "Notes", "target.md"), "issue-151-target")
		issue151Write(t, filepath.Join(folder, issue151OriginalName), "issue-151-original")
		issue151Write(t, filepath.Join(folder, issue151ConflictName), "issue-151-copy")
		issue151ChangeAfterCheck(t, issue151ConflictName, issue151MoveAndLink(t, filepath.Join("Notes", "target.md"), "moved.md"))

		if errMsg := ResolveConflict(folderID, issue151ConflictName, true); errMsg != conflictNotRegularError {
			t.Errorf("ResolveConflict(keepConflict: true) = %q, want %q", errMsg, conflictNotRegularError)
		}
		issue144AssertFileBytes(t, filepath.Join(folder, issue151OriginalName), []byte("issue-151-original"))
		issue144AssertFileBytes(t, filepath.Join(folder, "Notes", "target.md"), []byte("issue-151-target"))
		issue144AssertFileBytes(t, filepath.Join(folder, "moved.md"), []byte("issue-151-copy"))
		issue151AssertLink(t, filepath.Join(folder, issue151ConflictName), filepath.Join("Notes", "target.md"))
		issue143AssertNoOperationTemps(t, folder)
	})

	t.Run("directory", func(t *testing.T) {
		const folderID = "issue151changeddir"
		folder := issue151Folder(t, base, folderID)
		issue151Write(t, filepath.Join(folder, "Sub", issue151ConflictName), "issue-151-sub-copy")
		issue151Write(t, filepath.Join(folder, "Other", issue151ConflictName), "issue-151-other-copy")
		issue151ChangeAfterCheck(t, "Sub", issue151MoveAndLink(t, "Other", "Moved"))

		if got := ReadFileContent(folderID, filepath.Join("Sub", issue151ConflictName)); got != "error:not a regular file" {
			t.Errorf("ReadFileContent = %q, want the refusal of an entry that is not a regular file", got)
		}
		issue144AssertFileBytes(t, filepath.Join(folder, "Other", issue151ConflictName), []byte("issue-151-other-copy"))
		issue144AssertFileBytes(t, filepath.Join(folder, "Moved", issue151ConflictName), []byte("issue-151-sub-copy"))
	})

	t.Run("keep this version", func(t *testing.T) {
		const folderID = "issue151changedkeepthis"
		folder := issue151Folder(t, base, folderID)
		issue151Write(t, filepath.Join(folder, issue151ConflictName), "issue-151-copy")
		issue151ChangeAfterCheck(t, issue151ConflictName, issue151MoveAndMkdir(t, "moved.md"))

		if errMsg := ResolveConflict(folderID, issue151ConflictName, false); !strings.HasPrefix(errMsg, "delete conflict file: ") {
			t.Errorf("ResolveConflict(keepConflict: false) = %q, want the removal refused", errMsg)
		}
		if info, err := os.Lstat(filepath.Join(folder, issue151ConflictName)); err != nil || !info.IsDir() {
			t.Errorf("the directory in the copy's place is gone or changed: %v", err)
		}
		issue144AssertFileBytes(t, filepath.Join(folder, "moved.md"), []byte("issue-151-copy"))
	})

	t.Run("remove the copies of an original", func(t *testing.T) {
		const folderID = "issue151changedremovecopies"
		folder := issue151Folder(t, base, folderID)
		issue151Write(t, filepath.Join(folder, issue151OriginalName), "issue-151-original")
		issue151Write(t, filepath.Join(folder, issue151ConflictName), "issue-151-copy")
		issue151ChangeAfterCheck(t, issue151ConflictName, issue151MoveAndMkdir(t, "moved.md"))

		result := issue151RemoveCopies(t, folderID, issue151OriginalName)
		if result.Removed != 0 || !strings.HasPrefix(result.Error, "remove conflict copy: ") {
			t.Errorf("RemoveConflictFilesForOriginal = %+v, want the removal refused", result)
		}
		if info, err := os.Lstat(filepath.Join(folder, issue151ConflictName)); err != nil || !info.IsDir() {
			t.Errorf("the directory in the copy's place is gone or changed: %v", err)
		}
		issue144AssertFileBytes(t, filepath.Join(folder, issue151OriginalName), []byte("issue-151-original"))
	})
}

// "Keep this version" removes a conflict copy only: a name that is not a
// conflict copy's is refused, and the file stays.
func TestIssue151_KeepThisVersionRemovesOnlyAConflictCopy(t *testing.T) {
	base := issue151Engine(t)
	const folderID = "issue151keepthisname"
	folder := issue151Folder(t, base, folderID)
	issue151Write(t, filepath.Join(folder, issue151OriginalName), "issue-151-original")
	issue151Write(t, filepath.Join(folder, "Notes", "note.md"), "issue-151-note")

	for _, relPath := range []string{issue151OriginalName, filepath.Join("Notes", "note.md")} {
		if errMsg := ResolveConflict(folderID, relPath, false); errMsg != "invalid conflict filename" {
			t.Errorf("ResolveConflict(%q, keepConflict: false) = %q, want %q", relPath, errMsg, "invalid conflict filename")
		}
	}
	issue144AssertFileBytes(t, filepath.Join(folder, issue151OriginalName), []byte("issue-151-original"))
	issue144AssertFileBytes(t, filepath.Join(folder, "Notes", "note.md"), []byte("issue-151-note"))
}

// The list takes each entry's type from the directory read and looks again
// only at a name that matches a conflict copy's, and at each directory it
// enters.
func TestIssue151_TheListLooksAgainOnlyAtConflictCopiesAndDirectories(t *testing.T) {
	base := issue151Engine(t)
	const folderID = "issue151listlooks"
	folder := issue151Folder(t, base, folderID)
	for i := 0; i < 30; i++ {
		issue151Write(t, filepath.Join(folder, fmt.Sprintf("note%02d.md", i)), "issue-151-note")
		issue151Write(t, filepath.Join(folder, "Sub", fmt.Sprintf("note%02d.md", i)), "issue-151-note")
	}
	issue151Write(t, filepath.Join(folder, issue151ConflictName), "issue-151-copy")
	issue151Write(t, filepath.Join(folder, "Sub", issue151ConflictName), "issue-151-copy")

	directories := 0
	if err := filepath.WalkDir(folder, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && path != folder {
			directories++
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}

	check := lstatInFolder
	t.Cleanup(func() { lstatInFolder = check })
	looks := 0
	lstatInFolder = func(dir *os.Root, entry string) (os.FileInfo, error) {
		looks++
		return check(dir, entry)
	}

	if conflicts := issue151Conflicts(t, folderID); len(conflicts) != 2 {
		t.Fatalf("listed %d conflicts, want 2: %+v", len(conflicts), conflicts)
	}
	if want := directories + 2; looks != want {
		t.Errorf("the list looked at %d entries again, want %d (%d directories and 2 conflict copies)", looks, want, directories)
	}
}

// No answer of the conflict functions carries a path or a name, whatever kind
// of error the file system gives.
func TestIssue151_NoAnswerCarriesAPathOrAName(t *testing.T) {
	const name = "Secret Note.md"
	const path = "/Vault/Secret Note.md"
	kinds := map[string]error{
		"path":    &os.PathError{Op: "openat", Path: path, Err: syscall.EIO},
		"link":    &os.LinkError{Op: "renameat", Old: name, New: path, Err: syscall.EIO},
		"syscall": &os.SyscallError{Syscall: "unlinkat " + path, Err: syscall.EIO},
		"wrapped": fmt.Errorf("step %s: %w", name, &os.PathError{Op: "openat", Path: path, Err: syscall.EIO}),
	}
	assertClean := func(t *testing.T, where, answer string) {
		t.Helper()
		if answer == "" {
			t.Errorf("%s: no answer, want a failure", where)
		}
		if strings.Contains(answer, "Secret") || strings.Contains(answer, "/Vault") {
			t.Errorf("%s: answer %q carries a path or a name", where, answer)
		}
	}

	for kind, injected := range kinds {
		t.Run(kind, func(t *testing.T) {
			assertClean(t, "conflictEntryError", conflictEntryError(injected))
			assertClean(t, "readEntryError", readEntryError(injected))

			keepBoth := issue143NewFileFixture(t)
			ops := systemKeepBothFileOperations(issue143Root(t, keepBoth.dir))
			lstatFails := ops
			lstatFails.lstat = func(string) (os.FileInfo, error) { return nil, injected }
			assertClean(t, "keep both, check", keepBothConflictFile(filepath.Base(keepBoth.conflictPath), lstatFails))
			renameFails := ops
			renameFails.renameNoReplace = func(string, string) error { return injected }
			assertClean(t, "keep both, rename", keepBothConflictFile(filepath.Base(keepBoth.conflictPath), renameFails))

			faults := map[string]func(*conflictFileOperations){
				"read": func(ops *conflictFileOperations) {
					ops.readFile = func(string) ([]byte, error) { return nil, injected }
				},
				"lstat": func(ops *conflictFileOperations) {
					ops.lstat = func(string) (os.FileInfo, error) { return nil, injected }
				},
				"create": func(ops *conflictFileOperations) {
					ops.createTemp = func(string) (conflictTempFile, error) { return nil, injected }
				},
				"write": func(ops *conflictFileOperations) {
					issue143InjectTempFault(ops, func(file *issue143FaultingTempFile) {
						file.write = func([]byte) (int, error) { return 0, injected }
					})
				},
				"chmod": func(ops *conflictFileOperations) {
					issue143InjectTempFault(ops, func(file *issue143FaultingTempFile) {
						file.chmod = func(os.FileMode) error { return injected }
					})
				},
				"sync": func(ops *conflictFileOperations) {
					issue143InjectTempFault(ops, func(file *issue143FaultingTempFile) {
						file.sync = func() error { return injected }
					})
				},
				"close": func(ops *conflictFileOperations) {
					issue143InjectTempFault(ops, func(file *issue143FaultingTempFile) {
						file.close = func() error {
							file.conflictTempFile.Close()
							return injected
						}
					})
				},
				"rename": func(ops *conflictFileOperations) { ops.rename = func(string, string) error { return injected } },
				"remove": func(ops *conflictFileOperations) { ops.remove = func(string) error { return injected } },
			}
			for step, inject := range faults {
				fixture := issue143NewFileFixture(t)
				ops := systemConflictFileOperations(issue143Root(t, fixture.dir))
				inject(&ops)
				err := replaceConflictAndRemoveSource(filepath.Base(fixture.conflictPath), filepath.Base(fixture.originalPath), ops)
				if err == nil {
					t.Errorf("keep the other version, %s: no error", step)
					continue
				}
				assertClean(t, "keep the other version, "+step, err.Error())
			}

			copies := issue143NewFileFixture(t)
			copyOps := systemConflictCopyOperations(issue143Root(t, copies.dir))
			readDirFails := copyOps
			readDirFails.readDir = func() ([]os.DirEntry, error) { return nil, injected }
			_, answer := removeConflictCopies(filepath.Base(copies.originalPath), readDirFails)
			assertClean(t, "remove copies, read", answer)
			lstatCopyFails := copyOps
			lstatCopyFails.lstat = func(string) (os.FileInfo, error) { return nil, injected }
			_, answer = removeConflictCopies(filepath.Base(copies.originalPath), lstatCopyFails)
			assertClean(t, "remove copies, check", answer)
			removeCopyFails := copyOps
			removeCopyFails.remove = func(string) error { return injected }
			_, answer = removeConflictCopies(filepath.Base(copies.originalPath), removeCopyFails)
			assertClean(t, "remove copies, removal", answer)
		})
	}
}
