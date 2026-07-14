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
- Config in a non-standard place? `curl -fsSL https://vaultsync.eu/notify.sh | SYNCTHING_CONFIG=/path/to/config.xml sh` — the variable must prefix `sh` (the installer), not `curl`. Synology/QNAP/Unraid host layouts are probed automatically.
- The script contains nothing user-specific — identity comes from your own Syncthing's Device ID at runtime.

**Windows** — one step too, in PowerShell ([`scripts/install.ps1`](scripts/install.ps1)):

```powershell
irm https://vaultsync.eu/notify.ps1 | iex
```

It finds `config.xml` under `%LOCALAPPDATA%\Syncthing`, downloads the prebuilt helper with SHA-256 verification, registers a per-user Scheduled Task (runs hidden at logon, restarts on failure — no admin rights, no service wrapper), starts it, and ends with `--doctor`. Skeptical of `irm | iex`? Set `$env:VAULTSYNC_NOTIFY_DRYRUN = '1'` first to preview every action, or read the script. Config elsewhere? Set `$env:SYNCTHING_CONFIG` before running. Re-running upgrades the helper. Logs land in `%LOCALAPPDATA%\VaultSync\vaultsync-notify.log`.

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
  ghcr.io/psimaker/vaultsync-notify:2.0.2
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

Every `notify-v*` release ships static binaries for **linux/amd64**, **linux/arm64**, **darwin/amd64**, **darwin/arm64**, and **windows/amd64** — no Docker, no Go toolchain. The [one-step installer](#-one-step-setup) downloads and verifies these automatically on Linux (systemd service) and macOS (launchd agent); grab them manually from the [releases page](https://github.com/psimaker/vaultsync/releases) for anything else, and check the download against the release's `SHA256SUMS`. A missing checksum asset or local SHA-256 implementation aborts installation rather than accepting an unverified binary.

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
| `SYNCTHING_CONFIG` | No | standard locations | Explicit path to `config.xml`. When unset, standard per-platform, container and NAS host paths are probed (incl. `/var/syncthing/config/config.xml`, `/config/config.xml`, `/var/packages/syncthing/…` on Synology, `/share/*/.qpkg/…` on QNAP, `/mnt/user/appdata/syncthing/…` on Unraid). |
| `STARTUP_ANNOUNCE` | No | `true` | Send one wake-up on startup so the app self-activates. Set `false` to suppress it — change-driven delivery still works. |
| `SYNCTHING_CONFIG_WAIT_SECONDS` | No | `60` | First boot: wait up to this many seconds for Syncthing to write `config.xml` before exiting. `0` disables the wait (fail fast). |
| `DEBOUNCE_SECONDS` | No | `5` | Wait after the last event before triggering. Batches rapid changes into one push. |
| `WATCHED_FOLDERS` | No | all | Comma-separated Syncthing folder IDs to watch. Empty = all. |
| `STALE_RETRIGGER_SECONDS` | No | `21600` (6 h) | While a peer still needs data, re-send a wake-up on this cadence. Recovers phones that missed a push (APNs silent pushes expire after ~1 h) without waiting for the next vault change. `0` disables. |
| `VAULTSYNC_DIAGNOSTICS_CONFIG` | No | — | Absolute read-only runtime JSON. Diagnostics starts only when this and `VAULTSYNC_DIAGNOSTICS_STATE` are both present; use only the supported explicit Docker script. |
| `VAULTSYNC_DIAGNOSTICS_STATE` | No | — | Absolute separate mode-0700 writable state directory. Supplying only one diagnostics value is a fatal configuration error. |

> **NAS users (the common footgun).** `config.xml` is mode `0600`, so the helper must run as the uid that owns it or it can't read the key (you'll get a clear permission error). The `1000` default fits the official `syncthing/syncthing` image; set `PUID`/`PGID` for others — linuxserver = `911`, Unraid = `99:100`, Synology runs as the `syncthing` user. Synology/QNAP/Unraid host paths are probed automatically; set `SYNCTHING_CONFIG` only when your `config.xml` lives somewhere unusual.

