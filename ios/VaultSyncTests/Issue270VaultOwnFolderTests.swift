import Foundation
import Testing
@testable import VaultSync

/// #270: a share must land where Obsidian lists it as a vault — in its own
/// direct subfolder of the picked folder, never in the picked folder itself.
///
/// The roots are real directories shaped like the ones the folder picker
/// returns (read on a device and on the iOS 18 and 26 simulators): Obsidian's
/// folder is an app's `…/Containers/Data/Application/<UUID>/Documents`, a
/// folder the user made under "On My iPhone" is
/// `…/File Provider Storage/<name>`.
@MainActor
@Suite("A vault always gets its own folder (#270)", .serialized)
struct Issue270VaultOwnFolderTests {

    private let sandbox: URL

    init() throws {
        sandbox = FileManager.default.temporaryDirectory
            .appendingPathComponent("issue270-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: sandbox, withIntermediateDirectories: true)
    }

    /// `…/Containers/Data/Application/<UUID>/Documents` — what the picker
    /// returns for "On My iPhone" → "Obsidian".
    private func makeAppDocumentsRoot() throws -> URL {
        let root = sandbox
            .appendingPathComponent("Containers/Data/Application/\(UUID().uuidString)/Documents", isDirectory: true)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        return root
    }

    /// `…/File Provider Storage/<name>` — a folder the user created under
    /// "On My iPhone".
    private func makeUserFolder(named name: String) throws -> URL {
        let root = sandbox
            .appendingPathComponent("Containers/Shared/AppGroup/\(UUID().uuidString)/File Provider Storage/\(name)", isDirectory: true)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        return root
    }

    private func addVaultConfig(to directory: URL) throws {
        try FileManager.default.createDirectory(
            at: directory.appendingPathComponent(".obsidian", isDirectory: true),
            withIntermediateDirectories: true
        )
    }

    /// The accept's own decision for a share arriving now, made by the same
    /// code path the accept runs (`sharePlacement`).
    private func destination(root: URL, label: String) -> ShareTargetDecision? {
        let vaultManager = VaultManager()
        vaultManager._testSetAccess(accessible: true, detectedVaults: [], obsidianDirectoryURL: root)
        return vaultManager.previewShareDestination(
            folderID: "issue270-\(UUID().uuidString)",
            label: label,
            syncthingManager: SyncthingManager()
        )
    }

    private func child(_ root: URL, _ name: String) -> ShareTargetDecision {
        .path((root.path as NSString).appendingPathComponent(name))
    }

    private func isRefused(_ decision: ShareTargetDecision?) -> Bool {
        if case .refused = decision { return true }
        return false
    }

    @Test("A vault named like Obsidian's folder on disk (\"Documents\") still gets its own folder")
    func labelEqualToTheFolderNameGetsItsOwnFolder() throws {
        let root = try makeAppDocumentsRoot()
        #expect(destination(root: root, label: "Documents") == child(root, "Documents"))
        // Case-folding APFS: the comparison that collapsed was case-insensitive.
        #expect(destination(root: root, label: "documents") == child(root, "documents"))
    }

    @Test("A vault labelled \"Obsidian\" gets its own folder in Obsidian's folder")
    func labelObsidianGetsItsOwnFolder() throws {
        let root = try makeAppDocumentsRoot()
        #expect(destination(root: root, label: "Obsidian") == child(root, "Obsidian"))
    }

    @Test("A stray .obsidian in Obsidian's folder does not make that folder the vault")
    func strayConfigInObsidiansFolderKeepsItAContainer() throws {
        let root = try makeAppDocumentsRoot()
        try addVaultConfig(to: root)
        #expect(destination(root: root, label: "Notes") == child(root, "Notes"))
    }

    @Test("A folder the user named \"Obsidian\" never takes a vault labelled \"Obsidian\" into itself")
    func selfMadeObsidianFolderNeverTakesTheVaultItself() throws {
        let root = try makeUserFolder(named: "Obsidian")
        #expect(destination(root: root, label: "Obsidian") == child(root, "Obsidian"))
    }

    @Test("A picked folder that is itself a vault refuses the share instead of syncing into it")
    func pickedVaultRefusesTheShare() throws {
        // A vault inside Obsidian's folder, picked instead of the folder above it.
        let insideObsidian = try makeAppDocumentsRoot().appendingPathComponent("MyVault", isDirectory: true)
        try addVaultConfig(to: insideObsidian)
        #expect(isRefused(destination(root: insideObsidian, label: "Notes")))

        // A vault kept in a folder the user made.
        let userVault = try makeUserFolder(named: "MyVault")
        try addVaultConfig(to: userVault)
        #expect(isRefused(destination(root: userVault, label: "Notes")))
    }

    @Test("A new manual target under a picked vault is refused and not offered; a recorded one keeps its place")
    func manualTargetsUnderAPickedVault() throws {
        let pickedVault = try makeUserFolder(named: "MyVault")
        try addVaultConfig(to: pickedVault)
        // An empty folder inside the vault: under a container it would be an
        // eligible target.
        try FileManager.default.createDirectory(
            at: pickedVault.appendingPathComponent("Empty", isDirectory: true),
            withIntermediateDirectories: false
        )
        let vaultManager = VaultManager()
        vaultManager._testSetAccess(accessible: true, detectedVaults: [], obsidianDirectoryURL: pickedVault)
        let syncthingManager = SyncthingManager()

        // First-time choice: refused before anything reaches the engine.
        let refusal = vaultManager.acceptPendingShare(
            folder: SyncthingManager.PendingFolderInfo(id: "issue270-\(UUID().uuidString)", label: "Notes", offeredBy: []),
            intoTargetNamed: "Notes",
            syncthingManager: syncthingManager
        )
        #expect(refusal?.contains("contains your vaults") == true)
        #expect(!FileManager.default.fileExists(atPath: pickedVault.appendingPathComponent("Notes").path))
        #expect(vaultManager.eligibleShareTargets(syncthingManager: syncthingManager).isEmpty)

        // A target recorded earlier (#52) is where this share lives.
        let recorded = "issue270-\(UUID().uuidString)"
        ManualShareTargetStore.setTarget("Life", forFolder: recorded)
        #expect(
            vaultManager.previewShareDestination(folderID: recorded, label: "Notes", syncthingManager: syncthingManager)
                == child(pickedVault, "Life")
        )
    }

