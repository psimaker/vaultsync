# vaultsync-notify

Small sidecar that watches your Syncthing instance and sends a **wake-up signal** to the VaultSync Cloud Relay when your server changes. The relay forwards a silent APNs push, so VaultSync syncs promptly instead of waiting for the next background refresh. No file content or metadata leaves your server — only a Device ID.

> Cloud Relay accelerates **server → iPhone** only. For **iPhone → server**, open VaultSync (see [Product scope](#product-scope)).

---

## 🚀 Quick start — Docker Compose (key-free)

Runs Syncthing and the helper together. The helper reads the Syncthing API key from the shared `config.xml`, so there's **no key to copy** — the only value you supply is `RELAY_URL`.

```bash
cd notify
cp .env.example .env        # RELAY_URL defaults to the production relay
docker compose up -d
```

The moment the helper starts it sends one wake-up, and VaultSync flips to **Cloud Relay active** on its own.

> A plain `docker compose up` sends one **real** wake-up to production — intended for subscribers. Testing locally? Override `RELAY_URL` to a mock first (see the header of [`docker-compose.yml`](docker-compose.yml)).

---

## 🖥️ Run next to a host Syncthing

Syncthing running **natively on the host** (not in Compose)? Use either path — both auto-detect the key from `config.xml`.

**A. Paste-and-go `docker run`** (the command VaultSync shows after you subscribe):

```bash
docker run -d --name vaultsync-notify --restart unless-stopped \
  --network host \
  -v /PATH/TO/syncthing:/config:ro \
  -e SYNCTHING_CONFIG=/config/config.xml \
  -e RELAY_URL=https://relay.vaultsync.eu \
  ghcr.io/psimaker/vaultsync-notify:latest
```

Replace `/PATH/TO/syncthing` with your Syncthing config folder (often `~/.local/state/syncthing` or `~/.config/syncthing`). Permission error? Add `-u <uid>:<gid>` for the user that owns `config.xml`.

**B. Guided `bootstrap.sh`** — detects `config.xml`, validates Syncthing + relay connectivity, writes a Compose-safe `notify/.env`, and runs `--doctor`. It does **not** start the Compose stack.

```bash
cd notify && ./scripts/bootstrap.sh
```

If detection fails, set `SYNCTHING_CONFIG=/path/to/config.xml` and rerun.

---

## ⚙️ Environment variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `RELAY_URL` | **Yes** | — (binary) | Relay endpoint. No built-in default on purpose, so the helper never wakes a relay you didn't choose. `docker-compose.yml` and the in-app command set it to `https://relay.vaultsync.eu`. |
| `SYNCTHING_API_KEY` | No | auto-detected | Read from `config.xml` (`<gui><apikey>`) when unset — no need to copy it from the Web UI. Set to override. |
| `SYNCTHING_API_URL` | No | auto / `http://localhost:8384` | Read from `config.xml` (`<gui><address>`) when unset. Set when Syncthing is a sibling container (e.g. `http://syncthing:8384`). |
| `SYNCTHING_CONFIG` | No | standard locations | Explicit path to `config.xml`. When unset, standard per-platform and container paths are probed (incl. `/var/syncthing/config/config.xml`, `/config/config.xml`). Needed for Synology/QNAP. |
| `STARTUP_ANNOUNCE` | No | `true` | Send one wake-up on startup so the app self-activates. Set `false` to suppress it — change-driven delivery still works. |
| `SYNCTHING_CONFIG_WAIT_SECONDS` | No | `60` | First boot: wait up to this many seconds for Syncthing to write `config.xml` before exiting. `0` disables the wait (fail fast). |
| `DEBOUNCE_SECONDS` | No | `5` | Wait after the last event before triggering. Batches rapid changes into one push. |
| `WATCHED_FOLDERS` | No | all | Comma-separated Syncthing folder IDs to watch. Empty = all. |

> **NAS users (the common footgun).** `config.xml` is mode `0600`, so the helper must run as the uid that owns it or it can't read the key (you'll get a clear permission error). The `1000` default fits the official `syncthing/syncthing` image; set `PUID`/`PGID` for others — linuxserver = `911`, Unraid = `99:100`, Synology runs as the `syncthing` user. Synology/QNAP also need `SYNCTHING_CONFIG` pointed at the real `config.xml`.

<details>
<summary>First boot is briefly noisy — that's expected</summary>

On a fresh `docker compose up`, the helper can start before Syncthing has written `config.xml`. It waits up to `SYNCTHING_CONFIG_WAIT_SECONDS`, then (if still missing) exits and `restart: unless-stopped` retries until the file exists — a few noisy seconds, then it settles. The helper also needs Syncthing running: `docker compose up vaultsync-notify` alone (empty volume, no Syncthing) finds no config.
</details>

---

## 🩺 Diagnostics

**Doctor mode** — preflight checks with actionable failures (Compose reads `.env` for you):

```bash
docker compose run --rm vaultsync-notify --doctor
```

Validates: Syncthing API reachable · API key valid · Device ID readable · relay health reachable · trigger endpoint sane. Each check retries with per-attempt timeouts to ride out transient jitter.

**Runtime healthcheck** — the image's `HEALTHCHECK` runs `vaultsync-notify --healthcheck`, validating real readiness (Syncthing API, credentials, Device ID, relay health), not just process liveness.

---

## 🔧 Troubleshooting

| Symptom | Fix |
|---|---|
| `401`/`403` / permission reading the key | [Wrong / unreadable Syncthing API key](../docs/troubleshooting.md#wrong-syncthing-api-key-in-notify) |
| Relay timeouts, DNS, connection refused | [Relay unreachable](../docs/troubleshooting.md#relay-unreachable) |
| Subscribed but no wake-ups | [APNs not registered](../docs/troubleshooting.md#apns-not-registered) |
| Anything else | [End-to-end issue matrix](../docs/troubleshooting.md) |

---

## 📡 Product scope

Cloud Relay accelerates `server → iPhone` by waking VaultSync when the homeserver has outgoing changes. The container watches two Syncthing event types and ignores everything else:

- **`LocalIndexUpdated`** — a direct change on the homeserver itself.
- **`FolderCompletion`** with `needItems > 0` or `needBytes > 0` — a remote peer is behind and should be woken.

`iPhone → server` stays foreground-first on iOS: open VaultSync to push local edits back. Background refresh may help opportunistically, but the helper exposes no upload path. See the [Cloud Relay spec](../docs/relay-spec.md) for the protocol.

---

## 🛠️ Build from source

```bash
cd notify
go build -o vaultsync-notify        # native binary
docker build -t vaultsync-notify .  # Docker image
```

---

## Privacy & license

Only the Syncthing Device ID is sent to the relay, as a routing identifier. No file names, folder names, file sizes, or other metadata leave your server. Licensed [MPL-2.0](../LICENSE).
