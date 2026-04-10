#!/usr/bin/env bash
# Create local patched copies of dependencies and apply patches.
# The go.mod `replace` directives point to these directories.
#
# Usage: ./vendor-patch.sh
#        make patch

set -euo pipefail
cd "$(dirname "$0")"

# Temporarily drop replace directives so go mod download can fetch
# the original modules (they point to dirs that don't exist yet).
echo "==> Temporarily removing replace directives..."
cp go.mod go.mod.bak
sed -i '' '/^replace /d' go.mod

echo "==> Downloading Go modules..."
go mod download

echo "==> Restoring replace directives..."
mv go.mod.bak go.mod

GOMODCACHE=$(go env GOMODCACHE)

# --- Syncthing ---
ST_VERSION=$(grep 'syncthing/syncthing' go.sum | head -1 | awk '{print $2}' | sed 's|/go.mod||')
ST_CACHE="$GOMODCACHE/github.com/syncthing/syncthing@$ST_VERSION"
ST_LOCAL="_syncthing_patched"

echo "==> Creating local patched copy of syncthing..."
rm -rf "$ST_LOCAL"
cp -a "$ST_CACHE" "$ST_LOCAL"
chmod -R u+w "$ST_LOCAL"

echo "==> Applying syncthing patches..."
for patch in patches/syncthing/*.patch; do
    [ -f "$patch" ] || continue
    echo "    Applying $(basename "$patch")"
    patch -d "$ST_LOCAL" -p1 < "$patch"
done

# --- go-stun ---
STUN_VERSION=$(grep 'ccding/go-stun' go.sum | head -1 | awk '{print $2}' | sed 's|/go.mod||')
STUN_CACHE="$GOMODCACHE/github.com/ccding/go-stun@$STUN_VERSION"
STUN_LOCAL="_go-stun_patched"

echo "==> Creating local patched copy of go-stun..."
rm -rf "$STUN_LOCAL"
cp -a "$STUN_CACHE" "$STUN_LOCAL"
chmod -R u+w "$STUN_LOCAL"

echo "==> Applying go-stun patches..."
for patch in patches/go-stun/*.patch; do
    [ -f "$patch" ] || continue
    echo "    Applying $(basename "$patch")"
    patch -d "$STUN_LOCAL" -p1 < "$patch"
done

echo "==> Done. Build with 'make xcframework'."
