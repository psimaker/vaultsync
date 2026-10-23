#!/bin/bash
# sign-release-test.sh — runs desktop/macos/sign.sh against a stand-in for
# rcodesign that records every call and plays Apple (#222): the release path
# (the team checked before anything is signed, Developer ID flags and
# -C /dev/null on every call, no rcodesign_* or RUST_LOG reaching rcodesign,
# each bundle uploaded once, every step that talks to Apple tried again, the
# verdict read from the last poll line, a refused or missing verdict
# stopping everything, the ticket stapled before the zip) and the refusals.
# The stand-in signs nothing: what Apple's tools say about real signatures
# is verify.sh's part, on a Mac.
#
# With REAL_RCODESIGN naming the pinned rcodesign, every flag sign.sh passes
# is checked against that binary's own --help, and the two log lines
# sign.sh reads must be in it. Runs on Linux and macOS (bash 3.2).
set -euo pipefail

here=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
sign="$here/../sign.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

if [ -n "${REAL_RCODESIGN:-}" ]; then
	for line in 'created submission ID: ' 'poll state after '; do
		grep -a -q -F "$line" "$REAL_RCODESIGN" || fail "the pinned rcodesign no longer prints '$line'"
	done
	printf '%s\n' "$REAL_RCODESIGN" >"$tmp/real"
fi

# What the build jobs hand over: two bare agents, two packed bundles.
mkdir -p "$tmp/plain" "$tmp/macos"
for arch in arm64 amd64; do
	printf 'agent %s\n' "$arch" >"$tmp/plain/vaultsync_darwin_$arch"
	contents="$tmp/bundle-$arch/VaultSync.app/Contents"
	mkdir -p "$contents/MacOS"
	printf 'plist\n' >"$contents/Info.plist"
	cp "$tmp/plain/vaultsync_darwin_$arch" "$contents/MacOS/vaultsync"
	printf 'tray\n' >"$contents/MacOS/vaultsync-tray"
	tar -C "$tmp/bundle-$arch" -cf "$tmp/macos/VaultSync_darwin_$arch.app.tar" VaultSync.app
done
touch "$tmp/developer-id.p12" "$tmp/password" "$tmp/api-key.json"

# The stand-in. sign.sh clears rcodesign's environment, so it is steered by
# files in $tmp/current: status (the verdict; default Accepted), team (the
# certificate's; default TEAM123456), and the switches submit-noid and
# wait-dead. By default the first sign, the first upload, the first wait
# for each submission and the first staple of each bundle fail, as a
# network error would; a staple writes the ticket into the bundle.
mkdir "$tmp/bin"
cat >"$tmp/bin/rcodesign" <<'STANDIN'
#!/bin/bash
base=$(cd "$(dirname "$0")/.." && pwd)
cur="$base/current"
echo "$*" >>"$cur/calls"
env | grep -i -E '^(rcodesign_|rust_log=)' >>"$cur/leaked" || true
cmd=$1
shift
if [ -s "$base/real" ]; then
	help=$("$(cat "$base/real")" "$cmd" --help 2>&1) || { echo "the pinned rcodesign has no $cmd" >&2; exit 2; }
	for a in "$@"; do
		case $a in
			-*)
				grep -q -E -- "(^|[[:space:],])$a([[:space:],=]|$)" <<<"$help" || { echo "the pinned rcodesign's $cmd has no $a" >&2; exit 2; }
				;;
		esac
	done
fi
last=${*: -1}
first() { # MARK — true the first time MARK is seen
	[ -e "$cur/seen-$1" ] && return 1
	touch "$cur/seen-$1"
}
case $cmd in
	analyze-certificate)
		echo "# Certificate 0"
		echo "Team ID:                     $(cat "$cur/team" 2>/dev/null || echo TEAM123456)"
		;;
	sign)
		if first sign; then
			echo "CMS error: Time-Stamp Protocol error: bad HTTP response" >&2
			exit 1
		fi
		;;
	notary-submit)
		if first submit; then
			echo "error sending request: connection reset" >&2
			exit 1
		fi
		[ -e "$cur/submit-noid" ] && exit 0
		echo "created submission ID: sub-$(grep -c '^notary-submit' "$cur/calls")" >&2
		;;
	notary-wait)
		if [ -e "$cur/wait-dead" ] || first "wait-$last"; then
			echo "error sending request: connection reset" >&2
			exit 1
		fi
		echo "poll state after 0s: InProgress" >&2
		echo "poll state after 3s: InProgress" >&2
		echo "poll state after 6s: $(cat "$cur/status" 2>/dev/null || echo Accepted)" >&2
		echo "notary log> \"status\": \"$(cat "$cur/status" 2>/dev/null || echo Accepted)\"" >&2
		;;
	staple)
		if first "staple-$(printf '%s' "$last" | tr '/' '_')"; then
			exit 1
		fi
		echo ticket >"$last/Contents/CodeResources"
		;;
	*)
		echo "unexpected rcodesign call: $cmd $*" >&2
		exit 3
		;;
