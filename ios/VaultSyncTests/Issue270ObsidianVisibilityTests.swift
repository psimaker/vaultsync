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
    private static let container = ObsidianVisibility.DirectoryContent(hasOwnConfig: true, hasVaultSubfolders: true)

    private func findings(
        root: String?,
        folders: [(id: String, path: String)],
        content: ObsidianVisibility.DirectoryContent? = Issue270ObsidianVisibilityTests.singleVault,
        record: Set<String> = [],
        canonicalize: @escaping (String) -> String = { $0 }
    ) -> [ObsidianVisibility.Finding] {
        ObsidianVisibility.findings(
            selectedRoot: root,
            folders: folders,
            env: ObsidianVisibility.Environment(
                canonicalize: canonicalize,
                content: { _ in content },
                record: { record }
            )
        )
    }

    private func notAnAppFolder(_ root: String, _ folderIDs: [String]) -> [ObsidianVisibility.Finding] {
        [ObsidianVisibility.Finding(
            kind: .selectedFolderNotAnAppFolder,
            folderIDs: folderIDs,
            tokens: ["root:" + root.lowercased()]
        )]
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

    @Test("A reported vault stays reported when the folder later looks like a container — the user opened one of its folders in Obsidian")
    func reportedVaultStaysReported() {
        var contentReads = 0
        let found = ObsidianVisibility.findings(
            selectedRoot: Self.obsidiansFolder,
            folders: [(id: "notes", path: Self.obsidiansFolder)],
            env: ObsidianVisibility.Environment(
                canonicalize: { $0 },
                content: { _ in
                    contentReads += 1
                    return Self.container
                },
                record: { ["seen:notes"] }
            )
        )
        #expect(found.map(\.folderIDs) == [["notes"]])
        // What it holds now no longer decides, so it is not read.
        #expect(contentReads == 0)

        // A vault that was never reported is still judged by what the folder holds.
        #expect(findings(
            root: Self.obsidiansFolder,
            folders: [(id: "other", path: Self.obsidiansFolder)],
            content: Self.container,
            record: ["seen:notes"]
        ).isEmpty)
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
                record: { [] }
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

    // MARK: A selected folder that is not an app's own folder

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
            #expect(found == notAnAppFolder(root, ["all", "notes", "work"]))
        }
    }

    @Test("A self-made folder is reported while nothing syncs yet — the next share would land there")
    func selfMadeFolderWithoutVaults() {
        #expect(findings(root: Self.selfMadeObsidian, folders: []) == notAnAppFolder(Self.selfMadeObsidian, []))
    }

    @Test("A single vault picked as the selected folder: quiet while it syncs there, reported once nothing does — the only way to another folder")
    func singleVaultPickedAsTheSelectedFolder() {
        let root = Self.obsidiansFolder + "/MyVault"
        // Obsidian lists it: a direct subfolder of its folder with the vault in it.
        #expect(findings(root: root, folders: [(id: "notes", path: root)]).isEmpty)
        // The vault was removed. A share is refused here (the folder is
        // itself a vault) and no other screen opens the folder picker.
        #expect(findings(root: root, folders: []) == notAnAppFolder(root, []))
    }

    @Test("A folder inside Obsidian's folder that only holds vaults is reported: they arrive as folders inside it, not as vaults")
    func folderInsideObsidiansFolderUsedAsContainer() {
        let root = Self.obsidiansFolder + "/Vaults"
        let found = findings(root: root, folders: [
            (id: "recipes", path: root + "/Recipes"),
            (id: "notes", path: root + "/Notes"),
        ])
        #expect(found == notAnAppFolder(root, ["notes", "recipes"]))

        // Deeper down, even a vault in the selected folder itself is not one
        // Obsidian lists.
        let deep = Self.obsidiansFolder + "/Archive/2025"
        #expect(findings(root: deep, folders: [(id: "old", path: deep)]) == notAnAppFolder(deep, ["old"]))
    }

    @Test("Without a selected folder there is nothing to report")
    func noSelectedFolder() {
        #expect(findings(root: nil, folders: [(id: "notes", path: Self.selfMadeObsidian + "/Notes")]).isEmpty)
    }

    // MARK: Keeping the setup

    @Test("A vault the user keeps where it is stays quiet; another one in the same place is still reported")
    func keptVaultStaysQuiet() {
        let folders = [(id: "notes", path: Self.obsidiansFolder), (id: "second", path: Self.obsidiansFolder)]
        let found = findings(root: Self.obsidiansFolder, folders: folders, record: ["folder:notes", "seen:notes"])
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
                record: { ["folder:notes", "folder:second"] }
            )
        )
        #expect(allKept.isEmpty)
        #expect(contentReads == 0)
    }

    @Test("A selected folder the user keeps stays quiet while vaults sync there — and is reported again once nothing does")
    func keptSelectedFolder() {
        let root = Self.selfMadeObsidian
        let kept: Set<String> = ["root:" + root.lowercased()]
        let syncing = [(id: "notes", path: root + "/Notes")]
        #expect(findings(root: root, folders: syncing, record: kept).isEmpty)
        // Nothing syncs: nothing is being kept, the next share would land
        // here, and this notice is the one place that offers another folder.
        #expect(findings(root: root, folders: [], record: kept) == notAnAppFolder(root, []))
        // A different selected folder gets its own notice.
        #expect(findings(root: Self.iCloudDriveFolder, folders: [(id: "notes", path: Self.iCloudDriveFolder + "/Notes")], record: kept).count == 1)
        // What was kept for a vault says nothing about the selected folder.
        #expect(findings(root: root, folders: syncing, record: ["folder:notes"]).count == 1)
    }
}

