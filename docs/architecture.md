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
| Upload observed | Exact app/helper/homeserver/folder/operation correlation | Only an explicit foreground check in unreleased M5 app source can accept the exact paired-helper attestation for its active request/query. |
| Download observed | Exact controlled response correlation | Not implemented in the app; a helper response or synchronized file alone cannot set it. |
| Full roundtrip confirmed | One matching upload-then-download correlation | Not implemented; it requires the later controlled download from the same active chain. |

None automatically implies the next. Relay reachability is not a trigger;
trigger observation is not APNs delivery; push receipt is not background start;
engine reachability, scans, index updates, `idle`, and 100% completion are not
local data progress. A successful incoming file application proves that this
iPhone applied a file change, but not that network bytes moved, which peer
supplied every block, or that the check caused the change. Upload is a separate,
explicitly initiated Decision 024 field in unreleased source. Controlled download
remains independent and unset, so a full roundtrip cannot be derived.

Server snapshots contain only entitlement, provisioning, backend, and
per-homeserver Relay observation. The v1 push contains no homeserver/folder
identifier, so push receipt, background start, and organic background progress
remain explicitly iPhone-wide and must not mark server A or B. Only the manual
check can scope fresh local evidence to one folder and its sole configured peer.

#### Manual synchronization-path check

Relay Diagnostics exposes the passive-check entry point. An explicit tap takes a current
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
never create a false success. It remains separate from the explicit upload-only
operation below and cannot populate that operation's evidence. Controlled
download and roundtrip require their later Decision 024 milestones. Relay v1 is
unchanged.

#### Opt-in correlated-roundtrip helper runtime — foreground upload only

[Decisions 021–024](decisions/021-capability-negotiated-helper-contract-for-correlated-roundtrip-proof.md)
define the proof and rollout boundaries. Helper 2.0.2 is published and its
immutable digest plus upgrade, downgrade, and forward-recovery path are
verified. The source tree now also contains the unreleased app-side explicit
capability, pairing, credential-lifecycle, and namespace-authorization control
plane plus the explicit M5 foreground upload operation. Product upload is
implemented only to the exact signed-attestation boundary and remains
unreleased; controlled download and causal roundtrip are unset. VaultSync 2.0
remains NO-GO.

One upload operation begins only after a user tap and a second localized
confirmation. The app rechecks the exact current capability, pairing and
namespace authorization, settled `sendreceive` folder, one designated connected
unpaused peer, engine generation, path overlap, Syncthing ignore behavior, and
empty operation slot. It never discovers or configures a peer, share, folder,
namespace, trust decision, or ignore. A failed preflight creates nothing.

The app generates a fresh operation ID, two nonces, and exactly 256 random
request bytes in memory; signs Decision 024 types 3 and 4; exclusively creates
the exact request beneath the already authorized installation; and rescans only
the selected folder. It sends one byte-identical signed query on the fixed eight
poll schedule. Only an exact type-5 response from the TLS-pinned paired helper,
with every key, epoch, binding, operation, digest, nonce, signature, and clock
gate valid for the still-active tuple, sets `upload observed`. HTTP status,
request creation, rescan, index/idle/completion state, timestamps, and a
synchronized attestation copy cannot do so. Cancellation, view exit, refresh,
app/engine restart, target or credential change, timeout, and conflict are
terminal; late responses never upgrade them. Download and roundtrip remain
immutable false in this milestone.

The runtime is gated by an operator-authored read-only configuration plus a
separate writable state directory. If either is absent, existing helpers retain
their exact Trigger-v1 behavior and create no diagnostics credential, listener,
namespace, mapping, artifact, trust, or network request. The ordinary helper
installers and Compose configuration do not activate it.

When explicitly enabled, one TLS-1.3-only listener exposes exact fixed POST
paths for D022 pairing, D023 enablement/authorization, and D024 capability,
upload attestation, response authorization, and cleanup. A QR supplies the
one-time HMAC secret, helper public key, and P-256 SPKI pin; deterministic CBOR
and Ed25519 application signatures remain authoritative. Paths, identifiers,
keys, pins, bindings, nonces, bodies, and artifacts stay out of URLs and logs.
Every request has fixed body/time/rate limits and fails closed on unknown fields,
versions, suites, keys, epochs, signatures, replays, or tuple changes.

