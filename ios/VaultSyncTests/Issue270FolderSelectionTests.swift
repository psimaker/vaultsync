import Foundation
import Testing
@testable import VaultSync

/// #270: the folder VaultSync is pointed at has to be one Obsidian shows
/// vaults from. The paths are the forms the folder picker returns (read on a
/// device and on the iOS 18 and 26 simulators).
@Suite("Folder selection — only a folder Obsidian shows vaults from (#270)")
struct Issue270FolderSelectionRuleTests {

    private static let uuid = "0A1B2C3D-0000-4000-8000-000000000000"
    /// "On My iPhone" → "Obsidian".
    private static let obsidiansFolder = "/private/var/mobile/Containers/Data/Application/\(uuid)/Documents"
    /// The app's folder in iCloud Drive.
    private static let obsidiansICloudFolder = "/private/var/mobile/Library/Mobile Documents/iCloud~md~obsidian/Documents"
    /// A folder the user made under "On My iPhone" and named "Obsidian".
    private static let selfMadeObsidian = "/private/var/mobile/Containers/Shared/AppGroup/\(uuid)/File Provider Storage/Obsidian"
    /// A folder the user made in iCloud Drive.
    private static let iCloudDriveFolder = "/private/var/mobile/Library/Mobile Documents/com~apple~CloudDocs/Obsidian"

    private func verdict(
        _ path: String,
        _ mode: FolderSelectionMode,
        hasOwnConfig: Bool = false,
        hasVaultSubfolders: Bool = false
    ) -> FolderSelectionVerdict {
        VaultManager.folderSelectionVerdict(
            path: path,
            mode: mode,
            hasOwnConfig: hasOwnConfig,
            hasVaultSubfolders: hasVaultSubfolders
        )
    }

    @Test("Where a picked folder lies is read from its path")
    func placeOfAPickedFolder() {
        #expect(VaultManager.selectedFolderPlace(Self.obsidiansFolder) == .appFolder)
        #expect(VaultManager.selectedFolderPlace(Self.obsidiansICloudFolder) == .appFolder)
        #expect(VaultManager.selectedFolderPlace(Self.obsidiansFolder + "/MyVault") == .insideAppFolder)
        #expect(VaultManager.selectedFolderPlace(Self.obsidiansFolder + "/MyVault/Daily/2026") == .insideAppFolder)
        #expect(VaultManager.selectedFolderPlace(Self.obsidiansFolder + "/Documents") == .insideAppFolder)
        #expect(VaultManager.selectedFolderPlace(Self.obsidiansICloudFolder + "/MyVault") == .insideAppFolder)
        #expect(VaultManager.selectedFolderPlace(Self.selfMadeObsidian) == .elsewhere)
        #expect(VaultManager.selectedFolderPlace(Self.selfMadeObsidian + "/MyVault") == .elsewhere)
        #expect(VaultManager.selectedFolderPlace(Self.iCloudDriveFolder) == .elsewhere)
        #expect(VaultManager.selectedFolderPlace("/") == .elsewhere)
        #expect(VaultManager.selectedFolderPlace("") == .elsewhere)
    }

    @Test("First selection: an app's own folder that holds a vault is accepted, on the iPhone and in iCloud")
    func firstSelectionAcceptsObsidiansFolder() {
        for root in [Self.obsidiansFolder, Self.obsidiansICloudFolder] {
            for hasOwnConfig in [false, true] {
                #expect(verdict(root, .firstSelection, hasOwnConfig: hasOwnConfig, hasVaultSubfolders: true) == .accepted)
            }
        }
    }

