import Foundation

/// The line a pending share shows about where it will arrive (#270).
///
/// Built from the accept's own decision (`VaultManager.previewShareDestination`),
/// so the line and the accept cannot name different places. Advisory: the
/// accept decides again when it runs. Pure, so every case is unit-testable.
enum PendingShareDestination {

    struct Line: Equatable, Sendable {
        let text: String
        /// The destination needs a second look before accepting: Obsidian
        /// cannot show it, or it already holds files.
        let isCaution: Bool
    }

    /// Nil when there is nothing to say for certain: the folder is not
    /// connected, or the accept would refuse — the refusal then explains
    /// itself when the share is tried.
    static func line(
        decision: ShareTargetDecision?,
        rootPlace: VaultManager.SelectedFolderPlace?
    ) -> Line? {
        switch decision {
        case nil, .refused:
            return nil
        case .path(let path):
            let name = (path as NSString).lastPathComponent
            // A selected folder outside every app's own folder: the share
            // arrives in full, and Obsidian never opens it. Saying so here
            // is the last moment before the tap.
            if rootPlace == .elsewhere {
                return Line(
                    text: L10n.fmt("Arrives as “%@” in the folder you selected. Obsidian cannot show vaults from that folder.", name),
                    isCaution: true
                )
            }
            return Line(
                text: L10n.fmt("Arrives as “%@” in your Obsidian folder.", name),
                isCaution: false
            )
        case .requiresMergeConfirmation(_, let targetName):
            return Line(
                text: L10n.fmt("The folder “%@” already holds files. VaultSync asks you before it combines them with this vault.", targetName),
                isCaution: true
            )
        }
    }
}