**First boot is briefly noisy — that's expected.** On a fresh `docker compose up`, the helper can start before Syncthing has written `config.xml`. It waits up to `SYNCTHING_CONFIG_WAIT_SECONDS`, then (if still missing) exits and `restart: unless-stopped` retries until the file exists — a few noisy seconds, then it settles. The helper also needs Syncthing running: `docker compose up vaultsync-notify` alone (empty volume, no Syncthing) finds no config.

</details>

---

## 🩺 Diagnostics

### Opt-in helper runtime (helper-first; app support not yet released)

The source tree now connects the reviewed D022–D024 foundations to a local
helper runtime, but only behind two explicit operator-supplied values:
`VAULTSYNC_DIAGNOSTICS_CONFIG` and `VAULTSYNC_DIAGNOSTICS_STATE`. With neither
value, the helper opens no diagnostics listener, creates no state or credential,
and changes no folder, trust, namespace, artifact, Syncthing setting, or Relay
request. The ordinary one-line installers, PowerShell installer, bootstrap
script, and Docker Compose set neither value, so existing-user upgrades remain
Trigger-v1-only.

The configured runtime uses TLS 1.3 with a QR-delivered SPKI pin and exact
Ed25519 application signatures. Its URLs are fixed; identifiers and paths never
enter URLs or logs. Pairing, namespace creation, lifecycle rotation/revocation,
and trust all require explicit app and local-operator actions. Capability and
every operation recheck the exact local folder mode, pause state, current
expanded Syncthing ignores, pinned Device ID before and after those reads,
authenticated namespace, immutable authorization, exact ephemeral
host-path/mount-identity digest, keys, epochs, bindings, limits, and signatures.
Any Syncthing ignore-parser error fails closed.
Folder and expanded-ignore responses also have fixed byte, count, and
pattern-length ceilings.
For this supported host-bind row the Syncthing API must be an exact local
`http://127.0.0.1:<port>` endpoint. Syncthing, Relay, and local operator HTTP
clients reject redirects. It never
edits `.stignore`, shares, peers, discovery, or Syncthing configuration.
The explicitly selected diagnostics listener uses an unprivileged port from
1024 through 65535; no bind capability is added.
Every remote, operator, namespace, lifecycle, and admin mutation is serialized
through one protected cross-process lock, including commands run with
`docker exec`.

Only rootful Docker Engine on an explicitly confirmed standard Linux host is a
supported diagnostics package. The separate
[`scripts/diagnostics-docker.sh`](scripts/diagnostics-docker.sh) flow uses a
temporary exact parent bind solely for confirmed namespace creation, then
restarts with only the exact existing `VaultSync Diagnostics` child read/write,
a separate state bind, read-only runtime/config.xml files, read-only container
root, all capabilities dropped, and `no-new-privileges`. It resolves the helper
image to an immutable local content ID before deployment and rejects a
root-owned Syncthing config instead of running the helper as root. The installer
pins the source directory identity before the temporary bind and injects only a
SHA-256 mount-binding digest at runtime; the raw host path is not stored or
logged. Named volumes, their backing paths and subpaths, rootless Docker, remote
Docker daemons/contexts, non-Unix Docker endpoints, remote/NAS/FUSE storage,
Docker Desktop, WSL, systemd binaries, macOS, and Windows remain
diagnostics-unsupported.
The runtime rejects non-Linux activation and requires the operator config to be
owner-only mode `0400` with a single filesystem link. Before namespace creation,
the installer displays the exact resulting path and separately requires
acknowledgement of that path and possible peer/backup/version/conflict/tombstone
retention.

The installer has only forward, explicit crash completion. If namespace
creation and its protected root record became durable before the mount config
was written, rerunning `diagnostics-docker.sh enable` can resume only that exact
authenticated root after all Device/folder/ignore, source-inode, state, digest,
key/epoch, signature, and fixed-layout checks. Recovery mode creates nothing and
never adopts an unregistered or conflicting directory. Exact D023 authorization
messages are likewise idempotent after their file and state became durable,
even when the original signed candidate has since expired;
helper/TLS rotation remains unavailable while a registered root lacks its exact
authorization.

