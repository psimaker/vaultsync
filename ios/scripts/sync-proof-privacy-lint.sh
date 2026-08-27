#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CHECK_CORE="$ROOT/ios/VaultSync/Services/RelaySyncPathCheck.swift"
RELAY_SERVICE="$ROOT/ios/VaultSync/Services/RelayService.swift"
PRIVACY_POLICY="$ROOT/PRIVACY.md"
NOTIFY_LOG_SOURCES=(
  "$ROOT/notify/main.go"
  "$ROOT/notify/doctor.go"
  "$ROOT/notify/relay.go"
  "$ROOT/notify/syncthing.go"
)

if rg -n \
  'UserDefaults|KeychainService|FileManager|Logger\(|RelayService|SubscriptionManager|RelayProvisionStatusStore|RelayTriggerStore|Bridge(Add|Remove|Set|Rescan)' \
  "$CHECK_CORE"; then
  echo "❌ Sync-path check gained a persistence, relay, logging, or write dependency."
  exit 1
fi

if rg -n -U \
  '(?s)String(?:\.init)?\s*\(\s*(data|bytes|decoding)\s*:' \
  "$RELAY_SERVICE" ||
  rg -n \
    '(responseBody|requestBody|httpBody).*(logger|recordLastError|UserDefaults)|(logger|recordLastError|UserDefaults).*(responseBody|requestBody|httpBody)' \
    "$RELAY_SERVICE"; then
  echo "❌ Relay diagnostics can consume or persist a raw response/request body."
  exit 1
fi

if rg -n \
  'logger\..*(\.path|absoluteString|lastPathComponent|privacy: \.private|deviceID\b|deviceIDs([^.]|$)|folderID\b|folderIDs([^.]|$)|folderName|filePath|signedTransaction|transactionID|apnsToken|jwsRepresentation|\\\([[:space:]]*([[:alnum:]_]+\.)?(error|err)\b|\\\((message|detail)\)|\.localizedDescription)' \
  "$ROOT/ios/VaultSync" --glob '*.swift'; then
  echo "❌ Application logs contain a forbidden identifier, path, payload, or raw error."
  exit 1
fi

rg -q 'slog\.SetDefault\(.*io\.Discard' "$ROOT/go/bridge/syncthing.go"
rg -q 'log\.SetOutput\(io\.Discard' "$ROOT/go/bridge/syncthing.go"

if rg -n -U \
  'slog\.(Debug|Info|Warn|Error)\([^\n]*(\n[^\n]*){0,12}"(device_id|device|folder|marker|syncthing_url|relay_url|source|error|warning)"[[:space:]]*,' \
  "${NOTIFY_LOG_SOURCES[@]}"; then
  echo "❌ Notify logs contain a forbidden identifier, path, marker, or raw error field."
  exit 1
fi

if rg -n \
  'io\.ReadAll\([^\n]*resp\.Body|readBodySnippet|HTTPStatusError[^\n]*(URL|Body)' \
  "$ROOT/notify/relay.go" "$ROOT/notify/syncthing.go" "$ROOT/notify/errors.go"; then
  echo "❌ Notify dependency errors can retain a raw endpoint or response body."
  exit 1
fi

# #149: Reject the known stale Controlled Diagnostics release-status wording.
if rg -n -i -U \
  -e 'currently[[:space:]]+released[[:space:]]+app[[:space:]]+does[[:space:]]+not[[:space:]]+call[[:space:]]+it' \
  -e 'unreleased[[:space:]]+app[[:space:]]+source' \
  -e 'not[[:space:]]+yet[[:space:]]+the[[:space:]]+publicly[[:space:]]+released[[:space:]]+app' \
  -e 'Explicit[[:space:]]+Foreground[[:space:]]+Upload[[:space:]]+Check[[:space:]]*\([[:space:]]*Unreleased[[:space:]]+Source[[:space:]]*\)' \
  -- "$PRIVACY_POLICY"; then
  echo "❌ #149: PRIVACY.md still describes shipped Controlled Diagnostics behavior as unreleased."
  exit 1
else
  privacy_lint_status=$?
  if [ "$privacy_lint_status" -ne 1 ]; then
    echo "❌ #149: Unable to inspect PRIVACY.md for stale Controlled Diagnostics release-status wording." >&2
    exit "$privacy_lint_status"
  fi
fi

echo "✅ Sync-proof privacy lint passed — passive core, structured diagnostics, sanitized logs."
