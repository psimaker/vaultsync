package main

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// setupLinkRelease is a copy of release_of in hub/scripts/setup.sh
// (decision 080): the release a `vaultsync version` line names, as the setup
// link reads it, or false for a line that names none. The setup link
// replaces an installed VaultSync for Mac only with a release that is not
// older by this line, and never one whose line names no release (#277). The
// two rules move together: a change here is a change there, in one pull
// request.
//
//   - The line starts with "vaultsync ", and the version is the word after
//     it, up to the first space.
//   - The version is x.y.z in canonical numbers (no leading zero, at most
//     nine digits each: is_release), optionally followed by "-" and a suffix
//     of letters, digits, dots and dashes that does not end in "-".
func setupLinkRelease(line string) (string, bool) {
	rest, ok := strings.CutPrefix(line, "vaultsync ")
	if !ok {
		return "", false
	}
	rel, _, _ := strings.Cut(rest, " ")
	if !setupLinkReleaseWord.MatchString(rel) {
		return "", false
	}
	return rel, true
}

var setupLinkReleaseWord = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})(\.(0|[1-9][0-9]{0,8})){2}(-[0-9A-Za-z.-]*[0-9A-Za-z.])?$`)

// The copy reads the lines the setup link's dry-run test reads, the same
// way: a release, a release candidate, and what names none.
func TestIssue277_TheSetupLinksRuleReadsTheseLines(t *testing.T) {
	for _, tc := range []struct {
		line, want string
		ok         bool
	}{
		{"vaultsync 0.10.0 (Syncthing 2.1.7)", "0.10.0", true},
		{"vaultsync 0.11.0-rc.1 (Syncthing 2.1.7)", "0.11.0-rc.1", true},
		{"vaultsync 1.0.0 (Syncthing 2.1.7)", "1.0.0", true},
		{"vaultsync dev (Syncthing 2.1.7)", "", false},
		{"vaultsync dev-abc1234 (Syncthing 2.1.7)", "", false},
		{"vaultsync 0.11 (Syncthing 2.1.7)", "", false},
		{"vaultsync v0.11.0 (Syncthing 2.1.7)", "", false},
		{"vaultsync 0.11.0-rc- (Syncthing 2.1.7)", "", false},
		{"vaultsync 0.11.0- (Syncthing 2.1.7)", "", false},
		{"vaultsync 0.11.0-rc+1 (Syncthing 2.1.7)", "", false},
		{"vaultsync 01.0.0 (Syncthing 2.1.7)", "", false},
		{"vaultsync-0.11.0 (Syncthing 2.1.7)", "", false},
	} {
		got, ok := setupLinkRelease(tc.line)
		if got != tc.want || ok != tc.ok {
			t.Errorf("setupLinkRelease(%q) = %q, %v; want %q, %v", tc.line, got, ok, tc.want, tc.ok)
		}
	}
}

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
		name, stamp, want, release string
	}{
		{"a release", "0.4.0", "vaultsync 0.4.0 (Syncthing " + pin + ")", "0.4.0"},
		{"a release candidate", "0.4.0-rc.1", "vaultsync 0.4.0-rc.1 (Syncthing " + pin + ")", "0.4.0-rc.1"},
		{"a build of no release", "", "vaultsync dev (Syncthing " + pin + ")", ""},
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
			if got, ok := setupLinkRelease(line); got != tc.release || ok != (tc.release != "") {
				t.Fatalf("the setup link reads %q as release %q (%v), want %q", line, got, ok, tc.release)
			}
		})
	}
}