/// The record of what the user chose to keep and what was reported.
@Suite("Existing installs — the notice record (#270)")
struct Issue270ObsidianVisibilitySidecarTests {

    private let suiteName = "issue270-visibility-\(UUID().uuidString)"

    private func withDefaults(_ body: (UserDefaults) -> Void) throws {
        let defaults = try #require(UserDefaults(suiteName: suiteName))
        defer { defaults.removePersistentDomain(forName: suiteName) }
        body(defaults)
    }

    @Test("A record is written once, survives a second write, and a removed vault is forgotten")
    func recordAndForget() throws {
        try withDefaults { defaults in
            #expect(ObsidianVisibility.loadRecord(defaults: defaults).isEmpty)

            ObsidianVisibility.record(["folder:notes", "seen:notes", "root:/some/folder"], defaults: defaults)
            ObsidianVisibility.record(["folder:notes"], defaults: defaults)
            ObsidianVisibility.record([], defaults: defaults)
            #expect(ObsidianVisibility.loadRecord(defaults: defaults) == ["folder:notes", "seen:notes", "root:/some/folder"])

            // Removing another vault leaves the record alone.
            ObsidianVisibility.forget(folderID: "work", defaults: defaults)
            #expect(ObsidianVisibility.loadRecord(defaults: defaults) == ["folder:notes", "seen:notes", "root:/some/folder"])

            // Removing the vault forgets what was kept and reported for it,
            // and nothing else: the selected folder was kept on its own.
            ObsidianVisibility.forget(folderID: "notes", defaults: defaults)
            #expect(ObsidianVisibility.loadRecord(defaults: defaults) == ["root:/some/folder"])
        }
    }
}

/// The same classification read from real directories.
@Suite("Existing installs — read from real folders (#270)", .serialized)
struct Issue270ObsidianVisibilityLiveTests {

    private let sandbox: URL
    private let suiteName = "issue270-visibility-live-\(UUID().uuidString)"

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

