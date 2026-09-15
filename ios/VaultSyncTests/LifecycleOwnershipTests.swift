import Foundation
import Testing
@testable import VaultSync

extension EngineBridgeSuites {
    /// Engine-lifecycle ownership between the foreground scene and background
    /// runs (#183, decision 040). Real engines through the production paths,
    /// exactly like the raced handlers drive them.
    @Suite("Engine lifecycle has one owner and no ownership windows (#183)")
    struct LifecycleOwnershipTests {

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

        private static func resetLifecycle() {
            BackgroundSyncService.lifecycleLock.withLock { $0 = SyncLifecycleState() }
        }

        /// The scene released the lifecycle (it went to the background) and a
        /// background handler stopped the engine it now owns. The still-polling
        /// manager used to read that as a death and restart the engine — two
        /// owners, and an engine running in the background that nobody stops.
        @MainActor
        @Test("A manager without lifecycle ownership detaches on an engine stop instead of restarting")
        func detachedManagerDoesNotRestart() async {
            TestSupport.resetSyncthingState()
            Self.resetLifecycle()
            let manager = SyncthingManager()
            await manager.start()
            defer {
                manager.stop()
                TestSupport.resetSyncthingState()
                Self.resetLifecycle()
            }
            #expect(manager.isRunning)

            // Scene goes to the background: ownership released, polling continues.
            BackgroundSyncService.releaseForegroundLifecycleLock()
            // A background handler stops the engine it now owns.
            SyncBridgeService.stopSyncthing()

            let detached = await Self.waitUntil(timeout: 10) { !manager.isRunning }
            #expect(detached, "the poll loop must notice the stop")
            // Leave time for a wrong restart, then assert none happened.
            try? await Task.sleep(for: .seconds(3))
            #expect(!SyncBridgeService.isRunning(), "a manager that does not own the lifecycle must not restart the engine")
            #expect(!manager.isRunning)
            #expect(manager.userError == nil, "a background-owned stop is not an error")
        }

        /// A background stop is claimed under the lifecycle lock and then runs
        /// for seconds outside it. A foreground adoption landing in that window
        /// used to attach to an engine about to die (the #60 residual).
        @MainActor
        @Test("Adoption is refused while a background stop is in flight")
        func adoptionRefusedDuringBackgroundStop() {
            TestSupport.resetSyncthingState()
            Self.resetLifecycle()
            #expect(SyncBridgeService.startSyncthing(configDir: TestSupport.syncthingConfigPath()) == nil)
            defer {
                TestSupport.resetSyncthingState()
                Self.resetLifecycle()
            }

            let claimed = BackgroundSyncService.lifecycleLock.withLock { $0.beginBackgroundStop() }
            #expect(claimed)

            let manager = SyncthingManager()
            let adopted = manager.adoptRunningEngine()
            #expect(!adopted, "adopting during a background stop attaches to an engine about to die")
            #expect(!manager.isRunning)
            let foregroundActive = BackgroundSyncService.lifecycleLock.withLock { $0.foregroundActive }
            #expect(!foregroundActive, "a refused adoption leaves no foreground claim behind")

            BackgroundSyncService.lifecycleLock.withLock { $0.endBackgroundStop() }
            #expect(manager.adoptRunningEngine(), "once the stop claim clears, adoption succeeds")
            manager.stop()
        }

        /// The cold-start fallback after a refused adoption must not race the
        /// same stop: starting into it bounces off "already running" or
        /// attaches to the dying engine. `start()` waits for the claim to clear.
        @MainActor
        @Test("A cold start waits for an in-flight background stop to clear")
        func startWaitsForBackgroundStop() async {
            TestSupport.resetSyncthingState()
            Self.resetLifecycle()
            defer {
                TestSupport.resetSyncthingState()
                Self.resetLifecycle()
            }

            let claimed = BackgroundSyncService.lifecycleLock.withLock { $0.beginBackgroundStop() }
            #expect(claimed)

            let manager = SyncthingManager()
            let starting = Task { @MainActor in await manager.start() }
            try? await Task.sleep(for: .milliseconds(800))
            #expect(!SyncBridgeService.isRunning(), "start() must not run the bridge start while a background stop is in flight")
            #expect(!manager.isRunning)

            BackgroundSyncService.lifecycleLock.withLock { $0.endBackgroundStop() }
            await starting.value
            #expect(manager.isRunning)
            #expect(SyncBridgeService.isRunning())
            manager.stop()
        }

        /// Two background wake-ups overlap: the second (follower) used to answer
        /// `.alreadyIdle` on its own, so a failed leader run still read as
        /// success (decision 029). The follower must report what the leader
        /// observed.
        @MainActor
        @Test("A single-flight follower reports the leader's result, never a synthetic success")
        func followerMirrorsLeader() async {
            TestSupport.resetSyncthingState()
            Self.resetLifecycle()
            // Engine running, nobody owns it: the leader observes without
            // starting, finds no folders after its 3 s wait and ends with
            // .noFoldersConfigured — a deterministic multi-second window.
            #expect(SyncBridgeService.startSyncthing(configDir: TestSupport.syncthingConfigPath()) == nil)
            defer {
                TestSupport.resetSyncthingState()
                Self.resetLifecycle()
            }

            let leader = Task { await BackgroundSyncService.performBackgroundSync(reason: "app-refresh", maxDuration: 5) }
            try? await Task.sleep(for: .milliseconds(400))
            let follower = Task { await BackgroundSyncService.performBackgroundSync(reason: "app-refresh", maxDuration: 5) }
            let leaderResult = await leader.value
            let followerResult = await follower.value
            #expect(leaderResult == .noFoldersConfigured)
            #expect(followerResult == leaderResult, "the follower must mirror the leader's result")
        }
    }
}
