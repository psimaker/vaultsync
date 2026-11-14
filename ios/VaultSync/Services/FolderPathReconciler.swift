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

    /// Serializes the read-modify-write of the sidecar so concurrent reconciles
    /// (foreground + background) and `setRel`/`removeRel` (from the main actor)
    /// cannot lose a mapping through interleaved updates. The blocks only touch
    /// UserDefaults, so `sync` never risks a main-thread deadlock.
    private static let queue = DispatchQueue(label: "eu.vaultsync.folderpaths.sidecar")

    static func loadRel() -> [String: String] {
        queue.sync { UserDefaults.standard.dictionary(forKey: relStoreKey) as? [String: String] ?? [:] }
    }

    /// Record the relative path for a folder (when it is created, and when a
    /// healthy folder is adopted during reconcile). Atomic read-modify-write.
    static func setRel(_ rel: String, forFolder folderID: String) {
        queue.sync {
            var map = UserDefaults.standard.dictionary(forKey: relStoreKey) as? [String: String] ?? [:]
            guard map[folderID] != rel else { return }
            map[folderID] = rel
            UserDefaults.standard.set(map, forKey: relStoreKey)
        }
    }

    /// Drop a folder's mapping (when a folder is removed) so a later folder
    /// reusing the same ID does not inherit a stale relative path. Atomic.
    static func removeRel(forFolder folderID: String) {
        queue.sync {
            var map = UserDefaults.standard.dictionary(forKey: relStoreKey) as? [String: String] ?? [:]
            guard map[folderID] != nil else { return }
            map.removeValue(forKey: folderID)
            UserDefaults.standard.set(map, forKey: relStoreKey)
        }
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
        /// A snapshot of the current mappings, read once for decisions.
        var loadRel: () -> [String: String]
        /// Atomically record one folder's mapping (no bulk overwrite, so it can't
        /// clobber a concurrent writer's update to a different folder).
        var recordRel: (_ folderID: String, _ rel: String) -> Void
        /// Whether `path` currently exists as a directory.
        var dirExists: (String) -> Bool
        /// Apply a path change; returns an error message or nil.
        var setPath: (_ folderID: String, _ newPath: String) -> String?
    }

    /// For each folder:
    ///  - **known mapping, configured path still alive on disk**: never re-point
    ///    the folder — its data is right where config says, and re-pointing a
    ///    live folder is how a share gets attached to *different* content (e.g.
    ///    a `rel=""` vault-as-root folder would be yanked onto the whole
    ///    container after the user re-selects the parent folder; #45 follow-up).
    ///    Instead, reality wins: refresh the sidecar from the live path when it
    ///    lies under the current root, so a future container change rebases to
    ///    the right place.
    ///  - **known mapping, configured path gone**: the container moved (#25) —
    ///    rebase to `root(+rel)` when the target directory actually exists
    ///    (never point a folder at a missing or empty directory — that risks
    ///    marker loss / deletions).
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

        // Snapshot used only for per-folder decisions; writes go through the
        // atomic `recordRel`, so a concurrent reconcile/setRel can't be clobbered.
        let rel = env.loadRel()

        for folder in folders {
            let storedCanon = canonical(folder.path)

            if let r = rel[folder.id] {
                // Compare canonically so a pure /var↔/private/var difference is
                // not a "change".
                let desired = desiredPath(root: rawRoot, rel: r)
                guard canonical(desired) != storedCanon else { continue }

                // The mapping disagrees with the configured path. If that path
                // is still alive on disk, the folder's data is right there —
                // keep the path and refresh the mapping from reality instead.
                if env.dirExists(folder.path) {
                    if let actual = relativeIfUnder(storedCanon, root: canonRoot),
                       actual != r {
                        env.recordRel(folder.id, actual)
                        logger.info("Refreshed a live folder mapping")
                    }
                    continue
                }

                // Configured path is gone — the container moved (#25). Rebase
                // to the bookmark's natural path form.
                guard env.dirExists(desired) else {
                    logger.warning("Skipped folder rebase because the target is missing")
                    continue
                }
                if env.setPath(folder.id, desired) != nil {
                    logger.warning("Folder rebase failed")
                } else {
                    logger.info("Rebased a folder to the current Obsidian location")
                }
                continue
            }

            // No mapping yet: adopt a folder that currently lives under the root
            // (first launch after the update) so future launches can rebase it.
            if let r = relativeIfUnder(storedCanon, root: canonRoot) {
                env.recordRel(folder.id, r)
            }
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
            recordRel: { folderID, rel in setRel(rel, forFolder: folderID) },
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
