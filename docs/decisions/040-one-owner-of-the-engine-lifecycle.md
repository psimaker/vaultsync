# 040 — One owner of the engine lifecycle: claims exclude each other, stops are claimed before they run

**Context.** Foreground scene and background runs share one process-global engine. Ownership was a bare flag: the foreground set it before start or adoption, background handlers read it and then stopped the engine outside any lock, the manager kept polling after the scene released the flag, and a coalesced second background run answered `.alreadyIdle` on its own. Three windows followed: a background stop landing under a fresh adoption, a manager restarting an engine it no longer owned, and a failed leader run reported as success by its follower (#183).

**Decision.** `SyncLifecycleState` is the one place ownership changes, through its methods only. (1) A background stop is claimed under the lock (`beginBackgroundStop`, refused while the foreground owns), runs outside it, then releases the claim; every background stop path goes through `stopEngineIfBackgroundOwned`. (2) Foreground adoption is refused while a stop claim holds, and a cold start waits for the claim to clear before it touches the bridge. (3) The manager heals a dead engine (decision 009) only while it owns the lifecycle; without ownership it detaches quietly and the next scene activation attaches again. (4) A single-flight follower waits for the leader and reports the leader's result; a follower cancelled while waiting reports failure.

**Why.** Two owners of one engine cannot both be right; every window above ends with an engine stopped under someone who believes it runs, or with a success nobody observed (decision 029).

**Rejected alternative.** Performing the background stop inside the lifecycle lock. The stop blocks for seconds and the lock is also taken on the main thread — decision 009 already rejected it; the claim flag gives the same exclusion without holding the lock through the stop.

**Links.** #183, #151, decisions 009, 029, 030.
