# 038 — Engine liveness follows the supervisor; lifecycle waits have deadlines, synchronous startup work does not

**Context.** The bridge's running flag was set by `StartSyncthing` and cleared only by `StopSyncthing`. When Syncthing's main supervisor exited on its own (fatal service error), the flag stayed true: the app polled a dead engine and its one automatic restart (decision 009) bounced off "already running". Start and stop also waited on the early supervisor's config and event loops without a deadline while holding the bridge lock — a dead loop wedged every later call (#181).

**Decision.** Liveness derives from the supervisor: a watcher goroutine blocks on `App.Wait()` and marks the instance exited; `IsRunning` and every "is the engine running" guard go through `engineUpLocked()`, and `EngineExitReason` carries the why to the app. Every wait on another goroutine — config commits, the event subscription, `App.Stop` — runs through `awaitWithin` with a deadline and reports a timeout as the error string. A stop that overruns its deadline releases the bridge state at once but records the pending stop; `StartSyncthing` refuses to start until that stop has finished. Synchronous startup work (`OpenDatabase`, `App.Start`) is not bounded.

**Why.** A dead engine that looks alive silently breaks sync until the next scene cycle; a wait that never returns while holding the lock freezes the app. Refusing a start over a pending stop is fail-closed: two engines on one config directory would share the database and every folder.

**Rejected alternative.** Bounding `App.Start()` and `OpenDatabase` with an early return. Abandoned synchronous work cannot be cancelled — a partially started App keeps the services it already added, and a retry would open the same database and folders a second time; a slow but healthy start would be abandoned and stopped, which is worse than waiting.

**Links.** #181, #151, decision 009.
