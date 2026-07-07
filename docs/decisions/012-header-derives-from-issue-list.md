# 012 — The status header derives from the issue list

**Context:** The dashboard header was computed from its own input set (engine error, running flag, folder errors, syncing) while the "Sync Issues" section rendered `SyncthingManager.unresolvedIssues`. The entire warning tier — parked shares, disconnected required peers, conflicts, stale sync — never reached the header, so a green "All Synced" sat directly above visible issue rows (#66; same lie-class as #61's "Ready while the bridge was dead").

**Decision:** The header derives from the maximum severity of the same `unresolvedIssues` list the issues section renders (plus unreachable folders, which live in their own section), via the pure `SyncHeaderModel.derive`. "Ready" is claimed only when genuinely armed: vault accessible and a vault exists. Any new status surface must feed from this list, never from a parallel input set.

**Why:** Two derivations over disjoint inputs will disagree again the next time an issue kind is added — a single source of truth cannot.

**Rejected alternative:** Patching the header's own cascade to additionally check pending shares and disconnected peers — fixes today's two symptoms but recreates the divergence with the next issue kind (exactly how #66 happened after the issue list gained tiers).

**Links:** issue #66; `ios/VaultSync/ViewModels/SyncHeaderModel.swift`; `SyncHeaderModelTests`.
