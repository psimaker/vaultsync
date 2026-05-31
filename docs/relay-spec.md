# VaultSync Cloud Relay — Specification

> **Status:** Shipped in v1.4.0; signed-transaction (JWS) verification and server-side expiry enforcement added in v1.5.0. This document is the protocol and architecture reference for the relay, the `vaultsync-notify` sidecar, and the iOS client. Sections marked _Roadmap_ (self-hosted relay) are not yet built.

## Overview

Push-notification service that forwards Syncthing file-change events to iOS devices via APNs. It solves the core iOS limitation — no real-time background sync — by waking the app on demand instead of polling.

In the app, **Settings → Open Relay Diagnostics** is the live view of this: health endpoint, APNs registration, last trigger received, and whether the relay is actually *delivering wake-ups* versus merely *reachable*.

---

## Architecture

```
┌──────────────────────────────────┐
│        User's Homeserver         │
│                                  │
│  ┌────────────┐  ┌────────────┐  │
│  │  Syncthing  │  │ vaultsync- │  │
│  │  Instance   │──│   notify   │  │
│  │             │  │ (Docker)   │  │
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
- Docker container, runs alongside the user's Syncthing instance
- Subscribes to Syncthing's REST API event stream (`/rest/events`)
- Filters for relevant outgoing-change signals: `LocalIndexUpdated`, plus `FolderCompletion` only when a peer still needs data
- On file changes: sends a wake-up signal to the central relay
- Configurable debounce (default: 5s) to batch rapid changes into one push
- Automatically reads its Syncthing Device ID from `/rest/system/status` at startup
- No persistent storage required — stateless except for config

**Central Relay (relay.vaultsync.eu)**
- Receives wake-up signals from homeserver containers
- Forwards them as silent APNs pushes to registered iOS devices
- Manages device token registration via provision endpoint
- Minimal infrastructure: single service + database for tokens
- Horizontally scalable if needed (stateless request handling, shared DB)

---

## Authentication Model

Identity is based on **Syncthing Device IDs** — no user accounts, no API keys.

- The homeserver container auto-reads its own Device ID from the local Syncthing API
- The iOS app provisions the relay by sending the homeserver's Device ID (from its peer list), the APNs token, and the StoreKit **signed transaction (JWS)**
- The central relay verifies the signed transaction against Apple's certificate chain to confirm an active subscription and read its expiry (offline — no per-request Apple API call). Older app builds that send a bare numeric transaction ID are accepted for backward compatibility
- Trigger requests from the homeserver container are matched by Device ID — no Bearer auth needed

This eliminates the need for a user account system. The Syncthing Device ID serves as a stable, unique identifier that both the iOS app and the homeserver container already know.

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

// Response 409 (already provisioned — token updated)
{
  "status": "updated"
}
```

- Verifies the StoreKit signed transaction (JWS) against Apple's certificate chain (offline) and stores the verified expiry; legacy numeric transaction IDs are accepted for backward compatibility
- Creates or updates the device registration for the given Device ID
- One Device ID can have multiple APNs tokens (iPad + iPhone)
- On token rotation: call again with the new APNs token

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

### GET /health

No authentication required.

```json
// Response 200
{
  "status": "ok"
}
```

The `notify` client validates only `status == "ok"`. A 200 means the relay is *reachable*, not that pushes are delivered end-to-end — end-to-end delivery is confirmed only by an actual received trigger.

---

## API — Homeserver Container (vaultsync-notify)

Configuration via environment variables:

| Variable | Required | Description |
|---|---|---|
| `SYNCTHING_API_URL` | Yes | Syncthing REST API URL (e.g. `http://localhost:8384`) |
| `SYNCTHING_API_KEY` | Yes | Syncthing API key for event subscription |
| `RELAY_URL` | Yes | Central relay URL (default: `https://relay.vaultsync.eu`) |
| `DEBOUNCE_SECONDS` | No | Debounce interval for batching events (default: 5) |
| `WATCHED_FOLDERS` | No | Comma-separated folder IDs to watch (default: all) |

The container reads its own Syncthing Device ID automatically from `/rest/system/status` at startup. No manual Device ID configuration needed.

The container consumes the Syncthing event stream and pushes outbound to the relay.

---

## Security

### Data Privacy

- **No file content is ever transmitted.** The homeserver container sends only its Syncthing Device ID. No folder names, file names, file sizes, or metadata leave the homeserver.
- The central relay receives and forwards an anonymous wake-up signal — it has no knowledge of what changed or where.
- APNs payload is a silent push with no visible content (`content-available: 1`, no alert/body).

### Token Storage

- Device tokens are encrypted at rest (AES-256-GCM) in the central relay database
- Encryption key stored separately from the database (environment variable or secrets manager)
- Tokens reported invalid by APNs (BadDeviceToken / Unregistered) are removed automatically on the next trigger

