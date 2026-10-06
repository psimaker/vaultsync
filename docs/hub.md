# VaultSync Hub

The Hub is the simplest way to run VaultSync: one server or NAS keeps every
vault, computers and iPhones pair with a short code (or the iPhone scans its QR
code), and nobody has to see a Syncthing Web UI. "Bring your own Syncthing" keeps working exactly as before — the Hub is
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

Next to the code, `code` prints a QR code for VaultSync on iPhone and the same
pairing link as text: `vaultsync://pair?code=TULIP-ANCHOR-42&hub=192.168.1.20%3A8390`.
`hub=` is the Hub's address on your network and lets the iPhone skip the
search. It is there only when the Hub knows that address — the address of its
default route, when that is a private one — or when you name it: pass
`--address 192.168.1.20` when the link has no `hub=` or the wrong one (a VPN,
say), or `--no-qr` when your terminal cannot draw the QR code.

**Mac or Linux computer, nothing else installed** — the VaultSync desktop
agent `vaultsync` brings its own Syncthing (see
[Desktop agent](#desktop-agent-macos-and-linux) below): run `vaultsync setup`
and enter the code.

**Computer with Syncthing installed** — run the same link and choose
**1) Obsidian device**, then enter the code. The setup finds the Hub on your
network, asks which vault to join (or creates a new one) and either accepts the
share into a directory you name (`--path`) or leaves it for you to accept in
Syncthing. Non-interactive:

```sh
vaultsync-hub pair --code TULIP-ANCHOR-42 --vault "Notes" --create --path ~/Obsidian/Notes
```

**iPhone** — in VaultSync, open Devices → + → **Add Hub** (or **Add Hub** in
setup). Type the code, or tap **Scan QR instead** — scanning the QR code with
the iPhone's Camera app opens the same screen, already filled in. The app finds
the Hub on your network (the iPhone must be on the same network; allow
VaultSync under Settings → Privacy & Security → Local Network), asks which
vault to sync, and the Hub shares it. The vault is then accepted like every
other share: into its own folder inside your Obsidian folder, and never into a
folder that already holds files without asking you — that decision waits under
**Pending Shares** on the Sync tab. A link or QR code only fills in the screen;
pairing always takes your tap, and only with an address on your local network.
Your Hub's vault list is offered as it is: to start a new vault from the
iPhone's notes, create it on the Hub first (`vaultsync-hub vault create NAME`)
and choose it — the iPhone asks before it combines its folder of the same name
with the Hub's. When every vault on the Hub is already on the iPhone, Add Hub
offers **Reconnect with Hub** instead (it re-adds the Hub as a device if it was
removed). The Device ID way (Add Device) keeps working; a Hub's QR code scanned
there offers **Add Hub** instead.

## Desktop agent (macOS and Linux)

`vaultsync` is one program that turns a Mac or a Linux computer into a
VaultSync device: it installs its own Syncthing, pairs with your Hub by code
and keeps your Obsidian vaults in sync in the background. Windows follows.

Get it from the newest `desktop-v*` release on GitHub
(`vaultsync_darwin_arm64`, `vaultsync_linux_amd64`, …), check it against the
release's `SHA256SUMS`, make it executable and run it in a terminal:

```sh
curl -fLO https://github.com/psimaker/vaultsync/releases/download/desktop-vX.Y.Z/vaultsync_darwin_arm64
curl -fLO https://github.com/psimaker/vaultsync/releases/download/desktop-vX.Y.Z/SHA256SUMS
shasum -a 256 -c --ignore-missing SHA256SUMS
chmod +x vaultsync_darwin_arm64 && ./vaultsync_darwin_arm64 setup
```

Download it in a terminal as above: the agent is not notarized by Apple yet,
so macOS refuses to run a copy that a web browser downloaded. If that
happened, allow it once under System Settings → Privacy & Security.

(The setup link above switches its "1) Obsidian device" path to the agent
once a release of it exists.) `setup`

1. downloads Syncthing 2.1.6 from github.com and installs it only if it
   matches the checksum built into VaultSync,
2. installs a background service — a LaunchAgent on macOS, a systemd user
   service on Linux; no administrator rights — which runs that Syncthing with
   its own settings, next to (and never touching) a Syncthing you may run
   yourself,
3. finds your Hub on the network (several answer: it asks which one printed
   your code — the code only ever goes to that one) and asks for the code,
4. shows **Choose a vault to sync**: the vaults Obsidian lists on this
   computer, newest first, and the vaults on your Hub. A vault from your Hub
   goes into a new, empty folder (default `~/Vaults/<name>`); a vault from
   this computer becomes a new vault on your Hub — when its folder already
   holds files, only after you agree to sync that very folder, and only with
   a vault your Hub reports as new, empty and shared with this computer
   alone. Everything is checked again right before the folder starts
   syncing.

VaultSync keeps its sync engine, settings and pairing identity in one folder —
`~/Library/Application Support/VaultSync` on macOS,
`~/.local/state/vaultsync` on Linux — plus the service file where the system
expects it (`~/Library/LaunchAgents`, `~/.config/systemd/user`) and, on a Mac,
a log in `~/Library/Logs/VaultSync`. Your vaults stay where they are. The
sync engine's own log (in that folder's `syncthing/`) names your vaults and
their paths; it never leaves this computer. The background service's own
log (`~/Library/Logs/VaultSync` on a Mac, the journal on Linux) names no
vault and no path; when the service stops, the reason is in that folder's
`last-error.txt`. `setup` also links the command
as `~/.local/bin/vaultsync` (unless that name is taken) and says how to call
it if that folder is not on your `PATH`.