esac
STANDIN
chmod 755 "$tmp/bin/rcodesign"

# run NAME [SWITCH=value...] [-- VAR=value...] — sign.sh into $tmp/NAME/out;
# SWITCHes become the stand-in's files, VARs sign.sh's environment; the
# record ends in $tmp/NAME, output in $tmp/NAME/log; sets rc.
run() {
	local name=$1 vars=() a
	shift
	rm -rf "$tmp/current"
	mkdir "$tmp/current"
	while [ $# -gt 0 ]; do
		a=$1
		shift
		[ "$a" = -- ] && break
		printf '%s\n' "${a#*=}" >"$tmp/current/${a%%=*}"
	done
	vars=("$@")
	rc=0
	env PATH="$tmp/bin:$PATH" VAULTSYNC_SIGN_RETRY_DELAY=0 ${vars[@]+"${vars[@]}"} \
		"$sign" "$tmp/plain" "$tmp/macos" "$tmp/$name.out" >"$tmp/current/log" 2>&1 || rc=$?
	touch "$tmp/current/calls" "$tmp/current/leaked"
	mv "$tmp/current" "$tmp/$name"
}
release=(VAULTSYNC_SIGN_P12="$tmp/developer-id.p12" VAULTSYNC_SIGN_P12_PASSWORD_FILE="$tmp/password" VAULTSYNC_NOTARY_KEY="$tmp/api-key.json" VAULTSYNC_SIGN_TEAM=TEAM123456)
count() { grep -cE -- "$1" "$2" || true; }

# A release that Apple accepts, through one failure of every step that
# talks to Apple, with rcodesign settings in the caller's environment.
run accepted -- "${release[@]}" rcodesign_sign_digest=sha1 RCODESIGN_SIGN_DIGEST=sha1 RUST_LOG=error
[ "$rc" = 0 ] || { cat "$tmp/accepted/log"; fail "release: sign.sh exited $rc"; }
calls="$tmp/accepted/calls"
[ ! -s "$tmp/accepted/leaked" ] || fail "rcodesign saw: $(sort -u "$tmp/accepted/leaked" | tr '\n' ' ')"
[ "$(count ' -C /dev/null( |$)' "$calls")" = "$(wc -l <"$calls" | tr -d ' ')" ] || fail "a call without -C /dev/null"
[ "$(head -1 "$calls" | cut -d' ' -f1)" = analyze-certificate ] || fail "the certificate's team not checked first"
[ "$(count '^sign .*--for-notarization --p12-file .*/developer-id.p12 --p12-password-file .*/password' "$calls")" = 5 ] ||
	fail "release: not every signature with the Developer ID for notarization, or the failed one not tried again"
[ "$(count '^sign .*--binary-identifier eu.vaultsync.agent .*\.out/vaultsync_darwin_' "$calls")" = 3 ] || fail "bare agents not signed as eu.vaultsync.agent"
[ "$(count '^sign .*--binary-identifier Contents/MacOS/vaultsync:eu.vaultsync.agent .*/VaultSync.app$' "$calls")" = 2 ] ||
	fail "bundled agents not signed as eu.vaultsync.agent"
[ "$(count '^notary-submit .*/VaultSync.app$' "$calls")" = 3 ] || fail "the failed upload not tried again, or a bundle uploaded twice"
[ "$(count '^notary-submit' "$calls")" = 3 ] || fail "something other than the two bundles uploaded"
[ "$(count '^notary-wait .*--max-wait-seconds 2400 sub-2$' "$calls")" = 2 ] || fail "the wait for sub-2 not tried again after a network error"
[ "$(count '^notary-wait .*--max-wait-seconds 2400 sub-3$' "$calls")" = 2 ] || fail "the wait for sub-3 not tried again after a network error"
[ "$(count '^staple' "$calls")" = 4 ] || fail "the staples not tried again"
grep -q 'VaultSync_darwin_arm64: notary submission sub-2: Accepted' "$tmp/accepted/log" || fail "submission and verdict not in the log"
grep -q 'notary log>' "$tmp/accepted/log" || fail "Apple's notary log not in the log"
for arch in arm64 amd64; do
	[ -s "$tmp/accepted.out/vaultsync_darwin_$arch" ] || fail "release: vaultsync_darwin_$arch missing"
	[ "$(unzip -p "$tmp/accepted.out/VaultSync_darwin_$arch.zip" VaultSync.app/Contents/CodeResources)" = ticket ] ||
		fail "VaultSync_darwin_$arch.zip was made without the stapled ticket"
done

# A verdict other than Accepted stops everything: no staple, no zip.
run invalid status=Invalid -- "${release[@]}"
[ "$rc" != 0 ] || fail "an Invalid verdict was not refused"
grep -q 'did not accept VaultSync_darwin_arm64 (Invalid, submission sub-2)' "$tmp/invalid/log" || fail "the refusal does not name the bundle"
[ "$(count '^staple' "$tmp/invalid/calls")" = 0 ] || fail "stapled after an Invalid verdict"
[ ! -e "$tmp/invalid.out/VaultSync_darwin_arm64.zip" ] || fail "a zip was made after an Invalid verdict"

# No submission ID, or a wait that never gets through: nothing after it.
run noid submit-noid=1 -- "${release[@]}"
[ "$rc" != 0 ] || fail "an upload without a submission ID was not refused"
grep -q 'no submission ID for VaultSync_darwin_arm64' "$tmp/noid/log" || fail "the missing submission ID not named"
[ "$(count '^notary-wait' "$tmp/noid/calls")" = 0 ] || fail "waited without a submission ID"
run waitdead wait-dead=1 -- "${release[@]}"
[ "$rc" != 0 ] || fail "a wait that never got through was not refused"
[ "$(count '^notary-wait' "$tmp/waitdead/calls")" = 5 ] || fail "the wait not tried five times"
[ "$(count '^staple' "$tmp/waitdead/calls")" = 0 ] || fail "stapled without a verdict"

# Another team's certificate: refused before anything is signed.
run team team=OTHER12345 -- "${release[@]}"
[ "$rc" != 0 ] || fail "another team's certificate was not refused"
grep -q "the certificate is team 'OTHER12345', not TEAM123456" "$tmp/team/log" || fail "the team refusal does not name both teams"
[ "$(count '^sign' "$tmp/team/calls")" = 0 ] || fail "signed with another team's certificate"

# A pull request: ad hoc, the hardened runtime asked for, nothing for Apple.
run adhoc
[ "$rc" = 0 ] || { cat "$tmp/adhoc/log"; fail "ad hoc: sign.sh exited $rc"; }
[ "$(count '^sign -C /dev/null --code-signature-flags runtime ' "$tmp/adhoc/calls")" = 5 ] || fail "ad hoc: not every binary with the runtime flag"
[ "$(count 'Contents/MacOS/vaultsync:runtime' "$tmp/adhoc/calls")" = 2 ] || fail "ad hoc: the bundled agent without the runtime flag"
[ "$(count '--for-notarization|--p12-file|^notary|^staple|^analyze' "$tmp/adhoc/calls")" = 0 ] || fail "ad hoc: a release step ran"

# Refusals before rcodesign runs at all.
run half -- VAULTSYNC_SIGN_P12="$tmp/developer-id.p12"
[ "$rc" = 2 ] || fail "half of the release settings were not refused (exit $rc)"
[ ! -s "$tmp/half/calls" ] || fail "rcodesign ran with half of the release settings"
run delay -- VAULTSYNC_SIGN_RETRY_DELAY=soon
[ "$rc" = 2 ] || fail "a retry delay that is not a number was not refused (exit $rc)"

echo "sign-release-test: all cases passed${REAL_RCODESIGN:+ (flags checked against $REAL_RCODESIGN)}"