    @Test("First selection: a folder the user made is refused whatever it is named and whatever it holds")
    func firstSelectionRefusesASelfMadeFolder() {
        // The reported path: "Obsidian" is missing under "On My iPhone", the
        // user creates it, setup completes, and Obsidian never shows the vault.
        for path in [Self.selfMadeObsidian, Self.iCloudDriveFolder, Self.selfMadeObsidian + "/Notes"] {
            for hasOwnConfig in [false, true] {
                for hasVaultSubfolders in [false, true] {
                    #expect(
                        verdict(path, .firstSelection, hasOwnConfig: hasOwnConfig, hasVaultSubfolders: hasVaultSubfolders)
                            == .outsideAppFolder,
                        "\(path) config=\(hasOwnConfig) vaults=\(hasVaultSubfolders)"
                    )
                }
            }
        }
    }

    @Test("First selection: a vault picked instead of the folder above it is refused")
    func firstSelectionRefusesAVault() {
        for path in [Self.obsidiansFolder + "/MyVault", Self.obsidiansICloudFolder + "/MyVault", Self.obsidiansFolder + "/MyVault/Daily"] {
            for hasOwnConfig in [false, true] {
                #expect(verdict(path, .firstSelection, hasOwnConfig: hasOwnConfig) == .insideAppFolder)
            }
        }
    }

    @Test("First selection: an app's folder without a vault is refused — a leftover .obsidian is not one")
    func firstSelectionRefusesAnAppFolderWithoutAVault() {
        #expect(verdict(Self.obsidiansFolder, .firstSelection) == .appFolderWithoutVault)
        #expect(verdict(Self.obsidiansFolder, .firstSelection, hasOwnConfig: true) == .appFolderWithoutVault)
    }

    @Test("An install that already syncs keeps the rule it was set up under")
    func existingInstallKeepsItsRule() {
        // Every folder the earlier rule accepted is still accepted, so a
        // syncing vault's folder can always be granted again.
        #expect(verdict(Self.selfMadeObsidian, .existingInstall) == .accepted)
        #expect(verdict(Self.obsidiansFolder + "/MyVault", .existingInstall, hasOwnConfig: true) == .accepted)
        #expect(verdict(Self.selfMadeObsidian + "/Notes", .existingInstall, hasVaultSubfolders: true) == .accepted)
        #expect(verdict(Self.obsidiansFolder, .existingInstall, hasVaultSubfolders: true) == .accepted)
        // …and what it refused is still refused.
        #expect(verdict(Self.obsidiansFolder, .existingInstall) == .notAnObsidianFolder)
        #expect(verdict(Self.selfMadeObsidian + "/Notes", .existingInstall) == .notAnObsidianFolder)
    }
}

/// The same rule read from real directories, and its wiring into the grant.
@MainActor
@Suite("Folder selection against real folders (#270)", .serialized)
struct Issue270FolderSelectionLiveTests {

    private let sandbox: URL

