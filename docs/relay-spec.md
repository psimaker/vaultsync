# VaultSync Cloud Relay — Specification

> **Status:** Cloud Relay 1.3.0 is live in production. Provisioning requires a verified StoreKit signed transaction; existing legacy registrations have a bounded compatibility window through October 31, 2026. The additive observation/status contract is available in the Relay, and the matching app support is merged to `main`, but that app support has not shipped as VaultSync 2.0. A Relay-observed signal proves only accepted Relay processing: not helper identity, APNs delivery, background start, local data progress, upload, download, or a roundtrip. Existing Relay v1 provisioning, trigger, and push contracts remain unchanged. This document is the protocol and architecture reference for the relay, the `vaultsync-notify` sidecar, and the iOS client.

## Overview

Push-notification service that forwards Syncthing file-change events to iOS devices via APNs. It solves the core iOS limitation — no real-time background sync — by waking the app on demand instead of polling.

In the app code on `main`, the **Cloud Relay** tab → **Relay health & diagnostics** keeps backend reachability, per-homeserver Relay observation, and wake-ups received locally on this iPhone as separate evidence. That app support has not shipped as VaultSync 2.0, and none of those fields alone proves APNs delivery, background execution, or synchronization.

---

## Architecture

```
┌──────────────────────────────────┐
│        User's Homeserver         │
│                                  │
│  ┌────────────┐  ┌────────────┐  │
│  │  Syncthing │  │ vaultsync- │  │
│  │  Instance  │──│   notify   │  │
│  │            │  │ (Docker)   │  │
│  └────────────┘  └─────┬──────┘  │
│                        │         │
└────────────────────────┼─────────┘
                         │ HTTPS POST
                         │ (wake-up signal only)
                         ▼
              ┌─────────────────────┐
              │  relay.vaultsync.eu │
              │  (Central Relay)    │
              │                     │
              │  - Receives trigger │
              │  - Sends APNs push  │
              │  - Stores tokens    │
              └──────────┬──────────┘
                         │ APNs
                         ▼
              ┌─────────────────────┐
              │    iOS Device       │
              │    VaultSync App    │
              │                     │
              │  Push received →    │
              │  Start sync         │
              └─────────────────────┘
```

### Components

**vaultsync-notify (Homeserver Container)**
- Docker container or prebuilt static binary, runs alongside the user's Syncthing instance
- One-line installer (`curl -fsSL https://vaultsync.eu/notify.sh | sh`): detects `config.xml`, runs the helper as the uid:gid owning it, picks Docker or a binary-backed system service, and finishes with the `--doctor` preflight
- Subscribes to Syncthing's REST API event stream (`/rest/events`)
- Filters for relevant outgoing-change signals: `LocalIndexUpdated`, plus `FolderCompletion` only when a peer still needs data
- On file changes: sends a wake-up signal to the central relay
- Configurable debounce (default: 5s) to batch rapid changes into one push
- Auto-detects the Syncthing API key and URL from `config.xml` — no key to copy (override via env if needed)
- Reads its Syncthing Device ID from `/rest/system/status` at startup
- **Startup-announce** (`STARTUP_ANNOUNCE`, default on): sends one wake-up the moment it starts, so a freshly-subscribed device flips to "Cloud Relay active" without waiting for the next change
- **Stale-peer sweep** (`STALE_RETRIGGER_SECONDS`, default 6 h): while any unpaused peer still reports outstanding `need{Items,Bytes,Deletes}` via `/rest/db/completion`, re-sends a wake-up on a slow cadence. This recovers a phone that missed its push (APNs expiry is ~1 h) without waiting for the next vault change
- No persistent storage required — stateless except for config

**Central Relay (relay.vaultsync.eu)**
- Receives wake-up signals from homeserver containers
- Forwards them as silent APNs pushes to registered iOS devices
- Manages device token registration via provision endpoint
- Minimal infrastructure: single service + database for tokens
- Horizontally scalable if needed (stateless request handling, shared DB)

---

## Routing and Provisioning Model

Identity is based on **Syncthing Device IDs** — no user accounts, no API keys.

- The homeserver container auto-reads its own Device ID from the local Syncthing API
- The iOS app provisions the relay by sending the homeserver's Device ID (from its peer list), the APNs token, and the StoreKit **signed transaction (JWS)**
- The central relay verifies the signed transaction against Apple's certificate chain to confirm an active subscription and read its expiry (offline — no per-request Apple API call). During the compatibility window, an older app can refresh only a pre-existing matching legacy registration; it cannot create a new one
- Trigger v1 requests are matched by Device ID. They do not carry cryptographic sender authentication; knowledge of a Device ID is not proof of helper identity

