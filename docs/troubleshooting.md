# Troubleshooting

Find your symptom, jump to the fix. After each fix, retry from the app to confirm.

| Symptom | Go to |
|---|---|
| "Sync Engine Not Running", empty Device ID | [Syncthing Not Running](#syncthing-not-running) |
| `vaultsync-notify` logs `401`/`403`/permission denied | [Wrong Syncthing API Key in notify](#wrong-syncthing-api-key-in-notify) |
| "Relay Unreachable", health check fails | [Relay Unreachable](#relay-unreachable) |
| Stuck on "Confirm your first share" | [No Pending Shares Appear](#no-pending-shares-appear) |
| Subscribed, but no wake-ups arrive | [APNs Not Registered](#apns-not-registered) |
| Subscribed, but per-device provisioning says "Failed" | [APNs Not Registered](#apns-not-registered) |
| Ran the server installer, app still says "Not active yet" | [Relay quick triage](#relay-quick-triage) |
| Vault list empty, "folder not connected" | [Obsidian Folder Not Found](#obsidian-folder-not-found) |
| The vault syncs, but Obsidian does not show it | [Vault Syncs but Never Appears in Obsidian](#vault-syncs-but-never-appears-in-obsidian) |
| "Vault Folder Was Moved or Deleted" on a vault | [Vault Folder Was Moved or Deleted](#vault-folder-was-moved-or-deleted) |
| "Cannot access this folder" / reconnect needed | [Bookmark Access Expired](#bookmark-access-expired) |
| Syncs in foreground but not when closed | [Background Sync Not Working](#background-sync-not-working) |
| "Required device disconnected" | [Required Device Disconnected](#required-device-disconnected) |

## Relay quick triage

On your homeserver. The one-line installer sets the helper up as a `docker run` container (Linux with Docker), a systemd service (Linux without Docker), or a launchd agent (macOS); the Compose stack is a separate topology. Use the block that matches your install:

*Docker (one-line installer):*

```bash
docker logs --tail=200 vaultsync-notify
docker exec vaultsync-notify vaultsync-notify --doctor
```

*systemd (one-line installer without Docker):*

```bash
journalctl -u vaultsync-notify -n 200 --no-pager
systemctl status vaultsync-notify
# the unit carries the right env; simplest full doctor: re-run the installer,
# it ends with --doctor (and upgrades the helper as a side effect)
```

*launchd (macOS — agent installs log to `/tmp/vaultsync-notify.log`, LaunchDaemon installs via `sudo` to `/Library/Logs/vaultsync-notify.log`):*

```bash
tail -200 /tmp/vaultsync-notify.log /Library/Logs/vaultsync-notify.log 2>/dev/null
# agent install (no sudo):
SYNCTHING_CONFIG="$HOME/Library/Application Support/Syncthing/config.xml" \
  RELAY_URL=https://relay.vaultsync.eu ~/.local/bin/vaultsync-notify --doctor
# LaunchDaemon install (sudo, binary in /usr/local/bin, runs as the config owner):
sudo SYNCTHING_CONFIG="/Users/<config-owner>/Library/Application Support/Syncthing/config.xml" \
  RELAY_URL=https://relay.vaultsync.eu /usr/local/bin/vaultsync-notify --doctor
```

*Docker Compose stack:*

```bash
docker compose logs --tail=200 vaultsync-notify
docker compose run --rm vaultsync-notify --doctor
```

*Windows (one-step `install.ps1`):*

```powershell
Get-Content "$env:LOCALAPPDATA\VaultSync\vaultsync-notify.log" -Tail 200
$env:SYNCTHING_CONFIG = "$env:LOCALAPPDATA\Syncthing\config.xml"
$env:RELAY_URL = 'https://relay.vaultsync.eu'
& "$env:LOCALAPPDATA\VaultSync\vaultsync-notify.exe" --doctor
```

How to read `--doctor`: a relay rate-limit (HTTP 429) counts as **success** — it proves the trigger endpoint is reachable (the relay allows ~10 triggers/min/device). An inactive subscription prints a `WARN` for the trigger check (`WARN Relay trigger endpoint response sanity`, followed by `relay reports no active subscription for this device — …`) **without failing** — the helper reached the relay, but app↔relay provisioning is not active; reinstalling or changing the helper cannot repair that state. Every install also ships `vaultsync-notify --healthcheck`: the setup checks minus the trigger probe and minus the peer-state diagnostics below, silent, exit-code only — this is what the Docker `HEALTHCHECK` runs, and it works for scripts and monitoring on every flavor.

The doctor also reports **peer state**, always as a `WARN` (the check name, then an indented reason) and never as a failure — a device that is off or away is everyday life, not a setup error, so the exit code stays 0. `WARN Syncthing remote device connected` means no remote device is connected right now (or none is paired at all): open the Syncthing Web UI and check the device is added, resumed, and online on both sides. `WARN Syncthing folders shared with connected devices` means devices are connected but no folder is shared with any of them, so changes on the server cannot reach them: open the Syncthing Web UI, folder → Edit → Sharing, tick the device, then accept the share on the other device. If the doctor cannot read peer state at all (an older Syncthing, a momentary API error), the same checks print a `could not evaluate` WARN instead of failing. `--healthcheck` deliberately skips peer state — a legitimately offline peer never makes the container unhealthy.

In the app: **Cloud Relay** tab → **Relay health & diagnostics** → **Check Relay Status**. A green health check means the relay is *reachable*. **Last signal observed by Relay** confirms that the relay accepted a server signal; only **Last Wake-up Received** confirms delivery to this iPhone.

---

## Syncthing Not Running

**Looks like:** dashboard shows "Sync Engine Not Running", Device ID stays empty, sync actions fail instantly. Usually the bridge didn't finish starting (app backgrounded too soon, or a hiccup after update/reboot).

**Fix:**
1. Force-close VaultSync and reopen it.
2. Keep it in the foreground 20–30 seconds.
3. Confirm your Device ID appears in onboarding or settings.
4. Tap **Rescan Vault** from the vault detail page.
5. Still failing? Reboot the device and retry.

---

## Wrong Syncthing API Key in notify

`vaultsync-notify` reads the Syncthing API key straight from `config.xml` — there is **no key to paste**. So a `401`/`403` or a `permission denied` reading `config.xml` is almost always a *read-permission* or *wrong-file* problem, not a typo.

**Looks like:** logs show `401`, `403`, `unauthorized`, or `permission denied`; `--doctor` fails the Syncthing API check; no triggers are sent.

**Fix:**
1. **Run as the right user.** `config.xml` is mode `0600`, so the helper must run as the uid that owns it — the permission error names the file's actual owner and the exact `-u <uid>:<gid>` to use (helper 1.6.1+). The [one-line installer](../notify/README.md#-one-step-setup) gets this right automatically; manually: in Compose set `PUID`/`PGID` (official image `1000`, linuxserver `911`, Unraid `99:100`), with `docker run` add `-u <uid>:<gid>`.
2. **Point at the real config.** Set `SYNCTHING_CONFIG=/path/to/config.xml` if it isn't auto-detected (rare — standard, container, and Synology/QNAP/Unraid host layouts are all probed automatically).
3. **Clear a bad override.** If you set `SYNCTHING_API_KEY` yourself, it wins over auto-detect — remove it to fall back to `config.xml`, or update it to match Syncthing's current key.
4. **Recreate and re-check.** One-line install (Docker or systemd): simply re-run the installer — it re-resolves the reviewed version tag or replaces the checksum-verified binary, restarts the service, and ends with `--doctor`. Compose stack:
   ```bash
   docker compose up -d --force-recreate vaultsync-notify
   docker compose run --rm vaultsync-notify --doctor
   ```

---

## Relay Unreachable

**Looks like:** VaultSync shows "Relay Unreachable"; the diagnostics health check fails; `vaultsync-notify` logs show timeout, DNS, or connection-refused errors. Usually a network/firewall block or a wrong `RELAY_URL`.

**Fix:**
1. Confirm internet access on both the iOS device and the homeserver.
2. Check `RELAY_URL` (cloud value: `https://relay.vaultsync.eu`) where your install keeps it — one-line Docker install: `docker inspect vaultsync-notify | grep RELAY_URL`; systemd: `systemctl cat vaultsync-notify`; launchd: `~/Library/LaunchAgents/eu.vaultsync.notify.plist`; Compose stack: `notify/.env`.
3. Review homeserver egress rules (firewall, VPN, proxy).
4. Test relay health from the homeserver:
   ```bash
   curl -fsSL https://relay.vaultsync.eu/api/v1/health
   ```
5. In the app: **Cloud Relay** tab → **Relay health & diagnostics** → **Check Relay Status**.
6. Once reachable, trigger a file change and confirm **Last signal observed by Relay** updates, then check **Last Wake-up Received** separately for delivery to this iPhone.

---

## No Pending Shares Appear

**Looks like:** onboarding stays on "Confirm your first share"; nothing under **Pending Shares**; no vault activates. Usually the desktop never offered the folder to this device, or the Device IDs don't match.

**Fix:**
1. On desktop Syncthing, open the folder → **Sharing**, and confirm the iOS Device ID is listed.
2. Confirm that Device ID matches the one VaultSync shows.
3. Ensure desktop Syncthing is online and the folder isn't paused.
4. In VaultSync, pull to refresh (or reopen the app) and check **Pending Shares**; accept manually if shown.
5. Still empty? Unshare and re-share the folder from the desktop.

---

## APNs Not Registered

Silent push wake-ups use a background push that **does not need notification permission** — but they do need a valid APNs token and a provisioned device.

**Looks like:** diagnostics show the APNs token missing or registration failed;
you're subscribed but wake-ups never arrive; or per-device provisioning says
**Failed** while the helper reports an inactive subscription.

**Fix:**
1. On the server, run `--doctor` (see the quick-triage block above). A `WARN`
   on the trigger check saying `relay reports no active subscription for this
   device` means the helper reached the relay and the problem is in app↔relay
   provisioning — not on your homeserver. Reinstalling or changing the helper
   cannot repair this state.
2. Record **Settings → About → VaultSync version & build**, then record the
   hosted relay version without sending any private data:
   ```bash
   curl -fsSL https://relay.vaultsync.eu/api/v1/health
   ```
   Install any available VaultSync App Store update before retrying. If the app
   is already current but provisioning still fails, include both versions in a
   bug report: a relay/app provisioning-contract mismatch needs an app or hosted
   relay fix, not a homeserver change.
3. Open **Cloud Relay** tab → **Relay health & diagnostics**.
4. Tap **Retry APNs Registration** and confirm an **APNs Token** appears.
5. Tap **Retry Provisioning** to rebind the token to your device IDs (shown while subscribed).
6. Trigger a file change on your server and verify **Last signal observed by Relay** updates, then check **Last Wake-up Received** separately.

---

## Obsidian Folder Not Found

**Looks like:** setup says the Obsidian folder isn't connected; the picker shows no "Obsidian" under "On My iPhone"; or VaultSync declines the folder you picked. The Obsidian folder only shows up there once Obsidian holds a vault on the iPhone, and only that folder works: Obsidian opens vaults from its own folder and from nowhere else.

**Fix:**
1. Open Obsidian and create a vault with **Store in iCloud** turned off. Any name will do.
2. In VaultSync tap **Connect Obsidian Folder**.
3. In the picker choose **On My iPhone → Obsidian** and tap **Open** there, without going into one of your vaults.
4. Return to VaultSync and confirm it lists detected vaults or pending shares.

Do not create a folder named "Obsidian" yourself. A vault synced into such a folder arrives in full, but Obsidian never shows it.

These steps are for the first setup. If vaults already sync on this iPhone and VaultSync asks you to reconnect, select the folder they sync in again, whatever it is called — see [Bookmark Access Expired](#bookmark-access-expired). Another folder would cut those vaults off from their files.

---

## Vault Syncs but Never Appears in Obsidian

**Looks like:** VaultSync shows the vault as synced and the file count is right, but Obsidian does not list it. Obsidian lists the folders directly inside its own folder as vaults and nothing else. Earlier versions could set a vault up outside that: in a folder you made yourself, in a folder inside Obsidian's folder, or straight in Obsidian's folder instead of in a folder of its own.

**These steps belong to a notice.** VaultSync shows it under **Sync Issues**: "Obsidian Cannot Show This Vault" or "Obsidian Cannot Show Vaults From This Folder". **If your VaultSync shows no such notice, update it first and do not follow the steps:** in a version without the notice, a vault you remove and accept again goes back to the same place, and the files you would then be looking at are the vault itself, not an old copy.

Syncing continues either way, and VaultSync never moves a vault on its own. Before you remove a vault, make sure another device has everything you changed on this iPhone: open a note you edited here on that device. "Up to Date" in VaultSync only says that this iPhone has what the others sent.

**Fix — "Obsidian Cannot Show This Vault"** (the vault syncs straight into the folder you selected):
1. Open the vault in VaultSync and remove it. This only stops syncing it on this iPhone.
2. Under **Pending Shares**, accept the vault again. It now gets a folder of its own, and Obsidian lists it once the notes have arrived. If the share says that its folder already holds files, that folder is most likely part of the earlier copy: do not combine them — tap **Choose Vault…** and give the vault another folder name.

**Fix — "Obsidian Cannot Show Vaults From This Folder"** (VaultSync is connected to a folder that is not Obsidian's own):
1. Open Obsidian and create a vault with **Store in iCloud** turned off, if there is none on the iPhone yet. Without one, "Obsidian" does not appear under "On My iPhone".
2. In VaultSync, remove the vaults that sync in this folder. This only stops syncing them on this iPhone.
3. The notice now offers **Select Obsidian's Folder**. Tap it, choose **On My iPhone → Obsidian** and tap **Open** there, without going into one of your vaults.
4. Under **Pending Shares**, accept the vaults again. They arrive in Obsidian's folder, and Obsidian lists them.

The button appears only once no vault syncs in the old folder: selecting another folder earlier would cut those vaults off from the folder they live in.

**Afterwards:**
- The copy that arrived earlier stays where it was. Nothing needs it anymore, but it may hold a note that never reached another device. Delete it in the Files app only after you have checked that, and only the earlier copy — never the folder the vault syncs into now. Its place is shown under **Location** on the vault's page.
- Sync Filters belong to the vault's folder and start fresh in the new one. Set them again on the vault's page before large folders you had excluded start to sync.
- If the share on your computer is a folder that holds several vaults, it arrives as one folder, and Obsidian shows it as a single vault with yours as folders inside. Share each vault on its own from the computer instead.

If you keep things where they are on purpose, tap **Keep It This Way**: the notice goes away for that vault, or for that folder while vaults sync in it, and nothing else changes. New shares then arrive there again, including one that was waiting. Without it, a new share waits under **Pending Shares** until you accept it yourself.

---

## Vault Folder Was Moved or Deleted

**Looks like:** a vault shows "Vault Folder Was Moved or Deleted" and its syncing has stopped. The vault's folder was moved, renamed, replaced, or deleted outside VaultSync (in the Files app, by Obsidian, or by another app), so VaultSync can no longer verify that the folder still holds this vault's data.

**Fix:**
1. If you moved or renamed the folder: move it back to its original place and name — syncing resumes on its own.
2. If the folder is gone or was replaced: remove the vault in VaultSync on this iPhone, then accept its share again under **Pending Shares** — it syncs into a fresh folder.
3. If notes are missing on this iPhone, they are still on your other synced devices — re-accepting the share in step 2 brings them back.

VaultSync never moves, recreates, or deletes folders on its own, and a rescan cannot fix this — recovery here is always your manual decision.

---

## Bookmark Access Expired

VaultSync re-derives every vault's location from your Obsidian folder on launch, so most storage moves self-heal. When a vault still can't be reached, the security-scoped bookmark needs renewing.

**Looks like:** "VaultSync cannot access this folder" / reconnect required; background sync can't access Obsidian; a previously working vault stops.

**Fix:**
1. Tap **Reconnect to Obsidian** and re-select the same Obsidian folder in the Files picker.
2. Keep VaultSync in the foreground and run a rescan.
3. If a vault points at storage that's truly gone, use **Remove this vault** (it only stops syncing on this iPhone — your other devices keep their notes).

---

## Background Sync Not Working

iOS controls background time and may delay or skip it. Cloud Relay makes `server → iPhone` feel near-realtime; `iPhone → server` is reliable only when VaultSync is open.

**Looks like:** last sync goes stale; sync works in the foreground but not when the app is closed.

**Fix:**
1. Open VaultSync and run a manual rescan.
2. Clear any **Sync Issues** first (folder errors, pending shares, disconnected peers).
3. Reconnect the Obsidian folder if access warnings appear.
4. For relay users, confirm `vaultsync-notify --doctor` is green and **Last Wake-up Received** is recent.
5. Re-check the **Last sync** timestamp after the next background window.

> For `iPhone → server`, open VaultSync and let it sync in the foreground. iOS background time is system-controlled and not guaranteed. A one-time Shortcuts automation can do the opening for you every time you leave Obsidian — see [instant-upload.md](instant-upload.md).

---

## Required Device Disconnected

**Looks like:** Sync Issues reports a required device disconnected; vaults stop receiving desktop updates. Usually the desktop is offline or the network path is blocked.

**Fix:**
1. Ensure desktop Syncthing is running and online.
2. Confirm both devices can reach each other (LAN, VPN, or relay).
3. In VaultSync, check the device still exists under **Devices**.
4. Remove/re-add the device if its ID changed, then rescan.

---

## Still stuck?

Capture these before filing an issue:

1. VaultSync version + iOS version.
2. Screenshots of **Sync Issues** and **Relay health & diagnostics**.
3. `vaultsync-notify --doctor` output (if using the relay — per-flavor commands in the quick-triage block above).
4. Relevant `vaultsync-notify` log lines around the failure (`docker logs vaultsync-notify`, `journalctl -u vaultsync-notify`, or `/tmp/vaultsync-notify.log` depending on flavor).
