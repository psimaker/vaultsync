package bridge

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Every test that starts a real peer left a `syncthing serve` process behind
// once it had ended: serve ran the engine as a child of a monitor process, the
// cleanup killed the monitor, and the engine went on as an orphan with its
// ports held and its home deleted, until someone killed it by hand (#334).
// These tests look for the peer's processes by the one thing every one of them
// carries on its command line, the peer's home, and find none once the test
// that started the peer is over — also when the test binary died without
// running a cleanup.

func TestIssue334_APeerLeavesNoProcessWhenItsTestEnds(t *testing.T) {
	if testing.Short() {
		t.Skip("real peer process; skipped in -short")
	}
	skipWithoutProcessList(t)
	bin := peerBinary(t)

	var home string
	t.Run("peer", func(t *testing.T) {
		home = startPeer(t, bin, "issue334-peer").home
		running := processesNaming(t, "--home="+home, 0)
		if len(running) == 0 {
			t.Fatal("no process names the running peer's home — the check after this test would prove nothing")
		}
		t.Logf("while the peer runs, %d processes name its home:\n%s", len(running), describeProcesses(running))
		// Let the peer finish starting. An engine whose monitor was killed
		// dies of SIGPIPE at the next line it logs into the monitor's pipe;
		// one that has gone quiet — every peer at the end of a real test —
		// lives on, and those are what piled up.
		waitForQuietFile(t, filepath.Join(home, "stdout.log"), 2*time.Second, 30*time.Second)
	})
	if home == "" {
		t.Fatal("the peer did not start")
	}
	// The cleanup has returned: what it started is gone now, not later.
	if left := processesNaming(t, "--home="+home, 0); len(left) > 0 {
		endProcesses(left)
		t.Errorf("%d of the peer's processes still ran after its test had ended:\n%s", len(left), describeProcesses(left))
	}
}

const (
	issue334ChildEnv  = "VAULTSYNC_BRIDGE_ISSUE334_CHILD"
	issue334HomeLabel = "issue334-peer-home="
)

// A t.Cleanup does not run when the test binary itself dies: the panic a
// -timeout ends a run with, a panic in any goroutine but the test's own, a
// signal. The test runs this binary again as a child that starts a peer and
// then panics outside its test goroutine — the way the -timeout alarm does —
// and looks for the peer's processes once the child is gone.
func TestIssue334_APeerLeavesNoProcessWhenTheTestBinaryDies(t *testing.T) {
	if os.Getenv(issue334ChildEnv) != "" {
		peer := startPeer(t, peerBinary(t), "issue334-orphaned-peer")
		fmt.Printf("%s%s\n", issue334HomeLabel, peer.home)
		go func() { panic("issue 334: the test binary dies without running its cleanups") }()
		select {}
	}
	if testing.Short() {
		t.Skip("real peer process; skipped in -short")
	}
	skipWithoutProcessList(t)
	bin := peerBinary(t)

	child := exec.Command(os.Args[0], "-test.run=^TestIssue334_APeerLeavesNoProcessWhenTheTestBinaryDies$", "-test.count=1")
	// The child's temp dirs, the peer's home among them, land in this
	// test's own, which is removed when this test ends.
	child.Env = append(os.Environ(), issue334ChildEnv+"=1", "VAULTSYNC_BRIDGE_PEER_BIN="+bin, "TMPDIR="+t.TempDir())
	var stderr bytes.Buffer
	child.Stderr = &stderr
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	read := make(chan string, 1)
	go func() {
		home := ""
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if h, ok := strings.CutPrefix(scanner.Text(), issue334HomeLabel); ok {
				home = h
			}
		}
		read <- home
	}()
	var home string
	select {
	case home = <-read:
	case <-time.After(2 * time.Minute):
		_ = child.Process.Kill()
		home = <-read
	}
	waitErr := child.Wait()
	if home == "" {
		t.Fatalf("the child named no peer home (exit: %v)\n%s", waitErr, stderr.String())
	}
	if waitErr == nil {
		t.Fatalf("the child exited cleanly instead of dying:\n%s", stderr.String())
	}
	if left := processesNaming(t, "--home="+home, 10*time.Second); len(left) > 0 {
		endProcesses(left)
		t.Errorf("%d of the peer's processes still ran after the test binary that started them had died:\n%s",
			len(left), describeProcesses(left))
	}
}

// --- process listing ---------------------------------------------------------

type listedProcess struct {
	pid, ppid, pgid int
	stat, args      string
}

// A zombie is left only as an exit status for its parent to collect: it
// holds no port, no file and no memory.
func (p listedProcess) zombie() bool { return strings.HasPrefix(p.stat, "Z") }

func (p listedProcess) String() string {
	return fmt.Sprintf("pid %d ppid %d pgid %d stat %s: %s", p.pid, p.ppid, p.pgid, p.stat, p.args)
}

func describeProcesses(procs []listedProcess) string {
	lines := make([]string, len(procs))
	for i, p := range procs {
		lines[i] = "  " + p.String()
	}
	return strings.Join(lines, "\n")
}

func skipWithoutProcessList(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("lists processes with ps")
	}
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("no ps to list processes with")
	}
}

// listProcesses lists every process as ps sees it. -A and -ww mean the same
// to procps (Linux) and to BSD ps (macOS): every process, and each command
// line at its full length.
func listProcesses() ([]listedProcess, error) {
	out, err := exec.Command("ps", "-A", "-ww", "-o", "pid=,ppid=,pgid=,stat=,args=").Output()
	if err != nil {
		return nil, err
	}
	var procs []listedProcess
	for _, line := range strings.Split(string(out), "\n") {
		var fields [4]string
		rest := line
		for i := range fields {
			fields[i], rest, _ = strings.Cut(strings.TrimLeft(rest, " \t"), " ")
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		pgid, err3 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil || err3 != nil || fields[3] == "" {
			continue
		}
		procs = append(procs, listedProcess{pid: pid, ppid: ppid, pgid: pgid, stat: fields[3], args: strings.TrimSpace(rest)})
	}
	if len(procs) == 0 {
		return nil, fmt.Errorf("ps listed no process:\n%s", out)
	}
	return procs, nil
}

// processesNaming returns the live processes whose command line contains s,
// waiting up to timeout for there to be none.
func processesNaming(t *testing.T, s string, timeout time.Duration) []listedProcess {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		procs, err := listProcesses()
		if err != nil {
			t.Fatalf("list processes: %v", err)
		}
		var naming []listedProcess
		for _, p := range procs {
			if !p.zombie() && p.pid != os.Getpid() && strings.Contains(p.args, s) {
				naming = append(naming, p)
			}
		}
		if len(naming) == 0 || !time.Now().Before(deadline) {
			return naming
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// waitForQuietFile returns once path has not grown for quiet, and fails the
// test if that has not happened within limit.
func waitForQuietFile(t *testing.T, path string, quiet, limit time.Duration) {
	t.Helper()
	deadline := time.Now().Add(limit)
	size, since := int64(-1), time.Now()
	for time.Now().Before(deadline) {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case info.Size() != size:
			size, since = info.Size(), time.Now()
		case time.Since(since) >= quiet:
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s did not stay unchanged for %v within %v", path, quiet, limit)
}

// endProcesses kills what a failed check found, so that a failing run does
// not leave it behind either. Every process it is given names a home only
// this test created.
func endProcesses(procs []listedProcess) {
	for _, p := range procs {
		if proc, err := os.FindProcess(p.pid); err == nil {
			_ = proc.Kill()
		}
	}
}
