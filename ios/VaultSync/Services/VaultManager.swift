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
    /// itself a vault. A single-vault setup is legitimate, but additional
    /// shares can never get a sibling folder inside that scope (the #45
    /// follow-up nesting trap) — point at the container up front instead of
    /// failing at accept time. The UI presents and then clears it.
    private(set) var selectionAdvisory: String?

    func clearSelectionAdvisory() {
        selectionAdvisory = nil
    }

    private static let obsidianBookmarkID = "obsidian-root"

    // MARK: - Access Grant (one-time, from onboarding)

    /// Grant access to the Obsidian root directory via a user-picked URL.
    /// Returns nil on success, error message on failure.
    func grantAccess(url: URL) -> String? {
        guard BookmarkService.startAccessing(url: url) else {
            return L10n.tr("Could not access the selected folder.")
        }

        if let validationError = validateSelectedDirectory(url: url) {
            BookmarkService.stopAccessing(url: url)
            return validationError
        }

        do {
            try BookmarkService.saveBookmark(for: url, identifier: Self.obsidianBookmarkID)
        } catch {
            BookmarkService.stopAccessing(url: url)
            return L10n.fmt("Failed to save access permission: %@", error.localizedDescription)
        }

        obsidianDirectoryURL = url
        isAccessible = true
        needsReconnect = false
        accessIssue = nil
        scanForVaults()
        cleanupLegacyBookmarks()

        selectionAdvisory = nil
        var pickedConfigIsDirectory: ObjCBool = false
        let pickedFolderIsVault = FileManager.default.fileExists(
            atPath: url.appendingPathComponent(".obsidian", isDirectory: true).path,
            isDirectory: &pickedConfigIsDirectory
        ) && pickedConfigIsDirectory.boolValue
        if pickedFolderIsVault {
            selectionAdvisory = L10n.tr("The folder you selected is itself a vault. Syncing this one vault works, but additional vaults cannot get their own folder next to it. If you plan to sync more than one vault, select the folder that contains your vaults instead (\"On My iPhone\" → \"Obsidian\").")
        }

        logger.info("Obsidian directory access granted: \(url.path, privacy: .private) (isVault=\(pickedFolderIsVault))")
        return nil
    }

    // MARK: - Restore on Launch

    /// Restore access from saved bookmark. Safe to call multiple times.
    /// The `isAccessible` guard prevents double-calling `startAccessingSecurityScopedResource()`,
    /// which must be balanced 1:1 with `stopAccessingSecurityScopedResource()`.
    func restoreAccess() {
        if isAccessible { return }

        guard let (url, isStale) = BookmarkService.resolveBookmark(identifier: Self.obsidianBookmarkID) else {
            if BookmarkService.hasBookmark(identifier: Self.obsidianBookmarkID) {
                markReconnectRequired(
                    reason: L10n.tr("VaultSync can no longer resolve the saved Obsidian folder permission. Reconnect the Obsidian directory to continue syncing.")
                )
                logger.warning("Obsidian bookmark exists but could not be resolved")
            } else {
                logger.info("No Obsidian directory bookmark found")
            }
            return
        }

        guard BookmarkService.startAccessing(url: url) else {
            markReconnectRequired(
                reason: L10n.tr("VaultSync cannot access the saved Obsidian folder anymore. Reconnect the Obsidian directory to continue syncing.")
            )
            logger.warning("Cannot access Obsidian directory")
            return
        }

        if let validationError = validateSelectedDirectory(url: url) {
            BookmarkService.stopAccessing(url: url)
            markReconnectRequired(reason: validationError)
            logger.warning("Saved Obsidian directory failed validation")
            return
        }

        if isStale {
            do {
                try BookmarkService.saveBookmark(for: url, identifier: Self.obsidianBookmarkID)
                logger.info("Refreshed stale Obsidian bookmark")
            } catch {
                logger.warning("Could not refresh stale Obsidian bookmark")
            }
        }

        obsidianDirectoryURL = url
        isAccessible = true
        needsReconnect = false
        accessIssue = nil
        scanForVaults()

        logger.info("Obsidian directory restored: \(url.path, privacy: .private)")
    }

    // MARK: - Vault Discovery

    /// Scan the Obsidian directory for vault subdirectories (those containing `.obsidian/`).
    func scanForVaults() {
        guard let url = obsidianDirectoryURL else {
            detectedVaults = []
            return
        }

        let fm = FileManager.default
        guard let contents = try? fm.contentsOfDirectory(
            at: url,
            includingPropertiesForKeys: [.isDirectoryKey],
            options: [.skipsHiddenFiles]
        ) else {
            BookmarkService.stopAccessing(url: url)
            detectedVaults = []
            markReconnectRequired(
                reason: L10n.tr("VaultSync can no longer read your Obsidian directory. Reconnect the folder to restore sync access.")
            )
            return
        }

        detectedVaults = contents.compactMap { itemURL in
            var isDir: ObjCBool = false
            let obsidianDir = itemURL.appendingPathComponent(".obsidian", isDirectory: true)
            if fm.fileExists(atPath: obsidianDir.path, isDirectory: &isDir), isDir.boolValue {
                return itemURL.lastPathComponent
            }
            return nil
        }.sorted()

        logger.info("Detected \(self.detectedVaults.count) vault(s) in Obsidian directory")
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
    func acceptPendingShare(
        folder: SyncthingManager.PendingFolderInfo,
        syncthingManager: SyncthingManager
    ) -> String? {
        let rawName = folder.label.isEmpty ? folder.id : folder.label
        let folderName = Self.sanitizeDirectoryName(rawName)

        guard !folderName.isEmpty else {
            return L10n.fmt("Invalid folder name: '%@'", rawName)
        }

        guard let basePath = obsidianBasePath,
              let baseURL = obsidianDirectoryURL else {
            return L10n.tr("Obsidian directory not accessible.")
        }

        let fm = FileManager.default
        var isDir: ObjCBool = false
        let baseIsVault = fm.fileExists(
            atPath: baseURL.appendingPathComponent(".obsidian", isDirectory: true).path,
            isDirectory: &isDir
        ) && isDir.boolValue

        let nameMatchesBase = baseURL.lastPathComponent
            .compare(folderName, options: .caseInsensitive) == .orderedSame

        // Canonical, lowercased paths already held by *other* folders. The share
        // must never reuse one of these — that is exactly the merge that corrupts
        // both vaults (#45). `folders` is refreshed synchronously after every
        // accept, so sequential auto-accepts each see the prior vault's path.
        let occupied = Set(syncthingManager.folders.map {
            FolderPathReconciler.canonical($0.path).lowercased()
        })

        // A manually chosen target (#52) wins over the label-derived mapping.
        // The record survives folder removal, so remove + re-accept lands the
        // share back where the user put it, never silently at the label default.
        let manualTarget = ManualShareTargetStore.target(forFolder: folder.id)

        let decision = Self.resolveAcceptPath(
            manualTarget: manualTarget,
            rawRoot: basePath,
            baseIsVault: baseIsVault,
            nameMatchesBase: nameMatchesBase,
            folderName: folderName,
            occupiedCanonLower: occupied,
            canonicalize: FolderPathReconciler.canonical
        )

        let path: String
        switch decision {
        case .refused(let message):
            // Either the selected root is itself (or lies inside) another
            // folder's synced directory (#45 follow-up), or the stored manual
            // target has become unsafe. Refuse with guidance; the share stays
            // pending until the user acts.
            logger.error("Refusing share '\(folderName, privacy: .private)' (\(folder.id)): no safe location (manual=\(manualTarget != nil), baseIsVault=\(baseIsVault), occupied=\(occupied.count))")
            return message
        case .path(let resolved):
            path = resolved
        }

        // The local vault keeps the user's chosen name when one exists; the
        // share label on the offering devices is untouched either way (#52).
        let label = manualTarget ?? folderName
        logger.info("Accepting share '\(folderName, privacy: .private)' (\(folder.id)) → path: \(path, privacy: .private) (manual=\(manualTarget != nil), baseIsVault=\(baseIsVault), nameMatchesBase=\(nameMatchesBase), occupied=\(occupied.count))")

        if let err = syncthingManager.acceptPendingFolder(
            folderID: folder.id,
            label: label,
            path: path
        ) {
            return err
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
        logger.info("Auto-accepted pending share: \(folderName, privacy: .private) (\(folder.id))")
        return nil
    }

    /// Decide the local directory an incoming share syncs into, guaranteeing it
    /// never *overlaps* a path already held by a different folder — neither the
    /// same directory (the #45 merge) nor one nested inside it. A nested share
    /// is the same corruption one level down: the outer folder scans the inner
    /// vault's files as its own content and syncs them to its peers, and a peer
    /// deleting that stray copy propagates the deletion into the inner vault
    /// everywhere (#45 follow-up).
    ///
    /// The Obsidian root stays a pure container: each vault gets its own
    /// subdirectory `root/<name>`. The two legacy shortcuts that sync a share
    /// straight into the root — `baseIsVault` (the root itself is a single vault)
    /// and `nameMatchesBase` (avoid `Obsidian/obsidian` double-nesting) — are
    /// honoured ONLY while nothing overlaps the root. A genuine name clash
    /// between two vaults is disambiguated deterministically (`<name> (2)`,
    /// `(3)`, …) rather than silently merged.
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
        baseIsVault: Bool,
        nameMatchesBase: Bool,
        folderName: String,
        occupiedCanonLower: Set<String>,
        canonicalize: (String) -> String
    ) -> String? {
        func overlapsOccupied(_ candidate: String) -> Bool {
            overlaps(candidate, occupiedCanonLower: occupiedCanonLower, canonicalize: canonicalize)
        }

        // Collapse into the root only while nothing overlaps it.
        if (baseIsVault || nameMatchesBase) && !overlapsOccupied(rawRoot) {
            return rawRoot
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
    nonisolated static func resolveAcceptPath(
        manualTarget: String?,
        rawRoot: String,
        baseIsVault: Bool,
        nameMatchesBase: Bool,
        folderName: String,
        occupiedCanonLower: Set<String>,
        canonicalize: (String) -> String
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

        guard let path = resolveSharePath(
            rawRoot: rawRoot,
            baseIsVault: baseIsVault,
            nameMatchesBase: nameMatchesBase,
            folderName: folderName,
            occupiedCanonLower: occupiedCanonLower,
            canonicalize: canonicalize
        ) else {
            return .refused(message: L10n.tr("This share needs its own folder, but the folder selected for VaultSync is itself a vault, so everything inside it belongs to that vault. Re-select the folder that contains your vaults (\"On My iPhone\" → \"Obsidian\") — new vaults then sync side by side instead of inside each other."))
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
    nonisolated static func validateManualTarget(
        rawRoot: String,
        name: String,
        occupiedCanonLower: Set<String>,
        targetEntries: [String]?,
        canonicalize: (String) -> String
    ) -> ShareTargetDecision {
        let folderName = sanitizeDirectoryName(name)
        guard !folderName.isEmpty else {
            return .refused(message: L10n.tr("Enter a folder name."))
        }
        if rootIsCompromised(rawRoot, occupiedCanonLower: occupiedCanonLower, canonicalize: canonicalize) {
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
            name: targetName,
            occupiedCanonLower: occupied,
            targetEntries: entries,
            canonicalize: FolderPathReconciler.canonical
        )

        switch decision {
        case .refused(let message):
            logger.error("Refusing manual target for share (\(folder.id)): \(message, privacy: .private)")
            return message
        case .path(let path):
            if let err = syncthingManager.acceptPendingFolder(
                folderID: folder.id,
                label: sanitized,
                path: path
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
            logger.info("Accepted pending share into manual target (\(folder.id)) → \(path, privacy: .private)")
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

    // MARK: - Legacy Migration

    /// Remove old per-vault bookmarks after migration to obsidian-root.
    private func cleanupLegacyBookmarks() {
        let legacyIDs = BookmarkService.allBookmarkIdentifiers().filter { $0.hasPrefix("vault-") }
        for id in legacyIDs {
            BookmarkService.deleteBookmark(identifier: id)
            logger.info("Cleaned up legacy bookmark: \(id)")
        }
    }

    private func validateSelectedDirectory(url: URL) -> String? {
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
}

/// Outcome of deciding where a share may sync: a safe absolute path, or a
/// refusal whose message names the folder, the reason, and the user's next
/// step. Top-level value type (not nested in the `@MainActor` class) so the
/// pure decision cores stay callable and comparable from any isolation.
enum ShareTargetDecision: Equatable, Sendable {
    case path(String)
    case refused(message: String)
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
