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

        logger.info("Obsidian directory access granted: \(url.path, privacy: .private)")
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

        let path = Self.resolveSharePath(
            rawRoot: basePath,
            baseIsVault: baseIsVault,
            nameMatchesBase: nameMatchesBase,
            folderName: folderName,
            occupiedCanonLower: occupied,
            canonicalize: FolderPathReconciler.canonical
        )
        logger.info("Accepting share '\(folderName, privacy: .private)' (\(folder.id)) → path: \(path, privacy: .private) (baseIsVault=\(baseIsVault), nameMatchesBase=\(nameMatchesBase), occupied=\(occupied.count))")

        if let err = syncthingManager.acceptPendingFolder(
            folderID: folder.id,
            label: folderName,
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
    /// never reuses a path already held by a *different* folder.
    ///
    /// The Obsidian root stays a pure container: each vault gets its own
    /// subdirectory `root/<name>`. The two legacy shortcuts that sync a share
    /// straight into the root — `baseIsVault` (the root itself is a single vault)
    /// and `nameMatchesBase` (avoid `Obsidian/obsidian` double-nesting) — are
    /// honoured ONLY while the root is still free. The instant it is occupied a
    /// distinct vault is given its own subdirectory instead of being merged on
    /// top of the existing one (issue #45). A genuine name clash between two
    /// vaults is disambiguated deterministically (`<name> (2)`, `(3)`, …) rather
    /// than silently merged.
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
    ) -> String {
        func isFree(_ candidate: String) -> Bool {
            !occupiedCanonLower.contains(canonicalize(candidate).lowercased())
        }

        // Collapse into the root only when it is genuinely unoccupied.
        if (baseIsVault || nameMatchesBase) && isFree(rawRoot) {
            return rawRoot
        }

        // Default: the vault's own subdirectory under the root.
        let subfolder = (rawRoot as NSString).appendingPathComponent(folderName)
        if isFree(subfolder) {
            return subfolder
        }

        // Name clash with another vault — append the first free numeric suffix.
        // Bounded by the finite occupied set, so this always terminates.
        var suffix = 2
        while true {
            let candidate = (rawRoot as NSString).appendingPathComponent("\(folderName) (\(suffix))")
            if isFree(candidate) {
                return candidate
            }
            suffix += 1
        }
    }

    /// Replace characters that are invalid in iOS directory names.
    private static func sanitizeDirectoryName(_ name: String) -> String {
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