    @Test("The reported install: a vault collapsed into Obsidian's folder stays reported until it has a folder of its own")
    func collapsedVaultOnDisk() throws {
        defer { try? FileManager.default.removeItem(at: sandbox) }
        let defaults = try #require(UserDefaults(suiteName: suiteName))
        defer { defaults.removePersistentDomain(forName: suiteName) }
        // The vault has synced with a peer: what the folder holds is the share.
        let finishedSyncing = true
        func live(_ folders: [(id: String, path: String)], root: URL) -> [ObsidianVisibility.Finding] {
            ObsidianVisibility.findingsLive(
                selectedRoot: root.path,
                folders: folders,
                mayRemember: { _ in finishedSyncing },
                defaults: defaults
            )
        }

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
        let collapsed = live([(id: "notes", path: root.path)], root: root)
        #expect(collapsed.map(\.kind) == [.vaultInSelectedFolderItself])
        #expect(collapsed.first?.folderIDs == ["notes"])

        // Missing the vault, the user opens "Daily" in Obsidian, which
        // writes a .obsidian into it. By content the folder now reads as a
        // container — and nothing was fixed. The notice stays.
        try FileManager.default.createDirectory(
            at: root.appendingPathComponent("Daily/.obsidian", isDirectory: true),
            withIntermediateDirectories: true
        )
        #expect(ObsidianVisibility.liveContent(root.path)?.hasVaultSubfolders == true)
        #expect(live([(id: "notes", path: root.path)], root: root).map(\.folderIDs) == [["notes"]])

        // The way out, done by the user: remove, accept again — the vault
        // now syncs in a folder of its own, the leftovers still lie around.
        ObsidianVisibility.forget(folderID: "notes", defaults: defaults)
        let ownFolder = root.appendingPathComponent("Notes", isDirectory: true)
        try FileManager.default.createDirectory(at: ownFolder, withIntermediateDirectories: true)
        #expect(live([(id: "notes", path: ownFolder.path)], root: root).isEmpty)
    }

    @Test("A whole-folder sync whose vaults have not arrived yet reads like a single vault — reported for now, never remembered")
    func transientReadingIsNotRemembered() throws {
        defer { try? FileManager.default.removeItem(at: sandbox) }
        let defaults = try #require(UserDefaults(suiteName: suiteName))
        defer { defaults.removePersistentDomain(forName: suiteName) }
        func live(_ root: URL, finishedSyncing: Bool) -> [ObsidianVisibility.Finding] {
            ObsidianVisibility.findingsLive(
                selectedRoot: root.path,
                folders: [(id: "all", path: root.path)],
                mayRemember: { _ in finishedSyncing },
                defaults: defaults
            )
        }
        let relative = "Containers/Data/Application/\(UUID().uuidString)/Documents"
        let root = try makeDirectory(relative)
        // A stray .obsidian in Obsidian's folder; the share that syncs into
        // it is a folder of vaults, and none of them has arrived yet.
        _ = try makeDirectory(relative + "/.obsidian")
        #expect(live(root, finishedSyncing: false).map(\.kind) == [.vaultInSelectedFolderItself])
        #expect(ObsidianVisibility.loadRecord(defaults: defaults).isEmpty)

        // The vaults arrive: it is the setup Obsidian shows, and the notice
        // goes — it was never written down.
        _ = try makeDirectory(relative + "/Personal/.obsidian")
        #expect(live(root, finishedSyncing: true).isEmpty)
        #expect(ObsidianVisibility.loadRecord(defaults: defaults).isEmpty)
    }

    @Test("Obsidian's folder with the vaults inside is read as a container; a folder that is gone is not read at all")
    func containerAndMissingFolderOnDisk() throws {
        defer { try? FileManager.default.removeItem(at: sandbox) }
        let defaults = try #require(UserDefaults(suiteName: suiteName))
        defer { defaults.removePersistentDomain(forName: suiteName) }
        let relative = "Containers/Data/Application/\(UUID().uuidString)/Documents"
        let root = try makeDirectory(relative)
        _ = try makeDirectory(relative + "/.obsidian")
        _ = try makeDirectory(relative + "/Personal/.obsidian")

        #expect(ObsidianVisibility.liveContent(root.path) == .init(hasOwnConfig: true, hasVaultSubfolders: true))
        #expect(ObsidianVisibility.findingsLive(
            selectedRoot: root.path,
            folders: [(id: "all", path: root.path)],
            mayRemember: { _ in true },
            defaults: defaults
        ).isEmpty)
        // Nothing was reported, so nothing is remembered.
        #expect(ObsidianVisibility.loadRecord(defaults: defaults).isEmpty)

        #expect(ObsidianVisibility.liveContent(root.path + "-gone") == nil)
    }
}

/// How the findings reach the user: the issue cards and the status tier.
@Suite("Existing installs — the notice and its tier (#270)")
struct Issue270ObsidianVisibilityIssueTests {

