import Foundation
import os

/// Single-flight admission for background sync runs (#183, decision 040).
/// Only one run — the leader — drives the shared engine; every run admitted
/// while the leader is in flight — a follower — waits for the leader and
/// reports the leader's result. A follower used to answer `.alreadyIdle` on
/// its own, so a failed leader run could still read as success (decision 029:
/// nothing reports success it did not observe).
///
/// Pure coordination, no bridge access: unit-testable without an engine.
final class BackgroundSyncSingleFlight: Sendable {
    typealias SyncResult = BackgroundSyncService.SyncResult

    enum Entry: Equatable, Sendable {
        /// This run drives the engine and must call `finish(_:)` exactly once.
        case leader
        /// A leader was in flight. `leaderResult` is what it ended with, or
        /// nil when this follower was cancelled before the leader finished
        /// (its own task expired) — never a synthetic success.
        case follower(leaderResult: SyncResult?)
    }

    private struct State: Sendable {
        var leaderInFlight = false
        /// The run token of the current leader, when it passed one: the
        /// expiration handlers stop the engine only for the leader (#183).
        var leaderToken: ObjectIdentifier?
        var nextWaiter: UInt64 = 0
        var waiters: [UInt64: CheckedContinuation<Entry, Never>] = [:]
    }

    private let lock = OSAllocatedUnfairLock(initialState: State())

    /// Admit a run. Admission and the follower's registration happen under one
    /// lock hold, so a leader cannot finish between the two and strand the
    /// follower. `onFollow` runs on the follower's task right after admission,
    /// before it suspends — the place for a rescan nudge the leader benefits
    /// from while it is still running.
    func enter(token: BackgroundSyncRunToken? = nil, onFollow: @Sendable () -> Void = {}) async -> Entry {
        let waiterID = lock.withLock { state -> UInt64 in
            state.nextWaiter += 1
            return state.nextWaiter
        }
        return await withTaskCancellationHandler {
            await withCheckedContinuation { (continuation: CheckedContinuation<Entry, Never>) in
                let immediate: Entry? = lock.withLock { state in
                    if !state.leaderInFlight {
                        state.leaderInFlight = true
                        state.leaderToken = token.map(ObjectIdentifier.init)
                        return .leader
                    }
                    // A cancellation that arrived before this registration ran
                    // its handler already (and found nothing to wake): answer
                    // it here instead of waiting for a leader forever.
                    if Task.isCancelled {
                        return .follower(leaderResult: nil)
                    }
                    state.waiters[waiterID] = continuation
                    return nil
                }
                if let immediate {
                    continuation.resume(returning: immediate)
                } else {
                    onFollow()
                }
            }
        } onCancel: {
            // Only whoever removes the waiter resumes it — the leader's
            // finish and this handler can race, but never both resume.
            let waiter = lock.withLock { $0.waiters.removeValue(forKey: waiterID) }
            waiter?.resume(returning: .follower(leaderResult: nil))
        }
    }

    /// The leader's terminal result: wakes every follower with it and frees
    /// the slot for the next leader.
    func finish(_ result: SyncResult) {
        let waiters = lock.withLock { state -> [CheckedContinuation<Entry, Never>] in
            state.leaderInFlight = false
            state.leaderToken = nil
            let pending = Array(state.waiters.values)
            state.waiters.removeAll()
            return pending
        }
        for waiter in waiters {
            waiter.resume(returning: .follower(leaderResult: result))
        }
    }

    /// Whether a leader is in flight right now (diagnostics and tests).
    var isLeaderInFlight: Bool {
        lock.withLock { $0.leaderInFlight }
    }

    /// Whether the run identified by `token` is the leader in flight. A
    /// follower's expiration handler asks this before touching the engine.
    func isLeader(_ token: BackgroundSyncRunToken) -> Bool {
        lock.withLock { $0.leaderInFlight && $0.leaderToken == ObjectIdentifier(token) }
    }
}
