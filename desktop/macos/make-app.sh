#!/bin/sh
# make-app.sh VERSION AGENT TRAY OUT — assembles OUT/VaultSync.app from the
# two macOS binaries of one architecture (decision 052): the menu-bar app is
# the bundle's executable, the agent sits beside it in Contents/MacOS.
# Nothing is signed here — desktop-release.yml signs and notarizes the bundle
# on a tag. VERSION is the release version (0.2.0, 0.2.0-rc.1) or anything
# else for a development build, which gets 0.0.0 in Info.plist (the bundle
# version takes three numbers only).
set -eu

[ $# -eq 4 ] || {
	echo "usage: make-app.sh VERSION AGENT TRAY OUT" >&2
	exit 2
}
version=$1
agent=$2
tray=$3
out=$4
here=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)

short=${version%%-*}
printf '%s\n' "$short" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || short=0.0.0

for f in "$agent" "$tray"; do
	[ -f "$f" ] || {
		echo "make-app.sh: $f is not a file" >&2
		exit 1
	}
done

app="$out/VaultSync.app"
[ ! -e "$app" ] || {
	echo "make-app.sh: $app already exists" >&2
	exit 1
}
mkdir -p "$app/Contents/MacOS"
sed "s/@VERSION@/$short/g" "$here/Info.plist" >"$app/Contents/Info.plist"
printf 'APPL????' >"$app/Contents/PkgInfo"
cp "$tray" "$app/Contents/MacOS/vaultsync-tray"
cp "$agent" "$app/Contents/MacOS/vaultsync"
chmod 755 "$app/Contents/MacOS/vaultsync-tray" "$app/Contents/MacOS/vaultsync"
if command -v plutil >/dev/null 2>&1; then
	plutil -lint "$app/Contents/Info.plist" >/dev/null
fi
echo "$app ($short)"
