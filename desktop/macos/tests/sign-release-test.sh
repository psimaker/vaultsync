#!/bin/bash
# sign-release-test.sh — runs desktop/macos/sign.sh against a stand-in for
# rcodesign that records every call and plays Apple's notary service (#222):
# the release path (Developer ID flags and -C /dev/null on every call, each
# bundle uploaded once, a wait tried again after a network error, a verdict
# other than Accepted refused, a staple tried again) and the refusals (an
# RCODESIGN_* variable, half of the release settings). The stand-in signs
# nothing: what Apple's tools say about real signatures is verify.sh's part,
# on a Mac. Runs on Linux and macOS (bash 3.2).
set -euo pipefail

here=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
sign="$here/../sign.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

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

# The stand-in. FAKE is its record directory; FAKE_STATUS is the verdict a
# finished wait reports. Every wait and every staple fails on its first try
# for a target, as a network error would.
mkdir "$tmp/bin"
cat >"$tmp/bin/rcodesign" <<'STANDIN'
#!/bin/bash
echo "$*" >>"$FAKE/calls"
cmd=$1
shift
last=${*: -1}
case $cmd in
	sign) ;;
	notary-submit)
		n=$(grep -c '^notary-submit' "$FAKE/calls")
		echo "created submission ID: sub-$n" >&2
		;;
	notary-wait)
		if [ ! -e "$FAKE/waited-$last" ]; then
			touch "$FAKE/waited-$last"
			echo "error sending request: connection reset" >&2
			exit 1
		fi
		echo "poll state after 3s: ${FAKE_STATUS:-Accepted}" >&2
		echo "notary log> \"status\": \"${FAKE_STATUS:-Accepted}\"" >&2
		;;
	staple)
		mark="$FAKE/stapled-$(printf '%s' "$last" | tr '/' '_')"
		if [ ! -e "$mark" ]; then
			touch "$mark"
			exit 1
		fi
		;;
	*)
		echo "unexpected rcodesign call: $cmd $*" >&2
		exit 3
		;;
esac
STANDIN
chmod 755 "$tmp/bin/rcodesign"

# run NAME [VAR=value...] — sign.sh into $tmp/NAME/out, the stand-in's
# record in $tmp/NAME, output in $tmp/NAME/log; sets rc.
run() {
	local name=$1
	shift
	mkdir -p "$tmp/$name"
	rc=0
	env FAKE="$tmp/$name" PATH="$tmp/bin:$PATH" VAULTSYNC_SIGN_RETRY_DELAY=0 "$@" \
		"$sign" "$tmp/plain" "$tmp/macos" "$tmp/$name/out" >"$tmp/$name/log" 2>&1 || rc=$?
	touch "$tmp/$name/calls"
}
release=(VAULTSYNC_SIGN_P12="$tmp/developer-id.p12" VAULTSYNC_SIGN_P12_PASSWORD_FILE="$tmp/password" VAULTSYNC_NOTARY_KEY="$tmp/api-key.json")
count() { grep -cE -- "$1" "$2" || true; }

# A release that Apple accepts.
run accepted "${release[@]}"
[ "$rc" = 0 ] || { cat "$tmp/accepted/log"; fail "release: sign.sh exited $rc"; }
calls="$tmp/accepted/calls"
[ "$(count ' -C /dev/null ' "$calls")" = "$(wc -l <"$calls" | tr -d ' ')" ] || fail "a call without -C /dev/null"
[ "$(count '^sign .*--for-notarization --p12-file .*/developer-id.p12 --p12-password-file .*/password' "$calls")" = 4 ] ||
	fail "release: not every binary signed with the Developer ID for notarization"
[ "$(count '^sign .*--binary-identifier eu.vaultsync.agent .*/out/vaultsync_darwin_' "$calls")" = 2 ] || fail "bare agents not signed as eu.vaultsync.agent"
[ "$(count '^sign .*--binary-identifier Contents/MacOS/vaultsync:eu.vaultsync.agent .*/VaultSync.app$' "$calls")" = 2 ] ||
	fail "bundled agents not signed as eu.vaultsync.agent"
[ "$(count '^notary-submit .*/VaultSync.app$' "$calls")" = 2 ] || fail "each bundle not uploaded exactly once"
[ "$(count '^notary-submit' "$calls")" = 2 ] || fail "something other than the two bundles uploaded"
[ "$(count '^notary-wait .* sub-1$' "$calls")" = 2 ] || fail "the wait for sub-1 not tried again after a network error"
[ "$(count '^notary-wait .* sub-2$' "$calls")" = 2 ] || fail "the wait for sub-2 not tried again after a network error"
[ "$(count '^staple' "$calls")" = 4 ] || fail "the staples not tried again"
grep -q 'VaultSync_darwin_arm64: notary submission sub-1: Accepted' "$tmp/accepted/log" || fail "submission and verdict not in the log"
grep -q 'notary log>' "$tmp/accepted/log" || fail "Apple's notary log not in the log"
for f in vaultsync_darwin_arm64 vaultsync_darwin_amd64 VaultSync_darwin_arm64.zip VaultSync_darwin_amd64.zip; do
	[ -s "$tmp/accepted/out/$f" ] || fail "release: $f missing"
done

# A verdict other than Accepted stops everything: no staple, no zip.
run invalid "${release[@]}" FAKE_STATUS=Invalid
[ "$rc" != 0 ] || fail "an Invalid verdict was not refused"
grep -q 'did not accept VaultSync_darwin_arm64 (Invalid, submission sub-1)' "$tmp/invalid/log" || fail "the refusal does not name the bundle"
[ "$(count '^staple' "$tmp/invalid/calls")" = 0 ] || fail "stapled after an Invalid verdict"
[ ! -e "$tmp/invalid/out/VaultSync_darwin_arm64.zip" ] || fail "a zip was made after an Invalid verdict"

# A pull request: ad hoc, the hardened runtime asked for, nothing for Apple.
run adhoc
[ "$rc" = 0 ] || { cat "$tmp/adhoc/log"; fail "ad hoc: sign.sh exited $rc"; }
[ "$(count '^sign -C /dev/null --code-signature-flags runtime ' "$tmp/adhoc/calls")" = 4 ] || fail "ad hoc: not every binary with the runtime flag"
[ "$(count 'Contents/MacOS/vaultsync:runtime' "$tmp/adhoc/calls")" = 2 ] || fail "ad hoc: the bundled agent without the runtime flag"
[ "$(count '--for-notarization|--p12-file|^notary|^staple' "$tmp/adhoc/calls")" = 0 ] || fail "ad hoc: a release step ran"

# Refusals before anything is signed.
run rcvar "${release[@]}" RCODESIGN_SHA256=0
[ "$rc" = 2 ] || fail "an RCODESIGN_* variable was not refused (exit $rc)"
[ ! -s "$tmp/rcvar/calls" ] || fail "rcodesign ran with an RCODESIGN_* variable set"
run half VAULTSYNC_SIGN_P12="$tmp/developer-id.p12"
[ "$rc" = 2 ] || fail "half of the release settings were not refused (exit $rc)"
[ ! -s "$tmp/half/calls" ] || fail "rcodesign ran with half of the release settings"

echo "sign-release-test: all cases passed"
