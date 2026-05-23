# Changelog

All notable changes to VaultSync are documented here.

---

## [1.3.2] — 2026-05-23

### Fixed

- **Skip on iPhone now actually skips returning conflicts** ([#8](https://github.com/psimaker/vaultsync/issues/8)) — Tapping "Always skip on this iPhone" in the conflict resolver previously added only the original file's path to `.stignore`, so a fresh `sync-conflict-…` copy with a new timestamp would arrive from the desktop and the conflict reappeared. The Skip flow now also writes a `<path>.sync-conflict-*` glob, deletes any conflict copies of the file already sitting in the vault, and rescans so the conflict disappears from the Sync Issues list immediately. The Sync Filters → Custom Patterns list groups the pair as a single row with a "+ conflict copies" caption.
- **"Reconnecting…" replaces the transient "device disconnected" warning** — When VaultSync resumes after the app has been away from the foreground for a while, the home screen no longer briefly flashes a "1 Required Device Is Disconnected" warning while the embedded Syncthing process is still rebuilding its connection. Instead, the sync status reads "Reconnecting…" with a calm system spinner for up to 30 seconds. If the peer is genuinely offline beyond that window, the existing warning surfaces normally.

---

## [1.3.1] — 2026-05-17

### Fixed

- **Conflict button no longer also opens the browser** ([#6](https://github.com/psimaker/vaultsync/issues/6)) — Tapping "Resolve Conflicts" in the home-screen Sync Issues section pushed the conflict list **and** opened Safari at the same time, because the "Learn how to fix" link shared a list row with the navigation control. The link is now a dedicated button that consumes the tap, so the two gestures no longer collide. The same pattern was rolled out to every "Learn how to fix" spot in the app.
- **Missing German and Simplified Chinese translations for Sync Issue titles** — Strings like "1 Conflict Needs Resolution" appeared in English on non-English locales because the keys were absent from `Localizable.strings`. Added the missing entries for all four issue kinds (folder errors, disconnected devices, pending shares, conflicts).

### Changed

- **Home screen reads as content, not chrome** — The large "VaultSync" navigation title is gone; the bar stays inline so the dashboard and vault list lead the eye.
- **Device list shows just the name** — The long Syncthing Device ID no longer appears under each peer on the home screen. Tap a device to see the full ID in the detail view.
- **Cloud Relay settings declutter** — Per-device provisioning rows are now hidden when the device is already provisioned, so only devices that need attention surface. The "Retry Provisioning" button has been removed from Settings; the same action stays available in Relay Diagnostics where the other advanced controls live.
- **Settings → This Device tightened** — The standalone Device ID display has been removed. The "Copy Device ID" button is sufficient on its own.

---

## [1.3.0] — 2026-05-11

### Added

- **Pull-to-refresh on the main screen** — Swipe down on the vault list to trigger an immediate sync, the same gesture you use in Mail or Files. Useful when you've just edited a note in Obsidian and want it on your desktop right away.
- **Automatic rescan when returning to VaultSync** — If you've been away from the app for more than five seconds, opening it again triggers a fresh scan of your vaults. The "close VaultSync and reopen it" workaround is no longer needed to see recent edits sync through.

### Changed

- **Faster fallback rescan interval** — The safety-net rescan that catches changes the iOS file-system watcher misses (for example edits made in Obsidian's sandbox while VaultSync is in the background) now runs every minute instead of every hour. Existing vaults are migrated on first launch; vaults with a user-customised interval keep their value.

---

## [1.2.0] — 2026-05-09

### Added

- **Sync Filters per vault** — A new "Sync Filters" screen on each vault's detail page lets you toggle preset ignore-pattern groups in plain language: Workspace state, Trash, Git repository, macOS metadata (`.DS_Store`), Copilot index, and Obsidian app cache. No need to learn `.stignore` syntax to skip the obvious stuff.
- **Vault scan with size figures** — When you open Sync Filters, VaultSync scans the folder for known heavy directories (`.git`, `.copilot-index`, `node_modules`, `.obsidian/cache`) and shows their actual size in MB and file count. "Git repository — 45.2 MB, 1,847 files" makes the choice concrete. Matches are aggregated across all vault subdirectories in multi-vault Obsidian-root setups.
- **Always skip on this iPhone** — A new menu item in the conflict resolver lets you add the conflicting file's exact path to the folder's ignore list with one tap. The fastest way from "this file conflicts every time" to a permanent fix.
- **First-run recommendation sheet** — On first opening a vault's detail screen, a sheet pre-checks the recommended presets (Workspace state + Trash) and any heavy folders the scan found. Done applies everything; Skip dismisses without changes. Shown once per vault.
- **Custom patterns editor** — A section in Sync Filters for free-form `.stignore` patterns with swipe-to-delete and a footer link to the Syncthing pattern docs.
- **Sync Filters localization** — All new strings translated to English, German, and Simplified Chinese.

### Changed

- **Default ignore patterns** — Now derived from the new `IgnorePreset.recommended` set (Workspace state + Trash) instead of being hardcoded. Existing vaults keep their current `.stignore` lines untouched; the new UI just shows the corresponding presets as already-active.

---

## [1.1.0] — 2026-04-22

### Added

- **Localized setup surfaces** — The new onboarding and setup-status copy now ships in English, German, and Simplified Chinese.

### Changed

- **Calmer first launch** — First-run onboarding is now a short, informational 2-screen introduction. Real setup stays on the VaultSync home screen, where pairing, pending shares, vault activity, and sync status already live.
- **Setup Guide -> Setup Status** — Settings now open a live setup-status and troubleshooting view instead of the old onboarding-style guide. It focuses on essential sync readiness and points users back to the home screen for action.
- **More honest vault-sync status** — A pending share no longer counts as “done”. Setup Status now keeps vault syncing marked as needing attention until at least one vault is actually active.
- **Cleaner Settings** — Discovery controls were removed from Settings to reduce noise. Discovery remains enabled by default.
- **iOS support messaging corrected** — Project docs and release metadata now consistently reflect VaultSync’s iOS/iPadOS 18+ support, while `BGContinuedProcessingTask` remains an iOS 26+ enhancement when available.

## [1.0.2] — 2026-04-12

### Fixed

- **Silent-push sync reliability** — Silent pushes from Cloud Relay are now reliably acted upon even after iOS has suspended the process. A stale lifecycle lock previously caused the background sync handler to skip the bridge restart, leaving dead TCP sockets unreconnected. Pushes delivered but never produced a sync.
- **Direct homeserver edits now wake iPhone again** — `vaultsync-notify` no longer relies on `ItemFinished` alone. It now triggers on real outgoing-change markers (`LocalIndexUpdated`) and on `FolderCompletion` only when a peer is actually behind, so edits made directly on the homeserver once again produce a silent push without reintroducing the old `StateChanged` push storm.
- **Silent-push recovery fallback** — The iOS background sync path now treats folder rescans as the fast path, but if a silent push produces no real peer or sync activity within a short window it force-restarts the embedded Syncthing bridge and retries inside the same background execution budget. This closes the remaining gap where APNs delivery succeeded but Syncthing stayed on dead suspended sockets.
- **iOS suspend grace period** — The app now acquires a `UIApplication` background-task assertion when entering the background, giving pending Syncthing operations up to ~30 seconds to complete instead of being suspended within ~5 seconds.
- **Relay DB-reset recovery** — Re-provisioning interval reduced from 24 hours to 6 hours, and a re-provision probe is now triggered whenever Relay Diagnostics is opened. Restores push delivery automatically within 6h after a server-side token reset (e.g. from self-healing cleanup).
- **Cloud Relay push expiration** — Silent pushes were sent with `apns-expiration=0`, causing APNs to drop them after a single failed delivery attempt when the iPhone was briefly unreachable. Expiration is now set to +1 hour so APNs retries delivery until the device wakes.
- **Vault path nesting** — Accepting a pending share no longer creates a redundant subdirectory when the selected Obsidian root is itself a vault or when its folder name matches the share label (case-insensitive). Previously selecting `On My iPhone/Obsidian/` with a desktop share labelled `obsidian` produced `Obsidian/obsidian/` — Obsidian then couldn't see the synced files as part of the vault.
- **Background sync completion detection** — The idle-state check used by the silent-push and BGAppRefresh handlers now verifies that folders have no outstanding `needFiles`, `needBytes`, or `inProgressBytes` before declaring success. Previously Syncthing's momentary `idle` state between scan and sync phases was treated as "done", causing the handler to shut Syncthing down before any file was actually pulled. This matches the peer-side observation of connections lasting only ~1 second after a silent push.
- **vaultsync-notify trigger deduplication** — Trigger delivery is now deduplicated per folder/marker so repeated scan/completion cycles do not fan out into redundant APNs wake-ups for the same logical change.

### Changed

- **Relay Diagnostics cleanup** — Temporary per-run debug timeline UI used during the v1.0.2 reliability investigation has been removed again. Relay Diagnostics now keeps the operator-facing relay health and provisioning tools, while low-level tracing stays in app logs instead of persistent user-visible debug storage.
- **Transparent iOS limits** — Product docs now explicitly state that Cloud Relay is designed for near-realtime `server -> iPhone` wake-ups, while `iPhone -> server` remains most reliable when VaultSync is opened.

---

## [1.0.1] — 2026-04-11

### Added

- **Auto-ignore patterns for Obsidian vaults** — `.Trash`, `.obsidian/workspace.json`, and `.obsidian/workspace-mobile.json` are automatically added to `.stignore` when a vault is created, accepted, or on first app launch after update. Prevents the most common sync conflicts.
- **Automatic relay re-provisioning** — When iOS rotates the APNs device token, the app now detects the change and re-provisions with the Cloud Relay automatically. A periodic startup check (every 24h) ensures provisioning stays current even if the token doesn't change.
- **Relay self-healing** — The Cloud Relay server now removes device tokens rejected by APNs (`BadDeviceToken`, `Unregistered`) and cleans up stale tokens when a new token is registered.

### Changed

- **Home screen activity section** — Replaced inline preview rows with a single "Open activity timeline" link for a cleaner layout.

### Fixed

- **AcceptPendingFolder missing IgnorePerms** — Accepted folder shares now set `IgnorePerms: true`, matching the existing behavior of manually added folders. Fixes `chmod: operation not permitted` errors in Docker/containerized Syncthing environments.

---

## [1.0.0] — 2026-04-07

### Initial Release

- Embedded Syncthing v2.x via gomobile xcframework
- 5-step interactive setup checklist with QR code pairing
- Direct Obsidian vault folder access and synchronization
- Sync conflict resolver with side-by-side Markdown diffs
- Cloud Relay push-based instant sync via APNs ($0.99/month)
- vaultsync-notify Docker sidecar for homeserver integration
- Real-time sync activity timeline
- Sync issues panel with actionable remediation
- Relay diagnostics and health checks
- iOS Background Refresh and BGContinuedProcessingTask support
- Full VoiceOver and Dynamic Type accessibility
- MPL-2.0 open source license
