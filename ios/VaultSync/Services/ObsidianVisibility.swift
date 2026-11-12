import Foundation

/// Finds what already syncs to a place Obsidian does not list as a vault
/// (#270, decision 060).
///
/// Obsidian shows the direct subfolders of its own folder as vaults and
/// nothing else. Two setups an earlier version allowed end up outside that:
///
/// - **A vault in the selected folder itself.** Two shortcuts synced a share
///   straight into the selected folder (decision 058 removed them). When that
///   folder is an app's own folder, the vault's notes lie where Obsidian
///   lists vaults, not inside one.
/// - **A selected folder that is not an app's own folder.** A folder the
///   user made and named "Obsidian" passed the earlier folder check
///   (decision 059), and so did a folder inside Obsidian's folder. Whatever
///   syncs there, Obsidian does not list it as a vault.
///
/// Both keep syncing, and nothing here changes that: no folder is paused,
/// moved or re-pointed (decisions 002, 003). The finding only becomes a
/// notice with the manual way out, and the user may keep the setup — then
/// the notice stays away for that folder.
///
/// Mirrors `PathCollisionGuard`: a pure, injectable core for exhaustive unit
/// tests, a thin live wrapper, and a `UserDefaults` sidecar behind a
/// serialized queue.
enum ObsidianVisibility {

    enum Kind: String, Equatable, Sendable {
        /// A folder syncs into the selected folder itself, the selected
        /// folder is an app's own folder, and by what it holds it is a
        /// single vault.
        case vaultInSelectedFolderItself
        /// The selected folder is not an app's own folder: it lies outside
        /// every app's folder, or inside one without being the single vault
        /// that syncs there.
        case selectedFolderNotAnAppFolder
    }

    struct Finding: Equatable, Sendable {
        let kind: Kind
        /// The folders the finding is about, sorted. Empty for
        /// `.selectedFolderNotAnAppFolder` while nothing syncs there: the
        /// folder is still the wrong place for the next share, and only
        /// then may another folder be selected without cutting a syncing
        /// vault off.
        let folderIDs: [String]
        /// What "keep it this way" records for this finding.
        let tokens: [String]
    }

    /// What a directory holds, as far as the classification needs it.
    struct DirectoryContent: Equatable, Sendable {
        /// A `.obsidian/` directly inside it.
        var hasOwnConfig: Bool
        /// At least one direct subfolder with a `.obsidian/` inside.
        var hasVaultSubfolders: Bool

        /// A single vault by what it holds — the rule of decision 014
        /// without the exemption for app folders.
        var isSingleVault: Bool {
            VaultManager.rootIsItselfVault(
                isAppDocumentsRoot: false,
                hasOwnConfig: hasOwnConfig,
                hasVaultSubfolders: hasVaultSubfolders
            )
        }
    }

    // MARK: - Classification (pure)

    struct Environment {
        var canonicalize: (String) -> String
        /// What the directory at this path holds, or nil when it cannot be
        /// read. Called at most once, and only for a folder that syncs into
        /// an app's own folder itself.
        var content: (String) -> DirectoryContent?
        /// Everything the sidecar holds: what the user keeps, and which
        /// vaults were reported before.
        var record: () -> Set<String>
    }

    /// The findings for the selected folder and the configured folders.
    ///
    /// The selected folder is classified by its path alone, exactly as the
    /// folder check does (`VaultManager.selectedFolderPlace`), so the notice
    /// and the check can never disagree about what an app's own folder is.
    ///
    /// **An app's own folder.** A folder that syncs into it itself is
    /// reported when the folder is a single vault by what it holds. A
    /// whole-directory sync whose shared folder contains the vaults looks
    /// the same by path and is the setup Obsidian shows correctly; it must
    /// not be reported, and what cannot be read is not reported either.
    /// Once reported, a vault stays reported until it is removed or kept:
    /// the content changes as soon as the user opens one of its folders in
    /// Obsidian, and a notice that left by itself would read as "fixed".
    ///
    /// **Any other folder** is reported, with one exception: a single vault
    /// picked as the selected folder — a direct subfolder of an app's
    /// folder with a vault syncing into it — is where Obsidian lists it.
    ///
    /// Keeping a selected folder silences its notice only while vaults sync
    /// there. With nothing syncing there is nothing to keep, the next share
    /// would land in the wrong place, and the notice is the one place that
    /// offers another folder.
    static func findings(
        selectedRoot: String?,
        folders: [(id: String, path: String)],
        env: Environment
    ) -> [Finding] {
        guard let rawRoot = selectedRoot else { return [] }
        let record = env.record()
        // Lowercased like every path comparison in the collision guards:
        // the iOS data volume is case-folding APFS.
        let rootKey = env.canonicalize(rawRoot).lowercased()
        let folderKeys = folders.map { (id: $0.id, key: env.canonicalize($0.path).lowercased()) }
        let atRoot = folderKeys.filter { $0.key == rootKey }.map(\.id).sorted()
        let below = folderKeys.filter { $0.key.hasPrefix(rootKey + "/") }.map(\.id).sorted()

        func notAnAppFolder(_ folderIDs: [String]) -> [Finding] {
            let token = rootToken(canonicalLowercasedRoot: rootKey)
            if !folderIDs.isEmpty, record.contains(token) { return [] }
            return [Finding(kind: .selectedFolderNotAnAppFolder, folderIDs: folderIDs, tokens: [token])]
        }

        switch VaultManager.selectedFolderPlace(rawRoot) {
        case .appFolder:
            let open = atRoot.filter { !record.contains(folderToken($0)) }
            guard !open.isEmpty else { return [] }
            var reported = open.filter { record.contains(seenToken($0)) }
            if reported.count < open.count, env.content(rawRoot)?.isSingleVault == true {
                reported = open
            }
            guard !reported.isEmpty else { return [] }
            return [Finding(kind: .vaultInSelectedFolderItself, folderIDs: reported, tokens: reported.map(folderToken))]

        case .insideAppFolder:
            let parent = (rawRoot as NSString).deletingLastPathComponent
            if VaultManager.isAppDocumentsRoot(parent), !atRoot.isEmpty { return [] }
            return notAnAppFolder((atRoot + below).sorted())

        case .elsewhere:
            return notAnAppFolder((atRoot + below).sorted())
        }
    }

