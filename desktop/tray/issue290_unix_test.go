//go:build unix

package tray

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The program the menu starts gets its folder on its command line (#290).
func TestIssue290_TheStartedProgramGetsTheMenusFolder(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "args")
	program := filepath.Join(dir, "vaultsync")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + out + "'\necho ok\n"
	if err := os.WriteFile(program, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(dir, "state with space", "vaultsync")
	if said, err := Start(context.Background(), program, folder); err != nil || said != "ok" {
		t.Fatalf("said %q, %v", said, err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n"); strings.Join(got, "|") != "start|--state-dir|"+folder {
		t.Fatalf("its arguments: %q", got)
	}
}
