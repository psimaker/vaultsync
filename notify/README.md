# vaultsync-notify

Small sidecar that watches your Syncthing instance and sends a **wake-up signal** to the VaultSync Cloud Relay when your server changes. The relay forwards a silent APNs push, so VaultSync syncs promptly instead of waiting for the next background refresh. No file content or metadata leaves your server — only a Device ID.

> Cloud Relay accelerates **server → iPhone** only. For **iPhone → server**, open VaultSync (see **Product scope** below).

---

## ⚡ One-step setup

On the machine that runs Syncthing (server, NAS, or an always-on computer), run this one line — **it is the entire setup**:

<div align="center">

```bash
curl -fsSL https://vaultsync.eu/notify.sh | sh
```

**Nothing to edit, no API key to copy.**

</div>

The installer ([`scripts/install.sh`](scripts/install.sh)) finds your `config.xml`, runs the helper as the uid:gid that owns it (the #1 setup failure), and starts it — as a Docker container when Docker is available, otherwise as a prebuilt binary behind a systemd service (Linux) or launchd agent (macOS). It ends with the helper's own `--doctor` preflight, so a misconfiguration fails loudly with the fix spelled out.

The moment the helper starts it sends one wake-up, and VaultSync flips to **Cloud Relay active** on its own.

- Skeptical of `curl | sh`? Append `-s -- --dry-run` to preview every action without changing anything, or read the script first.
- Config in a non-standard place (typical on Synology/QNAP)? `curl -fsSL https://vaultsync.eu/notify.sh | SYNCTHING_CONFIG=/path/to/config.xml sh` — the variable must prefix `sh` (the installer), not `curl`.
- The script contains nothing user-specific — identity comes from your own Syncthing's Device ID at runtime.

---

## 🔧 Manual & advanced setup

The one line above is all most setups need. Prefer to run things yourself? Every path below is equivalent — pick one and expand it.

<details>
<summary><b>🐳 Docker Compose</b> — Syncthing and the helper together, key-free</summary>
<br>

Runs Syncthing and the helper together. The helper reads the Syncthing API key from the shared `config.xml`, so there's **no key to copy** — the only value you supply is `RELAY_URL`.

```bash
cd notify
cp .env.example .env        # RELAY_URL defaults to the production relay
docker compose up -d
```

> A plain `docker compose up` sends one **real** wake-up to production — intended for subscribers. Testing locally? Override `RELAY_URL` to a mock first (see the header of [`docker-compose.yml`](docker-compose.yml)).

</details>

<details>
<summary><b>🖥️ Run next to a host Syncthing</b> — paste-and-go <code>docker run</code> or guided <code>bootstrap.sh</code></summary>
<br>

Syncthing running **natively on the host** (not in Compose)? Use either path — both auto-detect the key from `config.xml`.

**A. Paste-and-go `docker run`** (the manual alternative VaultSync shows after you subscribe):

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

</details>

<details>
<summary><b>📦 Prebuilt binaries</b> — Linux, macOS, Windows; no Docker, no Go toolchain</summary>
<br>

Every `notify-v*` release ships static binaries for **linux/amd64**, **linux/arm64**, **darwin/amd64**, **darwin/arm64**, and **windows/amd64** — no Docker, no Go toolchain. The [one-step installer](#-one-step-setup) downloads and verifies these automatically on Linux (systemd service) and macOS (launchd agent); grab them manually from the [releases page](https://github.com/psimaker/vaultsync/releases) for anything else, and check the download against the release's `SHA256SUMS`.

Run manually — the only required value is `RELAY_URL`; the Syncthing key and URL are auto-detected from `config.xml`:

```bash
RELAY_URL=https://relay.vaultsync.eu ./vaultsync-notify
```

</details>

<details>
<summary><b>⚙️ Environment variables</b> — full reference, NAS permission notes</summary>
<br>

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
| `STALE_RETRIGGER_SECONDS` | No | `21600` (6 h) | While a peer still needs data, re-send a wake-up on this cadence. Recovers phones that missed a push (APNs silent pushes expire after ~1 h) without waiting for the next vault change. `0` disables. |

> **NAS users (the common footgun).** `config.xml` is mode `0600`, so the helper must run as the uid that owns it or it can't read the key (you'll get a clear permission error). The `1000` default fits the official `syncthing/syncthing` image; set `PUID`/`PGID` for others — linuxserver = `911`, Unraid = `99:100`, Synology runs as the `syncthing` user. Synology/QNAP also need `SYNCTHING_CONFIG` pointed at the real `config.xml`.

**First boot is briefly noisy — that's expected.** On a fresh `docker compose up`, the helper can start before Syncthing has written `config.xml`. It waits up to `SYNCTHING_CONFIG_WAIT_SECONDS`, then (if still missing) exits and `restart: unless-stopped` retries until the file exists — a few noisy seconds, then it settles. The helper also needs Syncthing running: `docker compose up vaultsync-notify` alone (empty volume, no Syncthing) finds no config.

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
