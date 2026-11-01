#!/bin/bash
# verify-team-test.sh — holds verify.sh's release checks (Developer ID, the
# team, the exact designated requirement) to a binary someone else signed
# and notarized (#222): the macOS build of the pinned rcodesign, team
# MK22MZP987, which codesign prints unquoted — the spelling verify.sh first
# refused. A codesign shim prints the same requirement quoted, as codesign
# does for a team that starts with a digit; another team, another
# identifier and a team that only starts the same must be refused. macOS
# only; downloads the fixture and checks it against its SHA-256.
set -euo pipefail

here=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
verify="$here/../verify.sh"
version=0.29.0
sha256=d1a532150adaf90048260d76359261aa716abafc45c53c5dc18845029184334a
identifier=rcodesign-47bf88cffedf675b
team=MK22MZP987
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

name="apple-codesign-${version}-aarch64-apple-darwin"
curl -fsSL --retry 3 -o "$tmp/$name.tar.gz" \
	"https://github.com/indygreg/apple-platform-rs/releases/download/apple-codesign%2F${version}/$name.tar.gz"
echo "$sha256  $tmp/$name.tar.gz" | shasum -a 256 -c - >/dev/null
tar -xzf "$tmp/$name.tar.gz" -C "$tmp"
bin="$tmp/$name/rcodesign"

"$verify" --check-binary "$bin" "$identifier" "$team" >"$tmp/log" 2>&1 || { cat "$tmp/log"; fail "the correctly signed fixture was refused"; }

# The same requirement as codesign prints it for a team that needs quotes.
mkdir "$tmp/shim"
cat >"$tmp/shim/codesign" <<SHIM
#!/bin/bash
if [ "\$*" = "-d -r- $bin" ]; then
	/usr/bin/codesign "\$@" 2>&1 | sed 's/leaf\[subject.OU\] = $team\$/leaf[subject.OU] = "$team"/'
	exit "\${PIPESTATUS[0]}"
fi
exec /usr/bin/codesign "\$@"
SHIM
chmod 755 "$tmp/shim/codesign"
PATH="$tmp/shim:$PATH" "$verify" --check-binary "$bin" "$identifier" "$team" >"$tmp/log" 2>&1 ||
	{ cat "$tmp/log"; fail "the quoted spelling of the same requirement was refused"; }
PATH="$tmp/shim:$PATH" codesign -d -r- "$bin" 2>&1 | grep -q "= \"$team\"\$" || fail "the shim did not quote the team"

for wrong in "$identifier ABCDE12345" "$identifier ${team%?}" "rcodesign $team"; do
	read -r id t <<<"$wrong"
	if "$verify" --check-binary "$bin" "$id" "$t" >"$tmp/log" 2>&1; then
		fail "verify.sh accepted identifier $id of team $t"
	fi
done

echo "verify-team-test: all cases passed"
