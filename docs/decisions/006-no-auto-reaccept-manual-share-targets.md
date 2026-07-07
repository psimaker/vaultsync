# 006 — Removed vaults are never auto-re-accepted; manual share targets persist

**Context:** While a peer still shares a folder, its offer reappears moments after local removal, and the auto-accept loop pulled it straight back in — "Remove Vault" was silently undone, and the #52 manual target picker was unreachable because a share never stayed pending long enough to act on.

**Decision:** `removeFolder` records the folder ID as user-removed; auto-accept skips recorded shares (they stay visible under Pending Shares until explicitly accepted, which lifts the record). A manually picked target (#52) is stored per folder ID, deliberately survives removal, and is honored by every later accept; an override that became unsafe is refused with guidance. Only empty folders (nothing but `.obsidian`) are eligible as existing targets.

**Why:** This is [002](002-manual-recovery-doctrine.md) applied to accept: re-accepting user data is never automatic once the user expressed intent by removing the folder — recovery and re-adding stay explicit user actions. Silently falling back to the share-label default when the chosen target is unsafe would split one vault's content across two directories.

**Rejected alternatives:** A global "ask before accepting shares" toggle — changes the default for every share and still cannot stop the instant re-accept after removal. Pruning the user-removed set against currently pending offers — would drop the record before the offer reappears (it can take a reconnect), re-enabling the zombie re-accept. Silent fallback to the label default on an unsafe override — hides a data-placement decision from the user.

**Links:** issue #52, issue #45 (follow-up), decision 002.
