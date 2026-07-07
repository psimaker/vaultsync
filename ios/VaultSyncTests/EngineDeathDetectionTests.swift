import Foundation
import Testing
@testable import VaultSync

extension EngineBridgeSuites {
    /// Dead bridge under an attached manager (#61): the residual #60
    /// adoption race (a background stop that passed its lock re-read just
    /// before the foreground claimed) leaves the manager polling a dead
    /// engine — empty folder JSON used to render as a healthy "Ready". The
    /// poll loop must detect the death, restart once per external
    /// generation, and surface an honest stopped state on a second death
    /// instead of flapping a crash-looping engine.
    ///
    /// These tests drive the REAL poll loop against a real engine that gets
    /// stopped underneath the manager, exactly like the raced background
    /// handler stops it.
    @Suite("Poll loop detects a dead bridge under an attached manager (#61)")
    struct EngineDeathDetectionTests {

        /// Wait on the main actor without blocking it, so the manager's poll
        /// and restart tasks can run between checks.
        @MainActor
        private static func waitUntil(
            timeout: TimeInterval,
            _ condition: @MainActor () -> Bool
        ) async -> Bool {
            let deadline = Date().addingTimeInterval(timeout)
            while Date() < deadline {
                if condition() { return true }
                try? await Task.sleep(for: .milliseconds(100))
            }
            return condition()
        }

        @MainActor
        @Test("First death after adoption auto-restarts the engine instead of polling 'Ready' forever")
        func firstDeathAutoRestarts() async {
            TestSupport.resetSyncthingState()
            BackgroundSyncService.lifecycleLock.withLock { $0.foregroundActive = false }

            let startErr = SyncBridgeService.startSyncthing(configDir: TestSupport.syncthingConfigPath())
            #expect(startErr == nil)

            let manager = SyncthingManager()
            #expect(manager.adoptRunningEngine())
            defer {
                manager.stop()
                TestSupport.resetSyncthingState()
            }

            // The raced background handler stops the engine right under the
            // freshly attached manager.
            SyncBridgeService.stopSyncthing()

            let recovered = await Self.waitUntil(timeout: 20) {
                manager.isRunning && SyncBridgeService.isRunning()
            }
            #expect(recovered, "poll loop should detect the death and cold-start once")
            #expect(manager.userError == nil)
            #expect(manager._testEngineDeathAutoRestartConsumed())
        }

        @MainActor
        @Test("Second death in the same generation stays stopped and surfaces an honest error, never 'Ready'")
        func secondDeathSurfacesStoppedState() async {
            TestSupport.resetSyncthingState()
            BackgroundSyncService.lifecycleLock.withLock { $0.foregroundActive = false }

            let startErr = SyncBridgeService.startSyncthing(configDir: TestSupport.syncthingConfigPath())
            #expect(startErr == nil)

            let manager = SyncthingManager()
            #expect(manager.adoptRunningEngine())
            defer {
                manager.stop()
                TestSupport.resetSyncthingState()
            }

            // This generation already used its one automatic restart.
            manager._testMarkEngineDeathAutoRestartConsumed()
            SyncBridgeService.stopSyncthing()

            let surfaced = await Self.waitUntil(timeout: 10) {
                manager.userError != nil
            }
            #expect(surfaced, "second death must surface a user-visible stopped state")
            #expect(!manager.isRunning)
            #expect(manager.userError?.title == L10n.tr("Sync Engine Stopped"))
            #expect(!SyncBridgeService.isRunning(), "no second auto-restart may run")
            // Background handlers regain lifecycle ownership.
            let foregroundActive = BackgroundSyncService.lifecycleLock.withLock { $0.foregroundActive }
            #expect(!foregroundActive)
            // New generation: accepts stay held until a fresh reconcile (#56).
            #expect(!manager.pathSettlement.settled)
        }

        /// Every externally initiated lifecycle transition hands the next
        /// generation a fresh auto-restart budget — otherwise one death would
        /// disable the safety net for the rest of the app session.
        @MainActor
        @Test("stop() and resetForRestart() hand the next generation a fresh auto-restart budget")
        func externalTransitionsResetBudget() {
            TestSupport.resetSyncthingState()
            BackgroundSyncService.lifecycleLock.withLock { $0.foregroundActive = false }

            let manager = SyncthingManager()

            manager._testMarkEngineDeathAutoRestartConsumed()
            manager.resetForRestart()
            #expect(!manager._testEngineDeathAutoRestartConsumed())

            manager._testMarkEngineDeathAutoRestartConsumed()
            manager.stop()
            #expect(!manager._testEngineDeathAutoRestartConsumed())
        }
    }
}
