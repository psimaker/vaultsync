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
// These tests look for the peer's processes by the peer's home on their
// command lines — and, where the test binary dies, also by the peer's process
// groups — and find none once the test that started the peer is over, also
// when the test binary died without running a cleanup.

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
		// What the wait is for: it lets this test fail on a helper that
		// kills only a monitor. The engine such a helper leaves behind dies
		// of SIGPIPE at the next line it logs into the dead monitor's pipe,
		// so a peer that is still starting would hide it; one that has gone
		// quiet — every peer at the end of a real test — lives on, and those
		// are what piled up. It is not what the test asserts: a peer that
		// never falls quiet is logged, and the check still runs.
		const quiet, limit = 2 * time.Second, 30 * time.Second
		if !waitForQuietFile(t, filepath.Join(home, "stdout.log"), quiet, limit) {
			t.Logf("the peer's log did not stay unchanged for %v within %v: this run checks the helper, "+
				"but could not tell it from one that kills only a monitor", quiet, limit)
		}
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
	issue334ChildEnv   = "VAULTSYNC_BRIDGE_ISSUE334_CHILD"
	issue334HomeLabel  = "issue334-peer-home="
	issue334GroupLabel = "issue334-peer-group="
)

// A t.Cleanup does not run when the test binary itself dies: the panic a
// -timeout ends a run with, a panic in any goroutine but the test's own, a
// signal. The test runs this binary again as a child that starts a peer and
// then panics outside its test goroutine — the way the -timeout alarm does —
// and looks for the peer's processes once the child is gone: those that name
// the peer's home, and every live member of the peer's process groups, so a
// process that names no home — the reaper — is seen too.
func TestIssue334_APeerLeavesNoProcessWhenTheTestBinaryDies(t *testing.T) {
	if os.Getenv(issue334ChildEnv) != "" {
		peer := startPeer(t, peerBinary(t), "issue334-orphaned-peer")
		fmt.Printf("%s%s\n", issue334HomeLabel, peer.home)
		// The groups the peer's processes are in, but not this binary's
		// own: that one is the parent's too, and its members are found
		// by the home they name.
		own := ownProcessGroup(t)
		for _, p := range processesNaming(t, "--home="+peer.home, 0) {
			if p.pgid != own {
				fmt.Printf("%s%d\n", issue334GroupLabel, p.pgid)
			}
		}
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
	type childOutput struct {
		home   string
		groups map[int]bool
		all    string // every line, for the failure messages
	}
	read := make(chan childOutput, 1)
	go func() {
		out := childOutput{groups: map[int]bool{}}
		var all strings.Builder
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			all.WriteString(line + "\n")
			if h, ok := strings.CutPrefix(line, issue334HomeLabel); ok {
				out.home = h
			}
			if g, ok := strings.CutPrefix(line, issue334GroupLabel); ok {
				if n, err := strconv.Atoi(g); err == nil {
					out.groups[n] = true
				}
			}
		}
		out.all = all.String()
		read <- out
	}()
	var out childOutput
	select {
	case out = <-read:
	case <-time.After(2 * time.Minute):
		_ = child.Process.Kill()
		out = <-read
	}
	waitErr := child.Wait()
	childSaid := fmt.Sprintf("--- the child's output ---\n%s--- the child's stderr ---\n%s", out.all, stderr.String())
	if out.home == "" {
		t.Fatalf("the child named no peer home (exit: %v)\n%s", waitErr, childSaid)
	}
	if waitErr == nil {
		t.Fatalf("the child exited cleanly instead of dying:\n%s", childSaid)
	}
	delete(out.groups, ownProcessGroup(t))
	t.Logf("checks the processes that name the peer's home and the members of its groups %v", out.groups)
	left := processesMatching(t, 10*time.Second, func(p listedProcess) bool {
		return out.groups[p.pgid] || strings.Contains(p.args, "--home="+out.home)
	})
	if len(left) > 0 {
		endProcesses(left)
		t.Errorf("%d of the peer's processes still ran after the test binary that started them had died "+
			"(peer groups %v):\n%s", len(left), out.groups, describeProcesses(left))
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
	return processesMatching(t, timeout, func(p listedProcess) bool { return strings.Contains(p.args, s) })
}

// processesMatching returns the live processes, other than this one, that
// match, waiting up to timeout for there to be none.
func processesMatching(t *testing.T, timeout time.Duration, match func(listedProcess) bool) []listedProcess {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		procs, err := listProcesses()
		if err != nil {
			t.Fatalf("list processes: %v", err)
		}
		var matching []listedProcess
		for _, p := range procs {
			if !p.zombie() && p.pid != os.Getpid() && match(p) {
				matching = append(matching, p)
			}
		}
		if len(matching) == 0 || !time.Now().Before(deadline) {
			return matching
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ownProcessGroup returns the process group this test binary is in, as ps
// lists it.
func ownProcessGroup(t *testing.T) int {
	t.Helper()
	procs, err := listProcesses()
	if err != nil {
		t.Fatalf("list processes: %v", err)
	}
	for _, p := range procs {
		if p.pid == os.Getpid() {
			return p.pgid
		}
	}
	t.Fatalf("ps does not list this process (%d)", os.Getpid())
	return 0
}

// waitForQuietFile reports whether path stayed unchanged for quiet within
// limit.
func waitForQuietFile(t *testing.T, path string, quiet, limit time.Duration) bool {
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
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// endProcesses kills what a failed check found, so that a failing run does
// not leave it behind either. Every process it is given names a home only
// this test created, or is in a process group the test's peer led.
func endProcesses(procs []listedProcess) {
	for _, p := range procs {
		if proc, err := os.FindProcess(p.pid); err == nil {
			_ = proc.Kill()
		}
	}
}
