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

### Relay and sync proof hierarchy

VaultSync models proof as independent fields, never as one derived “sync
succeeded” flag:

| Proof | Scope | What can set it |
|---|---|---|
| StoreKit entitlement verified locally | App subscription | A current `.verified` StoreKit transaction |
| Relay provisioning confirmed | Homeserver Device ID | A successful, entitlement-backed v1 provision response |
| Relay backend reachable | Relay endpoint | A successful health request |
| Relay observed a v1 trigger | Homeserver Device ID | A consistent v1 status response |
| Silent push received locally | This iPhone, unattributed | The iOS remote-notification delegate |
| Background sync started | This iPhone, unattributed | Entry into the silent-push sync path |
| Local data progress observed | Background run, or one eligible server/folder check | A fresh, successful incoming file application (`ItemFinished`) |
| Upload confirmed | Server/folder/check correlation | Not available with the current helper |
| Download confirmed | Server/folder/check correlation | Not available as a controlled directional proof with the current helper |
| Full roundtrip confirmed | One matching upload-then-download correlation | Not available with the current helper |

None automatically implies the next. Relay reachability is not a trigger;
trigger observation is not APNs delivery; push receipt is not background start;
engine reachability, scans, index updates, `idle`, and 100% completion are not
local data progress. A successful incoming file application proves that this
iPhone applied a file change, but not that network bytes moved, which peer
supplied every block, or that the check caused the change. Upload and controlled
download therefore remain independent and unset, so a full roundtrip cannot be
derived.

Server snapshots contain only entitlement, provisioning, backend, and
per-homeserver Relay observation. The v1 push contains no homeserver/folder
identifier, so push receipt, background start, and organic background progress
remain explicitly iPhone-wide and must not mark server A or B. Only the manual
check can scope fresh local evidence to one folder and its sole configured peer.

#### Manual synchronization-path check

Relay Diagnostics exposes the only entry point. An explicit tap takes a current
subscription-local event cursor, nanosecond production-time boundary, and engine
generation, then observes at most five times with 2/4/8/12-second delays. Results
are kept in memory per unique server/folder pair. Only an unpaused `sendreceive`
or `receiveonly` folder with one known, connected, unpaused peer is eligible;
offline peers, send-only or encrypted folders, and ambiguous multi-peer folders
remain unavailable or unsupported. One successful folder never upgrades another.

Evidence must be newer than both the check start and its baseline cursor, and an
engine-generation change interrupts the check rather than reusing events. The
result becomes stale after 15 minutes. Cancellation, leaving Diagnostics, or an
inactive app lifecycle ends polling and releases the single-flight lease before a
retry. The check is passive: it creates no probe artifact, performs no rescan or
write, changes no folder or device mapping, persists no check identifier/result,
and never runs during onboarding, app launch, a silent push, or ordinary
background sync.

Ignore rules, missing paths, an event-buffer overflow, or a runtime folder error
can prevent an observation and therefore end conservatively as incomplete; they
never create a false success. A controlled upload/download roundtrip needs a
separately designed, additive, capability-negotiated helper contract and a
demonstrably safe app-owned diagnostics namespace. Relay v1 is unchanged by this
milestone.

#### Dormant correlated-roundtrip foundations — no runtime support

[Decision 021](decisions/021-capability-negotiated-helper-contract-for-correlated-roundtrip-proof.md)
defines the proposed proof and rollout boundaries. It does not authorize a
runtime change. The current app/helper still creates no probe or namespace, has
no diagnostic pairing credential, and cannot confirm upload, controlled
download, or a full roundtrip.

The repository contains a dormant implementation foundation for the proposed
Decision 022 helper credential and pairing protocol. It includes deterministic
CBOR, the fixed Ed25519/HMAC/P-256 suite, a dedicated atomic helper state store,
stable opaque bindings, explicit QR/pinned-TLS bootstrap messages, lifecycle
rotation/revocation/recovery state, and shared Go/Swift golden vectors for all
message types. The Go code has no call from `main`, listener, installer,
container configuration, Syncthing client, Relay client, or product command;
the Swift implementation is test-only. Consequently no credential directory,
key, endpoint, authorization, or pairing record is created in an installed
helper or app. Runtime pairing, app Keychain storage, transport, capability
discovery, namespace access, and every evidence transition remain unavailable.

The repository also contains a dormant Decision 023 namespace and
least-privilege foundation. It implements the five deterministic ownership
record types and dual-signature/digest chains, the exact visible constant
`VaultSync Diagnostics`, stable installation components, a separate atomic
namespace state store, explicit collision-safe preparation primitives, and
bounded create-once/read/cleanup operations. Linux access is rooted at an open
directory handle and rechecks inode, device, ownership, mode, link count, file
allocation, and mount identity; path-like network input is not accepted. All
other operating systems fail closed as unsupported.

