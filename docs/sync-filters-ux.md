# Sync Filters — UX Spec

> Status: **draft for review** (issue [#1](https://github.com/psimaker/vaultsync/issues/1))
> Last updated: 2026-05-09

This document describes the planned UI for excluding files and folders from sync — the feature requested in issue #1 by @vitaly74. It exists so we can agree on the design *before* writing implementation code.

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
2. **Found in this vault** — only renders when the vault scan returned results. Shows actual byte size + file count for each detected heavy folder. The most persuasive piece of UI.
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

## 10. Open questions for @vitaly74

Three things I'd love your input on before I start building:

1. **Plugin caches** — you mentioned `.copilot-index` (already covered as its own preset). Are there other specific plugin caches you regularly run into on mobile? E.g. Dataview index, Templater compiled cache, etc. I'd rather ship 2–3 narrow presets than one "Plugin caches" toggle that does the wrong thing.

2. **Other heavy folders to auto-detect** — the vault scan currently looks for `.git`, `.copilot-index`, `node_modules`, and `.obsidian/cache`. What else have you seen eat space on a mobile vault?

3. **Naming** — does "Sync Filters" feel right to you? An alternative I considered was "Skip on this iPhone" as the section title (more direct, less jargon), with "Filters" as a fallback. No strong opinion either way.

Anything else missing? The whole point of doing this as a draft PR is to catch design issues before code lands.