This avoids a user account system while keeping provisioning fail-closed. The
Device ID is a routing identifier both sides already know, not a secret or an
authenticated helper credential. Authenticated trigger v2 remains a separate
wire milestone.

---

## API — Central Relay (relay.vaultsync.eu)

Base URL: `https://relay.vaultsync.eu/api/v1`

### POST /provision

Provision an iOS device for push notifications. Called by the iOS app after a successful StoreKit purchase.

```json
// Request
{
  "device_id": "XXXXXXX-XXXXXXX-...",  // Homeserver Syncthing Device ID
  "apns_token": "abc123...",            // APNs device token (hex string)
  "transaction_id": "eyJ...JWS..."      // StoreKit signed transaction (JWS); legacy builds send the numeric originalID
}

// Response 200
{
  "status": "provisioned"
}

```

- Verifies the StoreKit signed transaction (JWS) against Apple's certificate chain (offline) and stores the limited verification record described in the Privacy Policy
- Creates or updates the device registration for the given Device ID
- One Device ID can have multiple APNs tokens (iPad + iPhone)
- On token rotation: call again with the new APNs token
- HTTP 409 is a rejected stale/conflicting registration and must never be treated as success

### DELETE /provision

Remove a device token.

```json
// Request
{
  "device_id": "XXXXXXX-XXXXXXX-...",
  "apns_token": "abc123..."
}

// Response 200
{
  "status": "deprovisioned"
}
```

### POST /trigger

Wake-up signal from homeserver container. Sends silent push to all devices registered for this Device ID.

```json
// Request
{
  "device_id": "XXXXXXX-XXXXXXX-..."
}

// Response 202
{
  "status": "accepted",
  "devices_notified": 2
}
```

- Returns 202 Accepted immediately — push delivery is async
- No file content, no folder names, no metadata — just a wake-up signal
- Rate limited server-side (separate from the client `DEBOUNCE_SECONDS`): roughly 1 push per Device ID per ~30s window
- The sidecar's **startup-announce** posts here too. Relay observation can therefore show that a v1 signal for the Device ID was accepted, but v1 does not authenticate who sent it

After syntax, active-subscription, and database checks succeed, the Relay stores
the time at which it observed the signal. This happens before the 30-second push
debounce: a valid debounced signal counts as observed but never as another push
or APNs success. Rejected or database-failed requests do not update the time.

#### Error responses and how `vaultsync-notify` reacts

The trigger endpoint distinguishes a *subscription state* from a *misconfiguration*, and the sidecar reacts accordingly so a normal lapse never turns into a crash-restart loop:

| Status | Meaning | Sidecar behaviour |
|---|---|---|
| `400`/`401`/`402`/`403` | No active subscription for this Device ID — expired, cancelled, or not yet provisioned | **Recoverable.** Log and keep running; re-check on a slow cadence so delivery resumes automatically once the subscription is active again. |
| `404` | Endpoint missing — wrong `RELAY_URL` or a broken relay deployment | **Fatal.** Exit so the operator fixes the configuration (normally caught earlier by the startup `/health` check). |
| `429` | Server-side rate limit | Recoverable. Retry honouring `Retry-After`. |
| `5xx` / other | Transient relay/network fault | Recoverable. Retry with exponential backoff. |

The sidecar never exits on a subscription-state response; only a genuine misconfiguration (`404`) is fatal.

### POST /status

Read the last Relay-observed signal for one homeserver. This is a `POST` so the
Device ID and signed transaction stay out of URLs and query strings.

```json
// Request
{
  "device_id": "XXXXXXX-XXXXXXX-...",
  "signed_transaction": "eyJ...JWS..."
}

// Response 200, when a signal was observed
{
  "v1_trigger_observed": true,
  "last_trigger_observed_at": "2026-07-12T10:00:00Z",
  "checked_at": "2026-07-12T10:01:00Z"
}

// Response 200, when none was observed
{
  "v1_trigger_observed": false,
  "checked_at": "2026-07-12T10:01:00Z"
}
```

The Relay verifies the JWS with the same certificate, bundle, app, product,
environment, type, signed-date, expiry, revocation, and transaction-reason rules
as provisioning. The original transaction, product, and environment must match
the requested Device ID's active `verified` row. Missing, unverified, expired,
revoked, foreign, or legacy-only evidence fails closed. The route is
rate-limited and never returns tokens, transaction identifiers, JWS, device
counts, or information about another homeserver.

