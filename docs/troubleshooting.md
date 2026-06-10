# Troubleshooting

Find your symptom, jump to the fix. After each fix, retry from the app to confirm.

| Symptom | Go to |
|---|---|
| "Sync Engine Not Running", empty Device ID | [Syncthing Not Running](#syncthing-not-running) |
| `vaultsync-notify` logs `401`/`403`/permission denied | [Wrong Syncthing API Key in notify](#wrong-syncthing-api-key-in-notify) |
| "Relay Unreachable", health check fails | [Relay Unreachable](#relay-unreachable) |
| Stuck on "Confirm your first share" | [No Pending Shares Appear](#no-pending-shares-appear) |
| Subscribed, but no wake-ups arrive | [APNs Not Registered](#apns-not-registered) |
| Vault list empty, "folder not connected" | [Obsidian Folder Not Found](#obsidian-folder-not-found) |
| "Cannot access this folder" / reconnect needed | [Bookmark Access Expired](#bookmark-access-expired) |
| Syncs in foreground but not when closed | [Background Sync Not Working](#background-sync-not-working) |
| "Required device disconnected" | [Required Device Disconnected](#required-device-disconnected) |

**Relay quick triage** — on your homeserver:

```bash
docker compose logs --tail=200 vaultsync-notify
docker compose run --rm vaultsync-notify --doctor
```

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
1. **Run as the right user.** `config.xml` is mode `0600`, so the helper must run as the uid that owns it. In Compose set `PUID`/`PGID` (official image `1000`, linuxserver `911`, Unraid `99:100`); with `docker run`, add `-u <uid>:<gid>`.
2. **Point at the real config.** Set `SYNCTHING_CONFIG=/path/to/config.xml` if it isn't auto-detected (needed for Synology/QNAP).
3. **Clear a bad override.** If you set `SYNCTHING_API_KEY` yourself, it wins over auto-detect — remove it to fall back to `config.xml`, or update it to match Syncthing's current key.
4. **Recreate and re-check:**
   ```bash
   docker compose up -d --force-recreate vaultsync-notify
   docker compose run --rm vaultsync-notify --doctor
   ```

---

## Relay Unreachable

**Looks like:** VaultSync shows "Relay Unreachable"; the diagnostics health check fails; `vaultsync-notify` logs show timeout, DNS, or connection-refused errors. Usually a network/firewall block or a wrong `RELAY_URL`.

**Fix:**
1. Confirm internet access on both the iOS device and the homeserver.
2. Check `RELAY_URL` in `notify/.env` (cloud value: `https://relay.vaultsync.eu`).
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
3. `vaultsync-notify --doctor` output (if using the relay).
4. Relevant `vaultsync-notify` log lines around the failure.
