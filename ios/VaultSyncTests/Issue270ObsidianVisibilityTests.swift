import Foundation
import Testing
@testable import VaultSync

/// #270, existing installs: a vault that syncs where Obsidian does not list
/// it keeps syncing and gets a notice with the manual way out. The paths are
/// the forms the folder picker returns (read on a device and on the iOS 18
/// and 26 simulators).
@Suite("Existing installs — what Obsidian cannot show (#270)")
struct Issue270ObsidianVisibilityTests {

    private static let uuid = "0A1B2C3D-0000-4000-8000-000000000000"
    /// "On My iPhone" → "Obsidian".
    private static let obsidiansFolder = "/private/var/mobile/Containers/Data/Application/\(uuid)/Documents"
    /// The app's folder in iCloud Drive.
    private static let obsidiansICloudFolder = "/private/var/mobile/Library/Mobile Documents/iCloud~md~obsidian/Documents"
    /// A folder the user made under "On My iPhone" and named "Obsidian".
    private static let selfMadeObsidian = "/private/var/mobile/Containers/Shared/AppGroup/\(uuid)/File Provider Storage/Obsidian"
    /// A folder the user made in iCloud Drive.
    private static let iCloudDriveFolder = "/private/var/mobile/Library/Mobile Documents/com~apple~CloudDocs/Obsidian"

    private static let singleVault = ObsidianVisibility.DirectoryContent(hasOwnConfig: true, hasVaultSubfolders: false)

    private func findings(
        root: String?,
        folders: [(id: String, path: String)],
        content: ObsidianVisibility.DirectoryContent? = Issue270ObsidianVisibilityTests.singleVault,
        acknowledged: Set<String> = [],
        canonicalize: @escaping (String) -> String = { $0 }
    ) -> [ObsidianVisibility.Finding] {
        ObsidianVisibility.findings(
            selectedRoot: root,
            folders: folders,
            env: ObsidianVisibility.Environment(
                canonicalize: canonicalize,
                content: { _ in content },
                acknowledged: { acknowledged }
            )
        )
    }

    // MARK: A vault in the selected folder itself