This status means only “Relay observed a v1 signal for this Device ID.” It does
not authenticate the helper, prove APNs delivery, or prove synchronization.

### GET /health

No authentication required.

```json
// Response 200
{
  "status": "ok",
  "version": "1.3.0"
}
```

The `notify` client validates only `status == "ok"`. A 200 means the relay is
*reachable*, not that a server signal was observed or a push delivered. A silent
push recorded locally on the iPhone is stronger delivery evidence, but still not
proof that synchronization completed.

---

## API — Homeserver Container (vaultsync-notify)

Configuration via environment variables. `RELAY_URL` is the only required value — the Syncthing key and URL are auto-detected from `config.xml`:

| Variable | Required | Description |
|---|---|---|
| `RELAY_URL` | Yes | Central relay URL (`https://relay.vaultsync.eu`). No built-in default, so the helper never wakes a relay you didn't choose. |
| `SYNCTHING_API_KEY` | No | Auto-detected from `config.xml`; set to override. |
| `SYNCTHING_API_URL` | No | Auto-detected from `config.xml`; set for a sibling container (e.g. `http://syncthing:8384`). |
| `SYNCTHING_CONFIG` | No | Explicit path to `config.xml` when not in a standard location. |
| `STARTUP_ANNOUNCE` | No | Send one wake-up on startup (default `true`). |
| `SYNCTHING_CONFIG_WAIT_SECONDS` | No | First-boot wait for `config.xml` (default `60`). |
| `DEBOUNCE_SECONDS` | No | Debounce interval for batching events (default `5`). |
| `WATCHED_FOLDERS` | No | Comma-separated folder IDs to watch (default: all). |
| `STALE_RETRIGGER_SECONDS` | No | Re-send a wake-up on this cadence while a peer still needs data (default `21600` = 6 h; `0` disables). Recovers devices that missed a push — APNs silent pushes expire after ~1 h, and the change-driven path never fires twice for the same change. |

The container reads its own Syncthing Device ID automatically from `/rest/system/status` at startup — no manual Device ID configuration. It consumes the Syncthing event stream and pushes outbound to the relay. Full operator reference: [../notify/README.md](../notify/README.md).

---

## Security

### Data Privacy

- **No file content is ever transmitted.** The homeserver container sends only its Syncthing Device ID. No folder names, file names, file sizes, or metadata leave the homeserver.
- The central relay receives and forwards an anonymous wake-up signal — it has no knowledge of what changed or where.
- The central relay stores the last accepted v1 signal time per Device ID so the app can explain which wake-up leg is pending.
- APNs payload is a silent push with no visible content (`content-available: 1`, no alert/body).

### Token Storage

- Device tokens are encrypted at rest (AES-256-GCM) in the central relay database
- Encryption key stored separately from the database (environment variable or secrets manager)
- Tokens reported invalid by APNs (BadDeviceToken / Unregistered) are removed automatically on the next trigger

### Authentication

- No API keys or user accounts. The Syncthing Device ID is a routing identifier; trigger v1 does not cryptographically authenticate the sender
- Provisioning sends the StoreKit signed transaction (JWS), verified offline against Apple's certificate chain; the verified expiry gates the subscription server-side (an expired or revoked subscription stops receiving pushes). Compatibility requests from older apps can only refresh an exact pre-existing legacy mapping until the published cutoff
- Trigger requests are matched by Device ID — only Device IDs with an active, non-expired subscription receive push notifications
- Rate limiting per Device ID: 60 requests/minute for provisioning, 10 triggers/minute
- TLS required on all endpoints (HSTS, minimum TLS 1.2)

### APNs

- Token-based authentication (p8 key), not certificate-based
- Push type: `background` with `content-available: 1`
- Topic: app bundle ID (`eu.vaultsync.app`)
- Priority: 5 (low — allows iOS to batch/defer for power optimization)
- Expiration: +1 hour (allows APNs to retry delivery when the device is briefly unreachable)
- Environment: `project.yml` ships the `development` entitlement (`aps-environment`); App Store / TestFlight archives use the `production` environment — confirm the entitlement at archive time

## Pricing

| Tier | Price | What's included |
|---|---|---|
| **VaultSync App** | Free | Full sync, background refresh, all features |
| **Cloud Relay** | Monthly or yearly subscription | Push notifications via relay.vaultsync.eu |

