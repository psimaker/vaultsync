import Foundation

/// The text of the question before a share is combined with a folder that
/// already holds files (decision 007; #308, decision 066).
///
/// The question never names removing a vault and accepting it again as the
/// case to combine: that is also the way a vault comes back after its folder
/// was replaced by a copy (#315) or after it synced straight into the
/// selected folder (#270), and combining with a copy can send its older
/// text of a note out as the newer one (#331). Pure, so both texts are
/// unit-testable.
enum MergeQuestion {

    /// `targetInEarlierCopy` — the selected folder still holds a vault's
    /// earlier copy (`ObsidianVisibility.holdsEarlierCopy`), so the target
    /// is most likely one of its folders.
    static func message(targetName: String, shareLabel: String, targetInEarlierCopy: Bool) -> String {
        if targetInEarlierCopy {
            return L10n.fmt(
                "The folder \"%@\" lies in the folder you selected, which still holds the files of a vault that synced straight into it, so it is most likely part of that earlier copy. If you accept, its files will be combined with the shared vault \"%@\" and synced to the other devices sharing this vault: notes of the earlier copy can appear a second time, at the top level of the vault, and where it holds an older version of a note, the older version can replace the newer one on every device. Do not combine them: cancel and use \"Choose Vault…\" to give this vault another folder name.",
                targetName,
                shareLabel
            )
        }
        return L10n.fmt(
            "The folder \"%@\" already contains files. If you accept, those files and the contents of the shared vault \"%@\" will be combined and synced to the other devices sharing this vault. Accept only if this is the folder this vault synced in on this iPhone, unchanged since and not a copy put in its place, or if it holds notes you want to add to this vault. Never combine a copy of this vault: where a copy holds an older version of a note, the older version can replace the newer one on every device. For a copy, a different vault or unrelated files, cancel and use \"Choose Vault…\" to give this vault another folder name.",
            targetName,
            shareLabel
        )
    }
}
