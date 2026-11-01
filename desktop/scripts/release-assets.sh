#!/bin/sh
# release-assets.sh PLAIN SIGNED DIST — puts a desktop release's assets
# together (decision 052): every file the build made from PLAIN except the
# macOS ones, which come only as signed from SIGNED; the set checked
# against the expected list; one SHA256SUMS over all of them. The release
# job runs it on a tag; every pull request runs it over its ad hoc signed
# macOS binaries, so a change to what the build makes fails on the pull
# request, not at the tag. An unsigned macOS build never reaches DIST.
set -eu

[ $# -eq 3 ] || {
	echo "usage: release-assets.sh PLAIN SIGNED DIST" >&2
	exit 2
}
plain=$1
signed=$2
dist=$3
mkdir "$dist"

for f in "$plain"/*; do
	[ -f "$f" ] || continue
	case ${f##*/} in
		*_darwin_*) ;;
		*) cp "$f" "$dist/" ;;
	esac
done
for f in "$signed"/vaultsync_darwin_* "$signed"/VaultSync_darwin_*.zip; do
	[ -f "$f" ] || {
		echo "release-assets.sh: no signed macOS asset matches $f" >&2
		exit 1
	}
	cp "$f" "$dist/"
done

have=$(for f in "$dist"/*; do printf '%s\n' "${f##*/}"; done | LC_ALL=C sort | tr '\n' ' ')
want="VaultSync_darwin_amd64.zip VaultSync_darwin_arm64.zip vaultsync-tray_linux_amd64 vaultsync-tray_linux_arm64 vaultsync_darwin_amd64 vaultsync_darwin_arm64 vaultsync_linux_amd64 vaultsync_linux_arm64 "
if [ "$have" != "$want" ]; then
	echo "release-assets.sh: the assets are: $have" >&2
	echo "release-assets.sh: expected:       $want" >&2
	exit 1
fi

# setup.sh takes exactly the one line whose name is the asset it downloads
# (an awk filter), so the other lines are passed over.
sums=$(mktemp)
if command -v sha256sum >/dev/null 2>&1; then
	(cd "$dist" && sha256sum -- *) >"$sums"
else
	(cd "$dist" && shasum -a 256 -- *) >"$sums"
fi
mv "$sums" "$dist/SHA256SUMS"
cat "$dist/SHA256SUMS"
