# Sync Filters — UX Spec

> Status: **implemented** (issue [#1](https://github.com/psimaker/vaultsync/issues/1), shipped in v1.2.0)
> Last updated: 2026-05-09

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
│                                          │
│  Recommended                             │
│  ☑ Workspace state                       │
│    Prevents sync conflicts on which     │
│    notes were open.                      │
│  ☑ Trash                                 │
│    Files already deleted on other       │
│    devices.                              │
│                                          │
│  Found in this vault                     │
│  ☐ Git repository                        │
│    45.2 MB — 1,847 files                 │
│  ☐ Copilot index                         │
│    12.8 MB — 4,219 files                 │
│                                          │
│  Other presets                           │
│  ☐ macOS metadata                        │
│  ☐ Obsidian app cache                    │
│                                          │
│  Custom patterns                         │
│  *.tmp                                   │
│  Drafts/                                 │
│  [ Add pattern (e.g. *.tmp) ] [ Add ]    │
│                                          │
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
│                              [Skip] [Done]│
│                                          │
│  Skip these on this iPhone? You can     │
│  change this anytime in Sync Filters.    │
│                                          │
│  Recommended                             │
│  ☑ Workspace state                       │
│  ☑ Trash                                 │
│                                          │
│  Found in this vault                     │
│  ☑ Git repository    45.2 MB             │
│  ☑ Copilot index     12.8 MB             │
│                                          │
└─────────────────────────────────────────┘
```

Shown the **first time** a user opens a vault's detail screen, per vault. Persisted via a `UserDefaults` array of folder IDs that have been shown.

- **Done** — applies the checked presets/patterns to `.stignore` and dismisses.
- **Skip** — dismisses without changing `.stignore`. The folder is still marked as "seen", so the sheet won't reappear.
- Detected heavy folders are pre-checked but the user can uncheck before applying.

The Recommended set is also auto-applied silently when a new folder is added (so a fresh vault never syncs `workspace.json` even if the user instantly closes the sheet without tapping Done).

## 6. Conflict → Ignore

In `ConflictDiffView`, a new toolbar menu appears (top-right `⋯`):

```
⋯ menu
└─ Always skip on this iPhone
```

Tapping it adds the conflict's *exact relative path* to the folder's ignore list. Then a confirmation alert:

> "`'.obsidian/plugins/dataview/cache.db'` will no longer sync to this iPhone. You can undo this in Sync Filters."

Reasoning behind exact-path (not smart-glob): predictable. The user knows exactly what they ignored. If they later want to widen to `*.cache.db` or `.obsidian/plugins/dataview/*`, they can do that in the editor.

## 6.5 Multi-vault setups

In typical Obsidian use, the sync folder is the **Obsidian root** and individual vaults live as subdirectories inside it. Pattern matching handles this transparently: Syncthing automatically expands every unanchored pattern (anything without a leading `/`) to also match at any depth, so `.git` covers both `Obsidian/.git` and `Obsidian/Vault1/.git`. No `**/` prefix is needed in the preset definitions.

The vault scanner specifically descends one level into non-hidden subdirectories so that heavy folders inside vaults (e.g. `Obsidian/Personal/.git`, `Obsidian/Work/.git`) are detected and their sizes aggregated into a single "Found in this vault" entry per pattern.

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

All new strings shipped in English, German, and Simplified Chinese (the existing three locales).

## 10. Future considerations

Items that were deliberately scoped out of v1.2.0 and may make sense as follow-ups based on real-world usage feedback:

1. **Per-plugin cache presets** — A "Plugin caches" umbrella toggle was rejected as too coarse (different plugins store caches in different places). Concrete narrow presets for common offenders (e.g. Dataview index, Templater compiled cache) could be added if usage data shows they're frequently desired.

2. **Additional heavy folders for auto-detection** — The vault scan currently looks for `.git`, `.copilot-index`, `node_modules`, and `.obsidian/cache`. Extend `heavyDirCandidates` in `go/bridge/folderscan.go` if other large directories are commonly seen on mobile vaults.

3. **Alternative section naming** — "Sync Filters" was chosen over "Skip on this iPhone" as the section title. Re-evaluate based on user feedback if the term proves unclear.
