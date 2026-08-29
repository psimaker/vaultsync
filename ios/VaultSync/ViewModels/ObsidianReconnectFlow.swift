import Foundation

/// Sequencing core of the "reconnect the Obsidian folder" picker flow.
/// A successful access grant is published immediately, then existing folder
/// paths settle before the flow returns. Pending shares are deliberately not
/// touched: their 2.0.2 surface is inspection-only (#150).
///
/// All effects are injected so the sequencing is unit-testable without
/// SwiftUI, the filesystem, or the bridge.
enum ObsidianReconnectFlow {
    /// Runs the reconnect sequence. Returns the `grantAccess` error (the
    /// sequence aborts, nothing else runs), or nil once reconciliation ends.
    @MainActor
    static func run(
        grantAccess: @MainActor () -> String?,
        onGrantSucceeded: @MainActor () -> Void,
        reconcile: @MainActor () async -> Void
    ) async -> String? {
        if let error = grantAccess() {
            return error
        }
        // Immediate UI feedback (failure reset, selection advisory) must not
        // wait for the reconcile's engine round-trips.
        onGrantSucceeded()
        await reconcile()
        return nil
    }
}
