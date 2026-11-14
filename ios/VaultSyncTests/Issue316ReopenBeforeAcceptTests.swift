import Foundation
import Testing

/// A vault removed on this iPhone is offered again under Pending Shares only
/// once the peer announces it anew, and a peer does that when a connection is
/// set up — at the next start of VaultSync, not upon the removal (#316). Every
/// text that sends people to remove a vault and accept it again therefore says
/// to close VaultSync in the app switcher and open it again in between, in all
/// four languages; without that step they find Pending Shares empty.
@Suite("Remove a vault and accept it again: the texts say to reopen VaultSync in between (#316)")
struct Issue316ReopenBeforeAcceptTests {
    private struct Language {
        let code: String
        /// How the language writes the step that removes the vault.
        let remove: String
        /// How it names the app switcher in the step that reopens VaultSync.
        let reopen: String
        /// How it names Pending Shares, where the share is accepted again.
        let pendingShares: String
        /// How the merge question's example says that VaultSync was reopened.
        let reopenedInExample: String
    }

    private static let languages = [
        Language(code: "en", remove: "remov", reopen: "app switcher", pendingShares: "Pending Shares", reopenedInExample: "reopening VaultSync"),
        Language(code: "de", remove: "entfern", reopen: "App-Umschalter", pendingShares: "„Ausstehende Freigaben“", reopenedInExample: "VaultSync erneut geöffnet"),
        Language(code: "es", remove: "elimina", reopen: "selector de aplicaciones", pendingShares: "\"Comparticiones pendientes\"", reopenedInExample: "volver a abrir VaultSync"),
        Language(code: "zh-Hans", remove: "移除", reopen: "应用切换器", pendingShares: "“待处理共享”", reopenedInExample: "重新打开 VaultSync"),
    ]

    private static func strings(_ code: String, filePath: StaticString = #filePath) throws -> [String: String] {
        let url = URL(fileURLWithPath: "\(filePath)")
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .appendingPathComponent("VaultSync", isDirectory: true)
            .appendingPathComponent("\(code).lproj", isDirectory: true)
            .appendingPathComponent("Localizable.strings")
        let data = try Data(contentsOf: url)
        let list = try PropertyListSerialization.propertyList(from: data, format: nil)
        return try #require(list as? [String: String])
    }

    @Test("Every step that removes a vault and accepts it under Pending Shares reopens VaultSync in between, in every language")
    func removeReopenAccept() throws {
        let english = try Self.strings("en")
        // The key is the English text. Picked by what it says, so a text
        // added later with the same two steps is held to the same order.
        let keys = english.keys.filter {
            $0.contains("Pending Shares") && $0.lowercased().contains("remov")
        }
        // The two #270 notices, the whole-folder refusal, both #315 card
        // texts, the two #45 texts and the folder-missing text.
        #expect(keys.count >= 8)

        for language in Self.languages {
            let table = try Self.strings(language.code)
            for key in keys {
                let text = try #require(table[key], "\(language.code) has no text for: \(key)")
                // Searched in the text itself: an index into a lowercased
                // copy says nothing about a position in the original.
                let remove = try #require(
                    text.range(of: language.remove, options: .caseInsensitive),
                    "\(language.code) does not remove the vault in: \(text)"
                )
                let reopen = try #require(
                    text.range(of: language.reopen),
                    "\(language.code) does not reopen VaultSync in: \(text)"
                )
                let accept = try #require(
                    text.range(of: language.pendingShares, options: .backwards),
                    "\(language.code) does not accept under Pending Shares in: \(text)"
                )
                #expect(remove.upperBound <= reopen.lowerBound, "\(language.code): reopen before remove in: \(text)")
                #expect(reopen.upperBound <= accept.lowerBound, "\(language.code): accept before reopen in: \(text)")
            }
        }
    }

    @Test("The merge question's example of a vault's own earlier notes reopens VaultSync between removing and accepting")
    func mergeQuestionExample() throws {
        let key = "The folder \"%@\" already contains files. If you accept, those files and the contents of the shared vault \"%@\" will be combined and synced to the other devices sharing this vault. Accept only if this folder holds this vault's own earlier notes — for example after removing the vault, reopening VaultSync and accepting its share again. If it is a different vault or unrelated files, cancel and use \"Choose Vault…\" to pick a different location."
        for language in Self.languages {
            let text = try #require(try Self.strings(language.code)[key], "\(language.code) has no merge question")
            #expect(text.contains(language.reopenedInExample), "\(language.code): \(text)")
            #expect(text.components(separatedBy: "%@").count - 1 == 2)
        }
    }
}
