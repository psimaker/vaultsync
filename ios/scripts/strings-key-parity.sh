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
# and the same for the widget extension's own four files, plus
#   3. Every key the widget looks up is in them (#360).
#
# Keys are extracted with an escape-aware pattern (backslash escapes like \" are
# part of the key), so keys containing escaped quotes do not false-positive the
# way the naive `grep -o '^"[^"]*"'` check documented in AGENTS.md does.
#
# Usage: ios/scripts/strings-key-parity.sh   (exit 0 = parity, 1 = violations)

set -euo pipefail

# Byte order under every host locale (#202). macOS's uniq compares lines by
# collation, and in its UTF-8 locales (de_DE and en_US alike) "…" carries no
# weight: "Starting" and "Starting…" were reported as one duplicated key on a
# Mac while CI passed. The app looks a key up by exact equality, so byte order
# is also the right meaning of "the same key". LC_ALL rather than LC_COLLATE,
# because an LC_ALL in the caller's environment would win over LC_COLLATE.
export LC_ALL=C

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LOCALES=(en de es zh-Hans)

# Full key including escape sequences: "…" up to the first unescaped quote.
# ERE via -E, not BRE \| alternation: \| is a GNU extension that BSD/macOS sed
# treats as a literal, which silently extracted ZERO keys there — parity would
# false-green on exactly the machine the Swift work happens on.
extract_keys() {
  sed -nE 's/^"((\\.|[^"\\])*)" = .*/\1/p' "$1"
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

# The widget's own strings (#360). The extension resolves its text from its
# own bundle, never from the app's: a key its files lack shows the English
# key in every language. Its four files must carry the same key set without
# duplicates, and every key it looks up: VaultSyncWidgetL10n.tr/fmt in its
# sources, and String(localized:) in Theme.swift, which it compiles too
# (project.yml, the VaultSyncWidget target's sources).
for locale in "${LOCALES[@]}"; do
  file="$ROOT/VaultSyncWidget/$locale.lproj/Localizable.strings"
  [ -f "$file" ] || { echo "❌ Missing $file"; exit 1; }

  extract_keys "$file" | sort >"$tmpdir/widget-$locale.keys"

  dupes="$(uniq -d <"$tmpdir/widget-$locale.keys")"
  if [ -n "$dupes" ]; then
    echo "❌ Duplicate keys in VaultSyncWidget/$locale.lproj/Localizable.strings:"
    echo "$dupes"
    status=1
  fi
done

for locale in de es zh-Hans; do
  if ! diff -u "$tmpdir/widget-en.keys" "$tmpdir/widget-$locale.keys" >"$tmpdir/widget-diff.$locale"; then
    echo "❌ Key set of VaultSyncWidget/$locale.lproj differs from its en.lproj (missing keys fall back to English silently):"
    grep -E '^[+-]"?' "$tmpdir/widget-diff.$locale" | grep -vE '^(\+\+\+|---)' | head -40
    status=1
  fi
done

{
  grep -ohE 'VaultSyncWidgetL10n\.(tr|fmt)\("(\\.|[^"\\])*"' "$ROOT"/VaultSyncWidget/*.swift \
    | sed -E 's/^VaultSyncWidgetL10n\.(tr|fmt)\("//; s/"$//'
  grep -ohE 'String\(localized: "(\\.|[^"\\])*"' "$ROOT/VaultSync/Resources/Theme.swift" \
    | sed -E 's/^String\(localized: "//; s/"$//'
} | sort -u >"$tmpdir/widget-used.keys"

if [ ! -s "$tmpdir/widget-used.keys" ]; then
  echo "❌ Found no key the widget looks up — the extraction no longer matches its sources."
  status=1
fi

missing="$(comm -23 "$tmpdir/widget-used.keys" "$tmpdir/widget-en.keys")"
if [ -n "$missing" ]; then
  echo "❌ Keys the widget looks up that its Localizable.strings lack (shown in English in every language):"
  echo "$missing"
  status=1
fi

if [ "$status" -eq 0 ]; then
  count="$(wc -l <"$tmpdir/en.keys")"
  widget_count="$(wc -l <"$tmpdir/widget-en.keys")"
  echo "✅ Strings key parity passed — $count keys, identical across en/de/es/zh-Hans, no duplicates; widget: $widget_count keys, identical, every key it looks up present."
fi
exit "$status"