Each app installation pairs and authorizes independently. Another installation
may join an existing authenticated namespace only through its own signed D022
and D023 flow; there is no second namespace creation or transfer of trust or
identity. Revoked immutable authorization records remain historical. A later
namespace-wide helper-key manifest does not rewrite them, while every active
installation still needs a fresh exact D023 authorization epoch before it can
resume operations. Manifest rotation also repeats the pinned Device ID,
folder/ignore, root, and exact mount-binding preflight for every affected
namespace before the helper key becomes current.
A shared helper-key or TLS-pin rotation withholds signed capability success from
an early installation until all required proposed-state confirmations permit the
global commit; the early installation sees unavailable and then retries.

This is helper readiness, not app evidence. The released app does not call these
paths. No app-authored runtime request has established product upload evidence,
no response has passed a fresh post-authorization iPhone `ItemFinished`, and no
same-chain roundtrip exists. Upload, download, and roundtrip therefore remain
unset; cleanup remains separate from evidence. See
[`../docs/helper-runtime-packaging-readiness.md`](../docs/helper-runtime-packaging-readiness.md)
for the exact support, privacy, compatibility, and rollback boundary.

**Doctor mode** — preflight checks with actionable failures (Compose reads `.env` for you):

```bash
docker compose run --rm vaultsync-notify --doctor
```

Validates: Syncthing API reachable · API key valid · Device ID readable · relay health reachable · trigger endpoint sane. Each check retries with per-attempt timeouts to ride out transient jitter. It also diagnoses **peer state** — no remote device connected, or devices connected but no folder shared with them — as `WARN` lines (`WARN <check name>`, then an indented reason with the Syncthing-side fix) that never fail the doctor: an offline peer is everyday life, not a setup error.

**Runtime healthcheck** — the image's `HEALTHCHECK` runs `vaultsync-notify --healthcheck`, validating real readiness (Syncthing API, credentials, Device ID, relay health), not just process liveness. Peer state is deliberately excluded here: a legitimately offline peer must never flip the container to unhealthy.

**Version** — `vaultsync-notify --version` prints the installed helper version (needs no configuration). The installer shows old → new on upgrades. Docker installs pull the reviewed `2.0.2` tag and run only its resolved local content ID; failed pulls do not fall back to a stale tag. Binary installs select the newest published `notify-v*` release, require its `SHA256SUMS`, replace the binary, and restart the service. Future Docker upgrades require another reviewed version tag or an explicit `VAULTSYNC_NOTIFY_IMAGE` override.

**Diagnostics pairing comparison** — after the app has authenticated the
pending helper acceptance, run `diagnostics-docker.sh list` locally. The
pending row includes the exact D022 `transcript=` fingerprint that must match
the value in the app before activation. It is local comparison output, not a
credential; do not redirect it into service logs or support bundles. Active
rows omit it.

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

`iPhone → server` stays foreground-first on iOS: open VaultSync to push local edits back. Background refresh may help opportunistically. The released product has no controlled helper upload path: the optional, unpublished diagnostics endpoints above have no released app caller and set no upload evidence. See the [Cloud Relay spec](../docs/relay-spec.md) for the protocol.

---

## 🛠️ Build from source

```bash
cd notify
go build -o vaultsync-notify        # native binary
docker build -t vaultsync-notify .  # Docker image
```

---

## Privacy & license

Only the Syncthing Device ID is sent to the relay, as a routing identifier. No file names, folder names, file sizes, or other metadata leave your server.

Operational logs contain fixed states, error categories, status codes, bounded counts, and durations. They do not contain Device IDs, folder IDs, event markers, local or Relay endpoint URLs, config paths, Syncthing API keys, pairing/namespace values, or raw request/response bodies. The default Trigger-v1 helper keeps the API key and watched-folder selection in process memory. Only the explicitly configured diagnostics runtime creates its separate protected credential and namespace-mapping state described above; that state is never synchronized or sent to Cloud Relay.

Licensed [MPL-2.0](../LICENSE).
