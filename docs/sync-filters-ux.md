# Sync Filters — UX Spec

> **Internal design reference — not user documentation.** This captures the rationale, layout, and trade-offs behind the Sync Filters feature for maintainers extending it.
> Status: **implemented** (issue [#1](https://github.com/psimaker/vaultsync/issues/1), shipped in v1.2.0; Conflict→Skip extended to Skip Family in v1.3.2, issue [#8](https://github.com/psimaker/vaultsync/issues/8); automatic `.obsidian` conflict resolution added in v1.7.0 and retired under issue [#145](https://github.com/psimaker/vaultsync/issues/145), §6.6; the still-open fail-closed release requirement is tracked under issues [#150](https://github.com/psimaker/vaultsync/issues/150) and [#167](https://github.com/psimaker/vaultsync/issues/167), §6 and §6.6; multi-line paste + order-preserving filter writes in v1.7.1, issue [#43](https://github.com/psimaker/vaultsync/issues/43), §6.7). Current shipped app: v2.0.1.
> Last updated: 2026-08-28

This document is the design reference for the Sync Filters feature — the UI for excluding files and folders from sync requested in issue #1 by @vitaly74. It captures the rationale behind the layout, preset catalog, migration path, and multi-vault behavior; refer to it when extending or modifying the feature.

---

## 1. Why

The Syncthing engine supports per-folder ignore patterns via `.stignore` files, exposed by VaultSync's Go bridge through `GetFolderIgnores` and `SetFolderIgnores`. In 2.0.2, only existing Send Only folders may read or edit Sync Filters; receive-capable and unknown folder modes stop before filter access or mutation.

The goal isn't to expose raw Syncthing pattern syntax. The goal is **"keep this off my iPhone"** in plain language. Most users don't know what a glob pattern is, but they do know that `.git` is taking 45 MB and they don't need it on mobile.

## 2. Where it lives

Per-vault, on the existing vault detail screen. A new `Sync Filters` link appears in `vaultDetailView` between the Conflicts and Shared With sections:

```
Vault                    (name, path)
Sync Status              (state, completion, errors)
Conflicts                (inspection only when present)
► Sync Filters           (editable for Send Only only in 2.0.2)
Shared With              (devices)
Rescan Vault             (Send Only only in 2.0.2)
```

Position is intentional: filters are configuration, sharing/rescan are actions. Right after Conflicts keeps prevention controls near the copies a user has just inspected, without implying that 2.0.2 can resolve those conflicts.

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

1. **Recommended** — always visible. Workspace state + Trash are preselected in the first-run recommendation sheet; the regular list reflects only patterns actually stored in `.stignore`.
2. **Found in this vault** — only renders when the vault scan returned results. Shows actual byte size + file count for each detected heavy folder. The scanner checks both the sync folder root and one level deep (the typical "Obsidian root with vault subdirs" layout) and aggregates matches per pattern (e.g. ".git in 3 vaults — 127 MB total"). The most persuasive piece of UI.
3. **Other presets** — every preset that isn't already in Recommended or Found.
4. **Custom patterns** — anything in `.stignore` that isn't part of any preset. User can swipe-to-delete or add a new line.
5. **Footer** — link to the Syncthing pattern docs for power users.

For an existing Send Only folder, toggles write through to `.stignore` immediately. There is no save button. Receive-capable and unknown folders expose no filter read or write path in 2.0.2, and their rescan actions remain disabled.

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

Shown the **first time** a user opens an existing Send Only vault's detail screen, per vault. It is not shown for receive-capable or unknown folders. The shown-state is persisted via a `UserDefaults` array of folder IDs.

- **Done** — applies the checked presets/patterns to the Send Only folder's `.stignore` and dismisses.
- **Skip** — dismisses without changing `.stignore`. The folder is still marked as "seen", so the sheet won't reappear.
- Detected heavy folders are pre-checked but the user can uncheck before applying.

New vault creation and share acceptance are unavailable in 2.0.2. Loading an
existing folder at startup never writes a filter automatically. For an existing
Send Only folder, the preselected Recommended set reaches `.stignore` only when
the user taps **Done**. This avoids a delayed `.stignore` write and scheduled
scan racing a conflict safety stop; **Skip** remains non-mutating.

## 6. Conflict → Ignore

Versions before 2.0.2 offered **Always skip on this iPhone** from
`ConflictDiffView`. That legacy action wrote the original path plus a matching
`<path>.sync-conflict-*` pattern, removed existing conflict copies, and requested
a rescan. It could therefore mutate evidence before a byte-preserving recovery
had been proven.

In 2.0.2 the conflict view is inspection-only in every engine safety state.
There is no Skip Family menu, confirmation, filter write, conflict removal,
user-triggered rescan, retry prompt, or success summary. The bridge and manager
compatibility entry points return the stable path-free recovery-unavailable
error before accessing the folder or `.stignore`. Normal explicit Sync Filters
remain available only for Send Only folders; receive-capable and unknown modes
stop before filter access or mutation.

Previously recorded filter pairs remain stored and editable in Sync Filters;
there is no migration or automatic rewrite. Their representation as one row
with a `+ conflict copies` caption remains unchanged. This preserves an explicit
past choice without treating it as consent for a new conflict recovery action.

## 6.5 Multi-vault setups

In typical Obsidian use, the sync folder is the **Obsidian root** and individual vaults live as subdirectories inside it. Pattern matching handles this transparently: Syncthing automatically expands every unanchored pattern (anything without a leading `/`) to also match at any depth, so `.git` covers both `Obsidian/.git` and `Obsidian/Vault1/.git`. No `**/` prefix is needed in the preset definitions.

The vault scanner specifically descends one level into non-hidden subdirectories so that heavy folders inside vaults (e.g. `Obsidian/Personal/.git`, `Obsidian/Work/.git`) are detected and their sizes aggregated into a single "Found in this vault" entry per pattern.

## 6.6 Manual conflict review

Presets prevent predictable conflict sources, but `.obsidian` also contains
settings and plugin state that users expect to sync. VaultSync therefore keeps
these files in the same explicit conflict workflow as notes instead of choosing
a winner automatically:

- **The app-owned automatic resolver is retired.** Foreground and background
  code no longer invoke VaultSync's former modification-time resolver. This
  does not claim that the embedded engine retains every copy; the conflict UI
  shows only copies that are still available when they are read.
- **Legacy state cannot opt back in.** The historical
  `auto-resolve-state-conflicts-v1` preference remains stored so an update does
  not delete or reset user preferences, but its value is ignored. A persisted
  `true` cannot restore automatic last-writer-wins behavior.
- **Bridge compatibility is non-mutating.** The exported
  `AutoResolveStateConflicts` entry point remains as a gomobile compatibility
  no-op and makes no filesystem changes. Foreground and background code do not
  call it.
- **Receive-side sync is read-only in 2.0.2.** Existing send-receive,
  receive-only, and receive-encrypted folders do not scan, watch, pull, request
  file data, clean versions, mutate vault bytes or metadata, or mutate their
  local file index. Authenticated remote indexes may persist Need using only
  the derived local global/needed flags and resulting count buckets. Send Only
  retains its existing behavior.
- **The boundary starts at database open.** Protected folder databases with a
  pending migration stop before it runs; unknown orphan databases remain
  untouched, and recognizable schema, identity, alias, or integrity deviations
  stop before mutation. In supported iOS operation the internal engine database
  belongs exclusively to the foreground app process; decision 034 records this
  narrow 2.0.2 trust boundary. External vault editors and atomic-save races stay
  inside the receive-side protection model.
- **Explicit recovery is globally unavailable in 2.0.2.** Keep This, Keep Other,
  Keep Both, Skip Family, and conflict-triggered rescan controls are absent in
  every safety state. Their ABI-compatible entry points stop before runtime,
  folder, filter, filesystem, temporary-file, database, or rescan access.
  A stopped folder is not automatically paused, rewritten, migrated, or
  reaccepted.
- **Explicit recovery is a separate design boundary.** The existing path-based
  actions are not described as lossless recovery: a source or destination can
  change during a rename, capacity can fail, and independently derived names
  can collide across peers. A replacement recovery needs separate race,
  crash-cutpoint, capacity, restart, and two-node-convergence proof before the
  UI may expose it.
- **Counts mean files now.** The home banner, vault badges, and notifications
  count distinct conflicted files instead of conflict copies. Saved
  `MaxConflicts` values remain unchanged, including `0`, positive values, and
  `-1`, but none can reactivate receive-side mutation in 2.0.2.

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
- The three previously auto-applied default patterns (`.Trash`, `.obsidian/workspace.json`, `.obsidian/workspace-mobile.json`) **stay on disk untouched**.
- The derived state shows "Workspace state" and "Trash" as ON only when those patterns are already present.
- No migration sheet. No disk changes. No surprise.

New vault creation and share acceptance are unavailable in 2.0.2. If a later
release re-enables either flow, no preset or rescan may be applied by a delayed
Add, Accept, or startup task; the first-run sheet may write only after explicit
**Done** consent on an eligible Send Only folder, while **Skip** writes nothing.

## 8. Naming

In the 2.0.2 app:
- Section title: **"Sync Filters"**
- Send Only filter copy may use **"Choose what gets synced to this iPhone"**.
- Conflict views contain no Skip or Always Skip action. **"Always skip on this iPhone"** is historical terminology documented only in §6.

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
