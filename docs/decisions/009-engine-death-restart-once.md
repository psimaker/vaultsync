# 009 — Engine death under an attached manager: restart once, then stop honestly

**Context.** Adoption (#60) claims the lifecycle lock before verifying the engine runs, but a background stop whose lock re-read happened just before the claim can still land after verification. The manager then polls a dead engine: empty folder JSON rendered as a healthy "Ready" while nothing synced (#61).

**Decision.** The poll loop detects a dead bridge under an attached manager, resets exactly like the scene cold-start path, and automatically cold-starts the engine **once per externally initiated generation** (`stop`, `resetForRestart`, `adoptRunningEngine` each grant a fresh budget — the auto-restart's own `start()` does not). A second death in the same generation stays stopped and surfaces a user-visible error. The restart reconciles against the last known Obsidian root so accept decisions do not stay held until the next scene cycle (decision 008 — a scene return over a running engine never fires a reconcile).

**Why.** "Ready" over a dead engine silently breaks sync until the next background/foreground cycle; one restart heals invisibly. Unlimited restarts would flap a crash-looping engine forever.

**Rejected alternative.** Closing the race entirely by performing the background stop inside the lifecycle lock. `stopSyncthing` blocks for seconds and the lock is also taken on the main thread (`start`/`adoptRunningEngine`) — a stop-under-lock stalls the main thread long enough to freeze the UI or trip the watchdog.

**Links.** #60, #61.