    @Test("A vault that syncs into Obsidian's folder itself is reported, on the iPhone and in iCloud")
    func vaultInObsidiansFolderItself() {
        for root in [Self.obsidiansFolder, Self.obsidiansICloudFolder] {
            let found = findings(root: root, folders: [(id: "notes", path: root)])
            #expect(found == [ObsidianVisibility.Finding(
                kind: .vaultInSelectedFolderItself,
                folderIDs: ["notes"],
                tokens: ["folder:notes"]
            )])
        }
    }

    @Test("A whole-directory sync with the vaults inside is the setup Obsidian shows — not reported, stray .obsidian or not")
    func wholeDirectorySyncWithVaultsInside() {
        for hasOwnConfig in [false, true] {
            let found = findings(
                root: Self.obsidiansFolder,
                folders: [(id: "all", path: Self.obsidiansFolder)],
                content: .init(hasOwnConfig: hasOwnConfig, hasVaultSubfolders: true)
            )
            #expect(found.isEmpty)
        }
    }

    @Test("Without a .obsidian of its own the folder is not known to be a vault — not reported")
    func noConfigNoFinding() {
        let found = findings(
            root: Self.obsidiansFolder,
            folders: [(id: "all", path: Self.obsidiansFolder)],
            content: .init(hasOwnConfig: false, hasVaultSubfolders: false)
        )
        #expect(found.isEmpty)
    }

    @Test("A folder that cannot be read is not reported")
    func unreadableFolderIsNotReported() {
        let found = findings(
            root: Self.obsidiansFolder,
            folders: [(id: "notes", path: Self.obsidiansFolder)],
            content: nil
        )
        #expect(found.isEmpty)
    }

    @Test("A vault in its own folder inside Obsidian's folder is where it belongs — the disk is not even read")
    func vaultInItsOwnFolder() {
        var contentReads = 0
        let found = ObsidianVisibility.findings(
            selectedRoot: Self.obsidiansFolder,
            folders: [
                (id: "notes", path: Self.obsidiansFolder + "/Notes"),
                (id: "work", path: Self.obsidiansFolder + "/Work"),
            ],
            env: ObsidianVisibility.Environment(
                canonicalize: { $0 },
                content: { _ in
                    contentReads += 1
                    return Self.singleVault
                },
                acknowledged: { [] }
            )
        )
        #expect(found.isEmpty)
        #expect(contentReads == 0)
    }

    @Test("The selected folder and the vault's path are compared as the collision guards compare them")
    func comparisonIsCanonicalAndCaseInsensitive() {
        // The bookmark resolves to /var/…, the engine stores /private/var/…;
        // the data volume folds case.
        let root = "/var/mobile/Containers/Data/Application/\(Self.uuid)/Documents"
        let stored = "/private/var/mobile/Containers/Data/Application/\(Self.uuid)/documents/"
        let found = findings(
            root: root,
            folders: [(id: "notes", path: stored)],
            canonicalize: { path in
                var canonical = path.hasPrefix("/private/") ? String(path.dropFirst("/private".count)) : path
                while canonical.hasSuffix("/") { canonical.removeLast() }
                return canonical
            }
        )
        #expect(found.map(\.folderIDs) == [["notes"]])
    }

    @Test("A single vault picked as the selected folder is a direct subfolder of Obsidian's folder — Obsidian lists it")
    func singleVaultPickedAsTheSelectedFolder() {
        let root = Self.obsidiansFolder + "/MyVault"
        #expect(findings(root: root, folders: [(id: "notes", path: root)]).isEmpty)
        #expect(findings(root: root, folders: []).isEmpty)
    }

    // MARK: A selected folder outside every app's own folder

    @Test("A self-made folder is reported with the vaults that sync there")
    func selfMadeFolderWithVaults() {
        for root in [Self.selfMadeObsidian, Self.iCloudDriveFolder] {
            let found = findings(root: root, folders: [
                (id: "work", path: root + "/Work"),
                (id: "notes", path: root + "/Notes"),
                (id: "all", path: root),
                (id: "elsewhere", path: Self.obsidiansFolder + "/Other"),
                (id: "sibling", path: root + "-old/Notes"),
            ])
            #expect(found == [ObsidianVisibility.Finding(
                kind: .selectedFolderOutsideAppFolder,
                folderIDs: ["all", "notes", "work"],
                tokens: ["root:" + root.lowercased()]
            )])
        }
    }

    @Test("A self-made folder is reported while nothing syncs yet — the next share would land there")
    func selfMadeFolderWithoutVaults() {
        let found = findings(root: Self.selfMadeObsidian, folders: [])
        #expect(found.map(\.kind) == [.selectedFolderOutsideAppFolder])
        #expect(found.first?.folderIDs.isEmpty == true)
    }

    @Test("Without a selected folder there is nothing to report")
    func noSelectedFolder() {
        #expect(findings(root: nil, folders: [(id: "notes", path: Self.selfMadeObsidian + "/Notes")]).isEmpty)
    }

    // MARK: Keeping the setup

    @Test("A vault the user keeps where it is stays quiet; another one in the same place is still reported")
    func keptVaultStaysQuiet() {
        let folders = [(id: "notes", path: Self.obsidiansFolder), (id: "second", path: Self.obsidiansFolder)]
        let found = findings(root: Self.obsidiansFolder, folders: folders, acknowledged: ["folder:notes"])
        #expect(found.map(\.folderIDs) == [["second"]])
        #expect(found.flatMap(\.tokens) == ["folder:second"])

        var contentReads = 0
        let allKept = ObsidianVisibility.findings(
            selectedRoot: Self.obsidiansFolder,
            folders: folders,
            env: ObsidianVisibility.Environment(
                canonicalize: { $0 },
                content: { _ in
                    contentReads += 1
                    return Self.singleVault
                },
                acknowledged: { ["folder:notes", "folder:second"] }
            )
        )
        #expect(allKept.isEmpty)
        #expect(contentReads == 0)
    }

    @Test("A selected folder the user keeps stays quiet; a different one gets its own notice")
    func keptSelectedFolderStaysQuiet() {
        let kept: Set<String> = ["root:" + Self.selfMadeObsidian.lowercased()]
        #expect(findings(root: Self.selfMadeObsidian, folders: [], acknowledged: kept).isEmpty)
        #expect(findings(root: Self.iCloudDriveFolder, folders: [], acknowledged: kept).count == 1)
        // What was kept for a folder says nothing about the selected folder.
        #expect(findings(root: Self.selfMadeObsidian, folders: [], acknowledged: ["folder:notes"]).count == 1)
    }
}

/// The record of what the user chose to keep.
@Suite("Existing installs — the kept-notice record (#270)")
struct Issue270ObsidianVisibilitySidecarTests {

