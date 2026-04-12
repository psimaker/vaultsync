# VaultSync Troubleshooting

This guide covers the most common VaultSync failures and how to fix them quickly.

## Quick Triage

1. Open VaultSync and check **Sync Issues** plus **Settings → Cloud Relay**.
2. If you use Cloud Relay, open **Relay Diagnostics** and run **Run Full Diagnostics**.
3. On your homeserver, run:
   - `docker compose logs --tail=200 vaultsync-notify`
   - `docker compose exec vaultsync-notify /app/vaultsync-notify --doctor` (if available in your image)
4. Retry from the app after each fix so you can confirm the issue is resolved.

## Syncthing Not Running

### Symptoms
- Dashboard shows **Sync Engine Not Running**.
- Device ID stays empty in onboarding.
- Sync actions fail immediately.

### Likely Causes
- Syncthing bridge did not start after launch.
- App was backgrounded before startup finished.
- Temporary startup failure after update/reboot.

### Fix Steps
1. Force-close VaultSync and reopen it.
2. Keep the app in foreground for at least 20-30 seconds.
3. Confirm your Device ID appears in onboarding or settings.
4. Tap **Rescan Vault** from the vault detail page.
5. If it still fails, reboot the iPhone/iPad and retry.

## Wrong Syncthing API Key in notify

### Symptoms
- `vaultsync-notify` logs contain `401`, `403`, or `unauthorized`.
- `--doctor` fails on Syncthing API checks.
- Relay triggers are never sent.

### Likely Causes
- `SYNCTHING_API_KEY` in `notify/.env` does not match Syncthing GUI API key.
- Syncthing key was rotated but container env was not updated.

### Fix Steps
1. Open Syncthing Web UI on your homeserver.
2. Go to **Actions → Settings → GUI → API Key** and copy the key.
3. Update `notify/.env`:
   - `SYNCTHING_API_KEY=<copied key>`
4. Restart the container:
   - `docker compose up -d --force-recreate vaultsync-notify`
5. Run doctor again:
   - `docker compose exec vaultsync-notify /app/vaultsync-notify --doctor`
6. Confirm logs no longer show `401/403`.

## Relay Unreachable

### Symptoms
- VaultSync shows **Relay Unreachable**.
- Relay health check fails in Settings/Relay Diagnostics.
- `vaultsync-notify` logs show timeout, DNS, or connection refused errors.

### Likely Causes
- Internet outage or captive network.
- Firewall/VPN/proxy blocks `relay.vaultsync.eu`.
- Relay service temporary outage.
- Wrong `RELAY_URL` value in `notify/.env`.

### Fix Steps
1. Verify internet access on both iOS device and homeserver.
2. Confirm `RELAY_URL` in `notify/.env` (default cloud value: `https://relay.vaultsync.eu`).
3. Check homeserver egress rules (firewall, VPN, proxy).
4. From homeserver shell, test relay health:
   - `curl -fsSL https://relay.vaultsync.eu/api/v1/health`
5. In app, open **Settings → Cloud Relay → Test** or **Relay Diagnostics → Run Full Diagnostics**.
6. Retry provisioning after health check is green.

## No Pending Shares Appear

### Symptoms
- Onboarding stays on “Confirm your first share”.
- No entries under **Pending Shares**.
- No vaults become active after adding device.

### Likely Causes
- Desktop Syncthing never offered a folder to iOS device.
- Device IDs are mismatched.
- Folder share is paused or sent to another device ID.
- iOS device is offline/disconnected.

### Fix Steps
1. On desktop Syncthing, open the target folder and verify the iOS device ID is listed under **Sharing**.
2. Confirm the iOS Device ID in VaultSync matches what desktop Syncthing uses.
3. Ensure desktop Syncthing is online and folder is unpaused.
4. In VaultSync, keep app open and pull to refresh state (or reopen app).
5. Check **Pending Shares** section and accept manually if shown.
6. If still empty, unshare and re-share the folder from desktop Syncthing.

## APNs Not Registered

