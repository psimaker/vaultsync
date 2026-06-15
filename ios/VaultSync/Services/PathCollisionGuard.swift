import Foundation
import os

private let logger = Logger(subsystem: "eu.vaultsync.app", category: "pathcollision")

/// Detects and contains the "two vaults, one local folder" collision (issue #45)
/// on devices that an earlier version already merged.
///
/// The #45 fix (`VaultManager.resolveSharePath` + the Go `AcceptPendingFolder`
/// guard) prevents *new* collisions, but a device that collided under 1.6.0 /
/// 1.7.0 still has ≥2 folders configured on one local path — Syncthing keeps
/// merging their contents and pushing the mix back to every peer. This guard is
/// the migration shield: on launch it groups configured folders by their
/// canonical local path and, for any path shared by ≥2 folders, pauses each
/// colliding folder exactly ONCE to stop the corruption immediately, then leaves
/// a critical banner up until the user separates them. Recovery stays manual and
/// non-destructive — nothing is deleted, renamed, moved, or re-accepted
/// automatically.
///
/// Mirrors `FolderPathReconciler`: a pure, injectable core for exhaustive unit
/// tests; a thin live wrapper wired to the bridge; and a `UserDefaults` sidecar
/// guarded by a serialized queue. The same canonical-path rule
/// (`FolderPathReconciler.canonical` + `.lowercased()`) is reused so detection
/// agrees byte-for-byte with the accept-time guard (case-folding APFS).
///
/// `nonisolated` statics so it is callable from both the `@MainActor`
/// `SyncthingManager` and off-main detached tasks; the sidecar lives in
/// `UserDefaults.standard`, shared across the app's foreground and background
/// handlers in the same process.
enum PathCollisionGuard {

    // MARK: - Detection (pure)

    /// Group folders by canonical, lowercased local path and return only the
    /// groups where ≥2 distinct folders share one directory. Each returned set
    /// is one active collision (two vaults merging into one folder, #45); a path
    /// held by a single folder is never returned, so there are no false
    /// positives.
    ///
    /// `canonicalize` is injected (production: `FolderPathReconciler.canonical`);
    /// keys are lowercased because the iOS data volume is case-folding APFS —
    /// exactly how the accept-time guard compares paths.
    static func collidingFolderGroups(
        _ folders: [(id: String, path: String)],
        canonicalize: (String) -> String
    ) -> [Set<String>] {
        var idsByPath: [String: Set<String>] = [:]
        for folder in folders {
            let key = canonicalize(folder.path).lowercased()
            idsByPath[key, default: []].insert(folder.id)
        }
        return idsByPath.values.filter { $0.count >= 2 }
    }

    /// Flat set of every folder ID that belongs to some collision group.
    static func collidingFolderIDs(
        _ folders: [(id: String, path: String)],
        canonicalize: (String) -> String
    ) -> Set<String> {
        collidingFolderGroups(folders, canonicalize: canonicalize)
            .reduce(into: Set<String>()) { $0.formUnion($1) }
    }

    // MARK: - Auto-paused sidecar (folder IDs we have auto-paused exactly once)

    /// IDs we have already auto-paused. Persisted so each colliding folder is
    /// paused only ONCE: after that we never touch its pause state again, so a
    /// user who deliberately resumes a folder to recover is not fought.
    private static let autoPausedStoreKey = "vaultsync.pathCollisionAutoPaused"

    /// Serializes the read-modify-write of the sidecar so a launch-time pause
    /// pass and a `clearAutoPaused` (folder removal, main actor) cannot lose an
    /// entry through interleaving. The blocks touch only UserDefaults, so `sync`
    /// never risks a main-thread deadlock — same pattern as `FolderPathReconciler`.
    private static let queue = DispatchQueue(label: "eu.vaultsync.pathcollision.sidecar")

    static func loadAutoPaused() -> Set<String> {
        queue.sync { Set(UserDefaults.standard.array(forKey: autoPausedStoreKey) as? [String] ?? []) }
    }

    /// Record that a folder has been auto-paused. Atomic read-modify-write.
    static func markAutoPaused(_ folderID: String) {
        queue.sync {
            var ids = Set(UserDefaults.standard.array(forKey: autoPausedStoreKey) as? [String] ?? [])
            guard !ids.contains(folderID) else { return }
            ids.insert(folderID)
            UserDefaults.standard.set(ids.sorted(), forKey: autoPausedStoreKey)
        }
    }

