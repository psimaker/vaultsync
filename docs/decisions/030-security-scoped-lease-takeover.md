# 030 — Security-scoped access uses owned transactional leases

- Context: Folder grants could start access repeatedly, replace visible/bookmark state before a successful scan, and let a stale background refresh overwrite a newer folder choice (#147).
- Decision: Every successful security-scope start creates one idempotent owner token that performs exactly one matching stop; a failed start creates no token.
- Decision: A foreground replacement validates, scans, and prepares its bookmark before committing the new URL, bookmark, lease, and visible state. The prior lease remains active until adoption; failure preserves it, and reselecting the same resource reuses it.
- Decision: Stale bookmark refreshes compare-and-swap the exact bytes they resolved, while each background run owns and releases only its distinct token on every terminal path, including restart and cancellation.
- Why: Explicit ownership and commit ordering prevent leaked access, double stops, false success, lost prior access, and stale last-writer-wins rollback.
- Rejected alternative: URL/Boolean bookkeeping, stop calls hidden in scan helpers, or unconditional stale-bookmark writes, because none proves which successful start is being released or whether a newer choice must win.
- Links: issue [#147](https://github.com/psimaker/vaultsync/issues/147); `BookmarkService.swift`; `VaultManager.swift`; `BackgroundSyncService.swift`.
