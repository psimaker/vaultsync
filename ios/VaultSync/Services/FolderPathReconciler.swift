import Foundation
import os

private let logger = Logger(subsystem: "eu.vaultsync.app", category: "folderpaths")

/// Keeps Syncthing folder paths valid across iOS container changes.
///
/// iOS does not guarantee that an app's absolute sandbox path stays the same
/// across reinstall, restore-from-backup, or migration — yet Syncthing persists
/// **absolute** folder paths in its `config.xml`. When the container path
/// changes, a baked-in path goes stale and the folder enters a permanent
/// "operation not permitted" error (issue #25).
///
/// This reconciler removes that fragility by re-deriving each folder's path from
/// a stable source on **every** engine start: the security-scoped bookmark for
/// the Obsidian directory (re-resolved each launch) plus a small per-folder
/// "relative path under the Obsidian root" sidecar. Folders effectively become
/// container-relative, so the absolute path stored in `config.xml` is corrected
/// in place before it can cause an error.
///
/// `nonisolated` statics so it is callable from both the `@MainActor`
/// `SyncthingManager` (foreground) and the `nonisolated` `BackgroundSyncService`
/// (background). The sidecar lives in `UserDefaults.standard`, which is shared
/// across the app's foreground and background handlers (same process); the
/// widget extension has no need for it.
enum FolderPathReconciler {

    // MARK: - Sidecar store (folderID -> relative path under the Obsidian root)

    /// `""` means the folder syncs the whole Obsidian directory; a non-empty
    /// value (e.g. "Personal") is a vault subdirectory inside it.
    private static let relStoreKey = "vaultsync.folderRelPath"

    static func loadRel() -> [String: String] {
        UserDefaults.standard.dictionary(forKey: relStoreKey) as? [String: String] ?? [:]
    }

    static func saveRel(_ map: [String: String]) {
        UserDefaults.standard.set(map, forKey: relStoreKey)
    }

    /// Record the relative path for a folder (called when a folder is created).
    static func setRel(_ rel: String, forFolder folderID: String) {
        var map = loadRel()
        guard map[folderID] != rel else { return }
        map[folderID] = rel
        saveRel(map)
    }

    /// Drop a folder's mapping (called when a folder is removed) so a later
    /// folder reusing the same ID does not inherit a stale relative path.
    static func removeRel(forFolder folderID: String) {
        var map = loadRel()
        guard map[folderID] != nil else { return }
        map.removeValue(forKey: folderID)
        saveRel(map)
    }

    // MARK: - Path helpers

    /// Normalize a path so paths that differ only by `/var`↔`/private/var`
    /// symlinks or a trailing slash compare equal. Shared with `ContentView`'s
    /// folder→vault row mapping so rebasing and display use the same rule.
    static func canonical(_ path: String) -> String {
        URL(fileURLWithPath: path).resolvingSymlinksInPath().standardizedFileURL.path
    }

    /// Absolute path a folder should live at, given the (canonical) Obsidian
    /// root and the folder's relative subpath.
    static func desiredPath(root: String, rel: String) -> String {
        rel.isEmpty ? root : (root as NSString).appendingPathComponent(rel)
    }

    /// The relative path of `pathCanon` under `root` (both canonical), or nil if
    /// it is not the root and not inside it. Returns `""` when they are equal.
    static func relativeIfUnder(_ pathCanon: String, root: String) -> String? {
        if pathCanon == root { return "" }
        let prefix = root.hasSuffix("/") ? root : root + "/"
        if pathCanon.hasPrefix(prefix) {
            return String(pathCanon.dropFirst(prefix.count))
        }
        return nil
    }

    // MARK: - Reconcile (pure core, injectable for tests)

    /// Everything the reconcile needs from the outside world. Injecting these
    /// keeps the core logic free of the bridge, the filesystem, and UserDefaults
    /// so it is exhaustively unit-testable.
    struct Environment {
        var obsidianRoot: String?
        var loadRel: () -> [String: String]
        var saveRel: ([String: String]) -> Void
        /// Whether `path` currently exists as a directory.
        var dirExists: (String) -> Bool
        /// Apply a path change; returns an error message or nil.
        var setPath: (_ folderID: String, _ newPath: String) -> String?
    }

    /// For each folder:
    ///  - **known mapping**: rebase to `root(+rel)` when it differs and the
    ///    target directory actually exists (never point a folder at a missing
    ///    or empty directory — that risks marker loss / deletions).
    ///  - **no mapping but currently under the root**: backfill the sidecar
    ///    (no path change) — this adopts already-healthy folders on first launch
    ///    after the update.
    ///  - **no mapping and not under the root**: leave untouched; such a folder
    ///    surfaces via the guided "remove this vault" flow.
    static func reconcile(folders: [(id: String, path: String)], env: Environment) {
        guard !folders.isEmpty else { return }

        // Without a resolved Obsidian root this launch we can neither rebase
        // known folders nor backfill new ones — leave everything as-is.
        guard let rawRoot = env.obsidianRoot else { return }
        let canonRoot = canonical(rawRoot)

        var rel = env.loadRel()
        var relChanged = false

        for folder in folders {
            let storedCanon = canonical(folder.path)

            if let r = rel[folder.id] {
                // Rebase to the bookmark's natural path form; compare canonically
                // so a pure /var↔/private/var difference is not a "change".
                let desired = desiredPath(root: rawRoot, rel: r)
                guard canonical(desired) != storedCanon else { continue }
                guard env.dirExists(desired) else {
                    logger.warning("Skip rebase for \(folder.id, privacy: .public): target missing")
                    continue
                }
                if let err = env.setPath(folder.id, desired) {
                    logger.warning("Rebase failed for \(folder.id, privacy: .public): \(err, privacy: .public)")
                } else {
                    logger.info("Rebased folder \(folder.id, privacy: .public) to current Obsidian path")
                }
                continue
            }

            // No mapping yet: adopt a folder that currently lives under the root
            // (first launch after the update) so future launches can rebase it.
            if let r = relativeIfUnder(storedCanon, root: canonRoot) {
                rel[folder.id] = r
                relChanged = true
            }
        }

        if relChanged {
            env.saveRel(rel)
        }
    }

    // MARK: - Live reconcile (wired to the bridge)

    private struct BridgeFolder: Decodable {
        let id: String
        let path: String
    }

    /// Read the live folder list from the bridge and reconcile their paths
    /// against the current Obsidian root. Blocks briefly on bridge calls, so
    /// run it off the main actor.
    static func reconcileLive(obsidianRoot: String?) {
        let json = SyncBridgeService.getFoldersJSON()
        guard let data = json.data(using: .utf8),
              let decoded = try? JSONDecoder().decode([BridgeFolder].self, from: data) else {
            return
        }
        let folders = decoded.map { (id: $0.id, path: $0.path) }
        guard !folders.isEmpty else { return }

        let env = Environment(
            obsidianRoot: obsidianRoot,
            loadRel: loadRel,
            saveRel: saveRel,
            dirExists: { path in
                var isDir: ObjCBool = false
                return FileManager.default.fileExists(atPath: path, isDirectory: &isDir) && isDir.boolValue
            },
            setPath: { folderID, newPath in
                SyncBridgeService.setFolderPath(folderID: folderID, path: newPath)
            }
        )
        reconcile(folders: folders, env: env)
    }
}
