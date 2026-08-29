# 033 — Conflict recovery is inspection-only in 2.0.2

- Context: Existing recovery actions can delete, replace, rename, ignore, or rescan conflict files without a proven byte-preserving recovery doctrine (#150/#167).
- Decision: `ResolveConflict`, `KeepBothConflict`, and `RemoveConflictFilesForOriginal` retain their ABI signatures but return one stable, path-free recovery-unavailable error before any runtime, filesystem, temporary-file, database, filter, or rescan access.
- UI: Conflict copies still present can be inspected, but 2.0.2 makes no retention guarantee and exposes no executable recovery, retry, skip, confirmation, or success flow.
- Scope: `AutoResolveStateConflicts` remains non-mutating; no folder pause, configuration rewrite, persisted-state migration, or automatic reacceptance is introduced.
- Why: Inspection adds no recovery mutation, while an unproven action can silently propagate lost bytes to every peer.
- Rejected alternative: Rename-, quarantine-, snapshot-, or atomic-exchange recovery without a separately approved doctrine and proof.
- Re-entry: Mutating recovery requires separate owner approval plus collision, capacity, race, crash, restart, and two-node convergence evidence.
- Links: issues [#150](https://github.com/psimaker/vaultsync/issues/150) and [#167](https://github.com/psimaker/vaultsync/issues/167); decisions 002, 027, 028, and 032.
