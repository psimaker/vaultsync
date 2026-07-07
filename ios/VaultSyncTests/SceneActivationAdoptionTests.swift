import Testing
@testable import VaultSync

extension EngineBridgeSuites {
    /// Scene activation with a bridge-running but manager-cold engine (#60):
    /// a background handler (BGTask / silent push) starts the engine in a
    /// background-launched process without the manager ever attaching. The
    /// `.active` branch used to guard on the bridge alone, so this state got
    /// neither polling nor a reconcile — and the background cleanup later
    /// stopped the engine under the active scene.
    ///
    /// Lives in `EngineBridgeSuites`: the adoption tests assert on the
    /// process-global `BackgroundSyncService.lifecycleLock` and reset the
    /// process-global bridge, so they must never overlap with the other
    /// bridge-state suites.
    @Suite("Scene activation adopts a running engine (#60)")
    struct SceneActivationAdoptionTests {

        // MARK: - Decision core

        @Test("Bridge running, manager cold — the reported state — must adopt, not fall through to a rescan check")
        func bridgeRunningManagerColdAdopts() {
            #expect(
                BackgroundSyncService.sceneActivationAction(bridgeRunning: true, managerRunning: false)
                    == .adoptRunningEngine
            )
        }

        @Test("Bridge not running cold-starts, regardless of stale manager state")
        func bridgeNotRunningColdStarts() {
            #expect(
                BackgroundSyncService.sceneActivationAction(bridgeRunning: false, managerRunning: false)
                    == .coldStart
            )
            // Manager thinks it runs but the bridge died (BGTask expiration):
            // still a cold start — the caller resets the stale manager first.
            #expect(
                BackgroundSyncService.sceneActivationAction(bridgeRunning: false, managerRunning: true)
                    == .coldStart
            )
        }

        @Test("Bridge and manager both running is a normal foreground return")
        func bothRunningIsAlreadyAttached() {
            #expect(
                BackgroundSyncService.sceneActivationAction(bridgeRunning: true, managerRunning: true)
                    == .alreadyAttached
            )
        }

        // MARK: - Adoption with no engine to adopt

        /// The engine can stop between the caller's bridge check and the claim
        /// (a finishing background sync). Adoption must then report failure,
        /// release the lifecycle claim so background handlers regain ownership,
        /// and leave the manager cold for the caller's cold-start fallback.
        @MainActor
        @Test("Adopting a dead engine fails, releases the lifecycle claim, and leaves the manager cold")
        func adoptWithDeadEngineFailsClean() {
            TestSupport.resetSyncthingState() // guarantees the bridge is stopped
            BackgroundSyncService.lifecycleLock.withLock { $0.foregroundActive = false }

            let manager = SyncthingManager()
            let adopted = manager.adoptRunningEngine()

            #expect(!adopted)
            #expect(!manager.isRunning)
            #expect(manager.deviceID.isEmpty)
            #expect(manager.engineStartedAt == nil)
            let foregroundActive = BackgroundSyncService.lifecycleLock.withLock { $0.foregroundActive }
            #expect(!foregroundActive)
        }

        /// Decision 008: an adopted engine's paths are unsettled until its own
        /// reconcile completes. Adoption itself must never settle them — the
        /// fresh manager generation starts unsettled and only
        /// `reconcileFolderPaths` (fired by the caller after adoption) can
        /// complete it.
        @MainActor
        @Test("Adoption never settles paths — accepts stay held until the follow-up reconcile completes")
        func adoptionLeavesPathsUnsettled() {
            TestSupport.resetSyncthingState()
            BackgroundSyncService.lifecycleLock.withLock { $0.foregroundActive = false }

            let manager = SyncthingManager()
            #expect(!manager.pathSettlement.settled)

            _ = manager.adoptRunningEngine()
            #expect(!manager.pathSettlement.settled)
        }
    }
}
