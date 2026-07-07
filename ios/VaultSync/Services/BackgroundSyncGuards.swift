import Foundation

/// The lifecycle guards of `BackgroundSyncService.performBackgroundSync`,
/// extracted behind injectable seams so their exact read-timing is
/// unit-pinnable without a running engine (#61).
///
/// Why timing matters more than the boolean logic here: the forced-restart
/// guard re-reads the foreground lifecycle lock AT DECISION TIME. A snapshot
/// from cycle start goes stale exactly when the user opens the app mid-push
/// and the foreground adopts the running engine (#60) — restarting on the
/// stale value would stop the engine right under the adopted foreground
/// manager. That re-read is the invariant these seams pin.
struct BackgroundSyncGuards {

    /// Process-global reads the guards depend on. Production uses `.live`;
    /// tests inject closures and count/flip them mid-cycle.
    struct Environment {
        /// One blocking bridge call: is the embedded engine running?
        var bridgeRunning: () -> Bool
        /// Does the foreground scene currently own the engine lifecycle?
        var foregroundOwnsLifecycle: () -> Bool
    }

    let environment: Environment

    /// The single bridge read at cycle start. Both consumers — the
    /// silent-push fast path and the ownership decision — must judge the
    /// SAME snapshot: a second read could see a different engine state and
    /// let the cycle both rescan-as-running and start-as-stopped.
    func bridgeSnapshot() -> Bool {
        environment.bridgeRunning()
    }

    /// Silent pushes arrive after iOS suspended the process: the bridge's
    /// cached state still reports running while the kernel already tore down
    /// the peer sockets. A rescan wakes Go's dialer without the 5–15 s cost
    /// of a stop/start cycle — only worth it when the engine actually runs.
    static func shouldFastPathRescan(reason: String, bridgeAlreadyRunning: Bool) -> Bool {
        reason == "silent-push" && bridgeAlreadyRunning
    }

    struct LifecycleOwnership {
        /// The lock value the decision was based on — callers trace it.
        let foregroundOwns: Bool
        /// True when this background cycle must start (and later stop) the
        /// engine itself.
        let backgroundOwns: Bool
    }

    /// Reads the lifecycle lock exactly once. Background owns the engine only
    /// when nobody else does: not a still-running engine (a previous owner's),
    /// not a foreground scene holding the claim.
    func lifecycleOwnership(bridgeAlreadyRunning: Bool) -> LifecycleOwnership {
        let foregroundOwns = environment.foregroundOwnsLifecycle()
        return LifecycleOwnership(
            foregroundOwns: foregroundOwns,
            backgroundOwns: !bridgeAlreadyRunning && !foregroundOwns
        )
    }

    /// The forced-restart guard for a silent push that showed no wake
    /// evidence. Re-reads the lock at decision time — NEVER a cycle-start
    /// snapshot; see the type comment for the #60 race this closes. The
    /// expiration handlers re-read the same way.
    func shouldForceRestartForSilentPush(sawWakeEvidence: Bool) -> Bool {
        !sawWakeEvidence && !environment.foregroundOwnsLifecycle()
    }
}

extension BackgroundSyncGuards.Environment {
    /// Computed, not stored: a closure struct in a shared `static let` is not
    /// concurrency-safe under Swift 6 — each sync cycle gets a fresh value.
    static var live: Self {
        Self(
            bridgeRunning: { SyncBridgeService.isRunning() },
            foregroundOwnsLifecycle: {
                BackgroundSyncService.lifecycleLock.withLock { $0.foregroundActive }
            }
        )
    }
}