    @Test("A recorded target keeps its place when the share's label stops naming a folder")
    func recordedTargetSurvivesAnUnusableLabel() throws {
        let root = try makeAppDocumentsRoot()
        let recorded = "issue270-\(UUID().uuidString)"
        ManualShareTargetStore.setTarget("Life", forFolder: recorded)
        let vaultManager = VaultManager()
        vaultManager._testSetAccess(accessible: true, detectedVaults: [], obsidianDirectoryURL: root)
        for label in [".", "..", " . "] {
            #expect(
                vaultManager.previewShareDestination(folderID: recorded, label: label, syncthingManager: SyncthingManager())
                    == child(root, "Life"),
                "label \(label.debugDescription)"
            )
        }
    }

    @Test("An empty container places the first vault in its own folder")
    func emptyContainerPlacesTheVaultInItsOwnFolder() throws {
        let root = try makeAppDocumentsRoot()
        #expect(destination(root: root, label: "Notes") == child(root, "Notes"))
    }

    @Test("A label of \".\" or \"..\" is refused: it names the selected folder or the one above it")
    func dotLabelsAreRefused() throws {
        let root = try makeAppDocumentsRoot()
        for label in [".", "..", " . ", "\n..\n"] {
            #expect(isRefused(destination(root: root, label: label)), "label \(label.debugDescription)")
        }
        // A name that merely starts with dots is a folder like any other.
        #expect(destination(root: root, label: "...") == child(root, "..."))
        #expect(destination(root: root, label: ".notes") == child(root, ".notes"))
    }
}

