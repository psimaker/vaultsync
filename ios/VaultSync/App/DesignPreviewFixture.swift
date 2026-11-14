import Foundation

#if DEBUG
/// LAB: the design-preview fixture (#187). `-uiaudit-fixture design-preview`
/// seeds a representative, entirely fictional state — two vaults, three
/// devices, and per screen a conflict or a pending share — so every
/// redesigned screen can be captured on a simulator in light and dark without
/// a paired peer. `-design-preview-screen <name>` picks the screen.
///
/// Like every UI-audit fixture it runs with engine management disabled (see
/// `UIAuditFixture`): nothing here starts the engine, touches a bookmark, or
/// writes to disk, and paths never settle, so the automatic share-accept pass
/// stays parked (decision 008). Compiled out of release builds.
enum DesignPreviewFixture {
    enum Screen: String {
        /// Everything synced — the calm home screen.
        case home
        /// A conflict and a pending share waiting on the user.
        case attention
        /// An install connected to a folder that is not Obsidian's own
        /// (#270): the notice with its way out, and a pending share that
        /// says where it would arrive.
        case outsideObsidian = "outside-obsidian"
        /// A vault whose folder is gone, with a copy of it found under the
        /// selected folder (#315): the card that says so and names the way
        /// on.
        case earlierCopy = "earlier-copy"
        /// One vault mid-transfer.
        case syncing
        case devices
        case device
        case vault
        case conflicts
        case conflict
        case relay
        case settings
        /// First run: Obsidian connected, no device yet.
        case onboarding
        /// First run: Obsidian connected and a device added — the share
        /// from the computer is the next step.
        case onboardingShare = "onboarding-share"
        /// The Add Hub sheet (#174) over a first pairing — no device, no
        /// vault yet. `-design-preview-add-hub-step code|code-found|code-keyboard|vault|done`
        /// picks the step (default: code, the search still running;
        /// code-keyboard focuses the number field to show the number pad).
        case addHub = "add-hub"
    }

    /// The Add Hub step a design-preview run shows.
    static var addHubStep: String {
        UserDefaults.standard.string(forKey: "design-preview-add-hub-step") ?? "code"
    }

    /// The code step with the number field focused: the number pad and the
    /// Done above it.
    static var addHubShowsKeyboard: Bool {
        addHubStep == "code-keyboard"
    }

    /// `-design-preview-line-diff YES` opens the resolve screen with the
    /// line-by-line comparison switched on.
    static var showsLineDiff: Bool {
        UserDefaults.standard.bool(forKey: "design-preview-line-diff")
    }

    static var screen: Screen {
        UserDefaults.standard.string(forKey: "design-preview-screen")
            .flatMap(Screen.init(rawValue:)) ?? .home
    }

    static let notesID = "design-notes"
    static let workID = "design-work"
    static let serverID = "P7DHKQX-2LZC4VN-YB5TQ3M-W6KR2JD-HS4NFXA-9UEQ7CL-MVZ3TBW-K2RX8QA"
    static let macBookID = "J4QW2ZT-8NDKX3R-LC7VYHP-5MQB2TS-W9FRK4D-ZX6GTN2-QH3LPV8-B7YMC5E"
    static let iPadID = "T2MXH7R-WQ4ZLK9-D3NPC6V-8YBGJ5S-K7FRX2Q-HM9VTD4-ZC6LQW3-P5NJB8R"
    /// An app's own folder, in the form the folder picker hands out
    /// Obsidian's (#270) — under a path of another shape every pending
    /// share would say that Obsidian cannot show it.
    private static let obsidianRoot = "/DesignPreview/Containers/Data/Application/5B2E0C4A-9D17-4E6B-A3F1-7C8D2E4F6A1B/Documents"
    /// A folder the user made under "On My iPhone" and named "Obsidian".
    private static let selfMadeRoot = "/DesignPreview/Containers/Shared/AppGroup/0F3C5A7E-2B4D-4C6E-8A1F-9E7D5C3B1A20/File Provider Storage/Obsidian"

