#!/usr/bin/env bash
#
# Localizable.strings key-parity guardrail (#83).
#
# A key missing from one .lproj silently falls back to English at runtime, and
# a duplicate key silently shadows an earlier translation — neither is visible
# in review. This asserts, for all four Localizable.strings files:
#   1. No duplicate keys inside any single file.
#   2. All four files carry exactly the same key SET (not just the same count —
#      an added key in one file plus a dropped key in another cancels out in a
#      bare count comparison).
#
# Keys are extracted with an escape-aware pattern (backslash escapes like \" are
# part of the key), so keys containing escaped quotes do not false-positive the
# way the naive `grep -o '^"[^"]*"'` check documented in CLAUDE.md does.
#
# Usage: ios/scripts/strings-key-parity.sh   (exit 0 = parity, 1 = violations)

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LOCALES=(en de es zh-Hans)

# Full key including escape sequences: "…" up to the first unescaped quote.
extract_keys() {
  sed -n 's/^"\(\(\\.\|[^"\\]\)*\)" = .*/\1/p' "$1"
}

status=0
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

for locale in "${LOCALES[@]}"; do
  file="$ROOT/VaultSync/$locale.lproj/Localizable.strings"
  [ -f "$file" ] || { echo "❌ Missing $file"; exit 1; }

  extract_keys "$file" | sort >"$tmpdir/$locale.keys"

  dupes="$(uniq -d <"$tmpdir/$locale.keys")"
  if [ -n "$dupes" ]; then
    echo "❌ Duplicate keys in $locale.lproj/Localizable.strings:"
    echo "$dupes"
    status=1
  fi
done

for locale in de es zh-Hans; do
  if ! diff -u "$tmpdir/en.keys" "$tmpdir/$locale.keys" >"$tmpdir/diff.$locale"; then
    echo "❌ Key set of $locale.lproj differs from en.lproj (missing keys fall back to English silently):"
    grep -E '^[+-]"?' "$tmpdir/diff.$locale" | grep -vE '^(\+\+\+|---)' | head -40
    status=1
  fi
done

if [ "$status" -eq 0 ]; then
  count="$(wc -l <"$tmpdir/en.keys")"
  echo "✅ Strings key parity passed — $count keys, identical across en/de/es/zh-Hans, no duplicates."
fi
exit "$status"
