import Foundation
import Observation
import os

private let logger = Logger(subsystem: "eu.vaultsync.app", category: "vaults")

/// Manages access to the Obsidian directory on iOS.
///
/// On iOS, Obsidian stores all vaults in a fixed location visible as
/// "On My iPhone/Obsidian/" in the Files app. VaultManager holds a single
/// security-scoped bookmark to this directory and auto-discovers vaults within it.
@Observable @MainActor
final class VaultManager {

    /// URL of the Obsidian root directory (nil if no access granted yet).
    private(set) var obsidianDirectoryURL: URL?

    /// Whether the Obsidian directory is currently accessible.
    private(set) var isAccessible = false

    /// Vault names discovered in the Obsidian directory (subdirectories containing `.obsidian/`).
    private(set) var detectedVaults: [String] = []
    private(set) var accessIssue: SyncUserError?
    private(set) var needsReconnect = false

    /// One-time, non-blocking notice set when the user picks a folder that is
    /// itself a vault. A vault that already syncs into it keeps syncing, but
    /// no share is accepted there: it has no folder of its own to go to (the
    /// #45 follow-up nesting trap, #270) — point at the container up front
    /// instead of failing at accept time. The UI presents and then clears it.
    private(set) var selectionAdvisory: String?

    func clearSelectionAdvisory() {
        selectionAdvisory = nil
    }

    private static let obsidianBookmarkID = "obsidian-root"

    /// Lease ownership is separate from UI accessibility. A visible Boolean
    /// cannot prove which successful start a later stop is allowed to consume.
    @ObservationIgnored private var foregroundLease: SecurityScopedLease?

    /// External effects used by the foreground access path. Production uses
    /// the real bookmark/filesystem APIs; tests inject deterministic counters
    /// and failures so lease ownership can be proven without a file provider.
    struct Environment {
        var acquireAccess: @MainActor (URL, SecurityScopedLeaseOwner) -> SecurityScopedLease?
        var sameResource: @MainActor (URL, URL) -> Bool
        var validateDirectory: @MainActor (URL) -> String?
        var makeBookmarkData: @MainActor (URL) throws -> Data
        var persistBookmarkData: @MainActor (Data) -> Void
        var refreshBookmarkData: @MainActor (Data, Data) -> Bool
        var resolveBookmark: @MainActor () -> BookmarkService.ResolvedBookmark?
        var hasBookmark: @MainActor () -> Bool
        var scanVaults: @MainActor (URL) -> [String]?
        var cleanupLegacyBookmarks: @MainActor () -> Void

        @MainActor
        static var live: Self {
            Self(
                acquireAccess: { url, owner in
                    BookmarkService.acquireAccess(to: url, owner: owner)
                },
                // Conservative equality: a false negative only performs a
                // balanced handoff, while a false positive could reuse access
                // for a different provider resource.
                sameResource: { $0.standardizedFileURL == $1.standardizedFileURL },
                validateDirectory: { VaultManager.liveValidationError(for: $0) },
                makeBookmarkData: { try BookmarkService.makeBookmarkData(for: $0) },
                persistBookmarkData: {
                    BookmarkService.persistBookmarkData(
                        $0,
                        identifier: VaultManager.obsidianBookmarkID
                    )
                },
                refreshBookmarkData: { data, sourceData in
                    BookmarkService.refreshBookmarkData(
                        data,
                        replacing: sourceData,
                        identifier: VaultManager.obsidianBookmarkID
                    )
                },
                resolveBookmark: {
                    BookmarkService.resolveBookmark(identifier: VaultManager.obsidianBookmarkID)
                },
                hasBookmark: {
                    BookmarkService.hasBookmark(identifier: VaultManager.obsidianBookmarkID)
                },
                scanVaults: { VaultManager.vaultSubfolderNames(in: $0) },
                cleanupLegacyBookmarks: {
                    let legacyIDs = BookmarkService.allBookmarkIdentifiers()
                        .filter { $0.hasPrefix("vault-") }
                    for id in legacyIDs {
                        BookmarkService.deleteBookmark(identifier: id)
                        logger.info("Cleaned up legacy bookmark")
                    }
                }
            )
        }
    }

    private let environment: Environment

    init(environment: Environment = .live) {
        self.environment = environment
    }

    // MARK: - Access Grant (one-time, from onboarding)

    /// Grant access to the Obsidian root directory via a user-picked URL.
    /// Returns nil on success, error message on failure.
    func grantAccess(url: URL) -> String? {
        let priorLease = foregroundLease
        let reusesPriorLease = priorLease.map {
            $0.isActive && environment.sameResource($0.url, url)
        } ?? false

        var candidateLease: SecurityScopedLease?
        if !reusesPriorLease {
            guard let acquired = environment.acquireAccess(url, .foreground) else {
                return L10n.tr("Could not access the selected folder.")
            }
            candidateLease = acquired
        }
        // Only a newly acquired, not-yet-adopted candidate is released on
        // failure. Same-URL validation reuses the existing foreground lease.
        defer { candidateLease?.release() }

        if let validationError = environment.validateDirectory(url) {
            return validationError
        }

        guard let names = environment.scanVaults(url) else {
            return L10n.tr("VaultSync can no longer read your Obsidian directory. Reconnect the folder to restore sync access.")
        }

        let advisory = preparedSelectionAdvisory(for: url, detectedVaults: names)

        let bookmarkData: Data
        do {
            bookmarkData = try environment.makeBookmarkData(url)
        } catch {
            return L10n.fmt("Failed to save access permission: %@", error.localizedDescription)
        }

        environment.persistBookmarkData(bookmarkData)
        // No fallible step may occur between the bookmark commit and this
        // synchronous MainActor commit. The old lease stays active until all
        // candidate proof has succeeded and the new state is adopted.
        if let acquired = candidateLease {
            foregroundLease = acquired
            candidateLease = nil
        }
        obsidianDirectoryURL = url
        isAccessible = true
        needsReconnect = false
        accessIssue = nil
        detectedVaults = names
        selectionAdvisory = advisory.message

        if !reusesPriorLease {
            priorLease?.release()
        }
        cleanupLegacyBookmarks()

        logger.info("Obsidian directory access granted (selectedFolderIsVault=\(advisory.pickedFolderIsVault))")
        return nil
    }

