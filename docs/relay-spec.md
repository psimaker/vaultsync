# VaultSync Cloud Relay — Specification

## Overview

Push-Notification-Service that forwards Syncthing file-change events to iOS devices via APNs. Solves the core iOS limitation: no real-time background sync. Instead of polling, the relay wakes the app on demand.

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
- Optional sparse periodic poke to create additional `iPhone -> server` wake opportunities
- Automatically reads its Syncthing Device ID from `/rest/system/status` at startup
- No persistent storage required — stateless except for config
- Optionally exposes a direct Markdown upload endpoint so the iPhone can write changed notes into the server-side Syncthing volume during background wakes

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
- The iOS app provisions the relay by sending the homeserver's Device ID (from its peer list), the APNs token, and a StoreKit transaction ID
- The central relay validates the transaction ID with Apple to confirm an active subscription
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
  "transaction_id": 123456789           // StoreKit transaction originalID
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

- Validates the transaction ID with Apple's App Store Server API
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
- Rate limited: max 1 push per Device ID per debounce window (server-side, default 30s)

### GET /health

No authentication required.

```json
// Response 200
{
  "status": "ok",
  "version": "1.0.0"
}
```

---

## API — Homeserver Container (vaultsync-notify)

Configuration via environment variables:

| Variable | Required | Description |
|---|---|---|
| `SYNCTHING_API_URL` | Yes | Syncthing REST API URL (e.g. `http://localhost:8384`) |
| `SYNCTHING_API_KEY` | Yes | Syncthing API key for event subscription |
| `RELAY_URL` | Yes | Central relay URL (default: `https://relay.vaultsync.eu`) |
| `DEBOUNCE_SECONDS` | No | Debounce interval for batching events (default: 5) |
| `POKE_INTERVAL_MINUTES` | No | Sparse periodic silent-push wake-up for best-effort `iPhone -> server` catch-up |
| `UPLOAD_LISTEN_ADDR` | No | Optional bind address for the direct upload endpoint |
| `UPLOAD_ROOT_DIR` | No | Syncthing-backed filesystem root where uploaded Markdown files are written |
| `UPLOAD_AUTH_TOKEN` | No | Bearer token required by the direct upload endpoint |
| `WATCHED_FOLDERS` | No | Comma-separated folder IDs to watch (default: all) |

The container reads its own Syncthing Device ID automatically from `/rest/system/status` at startup. No manual Device ID configuration needed.

The container consumes the Syncthing event stream and pushes outbound to the relay. If the upload lane is enabled, it also exposes a direct authenticated Markdown upload endpoint for the iPhone.

---

## Security

### Data Privacy

- **No file content is ever transmitted.** The homeserver container sends only its Syncthing Device ID. No folder names, file names, file sizes, or metadata leave the homeserver.
- The central relay receives and forwards an anonymous wake-up signal — it has no knowledge of what changed or where.
- APNs payload is a silent push with no visible content (`content-available: 1`, no alert/body).

### Token Storage

- Device tokens are encrypted at rest (AES-256-GCM) in the central relay database
- Encryption key stored separately from the database (environment variable or secrets manager)
- Tokens are automatically purged after 90 days without a successful push delivery (APNs feedback)

### Authentication

- No API keys or user accounts — identity is the Syncthing Device ID
- Provisioning requires a valid StoreKit transaction ID, verified with Apple's App Store Server API
- Trigger requests are matched by Device ID — only provisioned Device IDs receive push notifications
- Rate limiting per Device ID: 60 requests/minute for provisioning, 10 triggers/minute
- TLS required on all endpoints (HSTS, minimum TLS 1.2)

### APNs

- Token-based authentication (p8 key), not certificate-based
- Push type: `background` with `content-available: 1`
- Topic: app bundle ID (`eu.vaultsync.app`)
- Priority: 5 (low — allows iOS to batch/defer for power optimization)
- Expiration: +1 hour (allows APNs to retry delivery when the device is briefly unreachable)

## Direct Upload Endpoint

If the upload endpoint is enabled in `vaultsync-notify`, VaultSync can send changed Markdown files directly to the homeserver during a relay-triggered background wake:

```text
PUT /api/v1/upload?path=brain/path/to/file.md
Authorization: Bearer <token>
Content-Type: text/markdown
X-VaultSync-Device-ID: <device-id>
```

Current scope:

- Markdown files only
- path traversal rejected
- empty files allowed
- writes land in the shared Syncthing volume and are then distributed by Syncthing as normal

This path is intended to improve `iPhone -> server` reliability under iOS background limits. It is not a full replacement for Syncthing conflict semantics.

---

## Pricing

| Tier | Price | What's included |
|---|---|---|
| **VaultSync App** | Free | Full sync, background refresh, all features |
| **Cloud Relay** | $0.99/month | Push notifications via relay.vaultsync.eu |
| **Self-hosted Relay** | Free (planned) | User runs everything — no central relay needed |

- Cloud Relay subscription managed via App Store (StoreKit 2, auto-renewable)
- App provisions the relay after successful purchase using the StoreKit transaction ID
- Homeserver container works identically regardless of cloud vs self-hosted relay
- No feature gates in the container itself — the gate is the central relay accepting provisioned Device IDs

---

## iOS Integration

### Required Changes

**1. APNs Registration**

- Add Push Notifications capability in Xcode
- Register for remote notifications at app launch (`UIApplication.shared.registerForRemoteNotifications()`)
- Implement `application(_:didRegisterForRemoteNotificationsWithDeviceToken:)` in AppDelegate
- Convert device token to hex string

**2. Provisioning**

- On successful StoreKit purchase: POST to `/api/v1/provision` with homeserver Device ID, APNs token, and transaction ID
- Homeserver Device ID comes from the peer list — the user has already added their homeserver as a Syncthing device
- Multiple peers: all peer Device IDs are provisioned (each homeserver gets its own registration)
- Provisioned Device IDs stored in Keychain for renewal handling
- On token refresh (iOS can rotate tokens): re-provision with new token

**3. Push Reception**

- Implement `application(_:didReceiveRemoteNotification:fetchCompletionHandler:)` with background mode
- On silent push received:
  1. Restore vault bookmarks (security-scoped access)
  2. Start Syncthing via Go bridge
  3. Poll for sync completion (max 30s — iOS background execution limit)
  4. Stop Syncthing, release bookmarks
  5. Call completion handler with `.newData` or `.noData`
- This is the same flow as the existing `BGAppRefreshTask` handler — extract shared logic

**4. Background Modes**

- Add `remote-notification` to `UIBackgroundModes` in Info.plist (in addition to existing `fetch` and `processing`)

**5. Subscription Management**

- StoreKit 2 integration for Cloud Relay subscription ($0.99/month)
- Settings view: subscription status, manage subscription link
- On subscription expiry: deprovision device tokens from relay

**6. Onboarding Extension**

- New optional step after vault setup: "Enable instant sync with Cloud Relay"
- Explain what it does: "Get notified instantly when files change on your server"
- Link to setup guide for homeserver container
- Option to skip (background refresh still works without relay)

---

## Self-Hosted Variant

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
4. **GDPR:** Device tokens are personal data. Privacy policy update needed. Data processing agreement for EU users?

---

## Troubleshooting References

- Relay/API/operator troubleshooting: [troubleshooting.md](troubleshooting.md)
- notify container setup and doctor mode: [../notify/README.md](../notify/README.md)