    private let suiteName = "issue270-visibility-\(UUID().uuidString)"

    private func withDefaults(_ body: (UserDefaults) -> Void) throws {
        let defaults = try #require(UserDefaults(suiteName: suiteName))
        defer { defaults.removePersistentDomain(forName: suiteName) }
        body(defaults)
    }

    @Test("Keeping is recorded once, survives a second tap, and a removed vault is forgotten")
    func acknowledgeAndClear() throws {
        try withDefaults { defaults in
            #expect(ObsidianVisibility.loadAcknowledged(defaults: defaults).isEmpty)

            ObsidianVisibility.acknowledge(["folder:notes", "root:/some/folder"], defaults: defaults)
            ObsidianVisibility.acknowledge(["folder:notes"], defaults: defaults)
            #expect(ObsidianVisibility.loadAcknowledged(defaults: defaults) == ["folder:notes", "root:/some/folder"])

            // Removing another vault leaves the record alone.
            ObsidianVisibility.clearAcknowledged(folderID: "work", defaults: defaults)
            #expect(ObsidianVisibility.loadAcknowledged(defaults: defaults) == ["folder:notes", "root:/some/folder"])

            // Removing the vault forgets its record and nothing else: the
            // selected folder was kept on its own.
            ObsidianVisibility.clearAcknowledged(folderID: "notes", defaults: defaults)
            #expect(ObsidianVisibility.loadAcknowledged(defaults: defaults) == ["root:/some/folder"])
        }
    }
}

/// The same classification read from real directories.
@Suite("Existing installs — read from real folders (#270)", .serialized)
struct Issue270ObsidianVisibilityLiveTests {

    private let sandbox: URL

    init() throws {
        sandbox = FileManager.default.temporaryDirectory
            .appendingPathComponent("issue270-visibility-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: sandbox, withIntermediateDirectories: true)
    }

    private func makeDirectory(_ relativePath: String) throws -> URL {
        let url = sandbox.appendingPathComponent(relativePath, isDirectory: true)
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        return url
    }

    private func liveFindings(root: URL, folders: [(id: String, path: String)]) -> [ObsidianVisibility.Finding] {
        ObsidianVisibility.findings(
            selectedRoot: root.path,
            folders: folders,
            env: ObsidianVisibility.Environment(
                canonicalize: FolderPathReconciler.canonical,
                content: ObsidianVisibility.liveContent,
                acknowledged: { [] }
            )
        )
    }

    @Test("The reported install: a vault collapsed into Obsidian's folder — and the notice ends once it has a folder of its own")
    func collapsedVaultOnDisk() throws {
        defer { try? FileManager.default.removeItem(at: sandbox) }
        let root = try makeDirectory("Containers/Data/Application/\(UUID().uuidString)/Documents")
        // What the old shortcut left behind: the vault's configuration, a
        // note and a plain subfolder directly in Obsidian's folder.
        for name in [".obsidian", "Daily"] {
            try FileManager.default.createDirectory(
                at: root.appendingPathComponent(name, isDirectory: true),
                withIntermediateDirectories: true
            )
        }
        try Data("# note".utf8).write(to: root.appendingPathComponent("Inbox.md"))

        #expect(ObsidianVisibility.liveContent(root.path) == .init(hasOwnConfig: true, hasVaultSubfolders: false))
        let collapsed = liveFindings(root: root, folders: [(id: "notes", path: root.path)])
        #expect(collapsed.map(\.kind) == [.vaultInSelectedFolderItself])
        #expect(collapsed.first?.folderIDs == ["notes"])

        // The way out, done by the user: remove, accept again — the vault
        // now syncs in a folder of its own, the leftovers still lie around.
        let ownFolder = root.appendingPathComponent("Notes", isDirectory: true)
        try FileManager.default.createDirectory(at: ownFolder, withIntermediateDirectories: true)
        #expect(liveFindings(root: root, folders: [(id: "notes", path: ownFolder.path)]).isEmpty)
    }

    @Test("Obsidian's folder with the vaults inside is read as a container; a folder that is gone is not read at all")
    func containerAndMissingFolderOnDisk() throws {
        defer { try? FileManager.default.removeItem(at: sandbox) }
        let relative = "Containers/Data/Application/\(UUID().uuidString)/Documents"
        let root = try makeDirectory(relative)
        _ = try makeDirectory(relative + "/.obsidian")
        _ = try makeDirectory(relative + "/Personal/.obsidian")

        #expect(ObsidianVisibility.liveContent(root.path) == .init(hasOwnConfig: true, hasVaultSubfolders: true))
        #expect(liveFindings(root: root, folders: [(id: "all", path: root.path)]).isEmpty)

        #expect(ObsidianVisibility.liveContent(root.path + "-gone") == nil)
    }
}

/// How the findings reach the user: the issue cards and the status tier.
@Suite("Existing installs — the notice and its tier (#270)")
struct Issue270ObsidianVisibilityIssueTests {