    // MARK: - Restore on Launch

    /// Restore access from saved bookmark. Safe to call multiple times: the
    /// owned token, not a UI Boolean, is the no-double-start guard.
    func restoreAccess() {
        if foregroundLease?.isActive == true { return }
        foregroundLease = nil

        guard let resolvedBookmark = environment.resolveBookmark() else {
            if environment.hasBookmark() {
                markReconnectRequired(
                    reason: L10n.tr("VaultSync can no longer resolve the saved Obsidian folder permission. Reconnect the Obsidian directory to continue syncing.")
                )
                logger.warning("Obsidian bookmark exists but could not be resolved")
            } else {
                logger.info("No Obsidian directory bookmark found")
            }
            return
        }
        let url = resolvedBookmark.url

        guard let acquiredLease = environment.acquireAccess(url, .foreground) else {
            markReconnectRequired(
                reason: L10n.tr("VaultSync cannot access the saved Obsidian folder anymore. Reconnect the Obsidian directory to continue syncing.")
            )
            logger.warning("Cannot access Obsidian directory")
            return
        }
        var candidateLease: SecurityScopedLease? = acquiredLease
        defer { candidateLease?.release() }

        if let validationError = environment.validateDirectory(url) {
            markReconnectRequired(reason: validationError)
            logger.warning("Saved Obsidian directory failed validation")
            return
        }

        guard let names = environment.scanVaults(url) else {
            markReconnectRequired(
                reason: L10n.tr("VaultSync can no longer read your Obsidian directory. Reconnect the folder to restore sync access.")
            )
            logger.warning("Saved Obsidian directory scan failed")
            return
        }

        if resolvedBookmark.isStale {
            let bookmarkData: Data
            do {
                bookmarkData = try environment.makeBookmarkData(url)
            } catch {
                logger.warning("Could not refresh stale Obsidian bookmark")
                markReconnectRequired(
                    reason: L10n.fmt("Failed to save access permission: %@", error.localizedDescription)
                )
                return
            }
            guard environment.refreshBookmarkData(
                bookmarkData,
                resolvedBookmark.sourceData
            ) else {
                markReconnectRequired(
                    reason: L10n.tr("VaultSync can no longer resolve the saved Obsidian folder permission. Reconnect the Obsidian directory to continue syncing.")
                )
                logger.warning("Skipped stale Obsidian bookmark refresh after a concurrent permission change")
                return
            }
            logger.info("Refreshed stale Obsidian bookmark")
        }

        foregroundLease = candidateLease
        candidateLease = nil
        obsidianDirectoryURL = url
        isAccessible = true
        needsReconnect = false
        accessIssue = nil
        detectedVaults = names
        selectionAdvisory = nil

        logger.info("Obsidian directory access restored")
    }

    // MARK: - Vault Discovery

    /// Scan the Obsidian directory for vault subdirectories (those containing `.obsidian/`).
    func scanForVaults() {
        guard let url = obsidianDirectoryURL else {
            detectedVaults = []
            return
        }

        guard let names = environment.scanVaults(url) else {
            // This wrapper is the foreground owner. The scan primitive itself
            // is read-only and never releases a lease it did not acquire.
            releaseForegroundLease()
            markReconnectRequired(
                reason: L10n.tr("VaultSync can no longer read your Obsidian directory. Reconnect the folder to restore sync access.")
            )
            return
        }

        detectedVaults = names

        logger.info("Detected \(self.detectedVaults.count) vault(s) in Obsidian directory")
    }

    /// Names of the direct subdirectories of `url` that hold a `.obsidian/`
    /// config — the vaults living inside it. Returns nil when the directory
    /// cannot be read at all (revoked access), as opposed to
    /// readable-but-empty (`[]`) — callers treat the two differently.
    nonisolated static func vaultSubfolderNames(in url: URL) -> [String]? {
        let fm = FileManager.default
        guard let contents = try? fm.contentsOfDirectory(
            at: url,
            includingPropertiesForKeys: [.isDirectoryKey],
            options: [.skipsHiddenFiles]
        ) else {
            return nil
        }
        return contents.compactMap { itemURL in
            var isDir: ObjCBool = false
            let obsidianDir = itemURL.appendingPathComponent(".obsidian", isDirectory: true)
            if fm.fileExists(atPath: obsidianDir.path, isDirectory: &isDir), isDir.boolValue {
                return itemURL.lastPathComponent
            }
            return nil
        }.sorted()
    }

    /// Whether the connected root is itself a single vault — as opposed to a
    /// container holding vaults one level down. A bare `.obsidian/` at the
    /// top level is NOT sufficient: the legacy whole-root sync can leave a
    /// stray vault config in the container (the offering peer's `.obsidian`
    /// synced straight into the root), and Obsidian never nests vaults in
    /// normal use — so the presence of any vault subfolder wins and the root
    /// stays a pure container (decision 014). Misclassifying here is what
    /// collapses a share into the container root, nesting every existing
    /// vault inside that share's synced tree (the #45 family).
    ///
    /// An app's own folder (`isAppDocumentsRoot`) is a container whatever it
    /// holds: Obsidian lists that folder's direct subfolders as vaults and
    /// never the folder itself, so a `.obsidian/` left in it — with every
    /// vault gone — still does not make it one (#270).
    nonisolated static func rootIsItselfVault(
        isAppDocumentsRoot: Bool,
        hasOwnConfig: Bool,
        hasVaultSubfolders: Bool
    ) -> Bool {
        !isAppDocumentsRoot && hasOwnConfig && !hasVaultSubfolders
    }

