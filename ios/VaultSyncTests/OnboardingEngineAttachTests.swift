import Foundation
import Testing
@testable import VaultSync

extension EngineBridgeSuites {
    /// Onboarding completion with a bridge-running/manager-cold engine (#61):
    /// the `hasCompletedOnboarding` onChange used to guard on
    /// `syncthingManager.isRunning` alone, so an engine started by a
    /// background handler made it call `start()` — and the Go floor's
    /// "already running" surfaced as a user-facing error in the first-run
    /// moment. Same latent state as #60, second consumer: the onChange must
    /// route through `sceneActivationAction` and adopt instead.
    ///
    /// These tests run against a REAL engine started directly through the
    /// bridge, exactly the way `BackgroundSyncService.performBackgroundSync`
    /// starts it in a background-launched process.
    @Suite("Onboarding completion adopts a background-started engine (#61)")
    struct OnboardingEngineAttachTests {

        /// The defect surface: `start()` against a background-started engine
        /// maps the Go floor's "already running" rejection to a `userError`.
        /// This is what the pre-fix onChange showed the user right after
        /// completing onboarding.
        @MainActor
        @Test("start() against a background-started engine surfaces a user error and never attaches")
        func startAgainstLiveEngineSurfacesUserError() async {
            TestSupport.resetSyncthingState()
            defer { TestSupport.resetSyncthingState() }
            BackgroundSyncService.lifecycleLock.withLock { $0.foregroundActive = false }

            let startErr = SyncBridgeService.startSyncthing(configDir: TestSupport.syncthingConfigPath())
            #expect(startErr == nil)
            #expect(SyncBridgeService.isRunning())

            let manager = SyncthingManager()
            await manager.start()

            #expect(manager.userError != nil)
            #expect(!manager.isRunning)
        }

        /// The fixed path: the onChange's `.adoptRunningEngine` branch
        /// attaches to the live engine without touching the floor guard — no
        /// error, manager state restored.
        @MainActor
        @Test("adoptRunningEngine() attaches to the live engine without surfacing an error")
        func adoptAttachesToLiveEngine() {
            TestSupport.resetSyncthingState()
            defer { TestSupport.resetSyncthingState() }
            BackgroundSyncService.lifecycleLock.withLock { $0.foregroundActive = false }

            let startErr = SyncBridgeService.startSyncthing(configDir: TestSupport.syncthingConfigPath())
            #expect(startErr == nil)

            let manager = SyncthingManager()
            let adopted = manager.adoptRunningEngine()

            #expect(adopted)
            #expect(manager.isRunning)
            #expect(manager.userError == nil)
            #expect(!manager.deviceID.isEmpty)
            let foregroundActive = BackgroundSyncService.lifecycleLock.withLock { $0.foregroundActive }
            #expect(foregroundActive)

            manager.stop()
        }
    }
}
