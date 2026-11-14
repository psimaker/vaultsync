import Foundation

/// Pure core behind the passive "not syncing yet" vault rows (#79): which
/// detected vaults have no Syncthing folder syncing them. Without these rows
/// a connected-but-not-yet-shared setup renders an empty vault list, which
/// reads as "detection is broken" (the #79 report) rather than "sharing is
/// the missing step".
///
/// Inputs arrive canonicalized and lowercased (`FolderPathReconciler.canonical`
/// + `lowercased()`, matching the occupied-set convention in
/// `VaultManager.resolveSharePath`) so `/var`↔`/private/var` and case-folding
/// APFS never make a synced vault look unsynced. Coverage is exact-path only:
/// a whole-directory folder at the root covers every vault, a per-vault
/// folder covers exactly `root/<name>`. Anything else (a folder overlapping
/// from outside the root) is the collision guards' territory, not this row's.
enum UnsyncedVaultsModel {

    /// `refusedCopyRels` are the places, relative to the root, of copies
    /// that were refused in a vault's place (#315) — "" for the root itself.
    /// Such a copy looks like a vault nothing syncs, and its vault's card
    /// already says what it is: listed here it would be offered for sharing
    /// from the computer, right below the card that says VaultSync does not
    /// go on in it.
    static func derive(
        detectedVaults: [String],
        folderPathsCanonLower: Set<String>,
        rootCanonLower: String?,
        refusedCopyRels: [String] = []
    ) -> [String] {
        guard let root = rootCanonLower, !root.isEmpty else { return [] }
        let taken = folderPathsCanonLower.union(refusedCopyRels.map { rel in
            rel.isEmpty ? root : (root as NSString).appendingPathComponent(rel.lowercased())
        })
        if taken.contains(root) { return [] }
        return detectedVaults.filter { vault in
            let subfolder = (root as NSString).appendingPathComponent(vault.lowercased())
            return !taken.contains(subfolder)
        }
    }
}
