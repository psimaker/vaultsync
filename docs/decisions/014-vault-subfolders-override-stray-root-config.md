# 014 — Vault subfolders override a stray root-level `.obsidian`

**Context:** "Is the connected root itself a vault?" was decided solely by the presence of `.obsidian/` at its top level (selection advisory in `grantAccess`, `baseIsVault` in the share accept). The legacy whole-root sync can leave a stray `.obsidian/` in the container — the offering peer's vault config synced straight into the root — permanently misclassifying a multi-vault container (observed on-device 2026-07-08: stray `.obsidian/` + `.stfolder` next to real vaults).

**Decision:** A root holding at least one vault subfolder (direct subdirectory containing `.obsidian/`) is a container, never a vault-as-root — regardless of a root-level `.obsidian/`. One pure core (`VaultManager.rootIsItselfVault`) feeds every classification site.

**Why:** Obsidian never nests vaults in normal use, so vault subfolders are the stronger signal. Misclassification collapses the next accepted share into the container root (`resolveSharePath` root shortcut), nesting every existing vault inside that share's synced tree — the #45 corruption family, with only the #54 merge confirmation left in between.

**Rejected alternative:** Inspecting the root `.obsidian/`'s contents to judge "real vault vs. stray leftover" — heuristic, Obsidian-version-dependent, and still wrong for a genuinely empty vault config.

**Links:** issue #79; regression suite `VaultRootClassificationTests`.