    /// The steps must not lean on a status that proves less than it seems to,
    /// and must not ask for the earlier copy to be deleted: it may hold the
    /// only version of a note written on the iPhone.
    private func expectSafeSteps(_ issue: SyncthingManager.SyncIssueItem) {
        let text = (issue.message + " " + issue.remediation).lowercased()
        #expect(!text.contains("up to date"))
        #expect(!text.contains("delete"))
    }

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
        let check = try #require(issue.remediation.range(of: "another device has everything you changed"))
        let remove = try #require(issue.remediation.range(of: "remove the vault here"))
        let accept = try #require(issue.remediation.range(of: "Accept it again under Pending Shares"))
        #expect(check.upperBound <= remove.lowerBound)
        #expect(remove.upperBound <= accept.lowerBound)
        expectSafeSteps(issue)
    }

    @Test("A selected folder that is not Obsidian's own, with vaults syncing there: remove first, the folder comes second")
    func selectedFolderWithVaultsIssue() throws {
        let issue = try #require(SyncthingManager.obsidianVisibilityIssues(
            findings: [.init(kind: .selectedFolderNotAnAppFolder, folderIDs: ["a", "b"], tokens: ["root:/x"])],
            labels: ["a": "Notes"]
        ).first)
        #expect(issue.kind == .selectedFolderNotObsidiansOwn)
        #expect(issue.severity == .warning)
        // The view offers the folder picker only at count 0.
        #expect(issue.count == 2)
        // A vault without a label is named by its ID.
        #expect(issue.message.contains("Notes, b"))
        let remove = try #require(issue.remediation.range(of: "remove the vaults here"))
        let select = try #require(issue.remediation.range(of: "Select Obsidian's Folder"))
        let accept = try #require(issue.remediation.range(of: "Accept the shares again"))
        #expect(remove.upperBound <= select.lowerBound)
        #expect(select.upperBound <= accept.lowerBound)
        expectSafeSteps(issue)
    }

    @Test("A selected folder that is not Obsidian's own, with nothing syncing there: the one step is selecting Obsidian's folder")
    func selectedFolderWithoutVaultsIssue() throws {
        let withVaults = try #require(SyncthingManager.obsidianVisibilityIssues(
            findings: [.init(kind: .selectedFolderNotAnAppFolder, folderIDs: ["a"], tokens: ["root:/x"])],
            labels: [:]
        ).first)
        let empty = try #require(SyncthingManager.obsidianVisibilityIssues(
            findings: [.init(kind: .selectedFolderNotAnAppFolder, folderIDs: [], tokens: ["root:/x"])],
            labels: [:]
        ).first)
        #expect(empty.count == 0)
        #expect(empty.folderID == nil)
        #expect(empty.remediation.contains("Select Obsidian's Folder"))
        #expect(empty.remediation.contains("without opening one of your vaults"))
        #expect(!empty.remediation.contains("remove"))
        #expect(empty.id != withVaults.id)
        expectSafeSteps(empty)
    }

    @Test("The folder picker is offered only while nothing syncs in the selected folder; keeping only while something does")
    func whichButtonsANoticeCarries() throws {
        func issue(_ kind: ObsidianVisibility.Kind, _ folderIDs: [String]) throws -> SyncthingManager.SyncIssueItem {
            try #require(SyncthingManager.obsidianVisibilityIssues(
                findings: [.init(kind: kind, folderIDs: folderIDs, tokens: ["t"])],
                labels: [:]
            ).first)
        }
        // Vaults still sync in the old folder: selecting another one would
        // cut them off from it. Remove first — the notice says so.
        let syncing = try issue(.selectedFolderNotAnAppFolder, ["a"])
        #expect(!SyncthingManager.noticeOffersFolderPicker(syncing))
        #expect(SyncthingManager.noticeOffersKeeping(syncing))

        let empty = try issue(.selectedFolderNotAnAppFolder, [])
        #expect(SyncthingManager.noticeOffersFolderPicker(empty))
        #expect(!SyncthingManager.noticeOffersKeeping(empty))

        // The selected folder is the right one; only the vault's place is not.
        let collapsed = try issue(.vaultInSelectedFolderItself, ["a"])
        #expect(!SyncthingManager.noticeOffersFolderPicker(collapsed))
        #expect(SyncthingManager.noticeOffersKeeping(collapsed))
    }

    @Test("While the engine has not delivered its folder list, nothing is said about what syncs where")
    func pendingFolderList() {
        // Folders are on record, the engine lists none yet: "nothing syncs
        // here" would open the folder picker over vaults that do.
        #expect(SyncthingManager.folderListIsPending(engineFolderCount: 0, recordedFolderCount: 2))
        // Nothing on record: the install really has no vault.
        #expect(!SyncthingManager.folderListIsPending(engineFolderCount: 0, recordedFolderCount: 0))
        #expect(!SyncthingManager.folderListIsPending(engineFolderCount: 2, recordedFolderCount: 2))
        #expect(!SyncthingManager.folderListIsPending(engineFolderCount: 1, recordedFolderCount: 0))
    }

    @Test("No finding, no notice")
    func noFindingNoIssue() {
        #expect(SyncthingManager.obsidianVisibilityIssues(findings: [], labels: ["notes": "Notes"]).isEmpty)
    }

    @Test("The notice holds the header at Action Needed and the widget at its warning tier, across a background sync")
    func noticeReachesHeaderAndWidget() {
        let issues = SyncthingManager.obsidianVisibilityIssues(
            findings: [.init(kind: .selectedFolderNotAnAppFolder, folderIDs: ["a"], tokens: ["root:/x"])],
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
            $0 == .vaultInSelectedFolderItself || $0 == .selectedFolderNotObsidiansOwn
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
        #expect(!manager.selectedFolderHidesVaultsFromObsidian)

        manager._testSetObsidianVisibilityFindings([
            .init(kind: .vaultInSelectedFolderItself, folderIDs: ["notes"], tokens: ["folder:notes"]),
        ])
        #expect(noticeKinds(manager) == [.vaultInSelectedFolderItself])
        let issue = try #require(manager.unresolvedIssues.first { $0.kind == .vaultInSelectedFolderItself })
        #expect(issue.message.contains("My Notes"))
        #expect(issue.severity == .warning)
        // The selected folder is Obsidian's own: shares are accepted as ever.
        #expect(!manager.selectedFolderHidesVaultsFromObsidian)

        manager._testSetObsidianVisibilityFindings([
            .init(kind: .selectedFolderNotAnAppFolder, folderIDs: [], tokens: ["root:/x"]),
        ])
        #expect(noticeKinds(manager) == [.selectedFolderNotObsidiansOwn])
        #expect(manager.selectedFolderHidesVaultsFromObsidian)

        manager._testSetObsidianVisibilityFindings([])
        #expect(noticeKinds(manager).isEmpty)
        #expect(!manager.selectedFolderHidesVaultsFromObsidian)
    }
}

