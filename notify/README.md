# vaultsync-notify

Lightweight sidecar container that watches your Syncthing instance for file changes and sends a wake-up signal to the VaultSync Cloud Relay. This triggers an instant push notification to your iOS device, so VaultSync can sync immediately instead of waiting for the next background refresh.

## How It Works

1. Subscribes to Syncthing's `/rest/events` API via long-polling.
2. Filters for real change indicators from Syncthing (`LocalIndexUpdated`, `FolderCompletion` with outstanding remote work).
3. Debounces rapid changes (default: 5 seconds) into a single notification.
4. Reads this Syncthing instance's Device ID automatically from `/rest/system/status`.
5. Sends a wake-up signal to `relay.vaultsync.eu` — no file content or metadata leaves your server.
6. The relay forwards a silent push to your iOS device via APNs.

> Cloud Relay accelerates **server → iPhone** sync only. For **iPhone → server**, open VaultSync (see [Product Scope](#product-scope)).

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

Run preflight diagnostics with actionable failures. With Docker (the usual setup), compose reads `.env` for you:

```bash
docker compose run --rm vaultsync-notify --doctor
```

If you built the binary from source, run `./vaultsync-notify --doctor` with the `.env` values exported into your shell.

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

Compose reads its values from `.env` — see the [Environment Variables](#environment-variables) table below.

## Runtime Healthcheck

The Docker image's `HEALTHCHECK` runs `vaultsync-notify --healthcheck`, validating real readiness — Syncthing API, credentials, Device ID, and relay health — not just process liveness.

## Troubleshooting

- Wrong API key (`401/403`): [docs/troubleshooting.md#wrong-syncthing-api-key-in-notify](../docs/troubleshooting.md#wrong-syncthing-api-key-in-notify)
- Relay connectivity failures: [docs/troubleshooting.md#relay-unreachable](../docs/troubleshooting.md#relay-unreachable)
- iOS push token/provisioning issues: [docs/troubleshooting.md#apns-not-registered](../docs/troubleshooting.md#apns-not-registered)
- End-to-end issue matrix: [docs/troubleshooting.md](../docs/troubleshooting.md)

## Environment Variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `SYNCTHING_API_URL` | No | auto / `http://localhost:8384` | Syncthing REST API URL. Auto-detected from `config.xml` (`<gui><address>`) when unset. Set explicitly when Syncthing runs in a sibling container (e.g. `http://syncthing:8384`). |
| `SYNCTHING_API_KEY` | No | auto-detected | Syncthing API key. Auto-detected from `config.xml` (`<gui><apikey>`) when unset — no need to copy it from the Web UI. Set explicitly to override. |
| `SYNCTHING_CONFIG` | No | standard locations | Explicit path to Syncthing's `config.xml` for auto-detection. When unset, standard per-platform and container paths are probed (incl. `/var/syncthing/config/config.xml` and `/config/config.xml`). |
| `RELAY_URL` | **Yes** | — | Central relay URL (`https://relay.vaultsync.eu`). Has no built-in default on purpose, so the helper never triggers a relay you didn't choose. `docker-compose.yml` and the in-app setup command supply it for you. |
| `DEBOUNCE_SECONDS` | No | `5` | Seconds to wait after the last event before sending a trigger. Batches rapid changes into one push. |
| `WATCHED_FOLDERS` | No | all | Comma-separated Syncthing folder IDs to watch. If unset/empty, watches all folders. |

> **Auto-detection (no API key to copy).** When `SYNCTHING_API_KEY`/`SYNCTHING_API_URL` are unset, the helper reads them directly from Syncthing's `config.xml`. Running next to Syncthing on the same host, the only thing you supply is `RELAY_URL`. In Docker, share Syncthing's config dir into the helper **read-only** and run it as the same uid that owns `config.xml` (the images default to `1000`); `config.xml` is mode `0600`, so a mismatched uid cannot read it (you'll get a clear permission error telling you so). Explicit env always wins over auto-detection.
>
> **First boot.** On a brand-new `docker compose up`, the helper may start before Syncthing has written `config.xml`; it then exits and `restart: unless-stopped` retries until the file exists (a few noisy seconds, then it settles). The helper also needs Syncthing to be running — `docker compose up vaultsync-notify` alone (empty volume, no Syncthing) will not find a config.

## Syncthing Events

The container watches these event types:

- **LocalIndexUpdated** — A direct change happened on the homeserver itself
- **FolderCompletion** with `needItems > 0` or `needBytes > 0` — a remote peer is behind and should be woken

All other events (device connections, config changes, and so on) are ignored.

## Product Scope

Cloud Relay is intended to accelerate `server -> iPhone` sync by waking VaultSync when the homeserver has outgoing changes.

`iPhone -> server` remains foreground-first on iOS. Opening VaultSync is the reliable way to push local iPhone edits back to the homeserver. iOS background refresh may still help opportunistically, but `vaultsync-notify` does not expose a separate public upload endpoint as part of the standard product path.

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
