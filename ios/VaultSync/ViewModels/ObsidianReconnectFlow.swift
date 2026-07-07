import Foundation

/// Sequencing core of the "reconnect the Obsidian folder" picker flow
/// (issue #53). Reconnecting produces no `pendingFolders` change event, so
/// the standing `onChange` trigger stays silent and existing pending shares
/// sat untouched until an unrelated change event — this flow runs the accept
/// pass explicitly after a successful grant.
///
/// The pass runs only AFTER the path reconcile has finished: an accept pass
/// running concurrently would compute its occupied-path set from the
/// pre-reconcile folder list — stale exactly when the user is repairing a
/// container move, which is when this flow typically runs.
///
/// `reconcile` cannot fail by type (the manager's reconcile task never
/// throws). If it never returns, the retry pass is deliberately NOT fired by
/// a timeout — a timed retry would reintroduce the stale-occupied-set race
/// this ordering exists to prevent; the standing `pendingFolders` change
/// trigger remains in place and covers any later change event.
///
/// All effects are injected so the sequencing is unit-testable without
/// SwiftUI, the filesystem, or the bridge.
enum ObsidianReconnectFlow {
    /// Runs the reconnect sequence. Returns the `grantAccess` error (the
    /// sequence aborts, nothing else runs), or nil once the retry pass ran.
    @MainActor
    static func run(
        grantAccess: @MainActor () -> String?,
        onGrantSucceeded: @MainActor () -> Void,
        reconcile: @MainActor () async -> Void,
        retryPendingShares: @MainActor () -> Void
    ) async -> String? {
        if let error = grantAccess() {
            return error
        }
        // Immediate UI feedback (failure reset, selection advisory) must not
        // wait for the reconcile's engine round-trips.
        onGrantSucceeded()
        await reconcile()
        retryPendingShares()
        return nil
    }
}