    /// `rootIsItselfVault` for the selected folder as it is on disk right now.
    /// A fresh read, not the cached `detectedVaults`: every caller decides a
    /// sync target with it.
    nonisolated static func selectedRootIsVault(_ root: URL) -> Bool {
        var isDirectory: ObjCBool = false
        let hasOwnConfig = FileManager.default.fileExists(
            atPath: root.appendingPathComponent(".obsidian", isDirectory: true).path,
            isDirectory: &isDirectory
        ) && isDirectory.boolValue
        return rootIsItselfVault(
            isAppDocumentsRoot: isAppDocumentsRoot(root.path),
            hasOwnConfig: hasOwnConfig,
            hasVaultSubfolders: !(vaultSubfolderNames(in: root) ?? []).isEmpty
        )
    }

    /// Whether `path` is an app's own folder as the folder picker hands it
    /// out: `…/Containers/Data/Application/<UUID>/Documents` for "On My
    /// iPhone" → the app, `…/Mobile Documents/<container>/Documents` for the
    /// app's folder in iCloud Drive. That is the only form in which Obsidian's
    /// folder reaches this app — its name on disk is `Documents`, never
    /// `Obsidian` (read on a device and on the iOS 18 and 26 simulators,
    /// #270). The path cannot tell which app the folder belongs to. Pure, so
    /// every site that classifies the selected folder decides alike.
    nonisolated static func isAppDocumentsRoot(_ path: String) -> Bool {
        let parts = (path as NSString).pathComponents.filter { $0 != "/" }
        guard parts.count >= 3, parts[parts.count - 1] == "Documents" else { return false }
        let owner = parts[parts.count - 2]
        let above = parts[parts.count - 3]
        if above == "Application" {
            return parts.count >= 5
                && parts[parts.count - 4] == "Data"
                && parts[parts.count - 5] == "Containers"
                && UUID(uuidString: owner) != nil
        }
        // `com~apple~CloudDocs` is iCloud Drive itself; its `Documents` is a
        // folder of the user's, not an app's.
        return above == "Mobile Documents" && owner != "com~apple~CloudDocs"
    }

    /// Why a just-picked root deserves a one-time advisory (#95).
    enum SelectionAdvisoryKind: Equatable, Sendable {
        /// The root lives in iCloud Drive: iCloud keeps files as dataless
        /// placeholders the engine cannot read reliably — sync stalls and
        /// conflict churn, the #79 class one level up.
        case iCloudRoot
        /// The root is itself a single vault (the #45 follow-up nesting trap).
        case rootIsVault
    }

    /// Pure classification (unit-testable): the iCloud advisory wins over the
    /// vault-as-root advisory — re-selecting "On My iPhone → Obsidian" fixes
    /// both, and only one advisory alert is ever presented per grant.
    nonisolated static func selectionAdvisoryKind(
        isUbiquitous: Bool,
        pickedFolderIsVault: Bool
    ) -> SelectionAdvisoryKind? {
        if isUbiquitous { return .iCloudRoot }
        if pickedFolderIsVault { return .rootIsVault }
        return nil
    }

    /// Pure path heuristic for iCloud Drive containers: every iCloud Drive
    /// path (com~apple~CloudDocs and app containers like iCloud~md~obsidian)
    /// runs through "…/Mobile Documents/…".
    nonisolated static func pathLooksUbiquitous(_ path: String) -> Bool {
        path.contains("/Mobile Documents/")
    }

    /// Live iCloud check: the resource key where iOS reports it, plus the
    /// path fallback for roots the key misses. Warn-only — never blocks the
    /// grant and never moves anything (safety rule 2).
    nonisolated static func urlLooksUbiquitous(_ url: URL) -> Bool {
        if (try? url.resourceValues(forKeys: [.isUbiquitousItemKey]))?.isUbiquitousItem == true {
            return true
        }
        return pathLooksUbiquitous(url.path)
    }

    // MARK: - Path Helpers

    /// The base path of the Obsidian directory, if accessible.
    var obsidianBasePath: String? {
        obsidianDirectoryURL?.path
    }

    // MARK: - Pending Share Auto-Accept

