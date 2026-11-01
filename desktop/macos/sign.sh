#!/bin/bash
# sign.sh PLAIN MACOS OUT — signs VaultSync for Mac (decision 052): the bare
# agents PLAIN/vaultsync_darwin_<arch> and the bundles in
# MACOS/VaultSync_darwin_<arch>.app.tar (make-app.sh's output as the macOS
# build job packs it) become OUT/vaultsync_darwin_<arch> and
# OUT/VaultSync_darwin_<arch>.zip.
#
# A release (desktop-v* tag) sets VAULTSYNC_SIGN_P12 and
# VAULTSYNC_SIGN_P12_PASSWORD_FILE (the Developer ID), VAULTSYNC_NOTARY_P8,
# VAULTSYNC_NOTARY_KEY_ID and VAULTSYNC_NOTARY_ISSUER (the App Store Connect
# key) and VAULTSYNC_SIGN_TEAM. Before anything is signed the certificate
# must be a Developer ID Application certificate of that team that has not
# expired; then the Developer ID signs with --for-notarization, Apple
# notarizes the bundles and their ticket is stapled. Without any of them —
# a pull request — the same commands sign ad hoc, with the hardened runtime
# asked for per binary, as --for-notarization sets it; nothing goes to Apple.
#
# The agent signs as eu.vaultsync.agent alone (what setup.sh installs) and
# inside the bundle: identifier and team make the designated requirement
# macOS ties a granted permission to, so the grant carries over to later
# releases (#222). Both copies are the same bytes signed the same way, so
# they share one code directory, and the bundle's notarization covers the
# bare agent too (verify.sh checks both). The bundle's executable takes
# CFBundleIdentifier.
#
# rcodesign (on PATH) runs through rc(): with -C /dev/null, so no
# rcodesign.toml changes what is signed, and with a cleared environment —
# rcodesign reads every variable named rcodesign_*, in any case, as
# configuration, RUST_LOG decides what it prints (which sign.sh reads), and
# the AWS SDK inside its notary upload would read ~/.aws. Every step that
# talks to Apple (the time-stamp server when signing, the notary service,
# the ticket lookup) is tried again; VAULTSYNC_SIGN_RETRY_DELAY is the pause
# between tries in seconds (default 30).
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
app="VaultSync for Mac.app"
delay=${VAULTSYNC_SIGN_RETRY_DELAY:-30}
case $delay in
	'' | *[!0-9]*)
		echo "sign.sh: VAULTSYNC_SIGN_RETRY_DELAY must be a whole number of seconds" >&2
		exit 2
		;;
esac

keep=(PATH="$PATH" HOME="${HOME:-/}" TMPDIR="${TMPDIR:-/tmp}" AWS_CONFIG_FILE=/dev/null AWS_SHARED_CREDENTIALS_FILE=/dev/null)
for v in HTTPS_PROXY https_proxy HTTP_PROXY http_proxy NO_PROXY no_proxy SSL_CERT_FILE SSL_CERT_DIR; do
	if [ -n "${!v:-}" ]; then
		keep+=("$v=${!v}")
	fi
done
rc() { # SUBCOMMAND ARGS...
	local sub=$1
	shift
	env -i "${keep[@]}" rcodesign "$sub" -C /dev/null "$@"
}

# tries N LOG COMMAND... runs COMMAND until it succeeds, at most N times,
# its output in LOG; says why each failed try failed, and fails after the
# last one.
tries() {
	local n=$1 log=$2 try
	shift 2
	for ((try = 1; try <= n; try++)); do
		if "$@" >"$log" 2>&1; then
			return 0
		fi
		echo "sign.sh: try $try of $n failed: $*" >&2
		if [ "$try" = "$n" ]; then
			tail -40 "$log" >&2
		else
			tail -5 "$log" >&2
			sleep "$delay"
		fi
	done
	return 1
}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

