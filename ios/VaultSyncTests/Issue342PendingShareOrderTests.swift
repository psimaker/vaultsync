import Foundation
import Testing
@testable import VaultSync

/// The pending shares came in the order the engine's map handed them out,
/// which changed from one poll to the next: rows swapped places under a tap
/// meant for another share, and every new order ran the automatic accept pass
/// again (#342). The bridge now answers sorted by ID; the app lists the shares
/// by the name a row shows, as Finder sorts names, then by folder ID.
@Suite("Pending shares keep one order between two polls (#342)")
struct Issue342PendingShareOrderTests {
    private typealias Folder = SyncthingManager.PendingFolderInfo

    private func folder(_ id: String, _ label: String) -> Folder {
        Folder(id: id, label: label, offeredBy: [])
    }

    @Test("Shares are listed by the name a row shows, as Finder sorts names")
    func listedByShownName() {
        let folders = [
            folder("id-z", "Zettel"),
            folder("id-v10", "Vault 10"),
            folder("archive", ""),
            folder("id-ae", "Ärzte"),
            folder("id-v2", "Vault 2"),
            folder("id-b", "banana"),
        ]
        let ordered = SyncthingManager.pendingFoldersInDisplayOrder(
            folders,
            locale: Locale(identifier: "en_US")
        ).map(\.id)
        // A share without a label shows, and sorts by, its folder ID; a name
        // with an umlaut sorts with its letter, not after Z; numbers in names
        // sort by value; case does not put capitals first.
        #expect(ordered == ["archive", "id-ae", "id-b", "id-v2", "id-v10", "id-z"])
    }

    @Test("The user's language decides where a letter with an accent goes")
    func languageDecidesAccentedLetters() {
        let folders = [folder("id-z", "Zettel"), folder("id-ae", "Ärzte")]
        let german = SyncthingManager.pendingFoldersInDisplayOrder(folders, locale: Locale(identifier: "de_DE"))
        let swedish = SyncthingManager.pendingFoldersInDisplayOrder(folders, locale: Locale(identifier: "sv_SE"))
        #expect(german.map(\.id) == ["id-ae", "id-z"])
        // Swedish puts Ä after Z.
        #expect(swedish.map(\.id) == ["id-z", "id-ae"])
    }

    @Test("Every order the shares arrive in gives the same list")
    func arrivalOrderDoesNotMatter() {
        let folders = [
            folder("id-2", "notes"),
            folder("id-1", "notes"),
            folder("id-3", "Notes"),
            folder("id-0", ""),
            folder("id-4", "Archive"),
        ]
        let results = Set(Self.permutations(folders).map {
            SyncthingManager.pendingFoldersInDisplayOrder($0).map(\.id)
        })
        #expect(results.count == 1)
        // Two shares with the same name keep the order of their folder IDs.
        let ordered = results.first ?? []
        let first = ordered.firstIndex(of: "id-1")
        let second = ordered.firstIndex(of: "id-2")
        #expect(first != nil && second != nil && first! < second!)
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
