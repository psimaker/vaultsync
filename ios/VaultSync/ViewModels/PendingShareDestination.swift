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
    ///
    /// `rootPlace` — where the selected folder lies. Only an app's own
    /// folder is a place Obsidian lists vaults from; under any other the
    /// line says so, because the share arrives in full either way.
    ///
    /// `rootHoldsEarlierCopy` — the selected folder still holds a vault's
    /// files directly, left by an accept that synced into the folder itself.
    /// A target that "already holds files" is then most likely one of that
    /// vault's own subfolders, and combining would turn the subfolder into
    /// the vault.
    static func line(
        decision: ShareTargetDecision?,
        rootPlace: VaultManager.SelectedFolderPlace?,
        rootHoldsEarlierCopy: Bool = false
    ) -> Line? {
        let obsidianListsIt = rootPlace == .appFolder
        switch decision {
        case nil, .refused:
            return nil
        case .path(let path):
            let name = (path as NSString).lastPathComponent
            guard obsidianListsIt else {
                return Line(
                    text: L10n.fmt("Arrives as “%@” in the folder you selected. Obsidian will not list it as a vault there.", name),
                    isCaution: true
                )
            }
            return Line(
                text: L10n.fmt("Arrives as “%@” in your Obsidian folder.", name),
                isCaution: false
            )
        case .requiresMergeConfirmation(_, let targetName):
            guard obsidianListsIt else {
                return Line(
                    text: L10n.fmt("The folder “%@” already holds files, and Obsidian will not list a vault in the folder you selected. VaultSync asks you before it combines anything.", targetName),
                    isCaution: true
                )
            }
            if rootHoldsEarlierCopy {
                return Line(
                    text: L10n.fmt("The folder “%@” already holds files. If they belong to the copy that arrived earlier, do not combine them: tap \"Choose Vault…\" and give this vault another folder name.", targetName),
                    isCaution: true
                )
            }
            return Line(
                text: L10n.fmt("The folder “%@” already holds files. VaultSync asks you before it combines them with this vault.", targetName),
                isCaution: true
            )
        }
    }
}