- Cloud Relay subscription managed via App Store (StoreKit 2, auto-renewable; monthly or yearly). The price is set in App Store Connect and shown in the user's local currency at runtime via StoreKit — never hard-coded (USD reference: ~$1.99/month or ~$14.99/year). No free trial.
- App provisions the relay after successful purchase using the StoreKit signed transaction (JWS)
- No feature gates in the container itself — the gate is the central relay accepting provisioned Device IDs

---

## iOS Integration

The iOS client code on `main` implements the relay flow described below; see `AppDelegate.swift`, `RelayService.swift`, and `SubscriptionManager.swift` for detail. Its observation/status support has not yet shipped as VaultSync 2.0.

- **APNs registration** — registers for remote notifications at launch and converts the device token to a hex string (`AppDelegate`).
- **Provisioning** — the app POSTs each known homeserver Device ID, the APNs token, and a currently locally verified signed transaction (JWS) to `/api/v1/provision`. It does this after purchase, Restore Purchases, renewal, APNs-token rotation, and once after updating an existing installation. Without verified signed evidence it sends no request and preserves the existing registration.
- **Migration state** — progress is stored independently per homeserver. A partial or transient failure remains retryable and never changes onboarding, Syncthing identity, vault selection, folder mapping, or vault paths.
- **Observation status** — only while Relay waiting/diagnostics is visible, the app presents its current locally verified active JWS to `POST /status` for each verified homeserver mapping. The first check is immediate, retries use 15/30/60/120-second delays, and the finite poll stops after five attempts, view exit, local wake-up, inactive/unverified entitlement, or rate limiting. Multi-homeserver results remain independent and are not persisted as subscription state.
- **Push reception** — a silent push restores the vault bookmarks, starts Syncthing via the Go bridge, polls for completion within the ~30s background budget, then stops Syncthing and releases the bookmarks. This shares the `BGAppRefreshTask` code path.
- **Background modes** — `UIBackgroundModes` includes `remote-notification` alongside `fetch` and `processing`.
- **Subscription management** — StoreKit 2 auto-renewable subscription; status, price, and a Manage Subscription link live in the Cloud Relay tab. The price is read from StoreKit at runtime and never hard-coded.

Cloud Relay is configured from its own **Cloud Relay** tab, not onboarding.

The app models StoreKit verification, verified provisioning, backend
reachability, per-homeserver Relay observation, local silent-push receipt,
background-sync start, and observed local sync progress as separate evidence.
No weaker proof sets a stronger success state. Upload, controlled download, and
a confirmed roundtrip are not implemented; they require a separately approved,
correlated helper contract and runtime milestone.

---

## Why there is no self-hosted relay

The relay's only job is sending APNs pushes — and APNs only accepts pushes for `eu.vaultsync.app` that are signed with VaultSync's own APNs key (a `p8` key bound to the app's bundle ID and developer team). That key cannot be distributed: anyone holding it could push to every VaultSync install, and sharing it violates Apple's terms. This is an APNs constraint, not a pricing decision — a self-hosted relay that wakes the App Store build is technically impossible no matter who runs the server.

What IS open:

- **`RELAY_URL` is configurable** in `vaultsync-notify` — used for development and testing against a mock relay. It exists so the helper never wakes a relay the operator didn't choose, not as a self-hosting path for the shipped app.
- **VaultSync is open source (MPL-2.0).** A developer building the app from source with their own bundle ID and Apple Developer account can run the entire stack themselves — own APNs key, own relay, own build. The architecture supports it; it is not a supported product configuration.

Everything else about the system is already self-hosted: notes sync peer-to-peer between the user's own devices, and the only thing that ever touches VaultSync infrastructure is an anonymous wake-up signal.

---

## Open Questions

1. **Multi-vault routing:** Should the push signal include which vault changed, so the app can prioritize? Currently: wake up and sync everything. Tradeoff: more metadata leaves the homeserver.
2. **Fallback behavior:** The sidecar's stale-peer sweep (`STALE_RETRIGGER_SECONDS`) now re-sends a wake-up while a peer still needs data, recovering missed/expired pushes. Remaining question: should the central relay also retry failed APNs sends itself, or is the sweep + BGAppRefreshTask polling enough?
3. **GDPR — data processing agreement:** Device tokens are personal data. The privacy policy now ships ([../PRIVACY.md](../PRIVACY.md), surfaced in Settings → About); a data processing agreement for EU users is still open.

---

## Troubleshooting References

- Relay/API/operator troubleshooting: [troubleshooting.md](troubleshooting.md)
- notify container setup and doctor mode: [../notify/README.md](../notify/README.md)