### Authentication

- No API keys or user accounts — identity is the Syncthing Device ID
- Provisioning sends the StoreKit signed transaction (JWS), verified offline against Apple's certificate chain; the verified expiry gates the subscription server-side (an expired or revoked subscription stops receiving pushes). Legacy numeric transaction IDs are still accepted for backward compatibility
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
| **Self-hosted Relay** | Free (roadmap) | User runs everything — no central relay needed |

- Cloud Relay subscription managed via App Store (StoreKit 2, auto-renewable; monthly or yearly). The price is set in App Store Connect and shown in the user's local currency at runtime via StoreKit — never hard-coded (USD reference: ~$1.99/month or ~$14.99/year). No free trial.
- App provisions the relay after successful purchase using the StoreKit signed transaction (JWS)
- Homeserver container works identically regardless of cloud vs self-hosted relay
- No feature gates in the container itself — the gate is the central relay accepting provisioned Device IDs

---

## iOS Integration (shipped in v1.4.0; signed-transaction (JWS) provisioning in v1.5.0)

The iOS client implements the full relay flow; see `AppDelegate.swift`, `RelayService.swift`, and `SubscriptionManager.swift` for detail.

- **APNs registration** — registers for remote notifications at launch and converts the device token to a hex string (`AppDelegate`).
- **Provisioning** — on a successful StoreKit purchase, the app POSTs each homeserver peer's Device ID, the APNs token, and the signed transaction (JWS) to `/api/v1/provision`. Provisioned IDs are stored in the Keychain and re-provisioned on token rotation; on subscription expiry the tokens are deprovisioned.
- **Push reception** — a silent push restores the vault bookmarks, starts Syncthing via the Go bridge, polls for completion within the ~30s background budget, then stops Syncthing and releases the bookmarks. This shares the `BGAppRefreshTask` code path.
- **Background modes** — `UIBackgroundModes` includes `remote-notification` alongside `fetch` and `processing`.
- **Subscription management** — StoreKit 2 auto-renewable subscription; status, price, and a Manage Subscription link live in Settings → Cloud Relay. The price is read from StoreKit at runtime and never hard-coded.

Cloud Relay is configured from **Settings → Cloud Relay**, not onboarding.

---

## Self-Hosted Variant

> **Roadmap — not yet built.** The components below are design exploration, not shipping code. Today `vaultsync-notify` points at the central `relay.vaultsync.eu` by default.

For users who don't want to use the central relay or pay for the subscription.

### What the User Deploys

**1. vaultsync-notify container** (same Docker image as cloud variant)
- Configured with `RELAY_URL` pointing to their own relay server instead of relay.vaultsync.eu

**2. vaultsync-relay-server** (additional component)
- Minimal server that receives triggers and sends APNs pushes
- Requires the user's own Apple Developer Account ($99/year) for APNs credentials
- OR: uses the VaultSync APNs credentials bundled in a self-hosted-friendly way (TBD — licensing/security implications)
- Docker image provided, single binary

### Self-Hosted Limitations

- User must manage their own APNs credentials (p8 key from Apple Developer Portal)
- No automatic token rotation handling — user manages the database
- No SLA or uptime guarantees
- User responsible for TLS termination (reverse proxy like Caddy/nginx)

### Self-Hosted Alternative: Direct Push (No Relay Server)

A simplified variant where the homeserver container sends APNs pushes directly:

- vaultsync-relay container configured with APNs credentials directly
- No intermediate relay server needed
- Simplest setup but requires Apple Developer Account
- Single container, no database
- Config: `APNS_KEY_FILE`, `APNS_KEY_ID`, `APNS_TEAM_ID`, `DEVICE_TOKEN` (hardcoded for single device)

---

## Open Questions

1. **APNs credentials for self-hosted:** Can we distribute our p8 key in a way that allows self-hosted users to send pushes without their own Developer Account? Likely no — security and ToS implications.
2. **Multi-vault routing:** Should the push signal include which vault changed, so the app can prioritize? Currently: wake up and sync everything. Tradeoff: more metadata leaves the homeserver.
3. **Fallback behavior:** If push delivery fails (APNs errors, network issues), should the container retry? Or rely on the existing BGAppRefreshTask polling as fallback?
4. **GDPR — data processing agreement:** Device tokens are personal data. The privacy policy now ships ([../PRIVACY.md](../PRIVACY.md), surfaced in Settings → About); a data processing agreement for EU users is still open.

---

## Troubleshooting References

- Relay/API/operator troubleshooting: [troubleshooting.md](troubleshooting.md)
- notify container setup and doctor mode: [../notify/README.md](../notify/README.md)