    /// Accept a pending folder share, syncing it into its own subdirectory under
    /// the Obsidian directory. Each vault is mapped to a distinct local path so
    /// two shares can never be merged into one directory and pushed back to both
    /// peers (issue #45). See `resolveSharePath` for the exact mapping rule.
    ///
    /// `mergeConfirmed` must be false unless the user explicitly confirmed
    /// syncing into an existing directory that already holds content: without
    /// it, a target holding anything beyond `.obsidian` yields
    /// `.needsMergeConfirmation` instead of an accept (#54) — the engine would
    /// otherwise merge two content sets and push the mix to every peer.
    func acceptPendingShare(
        folder: SyncthingManager.PendingFolderInfo,
        syncthingManager: SyncthingManager,
        mergeConfirmed: Bool
    ) -> PendingShareAcceptOutcome {
        let placement: SharePlacement
        switch sharePlacement(
            folderID: folder.id,
            label: folder.label,
            syncthingManager: syncthingManager,
            mergeConfirmed: mergeConfirmed
        ) {
        case .refused(let message):
            return .refused(message: message)
        case .decided(let decided):
            placement = decided
        }
        let decision = placement.decision
        let folderName = placement.folderName
        let manualTarget = placement.manualTarget
        let basePath = placement.basePath
        let rootIsVault = placement.rootIsVault
        let occupied = placement.occupied

        let path: String
        switch decision {
        case .refused(let message):
            // Either the selected root is itself (or lies inside) another
            // folder's synced directory (#45 follow-up), or the stored manual
            // target has become unsafe. Refuse with guidance; the share stays
            // pending until the user acts.
            logger.error("Refusing share because no safe location exists (manual=\(manualTarget != nil), rootIsVault=\(rootIsVault), occupiedCount=\(occupied.count))")
            return .refused(message: message)
        case .requiresMergeConfirmation(_, let targetName):
            // The label-derived target already holds content the engine would
            // merge with the share and push to every peer (#54). Only an
            // explicit, informed accept may do that — hand the decision back.
            logger.info("Share needs merge confirmation")
            return .needsMergeConfirmation(targetName: targetName)
        case .path(let resolved):
            path = resolved
        }

        // The local vault keeps the user's chosen name when one exists; the
        // share label on the offering devices is untouched either way (#52).
        let label = manualTarget ?? folderName
        logger.info("Accepting share (manual=\(manualTarget != nil), rootIsVault=\(rootIsVault), occupiedCount=\(occupied.count), mergeConfirmed=\(mergeConfirmed))")

        // A recorded manual target is recorded consent (#52/006); otherwise
        // only the user's fresh confirmation may open the Go hard floor.
        if let err = syncthingManager.acceptPendingFolder(
            folderID: folder.id,
            label: label,
            path: path,
            root: basePath,
            allowNonEmpty: manualTarget != nil || mergeConfirmed
        ) {
            return .refused(message: err)
        }

        // Record where this folder lives relative to the Obsidian root so its
        // absolute path can be re-derived after an iOS container change (#25).
        // Derive from the resolved path, not the share name, so the whole-dir
        // and subdirectory cases are both captured correctly. If the resolved
        // path unexpectedly isn't under the root, leave the mapping unset and
        // let the next launch's reconcile backfill it, rather than mislabeling a
        // subdirectory as the whole directory.
        if let rel = FolderPathReconciler.relativeIfUnder(
            FolderPathReconciler.canonical(path),
            root: FolderPathReconciler.canonical(basePath)
        ) {
            FolderPathReconciler.setRel(rel, forFolder: folder.id)
        }

        scanForVaults()
        logger.info("Auto-accepted pending share")
        return .accepted
    }

    /// Everything an accept decides before it acts: the target decision and
    /// the inputs it was made from (kept for the accept's log line).
    private struct SharePlacement {
        let decision: ShareTargetDecision
        let folderName: String
        let manualTarget: String?
        let basePath: String
        let rootIsVault: Bool
        let occupied: Set<String>
    }

    private enum SharePlacementResult {
        case refused(String)
        case decided(SharePlacement)
    }

    /// The placement decision for a share — computed in exactly one place, so
    /// the accept and the read-only preview (`previewShareDestination`) can
    /// never disagree.
    private func sharePlacement(
        folderID: String,
        label: String,
        syncthingManager: SyncthingManager,
        mergeConfirmed: Bool
    ) -> SharePlacementResult {
        let rawName = label.isEmpty ? folderID : label
        let folderName = Self.sanitizeDirectoryName(rawName)

        // A manually chosen target (#52) wins over the label-derived mapping.
        // The record survives folder removal, so remove + re-accept lands the
        // share back where the user put it, never silently at the label default.
        let manualTarget = ManualShareTargetStore.target(forFolder: folderID)

        // The label has to name a folder only when the target is derived
        // from it: a recorded target keeps its place whatever the offering
        // device calls the share now.
        guard manualTarget != nil || Self.isUsableDirectoryName(folderName) else {
            return .refused(L10n.fmt("Invalid folder name: '%@'", rawName))
        }

        guard let basePath = obsidianBasePath,
              let baseURL = obsidianDirectoryURL else {
            return .refused(L10n.tr("Obsidian directory not accessible."))
        }

        let rootIsVault = Self.selectedRootIsVault(baseURL)

        // Canonical, lowercased paths already held by *other* folders. The share
        // must never reuse one of these — that is exactly the merge that corrupts
        // both vaults (#45). `folders` is refreshed synchronously after every
        // accept, so sequential auto-accepts each see the prior vault's path.
        let occupied = Set(syncthingManager.folders.map {
            FolderPathReconciler.canonical($0.path).lowercased()
        })

        let decision = Self.resolveAcceptPath(
            manualTarget: manualTarget,
            rawRoot: basePath,
            rootIsVault: rootIsVault,
            folderName: folderName,
            occupiedCanonLower: occupied,
            mergeConfirmed: mergeConfirmed,
            canonicalize: FolderPathReconciler.canonical,
            // Full listing, hidden entries included — a `.stfolder` marker or
            // hidden leftover is exactly what must disqualify a target (#54).
            // nil covers both "does not exist" and "unreadable": the Go hard
            // floor re-checks on its own and refuses what it cannot verify.
            listingFor: { try? FileManager.default.contentsOfDirectory(atPath: $0) }
        )
        return .decided(SharePlacement(
            decision: decision,
            folderName: folderName,
            manualTarget: manualTarget,
            basePath: basePath,
            rootIsVault: rootIsVault,
            occupied: occupied
        ))
    }

    /// Where a share with this folder ID and label would land if it arrived
    /// now — the accept's own decision, without accepting anything (#174: the
    /// Add Hub sheet names the destination before the Hub shares the vault).
    /// Nil while the Obsidian folder is not connected. Advisory only: the
    /// accept decides again, on settled paths, when the share arrives.
    func previewShareDestination(
        folderID: String,
        label: String,
        syncthingManager: SyncthingManager
    ) -> ShareTargetDecision? {
        guard isAccessible, obsidianBasePath != nil else { return nil }
        switch sharePlacement(folderID: folderID, label: label, syncthingManager: syncthingManager, mergeConfirmed: false) {
        case .refused(let message):
            return .refused(message: message)
        case .decided(let placement):
            return placement.decision
        }
    }