### Symptoms
- Settings/Relay Diagnostics show APNs token missing or registration failed.
- Cloud Relay subscribed but instant wake-ups do not happen.

### Likely Causes
- Notifications disabled for VaultSync.
- APNs registration failed and status is stale.
- Token rotated and provisioning has not been retried.

### Fix Steps
1. On iOS: **Settings → Notifications → VaultSync** and enable notifications.
2. In VaultSync Settings, tap **Retry APNs Registration**.
3. Re-open **Relay Diagnostics** and confirm APNs token is present.
4. Tap **Retry Provisioning** to rebind token and device IDs.
5. Trigger a file change and verify **Last Trigger Received** updates.

## Obsidian Folder Not Found

### Symptoms
- Setup says Obsidian folder is not connected.
- Folder picker guidance appears repeatedly.
- Vault list stays empty even though app has storage access.

### Likely Causes
- Obsidian app not opened yet (folder not created).
- Wrong folder selected in picker.
- Selected location does not contain `.obsidian`.

### Fix Steps
1. Install/open Obsidian at least once on the device.
2. In VaultSync tap **Connect Obsidian Folder**.
3. In picker choose **On My iPhone → Obsidian** (or vault root containing `.obsidian`).
4. Tap **Open** and return to VaultSync.
5. Verify VaultSync now lists detected vaults or pending shares.

## Bookmark Access Expired

### Symptoms
- VaultSync reports Obsidian access expired or reconnect required.
- Background sync says it cannot access Obsidian.
- Previously working vault suddenly stops syncing.

### Likely Causes
- Security-scoped bookmark became stale.
- iOS storage permission token invalidated after restore/update/move.
- Selected folder moved or deleted.

### Fix Steps
1. In VaultSync tap **Reconnect Obsidian Folder**.
2. Re-select the same Obsidian folder in Files picker.
3. Keep VaultSync in foreground and run a rescan.
4. If reconnect fails, check that folder still exists and is readable in Files.
5. If folder path changed, select the new location and re-accept shares if needed.

## Background Sync Not Working

### Symptoms
- Last successful sync becomes stale.
- Sync Issues show background timeout or failure.
- Sync works in foreground but not when app is closed.

### Likely Causes
- Bookmark access unavailable in background.
- No shared folders configured yet.
- iOS background execution deadline exceeded.
- Syncthing bridge failed to start in background.
- iOS delayed or skipped the wake that would have attempted a background upload.

### Fix Steps
1. Open VaultSync in foreground and run a manual rescan.
2. Resolve any **Sync Issues** first (folder errors, pending shares, disconnected peers).
3. Reconnect Obsidian folder if bookmark/access warnings appear.
4. Keep Cloud Relay/APNs healthy if you depend on instant sync.
5. For relay users, validate `vaultsync-notify --doctor` output and relay health.
6. Re-check **Last sync** timestamp after the next background window.

### Important Limitation

With Cloud Relay enabled, VaultSync can now attempt `iPhone -> server` uploads automatically in the background. However, iOS still decides when those wakes are delivered. That means background uploads can be delayed even when everything is configured correctly. If a change is time-sensitive, opening VaultSync remains the most reliable way to force an immediate upload.

## Required Device Disconnected

### Symptoms
- Sync Issues reports required device disconnected.
- Vaults stop receiving updates from desktop.

### Likely Causes
- Desktop Syncthing offline.
- Network path blocked (LAN/VPN/firewall).
- Device removed or renamed unexpectedly.

### Fix Steps
1. Ensure desktop Syncthing is running and online.
2. Confirm both devices can reach each other (LAN or relay path).
3. In VaultSync, verify device still exists under **Devices**.
4. Remove/re-add device if ID changed.
5. Trigger a rescan after reconnecting.

## Still Stuck?

Capture these details before filing an issue:

1. VaultSync version and iOS version.
2. Screenshot of **Sync Issues** and **Relay Diagnostics**.
3. `vaultsync-notify --doctor` output (if using relay).
4. Relevant `vaultsync-notify` log lines around the failure timestamp.
