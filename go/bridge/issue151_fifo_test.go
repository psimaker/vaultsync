//go:build darwin || linux

package bridge

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A read opens without waiting and checks the type on the opened file before
// it reads: an entry that is a named pipe by the time it is opened is refused
// at once (#151).
func TestIssue151_AReadNeverWaitsOnAnEntryThatIsNotARegularFile(t *testing.T) {
	base := issue151Engine(t)
	const folderID = "issue151fifo"
	folder := issue151Folder(t, base, folderID)
	issue151Write(t, filepath.Join(folder, issue151ConflictName), "issue-151-copy")
	pipe := filepath.Join(folder, issue151ConflictName)
	issue151ChangeAfterCheck(t, issue151ConflictName, func(path string) {
		if err := os.Rename(path, filepath.Join(filepath.Dir(path), "moved.md")); err != nil {
			t.Errorf("rename %q: %v", filepath.Base(path), err)
		}
		if err := unix.Mkfifo(path, 0o600); err != nil {
			t.Errorf("mkfifo %q: %v", filepath.Base(path), err)
		}
	})

	done := make(chan string, 1)
	go func() { done <- ReadFileContent(folderID, issue151ConflictName) }()
	select {
	case got := <-done:
		if got != "error:not a regular file" {
			t.Errorf("ReadFileContent = %q, want the refusal of an entry that is not a regular file", got)
		}
	case <-time.After(10 * time.Second):
		t.Error("ReadFileContent still waits on the entry after 10 seconds")
		// Open the other end, so the waiting read ends with the test.
		go func() {
			if w, err := os.OpenFile(pipe, os.O_WRONLY, 0); err == nil {
				w.Close()
			}
		}()
		<-done
	}
	issue144AssertFileBytes(t, filepath.Join(folder, "moved.md"), []byte("issue-151-copy"))
}
