# vaultsync-notify

Lightweight sidecar container that watches your Syncthing instance for file changes and sends a wake-up signal to the VaultSync Cloud Relay. This triggers an instant push notification to your iOS device, so VaultSync can sync immediately instead of waiting for the next background refresh.

## How It Works

1. Subscribes to Syncthing's `/rest/events` API via long-polling.
2. Filters for file-change events (`ItemFinished`, `StateChanged`).
3. Debounces rapid changes (default: 5 seconds) into a single notification.
4. Reads this Syncthing instance's Device ID automatically from `/rest/system/status`.
5. Sends a wake-up signal to `relay.vaultsync.eu` — no file content or metadata leaves your server.
6. The relay forwards a silent push to your iOS device via APNs.

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
| `WATCHED_FOLDERS` | No | all | Comma-separated Syncthing folder IDs to watch. If unset/empty, watches all folders. |

## Syncthing Events

The container watches these event types:

- **ItemFinished** — A single file was synced (downloaded or uploaded)
- **StateChanged** — A folder's state changed (for example `syncing` -> `idle`)

All other events (device connections, config changes, and so on) are ignored.

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
