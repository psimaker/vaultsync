#!/bin/sh
# release-notes-test.sh — holds desktop/scripts/release-notes.sh (#283) to
# the text a desktop release publishes: a section exactly as written, its
# relative links pinned to the release's tag, the link to the whole
# changelog and what the assets are; a candidate's text the same but for
# its tag; 1.2.1 never the section of 1.2.10; and a refusal, with nothing
# printed, for a missing, doubled or empty section, a heading in another
# form, a link above the repository and a version that is not X.Y.Z or
# X.Y.Z-<suffix>. Then every released section of the real
# desktop/CHANGELOG.md must give a text, so a heading in another form fails
# on its pull request instead of stopping a tag. Runs on Linux and macOS.
set -eu

here=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
notes="$here/../release-notes.sh"
changelog="$here/../../CHANGELOG.md"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

# run NAME ARGS… — the text in $tmp/NAME.out, the errors in $tmp/NAME.err,
# the exit status in $rc.
run() {
	name=$1
	shift
	if "$notes" "$@" >"$tmp/$name.out" 2>"$tmp/$name.err"; then rc=0; else rc=$?; fi
}

# refused NAME STATUS — refused with STATUS, a reason given, nothing printed.
refused() {
	[ "$rc" = "$2" ] || { cat "$tmp/$1.err" >&2; fail "$1: exit $rc, want $2"; }
	[ ! -s "$tmp/$1.out" ] || fail "$1: printed a text although refused"
	[ -s "$tmp/$1.err" ] || fail "$1: refused without a reason"
}

# same NAME WANT — the text of NAME is WANT, byte for byte.
same() {
	[ "$rc" = 0 ] || { cat "$tmp/$1.err" >&2; fail "$1: exit $rc"; }
	cmp -s "$tmp/$1.out" "$2" || { diff "$2" "$tmp/$1.out" >&2 || true; fail "$1: not the expected text"; }
}

# What follows every section.
cat >"$tmp/footer" <<'EOF'

**Full changelog:** https://github.com/psimaker/vaultsync/blob/main/desktop/CHANGELOG.md

---

The desktop agent `vaultsync` for macOS and Linux: it installs its own pinned Syncthing, pairs with your VaultSync Hub by code and keeps your Obsidian vaults in sync. Set it up with the setup link (docs/hub.md): on a Mac it installs VaultSync for Mac (`VaultSync_darwin_<arch>.zip`) into Applications — the background service runs from it and its menu-bar icon opens at login — and on Linux it downloads `vaultsync_linux_<arch>`, with the menu-bar icon in `vaultsync-tray_linux_<arch>`. Everything for macOS is signed with a Developer ID and notarized by Apple. Check downloads against SHA256SUMS.
EOF

cat >"$tmp/changelog.md" <<'EOF'
# Changelog — a test

The [root changelog](../CHANGELOG.md).

---

## [Unreleased]

### Fixed

- **Not released** — in no release's text.

## [1.2.10] — 2026-12-10

### Fixed

- **Ten** — the section of 1.2.10.

## [1.2.1] — 2026-12-01

### Added