/// #270: no share goes into a folder Obsidian lists no vault from unless the
/// user taps for it — accepted automatically, it would be the report itself.
@MainActor
@Suite("Automatic accepts wait under a folder Obsidian cannot show (#270)")
struct Issue270AutomaticAcceptParkingTests {

    private static let offer = SyncthingManager.PendingFolderInfo(id: "notes", label: "Notes", offeredBy: [])

    @MainActor
    private final class Recorder {
        var accepts: [String] = []
        /// The selected folder is one Obsidian lists no vault from.
        var hides = true
    }

    private static func coordinator(hides: Bool, recorder: Recorder) -> ShareAcceptCoordinator {
        ShareAcceptCoordinator(environment: ShareAcceptCoordinator.Environment(
            settled: { true },
            vaultAccessible: { true },
            pendingFolders: { [offer] },
            autoAcceptEligible: { [offer] },
            accept: { folder, _ in
                recorder.accepts.append(folder.id)
                return .accepted
            },
            acceptIntoTarget: { _, _ in nil },
            unignorePendingFolder: { _ in },
            ignorePendingFolder: { _ in },
            selectedFolderHidesVaults: { hides }
        ))
    }

    @Test("The automatic pass parks the share with what to do, without an alert — and the user's own tap still accepts it")
    func parkedUntilTheUserDecides() throws {
        let recorder = Recorder()
        let c = Self.coordinator(hides: true, recorder: recorder)

        c.runAutomaticPass()
        c.runAutomaticPass()
        #expect(recorder.accepts.isEmpty)
        let parked = try #require(c.pendingShareFailures["notes"])
        #expect(parked.message.contains("not Obsidian's own folder"))
        #expect(parked.remediation.contains("Review and Accept"))
        // A decision waiting for the user, not an error.
        #expect(c.alertMessage == nil)

        c.retry(Self.offer)
        #expect(recorder.accepts == ["notes"])
        #expect(c.pendingShareFailures["notes"] == nil)
    }