    private static var root: String {
        screen == .outsideObsidian ? selfMadeRoot : obsidianRoot
    }

    static let conflict = SyncthingManager.ConflictInfo(
        originalPath: "Daily/2026-10-05.md",
        conflictPath: "Daily/2026-10-05.sync-conflict-20261005-091012-J4QW2ZT.md",
        conflictDate: "20261005-091012",
        deviceShortID: "J4QW2ZT"
    )

    /// Note bodies the conflict screen shows instead of a bridge read.
    static let conflictLocalText = """
        # Monday

        - [x] Call the bank
        - [ ] Draft the Hub post
        - [ ] Water the plants
        """
    static let conflictRemoteText = """
        # Monday

        - [ ] Call the bank
        - [x] Draft the Hub post
        - [ ] Water the plants
        - [ ] Buy coffee
        """

    @MainActor
    static func seed(syncthingManager: SyncthingManager, vaultManager: VaultManager) {
        let screen = self.screen
        let firstRun = screen == .onboarding || screen == .onboardingShare || screen == .addHub
        vaultManager._testSetAccess(
            accessible: true,
            detectedVaults: firstRun ? [] : ["Notes", "Work"],
            obsidianDirectoryURL: URL(fileURLWithPath: root, isDirectory: true)
        )
        syncthingManager._testSetRunning(true)
        syncthingManager._testSetLastBackgroundSyncOutcome(nil)
        guard !firstRun else {
            syncthingManager._testSetFolders([])
            syncthingManager._testApplyDeviceList(screen == .onboardingShare ? devices : [])
            return
        }

        syncthingManager._testSetFolders([
            folder(id: notesID, label: "Notes", deviceIDs: [serverID, macBookID]),
            folder(id: workID, label: "Work", deviceIDs: [serverID]),
        ])
        let workStatus: SyncthingManager.FolderStatusInfo
        switch screen {
        case .syncing:
            workStatus = status(state: "syncing", pct: 63, files: 307, bytes: 9_437_184, need: 3)
        case .earlierCopy:
            workStatus = status(state: "error", pct: 100, files: 310, bytes: 9_437_184, need: 0, errorReason: "folder_path_missing")
        default:
            workStatus = status(state: "idle", pct: 100, files: 310, bytes: 9_437_184, need: 0)
        }
        syncthingManager._testSetFolderStatuses([
            notesID: status(state: "idle", pct: 100, files: 1284, bytes: 44_040_192, need: 0),
            workID: workStatus,
        ])
        if screen == .earlierCopy {
            // Only a vault with a known place under the selected folder is
            // ever looked for there, so the card comes with its mapping.
            FolderPathReconciler._previewSetRel([workID: "Work"])
            syncthingManager._testSetFoldersWithoutTheirStorage([workID])
        }
        syncthingManager._testApplyDeviceList(devices)
        let now = Date()
        syncthingManager._testSetLastSyncTimes(
            global: now.addingTimeInterval(-120),
            byFolder: [notesID: now.addingTimeInterval(-120), workID: now.addingTimeInterval(-3_600)]
        )
        let withConflict: Set<Screen> = [.attention, .vault, .conflicts, .conflict]
        syncthingManager._testSetConflictFiles(withConflict.contains(screen) ? [notesID: [conflict]] : [:])
        // The real classification over the seeded state, without the disk
        // and without what an earlier run chose to keep.
        syncthingManager._testSetObsidianVisibilityFindings(ObsidianVisibility.findings(
            selectedRoot: root,
            folders: syncthingManager.folders.map { (id: $0.id, path: $0.path) },
            env: ObsidianVisibility.Environment(
                canonicalize: { $0 },
                content: { _ in nil },
                record: { [] }
            )
        ))
        syncthingManager._testSetPendingFolders(screen == .attention || screen == .outsideObsidian ? [
            SyncthingManager.PendingFolderInfo(
                id: "design-recipes",
                label: "Recipes",
                offeredBy: [
                    SyncthingManager.PendingDeviceInfo(
                        deviceID: serverID,
                        name: "Home Server",
                        time: "2026-10-05T09:00:00Z"
                    ),
                ]
            ),
        ] : [])
    }

