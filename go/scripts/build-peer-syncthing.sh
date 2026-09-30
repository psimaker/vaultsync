#!/usr/bin/env bash
# Build a loopback peer syncthing for the bridge suite (#185).
#
# The peer must behave like a stock server, so it is built from the pinned
# upstream module with only the build-enabling patches applied (001 relay nil
# guard, 002 noassets build, 003 dial timing) — never with VaultSync's
# behavioural patches (004 and up), which the bridge under test carries.
#
# Usage: go/scripts/build-peer-syncthing.sh <output-binary>
set -euo pipefail

# Resolve the output path against the caller's directory before anything cd's:
# the build runs inside a temp checkout that the EXIT trap deletes, so a
# relative path would put the binary there and report success over nothing.
out="${1:?usage: build-peer-syncthing.sh <output-binary>}"
case "$out" in
  /*) ;;
  *) out="$PWD/$out" ;;
esac
mkdir -p "$(dirname "$out")"

cd "$(dirname "$0")/.."
st_version=$(grep 'syncthing/syncthing' go.sum | head -1 | awk '{print $2}' | sed 's|/go.mod||')
src="$(go env GOMODCACHE)/github.com/syncthing/syncthing@${st_version}"
if [ ! -d "$src" ]; then
  echo "module cache lacks ${src}; run 'make patch' (go mod download) first" >&2
  exit 1
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp -a "$src" "$work/st"
chmod -R u+w "$work/st"
for p in patches/syncthing/001-*.patch patches/syncthing/002-*.patch patches/syncthing/003-*.patch; do
  patch -d "$work/st" -p1 < "$p" > /dev/null
done
(cd "$work/st" && CGO_ENABLED=0 go build -tags noassets -o "$out" ./cmd/syncthing)
echo "built stock-like peer syncthing ${st_version} at ${out}"