    @Test("A parked share goes on by itself once the folder is Obsidian's own or the user keeps it")
    func parkedShareIsReleased() {
        let recorder = Recorder()
        let c = ShareAcceptCoordinator(environment: ShareAcceptCoordinator.Environment(
            settled: { true },
            vaultAccessible: { true },
            pendingFolders: { [Self.offer] },
            autoAcceptEligible: { [Self.offer] },
            accept: { folder, _ in
                recorder.accepts.append(folder.id)
                return .accepted
            },
            acceptIntoTarget: { _, _ in nil },
            unignorePendingFolder: { _ in },
            ignorePendingFolder: { _ in },
            selectedFolderHidesVaults: { recorder.hides }
        ))
        c.runAutomaticPass()
        #expect(recorder.accepts.isEmpty)
        #expect(c.pendingShareFailures["notes"] != nil)

        // "Keep It This Way", or another folder was selected.
        recorder.hides = false
        c.runAutomaticPass()
        #expect(recorder.accepts == ["notes"])
        #expect(c.pendingShareFailures["notes"] == nil)
    }

    @Test("A failure the user's own attempt recorded is not taken for parking and stays")
    func ownFailureIsNotReleased() {
        let recorder = Recorder()
        let c = ShareAcceptCoordinator(environment: ShareAcceptCoordinator.Environment(
            settled: { true },
            vaultAccessible: { true },
            pendingFolders: { [Self.offer] },
            autoAcceptEligible: { [Self.offer] },
            accept: { _, _ in .refused(message: "no room") },
            acceptIntoTarget: { _, _ in nil },
            unignorePendingFolder: { _ in },
            ignorePendingFolder: { _ in },
            selectedFolderHidesVaults: { recorder.hides }
        ))
        c.runAutomaticPass()
        // The user tries anyway, and the accept refuses for its own reason.
        c.retry(Self.offer)
        #expect(c.pendingShareFailures["notes"]?.message == "no room")

        recorder.hides = false
        c.runAutomaticPass()
        #expect(c.pendingShareFailures["notes"]?.message == "no room")
    }

    @Test("Under Obsidian's own folder, and under a folder the user keeps, shares are accepted as ever")
    func acceptedWhereObsidianShowsIt() {
        let recorder = Recorder()
        let c = Self.coordinator(hides: false, recorder: recorder)
        c.runAutomaticPass()
        #expect(recorder.accepts == ["notes"])
        #expect(c.pendingShareFailures.isEmpty)
    }
}

/// #270: what a refused share and the folder picker tell the user has to be
/// something the user can do on the install they have.
@Suite("The way out that is named is one that exists (#270)")
struct Issue270WayOutTests {

    private static let uuid = "0A1B2C3D-0000-4000-8000-000000000000"
    private static let obsidiansFolder = "/private/var/mobile/Containers/Data/Application/\(uuid)/Documents"
    private static let selfMadeObsidian = "/private/var/mobile/Containers/Shared/AppGroup/\(uuid)/File Provider Storage/Obsidian"

    private func refusal(root: String, rootIsVault: Bool, occupied: Set<String>) -> String? {
        let decision = VaultManager.resolveAcceptPath(
            manualTarget: nil,
            manualTargetConsented: false,
            rawRoot: root,
            rootIsVault: rootIsVault,
            folderName: "Recipes",
            occupiedCanonLower: occupied,
            mergeConfirmed: false,
            canonicalize: { $0 },
            listingFor: { _ in nil }
        )
        guard case .refused(let message) = decision else { return nil }
        return message
    }

