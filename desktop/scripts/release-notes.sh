#!/bin/sh
# release-notes.sh VERSION [CHANGELOG] — prints the text of a desktop
# release (#283): its `## [X.Y.Z] — YYYY-MM-DD` section of
# desktop/CHANGELOG.md as it stands, the way the app's releases carry
# theirs, then the link to the whole changelog and what the assets are.
# VERSION is the tag without desktop-v. A candidate's X.Y.Z-<suffix> reads
# the section of X.Y.Z: its final tag goes on the same commit, so a section
# missing there must stop the candidate's run, not the release's after the
# signing and Apple's verdict. A relative link in the section points into
# the repository at this tag, so a release's text keeps working when a file
# moves later. It prints nothing and fails when VERSION is not
# X.Y.Z[-suffix] (exit 2), or when the section is missing, given twice or
# empty, its heading is not in that form, or a link points above the
# repository (exit 1). The build job runs it on every desktop tag before
# anything is signed; the release job writes the release's text with it.
# CHANGELOG is for the tests.
set -eu

usage() {
	echo "usage: release-notes.sh VERSION [CHANGELOG] — VERSION is X.Y.Z or X.Y.Z-<suffix>" >&2
	exit 2
}

if [ $# -lt 1 ] || [ $# -gt 2 ]; then
	usage
fi
version=$1
# The tag check's own form; what passes is safe in a URL.
case $version in
	'' | *[!0-9A-Za-z.-]*) usage ;;
esac
RN_VERSION=$version awk 'BEGIN { exit !(ENVIRON["RN_VERSION"] ~ /^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$/) }' || usage

here=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
changelog=${2:-$here/../CHANGELOG.md}
if [ ! -f "$changelog" ] || [ ! -r "$changelog" ]; then
	echo "release-notes.sh: no changelog at $changelog" >&2
	exit 1
fi

# Collects the section and prints it only once all of it has passed.
RN_VERSION=$version RN_FILE=$changelog awk '
function fail(msg) {
	print "release-notes.sh: " msg | "cat 1>&2"
	close("cat 1>&2")
	failed = 1
	exit 1
}
# Where a link in desktop/CHANGELOG.md leads, at this tag: ../X is the
# repository root, ./X, X the desktop folder; schemes and anchors stay.
function target(t, line) {
	if (t == "" || t ~ /^#/ || t ~ /^[A-Za-z][A-Za-z0-9+.-]*:/)
		return t
	if (t ~ /^\.\.\//) {
		t = substr(t, 4)
		if (t ~ /^\.\.\// || t ~ /^\//)
			fail(file " line " line " links to ../" t ", above the repository")
		return base t
	}
	if (t ~ /^\//)
		return base substr(t, 2)
	if (t ~ /^\.\//)
		t = substr(t, 3)
	return base "desktop/" t
}
function relink(s, line,    out) {
	out = ""
	while (match(s, /[]][(][^)]*[)]/)) {
		out = out substr(s, 1, RSTART + 1) target(substr(s, RSTART + 2, RLENGTH - 3), line) ")"
		s = substr(s, RSTART + RLENGTH)
	}
	return out s
}
BEGIN {
	version = ENVIRON["RN_VERSION"]
	file = ENVIRON["RN_FILE"]
	core = version
	sub(/-.*/, "", core)
	head = "## [" core "]"
	base = "https://github.com/psimaker/vaultsync/blob/desktop-v" version "/"
	found = 0
	insec = 0
	n = 0
}
/^## / {
	insec = 0
	if (index($0, head) == 1) {
		if (++found > 1)
			fail(file " has a second \"" head "\" section, at line " NR)
		if (substr($0, length(head) + 1) !~ /^ — [0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]$/)
			fail(file " line " NR " reads \"" $0 "\"; the heading of a release is \"" head " — YYYY-MM-DD\"")
		insec = 1
		start = NR
	}
}
insec { lines[++n] = $0 }
END {
	if (failed)
		exit 1
	if (!found)
		fail(file " has no \"" head " — YYYY-MM-DD\" section")
	# The blank lines and a --- separator before the next section.
	while (n > 1 && (lines[n] ~ /^[ \t]*$/ || lines[n] == "---"))
		n--
	if (n < 2)
		fail(file " line " start ": the section of " core " is empty")
	for (i = 1; i <= n; i++)
		out[i] = relink(lines[i], start + i - 1)
	for (i = 1; i <= n; i++)
		print out[i]
}
' "$changelog"

cat <<'EOF'

**Full changelog:** https://github.com/psimaker/vaultsync/blob/main/desktop/CHANGELOG.md

---

The desktop agent `vaultsync` for macOS and Linux: it installs its own pinned Syncthing, pairs with your VaultSync Hub by code and keeps your Obsidian vaults in sync. Set it up with the setup link (docs/hub.md): on a Mac it installs VaultSync for Mac (`VaultSync_darwin_<arch>.zip`) into Applications — the background service runs from it and its menu-bar icon opens at login — and on Linux it downloads `vaultsync_linux_<arch>`, with the menu-bar icon in `vaultsync-tray_linux_<arch>`. Everything for macOS is signed with a Developer ID and notarized by Apple. Check downloads against SHA256SUMS.
EOF
