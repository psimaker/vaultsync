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
/// - **A selected folder that is no app's own folder.** A folder the user
///   made and named "Obsidian" passed the earlier folder check (decision
///   059). Whatever syncs there, Obsidian never opens it.
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
        /// The selected folder lies outside every app's own folder.
        case selectedFolderOutsideAppFolder
    }

    struct Finding: Equatable, Sendable {
        let kind: Kind
        /// The folders the finding is about, sorted. May be empty for
        /// `.selectedFolderOutsideAppFolder`: the folder is the wrong place
        /// for the next share even while nothing syncs there.
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
    }

    // MARK: - Classification (pure)

    struct Environment {
        var canonicalize: (String) -> String
        /// What the directory at this path holds, or nil when it cannot be
        /// read. Called at most once, and only for a folder that syncs into
        /// an app's own folder itself.
        var content: (String) -> DirectoryContent?
        var acknowledged: () -> Set<String>
    }

    /// The findings for the selected folder and the configured folders.
    ///
    /// The selected folder is classified by its path alone, exactly as the
    /// folder check does (`VaultManager.selectedFolderPlace`), so the notice
    /// and the check can never disagree about what an app's own folder is.
    ///
    /// A folder in an app's own folder itself is reported only when that
    /// folder is a single vault by what it holds — a `.obsidian/` of its own
    /// and no vault subfolder, the rule of decision 014 without the
    /// exemption for app folders. A whole-directory sync whose shared folder
    /// contains the vaults looks the same by path and is the setup Obsidian
    /// shows correctly; it must not be reported. What cannot be read is not
    /// reported either.
    ///
    /// A single vault picked as the selected folder (a folder inside an
    /// app's own folder) is a direct subfolder of that app's folder:
    /// Obsidian lists it, and nothing is reported.
    static func findings(
        selectedRoot: String?,
        folders: [(id: String, path: String)],
        env: Environment
    ) -> [Finding] {
        guard let rawRoot = selectedRoot else { return [] }
        let acknowledged = env.acknowledged()
        // Lowercased like every path comparison in the collision guards:
        // the iOS data volume is case-folding APFS.
        let rootKey = env.canonicalize(rawRoot).lowercased()
        let folderKeys = folders.map { (id: $0.id, key: env.canonicalize($0.path).lowercased()) }

        switch VaultManager.selectedFolderPlace(rawRoot) {
        case .elsewhere:
            let token = rootToken(canonicalLowercasedRoot: rootKey)
            guard !acknowledged.contains(token) else { return [] }
            let inside = folderKeys
                .filter { $0.key == rootKey || $0.key.hasPrefix(rootKey + "/") }
                .map(\.id)
                .sorted()
            return [Finding(kind: .selectedFolderOutsideAppFolder, folderIDs: inside, tokens: [token])]

        case .appFolder:
            let atRoot = folderKeys
                .filter { $0.key == rootKey }
                .map(\.id)
                .filter { !acknowledged.contains(folderToken($0)) }
                .sorted()
            guard !atRoot.isEmpty,
                  let content = env.content(rawRoot),
                  VaultManager.rootIsItselfVault(
                      isAppDocumentsRoot: false,
                      hasOwnConfig: content.hasOwnConfig,
                      hasVaultSubfolders: content.hasVaultSubfolders
                  )
            else { return [] }
            return [Finding(kind: .vaultInSelectedFolderItself, folderIDs: atRoot, tokens: atRoot.map(folderToken))]

        case .insideAppFolder:
            return []
        }
    }

    static func folderToken(_ folderID: String) -> String {
        "folder:" + folderID
    }

    /// Keyed by the path, so another selected folder gets its own notice.
    static func rootToken(canonicalLowercasedRoot: String) -> String {
        "root:" + canonicalLowercasedRoot
    }

    // MARK: - Acknowledged sidecar ("keep it this way")

    /// Findings the user chose to keep. A notice that could never be put
    /// away would hold the status at "Action Needed" for good on an install
    /// that syncs outside Obsidian's folder on purpose.
    private static let storeKey = "vaultsync.obsidianVisibilityAcknowledged"

    /// Serializes the read-modify-write of the sidecar — same pattern as
    /// `PathCollisionGuard`. The blocks touch only UserDefaults.
    private static let queue = DispatchQueue(label: "eu.vaultsync.obsidianvisibility.sidecar")

    /// `defaults` is a parameter so the sidecar is testable against a store
    /// of its own; the app only ever uses the standard one.
    static func loadAcknowledged(defaults: UserDefaults = .standard) -> Set<String> {
        queue.sync { Set(defaults.array(forKey: storeKey) as? [String] ?? []) }
    }

    /// Record that the user keeps these findings. Atomic and idempotent.
    static func acknowledge(_ tokens: [String], defaults: UserDefaults = .standard) {
        queue.sync {
            var stored = Set(defaults.array(forKey: storeKey) as? [String] ?? [])
            let before = stored
            stored.formUnion(tokens)
            guard stored != before else { return }
            defaults.set(stored.sorted(), forKey: storeKey)
        }
    }

    /// Forget a folder's record when the folder is removed, so a later
    /// folder with the same ID is judged afresh. Atomic.
    static func clearAcknowledged(folderID: String, defaults: UserDefaults = .standard) {
        queue.sync {
            var stored = Set(defaults.array(forKey: storeKey) as? [String] ?? [])
            guard stored.remove(folderToken(folderID)) != nil else { return }
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

    static func findingsLive(selectedRoot: String?, folders: [(id: String, path: String)]) -> [Finding] {
        findings(
            selectedRoot: selectedRoot,
            folders: folders,
            env: Environment(
                canonicalize: FolderPathReconciler.canonical,
                content: liveContent,
                acknowledged: { loadAcknowledged() }
            )
        )
    }
}
