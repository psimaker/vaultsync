#!/bin/bash
# sign.sh PLAIN MACOS OUT — signs VaultSync for Mac (decision 052): the bare
# agents PLAIN/vaultsync_darwin_<arch> and the bundles in
# MACOS/VaultSync_darwin_<arch>.app.tar (make-app.sh's output as the macOS
# build job packs it) become OUT/vaultsync_darwin_<arch> and
# OUT/VaultSync_darwin_<arch>.zip.
#
# A release (desktop-v* tag) sets VAULTSYNC_SIGN_P12,
# VAULTSYNC_SIGN_P12_PASSWORD_FILE and VAULTSYNC_NOTARY_KEY (the JSON of
# `rcodesign encode-app-store-connect-api-key`), each naming a file: the
# Developer ID signs with --for-notarization, Apple notarizes the bundles and
# their ticket is stapled. Without any of them — a pull request — the same
# commands sign ad hoc, with the hardened runtime asked for per binary, as
# --for-notarization sets it; nothing goes to Apple.
#
# The agent signs as eu.vaultsync.agent alone (what setup.sh installs) and
# inside the bundle: identifier and team make the designated requirement
# macOS ties a granted permission to, so the grant carries over to later
# releases (#222). Both copies are the same bytes signed the same way, so
# they share one code directory, and the bundle's notarization covers the
# bare agent too (verify.sh checks both). The bundle's executable takes
# CFBundleIdentifier.
#
# rcodesign must be on PATH. Every call passes -C /dev/null, so no
# rcodesign.toml (in this directory or the user's) changes what is signed,
# and no variable named RCODESIGN_* may be set: rcodesign reads each one as
# configuration and refuses unknown keys. VAULTSYNC_SIGN_RETRY_DELAY (seconds,
# default 30) is the pause between tries of a step that talks to Apple.
set -euo pipefail

[ $# -eq 3 ] || {
	echo "usage: sign.sh PLAIN MACOS OUT" >&2
	exit 2
}
for v in $(compgen -e); do
	case $v in
		RCODESIGN_*)
			echo "sign.sh: $v is set; rcodesign would read it as configuration — unset every RCODESIGN_* variable" >&2
			exit 2
			;;
	esac
done
plain=$1
macos=$2
mkdir -p "$3"
out=$(CDPATH='' cd -- "$3" && pwd)
archs="arm64 amd64"
delay=${VAULTSYNC_SIGN_RETRY_DELAY:-30}

p12=${VAULTSYNC_SIGN_P12:-}
password=${VAULTSYNC_SIGN_P12_PASSWORD_FILE:-}
notary=${VAULTSYNC_NOTARY_KEY:-}
if [ -n "$p12$password$notary" ]; then
	# A signed but unnotarized release must never be possible.
	if [ -z "$p12" ] || [ -z "$password" ] || [ -z "$notary" ]; then
		echo "sign.sh: a release needs VAULTSYNC_SIGN_P12, VAULTSYNC_SIGN_P12_PASSWORD_FILE and VAULTSYNC_NOTARY_KEY together" >&2
		exit 2
	fi
	release=1
	mode=(--for-notarization --p12-file "$p12" --p12-password-file "$password")
	inner=()
else
	release=0
	mode=(--code-signature-flags runtime)
	inner=(--code-signature-flags Contents/MacOS/vaultsync:runtime)
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

for arch in $archs; do
	cp "$plain/vaultsync_darwin_$arch" "$out/vaultsync_darwin_$arch"
	rcodesign sign -C /dev/null "${mode[@]}" --binary-identifier eu.vaultsync.agent "$out/vaultsync_darwin_$arch"
	mkdir "$work/$arch"
	tar -C "$work/$arch" -xf "$macos/VaultSync_darwin_$arch.app.tar"
	if [ -n "$(find "$work/$arch" -name '._*' -print -quit)" ]; then
		echo "sign.sh: VaultSync_darwin_$arch.app.tar holds ._ files" >&2
		exit 1
	fi
	rcodesign sign -C /dev/null "${mode[@]}" ${inner[@]+"${inner[@]}"} \
		--binary-identifier Contents/MacOS/vaultsync:eu.vaultsync.agent "$work/$arch/VaultSync.app"
done

# tries N LOG COMMAND... runs COMMAND until it succeeds, at most N times,
# its output in LOG; prints LOG's end and fails after the last try.
tries() {
	local n=$1 log=$2 try
	shift 2
	for ((try = 1; try <= n; try++)); do
		if "$@" >"$log" 2>&1; then
			return 0
		fi
		echo "sign.sh: try $try of $n failed: $*" >&2
		[ "$try" = "$n" ] || sleep "$delay"
	done
	tail -40 "$log" >&2
	return 1
}

if [ "$release" = 1 ]; then
	# Both bundles go up first, so Apple works on them side by side; then
	# each is waited for. A network error ends rcodesign's wait but not the
	# notarization at Apple, so the wait is tried again with the same
	# submission — never a second upload. notary-wait exits 0 whatever
	# Apple decided: the last "poll state" line says it.
	ids=()
	for arch in $archs; do
		log="$work/submit-$arch.log"
		tries 3 "$log" rcodesign notary-submit -C /dev/null --api-key-file "$notary" "$work/$arch/VaultSync.app" || exit 1
		id=$(sed -n 's/.*created submission ID: //p' "$log" | tail -1)
		if [ -z "$id" ]; then
			cat "$log" >&2
			echo "sign.sh: no submission ID for VaultSync_darwin_$arch" >&2
			exit 1
		fi
		echo "VaultSync_darwin_$arch: notary submission $id"
		ids+=("$id")
	done
	set -- "${ids[@]}"
	for arch in $archs; do
		id=$1
		shift
		log="$work/wait-$arch.log"
		tries 5 "$log" rcodesign notary-wait -C /dev/null --api-key-file "$notary" --max-wait-seconds 2400 "$id" || exit 1
		status=$(sed -n 's/.*poll state after [0-9]*s: //p' "$log" | tail -1)
		echo "VaultSync_darwin_$arch: notary submission $id: $status"
		grep 'notary log>' "$log" || true
		if [ "$status" != Accepted ]; then
			echo "sign.sh: Apple did not accept VaultSync_darwin_$arch ($status, submission $id)" >&2
			exit 1
		fi
		# Stapling looks the ticket up once; right after "Accepted" that
		# lookup can still miss, so it is tried again.
		tries 5 "$work/staple-$arch.log" rcodesign staple -C /dev/null "$work/$arch/VaultSync.app" || exit 1
	done
fi

for arch in $archs; do
	rm -f "$out/VaultSync_darwin_$arch.zip"
	(cd "$work/$arch" && zip -qry -X "$out/VaultSync_darwin_$arch.zip" VaultSync.app)
done
ls -l "$out"