- **Links** ([#1](https://github.com/psimaker/vaultsync/issues/1)) — the [root changelog](../CHANGELOG.md), [a page](../docs/hub.md#update-the-hub), [the README](README.md), [the same](./README.md), [the root](/SECURITY.md), [an anchor](#added), [mail](mailto:a@example.org), [two](../CHANGELOG.md) on [one line](README.md) and [none]().

### Fixed

- **Last** — the last line.

---

## [1.2.0] — 2026-11-01

- **Older**.

## [1.1.0] — 2026-10-01

- **The last section in the file** — runs to its end.


EOF

# 1.2.1: the section as written, its links at desktop-v<VERSION>, the
# separator and blank lines before the next section left out.
want() { # VERSION — the expected text of 1.2.1 at that tag
	b="https://github.com/psimaker/vaultsync/blob/desktop-v$1"
	cat <<EOF
## [1.2.1] — 2026-12-01

### Added

- **Links** ([#1](https://github.com/psimaker/vaultsync/issues/1)) — the [root changelog]($b/CHANGELOG.md), [a page]($b/docs/hub.md#update-the-hub), [the README]($b/desktop/README.md), [the same]($b/desktop/README.md), [the root]($b/SECURITY.md), [an anchor](#added), [mail](mailto:a@example.org), [two]($b/CHANGELOG.md) on [one line]($b/desktop/README.md) and [none]().

### Fixed

- **Last** — the last line.
EOF
	cat "$tmp/footer"
}
want 1.2.1 >"$tmp/want-1.2.1"
run final 1.2.1 "$tmp/changelog.md"
same final "$tmp/want-1.2.1"

# A candidate reads the section of X.Y.Z, its links at its own tag.
want 1.2.1-rc.2 >"$tmp/want-rc"
run rc 1.2.1-rc.2 "$tmp/changelog.md"
same rc "$tmp/want-rc"
run suffix 1.2.1-signtest "$tmp/changelog.md"
[ "$rc" = 0 ] || fail "suffix: exit $rc"
sed 's#/blob/desktop-v1\.2\.1-signtest/#/blob/desktop-v1.2.1/#g' "$tmp/suffix.out" | cmp -s - "$tmp/final.out" || fail "suffix: another text than the release's"

# The section that is asked for, the last one to its end.
run ten 1.2.10 "$tmp/changelog.md"
[ "$rc" = 0 ] || fail "ten: exit $rc"
[ "$(sed -n 1p "$tmp/ten.out")" = "## [1.2.10] — 2026-12-10" ] || fail "ten: not the section of 1.2.10"
{
	printf '%s\n' "## [1.1.0] — 2026-10-01" "" "- **The last section in the file** — runs to its end."
	cat "$tmp/footer"
} >"$tmp/want-last"
run last 1.1.0 "$tmp/changelog.md"
same last "$tmp/want-last"

# Refusals.
run missing 9.9.9 "$tmp/changelog.md"
refused missing 1
grep -q -F '## [9.9.9] — YYYY-MM-DD' "$tmp/missing.err" || fail "missing: the reason does not name the section"
for v in Unreleased v1.2.1 desktop-v1.2.1 1.2 1.2.1.4 1.2.1- "1.2.1-rc 1" "1.2.1-rc.1/../x" 1.2.x ""; do
	run version "$v" "$tmp/changelog.md"
	[ "$rc" = 2 ] || fail "the version '$v': exit $rc, want 2"
	refused version 2
done
run none
refused none 2
run three 1.2.1 "$tmp/changelog.md" extra
refused three 2
run nofile 1.2.1 "$tmp/nothing.md"
refused nofile 1

# fixture NAME — a changelog of the lines on stdin.
fixture() { cat >"$tmp/$1.md"; }
printf '%s\n' "## [2.0.0] - 2026-12-01" "" "- **A hyphen**." | fixture hyphen
run hyphen 2.0.0 "$tmp/hyphen.md"
refused hyphen 1
grep -q -F '"## [2.0.0] — YYYY-MM-DD"' "$tmp/hyphen.err" || fail "hyphen: the reason does not give the heading's form"
printf '%s\n' "## [2.0.0]" "" "- **No date**." | fixture nodate
run nodate 2.0.0 "$tmp/nodate.md"
refused nodate 1
printf '%s\n' "## [2.0.0] — 2026-12-01 (draft)" "" "- **More**." | fixture more
run more 2.0.0 "$tmp/more.md"
refused more 1
printf '%s\n' "## [2.0.0] — 2026-12-01" "" "- **One**." "" "## [2.0.0] — 2026-12-02" "" "- **Two**." | fixture twice
run twice 2.0.0 "$tmp/twice.md"
refused twice 1
printf '%s\n' "## [2.0.0] — 2026-12-01" "" "---" "" "## [1.0.0] — 2026-11-01" "" "- **Older**." | fixture empty
run empty 2.0.0 "$tmp/empty.md"
refused empty 1
printf '%s\n' "## [2.0.0] — 2026-12-01" "" "- [Out](../../elsewhere.md)." | fixture above
run above 2.0.0 "$tmp/above.md"
refused above 1

# The real changelog: 0.3.0 as written, links aside, and only it.
real="## [0.3.0] — 2026-11-19"
run real 0.3.0
[ "$rc" = 0 ] || { cat "$tmp/real.err" >&2; fail "0.3.0: exit $rc"; }
[ "$(sed -n 1p "$tmp/real.out")" = "$real" ] || fail "0.3.0: the text does not start with its heading"
[ "$(grep -c '^## ' "$tmp/real.out")" = 1 ] || fail "0.3.0: a second section in the text"
for h in '### Added' '### Changed' '### Fixed' '- **Return alone chooses no vault in the Linux list**'; do
	grep -q -F -- "$h" "$tmp/real.out" || fail "0.3.0: '$h' is missing"
done
! grep -q -F '](../' "$tmp/real.out" || fail "0.3.0: a relative link is left"
grep -q -F '](https://github.com/psimaker/vaultsync/blob/desktop-v0.3.0/CHANGELOG.md)' "$tmp/real.out" ||
	fail "0.3.0: the root changelog's link is not pinned to desktop-v0.3.0"
lines=$(wc -l <"$tmp/footer")
tail -n "$lines" "$tmp/real.out" | cmp -s - "$tmp/footer" || fail "0.3.0: the text does not end with the footer"
# The section of the file between its heading and the next one, with the
# links pinned as the script pins them, blank lines aside.
sed -n '/^## \[0\.3\.0\] /,/^## \[/p' "$changelog" | sed '$d' |
	sed 's#](\.\./#](https://github.com/psimaker/vaultsync/blob/desktop-v0.3.0/#g' | grep -v '^$' >"$tmp/real-want"
total=$(wc -l <"$tmp/real.out")
head -n "$((total - lines))" "$tmp/real.out" | grep -v '^$' | cmp -s - "$tmp/real-want" ||
	fail "0.3.0: the section is not the file's, word for word"
run real-rc 0.3.0-rc.1
[ "$rc" = 0 ] || fail "0.3.0-rc.1: exit $rc"
grep -q -F '/blob/desktop-v0.3.0-rc.1/CHANGELOG.md)' "$tmp/real-rc.out" || fail "0.3.0-rc.1: links not at the candidate's tag"
sed 's#/blob/desktop-v0\.3\.0-rc\.1/#/blob/desktop-v0.3.0/#g' "$tmp/real-rc.out" | cmp -s - "$tmp/real.out" ||
	fail "0.3.0-rc.1: another text than the release's"

# Every released section of the real changelog gives a text.
grep '^## \[' "$changelog" | grep -v -x -F '## [Unreleased]' >"$tmp/heads" || true
count=0
while IFS= read -r heading; do
	v=${heading#'## ['}
	v=${v%%]*}
	run each "$v"
	[ "$rc" = 0 ] || { cat "$tmp/each.err" >&2; fail "desktop/CHANGELOG.md: \"$heading\" gives no release text"; }
	[ "$(sed -n 1p "$tmp/each.out")" = "$heading" ] || fail "desktop/CHANGELOG.md: \"$heading\" gives another section"
	count=$((count + 1))
done <"$tmp/heads"
[ "$count" -ge 3 ] || fail "desktop/CHANGELOG.md: only $count released sections found"

echo "release-notes-test: all cases passed ($count released sections of desktop/CHANGELOG.md)"
