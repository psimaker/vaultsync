# 003 — Never re-point a live folder (`.stfolder` marker is ground truth)

**Context:** iOS moves the app container, and users can re-select a different Obsidian folder — both leave Syncthing's configured folder paths pointing somewhere other than the data. Path repair (`FolderPathReconciler`, #25) must decide when moving a folder's configured path is safe.

**Decision:** `SetFolderPath` requires the target directory to hold this folder's own `.stfolder` marker, and the reconciler only rebases folders whose configured path is dead; a folder alive on disk always keeps its location, and its stored position is refreshed from reality instead (1.7.2).

**Why:** Re-pointing a send-receive folder at wrong content makes Syncthing treat the difference as local edits and propagates deletions/overwrites to every peer. The marker on disk is the only ground truth of folder identity; stored mappings are derived state and were wrong in practice.

**Rejected alternative:** Trusting the app's stored path mapping and re-pointing whenever it disagrees — 1.7.2 showed exactly that: a blocked rebase retried against a healthy vault on every launch.

**Links:** #25, #45 follow-up (PR #51), CHANGELOG 1.7.2.
