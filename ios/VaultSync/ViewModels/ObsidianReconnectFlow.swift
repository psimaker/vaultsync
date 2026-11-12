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

    /// Where the folder picker opens. Inside the selected folder when that
    /// is an app's own folder: reconnecting means tapping Open right there.
    /// One level up for any other folder: that folder is the wrong one
    /// (#270), usually titled "Obsidian" itself, and opening inside it puts
    /// Open under the user's thumb. A value derived from the selected
    /// folder, not a flag set next to the sheet's — the sheet reads it
    /// whenever it is built. The picker treats it as a starting point only.
    static func pickerStartURL(selectedFolder: URL?) -> URL? {
        guard let selectedFolder else { return nil }
        return VaultManager.selectedFolderPlace(selectedFolder.path) == .appFolder
            ? selectedFolder
            : selectedFolder.deletingLastPathComponent()
    }

    /// What the host's alert shows when `grantAccess` refuses the folder.
    /// The refusal is guidance — which folder to pick instead, and how —
    /// and reaches the user as written. Both hosts passed it through the
    /// engine-error mapper, which knows none of these texts and showed
    /// "VaultSync reported an unexpected error." in their place (#270).
    static func alertText(forRefusedGrant guidance: String) -> String {
        guidance
    }
}
