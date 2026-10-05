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
    private static let root = "/DesignPreview/Obsidian"

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
        let firstRun = screen == .onboarding || screen == .onboardingShare
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
        let workSyncing = screen == .syncing
        syncthingManager._testSetFolderStatuses([
            notesID: status(state: "idle", pct: 100, files: 1284, bytes: 44_040_192, need: 0),
            workID: workSyncing
                ? status(state: "syncing", pct: 63, files: 307, bytes: 9_437_184, need: 3)
                : status(state: "idle", pct: 100, files: 310, bytes: 9_437_184, need: 0),
        ])
        syncthingManager._testApplyDeviceList(devices)
        let now = Date()
        syncthingManager._testSetLastSyncTimes(
            global: now.addingTimeInterval(-120),
            byFolder: [notesID: now.addingTimeInterval(-120), workID: now.addingTimeInterval(-3_600)]
        )
        let withConflict: Set<Screen> = [.attention, .vault, .conflicts, .conflict]
        syncthingManager._testSetConflictFiles(withConflict.contains(screen) ? [notesID: [conflict]] : [:])
        syncthingManager._testSetPendingFolders(screen == .attention ? [
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
        need: Int
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
            errorReason: nil,
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
