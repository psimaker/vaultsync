# 029 — Continued processing succeeds only on verified idle

- Context: The continued-processing handler treated a stopped engine as success even when no readable folder snapshot proved completion (#146).
- Decision: Success requires verified evidence at completion that every expected configured folder is idle with no pending work.
- Decision: Engine death, an empty or unreadable snapshot, folder error, expiration, and cancellation are failures; foreground takeover is classified separately and never becomes idle success.
- Decision: The expected folder set is captured from the first readable snapshot and never narrowed. A folder that disappears and a folder that appears are both failures, so no folder can vanish from the idle proof mid-run.
- Decision: Conflict inspection runs only after verified idle and cannot upgrade the result; once terminal, late callbacks cannot overwrite it.
- Why: Scheduler success is an evidence claim. A stopped engine or missing state proves only that observation ended, not that notes finished syncing.
- Rejected alternative: Keep “engine stopped OR idle” as success, because crashes, expiration, and lifecycle transitions can all stop observation before work settles.
- Links: issue [#146](https://github.com/psimaker/vaultsync/issues/146); `ios/VaultSync/Services/BackgroundSyncService.swift`; `ios/VaultSyncTests/BackgroundSyncServiceTests.swift`.
