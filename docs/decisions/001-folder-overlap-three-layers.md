# 001 — Two Syncthing folders never overlap on disk (three enforcement layers)

**Context:** A server sharing more than one vault could hand the second share the same local folder as the first (1.6.0–1.7.0) or a subfolder inside an existing vault (1.7.1). Overlapping folders sync each other's content as their own — deleting the stray copy on any peer would have deleted the inner vault everywhere (#45).

**Decision:** The no-overlap invariant (equal, nested, or containing paths) is enforced in three independent layers, each with its own tests: the Go hard floor (`AcceptPendingFolder`/`AddFolder` reject with `folderPathOverlapError`), the Swift mapping (`VaultManager.resolveSharePath` returns `nil` rather than an overlapping path), and the launch shield (`PathCollisionGuard` pauses already-overlapping folders exactly once).

**Why:** The single-layer version failed twice — both #45 bugs lived in the Swift mapping. The Go floor backstops future mapping bugs, the Swift layer turns a hard engine error into user guidance, and only the shield catches damage that predates the fix.

**Rejected alternative:** Enforcing only in the Swift mapping — the proven failure mode; a bug there would silently re-open the hole with no backstop.

**Links:** #45, PR #47 (same-folder merge, 1.7.1), PR #51 (nesting, 1.7.2).
