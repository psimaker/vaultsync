#!/bin/sh
# install-rcodesign.sh DIR — puts the pinned rcodesign (Linux x86_64) into
# DIR after checking the download against its SHA-256 (decision 052). Pinned
# like govulncheck (#85): a new rcodesign must not change what is signed
# without a change here; bump both lines together.
set -eu

version=0.29.0
sha256=dbe85cedd8ee4217b64e9a0e4c2aef92ab8bcaaa41f20bde99781ff02e600002

[ $# -eq 1 ] || {
	echo "usage: install-rcodesign.sh DIR" >&2
	exit 2
}
dir=$1
name="apple-codesign-${version}-x86_64-unknown-linux-musl"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/$name.tar.gz" \
	"https://github.com/indygreg/apple-platform-rs/releases/download/apple-codesign%2F${version}/$name.tar.gz"
echo "$sha256  $tmp/$name.tar.gz" | sha256sum -c -
tar -xzf "$tmp/$name.tar.gz" -C "$tmp"
mkdir -p "$dir"
install -m 755 "$tmp/$name/rcodesign" "$dir/rcodesign"
"$dir/rcodesign" --version