    @Test("A vault in the selected folder itself becomes a warning that names it and the way out")
    func vaultInSelectedFolderItselfIssue() throws {
        let issues = SyncthingManager.obsidianVisibilityIssues(
            findings: [.init(kind: .vaultInSelectedFolderItself, folderIDs: ["notes"], tokens: ["folder:notes"])],
            labels: ["notes": "My Notes"]
        )
        let issue = try #require(issues.first)
        #expect(issues.count == 1)
        #expect(issue.kind == .vaultInSelectedFolderItself)
        #expect(issue.severity == .warning)
        #expect(issue.count == 1)
        #expect(issue.folderID == "notes")
        #expect(issue.message.contains("My Notes"))
        // Nothing is moved for the user: the steps are theirs, in order.
        #expect(issue.remediation.contains("moves nothing on its own"))
        let remove = try #require(issue.remediation.range(of: "remove it on this iPhone"))
        let accept = try #require(issue.remediation.range(of: "Accept it again under Pending Shares"))
        #expect(remove.upperBound <= accept.lowerBound)
    }

    @Test("A selected folder outside Obsidian becomes a warning — with the vaults that sync there, and without any")
    func selectedFolderOutsideObsidianIssue() throws {
        let withVaults = try #require(SyncthingManager.obsidianVisibilityIssues(
            findings: [.init(kind: .selectedFolderOutsideAppFolder, folderIDs: ["a", "b"], tokens: ["root:/x"])],
            labels: ["a": "Notes"]
        ).first)
        #expect(withVaults.kind == .selectedFolderOutsideObsidian)
        #expect(withVaults.severity == .warning)
        #expect(withVaults.count == 2)
        // A vault without a label is named by its ID.
        #expect(withVaults.message.contains("Notes, b"))
        // Remove first: with nothing syncing, the folder picked next is held
        // against the first-selection rule (decision 059).
        let remove = try #require(withVaults.remediation.range(of: "remove them on this iPhone"))
        let select = try #require(withVaults.remediation.range(of: "Select Obsidian's Folder"))
        let accept = try #require(withVaults.remediation.range(of: "Accept the shares again"))
        #expect(remove.upperBound <= select.lowerBound)
        #expect(select.upperBound <= accept.lowerBound)

        let empty = try #require(SyncthingManager.obsidianVisibilityIssues(
            findings: [.init(kind: .selectedFolderOutsideAppFolder, folderIDs: [], tokens: ["root:/x"])],
            labels: [:]
        ).first)
        #expect(empty.count == 0)
        #expect(empty.folderID == nil)
        #expect(empty.remediation.contains("Select Obsidian's Folder"))
        #expect(!empty.remediation.contains("remove"))
        #expect(empty.id != withVaults.id)
    }

    @Test("No finding, no notice")
    func noFindingNoIssue() {
        #expect(SyncthingManager.obsidianVisibilityIssues(findings: [], labels: ["notes": "Notes"]).isEmpty)
    }

    @Test("The notice holds the header at Action Needed and the widget at its warning tier, across a background sync")
    func noticeReachesHeaderAndWidget() {
        let issues = SyncthingManager.obsidianVisibilityIssues(
            findings: [.init(kind: .selectedFolderOutsideAppFolder, folderIDs: ["a"], tokens: ["root:/x"])],
            labels: [:]
        )
        let header = SyncHeaderModel.derive(.init(
            hasEngineError: false,
            engineRunning: true,
            issueSeverities: issues.map(\.severity),
            hasUnreachableFolders: false,
            isSyncing: false,
            hasSyncFolders: true,
            vaultAccessible: true,
            vaultNeedsReconnect: false,
            hasDetectedVaults: true
        ))
        #expect(header == .init(status: .attention, titleKey: "Action Needed"))
        #expect(SyncthingManager.durableIssueFloor(
            issues: issues.map { ($0.kind, $0.severity) },
            hasUnreachableFolders: false
        ) == .warning)
    }
}