    /// Decide the local directory an incoming share syncs into, guaranteeing it
    /// never *overlaps* a path already held by a different folder — neither the
    /// same directory (the #45 merge) nor one nested inside it. A nested share
    /// is the same corruption one level down: the outer folder scans the inner
    /// vault's files as its own content and syncs them to its peers, and a peer
    /// deleting that stray copy propagates the deletion into the inner vault
    /// everywhere (#45 follow-up).
    ///
    /// The Obsidian root is a pure container: each vault gets its own
    /// subdirectory `root/<name>`, and no share ever syncs into the root
    /// itself (#270, decision 058). Obsidian lists the direct subfolders of
    /// its folder as vaults and nothing else, so a share collapsed into the
    /// root arrives in full and never appears there. The two shortcuts that
    /// did collapse are gone: one fired when the label matched the root's
    /// name on disk — which for Obsidian's folder is `Documents` — the other
    /// when the root held a `.obsidian/` and no vault subfolder. A genuine
    /// name clash between two vaults is disambiguated deterministically
    /// (`<name> (2)`, `(3)`, …) rather than silently merged.
    ///
    /// Returns nil when NO safe location exists: once the root itself is (or
    /// lies inside) another folder's directory, every possible subdirectory
    /// would sit inside that folder's synced tree — exactly the vault-as-root
    /// setup from the #45 follow-up report. The caller refuses the share with
    /// guidance to re-select the container folder instead of silently nesting.
    ///
    /// Pure (no filesystem, no bridge) so the collision logic is exhaustively
    /// unit-testable; `canonicalize` is injected (production:
    /// `FolderPathReconciler.canonical`). Membership is tested case-insensitively
    /// because the iOS data volume is case-folding APFS.
    nonisolated static func resolveSharePath(
        rawRoot: String,
        folderName: String,
        occupiedCanonLower: Set<String>,
        canonicalize: (String) -> String
    ) -> String? {
        func overlapsOccupied(_ candidate: String) -> Bool {
            overlaps(candidate, occupiedCanonLower: occupiedCanonLower, canonicalize: canonicalize)
        }

        // Once the root is — or lies inside — an occupied directory, every
        // subdirectory would nest this share inside an existing folder's synced
        // tree. There is no safe location under this root at all.
        if rootIsCompromised(rawRoot, occupiedCanonLower: occupiedCanonLower, canonicalize: canonicalize) {
            return nil
        }

        // Default: the vault's own subdirectory under the root.
        let subfolder = (rawRoot as NSString).appendingPathComponent(folderName)
        if !overlapsOccupied(subfolder) {
            return subfolder
        }

        // Name clash with another vault — append the first free numeric suffix.
        // Terminates: the root is outside every occupied directory here, so
        // each occupied path can block at most finitely many candidates.
        var suffix = 2
        while true {
            let candidate = (rawRoot as NSString).appendingPathComponent("\(folderName) (\(suffix))")
            if !overlapsOccupied(candidate) {
                return candidate
            }
            suffix += 1
        }
    }

    /// Whether the candidate is one of the occupied directories, lies inside
    /// one, or holds one — any of these mixes two folders' contents (#45).
    /// Shared by the label-derived mapping (`resolveSharePath`) and the manual
    /// target validation (#52) so the two accept paths can never disagree on
    /// what counts as an overlap. Case-insensitive: case-folding APFS.
    nonisolated private static func overlaps(
        _ candidate: String,
        occupiedCanonLower: Set<String>,
        canonicalize: (String) -> String
    ) -> Bool {
        let c = canonicalize(candidate).lowercased()
        return occupiedCanonLower.contains { occ in
            occ == c || occ.hasPrefix(c + "/") || c.hasPrefix(occ + "/")
        }
    }

    /// Whether the root itself is — or lies inside — an occupied directory.
    /// Then every subdirectory would nest inside an existing folder's synced
    /// tree, so no safe location exists under this root at all (#45 follow-up).
    nonisolated private static func rootIsCompromised(
        _ rawRoot: String,
        occupiedCanonLower: Set<String>,
        canonicalize: (String) -> String
    ) -> Bool {
        let rootCanon = canonicalize(rawRoot).lowercased()
        return occupiedCanonLower.contains { occ in
            occ == rootCanon || rootCanon.hasPrefix(occ + "/")
        }
    }

    // MARK: - Manual Share Target (#52)