/// The pure rules behind the placement (#270) — no filesystem, no bridge.
@Suite("Selected-folder classification and folder names (#270)")
struct Issue270ClassificationTests {

    private let identity: (String) -> String = { $0 }

    @Test("An app's own folder is recognized in every form the picker returns it")
    func appDocumentsRootIsRecognized() {
        let uuid = "0A1B2C3D-0000-4000-8000-000000000000"
        // "On My iPhone" → the app, as stored on a device (iOS 27).
        #expect(VaultManager.isAppDocumentsRoot("/private/var/mobile/Containers/Data/Application/\(uuid)/Documents"))
        #expect(VaultManager.isAppDocumentsRoot("/var/mobile/Containers/Data/Application/\(uuid)/Documents"))
        // The same folder on a simulator (iOS 18.0 and 26.5).
        #expect(VaultManager.isAppDocumentsRoot(
            "/Users/dev/Library/Developer/CoreSimulator/Devices/\(uuid)/data/Containers/Data/Application/\(uuid)/Documents"
        ))
        // The app's folder in iCloud Drive.
        #expect(VaultManager.isAppDocumentsRoot("/private/var/mobile/Library/Mobile Documents/iCloud~md~obsidian/Documents"))
    }

    @Test("Everything else is not an app's own folder")
    func otherFoldersAreNotAppDocumentsRoots() {
        let uuid = "0A1B2C3D-0000-4000-8000-000000000000"
        let others = [
            // A folder the user made under "On My iPhone" and named "Obsidian".
            "/private/var/mobile/Containers/Shared/AppGroup/\(uuid)/File Provider Storage/Obsidian",
            // …or named "Documents".
            "/private/var/mobile/Containers/Shared/AppGroup/\(uuid)/File Provider Storage/Documents",
            // A vault inside the app's folder, and the container above it.
            "/private/var/mobile/Containers/Data/Application/\(uuid)/Documents/MyVault",
            "/private/var/mobile/Containers/Data/Application/\(uuid)",
            "/private/var/mobile/Containers/Data/Application/\(uuid)/Library",
            // Not a container ID.
            "/private/var/mobile/Containers/Data/Application/Obsidian/Documents",
            // iCloud Drive's own "Documents" folder and a vault in iCloud.
            "/private/var/mobile/Library/Mobile Documents/com~apple~CloudDocs/Documents",
            "/private/var/mobile/Library/Mobile Documents/iCloud~md~obsidian/Documents/MyVault",
            "/Documents",
            "/",
            ""
        ]
        for path in others {
            #expect(!VaultManager.isAppDocumentsRoot(path), "\(path)")
        }
    }

    @Test("An app's own folder is a container whatever it holds; other folders keep the #79 rule")
    func appDocumentsRootIsNeverAVault() {
        for hasOwnConfig in [false, true] {
            for hasVaultSubfolders in [false, true] {
                #expect(!VaultManager.rootIsItselfVault(
                    isAppDocumentsRoot: true,
                    hasOwnConfig: hasOwnConfig,
                    hasVaultSubfolders: hasVaultSubfolders
                ))
            }
        }
        #expect(VaultManager.rootIsItselfVault(isAppDocumentsRoot: false, hasOwnConfig: true, hasVaultSubfolders: false))
    }

    @Test("A folder name is usable unless it is empty, \".\" or \"..\"")
    func usableDirectoryNames() {
        for name in ["", ".", ".."] {
            #expect(!VaultManager.isUsableDirectoryName(name), "\(name.debugDescription)")
        }
        for name in ["Notes", "...", ".notes", "a.b", "Notes (2)"] {
            #expect(VaultManager.isUsableDirectoryName(name), "\(name)")
        }
    }

    @Test("A manually chosen target named \".\" or \"..\" is refused")
    func manualTargetDotNamesAreRefused() {
        for name in [".", "..", " .. "] {
            let decision = VaultManager.validateManualTarget(
                rawRoot: "/Obsidian",
                rootIsVault: false,
                name: name,
                occupiedCanonLower: [],
                targetEntries: nil,
                canonicalize: identity
            )
            guard case .refused = decision else {
                Issue.record("manual target \(name.debugDescription) must be refused, got \(decision)")
                continue
            }
        }
    }
}