    static func folderToken(_ folderID: String) -> String {
        "folder:" + folderID
    }

    /// A vault that was reported once (see `findings`).
    static func seenToken(_ folderID: String) -> String {
        "seen:" + folderID
    }

    /// Keyed by the path, so another selected folder gets its own notice.
    static func rootToken(canonicalLowercasedRoot: String) -> String {
        "root:" + canonicalLowercasedRoot
    }

    // MARK: - Sidecar (what the user keeps, what was reported)

    /// A notice that could never be put away would hold the status at
    /// "Action Needed" for good on an install that syncs outside Obsidian's
    /// folder on purpose; and a reported vault has to stay reported.
    private static let storeKey = "vaultsync.obsidianVisibilityAcknowledged"

    /// Serializes the read-modify-write of the sidecar — same pattern as
    /// `PathCollisionGuard`. The blocks touch only UserDefaults.
    private static let queue = DispatchQueue(label: "eu.vaultsync.obsidianvisibility.sidecar")

    /// `defaults` is a parameter so the sidecar is testable against a store
    /// of its own; the app only ever uses the standard one.
    static func loadRecord(defaults: UserDefaults = .standard) -> Set<String> {
        queue.sync { Set(defaults.array(forKey: storeKey) as? [String] ?? []) }
    }

    /// Add tokens to the record. Atomic and idempotent.
    static func record(_ tokens: [String], defaults: UserDefaults = .standard) {
        guard !tokens.isEmpty else { return }
        queue.sync {
            var stored = Set(defaults.array(forKey: storeKey) as? [String] ?? [])
            let before = stored
            stored.formUnion(tokens)
            guard stored != before else { return }
            defaults.set(stored.sorted(), forKey: storeKey)
        }
    }

    /// Forget everything about a folder when it is removed, so a later
    /// folder with the same ID is judged afresh. Atomic.
    static func forget(folderID: String, defaults: UserDefaults = .standard) {
        queue.sync {
            var stored = Set(defaults.array(forKey: storeKey) as? [String] ?? [])
            let before = stored
            stored.remove(folderToken(folderID))
            stored.remove(seenToken(folderID))
            guard stored != before else { return }
            defaults.set(stored.sorted(), forKey: storeKey)
        }
    }

    // MARK: - Live wiring

    /// Reads what the directory holds. Needs the access the selected folder
    /// was granted with; without it the listing fails and nil is returned.
    static func liveContent(_ path: String) -> DirectoryContent? {
        let url = URL(fileURLWithPath: path, isDirectory: true)
        guard let vaults = VaultManager.vaultSubfolderNames(in: url) else { return nil }
        var isDirectory: ObjCBool = false
        let hasOwnConfig = FileManager.default.fileExists(
            atPath: url.appendingPathComponent(".obsidian", isDirectory: true).path,
            isDirectory: &isDirectory
        ) && isDirectory.boolValue
        return DirectoryContent(hasOwnConfig: hasOwnConfig, hasVaultSubfolders: !vaults.isEmpty)
    }

    /// The findings as they stand now. Remembers a vault it reports as
    /// being in the selected folder itself (see `findings`) — but only one
    /// `mayRemember` vouches for: a folder that has finished a sync and
    /// needs nothing more. Before that, a whole-directory sync whose vaults
    /// have not arrived yet reads exactly like a single vault, and a record
    /// made then would report it for good.
    static func findingsLive(
        selectedRoot: String?,
        folders: [(id: String, path: String)],
        mayRemember: (String) -> Bool,
        defaults: UserDefaults = .standard
    ) -> [Finding] {
        let found = findings(
            selectedRoot: selectedRoot,
            folders: folders,
            env: Environment(
                canonicalize: FolderPathReconciler.canonical,
                content: liveContent,
                record: { loadRecord(defaults: defaults) }
            )
        )
        record(
            found.filter { $0.kind == .vaultInSelectedFolderItself }
                .flatMap(\.folderIDs)
                .filter(mayRemember)
                .map(seenToken),
            defaults: defaults
        )
        return found
    }
}
