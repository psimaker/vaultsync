//go:build !windows

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// #490: the server and commands such as `vaultsync-hub code` are two
// processes that change one state file. Each change is a fresh read, the
// change and a rename; two at once used to leave only the later one. Every
// read-modify-write now holds one lock shared by all of them (decision 097).

const issue490Writes = 150

// seedCode gives the state file an open code to count failures against.
func seedCode(t *testing.T, store *stateStore) {
	t.Helper()
	now := time.Now()
	if _, err := store.update(func(st *hubState) error {
		st.Pairing = &pairingCodeState{Scalar: []byte{4, 9, 0}, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// The writers pause a moment between writes, as the server and a command
// do; back to back, one could take the lock again before the other's next
// look, and the test would measure fairness instead of lost changes.
func countFailures(store *stateStore, n int) error {
	for i := 0; i < n; i++ {
		time.Sleep(time.Millisecond)
		if _, err := store.update(func(st *hubState) error {
			st.Pairing.Failures++
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func recordDevices(store *stateStore, n int) error {
	for i := 0; i < n; i++ {
		time.Sleep(time.Millisecond)
		if _, err := store.update(func(st *hubState) error {
			st.recordDevice(pairedDevice{DeviceID: fmt.Sprintf("D%04d", i), Name: "d"})
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func checkNothingLost(t *testing.T, path string) {
	t.Helper()
	st, err := newStateStore(path).load()
	if err != nil {
		t.Fatal(err)
	}
	if st.Pairing == nil || st.Pairing.Failures != issue490Writes || len(st.Devices) != issue490Writes {
		failures := -1
		if st.Pairing != nil {
			failures = st.Pairing.Failures
		}
		t.Fatalf("lost changes: %d of %d failed attempts and %d of %d pairings in the file", failures, issue490Writes, len(st.Devices), issue490Writes)
	}
}

// Two stores on one file hold it as two processes do: each its own mutex and
// its own descriptor of the lock file, which flock keeps apart.
func TestIssue490_TwoWritersOnOneFileLoseNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	server, command := newStateStore(path), newStateStore(path)
	seedCode(t, server)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := countFailures(server, issue490Writes); err != nil {
			t.Error(err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := recordDevices(command, issue490Writes); err != nil {
			t.Error(err)
		}
	}()
	wg.Wait()
	checkNothingLost(t, path)
}

// TestIssue490_ChildWriter is the second process of the next test; it runs
// only when that test starts it.
func TestIssue490_ChildWriter(t *testing.T) {
	path := os.Getenv("VAULTSYNC_HUB_490_CHILD")
	if path == "" {
		t.Skip("the second process of TestIssue490_AnotherProcessAndThisOneLoseNothing")
	}
	if err := countFailures(newStateStore(path), issue490Writes); err != nil {
		t.Fatal(err)
	}
}

func TestIssue490_AnotherProcessAndThisOneLoseNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	seedCode(t, newStateStore(path))
	child := exec.Command(os.Args[0], "-test.run=^TestIssue490_ChildWriter$", "-test.count=1")
	child.Env = append(os.Environ(), "VAULTSYNC_HUB_490_CHILD="+path)
	var out bytes.Buffer
	child.Stdout, child.Stderr = &out, &out
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	err := recordDevices(newStateStore(path), issue490Writes)
	if werr := child.Wait(); werr != nil {
		t.Fatalf("the other process: %v\n%s", werr, out.String())
	}
	if err != nil {
		t.Fatal(err)
	}
	checkNothingLost(t, path)
}

// A write waits a bounded time for the lock, then fails and changes nothing.
func TestIssue490_AWriteWaitsABoundedTimeThenFailsClosed(t *testing.T) {
	saved := stateLockWait
	stateLockWait = 300 * time.Millisecond
	t.Cleanup(func() { stateLockWait = saved })
	path := filepath.Join(t.TempDir(), "state.json")
	store := newStateStore(path)
	seedCode(t, store)
	before, _ := os.ReadFile(path)

	held, err := lockStateFile(path)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = store.update(func(st *hubState) error { st.Pairing.Failures++; return nil })
	took := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "is busy") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("a write while another holds the lock: %v", err)
	}
	if took < stateLockWait || took > stateLockWait+2*time.Second {
		t.Fatalf("waited %s for a %s bound", took, stateLockWait)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("a write without the lock changed the file")
	}
	held()
	if _, err := store.update(func(st *hubState) error { st.Pairing.Failures++; return nil }); err != nil {
		t.Fatalf("after the lock was released: %v", err)
	}
}

// The lock file is private to the state's owner, opened for writing (an
// exclusive lock on NFS needs that), never through a link, and a regular
// file only (decision 097).
func TestIssue490_ANewLockFileIsPrivate(t *testing.T) {
	old := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(old) })
	path := filepath.Join(t.TempDir(), "state.json")
	seedCode(t, newStateStore(path))
	for _, f := range []string{path, path + ".lock"} {
		if fi, err := os.Stat(f); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v, want 0600", f, fi.Mode().Perm(), err)
		}
	}
}

func TestIssue490_AStrayLockFileIsPutRight(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := newStateStore(path)
	seedCode(t, store)
	if err := os.Chmod(path+".lock", 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.update(func(st *hubState) error { st.Pairing.Failures++; return nil }); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path + ".lock"); fi.Mode().Perm() != 0o600 {
		t.Fatalf("a stray 0644 lock file is %v after a write", fi.Mode().Perm())
	}
}

func TestIssue490_ALockFileIsNeverALinkOrAnythingButAFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	store := newStateStore(path)
	seedCode(t, store)
	lock := path + ".lock"
	before, _ := os.ReadFile(path)
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	existing := filepath.Join(t.TempDir(), "existing")
	if err := os.WriteFile(existing, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		make func() error
		says string
	}{
		{"a link to nothing", func() error { return os.Symlink(elsewhere, lock) }, "is a link"},
		{"a link to a file", func() error { return os.Symlink(existing, lock) }, "is a link"},
		{"a directory", func() error { return os.Mkdir(lock, 0o700) }, "not a regular file"},
		{"a pipe", func() error { return syscall.Mkfifo(lock, 0o600) }, "not a regular file"},
	}
	for _, c := range cases {
		if err := os.RemoveAll(lock); err != nil {
			t.Fatal(err)
		}
		if err := c.make(); err != nil {
			t.Fatal(err)
		}
		_, err := store.update(func(st *hubState) error { st.Pairing.Failures++; return nil })
		if err == nil || !strings.Contains(err.Error(), c.says) || !strings.Contains(err.Error(), "nothing was changed") {
			t.Errorf("%s: %v", c.name, err)
		}
		if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
			t.Errorf("%s: the state file changed", c.name)
		}
	}
	if _, err := os.Lstat(elsewhere); !os.IsNotExist(err) {
		t.Fatalf("a link's target was made: %v", err)
	}
	if fi, _ := os.Stat(existing); fi.Mode().Perm() != 0o644 {
		t.Fatalf("a link's target was changed: %v", fi.Mode().Perm())
	}
}

// A lock file or a state file this user cannot use, and a folder it cannot
// make the lock in, fail closed; the words name the way back.
func TestIssue490_UnusableStateFailsClosedAndNamesTheWayBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens every file; these cases need another user")
	}
	path := filepath.Join(t.TempDir(), "state.json")
	store := newStateStore(path)
	seedCode(t, store)
	before, _ := os.ReadFile(path)
	unchanged := func(what string) {
		t.Helper()
		if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
			t.Fatalf("%s changed the state file", what)
		}
	}
	wayBack := func(what string, err error) {
		t.Helper()
		if err == nil || (!strings.Contains(err.Error(), "nothing was changed") && !strings.Contains(err.Error(), "cannot be read by this user")) ||
			!strings.Contains(err.Error(), "sudo chown --reference=hub hub/state.json hub/state.json.lock") ||
			!strings.Contains(err.Error(), "docker compose exec -u 0 hub vaultsync-hub code") {
			t.Fatalf("%s: %v", what, err)
		}
	}
	lock := path + ".lock"
	if err := os.Chmod(lock, 0); err != nil {
		t.Fatal(err)
	}
	_, err := store.update(func(st *hubState) error { st.Pairing.Failures++; return nil })
	wayBack("an unusable lock file", err)
	unchanged("an unusable lock file")
	if err := os.Chmod(lock, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	_, err = store.update(func(st *hubState) error { st.Pairing.Failures++; return nil })
	wayBack("an unreadable state file", err)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	unchanged("an unreadable state file")

	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	_, err = store.update(func(st *hubState) error { st.Pairing.Failures++; return nil })
	if err == nil || !strings.Contains(err.Error(), "cannot be opened") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("a folder without room for the lock file: %v", err)
	}
	unchanged("a folder without room for the lock file")
}

func TestIssue490_AFilesystemWithoutFileLocksFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := newStateStore(path)
	seedCode(t, store)
	before, _ := os.ReadFile(path)
	saved := flock
	flock = func(int, int) error { return syscall.ENOTSUP }
	t.Cleanup(func() { flock = saved })
	_, err := store.update(func(st *hubState) error { st.Pairing.Failures++; return nil })
	if err == nil || !strings.Contains(err.Error(), "does not support the file lock") || !strings.Contains(err.Error(), "Keep the Hub's folder on a local disk") {
		t.Fatalf("no flock: %v", err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("a write without the lock changed the file")
	}
}

// The real owner run: only as root (run by hand with sudo; CI's runner is no
// root). The decision itself is tested everywhere (issue490_owner_test.go).
func TestIssue490_AsRootAWriteKeepsTheStateFilesOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	const uid, gid = 4242, 4343
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	store := newStateStore(path)
	seedCode(t, store)
	if err := os.Chown(path, uid, gid); err != nil {
		t.Fatal(err)
	}
	// A stray lock file of an earlier build: root's, 0644.
	if err := os.Chmod(path+".lock", 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.update(func(st *hubState) error { st.Pairing.Failures++; return nil }); err != nil {
		t.Fatal(err)
	}
	ownerIs(t, path, uid, gid, 0o600)
	ownerIs(t, path+".lock", uid, gid, 0o600)

	// New files take their folder's owner.
	if err := os.Chown(dir, uid, gid); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(dir, "fresh.json")
	seedCode(t, newStateStore(fresh))
	ownerIs(t, fresh, uid, gid, 0o600)
	ownerIs(t, fresh+".lock", uid, gid, 0o600)

	// A state file left to root in a folder the service owns goes back to
	// the folder's owner on root's next write — the way back the refusal names.
	if err := os.Chown(fresh, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(fresh+".lock", 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := newStateStore(fresh).update(func(st *hubState) error { st.Pairing.Failures++; return nil }); err != nil {
		t.Fatal(err)
	}
	ownerIs(t, fresh, uid, gid, 0o600)
	ownerIs(t, fresh+".lock", uid, gid, 0o600)

	// As root, nothing is made or changed at a link's target.
	target := filepath.Join(t.TempDir(), "target")
	if err := os.Remove(path + ".lock"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path+".lock"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.update(func(st *hubState) error { st.Pairing.Failures++; return nil }); err == nil || !strings.Contains(err.Error(), "is a link") {
		t.Fatalf("a linked lock file as root: %v", err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("root made the link's target: %v", err)
	}
}

func ownerIs(t *testing.T, path string, uid, gid int, mode os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	if int(st.Uid) != uid || int(st.Gid) != gid || fi.Mode().Perm() != mode {
		t.Fatalf("%s is %d:%d %v, want %d:%d %v", path, st.Uid, st.Gid, fi.Mode().Perm(), uid, gid, mode)
	}
}

// The refusal for another user, through the effective user the decision reads.
func TestIssue490_AnotherUsersWriteThatCannotKeepTheOwnerIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := newStateStore(path)
	seedCode(t, store)
	before, _ := os.ReadFile(path)
	saved := geteuid
	geteuid = func() int { return os.Geteuid() + 1 }
	t.Cleanup(func() { geteuid = saved })
	_, err := store.update(func(st *hubState) error { st.Pairing.Failures++; return nil })
	want := "must belong to uid " + strconv.Itoa(os.Geteuid()) + ", the user the Hub's service runs as, and this user (uid " + strconv.Itoa(os.Geteuid()+1) + ") cannot make it so"
	if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "docker compose exec -u 0 hub vaultsync-hub code") {
		t.Fatalf("another user's write: %v", err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("a refused write changed the file")
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 2 {
		t.Fatalf("a refused write left files behind: %v", entries)
	}
}
