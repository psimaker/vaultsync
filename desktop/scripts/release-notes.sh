#!/bin/sh
# release-notes.sh VERSION [CHANGELOG] — prints the text of a desktop
# release (#283): its `## [X.Y.Z] — YYYY-MM-DD` section of
# desktop/CHANGELOG.md as it stands, the way the app's releases carry
# theirs, then the link to the whole changelog and what the assets are.
# VERSION is the tag without desktop-v. A candidate's X.Y.Z-<suffix> reads
# the section of X.Y.Z: its final tag goes on the same commit, so a section
# missing there must stop the candidate's run, not the release's after the
# signing and Apple's verdict.
#
# A relative link in the section points into the repository at this tag,
# its path resolved, so a release's text keeps working when a file moves
# later; code spans stay as written. What cannot become a working absolute
# link is refused with its line, so it fails on the pull request that
# writes it: a path that leaves the repository, an image or a target in
# <...> that is relative, a link definition ([r]: …) with a relative target.
#
# It prints nothing and fails when VERSION is not X.Y.Z[-suffix] (exit 2),
# or when the section is missing, given twice, holds no entry, its heading
# is not in that form, or a link is refused (exit 1). The build job runs it
# on every desktop tag before anything is signed; the release job writes
# the release's text with it. CHANGELOG is for the tests.
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
function refuse(line, msg) {
	fail(file " line " line ": " msg)
}
# A target that needs no repository: none, an anchor, //host or a scheme.
function absolute(t) {
	return t == "" || t ~ /^#/ || t ~ /^\/\// || t ~ /^[A-Za-z][A-Za-z0-9+.-]*:/
}
# A relative target at this tag: /X from the repository root, anything
# else from the desktop folder, . and .. resolved, ?query and #anchor kept.
function resolve(t, line,    path, rest, parts, stack, n, k, i, out) {
	rest = ""
	path = t
	if (match(t, /[?#]/)) {
		rest = substr(t, RSTART)
		path = substr(t, 1, RSTART - 1)
	}
	k = 0
	if (path ~ /^\//)
		path = substr(path, 2)
	else
		stack[++k] = "desktop"
	n = split(path, parts, "/")
	for (i = 1; i <= n; i++) {
		if (parts[i] == "" || parts[i] == ".")
			continue
		if (parts[i] == "..") {
			if (k == 0)
				refuse(line, "the link " t " leads out of the repository")
			k--
			continue
		}
		stack[++k] = parts[i]
	}
	out = ""
	for (i = 1; i <= k; i++)
		out = out (i > 1 ? "/" : "") stack[i]
	return base out rest
}
# Whether the [ that opens the ] at the end of pre is an image: ![.
function image(pre,    i, d, c) {
	d = 0
	for (i = length(pre); i > 0; i--) {
		c = substr(pre, i, 1)
		if (c == "]")
			d++
		else if (c == "[") {
			if (d == 0)
				return i > 1 && substr(pre, i - 1, 1) == "!"
			d--
		}
	}
	return 0
}
function target(t, line, img,    u) {
	u = t
	if (u ~ /^</)
		u = substr(u, 2)
	if (absolute(u))
		return t
	if (t ~ /^</)
		refuse(line, "the link target " t " is relative and in <...>; write it without the angle brackets, a space as %20")
	if (img)
		refuse(line, "the image " t " is relative; give it an https:// address")
	return resolve(t, line)
}
# The inline links of s, a stretch outside code; ctx is the line before s.
# match() sets RSTART and RLENGTH for everyone: each caller keeps its own.
function links(s, line, ctx,    out, p, l, t) {
	out = ""
	while (match(s, /[]][(][^)]*[)]/)) {
		p = RSTART
		l = RLENGTH
		t = target(substr(s, p + 2, l - 3), line, image(ctx out substr(s, 1, p - 1)))
		out = out substr(s, 1, p + 1) t ")"
		s = substr(s, p + l)
	}
	return out s
}
# Where in s the next run of exactly n backticks starts, or 0.
function closing(s, n,    p) {
	p = 0
	while (match(s, /`+/)) {
		if (RLENGTH == n)
			return p + RSTART
		p += RSTART + RLENGTH - 1
		s = substr(s, RSTART + RLENGTH)
	}
	return 0
}
# A line with its links at this tag; a code span, from a run of backticks
# to the next run as long, stays as written, and a run without one is text.
function relink(s, line,    out, d, p, n, run, r) {
	if (match(s, /^ ? ? ?[[][^]^][^]]*[]]:/)) {
		d = substr(s, RLENGTH + 1)
		sub(/^[ \t]+/, "", d)
		sub(/[ \t].*/, "", d)
		if (d ~ /^</) {
			d = substr(d, 2)
			sub(/>$/, "", d)
		}
		if (d == "")
			refuse(line, "the link definition " s " has no target on its line; write the link inline")
		if (!absolute(d))
			refuse(line, "the link definition " s " has a relative target; write the link inline")
	}
	out = ""
	while (match(s, /`+/)) {
		p = RSTART
		n = RLENGTH
		out = out links(substr(s, 1, p - 1), line, out)
		run = substr(s, p, n)
		s = substr(s, p + n)
		r = closing(s, n)
		if (r == 0) {
			out = out run
			continue
		}
		out = out run substr(s, 1, r - 1 + n)
		s = substr(s, r + n)
	}
	return out links(s, line, out)
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
	entries = 0
	for (i = 2; i <= n; i++)
		if (lines[i] !~ /^[ \t]*$/ && lines[i] !~ /^#/ && lines[i] != "---")
			entries++
	if (!entries)
		refuse(start, "the section of " core " holds no entry")
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
