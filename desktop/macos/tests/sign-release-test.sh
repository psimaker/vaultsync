#!/bin/bash
# sign-release-test.sh — runs desktop/macos/sign.sh against a stand-in for
# rcodesign that records every call and plays Apple (#222): the release path
# (the certificate's team, profile and expiry checked before anything is
# signed; the API key encoded by sign.sh and passed to every notary call;
# Developer ID flags and -C /dev/null on every call; nothing named
# rcodesign_*, RUST_LOG or an AWS setting reaching rcodesign; each bundle
# uploaded once under its own architecture; every step that talks to Apple
# tried again; the verdict read from the last poll line; a refused or
# missing verdict and exhausted staples stopping everything; the ticket
# stapled before the zip) and the refusals. The stand-in signs nothing:
# what Apple's tools say about real signatures is verify.sh's part, on a Mac.
#
# With REAL_RCODESIGN naming the pinned rcodesign, every flag sign.sh passes
# is checked against that binary's own --help, and the lines sign.sh reads
# must be in it. Runs on Linux and macOS (bash 3.2), in an environment of
# its own.
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
	for line in 'created submission ID: ' 'poll state after ' 'Team ID:' 'Guessed Certificate Profile:' 'Not Valid After:' 'Accepted' 'DeveloperIdApplication'; do
		grep -a -q -F "$line" "$REAL_RCODESIGN" || fail "the pinned rcodesign no longer has '$line'"
	done
	printf '%s\n' "$REAL_RCODESIGN" >"$tmp/real"
fi

# What the build jobs hand over: two bare agents, two packed bundles.
mkdir -p "$tmp/plain" "$tmp/macos"
for arch in arm64 amd64; do
	printf 'agent %s\n' "$arch" >"$tmp/plain/vaultsync_darwin_$arch"
	contents="$tmp/bundle-$arch/VaultSync for Mac.app/Contents"
	mkdir -p "$contents/MacOS"
	printf 'plist %s\n' "$arch" >"$contents/Info.plist"
	cp "$tmp/plain/vaultsync_darwin_$arch" "$contents/MacOS/vaultsync"
	printf 'tray\n' >"$contents/MacOS/vaultsync-tray"
	tar -C "$tmp/bundle-$arch" -cf "$tmp/macos/VaultSync_darwin_$arch.app.tar" "VaultSync for Mac.app"
done
touch "$tmp/developer-id.p12" "$tmp/password" "$tmp/AuthKey.p8"

# The stand-in. sign.sh clears rcodesign's environment, so it is steered by
# files in $tmp/current: status (the verdict; default Accepted, "none" for
# no poll line), team (default TEAM123456), profile (default
# DeveloperIdApplication), until (default 2099-01-01T00:00:00+00:00), and
# the switches submit-noid, wait-dead and staple-dead. By default the first
# try of every signature, every upload, every wait and every staple fails,
# as a network error would; encoding writes a key file sign.sh must pass
# on; a staple writes the ticket into the bundle.
mkdir "$tmp/bin"
cat >"$tmp/bin/rcodesign" <<'STANDIN'
#!/bin/bash
base=$(cd "$(dirname "$0")/.." && pwd)
cur="$base/current"
echo "$*" >>"$cur/calls"
env | grep -i -E '^(rcodesign_|rust_log=|aws_(profile|access|secret|region|endpoint))' >>"$cur/leaked" || true
env | grep -E '^AWS_(CONFIG_FILE|SHARED_CREDENTIALS_FILE)=' | grep -v '=/dev/null$' >>"$cur/leaked" || true
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
setting() { cat "$cur/$1" 2>/dev/null || echo "$2"; }
first() { # MARK — true the first time MARK is seen
	local mark
	mark="$cur/seen-$(printf '%s' "$1" | tr '/ ' '__')"
	[ -e "$mark" ] && return 1
	touch "$mark"
}
key() { # the --api-key-file argument must be the key sign.sh encoded
	local prev=""
	for a in "$@"; do
		if [ "$prev" = --api-key-file ]; then
			[ "$(cat "$a" 2>/dev/null)" = encoded-key ] && return 0
			echo "--api-key-file $a is not the encoded key" >&2
			exit 4
		fi
		prev=$a
	done
	echo "no --api-key-file" >&2
	exit 4
}
case $cmd in
	analyze-certificate)
		echo "# Certificate 0"
		echo "Team ID:                     $(setting team TEAM123456)"
		echo "Not Valid After:             $(setting until 2099-01-01T00:00:00+00:00)"
		echo "Guessed Certificate Profile: $(setting profile DeveloperIdApplication)"
		;;
	encode-app-store-connect-api-key)
		prev=""
		for a in "$@"; do
			[ "$prev" = -o ] && echo encoded-key >"$a"
			prev=$a
		done
		;;
	sign)
		if first "sign $last"; then
			echo "CMS error: Time-Stamp Protocol error: bad HTTP response" >&2
			exit 1
		fi
		;;
	notary-submit)
		key "$@"
		if first "submit $last"; then
			echo "error sending request: connection reset" >&2
			exit 1
		fi
		[ -e "$cur/submit-noid" ] && exit 0
		echo "created submission ID: sub-$(head -1 "$last/Contents/Info.plist" | cut -d' ' -f2)" >&2
		;;
	notary-wait)
		key "$@"
		if [ -e "$cur/wait-dead" ] || first "wait $last"; then
			echo "error sending request: connection reset" >&2
			exit 1
		fi
		verdict=$(setting status Accepted)
		# "none": no poll line at all, as when the log level hides them.
		[ "$verdict" = none ] && exit 0
		echo "poll state after 0s: InProgress" >&2
		echo "poll state after 3s: InProgress" >&2
		echo "poll state after 6s: $verdict" >&2
		echo "notary log> \"status\": \"$verdict\"" >&2
		;;
	staple)
		if [ -e "$cur/staple-dead" ] || first "staple $last"; then
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

