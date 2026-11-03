//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #264: an agent never installs a Syncthing older than the one VaultSync
// set up on this computer — an older VaultSync (an old copy, an older
// VaultSync for Mac dragged into Applications, an old download) stops and
// says so, instead of putting its older engine over the state the newer one
// wrote. The guard has to sit in the older build, so it ships before the
// first app release.

// newerEngineHere records a newer Syncthing than this build pins, its
// binary in place, and returns what the binary holds.
func newerEngineHere(t *testing.T, lay layout) string {
	t.Helper()
	writeFile(t, lay.Syncthing, "the newer engine")
	sum, err := fileSHA256(lay.Syncthing)
	if err != nil {
		t.Fatal(err)
	}
	if err := updateState(lay.State, func(s *agentState) {
		s.Syncthing.Version, s.Syncthing.BinarySHA256 = "v9.0.0", sum
	}); err != nil {
		t.Fatal(err)
	}
	return "the newer engine"
}

func untouched(t *testing.T, lay layout, engine string) {
	t.Helper()
	if have, _ := os.ReadFile(lay.Syncthing); string(have) != engine {
		t.Fatalf("the newer engine was replaced: %q", have)
	}
	if st, err := loadState(lay.State); err != nil || st.Syncthing.Version != "v9.0.0" {
		t.Fatalf("agent.json's engine changed: %+v %v", st.Syncthing, err)
	}
}

func TestIssue264_AnOlderSetupDoesNotPutAnOlderEngineBack(t *testing.T) {
	home := t.TempDir()
	lay, err := layoutFor("linux", home, envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	engine := newerEngineHere(t, lay)
	orig := ttyPath
	ttyPath = filepath.Join(t.TempDir(), "no-tty")
	defer func() { ttyPath = orig }()
	out := &bytes.Buffer{}
	term, closeTerm := openTerm(out)
	defer closeTerm()
	a := &app{goos: "linux", home: home, lay: lay, out: out, getenv: envOf(nil)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // whatever happens, nothing is downloaded
	_, _, err = a.ensureEngine(ctx, term)
	if !strings.Contains(refusalText(err), "older than the one set up on this computer") {
		t.Fatalf("an older setup with a newer engine here: %v\n%s", err, out)
	}
	if strings.Contains(out.String(), "Downloading") {
		t.Fatalf("an older setup set out to download its engine:\n%s", out)
	}
	untouched(t, lay, engine)
}

func TestIssue264_AnOlderServiceDoesNotPutAnOlderEngineBack(t *testing.T) {
	a, _ := runFixture(t)
	engine := newerEngineHere(t, a.lay)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := a.run(ctx, nil)
	if !errors.Is(err, errOlderAgent) {
		t.Fatalf("an older service with a newer engine here: %v", err)
	}
	if got := serviceSummary(err); got != errOlderAgent.Error() {
		t.Fatalf("the service log says %q", got)
	}
	untouched(t, a.lay, engine)
}

func TestIssue264_EngineVersionsCompare(t *testing.T) {
	for _, c := range []struct {
		a, b  string
		newer bool
	}{
		{"v2.1.7", "v2.1.6", true}, {"v2.2.0", "v2.1.9", true}, {"v3.0.0", "v2.9.9", true},
		{"v2.1.6", "v2.1.6", false}, {"v2.1.5", "v2.1.6", false}, {"v2.1.6-rc.1", "v2.1.6", false},
		{"", "v2.1.6", false}, {"garbage", "v2.1.6", false}, {"v2.10.0", "v2.9.0", true},
	} {
		if got := engineNewer(c.a, c.b); got != c.newer {
			t.Errorf("engineNewer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}
