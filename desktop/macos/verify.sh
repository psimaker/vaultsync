#!/bin/bash
# verify.sh DIR [TEAM] — checks on a Mac, with Apple's own tools, what
# sign.sh put in DIR (decision 052): every binary has a strict signature,
# the hardened runtime, no entitlements and its identifier
# (eu.vaultsync.agent for the agent, alone and in the bundle;
# eu.vaultsync.desktop for the bundle); the bare agent and the bundle's
# share one code directory (the bundle's notarization covers both); the
# modes survived; the binaries link only the system's libraries — the
# hardened runtime's library validation refuses anything else — and on an
# Apple silicon Mac the agent and the menu-bar app of both architectures
# start (the Intel ones under Rosetta, required on a tag).
#
# With TEAM (a release): every binary is signed with a Developer ID of that
# team and carries exactly the designated requirement a granted permission
# is tied to — a certificate from another team would pass every other
# check, and reset every user's grants; the bare agents' ticket is found
# online, the bundles' ticket is stapled, and Gatekeeper accepts the bundles
# as notarized. The online lookups are tried again: right after
# notarization they can still miss.
set -euo pipefail

if [ $# -lt 1 ] || [ $# -gt 2 ]; then
	echo "usage: verify.sh DIR [TEAM]" >&2
	exit 2
fi
CDPATH='' cd -- "$1"
team=${2:-}
archs="arm64 amd64"

fail() {
	echo "verify.sh: $*" >&2
	exit 1
}

check() { # path identifier
	codesign --verify --strict --verbose=2 "$1"
	local info
	info=$(codesign -dvv "$1" 2>&1)
	grep -qxF "Identifier=$2" <<<"$info" || fail "$1 is not signed as $2"
	grep -Eq '^CodeDirectory .*flags=0x[0-9a-f]+\([^)]*runtime' <<<"$info" || fail "$1 lacks the hardened runtime"
	[ -z "$(codesign -d --entitlements - --xml "$1" 2>/dev/null)" ] || fail "$1 carries entitlements"
	[ -n "$team" ] || return 0
	grep -q '^Authority=Developer ID Application: ' <<<"$info" || fail "$1 is not signed with a Developer ID"
	[ "$(sed -n 's/^TeamIdentifier=//p' <<<"$info")" = "$team" ] || fail "$1 is not signed by team $team"
	local want have
	want="designated => identifier \"$2\" and anchor apple generic and certificate 1[field.1.2.840.113635.100.6.2.6] /* exists */ and certificate leaf[field.1.2.840.113635.100.6.1.13] /* exists */ and certificate leaf[subject.OU] = $team"
	have=$(codesign -d -r- "$1" 2>&1 | grep '^designated => ' || true)
	# codesign prints the team quoted only where the requirement language
	# needs it (a team starting with a digit); both spell the same rule.
	if [ "$have" != "$want" ] && [ "$have" != "${want% = "$team"} = \"$team\"" ]; then
		fail "$1 has the designated requirement '$have'"
	fi
}

cdhash() {
	local h
	h=$(codesign -dvvv "$1" 2>&1 | sed -n 's/^CDHash=//p')
	[ -n "$h" ] || fail "no code directory hash for $1"
	printf '%s\n' "$h"
}

retry() { # what command...
	local what=$1 try
	shift
	for try in 1 2 3 4 5; do
		if "$@"; then
			return 0
		fi
		[ "$try" = 5 ] || sleep 20
	done
	fail "$what"
}

for arch in $archs; do
	agent="vaultsync_darwin_$arch"
	check "$agent" eu.vaultsync.agent
	rm -rf "x-$arch"
	mkdir "x-$arch"
	ditto -x -k "VaultSync_darwin_$arch.zip" "x-$arch"
	app="x-$arch/VaultSync.app"
	codesign --verify --strict --deep --verbose=2 "$app"
	check "$app" eu.vaultsync.desktop
	check "$app/Contents/MacOS/vaultsync" eu.vaultsync.agent
	for bin in vaultsync vaultsync-tray; do
		[ -x "$app/Contents/MacOS/$bin" ] || fail "$app/Contents/MacOS/$bin is not executable"
		libs=$(otool -L "$app/Contents/MacOS/$bin") || fail "otool cannot read $app/Contents/MacOS/$bin"
		others=$(tail -n +2 <<<"$libs" | awk '{ print $1 }' | grep -Ev '^(/usr/lib/|/System/Library/)' || true)
		[ -z "$others" ] || fail "$app/Contents/MacOS/$bin links $others"
	done
	bare=$(cdhash "$agent")
	bundled=$(cdhash "$app/Contents/MacOS/vaultsync")
	[ "$bare" = "$bundled" ] ||
		fail "the bare and the bundled agent of $arch differ; the bundle's notarization would not cover the bare one"
	if [ -n "$team" ]; then
		retry "no notarization ticket for $agent" codesign --verify --check-notarization -R=notarized "$agent"
		retry "no ticket stapled to VaultSync_darwin_$arch" xcrun stapler validate "$app"
		verdict=$(spctl --assess --type execute --verbose=4 "$app" 2>&1) || {
			echo "$verdict"
			fail "Gatekeeper rejects VaultSync_darwin_$arch"
		}
		echo "$verdict"
		grep -q 'source=Notarized Developer ID' <<<"$verdict" || fail "Gatekeeper does not see VaultSync_darwin_$arch as notarized"
	fi
done

if [ "$(uname -m)" = arm64 ]; then
	for arch in $archs; do
		run=()
		if [ "$arch" = amd64 ]; then
			if ! arch -x86_64 /usr/bin/true 2>/dev/null; then
				[ -z "$team" ] || fail "Rosetta is not installed: the Intel build cannot be started before the release"
				echo "verify.sh: no Rosetta here; the Intel build is not started"
				continue
			fi
			run=(arch -x86_64)
		fi
		chmod 755 "vaultsync_darwin_$arch"
		${run[@]+"${run[@]}"} "./vaultsync_darwin_$arch" version
		${run[@]+"${run[@]}"} "x-$arch/VaultSync.app/Contents/MacOS/vaultsync" version
		${run[@]+"${run[@]}"} "x-$arch/VaultSync.app/Contents/MacOS/vaultsync-tray" --version
	done
fi
echo "verify.sh: all checks passed${team:+ (team $team)}"