    @Test("A vault collapsed into Obsidian's folder blocks a new share — the refusal says to give that vault its own folder, not to re-select the folder already selected")
    func collapsedVaultBlocksANewShare() throws {
        let root = Self.obsidiansFolder
        let message = try #require(refusal(root: root, rootIsVault: false, occupied: [root.lowercased()]))
        #expect(message.contains("Remove that vault on this iPhone and accept it again"))
        #expect(!message.contains("Re-select"))

        // The same through "Choose Vault…".
        let manual = VaultManager.validateManualTarget(
            rawRoot: root,
            rootIsVault: false,
            name: "Recipes",
            occupiedCanonLower: [root.lowercased()],
            targetEntries: nil,
            canonicalize: { $0 }
        )
        #expect(manual == .refused(message: message))
    }

    @Test("A selected folder that is itself a vault keeps the guidance to select the folder above it")
    func selectedFolderIsAVault() throws {
        let picked = Self.obsidiansFolder + "/MyVault"
        let byContent = try #require(refusal(root: picked, rootIsVault: true, occupied: []))
        #expect(byContent.contains("Re-select the folder that contains your vaults"))
        let bySync = try #require(refusal(root: picked, rootIsVault: false, occupied: [picked.lowercased()]))
        #expect(bySync == byContent)
    }

    @Test("The folder picker opens in Obsidian's folder to reconnect it, and one level above any other folder")
    func whereTheFolderPickerStarts() {
        func start(_ path: String) -> String? {
            ObsidianReconnectFlow.pickerStartURL(selectedFolder: URL(fileURLWithPath: path, isDirectory: true))?.path
        }
        #expect(start(Self.obsidiansFolder) == Self.obsidiansFolder)
        // The wrong folder is usually titled "Obsidian" itself: starting
        // inside it puts Open right there.
        #expect(start(Self.selfMadeObsidian) == (Self.selfMadeObsidian as NSString).deletingLastPathComponent)
        // A single vault picked as the folder: one level up is Obsidian's own.
        #expect(start(Self.obsidiansFolder + "/MyVault") == Self.obsidiansFolder)
        #expect(ObsidianReconnectFlow.pickerStartURL(selectedFolder: nil) == nil)
    }
}

/// #270: a manual target is a folder name. The consent it stands for was
/// given for one directory, under the folder that was selected then.
@Suite("A manual target is consent only under the folder it was chosen in (#270)")
struct Issue270ManualTargetConsentTests {

    private func resolve(
        consented: Bool,
        mergeConfirmed: Bool = false,
        occupied: Set<String> = [],
        listing: [String]?
    ) -> ShareTargetDecision {
        VaultManager.resolveAcceptPath(
            manualTarget: "Personal",
            manualTargetConsented: consented,
            rawRoot: "/Obsidian",
            rootIsVault: false,
            folderName: "Notes",
            occupiedCanonLower: occupied,
            mergeConfirmed: mergeConfirmed,
            canonicalize: { $0 },
            listingFor: { $0 == "/Obsidian/Personal" ? listing : nil }
        )
    }

    @Test("Chosen under this selected folder: the share returns to its place, earlier notes and all")
    func consentedTargetKeepsItsPlace() {
        #expect(resolve(consented: true, listing: ["note.md", ".stfolder"]) == .path("/Obsidian/Personal"))
    }

    @Test("Chosen under another selected folder: a directory of that name that holds files needs the user's confirmation")
    func unconsentedTargetWithFilesAsks() {
        // After the selected folder changed, "Personal" is another
        // directory — here someone else's vault.
        #expect(resolve(consented: false, listing: ["note.md"])
            == .requiresMergeConfirmation(path: "/Obsidian/Personal", targetName: "Personal"))
        // Confirmed by the user: accepted, like any merge they decided.
        #expect(resolve(consented: false, mergeConfirmed: true, listing: ["note.md"]) == .path("/Obsidian/Personal"))
    }

    @Test("Chosen under another selected folder: an empty or missing directory of that name is taken without asking")
    func unconsentedEmptyTargetIsTaken() {
        #expect(resolve(consented: false, listing: nil) == .path("/Obsidian/Personal"))
        #expect(resolve(consented: false, listing: []) == .path("/Obsidian/Personal"))
        #expect(resolve(consented: false, listing: [".obsidian"]) == .path("/Obsidian/Personal"))
    }

    @Test("An overlap is refused before any question of consent")
    func overlapStillRefuses() {
        for consented in [false, true] {
            guard case .refused = resolve(consented: consented, occupied: ["/obsidian/personal"], listing: ["note.md"]) else {
                Issue.record("an overlapping manual target must be refused")
                continue
            }
        }
    }

    @Test("The selected folder is identified the way paths are compared everywhere else")
    func rootKeyIsCanonicalAndLowercased() {
        #expect(ManualShareTargetStore.rootKey("/tmp/Some/Folder/") == ManualShareTargetStore.rootKey("/tmp/some/folder"))
        #expect(ManualShareTargetStore.rootKey("/tmp/a") != ManualShareTargetStore.rootKey("/tmp/b"))
    }
}

