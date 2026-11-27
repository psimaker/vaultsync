package main

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// setupLinkReadsRelease is the line the setup link's release_of reads as a
// release (hub/scripts/setup.sh, decision 080): "vaultsync ", the version
// as one word — x.y.z in canonical numbers, a suffix such as -rc.1 allowed —
// then the Syncthing pin. The setup link replaces an installed VaultSync for
// Mac only with a release that is not older by this line, and never one
// whose line names no release (#277).
var setupLinkReadsRelease = regexp.MustCompile(`^vaultsync (0|[1-9][0-9]{0,8})(\.(0|[1-9][0-9]{0,8})){2}(-[0-9A-Za-z.-]+)? \(Syncthing [0-9]+\.[0-9]+\.[0-9]+\)$`)

// `vaultsync version` of a release build says its release in the form the
// setup link reads, and a build of no release says "dev", which the setup
// link reads as none. The builds are made as desktop-release.yml makes them
// — the release from the tag, stamped with -X main.version — so a version
// the stamp no longer reaches fails here, not as a setup link that keeps
// every installed app because each says "dev".
func TestIssue277_TheVersionLineIsTheFormTheSetupLinkReads(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the vaultsync binary")
	}
	pin := strings.TrimPrefix(syncthingVersion, "v")
	for _, tc := range []struct {
		name, stamp, want string
		release          bool
	}{
		{"a release", "0.4.0", "vaultsync 0.4.0 (Syncthing " + pin + ")", true},
		{"a release candidate", "0.4.0-rc.1", "vaultsync 0.4.0-rc.1 (Syncthing " + pin + ")", true},
		{"a build of no release", "", "vaultsync dev (Syncthing " + pin + ")", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "vaultsync")
			ldflags := "-s -w"
			if tc.stamp != "" {
				ldflags += " -X main.version=" + tc.stamp
			}
			build := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags="+ldflags, "-o", bin, ".")
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build vaultsync: %v\n%s", err, out)
			}
			out, err := exec.Command(bin, "version").Output()
			if err != nil {
				t.Fatalf("vaultsync version: %v", err)
			}
			line, ok := strings.CutSuffix(string(out), "\n")
			if !ok || strings.Contains(line, "\n") {
				t.Fatalf("vaultsync version printed %q, want one line", out)
			}
			if line != tc.want {
				t.Fatalf("vaultsync version printed %q, want %q", line, tc.want)
			}
			if got := setupLinkReadsRelease.MatchString(line); got != tc.release {
				t.Fatalf("the setup link reads %q as a release: %v, want %v", line, got, tc.release)
			}
		})
	}
}
