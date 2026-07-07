# 008 — Accept decisions only run on settled paths

**Context:** Accept safety (#45/#52/#54) judges overlap and emptiness against the configured folder paths. The launch-time reconcile (#25) may still be rewriting those paths when the first accept pass fires: #53 sequenced the reconnect flow's retry after the reconcile, but the cold-start trigger (`onChange(of: pendingFolders, initial: true)`) still raced it (#56) — the occupied-path set was stale exactly after an iOS container move, letting a share take the very path a folder was about to be rebased onto.

**Decision:** No accept decision — automatic pass, manual accept, retry, or merge confirmation (#54) — runs while a path reconcile is in flight or before one has completed for the current engine generation (`PathSettlement`; generation-tokened so a stale completion from a previous engine start never settles). Automatic passes park silently and re-fire when paths settle; a manual tap gets a transient "try again in a moment" (002: explain, never silently refuse). Data is never moved. The rule binds in two places: the reconnect flow's sequencing (#53, `ObsidianReconnectFlow`) and the cold-start gate (#56).

**Why:** Every accept-time guard is only as good as the paths it judges against; running one on pre-reconcile state re-opens the #45 merge through a timing window. Fail-closed: an abandoned reconcile (engine died) holds accepts rather than unlocking them.

**Rejected alternative:** A timed retry/timeout fallback — reintroduces the stale-set race the ordering exists to prevent (the same reasoning documented for #53). Accepting immediately and repairing afterwards — the repair would be an automatic move of user data (violates 002).

**Links:** issue #56, issues #25/#45/#52/#53/#54, decisions 002/007.
