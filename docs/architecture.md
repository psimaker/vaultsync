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

Key exports (read-only accessors return JSON and are named `Get…JSON`):
- **Lifecycle:** `StartSyncthing`, `StopSyncthing`, `IsRunning`
- **Identity:** `DeviceID`
- **Devices:** `AddDevice`, `RemoveDevice`, `RenameDevice`, `GetDevicesJSON`
- **Folders:** `AddFolder`, `RemoveFolder`, `RescanFolder`, `GetFoldersJSON`, `ShareFolderWithDevice`, `UnshareFolderFromDevice`
- **Status & config:** `GetFolderStatusJSON`, `GetConnectionsJSON`, `GetConfigJSON`, `SetDiscoveryEnabled`
- **Pending shares:** `GetPendingFoldersJSON`, `AcceptPendingFolder`
- **Conflicts:** `GetConflictFilesJSON`, `ResolveConflict`, `KeepBothConflict`, `ReadFileContent`, `RemoveConflictFilesForOriginal`
- **Filters:** `GetFolderIgnores`, `SetFolderIgnores`, `ScanFolderForKnownPatterns`
- **Events:** `GetEventsSince`

QR codes (scan-only) and conflict diffs are produced on the iOS side (`QRScannerView`, `ConflictDiffView`/`LineDiffView`), not via the bridge.

## Sync Strategy

- **Foreground:** Syncthing runs unrestricted. Immediate, continuous sync.
- **Background:** `BGAppRefreshTask` (requested ~15 min out; iOS decides the actual timing) + `BGContinuedProcessingTask` (iOS 26+ when available, longer runtime for user-initiated tasks). A ~30s grace window after backgrounding lets in-flight sync work finish.
- **Push sync (Cloud Relay):** Optional. Near-realtime `server → iPhone` wake-ups via APNs silent push notifications. See [relay-spec.md](relay-spec.md).

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

Cloud Relay is a `server → iPhone` acceleration path, not a guarantee of symmetric real-time background sync.

## Cloud Relay (`notify/`)

Optional Docker sidecar that watches Syncthing on the homeserver and sends APNs silent-push wake-ups to the iPhone. See [relay-spec.md](relay-spec.md) for the protocol; [troubleshooting.md](troubleshooting.md) for end-user issue handling.

## iOS App Structure

```
ios/VaultSync/
├── App/          — VaultSyncApp entry point, AppDelegate (push + background tasks)
├── Models/       — data types (SyncEventItem, IgnorePreset, RelayProvisionStatus, …)
├── ViewModels/   — view models (SetupChecklistViewModel)
├── Views/        — SwiftUI views (ContentView, Onboarding, Settings, RelayDiagnostics,
│                   SyncIssues, IgnorePatterns, Conflicts, QR scanner)
├── Services/     — SyncBridgeService, SyncthingManager, BackgroundSyncService, RelayService,
│                   KeychainService, SubscriptionManager, TipJarManager, BookmarkService,
│                   VaultManager, SyncHistoryStore
├── Resources/    — assets, theme, localization helpers
└── *.lproj/      — localizations (en, de, es, zh-Hans)
```
