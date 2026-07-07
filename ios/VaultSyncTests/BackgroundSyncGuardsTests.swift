import Testing
@testable import VaultSync

/// Pins for the background-sync lifecycle guards (#61) — the decision logic
/// `performBackgroundSync` runs on, extracted behind injectable seams. The
/// timing pins matter more than the truth tables: the forced-restart guard
/// must re-read the lifecycle lock at decision time (#60), and the ownership
/// decision must judge the cycle-start bridge snapshot, not a second read.
@Suite("Background sync lifecycle guards (#61)")
struct BackgroundSyncGuardsTests {

    // MARK: - Silent-push fast path

    @Test("Fast-path rescan only for a silent push against a running engine")
    func fastPathRescanMatrix() {
        #expect(BackgroundSyncGuards.shouldFastPathRescan(reason: "silent-push", bridgeAlreadyRunning: true))
        #expect(!BackgroundSyncGuards.shouldFastPathRescan(reason: "silent-push", bridgeAlreadyRunning: false))
        #expect(!BackgroundSyncGuards.shouldFastPathRescan(reason: "app-refresh", bridgeAlreadyRunning: true))
        #expect(!BackgroundSyncGuards.shouldFastPathRescan(reason: "processing", bridgeAlreadyRunning: true))
    }

    // MARK: - Lifecycle ownership

    @Test("Background owns the lifecycle only when the engine is stopped and no foreground claim exists")
    func ownershipMatrix() {
        func ownership(bridgeRunning: Bool, foregroundOwns: Bool) -> BackgroundSyncGuards.LifecycleOwnership {
            let guards = BackgroundSyncGuards(environment: .init(
                bridgeRunning: { Issue.record("ownership must judge the cycle-start snapshot, never re-read the bridge"); return false },
                foregroundOwnsLifecycle: { foregroundOwns }
            ))
            return guards.lifecycleOwnership(bridgeAlreadyRunning: bridgeRunning)
        }

        #expect(ownership(bridgeRunning: false, foregroundOwns: false).backgroundOwns)
        #expect(!ownership(bridgeRunning: false, foregroundOwns: true).backgroundOwns)
        #expect(!ownership(bridgeRunning: true, foregroundOwns: false).backgroundOwns)
        #expect(!ownership(bridgeRunning: true, foregroundOwns: true).backgroundOwns)
        // The traced lock value is the one the decision used.
        #expect(ownership(bridgeRunning: false, foregroundOwns: true).foregroundOwns)
        #expect(!ownership(bridgeRunning: false, foregroundOwns: false).foregroundOwns)
    }

    @Test("Ownership reads the lifecycle lock exactly once per decision")
    func ownershipReadsLockOnce() {
        var lockReads = 0
        let guards = BackgroundSyncGuards(environment: .init(
            bridgeRunning: { false },
            foregroundOwnsLifecycle: { lockReads += 1; return false }
        ))
        _ = guards.lifecycleOwnership(bridgeAlreadyRunning: false)
        #expect(lockReads == 1)
    }

    // MARK: - Forced restart (the #60 race pin)

    @Test("No wake evidence and no foreground claim forces the restart")
    func forcedRestartBaseline() {
        let guards = BackgroundSyncGuards(environment: .init(
            bridgeRunning: { true },
            foregroundOwnsLifecycle: { false }
        ))
        #expect(guards.shouldForceRestartForSilentPush(sawWakeEvidence: false))
        #expect(!guards.shouldForceRestartForSilentPush(sawWakeEvidence: true))
    }

    /// The exact #60 race: at cycle start nobody owns the lifecycle, then the
    /// user opens the app mid-push and the foreground adopts the running
    /// engine. The forced-restart decision must see the ADOPTED state — a
    /// cycle-start snapshot would stop and restart the engine right under
    /// the foreground manager.
    @Test("Foreground adoption between cycle start and decision suppresses the forced restart")
    func adoptionMidPushSuppressesForcedRestart() {
        var foregroundOwns = false
        let guards = BackgroundSyncGuards(environment: .init(
            bridgeRunning: { true },
            foregroundOwnsLifecycle: { foregroundOwns }
        ))

        // Cycle start: engine running, no foreground claim — background
        // proceeds without ownership, as in a real suspended-process push.
        let ownership = guards.lifecycleOwnership(bridgeAlreadyRunning: guards.bridgeSnapshot())
        #expect(!ownership.foregroundOwns)
        #expect(!ownership.backgroundOwns)

        // Mid-push: the foreground scene adopts the engine (#60).
        foregroundOwns = true

        // Decision time: the guard's re-read must suppress the restart.
        #expect(!guards.shouldForceRestartForSilentPush(sawWakeEvidence: false))
    }

    @Test("bridgeSnapshot is the single bridge read of a cycle")
    func bridgeSnapshotReadsBridgeOnce() {
        var bridgeReads = 0
        let guards = BackgroundSyncGuards(environment: .init(
            bridgeRunning: { bridgeReads += 1; return true },
            foregroundOwnsLifecycle: { false }
        ))
        let running = guards.bridgeSnapshot()
        _ = guards.lifecycleOwnership(bridgeAlreadyRunning: running)
        _ = guards.shouldForceRestartForSilentPush(sawWakeEvidence: false)
        #expect(bridgeReads == 1)
    }
}