    /// Decide where a pending share syncs: a manually chosen target recorded
    /// earlier (#52) wins over the label-derived mapping. An override that has
    /// become unsafe is refused with guidance — never silently replaced by the
    /// share-label default, because the user chose that location deliberately
    /// and accepting somewhere else would split the vault across two
    /// directories. The override path is NOT required to be empty: after
    /// remove + re-accept it legitimately holds this same share's earlier
    /// content (exactly like the label-default path on a plain re-accept).
    ///
    /// The label-derived target, by contrast, carries no recorded consent: if
    /// the resolved directory (label default or numeric-suffix
    /// disambiguation alike) already holds anything beyond `.obsidian`,
    /// accepting would merge two content sets and push the mix to every peer
    /// (#54). Unless `mergeConfirmed` says the user explicitly approved that,
    /// the decision is handed back as `.requiresMergeConfirmation` — never
    /// silently diverted to a different location (006's rejected fallback) and
    /// never silently merged. `listingFor` returns a directory's full listing
    /// (hidden entries included) or nil when it does not exist — injected so
    /// the rule is unit-testable without the filesystem.
    ///
    /// `rootIsVault` — the selected folder is itself a vault
    /// (`rootIsItselfVault`): the label-derived mapping then has no place for
    /// the share. Syncing into the root would put it where the user's vault
    /// is; a subfolder would bury it inside that vault, where Obsidian shows
    /// a folder, not a vault. It is refused with the way out (#270).
    nonisolated static func resolveAcceptPath(
        manualTarget: String?,
        rawRoot: String,
        rootIsVault: Bool,
        folderName: String,
        occupiedCanonLower: Set<String>,
        mergeConfirmed: Bool,
        canonicalize: (String) -> String,
        listingFor: (String) -> [String]?
    ) -> ShareTargetDecision {
        if let target = manualTarget {
            let candidate = (rawRoot as NSString).appendingPathComponent(target)
            if rootIsCompromised(rawRoot, occupiedCanonLower: occupiedCanonLower, canonicalize: canonicalize)
                || overlaps(candidate, occupiedCanonLower: occupiedCanonLower, canonicalize: canonicalize) {
                return .refused(message: L10n.fmt(
                    "This share is set to sync into \"%@\", but that location now overlaps a folder another vault already syncs. Tap \"Choose Vault…\" on the share to pick a new location, or remove the vault that syncs there, then accept the share again.",
                    target
                ))
            }
            return .path(candidate)
        }

        guard !rootIsVault, let path = resolveSharePath(
            rawRoot: rawRoot,
            folderName: folderName,
            occupiedCanonLower: occupiedCanonLower,
            canonicalize: canonicalize
        ) else {
            return .refused(message: L10n.tr("This share needs its own folder, but the folder selected for VaultSync is itself a vault, so everything inside it belongs to that vault. Re-select the folder that contains your vaults (\"On My iPhone\" → \"Obsidian\") — new vaults then sync side by side instead of inside each other."))
        }

        if !mergeConfirmed,
           let entries = listingFor(path),
           !isEmptyVaultListing(entries) {
            return .requiresMergeConfirmation(
                path: path,
                targetName: (path as NSString).lastPathComponent
            )
        }
        return .path(path)
    }

    /// Validate a manually chosen share target (issue #52): `name` — a picked
    /// existing vault or a new-folder name — becomes `root/<sanitized name>`.
    /// Refusal messages name the folder, the reason, and the user's next step.
    /// Pure: the caller supplies the target's directory listing
    /// (`targetEntries`; nil when the directory does not exist yet, hidden
    /// entries included otherwise) so every rule is unit-testable without the
    /// filesystem.
    ///
    /// Only *empty* vaults are eligible as existing targets: linking a share
    /// to a folder that already holds content would merge two content sets,
    /// which needs its own safety design (out of scope for #52).
    ///
    /// `rootIsVault`: a new choice under a selected folder that is itself a
    /// vault is refused like a label-derived target — `root/<name>` would lie
    /// inside that vault, where Obsidian shows a folder, not a vault (#270).
    /// A target recorded earlier is not validated here and keeps its place
    /// (`resolveAcceptPath`).
    nonisolated static func validateManualTarget(
        rawRoot: String,
        rootIsVault: Bool,
        name: String,
        occupiedCanonLower: Set<String>,
        targetEntries: [String]?,
        canonicalize: (String) -> String
    ) -> ShareTargetDecision {
        let folderName = sanitizeDirectoryName(name)
        guard isUsableDirectoryName(folderName) else {
            return .refused(message: L10n.tr("Enter a folder name."))
        }
        if rootIsVault
            || rootIsCompromised(rawRoot, occupiedCanonLower: occupiedCanonLower, canonicalize: canonicalize) {
            return .refused(message: L10n.tr("This share needs its own folder, but the folder selected for VaultSync is itself a vault, so everything inside it belongs to that vault. Re-select the folder that contains your vaults (\"On My iPhone\" → \"Obsidian\") — new vaults then sync side by side instead of inside each other."))
        }
        let candidate = (rawRoot as NSString).appendingPathComponent(folderName)
        if overlaps(candidate, occupiedCanonLower: occupiedCanonLower, canonicalize: canonicalize) {
            return .refused(message: L10n.fmt(
                "The target folder \"%@\" is already synced by another vault — the same folder, or one above or inside it. Choose a different folder, or remove the vault that syncs there first.",
                folderName
            ))
        }
        if let entries = targetEntries, !isEmptyVaultListing(entries) {
            return .refused(message: L10n.fmt(
                "The folder \"%@\" already contains files. A share can only be linked to an empty vault — choose an empty vault, or enter a new folder name.",
                folderName
            ))
        }
        return .path(candidate)
    }

    /// True when a directory listing qualifies as an *empty vault*: nothing
    /// inside except (at most) Obsidian's `.obsidian` configuration folder.
    /// Anything else — notes, a `.stfolder` sync marker, hidden leftovers —
    /// disqualifies the directory, because linking a share there would merge
    /// two content sets. Case-insensitive: case-folding APFS.
    nonisolated static func isEmptyVaultListing(_ entries: [String]) -> Bool {
        entries.allSatisfy { $0.compare(".obsidian", options: .caseInsensitive) == .orderedSame }
    }

