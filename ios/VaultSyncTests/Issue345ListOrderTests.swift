import Foundation
import Testing
@testable import VaultSync

/// The vaults and the devices came in the order the engine's maps handed
/// them out, which changed from one poll to the next: with two vaults or two
/// devices the rows could swap places between two polls, and every new order
/// was a change to whatever reads the lists (#345). The bridge now answers
/// sorted by ID; the app lists them by the name a row shows, as Finder sorts
/// names, then by ID — the pattern of the pending shares (#342).
@Suite("Vaults and devices keep one order between two polls (#345)")
struct Issue345ListOrderTests {
    private static func folderJSON(_ id: String, _ label: String) -> String {
        #"{"id":"\#(id)","label":"\#(label)","path":"/tmp/issue345/\#(id)","type":"sendreceive","paused":false,"deviceIDs":[]}"#
    }

    private static func deviceJSON(_ id: String, _ name: String) -> String {
        #"{"deviceID":"\#(id)","name":"\#(name)","connected":false,"paused":false}"#
    }

    private static func decodeDevices(_ items: [String]) throws -> [SyncthingManager.DeviceInfo] {
        try JSONDecoder().decode(
            [SyncthingManager.DeviceInfo].self,
            from: Data("[\(items.joined(separator: ","))]".utf8)
        )
    }

    @MainActor
    private func makeManager() -> SyncthingManager {
        let defaults = TestSupport.makeIsolatedDefaults(label: "Issue345")
        let manager = SyncthingManager(syncHistoryStore: SyncHistoryStore(defaults: defaults, storageKey: "history"))
        manager._testSetRunning(true)
        return manager
    }

    // MARK: - The manager

    @MainActor
    @Test("The same vaults read in another order keep their places")
    func foldersKeepTheirPlacesAcrossPolls() {
        let manager = makeManager()
        let zettel = Self.folderJSON("vault-a", "Zettel")
        let notes = Self.folderJSON("vault-b", "Notes")
        let archive = Self.folderJSON("vault-c", "archive")

        var seen: [[String]] = []
        for order in [[zettel, notes, archive], [archive, zettel, notes], [notes, archive, zettel]] {
            manager._testApplyPolledFolderRead(
                json: "[\(order.joined(separator: ","))]",
                readStartedAt: 0,
                engineRunningAcrossRead: true
            )
            seen.append(manager.folders.map(\.id))
        }
        // By the name a row shows: "archive", "Notes", "Zettel".
        #expect(seen == Array(repeating: ["vault-c", "vault-b", "vault-a"], count: 3))
    }

    @MainActor
    @Test("The same devices read in another order keep their places")
    func devicesKeepTheirPlacesAcrossPolls() throws {
        let manager = makeManager()
        let hub = Self.deviceJSON("DEVICE-A", "Zulu Hub")
        let mac = Self.deviceJSON("DEVICE-B", "mac")
        let phone = Self.deviceJSON("DEVICE-C", "iPad")

        var seen: [[String]] = []
        for order in [[hub, mac, phone], [phone, hub, mac], [mac, phone, hub]] {
            manager._testApplyDeviceList(try Self.decodeDevices(order))
            seen.append(manager.devices.map(\.deviceID))
        }
        // By the name a row shows: "iPad", "mac", "Zulu Hub".
        #expect(seen == Array(repeating: ["DEVICE-C", "DEVICE-B", "DEVICE-A"], count: 3))
    }

    // MARK: - The vaults

    private static func folder(_ id: String, _ label: String) -> SyncthingManager.FolderInfo {
        SyncthingManager.FolderInfo(id: id, label: label, path: "/tmp/issue345/\(id)", type: "sendreceive", paused: false, deviceIDs: [])
    }

    @Test("Vaults are listed by the name a row shows, as Finder sorts names")
    func vaultsListedByShownName() {
        let folders = [
            Self.folder("id-z", "Zettel"),
            Self.folder("id-v10", "Vault 10"),
            Self.folder("archive", ""),
            Self.folder("id-ae", "Ärzte"),
            Self.folder("id-v2", "Vault 2"),
            Self.folder("id-b", "banana"),
        ]
        let ordered = SyncthingManager.foldersInDisplayOrder(folders, locale: Locale(identifier: "en_US")).map(\.id)
        // A vault without a label shows, and sorts by, its folder ID; a name
        // with an umlaut sorts with its letter, not after Z; numbers in names
        // sort by value; case does not put capitals first.
        #expect(ordered == ["archive", "id-ae", "id-b", "id-v2", "id-v10", "id-z"])
    }