extension EngineBridgeSuites {
    /// #270 through every layer: the app's own accept against the real engine.
    /// The mapping decides `root/<name>`, the bridge is handed the selected
    /// folder, and the Go floor accepts into its direct subfolder.
    @Suite("A real accept lands in a direct subfolder of the selected folder (#270)")
    struct Issue270AcceptThroughTheBridgeTests {

        @MainActor
        @Test("Obsidian's folder with a leftover .obsidian: a vault labelled like the folder, and a manually placed one, each get their own folder")
        func acceptsLandInTheirOwnFolders() throws {
            TestSupport.resetSyncthingState()
            BackgroundSyncService.lifecycleLock.withLock { $0.releaseForeground() }
            #expect(SyncBridgeService.startSyncthing(configDir: TestSupport.syncthingConfigPath()) == nil)

            let syncthingManager = SyncthingManager()
            #expect(syncthingManager.adoptRunningEngine())

            let labelled = "issue270-\(UUID().uuidString)"
            let placed = "issue270-\(UUID().uuidString)"
            let sandbox = FileManager.default.temporaryDirectory
                .appendingPathComponent("issue270-\(UUID().uuidString)", isDirectory: true)
            defer {
                syncthingManager.stop()
                TestSupport.resetSyncthingState()
                FolderPathReconciler.removeRel(forFolder: labelled)
                FolderPathReconciler.removeRel(forFolder: placed)
                try? FileManager.default.removeItem(at: sandbox)
            }

            // Both conditions that used to sync into the root itself: the
            // root's name on disk equals the label, and the root holds a
            // `.obsidian/` and no vault subfolder.
            let root = sandbox
                .appendingPathComponent("Containers/Data/Application/\(UUID().uuidString)/Documents", isDirectory: true)
            try FileManager.default.createDirectory(
                at: root.appendingPathComponent(".obsidian", isDirectory: true),
                withIntermediateDirectories: true
            )
            let vaultManager = VaultManager()
            vaultManager._testSetAccess(accessible: true, detectedVaults: [], obsidianDirectoryURL: root)

            func syncedPath(of folderID: String) -> String? {
                syncthingManager.folders.first { $0.id == folderID }
                    .map { FolderPathReconciler.canonical($0.path) }
            }
            func canonicalChild(_ name: String) -> String {
                FolderPathReconciler.canonical(root.appendingPathComponent(name, isDirectory: true).path)
            }

            let outcome = vaultManager.acceptPendingShare(
                folder: SyncthingManager.PendingFolderInfo(id: labelled, label: "Documents", offeredBy: []),
                syncthingManager: syncthingManager,
                mergeConfirmed: false
            )
            #expect(outcome == .accepted)
            #expect(syncedPath(of: labelled) == canonicalChild("Documents"))
            #expect(FolderPathReconciler.loadRel()[labelled] == "Documents")

            let manualError = vaultManager.acceptPendingShare(
                folder: SyncthingManager.PendingFolderInfo(id: placed, label: "Elsewhere", offeredBy: []),
                intoTargetNamed: "Chosen",
                syncthingManager: syncthingManager
            )
            #expect(manualError == nil)
            #expect(syncedPath(of: placed) == canonicalChild("Chosen"))

            // The engine's marker sits in each vault's own folder — the
            // selected folder itself was never made a sync folder.
            #expect(FileManager.default.fileExists(atPath: root.appendingPathComponent("Documents/.stfolder").path))
            #expect(!FileManager.default.fileExists(atPath: root.appendingPathComponent(".stfolder").path))
        }
    }
}
