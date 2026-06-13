# Sync Filters — UX Spec

> **Internal design reference — not user documentation.** This captures the rationale, layout, and trade-offs behind the Sync Filters feature for maintainers extending it.
> Status: **implemented** (issue [#1](https://github.com/psimaker/vaultsync/issues/1), shipped in v1.2.0; Conflict→Skip extended to Skip Family in v1.3.2, issue [#8](https://github.com/psimaker/vaultsync/issues/8); conflict auto-resolution for `.obsidian` state files added in v1.7.0, §6.6; multi-line paste + order-preserving filter writes in v1.7.1, issue [#43](https://github.com/psimaker/vaultsync/issues/43), §6.7). Current app: v1.7.1.
> Last updated: 2026-06-13

This document is the design reference for the Sync Filters feature — the UI for excluding files and folders from sync requested in issue #1 by @vitaly74. It captures the rationale behind the layout, preset catalog, migration path, and multi-vault behavior; refer to it when extending or modifying the feature.

---

## 1. Why

The Syncthing engine already supports per-folder ignore patterns via `.stignore` files. VaultSync's Go bridge already exposes them (`GetFolderIgnores`, `SetFolderIgnores`). What's missing is the **UI** — without it, users can't see, add, or remove patterns from inside the app.

The goal isn't to expose raw Syncthing pattern syntax. The goal is **"keep this off my iPhone"** in plain language. Most users don't know what a glob pattern is, but they do know that `.git` is taking 45 MB and they don't need it on mobile.

## 2. Where it lives

Per-vault, on the existing vault detail screen. A new `Sync Filters` link appears in `vaultDetailView` between the Conflicts and Shared With sections:

```
Vault                    (name, path)
Sync Status              (state, completion, errors)
Conflicts                (when present)
► Sync Filters           ← new
Shared With              (devices)
Rescan Vault
```

Position is intentional: filters are configuration, sharing/rescan are actions. Right after Conflicts means a user who just resolved a `workspace.json` conflict sees the link to "stop this from happening again" immediately below.

## 3. The screen — `IgnorePatternsView`

```
┌─ Sync Filters — "My Vault" ─────────────┐
│                                         │
│  Recommended                            │
│  ☑ Workspace state                      │
│    Prevents sync conflicts on which      │
│    notes were open.                     │
│  ☑ Trash                                │
│    Files already deleted on other       │
│    devices.                             │
│                                         │
│  Found in this vault                    │
│  ☐ Git repository                       │
│    45.2 MB — 1,847 files                 │
│  ☐ Copilot index                        │
│    12.8 MB — 4,219 files                 │
│                                         │
│  Other presets                          │
│  ☐ macOS metadata                       │
│  ☐ Obsidian app cache                   │
│                                         │
│  Custom patterns                        │
│  *.tmp                                  │
│  Drafts/                                │
│  [ Add pattern (e.g. *.tmp) ] [ Add ]   │
│                                         │
│  How filters work →                      │
└─────────────────────────────────────────┘
```

Five sections, each rendered as a `List` section:

1. **Recommended** — always visible. Workspace state + Trash, both ON by default for new vaults.
2. **Found in this vault** — only renders when the vault scan returned results. Shows actual byte size + file count for each detected heavy folder. The scanner checks both the sync folder root and one level deep (the typical "Obsidian root with vault subdirs" layout) and aggregates matches per pattern (e.g. ".git in 3 vaults — 127 MB total"). The most persuasive piece of UI.
3. **Other presets** — every preset that isn't already in Recommended or Found.
4. **Custom patterns** — anything in `.stignore` that isn't part of any preset. User can swipe-to-delete or add a new line.
5. **Footer** — link to the Syncthing pattern docs for power users.

All toggles write through to `.stignore` immediately. No save button.

## 4. Preset catalog

| ID | Label | Patterns | Default in sheet |
|---|---|---|---|
| `workspace` | Workspace state | `.obsidian/workspace.json`, `.obsidian/workspace-mobile.json` | **ON** |
| `trash` | Trash | `.Trash` | **ON** |
| `git` | Git repository | `.git` | OFF (auto-on if scan finds it) |
| `macos` | macOS metadata | `.DS_Store`, `._*` | OFF |
| `copilot` | Copilot index | `.copilot-index` | OFF (auto-on if scan finds it) |
| `obsidianCache` | Obsidian app cache | `.obsidian/cache` | OFF |

Two presets that the issue thread mentioned but I'm **not** including in the initial catalog:

- **"Plugin caches"** as a single preset is too coarse. Different plugins store caches in different places (Dataview's `cache.db`, Copilot's `.copilot-index`, etc.). Bundling them all under one toggle either misses real caches or accidentally excludes plugin data the user wants. I'd rather ship specific presets per plugin than one fuzzy bucket.
- **General `.gitignore` / `.gitattributes`** — these are tiny text files most users want to sync (config-like). The `git` preset only excludes the `.git/` directory itself.

## 5. First-run recommendation sheet

```
┌─ Sync Filters ──────────────────────────┐
│                            [Skip] [Done]│
│                                         │
│  Skip these on this iPhone? You can     │
│  change this anytime in Sync Filters.   │
│                                         │
│  Recommended                            │
│  ☑ Workspace state                      │
│  ☑ Trash                                │
│                                         │
│  Found in this vault                    │
│  ☑ Git repository    45.2 MB            │
│  ☑ Copilot index     12.8 MB            │
│                                         │
└─────────────────────────────────────────┘
```

Shown the **first time** a user opens a vault's detail screen, per vault. Persisted via a `UserDefaults` array of folder IDs that have been shown.

- **Done** — applies the checked presets/patterns to `.stignore` and dismisses.
- **Skip** — dismisses without changing `.stignore`. The folder is still marked as "seen", so the sheet won't reappear.
- Detected heavy folders are pre-checked but the user can uncheck before applying.

The Recommended set is also auto-applied silently when a new folder is added (so a fresh vault never syncs `workspace.json` even if the user instantly closes the sheet without tapping Done).

## 6. Conflict → Ignore

In `ConflictDiffView`, a toolbar menu appears (top-right `⋯`):

```text
⋯ menu
└─ Always skip on this iPhone
```

Tapping it performs a **Skip Family** action (added in v1.3.2, see issue [#8](https://github.com/psimaker/vaultsync/issues/8)):

1. Writes a *pair* of patterns to `.stignore`: the file's exact relative path and a matching `<path>.sync-conflict-*` glob.
2. Deletes any sync-conflict copies of that file currently on disk.
3. Rescans the folder and refreshes the conflict cache so the conflict disappears from the home-screen Sync Issues list immediately.

Confirmation alert:

> "`'.obsidian/plugins/dataview/cache.db'` and its conflict copies will no longer sync to this iPhone. You can undo this in Sync Filters."

If existing conflict copies were removed, a second line is appended:

> "2 existing conflict copies were removed."

Reasoning behind the family approach: the v1.2.0 design used an exact-path pattern for predictability, but that left a hole — a fresh `sync-conflict-…` copy with a new timestamp would arrive from the desktop and the conflict reappeared. Pairing the original path with the conflict-copy glob makes "skip" actually mean skip, without sacrificing predictability: the two `.stignore` lines are still plain, no smart-glob heuristics, no hidden state. In the Sync Filters list the pair is presented as a single row with a `+ conflict copies` caption.

The original file itself is **not** deleted from disk — only the conflict-copy variants. Users who later want to revert can swipe-to-delete the row in Sync Filters; both lines are removed atomically.

## 6.5 Multi-vault setups

In typical Obsidian use, the sync folder is the **Obsidian root** and individual vaults live as subdirectories inside it. Pattern matching handles this transparently: Syncthing automatically expands every unanchored pattern (anything without a leading `/`) to also match at any depth, so `.git` covers both `Obsidian/.git` and `Obsidian/Vault1/.git`. No `**/` prefix is needed in the preset definitions.

The vault scanner specifically descends one level into non-hidden subdirectories so that heavy folders inside vaults (e.g. `Obsidian/Personal/.git`, `Obsidian/Work/.git`) are detected and their sizes aggregated into a single "Found in this vault" entry per pattern.

## 6.6 Conflict auto-resolution (v1.7.0)

Presets prevent the *predictable* conflict sources, but `.obsidian` holds many
more files that churn on every device (`app.json`, `graph.json`, per-plugin
`data.json`, …) — and ignoring all of them by default would also stop plugin
*settings* from syncing, which users do want. So v1.7.0 attacks the noise from
the other side:

- **Auto-resolve, not ignore.** Conflict copies of any file inside a
  `.obsidian` directory (any depth — covers vault-subdir layouts) are resolved
  automatically with last-writer-wins: the newer mtime becomes the original,
  the loser is deleted. A copy whose original vanished is promoted instead of
  deleted, so nothing is ever lost. Implemented in the Go bridge
  (`AutoResolveStateConflicts`, `go/bridge/conflicts.go`); triggered from the
  foreground 2s poll (gated on the conflict scan actually containing a state
  conflict) and from the background-sync path *before* the conflict
  notification fires.
- **Notes are exempt by design.** Anything outside `.obsidian` keeps the full
  manual flow (diff view, Keep This/Other/Both, Skip Family) — last-writer-wins
  on user content would mean silent data loss.
- **Opt-out, not opt-in.** Settings → Conflicts → "Auto-Resolve Settings
  Conflicts" (`SyncthingManager.autoResolveStateConflictsKey`, missing key =
  ON). Users who turn it off see state conflicts in the normal manual flow
  again.
- **Counts mean files now.** The home banner, vault badges, and notifications
  count distinct conflicted files instead of conflict copies — with
  `MaxConflicts: 10` a single churn-prone file used to read as "10 conflicts".

## 6.7 Multi-line paste & order-preserving writes (v1.7.1)

Two fixes from issue [#43](https://github.com/psimaker/vaultsync/issues/43):

- **The "Add pattern" field is multi-line.** It was a single-line `TextField`,
  so iOS flattened a pasted multi-line `.stignore` block into one line
  (newlines → spaces) and stored it as a single custom pattern — usually inert,
  since such a paste typically starts with a `//` comment (which Syncthing
  treats as a comment line). The field now uses `axis: .vertical`, and input is
  parsed by `IgnorePatternInput.parse` (`Models/SkipFamily.swift`): split on
  newlines only (so patterns containing spaces survive), trimmed, with blank and
  `//` comment lines dropped. Patterns are written in one ordered, de-duplicated
  pass via `SyncthingManager.addIgnorePatterns`.
- **Deletes preserve `.stignore` order.** `deleteCustom` and the detected-pattern
  off-toggle rebuilt the file from an unordered `Set`, reshuffling line order on
  every change. Since Syncthing matches first-pattern-wins (an earlier
  `!`-include can override a later rule), both paths now route through
  `SyncthingManager.removeIgnorePatterns`, which reads the file, removes the
  targeted lines, and keeps the order of the rest.

Removal remains swipe-to-delete on the custom row (§3, item 4); no visible
delete button was added.

## 7. Migration

For users updating from a current build:
- The 3 silent default patterns (`.Trash`, `.obsidian/workspace.json`, `.obsidian/workspace-mobile.json`) **stay on disk untouched**.
- The new derived state automatically shows "Workspace state" and "Trash" as ON.
- No migration sheet. No disk changes. No surprise.

For new vaults added after this lands:
- Recommended presets are silently applied (same as before — keeps `workspace.json` from generating immediate conflicts).
- The first-run sheet appears on first vault-detail open, with Recommended already checked and any scan results pre-checked.

## 8. Naming

Throughout the app:
- Section title: **"Sync Filters"**
- CTAs and copy: **"Skip on this iPhone"**, **"Always skip on this iPhone"**, **"Choose what gets synced to this iPhone"**

Avoiding:
- "Ignore patterns" — Syncthing-jargon, users don't think in patterns
- "Exclusions" — corporate-y
- "Filter rules" — too abstract

## 9. Localization

All new strings shipped in English, German, Spanish, and Simplified Chinese (the four shipping locales).

## 10. Future considerations

Items that were deliberately scoped out of v1.2.0 and may make sense as follow-ups based on real-world usage feedback:

1. **Per-plugin cache presets** — A "Plugin caches" umbrella toggle was rejected as too coarse (different plugins store caches in different places). Concrete narrow presets for common offenders (e.g. Dataview index, Templater compiled cache) could be added if usage data shows they're frequently desired.

2. **Additional heavy folders for auto-detection** — The vault scan currently looks for `.git`, `.copilot-index`, `node_modules`, and `.obsidian/cache`. Extend `heavyDirCandidates` in `go/bridge/folderscan.go` if other large directories are commonly seen on mobile vaults.

3. **Alternative section naming** — "Sync Filters" was chosen over "Skip on this iPhone" as the section title. Re-evaluate based on user feedback if the term proves unclear.
