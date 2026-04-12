# vaultsync-notify

Lightweight sidecar container that watches your Syncthing instance for file changes and sends a wake-up signal to the VaultSync Cloud Relay. This triggers an instant push notification to your iOS device, so VaultSync can sync immediately instead of waiting for the next background refresh.

## How It Works

1. Subscribes to Syncthing's `/rest/events` API via long-polling.
2. Filters for real change indicators from Syncthing (`LocalIndexUpdated`, `FolderCompletion` with outstanding remote work).
3. Debounces rapid changes (default: 5 seconds) into a single notification.
4. Optionally sends a sparse periodic wake-up poke for `iPhone -> server` catch-up when no server-side change was seen yet.
5. Reads this Syncthing instance's Device ID automatically from `/rest/system/status`.
6. Sends a wake-up signal to `relay.vaultsync.eu` — no file content or metadata leaves your server.
7. The relay forwards a silent push to your iOS device via APNs.
8. Optionally exposes a direct-upload endpoint for iPhone-originated Markdown changes.

## Quick Start (One Command)

From the `notify/` directory:

```bash
./scripts/bootstrap.sh
```

What bootstrap does:

1. Auto-detects Syncthing `config.xml` in common Linux/macOS locations.
2. Extracts the Syncthing API key from `config.xml` automatically.
3. Infers a default Syncthing API URL from Syncthing GUI config.
4. Validates Syncthing API access and relay health with retries/timeouts.
5. Writes `notify/.env` with secure permissions.
6. Runs built-in `--doctor` checks (when local binary or Go toolchain is available).
7. Optionally starts `docker compose up -d vaultsync-notify`.

If auto-detection fails, set `SYNCTHING_CONFIG=/path/to/config.xml` and rerun bootstrap.

## Doctor Mode

Run preflight diagnostics with actionable failures:

```bash
cd notify
set -a
. ./.env
set +a
./vaultsync-notify --doctor
```

Doctor validates:

- Syncthing API reachable
- API key valid
- Device ID readable
- Relay health endpoint reachable
- Relay trigger endpoint response sanity

Each check uses retries and per-attempt timeouts to avoid false negatives during transient network jitter.

## Advanced / Manual Docker Setup

Manual setup remains fully supported for operators who prefer explicit control.

```bash
cd notify
cp .env.example .env
# edit .env with your values
docker compose up -d vaultsync-notify
```

Compose values are read from `.env`:

- `SYNCTHING_API_URL` (default fallback: `http://syncthing:8384`)
- `SYNCTHING_API_KEY` (required)
- `RELAY_URL` (default fallback: `https://relay.vaultsync.eu`)
- `DEBOUNCE_SECONDS` (default fallback: `5`)
- `POKE_INTERVAL_MINUTES` (default fallback: `0`, disabled)
- `UPLOAD_LISTEN_ADDR` (optional; enables the direct-upload endpoint)
- `UPLOAD_PORT_PUBLISH` (optional Docker publish rule for the upload endpoint)
- `UPLOAD_ROOT_DIR` (optional; Syncthing folder root where uploaded Markdown files are written)
- `UPLOAD_AUTH_TOKEN` (required when upload endpoint is enabled)
- `WATCHED_FOLDERS` (optional; empty means all folders)

## Runtime Healthcheck

The Docker image includes a real readiness healthcheck:

```text
vaultsync-notify --healthcheck
```

The healthcheck validates dependency readiness (Syncthing API + credentials + Device ID + relay health) instead of only checking process liveness.

## Troubleshooting

