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

func countFailures(store *stateStore, n int) error {
	for i := 0; i < n; i++ {
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

// flock needs no write access: a lock file another user made, which this one
// may only read, still serves. One this process cannot open at all, a folder
// it cannot make the lock file in, and a filesystem without file locks each
// fail closed and say why.
func TestIssue490_TheLockFileNeedsOnlyToBeReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := newStateStore(path)
	seedCode(t, store)
	lockPath := path + ".lock"
	if err := os.Chmod(lockPath, 0o444); err != nil {
		t.Fatal(err)
	}
	if _, err := store.update(func(st *hubState) error { st.Pairing.Failures++; return nil }); err != nil {
		t.Fatalf("a read-only lock file: %v", err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root opens every file; the unreadable cases need another user")
	}
	before, _ := os.ReadFile(path)
	if err := os.Chmod(lockPath, 0); err != nil {
		t.Fatal(err)
	}
	_, err := store.update(func(st *hubState) error { st.Pairing.Failures++; return nil })
	if err == nil || !strings.Contains(err.Error(), "cannot be opened") || !strings.Contains(err.Error(), lockPath) {
		t.Fatalf("an unreadable lock file: %v", err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("a write without the lock changed the file")
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	_, err = store.update(func(st *hubState) error { st.Pairing.Failures++; return nil })
	if err == nil || !strings.Contains(err.Error(), "cannot be opened") {
		t.Fatalf("a folder without room for the lock file: %v", err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("a write without the lock changed the file")
	}
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
	if err == nil || !strings.Contains(err.Error(), "does not support file locks") {
		t.Fatalf("no flock: %v", err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("a write without the lock changed the file")
	}
}

// A lock file is made 0644 whatever the umask: everyone who may write the
// state must be able to open it.
func TestIssue490_ANewLockFileIsOpenToEveryWriter(t *testing.T) {
	old := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(old) })
	path := filepath.Join(t.TempDir(), "state.json")
	seedCode(t, newStateStore(path))
	fi, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("lock file mode %v, want 0644", fi.Mode().Perm())
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode %v, want 0600", st.Mode().Perm())
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
	if _, err := store.update(func(st *hubState) error { st.Pairing.Failures++; return nil }); err != nil {
		t.Fatal(err)
	}
	ownerIs(t, path, uid, gid, 0o600)

	// A new file takes its folder's owner.
	if err := os.Chown(dir, uid, gid); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(dir, "fresh.json")
	seedCode(t, newStateStore(fresh))
	ownerIs(t, fresh, uid, gid, 0o600)
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
	want := "belongs to another user (uid " + strconv.Itoa(os.Geteuid()) + ")"
	if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "docker compose exec hub") {
		t.Fatalf("another user's write: %v", err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("a refused write changed the file")
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 2 {
		t.Fatalf("a refused write left files behind: %v", entries)
	}
}