    /// Accept a pending share into a target the user picked (issue #52): an
    /// existing empty vault under the Obsidian directory, or a new folder with
    /// a custom name. Records the choice so any later re-accept (after the
    /// user removes the vault) returns to this location instead of the
    /// share-label default. Returns nil on success, error message on failure.
    func acceptPendingShare(
        folder: SyncthingManager.PendingFolderInfo,
        intoTargetNamed targetName: String,
        syncthingManager: SyncthingManager
    ) -> String? {
        guard let basePath = obsidianBasePath,
              let baseURL = obsidianDirectoryURL else {
            return L10n.tr("Obsidian directory not accessible.")
        }

        let occupied = Set(syncthingManager.folders.map {
            FolderPathReconciler.canonical($0.path).lowercased()
        })

        // Full listing, hidden entries included: a `.stfolder` marker or a
        // hidden leftover must disqualify a target, so an enumeration that
        // skips hidden files would hide exactly what matters.
        let sanitized = Self.sanitizeDirectoryName(targetName)
        let targetURL = baseURL.appendingPathComponent(sanitized, isDirectory: true)
        let entries = try? FileManager.default.contentsOfDirectory(atPath: targetURL.path)

        let decision = Self.validateManualTarget(
            rawRoot: basePath,
            rootIsVault: Self.selectedRootIsVault(baseURL),
            name: targetName,
            occupiedCanonLower: occupied,
            targetEntries: entries,
            canonicalize: FolderPathReconciler.canonical
        )

        switch decision {
        case .refused(let message):
            logger.error("Refusing unsafe manual share target")
            return message
        case .requiresMergeConfirmation:
            // Unreachable from validateManualTarget — #52 targets must be
            // empty. Refuse defensively rather than trust the impossible.
            return L10n.fmt(
                "The folder \"%@\" already contains files. A share can only be linked to an empty vault — choose an empty vault, or enter a new folder name.",
                sanitized
            )
        case .path(let path):
            // Validated empty above — the Go hard floor re-checks (#54).
            if let err = syncthingManager.acceptPendingFolder(
                folderID: folder.id,
                label: sanitized,
                path: path,
                root: basePath,
                allowNonEmpty: false
            ) {
                return err
            }
            // Only a successful accept records the choice — a failed one must
            // not pin future auto-accepts to a target that never materialized.
            ManualShareTargetStore.setTarget(sanitized, forFolder: folder.id)
            if let rel = FolderPathReconciler.relativeIfUnder(
                FolderPathReconciler.canonical(path),
                root: FolderPathReconciler.canonical(basePath)
            ) {
                FolderPathReconciler.setRel(rel, forFolder: folder.id)
            }
            scanForVaults()
            logger.info("Accepted pending share into manual target")
            return nil
        }
    }

    /// Names of existing subdirectories under the Obsidian root that qualify
    /// as manual share targets (#52): empty vaults (nothing inside except at
    /// most `.obsidian`) whose path does not overlap any configured folder's.
    /// The picker lists these; everything else is reachable only as a newly
    /// created folder.
    func eligibleShareTargets(syncthingManager: SyncthingManager) -> [String] {
        guard let baseURL = obsidianDirectoryURL,
              let basePath = obsidianBasePath else {
            return []
        }
        let occupied = Set(syncthingManager.folders.map {
            FolderPathReconciler.canonical($0.path).lowercased()
        })
        let fm = FileManager.default
        guard let contents = try? fm.contentsOfDirectory(
            at: baseURL,
            includingPropertiesForKeys: [.isDirectoryKey],
            options: [.skipsHiddenFiles]
        ) else {
            return []
        }
        let rootIsVault = Self.selectedRootIsVault(baseURL)
        return contents.compactMap { itemURL -> String? in
            var isDir: ObjCBool = false
            guard fm.fileExists(atPath: itemURL.path, isDirectory: &isDir), isDir.boolValue else {
                return nil
            }
            let name = itemURL.lastPathComponent
            // A name that changes under sanitization would be validated (and
            // created) as a different directory than the one listed — skip it.
            guard Self.sanitizeDirectoryName(name) == name else { return nil }
            guard let entries = try? fm.contentsOfDirectory(atPath: itemURL.path) else { return nil }
            guard case .path = Self.validateManualTarget(
                rawRoot: basePath,
                rootIsVault: rootIsVault,
                name: name,
                occupiedCanonLower: occupied,
                targetEntries: entries,
                canonicalize: FolderPathReconciler.canonical
            ) else {
                return nil
            }
            return name
        }.sorted()
    }

