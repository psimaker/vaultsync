# Troubleshooting

Find your symptom, jump to the fix. After each fix, retry from the app to confirm.

| Symptom | Go to |
|---|---|
| "Sync Engine Not Running", empty Device ID | [Syncthing Not Running](#syncthing-not-running) |
| `vaultsync-notify` logs `401`/`403`/permission denied | [Wrong Syncthing API Key in notify](#wrong-syncthing-api-key-in-notify) |
| "Relay Unreachable", health check fails | [Relay Unreachable](#relay-unreachable) |
| Stuck on "Confirm your first share" | [No Pending Shares Appear](#no-pending-shares-appear) |
| Subscribed, but no wake-ups arrive | [APNs Not Registered](#apns-not-registered) |
| Ran the server installer, app still says "Not active yet" | [Relay quick triage](#relay-quick-triage) |
| Vault list empty, "folder not connected" | [Obsidian Folder Not Found](#obsidian-folder-not-found) |
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
SYNCTHING_CONFIG="$HOME/Library/Application Support/Syncthing/config.xml" \
  RELAY_URL=https://relay.vaultsync.eu ~/.local/bin/vaultsync-notify --doctor
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

How to read `--doctor`: a relay rate-limit (HTTP 429) counts as **success** — it proves the trigger endpoint is reachable (the relay allows ~10 triggers/min/device). An inactive subscription prints `WARN: relay reports no active subscription for this device` **without failing** — that state is fixed in the app (subscribe / re-provision), not on the server. Every install also ships `vaultsync-notify --healthcheck`: the same checks minus the trigger probe, silent, exit-code only — this is what the Docker `HEALTHCHECK` runs, and it works for scripts and monitoring on every flavor.

In the app: **Cloud Relay** tab → **Relay health & diagnostics** → **Check Relay Status**. A green health check means the relay is *reachable*; only an updated **Last Trigger Received** proves wake-ups are actually delivered.

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
4. **Recreate and re-check.** One-line install (Docker or systemd): simply re-run the installer — it pulls the latest image / replaces the binary, restarts the service, and ends with `--doctor`. Compose stack:
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
6. Once reachable, trigger a file change and confirm **Last Trigger Received** updates — that's the proof wake-ups are delivered, not just that the relay is up.

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

**Looks like:** diagnostics show the APNs token missing or registration failed; you're subscribed but wake-ups never arrive.

**Fix:**
0. On the server, run `--doctor` (see the quick-triage block above). A `WARN: relay reports no active subscription for this device` line means the server side is healthy and the problem is app-side: subscription or provisioning — continue below.
1. Open **Cloud Relay** tab → **Relay health & diagnostics**.
2. Tap **Retry APNs Registration** and confirm an **APNs Token** appears.
3. Tap **Retry Provisioning** to rebind the token to your device IDs (shown while subscribed).
4. Trigger a file change on your server and verify **Last Trigger Received** updates.

---

## Obsidian Folder Not Found

**Looks like:** setup says the Obsidian folder isn't connected; the picker guidance keeps reappearing; the vault list stays empty. Usually Obsidian hasn't created its folder yet, or the wrong folder was picked.

**Fix:**
1. Install/open Obsidian at least once on the device.
2. In VaultSync tap **Connect Obsidian Folder**.
3. In the picker choose **On My iPhone → Obsidian** (or the vault root that contains `.obsidian`).
4. Tap **Open** and return to VaultSync.
5. Confirm VaultSync now lists detected vaults or pending shares.

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
4. For relay users, confirm `vaultsync-notify --doctor` is green and **Last Trigger Received** is recent.
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