- Wrong API key (`401/403`): [docs/troubleshooting.md#wrong-syncthing-api-key-in-notify](../docs/troubleshooting.md#wrong-syncthing-api-key-in-notify)
- Relay connectivity failures: [docs/troubleshooting.md#relay-unreachable](../docs/troubleshooting.md#relay-unreachable)
- iOS push token/provisioning issues: [docs/troubleshooting.md#apns-not-registered](../docs/troubleshooting.md#apns-not-registered)
- End-to-end issue matrix: [docs/troubleshooting.md](../docs/troubleshooting.md)

## Environment Variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `SYNCTHING_API_URL` | Yes | — | Syncthing REST API URL (e.g. `http://syncthing:8384` or `http://localhost:8384`) |
| `SYNCTHING_API_KEY` | Yes | — | Syncthing API key (Settings > GUI > API Key in Syncthing Web UI) |
| `RELAY_URL` | Yes | — | Central relay URL (`https://relay.vaultsync.eu`) |
| `DEBOUNCE_SECONDS` | No | `5` | Seconds to wait after the last event before sending a trigger. Batches rapid changes into one push. |
| `POKE_INTERVAL_MINUTES` | No | `0` | Optional periodic silent-push wake-up. Use this to prompt background upload checks from the iPhone even when no server-side event has happened yet. `0` disables the workaround. |
| `UPLOAD_LISTEN_ADDR` | No | disabled | Optional bind address for the direct-upload endpoint (for example `:8091`). |
| `UPLOAD_PORT_PUBLISH` | No | `127.0.0.1:8091:8091` | Docker port publish rule for the experimental upload endpoint. Replace with a public binding or front it with a reverse proxy if the iPhone must reach it directly. |
| `UPLOAD_ROOT_DIR` | No | disabled | Absolute or relative filesystem path where uploaded Markdown files are stored. In Docker Compose this should usually be `/var/syncthing/obsidian` so uploads land in the shared Syncthing volume. |
| `UPLOAD_AUTH_TOKEN` | No | disabled | Bearer token required by the experimental direct-upload endpoint. Must be set when the upload endpoint is enabled. |
| `WATCHED_FOLDERS` | No | all | Comma-separated Syncthing folder IDs to watch. If unset/empty, watches all folders. |

## Syncthing Events

The container watches these event types:

- **LocalIndexUpdated** — A direct change happened on the homeserver itself
- **FolderCompletion** with `needItems > 0` or `needBytes > 0` — a remote peer is behind and should be woken

All other events (device connections, config changes, and so on) are ignored.

When `POKE_INTERVAL_MINUTES` is enabled, `vaultsync-notify` also sends a periodic wake-up if no recent trigger succeeded and there is no pending change-trigger already queued. This is intended as a best-effort `iPhone -> server` workaround, not as an instant sync guarantee.

## Direct Upload Endpoint

If `UPLOAD_LISTEN_ADDR`, `UPLOAD_ROOT_DIR`, and `UPLOAD_AUTH_TOKEN` are all set, `vaultsync-notify` also serves:

```text
PUT /api/v1/upload
Authorization: Bearer <token>
X-VaultSync-Relative-Path: brain/path/to/file.md
X-VaultSync-Device-ID: <optional-device-id>
Content-Type: text/markdown
```

The request body is written directly into `UPLOAD_ROOT_DIR/<relative-path>`.

Current scope and guardrails:

- Only Markdown files (`.md`) are accepted.
- Path traversal is rejected.
- Empty uploads are allowed because freshly created notes can legitimately be zero-byte files.
- This `iPhone -> server` lane improves reliability under iOS background limits, but it should still be treated as best effort rather than an instant-sync guarantee.

Operational note:

- The notify container must see the same Syncthing data volume as the Syncthing container. The bundled `docker-compose.yml` mounts `syncthing-data:/var/syncthing` into both services so `UPLOAD_ROOT_DIR=/var/syncthing/obsidian` writes into the live Syncthing vault.

## Building from Source

```bash
# Native binary
cd notify
go build -o vaultsync-notify

# Docker image
docker build -t vaultsync-notify .
```

## Privacy

Only the Syncthing Device ID is sent to the relay as an identifier for push routing. No file names, folder names, file sizes, or any other metadata leaves your server.

## License

MPL-2.0 — see [LICENSE](../LICENSE) in the repository root.