    /// Replace characters that are invalid in iOS directory names.
    nonisolated static func sanitizeDirectoryName(_ name: String) -> String {
        let invalid = CharacterSet(charactersIn: "/:\0")
        return name.components(separatedBy: invalid)
            .joined(separator: "-")
            .trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// Whether a sanitized name stands for a folder of its own. `.` is the
    /// selected folder itself and `..` the one above it: a share labelled so
    /// would sync into the root or out of it (#270). The Go floor refuses
    /// both as well.
    nonisolated static func isUsableDirectoryName(_ name: String) -> Bool {
        !name.isEmpty && name != "." && name != ".."
    }

    // MARK: - Legacy Migration

    private func preparedSelectionAdvisory(
        for url: URL,
        detectedVaults: [String]
    ) -> (message: String?, pickedFolderIsVault: Bool) {
        var pickedConfigIsDirectory: ObjCBool = false
        let pickedFolderHasOwnConfig = FileManager.default.fileExists(
            atPath: url.appendingPathComponent(".obsidian", isDirectory: true).path,
            isDirectory: &pickedConfigIsDirectory
        ) && pickedConfigIsDirectory.boolValue
        let pickedFolderIsVault = Self.rootIsItselfVault(
            isAppDocumentsRoot: Self.isAppDocumentsRoot(url.path),
            hasOwnConfig: pickedFolderHasOwnConfig,
            hasVaultSubfolders: !detectedVaults.isEmpty
        )

        let message: String?
        switch Self.selectionAdvisoryKind(
            isUbiquitous: Self.urlLooksUbiquitous(url),
            pickedFolderIsVault: pickedFolderIsVault
        ) {
        case .iCloudRoot:
            message = L10n.tr("The folder you selected is stored in iCloud Drive. iCloud can keep files as placeholders that are not fully downloaded on this iPhone, which can stall syncing and create conflicts. For reliable syncing, use your vaults under \"On My iPhone\" → \"Obsidian\" and select that folder instead.")
        case .rootIsVault:
            message = L10n.tr("The folder you selected is itself a vault. A vault that already syncs here keeps syncing, but a new share needs a folder of its own and cannot be accepted here. Select the folder that contains your vaults instead (\"On My iPhone\" → \"Obsidian\").")
        case nil:
            message = nil
        }
        return (message, pickedFolderIsVault)
    }

    /// Remove old per-vault bookmarks after migration to obsidian-root.
    private func cleanupLegacyBookmarks() {
        environment.cleanupLegacyBookmarks()
    }

    private func releaseForegroundLease() {
        let lease = foregroundLease
        foregroundLease = nil
        lease?.release()
    }

    nonisolated private static func liveValidationError(for url: URL) -> String? {
        let fm = FileManager.default
        var isDirectory: ObjCBool = false
        guard fm.fileExists(atPath: url.path, isDirectory: &isDirectory), isDirectory.boolValue else {
            return L10n.tr("Please select a folder. In the picker choose \"On My iPhone\" → \"Obsidian\".")
        }

        let isNamedObsidian = url.lastPathComponent.compare("Obsidian", options: .caseInsensitive) == .orderedSame
        var selectedConfigIsDirectory: ObjCBool = false
        let selectedFolderContainsVaultConfig = fm.fileExists(
            atPath: url.appendingPathComponent(".obsidian", isDirectory: true).path,
            isDirectory: &selectedConfigIsDirectory
        ) && selectedConfigIsDirectory.boolValue

        guard let contents = try? fm.contentsOfDirectory(
            at: url,
            includingPropertiesForKeys: [.isDirectoryKey],
            options: [.skipsHiddenFiles]
        ) else {
            return L10n.tr("VaultSync cannot read this folder. Reopen the picker and select \"On My iPhone\" → \"Obsidian\".")
        }

        let containsVaultSubfolders = contents.contains { itemURL in
            var obsidianIsDirectory: ObjCBool = false
            let configPath = itemURL.appendingPathComponent(".obsidian", isDirectory: true).path
            return fm.fileExists(atPath: configPath, isDirectory: &obsidianIsDirectory) && obsidianIsDirectory.boolValue
        }

        if isNamedObsidian || selectedFolderContainsVaultConfig || containsVaultSubfolders {
            return nil
        }

        return L10n.tr("This folder does not look like your Obsidian directory yet. In the picker choose \"On My iPhone\" → \"Obsidian\".")
    }

    private func markReconnectRequired(reason: String) {
        obsidianDirectoryURL = nil
        isAccessible = false
        detectedVaults = []
        needsReconnect = true
        accessIssue = SyncUserError(
            category: .fileAccess,
            title: L10n.tr("Reconnect Obsidian Directory"),
            message: reason,
            remediation: L10n.tr("Open the folder picker again and select your Obsidian directory."),
            technicalDetails: reason
        )
    }

    // MARK: - Test hooks

    #if DEBUG
    /// LAB: seed the published access state for a design-preview fixture run
    /// (#187), where no folder picker can grant real access on a simulator.
    /// Sets only the observable values the UI reads — no bookmark, lease, or
    /// filesystem access happens. Compiled out of release builds.
    func _testSetAccess(accessible: Bool, detectedVaults: [String], obsidianDirectoryURL: URL?) {
        self.obsidianDirectoryURL = obsidianDirectoryURL
        self.detectedVaults = detectedVaults
        isAccessible = accessible
        needsReconnect = false
        accessIssue = nil
    }
    #endif
}

/// Outcome of deciding where a share may sync: a safe absolute path, a
/// refusal whose message names the folder, the reason, and the user's next
/// step — or a target that exists with content, where only the user may
/// decide whether merging is intended (#54). Top-level value type (not nested
/// in the `@MainActor` class) so the pure decision cores stay callable and
/// comparable from any isolation.
enum ShareTargetDecision: Equatable, Sendable {
    case path(String)
    case refused(message: String)
    case requiresMergeConfirmation(path: String, targetName: String)
}

/// Outcome of an accept attempt: accepted, refused with a user-facing
/// message, or blocked on the user's explicit merge decision (#54) — the
/// caller presents the confirmation and re-runs the accept with
/// `mergeConfirmed: true`, which re-validates everything at confirm time.
enum PendingShareAcceptOutcome: Equatable, Sendable {
    case accepted
    case refused(message: String)
    case needsMergeConfirmation(targetName: String)
}

/// Sidecar: the local folder name the user manually picked for a share
/// (issue #52), keyed by Syncthing folder ID. Deliberately SURVIVES folder
/// removal — removing a vault and accepting the returning share must land it
/// back in the chosen location, never silently in the share-label default —
/// so `SyncthingManager.removeFolder` must not clear it (unlike the
/// reconciler's rel sidecar, which tracks where a *configured* folder lives).
/// Entries are never pruned: the map stays tiny (one name per manually placed
/// share, ever) and a stale entry is harmless — every accept re-validates it
/// against the overlap guards. Same serialized-queue UserDefaults pattern as
/// `FolderPathReconciler`.
enum ManualShareTargetStore {
    private static let storeKey = "vaultsync.manualShareTargets"
    private static let queue = DispatchQueue(label: "eu.vaultsync.manualsharetargets.sidecar")

    static func target(forFolder folderID: String) -> String? {
        queue.sync {
            (UserDefaults.standard.dictionary(forKey: storeKey) as? [String: String])?[folderID]
        }
    }

    /// Record the chosen folder name after a successful manual accept.
    /// Atomic read-modify-write.
    static func setTarget(_ name: String, forFolder folderID: String) {
        queue.sync {
            var map = UserDefaults.standard.dictionary(forKey: storeKey) as? [String: String] ?? [:]
            guard map[folderID] != name else { return }
            map[folderID] = name
            UserDefaults.standard.set(map, forKey: storeKey)
        }
    }
}
