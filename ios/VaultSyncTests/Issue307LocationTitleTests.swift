import Foundation
import Testing
@testable import VaultSync

/// A vault's Location row named the selected folder by its name on disk.
/// Obsidian's folder reaches the app as `…/Application/<UUID>/Documents`, so a
/// vault in it read "Documents › Notes", while a vault in a folder the user
/// made and named "Obsidian" — a place Obsidian never lists — read
/// "Obsidian › Notes" (#307). The title now names an app's own folder the way
/// the Files app places it, without naming the app: the path cannot tell
/// which app it belongs to. The path forms are the ones #270 read on a device
/// and on the iOS 18 and 26 simulators.
@Suite("A vault's Location names an app's folder the way Files does (#307)")
struct Issue307LocationTitleTests {
    private static let uuid = "0A1B2C3D-0000-4000-8000-000000000000"
    private static let device = "/private/var/mobile/Containers/Data/Application/\(uuid)/Documents"
    private static let deviceUnresolved = "/var/mobile/Containers/Data/Application/\(uuid)/Documents"
    private static let simulator =
        "/Users/dev/Library/Developer/CoreSimulator/Devices/\(uuid)/data/Containers/Data/Application/\(uuid)/Documents"
    private static let iCloudApp = "/private/var/mobile/Library/Mobile Documents/iCloud~md~obsidian/Documents"
    private static let selfMadeObsidian =
        "/private/var/mobile/Containers/Shared/AppGroup/\(uuid)/File Provider Storage/Obsidian"
    private static let selfMadeDocuments =
        "/private/var/mobile/Containers/Shared/AppGroup/\(uuid)/File Provider Storage/Documents"

    private func title(_ path: String, base: String?) -> String {
        VaultLocationTitle.title(path: path, base: base)
    }

    @Test("A vault in an app's folder on the iPhone reads \"On My iPhone › App Folder › Notes\"",
          arguments: [device, deviceUnresolved, simulator])
    func vaultInAppFolderOnTheIPhone(root: String) {
        #expect(title(root + "/Notes", base: root) == "On My iPhone › App Folder › Notes")
    }

    @Test("A vault in an app's folder in iCloud Drive reads \"iCloud Drive › App Folder › Notes\"")
    func vaultInAppFolderInICloud() {
        #expect(title(Self.iCloudApp + "/Notes", base: Self.iCloudApp) == "iCloud Drive › App Folder › Notes")
    }

    @Test("The disk name \"Documents\" never shows for an app's folder")
    func noDocumentsForAnAppFolder() {
        for root in [Self.device, Self.deviceUnresolved, Self.simulator, Self.iCloudApp] {
            for path in [root, root + "/Notes", root + "/Notes/Daily"] {
                let shown = title(path, base: root)
                #expect(!shown.split(separator: "›").contains { $0.trimmingCharacters(in: .whitespaces) == "Documents" },
                        "\(path) → \(shown)")
            }
        }
    }

    @Test("A folder the user made keeps its own name and never reads like an app's folder",
          arguments: [selfMadeObsidian, selfMadeDocuments])
    func selfMadeFolderKeepsItsName(root: String) {
        let name = (root as NSString).lastPathComponent
        let shown = title(root + "/Notes", base: root)
        #expect(shown == "\(name) › Notes")
        #expect(shown != title(Self.device + "/Notes", base: Self.device))
    }

    @Test("A vault synced as part of the whole folder, and deeper folders, keep every level below the app's folder")
    func nestedLevels() {
        #expect(title(Self.device + "/Notes/Daily", base: Self.device) == "On My iPhone › App Folder › Notes › Daily")
    }

    @Test("A folder that syncs straight into the app's folder is named as that folder")
    func pathIsTheAppFolder() {
        #expect(title(Self.device, base: Self.device) == "On My iPhone › App Folder")
        #expect(title(Self.iCloudApp, base: Self.iCloudApp) == "iCloud Drive › App Folder")
    }

    @Test("A vault in an app's folder is named so even when another folder is connected now")
    func appFolderOutsideTheConnectedFolder() {
        #expect(title(Self.device + "/Notes", base: Self.selfMadeObsidian) == "On My iPhone › App Folder › Notes")
        #expect(title(Self.device + "/Notes", base: nil) == "On My iPhone › App Folder › Notes")
    }

    @Test("A folder outside every app's folder and outside the connected one shows its own name")
    func elsewhereShowsTheFolderName() {
        #expect(title(Self.selfMadeObsidian + "/Notes", base: nil) == "Notes")
        #expect(title("/private/var/mobile/Library/Mobile Documents/com~apple~CloudDocs/Documents/Notes",
                      base: nil) == "Notes")
    }
}
