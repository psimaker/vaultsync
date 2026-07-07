# 002 — Recovery from data damage is never automatic

**Context:** When VaultSync detects existing damage (two vaults merged into one folder, or one nested inside another, #45), an automatic repair would have to move, rename, delete, or re-accept user data whose true state only the user knows — after a merge, the app cannot tell which files belong to which vault.

**Decision:** Recovery is always: pause the affected folders (exactly once — a folder the user deliberately resumes is never re-paused), explain the problem in a critical issue with concrete recovery steps, and let the user act.

**Why:** Sync propagates every local action to all peers, so a wrong automatic move or delete becomes irreversible, fleet-wide data loss. Pausing is the only intervention that is always safe and reversible; the pause-once rule keeps the shield idempotent and respects user overrides.

**Rejected alternative:** Auto-splitting or auto-moving the affected folders to fresh paths — a wrong split would sync wrong content to every peer, turning a recoverable mess into permanent loss.

**Links:** #45, PR #47, PR #51; CHANGELOG 1.7.1/1.7.2.