    @Test("The user's language decides where a vault with an accent goes")
    func languageDecidesAccentedVaults() {
        let folders = [Self.folder("id-z", "Zettel"), Self.folder("id-ae", "Ärzte")]
        let german = SyncthingManager.foldersInDisplayOrder(folders, locale: Locale(identifier: "de_DE"))
        let swedish = SyncthingManager.foldersInDisplayOrder(folders, locale: Locale(identifier: "sv_SE"))
        #expect(german.map(\.id) == ["id-ae", "id-z"])
        // Swedish puts Ä after Z.
        #expect(swedish.map(\.id) == ["id-z", "id-ae"])
    }

    @Test("Every order the vaults arrive in gives the same list; two of one name keep the order of their IDs")
    func vaultArrivalOrderDoesNotMatter() {
        let folders = [
            Self.folder("id-2", "notes"),
            Self.folder("id-1", "notes"),
            Self.folder("id-3", "Notes"),
            Self.folder("id-0", ""),
            Self.folder("id-4", "Archive"),
        ]
        let results = Set(Self.permutations(folders).map {
            SyncthingManager.foldersInDisplayOrder($0, locale: Locale(identifier: "en_US")).map(\.id)
        })
        #expect(results.count == 1)
        let ordered = results.first ?? []
        let first = ordered.firstIndex(of: "id-1")
        let second = ordered.firstIndex(of: "id-2")
        #expect(first != nil && second != nil && first! < second!)
    }

    // MARK: - The devices

    private static func device(_ id: String, _ name: String) throws -> SyncthingManager.DeviceInfo {
        try decodeDevices([deviceJSON(id, name)])[0]
    }

    @Test("Devices are listed by name; those without a name come after them, by device ID")
    func devicesWithoutANameComeLast() throws {
        let devices = try [
            Self.device("DEVICE-K", "MacBook"),
            Self.device("DEVICE-B", ""),
            Self.device("DEVICE-Z", "hermes"),
            Self.device("DEVICE-A", ""),
            Self.device("DEVICE-C", "Ärzte-Mac"),
            Self.device("DEVICE-M", "Hub 10"),
            Self.device("DEVICE-N", "Hub 9"),
        ]
        let ordered = SyncthingManager.devicesInDisplayOrder(devices, locale: Locale(identifier: "de_DE")).map(\.deviceID)
        // An unnamed device's row shows "Unnamed", not its ID: by its ID it
        // would sit between "Ärzte-Mac" and "hermes", by a key nobody sees.
        // It comes after every named device instead.
        #expect(ordered == ["DEVICE-C", "DEVICE-Z", "DEVICE-N", "DEVICE-M", "DEVICE-K", "DEVICE-A", "DEVICE-B"])
    }

    @Test("Every order the devices arrive in gives the same list; two of one name keep the order of their IDs")
    func deviceArrivalOrderDoesNotMatter() throws {
        let devices = try [
            Self.device("DEVICE-2", "mac"),
            Self.device("DEVICE-1", "mac"),
            Self.device("DEVICE-3", "Mac"),
            Self.device("DEVICE-0", ""),
            Self.device("DEVICE-5", ""),
            Self.device("DEVICE-4", "Hub"),
        ]
        let results = Set(Self.permutations(devices).map {
            SyncthingManager.devicesInDisplayOrder($0, locale: Locale(identifier: "en_US")).map(\.deviceID)
        })
        #expect(results.count == 1)
        let ordered = results.first ?? []
        let first = ordered.firstIndex(of: "DEVICE-1")
        let second = ordered.firstIndex(of: "DEVICE-2")
        #expect(first != nil && second != nil && first! < second!)
        #expect(ordered.suffix(2) == ["DEVICE-0", "DEVICE-5"])
    }

    // MARK: - The vault rows

    @Test("The vault rows of a whole-directory folder sort by the vault names they show, among the other rows")
    func vaultRowsSortByTheirOwnName() {
        struct Row { let id: String; let name: String }
        // The rows of one folder that syncs the whole Obsidian directory
        // carry the names of the vaults in it; a vault of its own carries its
        // label. All of them are listed by what they show.
        let rows = [
            Row(id: "root/Zettel", name: "Zettel"),
            Row(id: "root/archive", name: "archive"),
            Row(id: "work", name: "Projects"),
            Row(id: "root/Notes", name: "Notes"),
        ]
        let ordered = SyncthingManager.inDisplayOrder(rows, name: { $0.name }, id: { $0.id }, locale: Locale(identifier: "en_US"))
        #expect(ordered.map(\.id) == ["root/archive", "root/Notes", "work", "root/Zettel"])
    }

    private static func permutations<T>(_ items: [T]) -> [[T]] {
        guard items.count > 1 else { return [items] }
        var result: [[T]] = []
        for index in items.indices {
            var rest = items
            let item = rest.remove(at: index)
            for tail in permutations(rest) {
                result.append([item] + tail)
            }
        }
        return result
    }
}
