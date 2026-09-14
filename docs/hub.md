# VaultSync Hub

The Hub is the simplest way to run VaultSync: one server or NAS keeps every
vault, computers pair with a short code (the iPhone app pairs by Device ID
until code/QR pairing ships), and nobody has to see a Syncthing Web UI. "Bring your own Syncthing" keeps working exactly as before — the Hub is
an addition, not a replacement.

## Set up a Hub (once)

On a Linux server or NAS with Docker:

```sh
curl -fsSL https://vaultsync.eu/setup.sh | sh
```

Choose **2) Hub**. The setup

1. checks Docker (with the compose plugin) — it prints the install one-liner if missing,
2. creates `/srv/vaultsync/` (override with `VAULTSYNC_HUB_DIR`) with three
   plain directories: `vaults/` (your notes), `syncthing/` (identity, config,
   database), `hub/` (pairing state),
3. starts three containers — the Hub's own Syncthing, the `hub` coordinator and
   the `notify` wake-up helper for VaultSync on iPhone,
4. prints the Hub's **Device ID** and a **pairing code**.

Everything lives under that one directory as plain files. Back it up like any
other directory; move it, and the Hub moves with it.

Skeptical of `curl | sh`? Append `-s -- --dry-run` to see every step first, or
read `hub/scripts/setup.sh`.

### Ports

| Port | Purpose | Exposure |
|---|---|---|
| 22000 tcp+udp | sync protocol | forward on your router if devices should sync from outside the home network (optional — Syncthing relays work without it) |
| 21027 udp | Syncthing local discovery | LAN |
| 8390 tcp+udp | pairing and Hub discovery | listens on all interfaces, answers only private/loopback/link-local source addresses — **never forward it** |
| 8384 | Syncthing Web UI | loopback only (`ssh -L 8384:127.0.0.1:8384 hub`) |

If port 22000 is already taken (an existing Syncthing on the same machine), the
setup uses 22001 and says so.

## Pair a device

A pairing code looks like `TULIP-ANCHOR-42`. It is valid for 24 hours, on the
same network as the Hub, and is locked after five wrong attempts. Get a fresh
one any time:

```sh
cd /srv/vaultsync && docker compose exec hub vaultsync-hub code
```

**Computer with Syncthing installed** — run the same link and choose
**1) Obsidian device**, then enter the code. The setup finds the Hub on your
network, asks which vault to join (or creates a new one) and either accepts the
share into a directory you name (`--path`) or leaves it for you to accept in
Syncthing. Non-interactive:

```sh
vaultsync-hub pair --code TULIP-ANCHOR-42 --vault "Notes" --create --path ~/Obsidian/Notes
```

**iPhone** — today: add the Hub in VaultSync by its Device ID (shown by the
setup and by `vaultsync-hub status`), then accept the vault the Hub shares.
Pairing by code or QR from the app follows in a later release.

## What the Hub guarantees

- **Nothing is ever merged automatically.** A vault is created only in a fresh,
  empty directory under `vaults/`. A directory that already holds files becomes
  a vault only when you say so on the Hub's own shell: `vaultsync-hub vault adopt NAME`.
  On a device, `--path` must be empty unless the Hub's vault is brand new.
- **The Hub never deletes or moves your files.** It never changes a folder's
  path, never removes a folder, never repoints anything. Conflict copies are
  kept without limit. Overwritten and deleted versions are kept for 30 days in
  `.stversions` inside each vault, then cleaned by Syncthing's versioner.
- **Two vaults never overlap on disk.** Same rule as in the app.
- **Pairing is local and authenticated.** The code is turned into a session key
  with a password-authenticated key exchange (SPAKE2). Someone listening on the
  network learns nothing that lets them guess the code offline, and someone
  without the code cannot impersonate the Hub or a device. Requests from
  non-private addresses are refused outright, but that is a backstop, not a
  reason to forward the port.

## Everyday commands

```sh
cd /srv/vaultsync
docker compose exec hub vaultsync-hub status          # ID, vaults, devices, pairing state
docker compose exec hub vaultsync-hub code            # new pairing code
docker compose exec hub vaultsync-hub vault list
docker compose exec hub vaultsync-hub vault create "Work"
docker compose exec hub vaultsync-hub vault adopt "Old Notes"   # existing dir in vaults/
docker compose pull && docker compose up -d           # update
```

## Troubleshooting

- **"no Hub answered"** on a device: device and Hub must be on the same network
  for pairing (afterwards they sync from anywhere). Pass `--hub HOST:8390` to
  skip discovery. The `hub` container runs with host networking on purpose —
  Docker Desktop on macOS/Windows cannot do that, which is why the Hub targets
  Linux.
- **"the pairing code was locked"**: issue a new one with `vaultsync-hub code`.
- **"directory already holds files"**: that is the merge guard. Use
  `vault adopt` on the Hub, or an empty directory on the device.
- **Existing Syncthing on the same machine**: the Hub runs its own instance side
  by side (port 22001). Adopting the existing instance is intentionally not
  automated; see the FAQ in the README.
