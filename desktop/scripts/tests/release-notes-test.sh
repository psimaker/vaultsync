#!/bin/sh
# release-notes-test.sh — holds desktop/scripts/release-notes.sh (#283) to
# the text a desktop release publishes: a section exactly as written, its
# relative links pinned to the release's tag with their paths resolved,
# code spans, //host and absolute links as written, the link to the whole
# changelog and what the assets are; a candidate's text the same but for
# its tag; 1.2.1 never the section of 1.2.10. A refusal, with nothing
# printed and the line named, for a missing or doubled section, one without
# an entry, a heading in another form, a link that leaves the repository,
# a relative image, a relative target in <...>, a link definition with a
# relative target, and a version that is not X.Y.Z or X.Y.Z-<suffix>. Then
# every released section of the real desktop/CHANGELOG.md must give a
# text, so such a line fails on its pull request instead of stopping a
# tag. Runs on Linux and macOS.
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
- **Paths** — [up and back](../docs/../CHANGELOG.md), [down and back](sub/../README.md), [dots](./a/./b.md), [a query](../docs/hub.md?plain=1#update), [a query with dots](README.md?path=../../x), [parentheses](../a(b).md), [a host](//vaultsync.eu/x) and [a definition][r].
- **Code and images** — `a[0](b)` and ``x ` [y](z)`` stay, [`code`](README.md) is a link, ![remote](https://example.org/a.png), [![remote](https://example.org/a.png)](README.md) and [angled](<https://example.org/a b>).
- **A lone backtick** — a ` and [after it](README.md).

[r]: https://example.org/r
   [s]: <https://example.org/s> "S"
[^1]: A footnote.

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
	sed "s#@B@#https://github.com/psimaker/vaultsync/blob/desktop-v$1#g" <<'EOF'
## [1.2.1] — 2026-12-01

### Added

- **Links** ([#1](https://github.com/psimaker/vaultsync/issues/1)) — the [root changelog](@B@/CHANGELOG.md), [a page](@B@/docs/hub.md#update-the-hub), [the README](@B@/desktop/README.md), [the same](@B@/desktop/README.md), [the root](@B@/SECURITY.md), [an anchor](#added), [mail](mailto:a@example.org), [two](@B@/CHANGELOG.md) on [one line](@B@/desktop/README.md) and [none]().
- **Paths** — [up and back](@B@/CHANGELOG.md), [down and back](@B@/desktop/README.md), [dots](@B@/desktop/a/b.md), [a query](@B@/docs/hub.md?plain=1#update), [a query with dots](@B@/desktop/README.md?path=../../x), [parentheses](@B@/a(b).md), [a host](//vaultsync.eu/x) and [a definition][r].
- **Code and images** — `a[0](b)` and ``x ` [y](z)`` stay, [`code`](@B@/desktop/README.md) is a link, ![remote](https://example.org/a.png), [![remote](https://example.org/a.png)](@B@/desktop/README.md) and [angled](<https://example.org/a b>).
- **A lone backtick** — a ` and [after it](@B@/desktop/README.md).

[r]: https://example.org/r
   [s]: <https://example.org/s> "S"
[^1]: A footnote.

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
printf '%s\n' "## [2.0.0] — 2026-12-01" "" "### Fixed" "" "---" "" "## [1.0.0] — 2026-11-01" "" "- **Older**." | fixture headings
run headings 2.0.0 "$tmp/headings.md"
refused headings 1
grep -q -F 'line 1: the section of 2.0.0 holds no entry' "$tmp/headings.err" || fail "headings: the reason does not name the heading's line"

# refusedat NAME LINE — a link refused, nothing printed, and its line named.
refusedat() {
	printf '%s\n' "## [2.0.0] — 2026-12-01" "" "- **One** — before." "$2" "" "## [1.0.0] — 2026-11-01" "" "- **Older**." | fixture "$1"
	run "$1" 2.0.0 "$tmp/$1.md"
	refused "$1" 1
	grep -q -F "$1.md line 4: " "$tmp/$1.err" || { cat "$tmp/$1.err" >&2; fail "$1: the reason does not name line 4"; }
}
refusedat above "- [Out](../../elsewhere.md)."
refusedat dot-above "- [Out](./../../elsewhere.md)."
refusedat mid-above "- [Out](../docs/../../elsewhere.md)."
refusedat root-above "- [Out](/../elsewhere.md)."
refusedat image "- ![A picture](../docs/images/a.png)."
refusedat image-inside "- [![A picture](images/a.png)](https://example.org)."
refusedat angled "- [A file](<../a b.md>)."
refusedat definition "[r]: ../CHANGELOG.md"
refusedat definition-angled "  [r]: <../CHANGELOG.md> \"Root\""
refusedat definition-empty "[r]:"

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
