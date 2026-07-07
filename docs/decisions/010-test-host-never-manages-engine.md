# 010 — The unit-test host never manages the engine lifecycle

**Context.** Unit tests run inside the VaultSync app as test host, and the embedded Syncthing bridge is process-global. Bridge-state tests (#60/#61) start real engines and stop them under attached managers. The host app's scene-activation and onboarding handlers start engines of their own — and since the death detector (decision 009), the host's manager would auto-restart an engine a test deliberately stopped, straight into the test's assertions (observed as a live flake in `secondDeathSurfacesStoppedState`).

**Decision.** Every app-level code path that starts, adopts, or stops the engine bails when `TestHost.isActive` (scene-phase handler, onboarding-completion handler, `OnboardingView.onAppear`). New lifecycle call sites must do the same. Suites that mutate bridge state are additionally declared inside the `.serialized` `EngineBridgeSuites` umbrella so they never overlap each other.

**Why.** Two managers in one process fight over one engine; the host polls on a 2-second cadence, so no test-side ordering can prevent interference.

**Rejected alternative.** Leaving the host active and hardening tests around it (longer waits, retries). The host's restarts are timing-dependent and land mid-assertion; the flake reproduced even with a single test running solo.

**Links.** #61.
