# Architecture

## Overview

VaultSync embeds Syncthing's Go reference implementation as an iOS library via gomobile. This avoids reimplementing the Syncthing protocol in Swift and guarantees protocol compatibility.

```
┌─────────────────────────────────┐
│         SwiftUI Frontend        │
│   (iOS-native UI, Swift 6)      │
├─────────────────────────────────┤
│       Swift ↔ Go Bridge         │
│  (thin API layer via gomobile   │
│   → exported as .xcframework)   │
├─────────────────────────────────┤
│      syncthing/lib (Go)         │
│  (protocol, discovery, sync)    │
└─────────────────────────────────┘
         ↕ filesystem ↕
┌─────────────────────────────────┐
│    Obsidian Vault (direct)      │
│  (access to Obsidian sandbox)   │
└─────────────────────────────────┘
```

## Go Bridge (`go/bridge/`)

Minimal API exported via gomobile. Only primitive types + `string` + `[]byte` cross the bridge — complex data is JSON-serialized.

Key exports:
- **Lifecycle:** `StartSyncthing`, `StopSyncthing`, `IsRunning`
- **Identity:** `DeviceID`, `GenerateQRCode`
- **Devices:** `AddDevice`, `RemoveDevice`, `ListDevices`
- **Folders:** `AddFolder`, `RemoveFolder`, `ListFolders`, `RescanFolder`
- **Sync:** `SyncStatus`, `GetPendingFolders`, `AcceptPendingFolder`
- **Conflicts:** `ListConflicts`, `ResolveConflict`, `GetConflictDiff`
- **Events:** `GetRecentChanges`, `GetLatestEvent`

## Sync Strategy

- **Foreground:** Syncthing runs unrestricted. Immediate, continuous sync.
- **Background:** `BGAppRefreshTask` (~30s) + `BGContinuedProcessingTask` (iOS 26+ when available, longer runtime for user-initiated tasks).
- **Push sync (Cloud Relay):** Optional. Near-realtime `server -> iPhone` wake-ups via APNs silent push notifications. See [relay-spec.md](relay-spec.md).

## Directional Behavior

VaultSync is intentionally asymmetric in the background:

- **Server -> iPhone**
  - `vaultsync-notify` watches Syncthing for real outgoing-change markers
  - Cloud Relay sends a silent push
  - VaultSync wakes and pulls through Syncthing
- **iPhone -> Server**
  - iOS does not guarantee timely background execution for local file changes originating in Obsidian
  - the reliable path is to open VaultSync, let embedded Syncthing run in foreground, and push changes normally
  - background refresh may help opportunistically, but it is not a contractual part of the product

VaultSync therefore treats Cloud Relay as a `server -> iPhone` acceleration path, not a guarantee of symmetric real-time background sync in both directions.

## Cloud Relay (`notify/`)

Optional Docker container for push-based sync notifications. The relay bridges APNs push notifications with Syncthing event monitoring on the user's homeserver, enabling instant sync triggers without polling.

See [relay-spec.md](relay-spec.md) for the protocol specification.

## Operator Guidance

- End-user issue handling: [troubleshooting.md](troubleshooting.md)

## iOS App Structure

```
ios/VaultSync/
├── App/          — VaultSyncApp entry point, AppDelegate (push + background tasks)
├── Views/        — SwiftUI views (ContentView, Onboarding, Settings, Conflicts, QR)
└── Services/     — SyncBridge, SyncthingManager, BackgroundSync, Relay, Keychain, Subscriptions
```
