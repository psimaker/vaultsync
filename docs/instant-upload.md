# Instant iPhone → server uploads (Shortcuts automation)

VaultSync's architecture is [intentionally asymmetric](architecture.md): Cloud Relay makes `server → iPhone` near-realtime, but iOS gives no app a reliable way to *upload* in the background. Notes you write in Obsidian on the iPhone reach your server when VaultSync next runs in the foreground — or during a system-scheduled background window.

A one-time **Shortcuts personal automation** closes that gap: every time you leave Obsidian, iOS opens VaultSync for a moment, which triggers an immediate rescan and sync. Your edits are on the server seconds after you close Obsidian.

## Set it up (about a minute)

1. Open the **Shortcuts** app → **Automation** tab → **+** (New Automation).
2. Choose **App**.
3. Tap **App** and select **Obsidian**. Check **Is Closed**, and choose **Run Immediately** (on older iOS versions: turn off "Ask Before Running").
4. Tap **Next**, then create a new shortcut with a single action: **Open URLs** with this URL:

   ```
   vaultsync://sync
   ```

5. Done. Close Obsidian once to test — VaultSync should open and start a scan right away.

To sync only a specific vault, use `vaultsync://sync?folder=<folder-id>` (the folder ID is shown in the vault's detail view).

## What to expect

- **"Is Closed" means "you switched away"** — the automation fires whenever you leave Obsidian, not only when you force-quit it. That is exactly what you want: every editing session ends with a sync.
- **VaultSync comes to the foreground briefly.** That is how iOS automations work; there is no silent variant. Switch back to whatever you were doing — the [~30s grace window](architecture.md) after backgrounding is enough to finish a typical note upload.
- **No automation needed for the other direction.** Changes made on your server reach the iPhone via Cloud Relay wake-ups (or on the next app open without it).

## Manual alternatives

- **Widget**: the home screen and lock screen widgets are tap-to-sync — one tap opens VaultSync and triggers the same `vaultsync://sync` action.
- **Pull-to-refresh** inside VaultSync rescans on demand.