p12=${VAULTSYNC_SIGN_P12:-}
password=${VAULTSYNC_SIGN_P12_PASSWORD_FILE:-}
p8=${VAULTSYNC_NOTARY_P8:-}
key_id=${VAULTSYNC_NOTARY_KEY_ID:-}
issuer=${VAULTSYNC_NOTARY_ISSUER:-}
team=${VAULTSYNC_SIGN_TEAM:-}
if [ -n "$p12$password$p8$key_id$issuer$team" ]; then
	# A signed but unnotarized release must never be possible.
	if [ -z "$p12" ] || [ -z "$password" ] || [ -z "$p8" ] || [ -z "$key_id" ] || [ -z "$issuer" ] || [ -z "$team" ]; then
		echo "sign.sh: a release needs VAULTSYNC_SIGN_P12, VAULTSYNC_SIGN_P12_PASSWORD_FILE, VAULTSYNC_NOTARY_P8, VAULTSYNC_NOTARY_KEY_ID, VAULTSYNC_NOTARY_ISSUER and VAULTSYNC_SIGN_TEAM together" >&2
		exit 2
	fi
	release=1
	mode=(--for-notarization --p12-file "$p12" --p12-password-file "$password")
	inner=()
	# The wrong certificate would sign — another team's even notarizes —
	# and fail only at Apple or at the users': another team resets every
	# granted permission. It stops here, before anything is signed.
	if ! certificate=$(rc analyze-certificate --p12-file "$p12" --p12-password-file "$password" 2>&1); then
		tail -5 <<<"$certificate" >&2
		echo "sign.sh: the certificate cannot be read: a wrong password, or a .p12 not in the legacy format rcodesign reads (see desktop/README.md)" >&2
		exit 1
	fi
	first() { awk -v field="$1" 'index($0, field) == 1 { print $NF; exit }' <<<"$certificate"; }
	have=$(first "Team ID:")
	if [ "$have" != "$team" ]; then
		echo "sign.sh: the certificate is team '${have}', not $team" >&2
		exit 1
	fi
	profile=$(first "Guessed Certificate Profile:")
	if [ "$profile" != DeveloperIdApplication ]; then
		echo "sign.sh: the certificate is '${profile}', not a Developer ID Application certificate" >&2
		exit 1
	fi
	until=$(first "Not Valid After:")
	if [[ ! "$until" > "$(date -u +%Y-%m-%dT%H:%M:%S+00:00)" ]]; then
		echo "sign.sh: the certificate expired on ${until}" >&2
		exit 1
	fi
	notary="$work/api-key.json"
	rc encode-app-store-connect-api-key -o "$notary" "$issuer" "$key_id" "$p8" >"$work/encode.log" 2>&1 || {
		tail -5 "$work/encode.log" >&2
		echo "sign.sh: the App Store Connect key cannot be encoded" >&2
		exit 1
	}
else
	release=0
	mode=(--code-signature-flags runtime)
	inner=(--code-signature-flags Contents/MacOS/vaultsync:runtime)
fi

for arch in $archs; do
	cp "$plain/vaultsync_darwin_$arch" "$out/vaultsync_darwin_$arch"
	tries 3 "$work/sign.log" rc sign "${mode[@]}" --binary-identifier eu.vaultsync.agent "$out/vaultsync_darwin_$arch" || exit 1
	mkdir "$work/$arch"
	tar -C "$work/$arch" -xf "$macos/VaultSync_darwin_$arch.app.tar"
	if [ -n "$(find "$work/$arch" -name '._*' -print -quit)" ]; then
		echo "sign.sh: VaultSync_darwin_$arch.app.tar holds ._ files" >&2
		exit 1
	fi
	tries 3 "$work/sign.log" rc sign "${mode[@]}" ${inner[@]+"${inner[@]}"} \
		--binary-identifier Contents/MacOS/vaultsync:eu.vaultsync.agent "$work/$arch/$app" || exit 1
done

if [ "$release" = 1 ]; then
	# Both bundles go up first, so Apple works on them side by side; then
	# each is waited for. A network error ends rcodesign's wait but not the
	# notarization at Apple, so the wait is tried again with the same
	# submission. notary-wait exits 0 whatever Apple decided: its last
	# "poll state" line says it. Three waits of 30 minutes fit the signing
	# job's two hours with room for the second bundle and the staples.
	ids=()
	for arch in $archs; do
		log="$work/submit-$arch.log"
		tries 3 "$log" rc notary-submit --api-key-file "$notary" "$work/$arch/$app" || exit 1
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
		tries 3 "$log" rc notary-wait --api-key-file "$notary" --max-wait-seconds 1800 "$id" || exit 1
		status=$(sed -n 's/.*poll state after [0-9]*s: //p' "$log" | tail -1)
		echo "VaultSync_darwin_$arch: notary submission $id: $status"
		grep 'notary log>' "$log" || true
		if [ "$status" != Accepted ]; then
			echo "sign.sh: Apple did not accept VaultSync_darwin_$arch (${status:-no verdict}, submission $id)" >&2
			exit 1
		fi
		# Stapling looks the ticket up once; right after "Accepted" that
		# lookup can still miss, so it is tried again.
		tries 5 "$work/staple-$arch.log" rc staple "$work/$arch/$app" || exit 1
	done
fi

for arch in $archs; do
	rm -f "$out/VaultSync_darwin_$arch.zip"
	(cd "$work/$arch" && zip -qry -X "$out/VaultSync_darwin_$arch.zip" "$app")
done
ls -l "$out"
