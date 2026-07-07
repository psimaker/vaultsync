# Changelog

All notable changes to VaultSync are documented here.

---

## [Unreleased]

### Changed

- **A vault you remove now stays removed** ([#52](https://github.com/psimaker/vaultsync/issues/52)) — as long as another device still shares a removed folder, its share request reappears within moments, and VaultSync used to accept it again automatically — silently undoing the removal. A share whose vault you removed now stays under "Pending Shares" until you accept it yourself. The recovery steps for overlapping vaults are now explicit for the same reason: remove the affected vault, then accept the returning share — nothing is re-added behind your back.

### Added

- **Choose where a share syncs** ([#52](https://github.com/psimaker/vaultsync/issues/52)) — a new "Choose Vault…" option on every pending share lets you pick an existing *empty* vault (create the vault in Obsidian first, then link the share to it) or create a folder with a name of your choice, instead of the automatic folder named after the share — share names like "Obsidian-Vault-Life" no longer dictate your local vault name. VaultSync remembers the choice: remove the vault and accept the share again, and it returns to the folder you picked — never silently to the automatic one. Locations that overlap another vault's folder are refused at every layer, and only empty vaults can be linked, so two vaults can never mix (the #45 guarantees are untouched). Localized in English, German, Spanish, and Simplified Chinese.

### Fixed

- **A share can no longer sync into a folder that already contains other files without asking** ([#54](https://github.com/psimaker/vaultsync/issues/54)) — if a folder under your Obsidian directory happened to have the same name as an incoming share (or the folder selected for VaultSync was itself a vault), the share was accepted straight into it — silently combining two sets of files and syncing the result to your other devices. VaultSync now stops before that happens: the share waits under "Pending Shares" with an explanation, and only you can approve the merge — in a dialog that spells out exactly what will be combined and where it will be synced. Approving is right when the folder holds this same vault's earlier notes (for example after removing the vault and accepting it again); for anything else, "Choose Vault…" picks a safe location. Enforced in the sync engine itself as well, so no code path can merge silently. Localized in English, German, Spanish, and Simplified Chinese.
- **Shares are no longer accepted while vault locations are still being checked after launch** ([#56](https://github.com/psimaker/vaultsync/issues/56)) — right after starting, VaultSync briefly re-checks where every vault lives on this iPhone (that is what repairs sync when iOS has moved the app's storage). A share arriving in exactly that window could be judged against the not-yet-repaired locations — in the worst case it took the very folder an existing vault was about to be restored to, mixing the two. All accept decisions — automatic and manual, including the merge confirmation — now wait until that check has finished: automatic accepts simply continue on their own a moment later, and tapping an accept button during the window asks you to try again in a moment. Nothing is ever moved automatically. Localized in English, German, Spanish, and Simplified Chinese.
- **Waiting shares are picked up right after reconnecting the Obsidian folder** ([#53](https://github.com/psimaker/vaultsync/issues/53)) — after re-selecting the Obsidian folder, a share that had been waiting (for example because the folder connection was broken, or because there was no safe location under the previously selected folder) was not retried until some unrelated event — often not before an app restart. Reconnecting now retries waiting shares immediately — and only after the vault locations have settled against the newly selected folder, so the safety checks judge against current locations, never stale ones.
- **Opening the app during a background sync no longer shows a stopped engine — or silently stops it** ([#60](https://github.com/psimaker/vaultsync/issues/60)) — when iOS woke VaultSync in the background (scheduled refresh, overnight catch-up, or a Cloud Relay wake-up) and you opened the app while that run was still going, the app failed to attach to the already-running sync engine: the vault list stayed empty and the status looked stopped — and when the background run then finished, it shut the engine down right under the open app, leaving sync dead until the next reopen. The app now adopts the running engine the moment it comes to the foreground: status and vaults load normally, the engine keeps running, and share accepts keep waiting until vault locations have settled, exactly as after a normal start.

## [1.7.2] — 2026-07-07

### Fixed

- **A new vault can no longer end up *inside* an existing vault** ([#45](https://github.com/psimaker/vaultsync/issues/45) follow-up) — when the folder selected for VaultSync was itself a vault (instead of the Obsidian folder that contains your vaults), 1.7.1 gave a second share a subfolder *inside* that vault. Obsidian then treated the new vault as part of the old one, and the outer vault synced the inner vault's notes to its own devices — deleting that stray copy anywhere would have deleted the inner vault everywhere. Folder locations that overlap an existing vault's folder are now refused at every layer (share auto-accept, the sync engine itself), and the app explains the fix: select the folder that *contains* your vaults ("On My iPhone" → "Obsidian") — every new share then syncs into its own folder next to the others.
- **Vaults already nested by 1.7.1 are now caught and paused** — the launch-time shield that catches same-folder merges now also detects one vault's folder lying inside another's, pauses both once to stop the mixing, and shows a critical "One Vault Is Nested Inside Another" prompt with the recovery steps (re-select the container folder, remove the inner vault so it is re-added into its own folder, and only then clean up the leftover copy). Localized in English, German, Spanish, and Simplified Chinese.
- **Re-selecting a different Obsidian folder no longer tries to re-point healthy vaults** — after changing VaultSync's Obsidian folder, the launch-time path repair could try to move a vault that was still syncing fine onto the newly selected location (the engine's safety marker check blocked it, but it retried with a warning on every launch). A vault whose folder is alive on disk now always keeps its location, and its stored position is refreshed from reality instead.
- **Picking a single vault as the Obsidian folder now says so up front** — selecting a folder that is itself a vault still works for that one vault, but the app now explains immediately that additional vaults will need the containing folder, instead of failing later when the second share arrives.

## [1.7.1] — 2026-06-15

### Fixed

- **Multiple vaults from one server no longer merge into the same folder** ([#45](https://github.com/psimaker/vaultsync/issues/45)) — when a server shared more than one vault, the second vault could be assigned the same local location as the first, so Syncthing merged their contents and pushed the mixed result back to both peers — silently. Each vault now always syncs into its own folder under your Obsidian directory, and two vaults can never be pointed at the same location (a same-named second vault gets its own distinct folder instead of being merged). Existing single-vault setups are unchanged.
- **Vaults an older version already merged into one folder are now caught and paused** ([#45](https://github.com/psimaker/vaultsync/issues/45)) — the change above stops *new* collisions, but a device that had already merged two vaults onto one folder under 1.6.0 or 1.7.0 kept mixing them on every sync. VaultSync now detects that on launch, pauses the affected vaults once to stop the bleeding (and never re-pauses one you deliberately resume to recover), and shows a clear, critical prompt for separating them: remove the affected vault and it is re-added into its own folder — restore the clean copy on your computer first if the files are already mixed. Localized in English, German, Spanish, and Simplified Chinese.
- **Pasting a block of sync filters now works** ([#43](https://github.com/psimaker/vaultsync/issues/43)) — the Sync Filters "Add pattern" field was single-line, so pasting a multi-line list of patterns collapsed into one unusable entry — and because such a paste usually begins with a `//` comment, it silently matched nothing. The field is now multi-line and adds **one pattern per line**: blank lines and `//` comments are skipped, and patterns that contain spaces stay intact.
- **Editing one filter no longer reshuffles the rest** — removing a custom pattern, or turning off a detected one, used to rewrite the entire `.stignore` in an arbitrary order. Syncthing applies patterns top to bottom (so an earlier `!`-include can override a later rule), so the original line order is now preserved on every change.

### Changed

- **More reliable local discovery on the same Wi-Fi** — now that Apple has approved the multicast networking entitlement, VaultSync uses multicast for Syncthing's local peer discovery, so your other devices on the same network are found directly and quickly instead of leaning on global discovery or a relay. This completes the local-network work started in 1.7.0 (the Local Network permission and direct dialing).

## [1.7.0] — 2026-06-12

### Added

- **Settings conflicts now resolve themselves** — conflicts in Obsidian's app settings and plugin state (anything inside `.obsidian/`) are resolved automatically, the newest version wins. These files change constantly on every device and were the main source of "Conflicts Need Resolution" noise; your notes are never touched — a real note conflict still waits for your decision. Runs during normal use and before background-sync notifications, so the iPhone no longer lights up for conflicts the app can settle on its own. Opt out anytime in Settings → Conflicts. Localized in English, German, Spanish, and Simplified Chinese.

### Changed

- **Cloud Relay now reads as the one-step setup it is** — the in-app server setup screen leads with the single installer line and folds the manual `docker run` path into a collapsed "Manual & advanced setup" section that links to the full guide; the Relay pitch and the "not active yet" state now say plainly that a single line on your server finishes the job. The README and the helper guide follow the same shape: the one-liner front and center, every manual path (Docker Compose, `docker run`, prebuilt binaries, environment variables) behind expandable sections. Localized in English, German, Spanish, and Simplified Chinese.
- **Calmer conflict counts** — the home-screen issue banner, vault badges, and conflict notifications now count conflicted *files* instead of every conflict copy, so one churn-prone file with many saved copies no longer reads as "10 conflicts" when there is a single decision to make.

- **Much faster reconnect after a cold start** — three layers: the app now asks for the iOS *Local Network* permission — without it, iOS blocks direct connections to devices on the same Wi-Fi and that traffic detours through a relay; VaultSync remembers where each device was last reached and dials that address immediately on launch instead of waiting for a discovery round trip; and the embedded sync engine retries failed dials every second (was 5 s) while warming up and gives up on dead addresses twice as fast, so the relay fallback kicks in sooner when it is genuinely needed.
- **A calm launch instead of false alarms** — reconnecting after a cold start is normal warm-up, and the app now treats it that way. The status header keeps its positive state with a small spinner and "Connecting to *device*…" instead of switching to "Reconnecting…"; the dashboard shows a neutral "Connecting to devices…" instead of an orange "0 of 1 devices connected"; and the Devices list shows a spinner + "Connecting…" while a reconnect is in progress. A device that stays away reads as a neutral gray "Offline" (the ✕ badge is gone — disconnected peers are a normal state for an offline-first sync tool), and paused devices are labeled "Paused". After a cold start, nothing is reported as a problem for 60 seconds (mid-session disconnects keep the 30-second window). Localized in English, German, Spanish, and Simplified Chinese.
- **Smoother launch** — the sync engine's startup work (certificates, config, index database) no longer runs on the UI thread.

## [1.6.0] — 2026-06-10

### Added

- **Cloud Relay server setup is now one line** — `curl -fsSL https://vaultsync.eu/notify.sh | sh` on the server replaces the edit-this-command Docker snippet: the installer finds Syncthing's `config.xml` on its own, runs the helper as exactly the user that owns it (the #1 setup failure), starts it via Docker — or, without Docker, installs a prebuilt binary behind a systemd service (Linux) or launchd agent (macOS) — and finishes with the helper's `--doctor` preflight so problems surface immediately with the fix spelled out. Skeptics can append `--dry-run` to preview every action without changing anything. The in-app setup screen now leads with the one-liner; the manual `docker run` command remains as the alternative. Localized in English, German, Spanish, and Simplified Chinese.
- **Prebuilt `vaultsync-notify` binaries** — every `notify-v*` release now ships static helper binaries for linux/amd64, linux/arm64, macOS (Intel and Apple silicon), and Windows, with a `SHA256SUMS` file — running the helper without Docker no longer requires a Go toolchain.
- **Helper permission errors now spell out the exact fix** — when the helper cannot read `config.xml`, the error names the file's actual owner and the exact `-u uid:gid` flag to use, instead of a generic "match the owner" hint (it also resolves the owner through an unreadable config directory).
- **Missed wake-ups now catch up on their own** — A device that misses a Cloud Relay wake-up (offline too long, push expired) no longer stays stale until the next vault change: the server helper (`vaultsync-notify`) now re-sends a wake-up on a slow cadence while any of your devices still needs data (default every 6 hours; `STALE_RETRIGGER_SECONDS`, `0` disables). Fully synced devices never cause a push, and a wake-up that is already on its way is never duplicated.
- **Overnight catch-up sync** — VaultSync now also schedules a long-running background task that iOS runs while the iPhone is charging with a network connection — typically overnight. It gets a multi-minute budget instead of the ~30 seconds of a normal background refresh, so large catch-ups complete on the charger instead of timing out.
- **See what Cloud Relay actually delivers** — Relay Diagnostics now counts the wake-ups received in the last 7 days (stored only on your device, never reported anywhere), warns live when Low Power Mode is deferring silent pushes, and explains the most common silent killer: force-quitting VaultSync from the app switcher stops all wake-ups until the next manual launch. Localized in English, German, Spanish, and Simplified Chinese.
- **Instant iPhone → server uploads, automated** — A new guide ([docs/instant-upload.md](docs/instant-upload.md)) shows the one-time Shortcuts automation that opens VaultSync every time you leave Obsidian, so your edits reach the server seconds after you close the app. The home-screen and lock-screen widgets are already tap-to-sync.

### Changed

- **A calmer, more consistent home screen** — the dashboard's Cloud Relay hints, sync errors, and the Obsidian connection prompt now share the app's standard card and row styles, and every error card carries a real action button instead of prose directions. Vaults get the same full-size status treatment as devices, with conflict counts as a clear badge.
- **Friendlier first run** — "no vaults yet" and "no devices yet" are now proper empty-state screens with guidance and an **Add Device** button; adding a device moved to the navigation bar of the Devices tab.
- **The Cloud Relay offer no longer switches tabs on its own** — after your first successful sync it appears as a dismissable card on the Sync tab (**View Cloud Relay** / **Not now**) instead of pulling you out of what you were doing.
- **Tappable setup checklist** — checklist steps with an in-app fix (connect your Obsidian folder, add a device, open the Relay tab) now carry a button that takes you straight there.
- **Design and accessibility polish** — onboarding, badges, spacing, and monospaced text now come from the shared design tokens (correct in light, dark, and increased-contrast mode), action buttons meet the 44pt touch-target minimum, renaming a device shows a saved confirmation, and the widget's last-sync time is labeled for VoiceOver. Localized in English, German, Spanish, and Simplified Chinese.
- **Honest self-hosted relay documentation** — The relay specification no longer advertises a free self-hosted relay tier as roadmap. It now explains the real constraint: Apple's push service only accepts wake-ups for the App Store app signed with VaultSync's own key, which can never be distributed. Building the entire stack from source with your own Apple Developer account remains possible under MPL-2.0.

## [1.5.1] — 2026-06-01

### Added

- **Cloud Relay activates itself** — after you subscribe, VaultSync registers your devices with the relay automatically, and the server helper (`vaultsync-notify`) now confirms activation on its own: the moment it starts it sends one wake-up, and the app flips to **Cloud Relay active** on its own the moment that wake-up arrives — no extra step or change needed. A reactivation card reaches subscribers whose server has never woken their iPhone, and the Cloud Relay screen shows honest live states — **not active yet**, **active**, or **went quiet** — instead of guessing. Localized in English, German, Spanish, and Simplified Chinese.
- **No API key to copy when setting up the server helper** — `vaultsync-notify` reads the Syncthing API key (and address) straight from Syncthing's `config.xml`, so server setup no longer asks you to open the Syncthing web UI and paste a key. The in-app command and `docker compose up -d` are key-free; running next to Syncthing, the only value you supply is the relay URL (which stays explicit, so the helper never wakes a relay you didn't choose). A first-boot wait lets the helper ride out a fresh `docker compose up` while Syncthing is still writing its config.

### Fixed

- **A vault stuck on "VaultSync cannot access this folder" can now recover** ([#25](https://github.com/psimaker/vaultsync/issues/25)) — iOS can change an app's private storage location (on reinstall, restore, or device migration), which left an older vault pointing at a path that no longer exists and stuck in a permanent error with no way out. VaultSync now re-derives every vault's location from your Obsidian folder each time it starts: a change to the app's own storage is repaired automatically, and after a device restore or migration you reconnect the Obsidian folder once and every vault is rebased onto its existing data — no re-download and no lost history. A vault that still can't be reached shows a clear **Remove this vault** (and, where possible, **Reconnect to Obsidian**) instead of an inert error, and every vault detail screen gained a **Remove Vault** action. Localized in English, German, Spanish, and Simplified Chinese.

### Changed

- **Sync Filters are safer to write** — applying the default ignore patterns now reads, merges, and writes your `.stignore` in one step and refuses to write if it can't first read the current rules, so a transient read error can never overwrite your custom filters with just the defaults.

## [1.5.0] — 2026-05-31

### Added

- **A complete visual redesign** — VaultSync moves to a coherent design system: a single brand accent (instead of stray system blue), a status palette that resolves correctly in light and dark mode, and a shared component kit used across the app and the home-screen widget.
- **Tabbed home screen** — the single overloaded screen is split into **Sync**, **Devices**, and **Cloud Relay** tabs, led by a persistent status header that states one glanceable truth ("All Synced" / "Syncing…" / "Needs Attention").
- **Onboarding that does it for you** — onboarding steps now launch the real task (choose your Obsidian folder, pair a device, scan a QR code) and turn green as you complete them, instead of describing setup in prose.
- **Guided Cloud Relay server setup** — Cloud Relay needs a small helper (`vaultsync-notify`) on your server, and the app now says so clearly. A new **Set Up Your Server** screen explains the step and offers a copyable one-line command (with the relay URL pre-filled); it appears right after you subscribe and from the Cloud Relay tab. Localized in English, German, Spanish, and Simplified Chinese.
- **Yearly Cloud Relay plan** — Cloud Relay is now available as a yearly subscription in addition to monthly, at a lower effective monthly price. Both prices are read from StoreKit and shown correctly per storefront.
- **In-context Cloud Relay offer** — After your first successful sync, VaultSync offers Cloud Relay in context (with a one-tap path into server setup), and the home screen shows an unobtrusive upgrade row for non-subscribers.

### Changed

- **Cloud Relay has its own tab** — Cloud Relay moved out of Settings into a dedicated tab that brings the subscribe offer, server-helper setup, delivery status, diagnostics, and manage-subscription together. When you're not subscribed it leads with a focused, privacy-first pitch — a tiny wake-up on top of your already-free peer-to-peer sync, not cloud storage — and lists the monthly plan first with the yearly plan shown as savings.
- **Status is never color-only** — every sync state pairs an icon and a text label with its color, so it is clear for VoiceOver and color-blind users and reads identically on the home screen, the activity log, and the widget.
- **Clearer conflict resolution and honest progress** — Keep This / Keep Both / Keep Other are full-width buttons that always confirm before changing any files (previously "Keep Both" applied with none); the home screen lists your actual Obsidian vaults instead of the raw sync folder; and the vault rescan reflects the real scan state instead of a fixed timer.
- **Honest Cloud Relay status** — The setup checklist and the Cloud Relay tab no longer call Cloud Relay "ready" just because you subscribed. They now reflect real delivery: *waiting for your server* until a wake-up actually arrives, then *delivering wake-ups*.
- **Cloud Relay monthly price** — The monthly price was raised; the app always shows the live, storefront-correct price from StoreKit and never hard-codes an amount.
- **Verified subscriptions** — The relay now verifies the App Store signed transaction against Apple's certificate chain and enforces the subscription expiry server-side, so an expired or cancelled subscription stops receiving wake-ups.

### Fixed

- **Cloud Relay server helper no longer crash-loops** — when a subscription is inactive the relay replies with a 4xx; the `vaultsync-notify` helper treated that as fatal and, under `restart: unless-stopped`, restarted in a loop. It now logs the response and keeps running.
- **The widget can't show a false "all good"** — an unrecognised sync status now surfaces as *needs attention* instead of silently falling back to the green idle state, and the widget gained VoiceOver labels.
- **Localization** — the redesign's new strings are translated to German, Spanish, and Simplified Chinese with full key parity across all four languages, and existing translation errors and relay terminology drift were corrected.
- **iPhone and iPad both get wake-ups** — When an iPhone and iPad shared the same server, they could displace each other's push registration so only one received Cloud Relay wake-ups. Both are now kept, and tokens Apple reports as invalid are cleaned up automatically.

## [1.4.0] — 2026-05-30

### Added

- **Turn off conflict notifications** ([#10](https://github.com/psimaker/vaultsync/issues/10)) — A new Settings → Notifications toggle mutes the sync-conflict banner without touching anything else. It gates only the banner, so Cloud Relay wake-ups and background sync keep working, and turning it off no longer requires disabling iOS notifications for the whole app. Defaults on; existing installs keep their current behaviour.
- **Support VaultSync** — An optional "Support VaultSync" section in Settings offers two one-time contributions (Small and Big). They unlock nothing — VaultSync stays fully functional without them — and you can contribute as often as you like. Localized for English, German, Spanish, and Simplified Chinese.
- **Spanish localization** — VaultSync is now fully localized in Spanish (`es`), joining English, German, and Simplified Chinese across the app and the home-screen widget.

### Changed

- **Cloud Relay price shown correctly per region** — The subscribe button and the subscription details previously mixed the localized App Store price with a hard-coded "$0.99/month", so non-US storefronts saw two different currencies. Both now show a single price taken straight from StoreKit (for example "0,99 € / month" or "A$1.99 / month"), so the displayed price is always correct for the user's storefront.
- **Clearer Support and Cloud Relay wording** — The Settings → Support footer now frames a contribution around keeping VaultSync independent, open-source (MPL-2.0), and ad-free — it still unlocks nothing. The Cloud Relay description explains the silent-push wake-up more plainly and honestly: changes on your server wake the app the moment they happen so incoming sync feels instant, and the relay only sends a wake-up signal — it never sees your notes. The German, Spanish, and Simplified Chinese translations were polished for terminology and punctuation consistency in the same pass.

### Fixed

- **Conflict notifications no longer spam the screen** ([#10](https://github.com/psimaker/vaultsync/issues/10)) — The conflict banner was posted with a fresh identifier on every background sync that reached idle, so iOS never coalesced them: the same "28 conflicts" message re-lit the screen over and over and drained battery. VaultSync now uses a stable notification that replaces itself in place, only alerts (with sound) when the conflict count actually grows, refreshes quietly when it shrinks, and clears when the last conflict is resolved.
- **Disabling notifications no longer reports Cloud Relay as broken** ([#10](https://github.com/psimaker/vaultsync/issues/10)) — Silent push wake-ups do not need alert permission, but the app used to flag APNs/relay as "failed" purely because notifications were turned off, cascading a misleading "relay broken" message across diagnostics. Relay health is now judged from the things that actually matter (subscription, APNs token, provisioning, and a recent silent-push trigger), and the alert-permission state is shown as a separate, informational row.
- **More reliable background sync** — Two background wake-ups that fired at the same time could tear down each other's sync mid-transfer; a single-flight guard now prevents that. The silent-push time budget is measured from the start of the run so long setups can't overrun iOS's window and get future wake-ups throttled. And a folder stuck in an error state no longer spins out the full background deadline or raises a misleading "Background Sync Timed Out".
- **Localized conflict notification text** — The conflict banner body was English-only on German and Simplified Chinese devices; it is now translated.

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
