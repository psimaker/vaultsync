#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CHECK_CORE="$ROOT/ios/VaultSync/Services/RelaySyncPathCheck.swift"
RELAY_SERVICE="$ROOT/ios/VaultSync/Services/RelayService.swift"

if rg -n \
  'UserDefaults|KeychainService|FileManager|Logger\(|RelayService|SubscriptionManager|RelayProvisionStatusStore|RelayTriggerStore|Bridge(Add|Remove|Set|Rescan)' \
  "$CHECK_CORE"; then
  echo "❌ Sync-path check gained a persistence, relay, logging, or write dependency."
  exit 1
fi

if rg -n \
  'String\(data:.*encoding:.*utf8|responseBody|httpBody.*(logger|recordLastError)' \
  "$RELAY_SERVICE"; then
  echo "❌ Relay diagnostics can consume or persist a raw response/request body."
  exit 1
fi

if rg -n \
  'logger\..*(\.path|absoluteString|lastPathComponent|privacy: \.private|deviceID[^s]|folderID|folderName|filePath|signedTransaction|transactionID|apnsToken|jwsRepresentation|\\\(error\)|\\\(err\)|\\\(message\)|\\\(detail\))' \
  "$ROOT/ios/VaultSync" --glob '*.swift'; then
  echo "❌ Application logs contain a forbidden identifier, path, payload, or raw error."
  exit 1
fi

rg -q 'slog\.SetDefault\(.*io\.Discard' "$ROOT/go/bridge/syncthing.go"
rg -q 'log\.SetOutput\(io\.Discard' "$ROOT/go/bridge/syncthing.go"

echo "✅ Sync-proof privacy lint passed — passive core, structured diagnostics, sanitized logs."