/// The manager's issue list is the one source the header, the Sync Issues
/// section and the widget read (decision 012).
@MainActor
@Suite("Existing installs — the notice in the issue list (#270)")
struct Issue270ObsidianVisibilityIssueListTests {

    private static let root = "/private/var/mobile/Containers/Data/Application/0A1B2C3D-0000-4000-8000-000000000000/Documents"

    private func noticeKinds(_ manager: SyncthingManager) -> [SyncthingManager.SyncIssueItem.Kind] {
        manager.unresolvedIssues.map(\.kind).filter {
            $0 == .vaultInSelectedFolderItself || $0 == .selectedFolderOutsideObsidian
        }
    }

    @Test("A finding is an entry of the issue list, named by the vault's label, and leaves the list with the finding")
    func findingsAreIssues() throws {
        let manager = SyncthingManager()
        manager._testSetFolders([
            SyncthingManager.FolderInfo(
                id: "notes",
                label: "My Notes",
                path: Self.root,
                type: "sendreceive",
                paused: false,
                deviceIDs: []
            ),
        ])
        // The folder list alone raises nothing: the findings are read on
        // settled paths, never derived on the fly from an unsettled list.
        #expect(noticeKinds(manager).isEmpty)

        manager._testSetObsidianVisibilityFindings([
            .init(kind: .vaultInSelectedFolderItself, folderIDs: ["notes"], tokens: ["folder:notes"]),
            .init(kind: .selectedFolderOutsideAppFolder, folderIDs: [], tokens: ["root:/x"]),
        ])
        #expect(noticeKinds(manager) == [.vaultInSelectedFolderItself, .selectedFolderOutsideObsidian])
        let issue = try #require(manager.unresolvedIssues.first { $0.kind == .vaultInSelectedFolderItself })
        #expect(issue.message.contains("My Notes"))
        #expect(issue.severity == .warning)

        manager._testSetObsidianVisibilityFindings([])
        #expect(noticeKinds(manager).isEmpty)
    }
}

/// #270: a pending share says where it will arrive before it is accepted.
@Suite("Pending shares — where the vault will arrive (#270)")
struct Issue270PendingShareDestinationTests {

    private static let root = "/private/var/mobile/Containers/Data/Application/0A1B2C3D-0000-4000-8000-000000000000/Documents"

    @Test("A share headed for its own folder names that folder")
    func ownFolder() {
        for place in [VaultManager.SelectedFolderPlace.appFolder, .insideAppFolder] {
            let line = PendingShareDestination.line(decision: .path(Self.root + "/Recipes"), rootPlace: place)
            #expect(line?.isCaution == false)
            #expect(line?.text.contains("Recipes") == true)
            #expect(line?.text.contains("your Obsidian folder") == true)
        }
    }

    @Test("A name clash shows the folder the share really gets")
    func suffixedFolder() {
        let line = PendingShareDestination.line(decision: .path(Self.root + "/Recipes (2)"), rootPlace: .appFolder)
        #expect(line?.text.contains("Recipes (2)") == true)
    }

    @Test("Under a selected folder outside Obsidian the line says that Obsidian cannot show the vault")
    func outsideObsidian() {
        let line = PendingShareDestination.line(decision: .path("/elsewhere/Obsidian/Recipes"), rootPlace: .elsewhere)
        #expect(line?.isCaution == true)
        #expect(line?.text.contains("Recipes") == true)
        #expect(line?.text.contains("Obsidian cannot show") == true)
    }

    @Test("A folder that already holds files is announced before the tap")
    func existingFolder() {
        let line = PendingShareDestination.line(
            decision: .requiresMergeConfirmation(path: Self.root + "/Recipes", targetName: "Recipes"),
            rootPlace: .appFolder
        )
        #expect(line?.isCaution == true)
        #expect(line?.text.contains("Recipes") == true)
        #expect(line?.text.contains("asks you before") == true)
    }

    @Test("No line while the folder is not connected or the accept would refuse")
    func nothingToSay() {
        #expect(PendingShareDestination.line(decision: nil, rootPlace: nil) == nil)
        #expect(PendingShareDestination.line(decision: .refused(message: "no"), rootPlace: .appFolder) == nil)
        #expect(PendingShareDestination.line(decision: .refused(message: "no"), rootPlace: .elsewhere) == nil)
    }
}
