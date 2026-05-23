# Conflict Skip Family — Design

> Status: **proposed**
> Author: Umut Erdem
> Date: 2026-05-23
> Tracks: [Issue #8 — Issue with ignoring files](https://github.com/psimaker/vaultsync/issues/8) (reported by @vitaly74)

---

## 1. Problem

When a user opens a conflict in `ConflictDiffView` and taps **⋯ → "Always skip on this iPhone"**, the app adds the conflict's *original* relative path (e.g. `notes/foo.md`) to the folder's `.stignore`. The user then resolves the conflict (e.g. "Use remote copy") and expects that file to be silent on this iPhone from now on.

In practice, the conflict reappears later:

- A `.stignore` entry `notes/foo.md` matches the **original file** but **not** Syncthing's conflict copies, which are named:
  ```
  notes/foo.sync-conflict-<YYYYMMDD>-<HHMMSS>-<deviceShortID>.md
  ```
- The next time the file diverges between iPhone and another device, a fresh `sync-conflict-…` copy is created and synced to the iPhone unimpeded. VaultSync's conflict scanner picks it up. The user sees the conflict return — even though they explicitly chose to ignore the file.

The current behavior contradicts the user's mental model of "skip". The sync-filters UX spec (`docs/sync-filters-ux.md` §6) intentionally chose *exact-path* matching for predictability, but this turns out to leave a hole that produces exactly the surprise the spec aimed to prevent.

This design closes that hole, and additionally cleans up two pre-existing latent problems:

1. **Stale conflict files accumulate.** Conflict copies left on disk after partial resolutions never get garbage-collected, eating storage and producing phantom "1 conflict needs resolution" entries.
2. **Sync filters list can grow noisy.** If the fix naively writes two `.stignore` lines per skip, the Custom Patterns list becomes confusing to scan.

---

## 2. Goals

- Tapping "Always skip on this iPhone" makes the file **and its conflict copies** silent on this device — past, present, and future.
- The original file is **not** deleted from the device (no data loss for content the user might still want to read).
- The skip flow remains exactly **one tap** for the user. No new dialogs, no new confirmation steps.
- The Sync Filters view stays clean: a skipped file appears as **one** entry, not two.
- The fix is implemented in terms the existing codebase already uses (no new dependency, no protocol changes).

## 3. Non-Goals

- We are **not** ignoring `*.sync-conflict-*` globally. Conflict files are the user-visible signal of divergence; suppressing them everywhere would silently hide real sync problems.
- We are **not** changing the resolve flow itself (`Keep This` / `Keep Other` / `Keep Both`). Those work correctly today.
- We are **not** introducing `(?d)` deletable-prefix semantics. Their behavior is subtle (delete only when the file no longer exists on any peer) and harder to explain than active cleanup.
- We are **not** offering a per-pattern advanced editor in v1. Power users can still hand-edit `.stignore` through the existing Custom Patterns editor; the family concept only governs how *we* write and read pairs.

---

## 4. Design

### 4.1 The "Skip Family" concept

A *skip family* is a logical pair of `.stignore` patterns that together represent "ignore this file in every form it can appear":

- **Original pattern:** the file's relative path, e.g. `notes/foo.md`
- **Conflict-copies pattern:** the corresponding glob, e.g. `notes/foo.sync-conflict-*`

The pair is recognized purely by naming convention — we don't introduce any new metadata, comments, or `.stignore` extensions. Pattern matching is well-defined: given any line `X` in `.stignore` and a sibling line `X.sync-conflict-*` (or `<dir>/<basename>.sync-conflict-*` for nested files), the two are a family.

This means existing manually-edited `.stignore` files keep working unchanged: a user with only `notes/foo.md` in their ignores sees that single line as a regular custom pattern.

### 4.2 Conflict-copy glob

For a file at relative path `dir/basename.ext`, the conflict-copy pattern is:

```
dir/basename.sync-conflict-*
```

Note: the suffix part of the conflict filename also includes the original extension (`…-DEVID.md`), but `sync-conflict-*` already matches everything after `sync-conflict-` until the end of the filename, so we do **not** need to repeat the extension. This is simpler and avoids a bug class where files with no extension would otherwise need a separate case.

For files at the folder root (e.g. `README.md`), the pattern is `README.sync-conflict-*`.

### 4.3 Write path: "Always skip" tap

When the user taps "Always skip on this iPhone" in `ConflictDiffView`:

1. Compute the conflict-copy glob from `conflict.originalPath`.
2. Add both patterns to `.stignore` via `setIgnorePatterns` (skipping any already present — `addIgnorePattern` already handles this idempotency for single patterns; we extend the same idempotency to the pair).
3. Call a new Go-bridge function `RemoveConflictFilesForOriginal(folderID, originalPath)` that walks the folder, finds every file whose name matches `<basename>.sync-conflict-*<ext>` in the same directory as the original, and deletes them. This is the active cleanup that distinguishes Option C from a pure ignore-pattern fix.
4. Trigger a folder rescan so Syncthing's in-memory index reflects both the new ignore rules and the removed files.
5. Refresh the iOS-side conflict cache so the resolved conflict disappears from the list immediately.
6. Show the existing success alert with a slightly expanded message (see §4.6).

### 4.4 Read path: rendering in Sync Filters

`IgnorePatternsView` currently buckets `.stignore` lines into:

- Preset toggles (Workspace state, Trash, …)
- Detected-pattern toggles (Git repository, Copilot index, …)
- Custom patterns (everything else)

The Custom Patterns section gets a new pre-grouping step:

- Iterate the unbucketed lines and pair each `X` with its `<dir>/<basename>.sync-conflict-*` sibling if present.
- Each recognized pair is rendered as **one** list row:
  ```
  notes/foo.md
  + Konflikt-Kopien
  ```
  (Localized: en "+ conflict copies", de "+ Konflikt-Kopien", zh-Hans "+ 冲突副本")
- Swipe-to-delete on a paired row removes **both** patterns atomically.
- Lines without a paired sibling continue to render as single, ungrouped custom-pattern rows — preserving today's behavior for manual entries.

This is purely a rendering layer over `.stignore`. The file on disk still contains two independent lines; we just present them as one.

### 4.5 Bridge: cleanup function

New Go function in `go/bridge/conflicts.go`:

```go
// RemoveConflictFilesForOriginal removes every sync-conflict copy of the file
// at originalPath inside the given folder. Returns a JSON string of the form:
//   {"removed": <int>, "error": "<msg or empty>"}
// Symmetric with GetConflictFilesJSON's JSON-return style; gomobile-safe
// (no tuple returns across the bridge).
func RemoveConflictFilesForOriginal(folderID, originalPath string) string
```

It uses the existing `conflictPattern` regex and the safe-path validation already in the file. Symmetry with `KeepBothConflict` and `ResolveConflict` is intentional — same input shape, same safety checks. The JSON-result shape mirrors `GetConflictFilesJSON`, so the Swift decoder pattern is identical.

The Swift side exposes it via `SyncBridgeService` and calls it from a new `SyncthingManager.skipFileAndCleanupConflicts(folderID:originalPath:)` method that wraps both the `.stignore` write and the cleanup into a single semantically-meaningful operation.

### 4.6 Alert copy

Existing (en):
> "'%@' will no longer sync to this iPhone. You can undo this in Sync Filters."

New (en):
> "'%@' and its conflict copies will no longer sync to this iPhone. You can undo this in Sync Filters."

If `RemoveConflictFilesForOriginal` removed at least one file, append a one-line note in the same alert body — no extra dialog:
> "X existing conflict copies were removed."

German and Simplified Chinese strings ship in the same change.

---

## 5. Data flow

```
┌──────────────────────────────┐
│  ConflictDiffView            │
│   ⋯ → "Always skip…"         │
└──────────────┬───────────────┘
               │ skipFileAndCleanupConflicts(folderID, originalPath)
               ▼
┌──────────────────────────────┐
│  SyncthingManager (Swift)    │
│  1. compute conflict glob    │
│  2. addIgnorePatterns(both)  │
│  3. removeConflictFiles(...) │ ── via SyncBridgeService ──┐
│  4. rescanFolder              │                            │
│  5. refreshConflicts          │                            │
└──────────────┬───────────────┘                             │
               │                                              │
               │ via gomobile                                 │
               ▼                                              ▼
┌──────────────────────────────┐    ┌──────────────────────────────┐
│  SetFolderIgnores (Go)       │    │  RemoveConflictFilesFor      │
│   writes .stignore            │    │  Original (Go, new)          │
│   updates Syncthing model    │    │   walks folder, os.Remove    │
└──────────────────────────────┘    └──────────────────────────────┘
```

`IgnorePatternsView` reads `.stignore` lines via `GetFolderIgnores` (unchanged) and applies the new family-pairing in its own bucketing logic.

---

## 6. Edge cases

- **Original path has no extension** (e.g. `Makefile`): conflict glob is `Makefile.sync-conflict-*`. Confirmed correct by the existing `conflictPattern` regex, which requires an extension on the conflict copy — but the glob doesn't need to assume one.
- **File in nested subdirectory**: `Vault/Personal/foo.md` → glob `Vault/Personal/foo.sync-conflict-*`. Syncthing pattern matching treats this as anchored relative to folder root, which is what we want.
- **User taps "Skip" on a file with multiple conflict copies** (e.g. one from each of two desktops): all of them match the same glob, all are removed by `RemoveConflictFilesForOriginal` in one pass.
- **User taps "Skip" twice** (e.g. on two different conflicts of the same original): `addIgnorePattern`'s existing duplicate check applies to both patterns; second tap is a no-op for the `.stignore` write, but the cleanup still runs (idempotent — nothing to remove the second time).
- **Family-pairing false positives**: Could a user have a *legitimate* custom pattern named `something.sync-conflict-*`? Conceivable in theory, but in practice Syncthing's own conflict-copy naming reserves this suffix. We accept the small risk and document it.
- **Family-pairing on Custom Patterns view, where only one half exists**: e.g. a user manually added `notes/foo.sync-conflict-*` without a matching original. We render it as a single ungrouped row (no false pairing), since the pair-detection requires both halves.
- **Existing `.stignore` files from v1.2.0–v1.3.1**: untouched. No migration needed. Users who previously tapped "Always skip" still have only the single-pattern entry; the next time they hit the same conflict (which is exactly the bug being fixed), the new logic kicks in and writes the second pattern.

---

## 7. Testing

- **Unit tests, Go (`go/bridge/conflicts_test.go`):**
  - `RemoveConflictFilesForOriginal` removes the expected files for a single conflict, multiple conflicts, nested paths, and paths with no conflict copies (returns 0, no error).
  - Path-traversal protection (existing `safePath`) is respected.
- **Unit tests, Swift (`ios/VaultSyncTests/`):**
  - New `SkipFamilyTests`: given a relative path, the produced conflict glob matches the expected string for root files, nested files, no-extension files.
  - Extend `IgnorePatternsViewModel`-style logic (or whatever the equivalent is in `IgnorePatternsView`) to verify the pair-detection algorithm correctly groups `X` + `X.sync-conflict-*` and leaves singletons alone.
- **Manual smoke (per release-test checklist):**
  - Create a conflict on a test vault, tap Skip, resolve, force a fresh conflict by re-diverging the file → conflict does **not** reappear.
  - Verify the Sync Filters → Custom Patterns view shows one grouped row per skip.

---

## 8. UX spec update

After this lands, `docs/sync-filters-ux.md` §6 ("Conflict → Ignore") gets a follow-up edit:

- The exact-path rationale is retained, but the section now documents that the skip operation writes **a pair** of patterns (original + conflict-copy glob), rendered as a single row in the Sync Filters list.
- The alert copy is updated to match §4.6 above.
- A short note in §10 ("Future considerations") that the family concept could be extended to other naming-convention pairs in the future (e.g. backups, `.bak` siblings) if usage data suggests value.

That edit lands in the same PR.

---

## 9. Out of scope (for explicit confirmation)

- Server-side `.stignore` changes. The iPhone's filter only affects the iPhone.
- Telemetry/logging on how often Skip is used. VaultSync doesn't collect analytics by design.
- A "soft undo" notification ("Undo Skip" toast). The user can already undo via Sync Filters; an extra UI affordance isn't worth the surface area for v1.

---

## 10. Rollout

- Single release vehicle, no feature flag.
- Bump `CFBundleShortVersionString` to **1.3.2** and `CFBundleVersion` to **24** in `ios/project.yml` (matches the existing patch-bump pattern from 1.3.0 → 1.3.1).
- CHANGELOG entry under `## [1.3.2]`:
  > **Skip on iPhone now actually skips returning conflicts** ([#8](https://github.com/psimaker/vaultsync/issues/8)) — Tapping "Always skip on this iPhone" in the conflict resolver now also covers future conflict copies of the same file and removes any existing copies on disk, so the conflict no longer reappears after the next divergent edit. The Sync Filters list shows skipped files as one grouped entry.