/// #270: a pending share says where it will arrive before it is accepted.
@Suite("Pending shares — where the vault will arrive (#270)")
struct Issue270PendingShareDestinationTests {

    private static let root = "/private/var/mobile/Containers/Data/Application/0A1B2C3D-0000-4000-8000-000000000000/Documents"

    @Test("A share headed for its own folder in Obsidian's folder names that folder")
    func ownFolder() {
        let line = PendingShareDestination.line(decision: .path(Self.root + "/Recipes"), rootPlace: .appFolder)
        #expect(line?.isCaution == false)
        #expect(line?.text.contains("Recipes") == true)
        #expect(line?.text.contains("your Obsidian folder") == true)
    }

    @Test("A name clash shows the folder the share really gets")
    func suffixedFolder() {
        let line = PendingShareDestination.line(decision: .path(Self.root + "/Recipes (2)"), rootPlace: .appFolder)
        #expect(line?.text.contains("Recipes (2)") == true)
    }

    @Test("Under any selected folder that is not Obsidian's own, the line says that Obsidian will not list the vault")
    func notObsidiansOwnFolder() {
        // Outside every app's folder, and inside one: a share below a folder
        // in Obsidian's folder arrives as a folder inside it, not as a vault.
        for place in [VaultManager.SelectedFolderPlace.elsewhere, .insideAppFolder] {
            let line = PendingShareDestination.line(decision: .path("/selected/Recipes"), rootPlace: place)
            #expect(line?.isCaution == true)
            #expect(line?.text.contains("Recipes") == true)
            #expect(line?.text.contains("will not list it as a vault") == true)
            #expect(line?.text.contains("your Obsidian folder") == false)
        }
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

    @Test("A folder that already holds files under a folder Obsidian cannot show keeps both warnings")
    func existingFolderWhereObsidianCannotShowIt() {
        for place in [VaultManager.SelectedFolderPlace.elsewhere, .insideAppFolder] {
            let line = PendingShareDestination.line(
                decision: .requiresMergeConfirmation(path: "/selected/Recipes", targetName: "Recipes"),
                rootPlace: place,
                rootHoldsEarlierCopy: true
            )
            #expect(line?.isCaution == true)
            #expect(line?.text.contains("already holds files") == true)
            #expect(line?.text.contains("will not list a vault") == true)
        }
    }

    @Test("While the earlier copy still lies in Obsidian's folder, a target that holds files is most likely part of it: do not combine")
    func existingFolderNextToTheEarlierCopy() {
        // The vault "Notes" was collapsed into Obsidian's folder and has a
        // top-level folder "Notes". Removed and accepted again, the share
        // resolves to that subfolder.
        let line = PendingShareDestination.line(
            decision: .requiresMergeConfirmation(path: Self.root + "/Notes", targetName: "Notes"),
            rootPlace: .appFolder,
            rootHoldsEarlierCopy: true
        )
        #expect(line?.isCaution == true)
        #expect(line?.text.contains("do not combine") == true)
        #expect(line?.text.contains("Choose Vault…") == true)
    }

    @Test("No line while the folder is not connected or the accept would refuse")
    func nothingToSay() {
        #expect(PendingShareDestination.line(decision: nil, rootPlace: nil) == nil)
        #expect(PendingShareDestination.line(decision: .refused(message: "no"), rootPlace: .appFolder) == nil)
        #expect(PendingShareDestination.line(decision: .refused(message: "no"), rootPlace: .elsewhere) == nil)
    }
}
