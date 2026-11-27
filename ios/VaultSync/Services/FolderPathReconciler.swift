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

#if DEBUG
    /// LAB: shows folders as mapped for the design-preview fixture without
    /// writing the sidecar. The argument domain lives in memory only and is
    /// read before the stored one.
    static func _previewSetRel(_ map: [String: String]) {
        var arguments = UserDefaults.standard.volatileDomain(forName: UserDefaults.argumentDomain)
        arguments[relStoreKey] = map
        UserDefaults.standard.setVolatileDomain(arguments, forName: UserDefaults.argumentDomain)
    }
#endif

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
        /// Apply a path change and say how the bridge answered.
        var setPath: (_ folderID: String, _ newPath: String) -> SetPathResult
    }

    /// The bridge's answer to a path change.
    enum SetPathResult: Equatable, Sendable {
        case moved
        /// The directory carries the folder's marker but is not the directory
        /// the folder synced in — a copy of it (#315). The bridge leaves the
        /// folder where it is; the user is told why.
        case notItsStorage
        /// The directory is the folder's, but another folder syncs it, a
        /// folder inside it or a folder around it (#330, decision 082). Two
        /// folders never overlap; the bridge leaves the folder where it is,
        /// and the user is told why.
        case overlapsAnotherFolder
        /// Any other refusal or failure.
        case failed
    }

    /// What a reconcile found and could not repair by itself.
    struct Outcome: Equatable, Sendable {
        /// Folders whose directory is gone while a directory of their name
        /// exists under the selected folder that the bridge refused as a
        /// copy (#315).
        var notTheirStorage: Set<String> = []
        /// Folders whose directory is gone while their own directory, found
        /// under the selected folder, overlaps another folder's — where that
        /// one is, or where this reconcile would have repaired it to (#330).
        var overlapsAnotherFolder: Set<String> = []

        /// What of this outcome still holds for the folders configured now.
        /// A reconcile runs beside the main actor, and a folder removed
        /// while it ran is no longer there to report.
        func cut(to configured: some Sequence<String>) -> Outcome {
            let configured = Set(configured)
            return Outcome(
                notTheirStorage: notTheirStorage.intersection(configured),
                overlapsAnotherFolder: overlapsAnotherFolder.intersection(configured)
            )
        }
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
    ///    marker loss / deletions). Whether that directory is the one the
    ///    folder synced in is the bridge's decision (its marker, and the
    ///    marker's file number where the bridge recorded it — decision 063);
    ///    a directory it refuses as a copy is reported in the outcome, and
    ///    the folder stays where it is (#315). Two folders never overlap
    ///    (decision 082): before the bridge is asked for any repair, a
    ///    folder whose new path is, holds or lies in another folder's path is
    ///    held where it is, and where two new paths overlap both folders are
    ///    — whatever order the folders come in (#330). One that overlaps only
    ///    the old path of a folder moving away in the same pass waits for
    ///    the next pass, without a finding.
    ///  - **no mapping but currently under the root**: backfill the sidecar
    ///    (no path change) — this adopts already-healthy folders on first launch
    ///    after the update.
    ///  - **no mapping and not under the root**: leave untouched; such a folder
    ///    surfaces via the guided "remove this vault" flow.
    @discardableResult
    static func reconcile(folders: [(id: String, path: String)], env: Environment) -> Outcome {
        var outcome = Outcome()
        guard !folders.isEmpty else { return outcome }

        // Without a resolved Obsidian root this launch we can neither rebase
        // known folders nor backfill new ones — leave everything as-is.
        guard let rawRoot = env.obsidianRoot else { return outcome }
        let canonRoot = canonical(rawRoot)

        // Snapshot used only for per-folder decisions; writes go through the
        // atomic `recordRel`, so a concurrent reconcile/setRel can't be clobbered.
        let rel = env.loadRel()

        // The repairs this pass would ask the bridge for, in folder order.
        // They are asked for only once all of them are known (#330).
        var repairs: [(id: String, target: String)] = []

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
                repairs.append((id: folder.id, target: desired))
                continue
            }

            // No mapping yet: adopt a folder that currently lives under the root
            // (first launch after the update) so future launches can rebase it.
            if let r = relativeIfUnder(storedCanon, root: canonRoot) {
                env.recordRel(folder.id, r)
            }
        }

        let holds = heldRepairs(repairs, folders: folders)
        for repair in repairs {
            if holds.held.contains(repair.id) {
                outcome.overlapsAnotherFolder.insert(repair.id)
                logger.warning("Held a folder rebase back because the target overlaps another folder's directory")
                continue
            }
            if holds.waiting.contains(repair.id) {
                logger.info("Deferred a folder rebase until a folder in its target has moved away")
                continue
            }
            switch env.setPath(repair.id, repair.target) {
            case .moved:
                logger.info("Rebased a folder to the current Obsidian location")
            case .notItsStorage:
                outcome.notTheirStorage.insert(repair.id)
                logger.warning("Skipped folder rebase because the target is a copy of the folder's directory")
            case .overlapsAnotherFolder:
                outcome.overlapsAnotherFolder.insert(repair.id)
                logger.warning("Skipped folder rebase because the target overlaps another folder's directory")
            case .failed:
                logger.warning("Folder rebase failed")
            }
        }
        return outcome
    }

    /// The repairs of one pass that would leave two folders overlapping on
    /// disk (decision 001), held back before the bridge is asked for any
    /// (#330, decision 082). The bridge's own floor sees only where the
    /// other folders are now; in a container move every folder of an
    /// overlapping pair still has its old path until its repair, so the
    /// floor alone takes whichever comes first.
    ///
    /// Every repair counts whose target exists, whether or not the folder's
    /// marker is there: an earlier version that put two folders into one
    /// directory left a marker for the first only — the engine writes one
    /// only where `.stfolder` is missing — and a pair must not come apart over
    /// that.
    ///
    /// - **held**: where two repairs' targets overlap, both — neither folder
    ///   goes there, and each keeps its old path, with the finding, until one
    ///   of the two is removed. A repair whose target is, holds or lies in
    ///   the configured path of a folder that is not being repaired: that
    ///   folder stays there.
    /// - **waiting**: a repair whose target overlaps only the old path of a
    ///   folder that is itself being repaired in this pass. It is not asked
    ///   for and gets no finding: the next pass repairs it once that folder
    ///   has moved away.
    static func heldRepairs(
        _ repairs: [(id: String, target: String)],
        folders: [(id: String, path: String)]
    ) -> (held: Set<String>, waiting: Set<String>) {
        let targets = Dictionary(
            repairs.map { ($0.id, overlapKey($0.target)) },
            uniquingKeysWith: { first, _ in first }
        )
        var held = Set<String>()
        var waiting = Set<String>()
        for repair in repairs {
            guard let key = targets[repair.id] else { continue }
            for other in folders where other.id != repair.id && keysOverlap(key, overlapKey(other.path)) {
                if targets[other.id] != nil {
                    waiting.insert(repair.id)
                } else {
                    held.insert(repair.id)
                }
            }
            for other in repairs where other.id != repair.id {
                if let otherKey = targets[other.id], keysOverlap(key, otherKey) {
                    held.insert(repair.id)
                    held.insert(other.id)
                }
            }
        }
        return (held, waiting.subtracting(held))
    }

    /// A path in the form two paths are compared in for an overlap: canonical
    /// and lowercased, as `PathCollisionGuard` compares them — the iOS data
    /// volume is case-folding APFS. `canonical` drops a leading `/private`
    /// only from a path that exists; dropping it here too lets a folder's
    /// old path, gone, compare in the same form as a target that exists.
    private static func overlapKey(_ path: String) -> String {
        let key = canonical(path).lowercased()
        return key.hasPrefix("/private/") ? String(key.dropFirst("/private".count)) : key
    }

    /// The same directory, or one inside the other. Boundary-aware: "/vaulta"
    /// does not lie in "/vault".
    private static func keysOverlap(_ a: String, _ b: String) -> Bool {
        a == b || a.hasPrefix(b + "/") || b.hasPrefix(a + "/")
    }

    // MARK: - Live reconcile (wired to the bridge)

    private struct BridgeFolder: Decodable {
        let id: String
        let path: String
    }

    /// Read the live folder list from the bridge and reconcile their paths
    /// against the current Obsidian root. Blocks briefly on bridge calls, so
    /// run it off the main actor.
    @discardableResult
    static func reconcileLive(obsidianRoot: String?) -> Outcome {
        // First of all: every folder whose directory can be reached now gets
        // its record, before anything is decided. A folder that was out of
        // reach when the engine started is in reach at this point at the
        // latest — a reconcile follows every folder selection (#315).
        SyncBridgeService.noteFolderStorage()
        let json = SyncBridgeService.getFoldersJSON()
        guard let data = json.data(using: .utf8),
              let decoded = try? JSONDecoder().decode([BridgeFolder].self, from: data) else {
            return Outcome()
        }
        let folders = decoded.map { (id: $0.id, path: $0.path) }
        guard !folders.isEmpty else { return Outcome() }

        let env = Environment(
            obsidianRoot: obsidianRoot,
            loadRel: loadRel,
            recordRel: { folderID, rel in setRel(rel, forFolder: folderID) },
            dirExists: { path in
                var isDir: ObjCBool = false
                return FileManager.default.fileExists(atPath: path, isDirectory: &isDir) && isDir.boolValue
            },
            setPath: bridgeSetPath
        )
        return reconcile(folders: folders, env: env)
    }

    /// Ask the bridge to re-point a folder, and tell its refusals apart by
    /// the texts it hands out for them.
    static func bridgeSetPath(folderID: String, newPath: String) -> SetPathResult {
        guard let refusal = SyncBridgeService.setFolderPath(folderID: folderID, path: newPath) else {
            return .moved
        }
        switch refusal {
        case SyncBridgeService.folderStorageMismatchError:
            return .notItsStorage
        case SyncBridgeService.folderPathOverlapError:
            return .overlapsAnotherFolder
        default:
            return .failed
        }
    }
}
