package bridge

import (
	"strings"
	"testing"
)

func TestPing(t *testing.T) {
	if got := Ping(); got != "pong" {
		t.Errorf("Ping() = %q, want %q", got, "pong")
	}
}

func TestVersion(t *testing.T) {
	v := Version()
	if !strings.HasPrefix(v, "go") {
		t.Errorf("Version() = %q, want prefix %q", v, "go")
	}
}

func TestArch(t *testing.T) {
	if got := Arch(); got == "" {
		t.Error("Arch() returned empty string")
	}
}

func TestSyncthingVersion(t *testing.T) {
	v := SyncthingVersion()
	if v == "" {
		t.Error("SyncthingVersion() returned empty string")
	}
	// build.Version is "unknown-dev" unless set via ldflags at build time
	t.Logf("SyncthingVersion() = %q", v)
}