    /// The Add Hub flow at the requested step, without a network: a Hub
    /// that answered the search, the canvas's code and catalog.
    @MainActor
    static func hubPairingModel(syncthingManager: SyncthingManager) -> HubPairingModel {
        let notes = HubVault(id: "vs-4f2a91c07b3e", label: "Notes", files: 1_284, devices: 2)
        let work = HubVault(id: "vs-9c1d22aa0e57", label: "Work", files: 310, devices: 1)
        let hello = HubHello(hubName: "VaultSync Hub", hubDeviceID: serverID, catalogAvailable: true, vaults: [notes, work])
        let model = HubPairingModel(environment: HubPairingModel.Environment(
            begin: { "design-preview" },
            end: { _ in },
            normalizeCode: { SyncBridgeService.hubPairingNormalizeCode($0) },
            discover: { _ in .success([HubCandidate(address: "192.168.1.20:8390", name: "VaultSync Hub")]) },
            handshake: { _, _, _ in .success(hello) },
            provision: { _, _, _ in .success(notes) },
            register: { _, _ in .success(()) },
            engineRunning: { true },
            localFolderIDs: { [] },
            deviceName: { "iPhone" }
        ))
        let fields = HubCodeFields(first: "TULIP", second: "ANCHOR", number: "42")
        let found = HubPairingModel.Discovery.found([HubCandidate(address: "192.168.1.20:8390", name: "VaultSync Hub")])
        switch addHubStep {
        case "vault":
            model._previewSet(fields: fields, discovery: found, hello: hello, selectedVaultID: notes.id, provisioned: nil, path: [.vault])
        case "done":
            model._previewSet(fields: fields, discovery: found, hello: hello, selectedVaultID: notes.id, provisioned: notes, path: [.vault, .done])
        case "code-found", "code-keyboard":
            model._previewSet(fields: fields, discovery: found, hello: nil, selectedVaultID: nil, provisioned: nil, path: [])
        default:
            // The canvas's moment: the code typed, the search still running.
            model._previewSet(fields: fields, discovery: .searching, hello: nil, selectedVaultID: nil, provisioned: nil, path: [])
        }
        return model
    }

    private static func folder(id: String, label: String, deviceIDs: [String]) -> SyncthingManager.FolderInfo {
        SyncthingManager.FolderInfo(
            id: id,
            label: label,
            path: "\(root)/\(label)",
            type: "sendreceive",
            paused: false,
            deviceIDs: deviceIDs
        )
    }

    private static func status(
        state: String,
        pct: Double,
        files: Int,
        bytes: Int64,
        need: Int,
        errorReason: String? = nil
    ) -> SyncthingManager.FolderStatusInfo {
        SyncthingManager.FolderStatusInfo(payload: .init(
            state: state,
            stateChanged: "2026-10-05T09:12:00Z",
            completionPct: pct,
            globalBytes: bytes,
            globalFiles: files + need,
            localBytes: bytes,
            localFiles: files,
            needBytes: Int64(need) * 4_096,
            needFiles: need,
            inProgressBytes: 0,
            errorReason: errorReason,
            errorMessage: nil,
            errorPath: nil,
            errorChanged: nil
        ))
    }

    /// `DeviceInfo` decodes only — the same construction path the other
    /// UI-audit fixtures use. The iPad is offline and shares no vault, so it
    /// raises no "required device" issue.
    private static var devices: [SyncthingManager.DeviceInfo] {
        let json = """
            [
              {"deviceID": "\(serverID)", "name": "Home Server", "connected": true, "paused": false},
              {"deviceID": "\(macBookID)", "name": "MacBook", "connected": true, "paused": false},
              {"deviceID": "\(iPadID)", "name": "iPad", "connected": false, "paused": false}
            ]
            """
        return (try? JSONDecoder().decode([SyncthingManager.DeviceInfo].self, from: Data(json.utf8))) ?? []
    }
}
#endif
