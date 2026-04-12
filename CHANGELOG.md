# Changelog

All notable changes to VaultSync are documented here.

---

## [1.0.2] — 2026-04-12

### Fixed

- **Silent-push sync reliability** — Silent pushes from Cloud Relay are now reliably acted upon even after iOS has suspended the process. A stale lifecycle lock previously caused the background sync handler to skip the bridge restart, leaving dead TCP sockets unreconnected. Pushes delivered but never produced a sync.
- **iOS suspend grace period** — The app now acquires a `UIApplication` background-task assertion when entering the background, giving pending Syncthing operations up to ~30 seconds to complete instead of being suspended within ~5 seconds.
- **Relay DB-reset recovery** — Re-provisioning interval reduced from 24 hours to 6 hours, and a re-provision probe is now triggered whenever Relay Diagnostics is opened. Restores push delivery automatically within 6h after a server-side token reset (e.g. from self-healing cleanup).
- **Cloud Relay push expiration** — Silent pushes were sent with `apns-expiration=0`, causing APNs to drop them after a single failed delivery attempt when the iPhone was briefly unreachable. Expiration is now set to +1 hour so APNs retries delivery until the device wakes.
- **Vault path nesting** — Accepting a pending share no longer creates a redundant subdirectory when the selected Obsidian root is itself a vault or when its folder name matches the share label (case-insensitive). Previously selecting `On My iPhone/Obsidian/` with a desktop share labelled `obsidian` produced `Obsidian/obsidian/` — Obsidian then couldn't see the synced files as part of the vault.
- **Background sync completion detection** — The idle-state check used by the silent-push and BGAppRefresh handlers now verifies that folders have no outstanding `needFiles`, `needBytes`, or `inProgressBytes` before declaring success. Previously Syncthing's momentary `idle` state between scan and sync phases was treated as "done", causing the handler to shut Syncthing down before any file was actually pulled. This matches the peer-side observation of connections lasting only ~1 second after a silent push.
- **vaultsync-notify event filter** — The relay trigger now fires only on `ItemFinished` events. `StateChanged` previously produced multiple pushes per actual file change (one per `idle→scanning→syncing→idle` transition), accelerating iOS silent-push throttling and reducing delivery reliability.

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
