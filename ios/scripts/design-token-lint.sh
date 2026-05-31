#!/usr/bin/env bash
#
# Design-token lint guardrail.
#
# Fails if SwiftUI views use raw system status colors (.red/.green/.orange/.blue)
# or hardcoded color literals (Color(red:…), Color(.systemGreen), …) instead of
# the semantic tokens defined in Theme.swift. This keeps the redesign's single
# source of truth — one accent + the .statusSuccess/.statusAttention/.statusError/
# .statusInfo/.statusInactive palette — from eroding back into per-file literals.
#
# Allowed: .white/.black/.clear/.primary/.secondary/.tertiary and the vault* /
# status* tokens. Theme.swift itself is excluded (it defines the tokens).
#
# Usage: ios/scripts/design-token-lint.sh   (exit 0 = clean, 1 = violations)

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIRS=("$ROOT/VaultSync/Views" "$ROOT/VaultSync/App" "$ROOT/VaultSyncWidget")

# Raw status colors inside a color modifier, bare Color.<status>, hardcoded RGB,
# UIColor.system<Status> bridges, AND return-position / ternary / switch-expression
# status colors (e.g. `return .red`, `cond ? .green : .red`, `case .x: .orange`) —
# the return-position blind spot that previously let helper functions slip through.
PATTERN='(foregroundStyle|foregroundColor|tint|fill|background)\(\s*\.(red|green|orange|blue)\b|Color\.(red|green|orange|blue)\b|Color\(red:|Color\(uiColor: ?\.system(Red|Green|Orange|Blue)|Color\(\.system(Red|Green|Orange|Blue)|return +\.(red|green|orange|blue)\b|[:?] +\.(red|green|orange|blue)\b'

hits="$(grep -rnE "$PATTERN" "${DIRS[@]}" --include='*.swift' 2>/dev/null || true)"

if [ -n "$hits" ]; then
  echo "❌ Design-token lint failed — use Theme.swift tokens instead of raw colors:"
  echo "   (.statusSuccess / .statusAttention / .statusError / .statusInfo / .statusInactive / .vaultAccent)"
  echo ""
  echo "$hits"
  exit 1
fi

echo "✅ Design-token lint passed — no raw status/hardcoded colors in views."
