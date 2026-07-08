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

    static func derive(
        detectedVaults: [String],
        folderPathsCanonLower: Set<String>,
        rootCanonLower: String?
    ) -> [String] {
        guard let root = rootCanonLower, !root.isEmpty else { return [] }
        if folderPathsCanonLower.contains(root) { return [] }
        return detectedVaults.filter { vault in
            let subfolder = (root as NSString).appendingPathComponent(vault.lowercased())
            return !folderPathsCanonLower.contains(subfolder)
        }
    }
}