    init() throws {
        sandbox = FileManager.default.temporaryDirectory
            .appendingPathComponent("issue270-selection-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: sandbox, withIntermediateDirectories: true)
    }

    private func makeDirectory(_ relativePath: String) throws -> URL {
        let url = sandbox.appendingPathComponent(relativePath, isDirectory: true)
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        return url
    }

    private func addVault(named name: String, to directory: URL) throws {
        try FileManager.default.createDirectory(
            at: directory.appendingPathComponent("\(name)/.obsidian", isDirectory: true),
            withIntermediateDirectories: true
        )
    }

    private func makeAppDocumentsRoot() throws -> URL {
        try makeDirectory("Containers/Data/Application/\(UUID().uuidString)/Documents")
    }

    private func makeSelfMadeObsidianFolder() throws -> URL {
        try makeDirectory("Containers/Shared/AppGroup/\(UUID().uuidString)/File Provider Storage/Obsidian")
    }

    @Test("A self-made \"Obsidian\" folder is refused on first selection with the way to Obsidian's own folder")
    func selfMadeFolderIsRefusedWithGuidance() throws {
        let folder = try makeSelfMadeObsidianFolder()
        // Refused even when a vault was copied into it: Obsidian cannot open it there.
        try addVault(named: "Notes", to: folder)

        let message = try #require(VaultManager.liveValidationError(for: folder, mode: .firstSelection))
        #expect(message.contains("not Obsidian's own folder"))
        #expect(message.contains("\"On My iPhone\" → \"Obsidian\""))
        // The folder is missing under "On My iPhone" until Obsidian holds a
        // vault there — the guidance has to say how to make it appear.
        #expect(message.contains("create a vault"))

        #expect(VaultManager.liveValidationError(for: folder, mode: .existingInstall) == nil)
    }

    @Test("Obsidian's folder with a vault is accepted; without one, or with a vault picked instead, each gets its own guidance")
    func obsidiansFolderAndItsRefusals() throws {
        let root = try makeAppDocumentsRoot()

        // Visible, but no vault in it yet.
        try FileManager.default.createDirectory(
            at: root.appendingPathComponent("Plain Folder", isDirectory: true),
            withIntermediateDirectories: false
        )
        let noVault = try #require(VaultManager.liveValidationError(for: root, mode: .firstSelection))
        #expect(noVault.contains("does not hold a vault yet"))

        try addVault(named: "MyVault", to: root)
        #expect(VaultManager.liveValidationError(for: root, mode: .firstSelection) == nil)

        let vault = root.appendingPathComponent("MyVault", isDirectory: true)
        let insideApp = try #require(VaultManager.liveValidationError(for: vault, mode: .firstSelection))
        #expect(insideApp.contains("inside an app's folder"))
        #expect(insideApp.contains("without opening one of your vaults"))
    }

    @Test("Something that is not a folder is refused in either mode")
    func notAFolderIsRefused() throws {
        let missing = sandbox.appendingPathComponent("missing", isDirectory: true)
        #expect(VaultManager.liveValidationError(for: missing, mode: .firstSelection) != nil)
        #expect(VaultManager.liveValidationError(for: missing, mode: .existingInstall) != nil)
    }

    // MARK: - Wiring into the grant

    /// The live environment with the parts a test cannot have replaced: the
    /// security scope and the bookmark store. Validation and the vault scan
    /// stay the real ones and read the real directories.
    private func environment(
        hasSyncedFolders: Bool,
        storedBookmark: URL? = nil
    ) -> VaultManager.Environment {
        var environment = VaultManager.Environment.live
        let access = BookmarkService.AccessEnvironment(start: { _ in true }, stop: { _ in })
        environment.acquireAccess = { url, owner in
            BookmarkService.acquireAccess(to: url, owner: owner, environment: access)
        }
        environment.makeBookmarkData = { _ in Data("issue-270".utf8) }
        environment.persistBookmarkData = { _ in }
        environment.refreshBookmarkData = { _, _ in true }
        environment.resolveBookmark = {
            storedBookmark.map {
                BookmarkService.ResolvedBookmark(url: $0, isStale: false, sourceData: Data("issue-270".utf8))
            }
        }
        environment.hasBookmark = { storedBookmark != nil }
        environment.cleanupLegacyBookmarks = {}
        environment.hasSyncedFolders = { hasSyncedFolders }
        return environment
    }

    @Test("Nothing syncs yet: the grant refuses a self-made folder and takes Obsidian's")
    func grantOnAFreshInstall() throws {
        let selfMade = try makeSelfMadeObsidianFolder()
        let fresh = VaultManager(environment: environment(hasSyncedFolders: false))
        let refusal = try #require(fresh.grantAccess(url: selfMade))
        #expect(refusal.contains("not Obsidian's own folder"))
        #expect(!fresh.isAccessible)

        let root = try makeAppDocumentsRoot()
        try addVault(named: "MyVault", to: root)
        #expect(fresh.grantAccess(url: root) == nil)
        #expect(fresh.isAccessible)
        #expect(fresh.detectedVaults == ["MyVault"])
    }

    @Test("A vault already syncs: the folder it lives in can be granted again, by either signal")
    func grantOnAnInstallThatSyncs() throws {
        let selfMade = try makeSelfMadeObsidianFolder()

        // The reconciler's record says a folder is configured.
        let byRecord = VaultManager(environment: environment(hasSyncedFolders: true))
        #expect(byRecord.grantAccess(url: selfMade) == nil)
        #expect(byRecord.isAccessible)

        // The record is empty, the engine reports a folder.
        let byEngine = VaultManager(environment: environment(hasSyncedFolders: false))
        #expect(byEngine.grantAccess(url: selfMade, engineHasFolders: true) == nil)
        #expect(byEngine.isAccessible)
    }

    @Test("A folder granted before stays granted at launch, whatever syncs")
    func restoreKeepsAnEarlierGrant() throws {
        let selfMade = try makeSelfMadeObsidianFolder()
        for hasSyncedFolders in [false, true] {
            let manager = VaultManager(environment: environment(hasSyncedFolders: hasSyncedFolders, storedBookmark: selfMade))
            manager.restoreAccess()
            #expect(manager.isAccessible, "hasSyncedFolders=\(hasSyncedFolders)")
            #expect(!manager.needsReconnect)
        }
    }
}