# run NAME [SWITCH=value...] [-- VAR=value...] — sign.sh into $tmp/NAME.out,
# in an environment of its own; SWITCHes become the stand-in's files, VARs
# sign.sh's environment; the record ends in $tmp/NAME, output in
# $tmp/NAME/log; sets rc.
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
	env -i PATH="$tmp/bin:$PATH" HOME="$tmp" TMPDIR="${TMPDIR:-/tmp}" VAULTSYNC_SIGN_RETRY_DELAY=0 ${vars[@]+"${vars[@]}"} \
		"$sign" "$tmp/plain" "$tmp/macos" "$tmp/$name.out" >"$tmp/current/log" 2>&1 || rc=$?
	touch "$tmp/current/calls" "$tmp/current/leaked"
	mv "$tmp/current" "$tmp/$name"
}
release=(VAULTSYNC_SIGN_P12="$tmp/developer-id.p12" VAULTSYNC_SIGN_P12_PASSWORD_FILE="$tmp/password"
	VAULTSYNC_NOTARY_P8="$tmp/AuthKey.p8" VAULTSYNC_NOTARY_KEY_ID=KEY123 VAULTSYNC_NOTARY_ISSUER=issuer-uuid VAULTSYNC_SIGN_TEAM=TEAM123456)
count() { grep -cE -- "$1" "$2" || true; }

# A release that Apple accepts, through one failure of every step that
# talks to Apple, with rcodesign and AWS settings in the environment.
run accepted -- "${release[@]}" rcodesign_sign_digest=sha1 RCODESIGN_SIGN_DIGEST=sha1 RUST_LOG=error AWS_PROFILE=other AWS_CONFIG_FILE="$tmp/aws"
[ "$rc" = 0 ] || { cat "$tmp/accepted/log"; fail "release: sign.sh exited $rc"; }
calls="$tmp/accepted/calls"
[ ! -s "$tmp/accepted/leaked" ] || fail "rcodesign saw: $(sort -u "$tmp/accepted/leaked" | tr '\n' ' ')"
[ "$(count ' -C /dev/null( |$)' "$calls")" = "$(wc -l <"$calls" | tr -d ' ')" ] || fail "a call without -C /dev/null"
[ "$(head -1 "$calls" | cut -d' ' -f1)" = analyze-certificate ] || fail "the certificate not checked first"
grep -q '^encode-app-store-connect-api-key .* -o .* issuer-uuid KEY123 .*/AuthKey.p8$' "$calls" || fail "the API key not encoded from issuer, key ID and .p8"
[ "$(count '^sign .*--for-notarization --p12-file .*/developer-id.p12 --p12-password-file .*/password' "$calls")" = 8 ] ||
	fail "release: not every signature with the Developer ID for notarization, or a failed one not tried again"
for arch in arm64 amd64; do
	[ "$(count "^sign .*--binary-identifier eu.vaultsync.agent .*\.out/vaultsync_darwin_$arch\$" "$calls")" = 2 ] ||
		fail "the bare $arch agent not signed as eu.vaultsync.agent, or not tried again"
	[ "$(count "^sign .*--binary-identifier Contents/MacOS/vaultsync:eu.vaultsync.agent .*/$arch/VaultSync for Mac.app\$" "$calls")" = 2 ] ||
		fail "the $arch bundle's agent not signed as eu.vaultsync.agent, or not tried again"
	[ "$(count "^notary-submit .*/$arch/VaultSync for Mac.app\$" "$calls")" = 2 ] || fail "the $arch bundle not uploaded, or the failed upload not tried again"
	[ "$(count "^notary-wait .*--max-wait-seconds [0-9]+ sub-$arch\$" "$calls")" = 2 ] || fail "the wait for $arch not tried again after a network error"
	[ "$(count "^staple .*/$arch/VaultSync for Mac.app\$" "$calls")" = 2 ] || fail "the $arch staple not tried again"
	grep -q "VaultSync_darwin_$arch: notary submission sub-$arch: Accepted" "$tmp/accepted/log" || fail "$arch submission and verdict not in the log"
	[ -s "$tmp/accepted.out/vaultsync_darwin_$arch" ] || fail "release: vaultsync_darwin_$arch missing"
	[ "$(unzip -p "$tmp/accepted.out/VaultSync_darwin_$arch.zip" "VaultSync for Mac.app/Contents/CodeResources")" = ticket ] ||
		fail "VaultSync_darwin_$arch.zip was made without the stapled ticket"