    /// Forget a folder's auto-paused record (called when the folder is removed)
    /// so a future folder reusing the same ID can be paused again if it collides.
    /// Atomic.
    static func clearAutoPaused(_ folderID: String) {
        queue.sync {
            var ids = Set(UserDefaults.standard.array(forKey: autoPausedStoreKey) as? [String] ?? [])
            guard ids.contains(folderID) else { return }
            ids.remove(folderID)
            UserDefaults.standard.set(ids.sorted(), forKey: autoPausedStoreKey)
        }
    }

    // MARK: - Pause-once core (pure, injectable)

    /// Everything the pause pass needs from the outside world. Injecting these
    /// keeps the logic free of the bridge and UserDefaults so it is exhaustively
    /// unit-testable.
    struct Environment {
        var canonicalize: (String) -> String
        var loadAutoPaused: () -> Set<String>
        var markAutoPaused: (String) -> Void
        /// Pause one folder; returns an error message, or nil on success.
        var setPaused: (_ folderID: String) -> String?
    }

    /// Pause every colliding folder that has not yet been auto-paused — exactly
    /// once each. A folder already paused (by the user, or a previous pass) is
    /// recorded without a redundant bridge call. A folder we have auto-paused
    /// before is skipped entirely and never re-paused, so a deliberate resume for
    /// recovery is respected (the explicit #45 guardrail). An ID is recorded only
    /// after a successful — or already-in-effect — pause, so a failed bridge call
    /// is retried on the next launch rather than silently dropped. Returns the
    /// IDs newly paused by this pass (for logging).
    @discardableResult
    static func pauseCollisions(
        folders: [(id: String, path: String, paused: Bool)],
        env: Environment
    ) -> [String] {
        let colliding = collidingFolderIDs(
            folders.map { (id: $0.id, path: $0.path) },
            canonicalize: env.canonicalize
        )
        guard !colliding.isEmpty else { return [] }

        let alreadyHandled = env.loadAutoPaused()
        let pausedByID = Dictionary(
            folders.map { ($0.id, $0.paused) },
            uniquingKeysWith: { first, _ in first }
        )

        var newlyPaused: [String] = []
        for id in colliding.sorted() where !alreadyHandled.contains(id) {
            if pausedByID[id] == true {
                // Already paused — record that we have handled it (so a later
                // resume is not undone) without issuing a redundant bridge call.
                env.markAutoPaused(id)
                continue
            }
            if let err = env.setPaused(id) {
                logger.warning("Could not pause colliding folder \(id, privacy: .private): \(err, privacy: .private)")
                continue // leave unrecorded → retried on the next launch
            }
            env.markAutoPaused(id)
            newlyPaused.append(id)
        }
        return newlyPaused
    }

    // MARK: - Live wiring (bridge)

    private struct BridgeFolder: Decodable {
        let id: String
        let path: String
        let paused: Bool
    }

    /// Read the live folder list from the bridge and pause any active path
    /// collision once. Blocks briefly on bridge calls, so run it off the main
    /// actor (alongside `FolderPathReconciler.reconcileLive`).
    static func pauseCollisionsLive() {
        let json = SyncBridgeService.getFoldersJSON()
        guard let data = json.data(using: .utf8),
              let decoded = try? JSONDecoder().decode([BridgeFolder].self, from: data) else {
            return
        }
        // A collision needs ≥2 folders by definition — cheap early-out.
        guard decoded.count >= 2 else { return }

        let folders = decoded.map { (id: $0.id, path: $0.path, paused: $0.paused) }
        let env = Environment(
            canonicalize: FolderPathReconciler.canonical,
            loadAutoPaused: loadAutoPaused,
            markAutoPaused: markAutoPaused,
            setPaused: { id in SyncBridgeService.setFolderPaused(folderID: id, paused: true) }
        )
        let paused = pauseCollisions(folders: folders, env: env)
        if !paused.isEmpty {
            logger.warning("Auto-paused \(paused.count) folder(s) sharing one local path to stop a vault merge (#45)")
        }
    }
}