The helper state store keeps separate signing/TLS keys, opaque bindings,
authorized app public keys, lifecycle state, revocations, and stable local
folder-to-mount mappings. App-key, helper-key, and TLS-pin transitions remain
operation-unavailable until a capability query authenticated under the exact
committed proposed state succeeds. Helper rotation appends a dual-signed D023
manifest; app/helper rotation then requires a new dual-signed immutable
authorization epoch before any operation can resume. Loss or suspected
compromise requires explicit re-pairing, never trust transfer.
A shared helper-key or TLS-pin transition does not return signed capability
success to an early installation while another required installation is still
unconfirmed; it remains unavailable until the global commit is durable, then an
exact retry recovers.
All diagnostics protocol, operator, namespace, startup-reconciliation, and
admin mutations share a protected cross-process lock. This prevents a separate
`docker exec` process from rotating or revoking credentials between a runtime
state check, an immutable namespace append, and the corresponding atomic state
update.

Namespace creation remains a distinct local operator mutation. A signed app
enablement is necessary but insufficient: a one-shot Linux installer rechecks
the exact local Syncthing folder, `.stfolder`, canonical path, expanded ignore
rules, bindings, signatures, and collision state, then creates the one visible
`VaultSync Diagnostics` child. The host source device/inode captured before the
bind must match inside that one-shot container before any creation. Normal
runtime receives only the exact child, opens it by descriptor, and rechecks
inode, device, mount identity, ownership, mode, link count, allocation, fixed
layout, and immutable chains. It also verifies an ephemeral SHA-256 deployment
binding over the folder ID, Syncthing's canonical path, fixed alias, and opened
namespace device/inode; the raw host path is not persisted or logged. Network
input cannot select a path. Capability and every operation pin the local Device
ID before and after re-reading the exact folder mode/pause state and
Syncthing-expanded ignore patterns; a reported ignore-parser error fails closed.
Folder and ignore responses are bounded by fixed byte, entry-count, and
pattern-length ceilings before matching.
The supported package permits only an exact
loopback HTTP Syncthing API endpoint. Syncthing, Relay, and operator HTTP clients
reject redirects. The helper never edits configuration, ignores, discovery,
shares, peers, or trust.

Crash recovery is forward-only. An exact authorization message may be retried
after both its immutable file and credential update became durable, including
after its original signed expiry because that branch cannot create or advance
state. If root creation and its protected root record completed before the local
mount config was written, a repeated explicit installer command can resume only
that same root after rechecking the current helper key/epoch, active folder
authorization, root digest/signature/layout, parent device/inode, Syncthing
Device/folder, and ignores. Recovery mode creates nothing and never adopts an
unregistered root; helper/TLS rotation is blocked while a registered root lacks
authorization.

Each app installation pairs separately and receives an independent stable D023
installation binding. It can join an existing authenticated namespace only with
its own signed authorization; no trust or identity is inherited and no second
namespace is created. Revocation preserves that installation's immutable signed
history. A later namespace-wide helper manifest can advance for another active
installation without rewriting revoked records, but that active installation
must append a fresh authorization epoch before runtime operations resume. The
manifest append itself requires every affected namespace to pass the pinned
Device ID, folder/ignore, root, and deployment-mount preflight first.

Only rootful Docker Engine on an explicitly confirmed standard Linux host is a
supported diagnostics package. The container uses an immutable image content
ID, read-only root, dropped capabilities, `no-new-privileges`, read-only config
with exact private mode and single-link identity, read-only exact `config.xml`,
an exact non-root config-owner UID/GID, separate state, and one exact namespace
host bind. The installer displays the exact namespace path and requires separate
acknowledgement of path and retained copies before creation. Named volumes and
their backing paths, rootless Docker, remote/NAS/FUSE storage,
remote Docker daemons/contexts, non-Unix Docker endpoints, Docker Desktop, WSL,
systemd binaries, macOS, and Windows remain unsupported.
The runtime itself rejects non-Linux activation rather than exposing a partial
listener on an unsupported binary. Upgrade and downgrade preserve credentials,
namespace, mappings, backups, versions, conflict copies, and tombstones; an old
helper yields capability unavailable and never a weaker success.

The helper can reconstruct an exact authorized runtime session and process the
existing D024 foundations. The unreleased M5 app can use only its capability and
upload-attestation paths, but signatures establish only authorship and causal
bindings—not transport route, exact network bytes, direct peer, block
provenance, future delivery, or global sync health. No response has passed a
fresh post-authorization iPhone cursor/nanosecond/generation/`ItemFinished`
baseline. Cleanup remains evidence-orthogonal. Helper-first publication,
production rollout, rollback, the M5 real-device/PR gate, and the later download
and roundtrip app milestones remain mandatory.
See [helper runtime and packaging readiness](helper-runtime-packaging-readiness.md).
The app-side scope, compatibility, persistence, consent, and rollback boundaries
are documented in
[app capability, pairing, and namespace readiness](app-capability-pairing-namespace-readiness.md)
and [M5 foreground upload-only readiness](m5-upload-attestation-readiness.md).

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