done
[ "$(count '^notary-submit' "$calls")" = 4 ] || fail "something other than the two bundles uploaded"
grep -q 'notary log>' "$tmp/accepted/log" || fail "Apple's notary log not in the log"
grep -q 'try 1 of 3 failed: rc sign' "$tmp/accepted/log" || fail "a failed try not said"
grep -q 'Time-Stamp Protocol error' "$tmp/accepted/log" || fail "a failed try's reason not shown"

# Verdicts other than Accepted stop everything: no staple, no zip.
for verdict in Invalid Rejected none; do
	run "verdict-$verdict" status="$verdict" -- "${release[@]}"
	[ "$rc" != 0 ] || fail "the verdict '$verdict' was not refused"
	want=$verdict
	[ "$verdict" = none ] && want="no verdict"
	grep -q "did not accept VaultSync_darwin_arm64 ($want, submission sub-arm64)" "$tmp/verdict-$verdict/log" || fail "the refusal of '$verdict' does not name the bundle"
	[ "$(count '^staple' "$tmp/verdict-$verdict/calls")" = 0 ] || fail "stapled after the verdict '$verdict'"
	[ ! -e "$tmp/verdict-$verdict.out/VaultSync_darwin_arm64.zip" ] || fail "a zip was made after the verdict '$verdict'"
done

# No submission ID, a wait that never gets through, staples that never do.
run noid submit-noid=1 -- "${release[@]}"
[ "$rc" != 0 ] || fail "an upload without a submission ID was not refused"
grep -q 'no submission ID for VaultSync_darwin_arm64' "$tmp/noid/log" || fail "the missing submission ID not named"
[ "$(count '^notary-wait' "$tmp/noid/calls")" = 0 ] || fail "waited without a submission ID"
run waitdead wait-dead=1 -- "${release[@]}"
[ "$rc" != 0 ] || fail "a wait that never got through was not refused"
[ "$(count '^notary-wait' "$tmp/waitdead/calls")" = 3 ] || fail "the wait not tried three times"
[ "$(count '^staple' "$tmp/waitdead/calls")" = 0 ] || fail "stapled without a verdict"
run stapledead staple-dead=1 -- "${release[@]}"
[ "$rc" != 0 ] || fail "staples that never worked were not refused"
[ "$(count '^staple' "$tmp/stapledead/calls")" = 5 ] || fail "the staple not tried five times"
[ ! -e "$tmp/stapledead.out/VaultSync_darwin_arm64.zip" ] || fail "a zip was made without a ticket"

# The wrong certificate: refused before anything is signed.
for wrong in team=OTHER12345 profile=DeveloperIdInstaller until=2020-01-01T00:00:00+00:00; do
	run "cert-${wrong%%=*}" "$wrong" -- "${release[@]}"
	[ "$rc" != 0 ] || fail "a certificate with $wrong was not refused"
	[ "$(count '^(sign|notary|encode)' "$tmp/cert-${wrong%%=*}/calls")" = 0 ] || fail "went on with a certificate with $wrong"
done
grep -q "the certificate is team 'OTHER12345', not TEAM123456" "$tmp/cert-team/log" || fail "the team refusal does not name both teams"
grep -q "expired on 2020-01-01" "$tmp/cert-until/log" || fail "the expiry refusal does not name the date"

# A pull request: ad hoc, the hardened runtime asked for, nothing for Apple.
run adhoc
[ "$rc" = 0 ] || { cat "$tmp/adhoc/log"; fail "ad hoc: sign.sh exited $rc"; }
[ "$(count '^sign -C /dev/null --code-signature-flags runtime ' "$tmp/adhoc/calls")" = 8 ] || fail "ad hoc: not every binary with the runtime flag"
[ "$(count 'Contents/MacOS/vaultsync:runtime' "$tmp/adhoc/calls")" = 4 ] || fail "ad hoc: the bundled agent without the runtime flag"
[ "$(count '--for-notarization|--p12-file|^notary|^staple|^analyze|^encode' "$tmp/adhoc/calls")" = 0 ] || fail "ad hoc: a release step ran"

# Refusals before rcodesign runs at all.
run half -- VAULTSYNC_SIGN_P12="$tmp/developer-id.p12"
[ "$rc" = 2 ] || fail "half of the release settings were not refused (exit $rc)"
[ ! -s "$tmp/half/calls" ] || fail "rcodesign ran with half of the release settings"
run delay -- VAULTSYNC_SIGN_RETRY_DELAY=soon
[ "$rc" = 2 ] || fail "a retry delay that is not a number was not refused (exit $rc)"

echo "sign-release-test: all cases passed${REAL_RCODESIGN:+ (flags checked against $REAL_RCODESIGN)}"