```sh
vaultsync status       # what syncs where, and whether your Hub is connected
vaultsync pair         # another vault, or another Hub
vaultsync stop         # pause until vaultsync start
vaultsync uninstall    # remove the background service; vaults stay
                       # (--remove-data also removes the settings, the pairing
                       #  identity, the sync database and the command link —
                       #  never vault files)
```

Scripts and remote shells pass everything as flags; anything that would need
a decision is refused with the flag to add:

```sh
vaultsync setup --code TULIP-ANCHOR-42 --hub 192.168.1.20 \
  --vault Notes --create --path ~/Vaults/Notes --yes
```

`--yes` is consent to sync a `--path` that already holds files with the new
vault named by `--vault`; it never chooses a Hub and never overrides a
refusal.

**What the agent refuses — and why.** A vault inside iCloud Drive, OneDrive,
Dropbox, Google Drive or Nextcloud (or one that contains such a folder):
those services replace files they have not downloaded with placeholders, and
a vanished placeholder looks like a deletion that would reach every device.
Make a fully downloaded copy outside, for example in `~/Vaults`, and open
that copy in Obsidian. A folder that overlaps one VaultSync — or the
Syncthing you run yourself — already syncs. A folder with files for a vault
your Hub already has: VaultSync does not combine two vaults on its own;
download your Hub's vault into a new folder, or give this computer's vault a
different name on your Hub. Decision 046 records the rules — and the one gap
the Hub cannot close yet: it counts the files it holds, not files still
arriving, so a vault it reports as new and empty could, in a narrow race,
receive another device's files while this computer's are on their way.

**On a Mac.** macOS may ask whether *vaultsync* may find devices on your local
network and access a folder in Documents, Desktop or Downloads; allow both, or
the background service cannot reach your Hub directly (it then falls back to
a slower relay) or cannot read the vault. `vaultsync status` names the
setting when it sees macOS refusing. Keeping vaults in `~/Vaults` avoids the
folder prompt.

**On Linux.** The service runs while you are logged in. To keep syncing after
you log out, allow it once: `loginctl enable-linger` (setup never does this
for you). Without a systemd user session (some containers, WSL), run
`vaultsync setup --no-service` and keep `vaultsync run` running yourself.

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
docker compose pull && docker compose up -d           # refresh the images .env names
```

## Update the Hub

`docker compose pull` fetches the images at the tags the stack names: the
Hub's Syncthing follows its 1.x line, but the Hub itself stays on the version
in `.env` (`VAULTSYNC_HUB_IMAGE`) — re-running the setup keeps `.env` too. To
move a Hub to 0.2.0 (the release that prints the QR code):

```sh
cd /srv/vaultsync
sed -i 's|^VAULTSYNC_HUB_IMAGE=.*|VAULTSYNC_HUB_IMAGE=ghcr.io/psimaker/vaultsync-hub:0.2.0|' .env
docker compose pull && docker compose up -d
```

Vaults, paired devices and an open pairing code carry over unchanged.

## Troubleshooting

- **"no Hub answered"** on a device: device and Hub must be on the same network
  for pairing (afterwards they sync from anywhere). Pass `--hub HOST:8390` to
  skip discovery. On iPhone, also check Settings → Privacy & Security → Local
  Network → VaultSync, or scan the QR code — it carries the Hub's address when
  the printed link has `hub=`; if it has none, print a new code with
  `vaultsync-hub code --address <the Hub's LAN IP>`.
- **"The Hub address from the QR code did not answer"** on iPhone: the address
  `code` put into the QR code is not reachable from the iPhone (a VPN or a
  second network on the Hub). Tap **Search the Network Instead**, or print a
  new code with `vaultsync-hub code --address <the Hub's LAN IP>`. The `hub` container runs with host networking on purpose —
  Docker Desktop on macOS/Windows cannot do that, which is why the Hub targets
  Linux.
- **"the pairing code was locked"**: issue a new one with `vaultsync-hub code`.
- **"directory already holds files"**: that is the merge guard. Use
  `vault adopt` on the Hub, or an empty directory on the device.
- **Existing Syncthing on the same machine**: the Hub runs its own instance side
  by side (port 22001). Adopting the existing instance is intentionally not
  automated; see the FAQ in the README.
