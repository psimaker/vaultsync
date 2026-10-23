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
# Developer ID signs with --for-notarization, Apple notarizes everything and
# the bundles get their ticket stapled. Without any of them — a pull
# request — the same commands sign ad hoc, with the hardened runtime asked
# for per binary, as --for-notarization sets it; nothing goes to Apple.
#
# The agent signs as eu.vaultsync.agent alone (what setup.sh installs) and
# inside the bundle: identifier and team make the designated requirement
# macOS ties a granted permission to, so the grant carries over to every
# later release (#222). The bundle's executable takes CFBundleIdentifier.
#
# rcodesign must be on PATH. Never export a variable named RCODESIGN_*:
# rcodesign reads each one as configuration and refuses unknown keys.
set -euo pipefail

[ $# -eq 3 ] || {
	echo "usage: sign.sh PLAIN MACOS OUT" >&2
	exit 2
}
plain=$1
macos=$2
mkdir -p "$3"
out=$(CDPATH='' cd -- "$3" && pwd)
archs="arm64 amd64"

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
	rcodesign sign "${mode[@]}" --binary-identifier eu.vaultsync.agent "$out/vaultsync_darwin_$arch"
	mkdir "$work/$arch"
	tar -C "$work/$arch" -xf "$macos/VaultSync_darwin_$arch.app.tar"
	if [ -n "$(find "$work/$arch" -name '._*' -print -quit)" ]; then
		echo "sign.sh: VaultSync_darwin_$arch.app.tar holds ._ files" >&2
		exit 1
	fi
	rcodesign sign "${mode[@]}" ${inner[@]+"${inner[@]}"} \
		--binary-identifier Contents/MacOS/vaultsync:eu.vaultsync.agent "$work/$arch/VaultSync.app"
done

if [ "$release" = 1 ]; then
	# A Mach-O cannot hold a ticket: the bare agents go up in one zip, and
	# Gatekeeper looks their ticket up online. All uploads wait side by
	# side, so Apple's queue costs its time once, not once per upload.
	agents=()
	for arch in $archs; do agents+=("vaultsync_darwin_$arch"); done
	(cd "$out" && zip -q -X "$work/agents.zip" "${agents[@]}")
	targets=("$work/agents.zip")
	for arch in $archs; do targets+=("$work/$arch/VaultSync.app"); done
	pids=()
	logs=()
	for target in "${targets[@]}"; do
		log="$work/notary-${#logs[@]}.log"
		rcodesign notary-submit --api-key-file "$notary" --wait --max-wait-seconds 2400 "$target" >"$log" 2>&1 &
		pids+=("$!")
		logs+=("$log")
	done
	failed=0
	for i in "${!pids[@]}"; do
		if ! wait "${pids[$i]}"; then
			failed=1
			echo "sign.sh: notarization failed:" >&2
			tail -40 "${logs[$i]}" >&2
		fi
	done
	[ "$failed" = 0 ] || exit 1
	# Stapling looks the ticket up once; right after "Accepted" that lookup
	# can still miss, so it is tried again.
	for arch in $archs; do
		for try in 1 2 3 4 5; do
			if rcodesign staple "$work/$arch/VaultSync.app"; then
				break
			fi
			if [ "$try" = 5 ]; then
				echo "sign.sh: could not staple VaultSync_darwin_$arch" >&2
				exit 1
			fi
			sleep 30
		done
	done
fi

for arch in $archs; do
	(cd "$work/$arch" && zip -qry -X "$out/VaultSync_darwin_$arch.zip" VaultSync.app)
done
ls -l "$out"
