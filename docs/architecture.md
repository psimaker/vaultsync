# Architecture

VaultSync embeds Syncthing's Go reference implementation as an iOS library via gomobile — no reimplementation of the protocol in Swift, and guaranteed wire compatibility.

```
┌─────────────────────────────────┐
│         SwiftUI Frontend        │   iOS-native UI, Swift 6
├─────────────────────────────────┤
│       Swift ↔ Go Bridge         │   thin API via gomobile
│                                 │   → exported as .xcframework
├─────────────────────────────────┤
│        syncthing/lib (Go)       │   protocol, discovery, sync
└─────────────────────────────────┘
              ↕ filesystem
┌─────────────────────────────────┐
│    Obsidian Vault (direct)      │   Obsidian's iOS sandbox
└─────────────────────────────────┘
```

## 🔄 Sync strategy

- **Foreground** — Syncthing runs unrestricted: immediate, continuous sync.
- **Background** — `BGAppRefreshTask` (requested ~15 min out; iOS decides the actual timing) + `BGProcessingTask` (overnight catch-up: multi-minute budget while charging with network) + `BGContinuedProcessingTask` (iOS 26+, longer runtime for user-initiated tasks). A ~30s grace window after backgrounding lets in-flight work finish.
- **Push (Cloud Relay)** — optional. Near-realtime `server → iPhone` wake-ups via APNs silent push. See [relay-spec.md](relay-spec.md).

VaultSync is intentionally **asymmetric**:

| Direction | Path |
|---|---|
| **Server → iPhone** | `vaultsync-notify` spots outgoing changes → Cloud Relay silent push → VaultSync wakes and pulls. |
| **iPhone → Server** | iOS doesn't guarantee timely background execution for local edits. The reliable path is to open VaultSync and let embedded Syncthing run in the foreground — a [Shortcuts automation](instant-upload.md) can do that automatically whenever you leave Obsidian. |

Cloud Relay is a `server → iPhone` *acceleration* path, not a guarantee of symmetric real-time background sync.

### Connection paths & iOS network privacy

How peers are reached, fastest first:

1. **Direct LAN (TCP/QUIC to a private address)** — requires the iOS *Local
   Network* permission (`NSLocalNetworkUsageDescription`, prompt shown on first
   LAN dial). Peer LAN addresses come from global discovery; without this
   permission iOS silently blocks the dial and every connection detours through
   a relay.
2. **Direct WAN (TCP/QUIC)** — depends on the peer's NAT/port-mapping.
3. **Syncthing relays** — slowest; the fallback when 1–2 fail.

Syncthing's *local* (multicast/broadcast) discovery additionally needs the
restricted `com.apple.developer.networking.multicast` entitlement, which Apple
grants only on request (Developer portal → entitlement request form). Until
that is granted, LAN peers are found via **global discovery + direct LAN dial**,
which is nearly as fast. After a connection succeeds, the bridge caches the
dialed address in the device's config (`addresscache.go`), so the next cold
start dials it immediately without waiting for discovery.

## 🌉 Go bridge (`go/bridge/`)

Minimal API exported via gomobile. Only primitives + `string` + `[]byte` cross the bridge; complex data is JSON-serialized (read accessors are named `Get…JSON`). QR scanning and conflict diffs are produced on the iOS side, not via the bridge.

<details>
<summary>Key exports</summary>

- **Lifecycle:** `StartSyncthing`, `StopSyncthing`, `IsRunning`
- **Identity:** `DeviceID`
- **Devices:** `AddDevice`, `RemoveDevice`, `RenameDevice`, `GetDevicesJSON`
- **Folders:** `AddFolder`, `RemoveFolder`, `RescanFolder`, `GetFoldersJSON`, `ShareFolderWithDevice`, `UnshareFolderFromDevice`
- **Status & config:** `GetFolderStatusJSON`, `GetConnectionsJSON`, `GetConfigJSON`, `SetDiscoveryEnabled`
- **Pending shares:** `GetPendingFoldersJSON`, `AcceptPendingFolder`
- **Conflicts:** `GetConflictFilesJSON`, `ResolveConflict`, `KeepBothConflict`, `ReadFileContent`, `RemoveConflictFilesForOriginal`
- **Filters:** `GetFolderIgnores`, `SetFolderIgnores`, `ScanFolderForKnownPatterns`
- **Events:** `GetEventsSince`
</details>

## 📱 iOS app structure (`ios/VaultSync/`)

| Group | Contents |
|---|---|
| `App/` | `VaultSyncApp` entry point, `AppDelegate` (push + background tasks) |
| `Models/` | data types (`SyncEventItem`, `IgnorePreset`, `RelayProvisionStatus`, …) |
| `ViewModels/` | `SetupChecklistViewModel` |
| `Views/` | SwiftUI views (Content, Onboarding, Cloud Relay, Relay Diagnostics, Sync Issues, Conflicts, QR scanner, …) |
| `Services/` | `SyncBridgeService`, `SyncthingManager`, `BackgroundSyncService`, `RelayService`, `KeychainService`, `SubscriptionManager`, `TipJarManager`, `BookmarkService`, `VaultManager`, `SyncHistoryStore` |
| `Resources/` · `*.lproj/` | assets, theme, localization helpers (en, de, es, zh-Hans) |

The optional `notify/` sidecar watches Syncthing on the homeserver and sends APNs wake-ups; see [relay-spec.md](relay-spec.md) for the protocol and [troubleshooting.md](troubleshooting.md) for end-user issues.