This code is still unreachable from the installed helper and app. No product
installer, CLI command, endpoint, listener, automatic folder creation,
Syncthing configuration mutation, ignore-rule change, capability advertisement,
or app enablement flow exists. The only packaging execution is a local test
harness. It first gives an explicit installer phase one selected test folder,
then proves a read-only-root Linux container can operate with only the exact
existing namespace host bind, a separate state bind, and read-only config; it
also rejects a separately mounted child. Docker named volumes, rootless Docker,
NAS, Linux host/systemd, macOS, and Windows packaging remain unsupported.

The dormant M5 foundation implements only Decision 024 message types 3–5 for
the upload leg. Given an already authenticated M4 namespace handle and fixed
test binding, the helper-side attestor verifies the byte-exact active query,
re-opens and completely validates the exact app-authored 256-byte request,
atomically publishes one immutable helper-signed attestation, and returns those
same bytes idempotently. Its limits are fixed: one active operation per tuple,
eight polls, three starts/hour and twelve/day per app/folder, sixty starts/day
and eight active operations helper-wide, plus 30 requests/minute per paired app
and 120/minute helper-wide including invalid input. Restarts can recover only
the exact already-persisted attestation from authenticated artifacts; app-side
test state never resumes evidence after restart.

M5 is still not a product or helper runtime path. The Go attestor has no call
from `main`, listener, endpoint, transport, log, operation database, Relay or
Syncthing client. Swift parsing and acceptance exist only in the test target.
No capability response is emitted, and M5 itself contains no response or
cleanup phase. A dedicated E2E test runs two temporary local Syncthing instances
with discovery, Relay, NAT, upgrades, usage reporting, and crash reporting
disabled; it proves request propagation, confined helper reading, durable
attestation-before-reply, exact pinned-mock acceptance, and that a synchronized
attestation copy is not upload evidence. It publishes nothing.

The separate dormant M6 foundation implements only Decision 024 message types
6–9. It verifies an exact app-signed response authorization against the confined
request and helper attestation, atomically persists one immutable helper-signed
response containing exactly 256 random payload bytes, and performs
app-authenticated, digest-targeted cleanup of verified helper-owned artifacts.
Exact response bytes survive duplicate calls,
crashes, races, and helper restart; cleanup is idempotent and cannot target a
path supplied by the request. Go/Swift golden, parser, fuzz, model, privacy,
Linux confinement, crash, restart, and race tests cover this boundary.

M6 also remains unreachable from the installed helper and app. It adds no
listener, endpoint, advertised flag, capability response, namespace creation,
retry scheduler, startup scan, packaging, or publication. The response has not
been synchronized back to an iPhone and has no post-authorization cursor,
nanosecond, generation, or exact `ItemFinished` acceptance. Controlled download
and causal roundtrip therefore remain unimplemented and unset.

The proposed data plane uses a visible, exclusively app-owned namespace inside
one selected Syncthing folder. A fresh app-signed random request would need an
authenticated helper attestation that the paired helper read the exact request;
a fresh helper-signed response would then need to be applied and verified on
this iPhone inside the active cursor, nanosecond, and engine-generation bounds.
Only matching upload-then-download evidence for one operation, helper, folder,
and homeserver could derive a scoped roundtrip. Signatures can establish logical
app/helper authorship and causality, but not exact byte counts, a direct
transport peer, or per-block provenance.

Runtime remains blocked despite the dormant pairing, namespace, and upload
attestation foundations.
There is no production packaging or rollout, no Syncthing-matcher integration,
no app consent/enablement flow, and no authenticated operational correlation.
Product upload acceptance, controlled download, and roundtrip evidence are not
implemented. Rollout must be helper-first; an old or unreachable helper yields
capability unavailable rather than an error or a weaker success. Trigger v1,
provisioning, Relay status, APNs, StoreKit, and the Cloud Relay privacy boundary
remain unchanged, and the Relay never receives namespace or operation names,
paths, values, contents, correlations, or results.

### Connection paths & iOS network privacy

How peers are reached, fastest first:

1. **Direct LAN (TCP/QUIC to a private address)** — requires the iOS *Local
   Network* permission (`NSLocalNetworkUsageDescription`, prompt shown on first
   LAN dial). Peer LAN addresses come from global discovery; without this
   permission iOS silently blocks LAN dials — peers reachable over a direct
   WAN path (2) still connect directly, only the rest fall back to a relay (3).
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
- **Events:** `GetEventsSince`, `EventStreamGeneration`
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
