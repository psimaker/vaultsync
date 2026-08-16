# 028 — `.obsidian` conflicts require a manual choice

- Context: VaultSync automatically applied last-writer-wins to `.obsidian` conflicts, allowing a background or foreground pass to delete or replace user bytes without a contemporaneous decision (#145).
- Decision: No VaultSync-owned automatic path deletes, replaces, renames, or promotes a `.obsidian` original or conflict copy. These settings and plugin-state conflicts wait for manual review.
- Compatibility: The legacy preference remains stored but is ignored, and the exported `AutoResolveStateConflicts` bridge entry point remains as a non-mutating compatibility no-op.
- Why: Modification time cannot establish user intent, especially with clock skew, and a silent choice can propagate an unwanted result to every peer.
- Rejected alternative: Keep opt-out last-writer-wins, because a missing or persisted `true` value would continue authorizing mutation without a decision at the time of conflict.
- Boundary: Manual conflict actions remain available after explicit confirmation; Syncthing's separate conflict-copy retention is unchanged and not guaranteed indefinitely.
- Links: issue [#145](https://github.com/psimaker/vaultsync/issues/145); `go/bridge/conflicts.go`; `ios/VaultSync/Services/SyncthingManager.swift`; `ios/VaultSync/Services/BackgroundSyncService.swift`.
