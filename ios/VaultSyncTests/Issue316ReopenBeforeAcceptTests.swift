import Foundation
import Testing

/// A vault removed on this iPhone is offered again under Pending Shares only
/// once the peer announces it anew, and a peer does that when a connection is
/// set up — in practice after VaultSync was closed in the app switcher and
/// opened again, not upon the removal (#316). The engine writes a removal to
/// disk about five seconds after it (#302), and closing the app from the app
/// switcher ends it at once, so the step waits ten seconds first. Every text
/// that sends people to remove a vault and accept it again carries that step
/// between the two, in all four languages; without it they find Pending
/// Shares empty.
@Suite("Remove a vault and accept it again: the texts say to reopen VaultSync in between (#316)")
struct Issue316ReopenBeforeAcceptTests {
    private struct Language {
        let code: String
        /// How the language writes the step that removes the vault.
        let remove: String
        /// The wait before VaultSync is closed.
        let wait: String
        /// How it names the app switcher in the step that reopens VaultSync.
        let reopen: String
        /// How that step says to open VaultSync again: the app switcher alone
        /// would also stand in a text that only says to close it.
        let openAgain: String
        /// How it names Pending Shares, where the share is accepted again.
        let pendingShares: String
    }

    private static let languages = [
        Language(code: "en", remove: "remov", wait: "wait ten seconds", reopen: "app switcher", openAgain: "open it again", pendingShares: "Pending Shares"),
        Language(code: "de", remove: "entfern", wait: "zehn Sekunden", reopen: "App-Umschalter", openAgain: "öffne es erneut", pendingShares: "„Ausstehende Freigaben“"),
        Language(code: "es", remove: "elimina", wait: "diez segundos", reopen: "selector de aplicaciones", openAgain: "vuelve a abrirla", pendingShares: "\"Comparticiones pendientes\""),
        Language(code: "zh-Hans", remove: "移除", wait: "等待十秒", reopen: "应用切换器", openAgain: "重新打开", pendingShares: "“待处理共享”"),
    ]

    /// The question every removal passes, and the notice left on screen once
    /// the vaults of a folder that is not Obsidian's own were removed: neither
    /// names the removal as a step of its own, so the order is checked from
    /// the start of the text.
    static let removalQuestion = "“%@” will stop syncing on this iPhone. Files already on your other devices are not deleted. To sync it here again later: wait ten seconds, close VaultSync in the app switcher and open it again, and accept it under Pending Shares once it shows there."
    static let noticeAfterTheRemoval = "Tap Select Obsidian's Folder. In the picker, tap Browse at the bottom, then \"On My iPhone\" → \"Obsidian\", and tap Open there, without opening one of your vaults. If two folders there are named \"Obsidian\", Obsidian's own is the one that holds the vault you created in Obsidian on this iPhone, and it may carry Obsidian's icon; the other was made by hand. If no \"Obsidian\" there holds a vault you created in Obsidian on this iPhone, open Obsidian and create one with \"Store in iCloud\" turned off first. Vaults you stopped syncing here are offered again after that: wait ten seconds, close VaultSync in the app switcher and open it again, and accept them under Pending Shares once they show there."

    /// From `start` on: the wait, then the reopen, and Pending Shares only
    /// after both. Pending Shares named before the reopen would be an accept
    /// that comes too early.
    private static func expectWaitReopenAccept(
        _ text: String,
        from start: String.Index,
        _ language: Language,
        sourceLocation: SourceLocation = #_sourceLocation
    ) throws {
        let rest = start..<text.endIndex
        let wait = try #require(
            text.range(of: language.wait, options: .caseInsensitive, range: rest),
            "\(language.code) does not wait before closing VaultSync in: \(text)",
            sourceLocation: sourceLocation
        )
        let reopen = try #require(
            text.range(of: language.reopen, range: rest),
            "\(language.code) does not reopen VaultSync in: \(text)",
            sourceLocation: sourceLocation
        )
        let openAgain = try #require(
            text.range(of: language.openAgain, range: reopen.upperBound..<text.endIndex),
            "\(language.code) closes VaultSync without opening it again in: \(text)",
            sourceLocation: sourceLocation
        )
        let accept = try #require(
            text.range(of: language.pendingShares, range: rest),
            "\(language.code) does not accept under Pending Shares in: \(text)",
            sourceLocation: sourceLocation
        )
        #expect(wait.upperBound <= reopen.lowerBound, "\(language.code): the wait comes after the reopen in: \(text)", sourceLocation: sourceLocation)
        #expect(openAgain.upperBound <= accept.lowerBound, "\(language.code): Pending Shares before the reopen in: \(text)", sourceLocation: sourceLocation)
    }

    @Test("Every text that removes a vault and accepts it under Pending Shares waits and reopens VaultSync in between, in every language")
    func removeWaitReopenAccept() throws {
        let english = try TestSupport.localizedStrings("en")
        // The key is the English text. Picked by what it says, so a text
        // added later with the same two steps is held to the same order.
        let keys = english.keys.filter {
            $0.contains("Pending Shares") && $0.lowercased().contains("remov")
        }
        // The two #270 notices, the whole-folder refusal, both #315 card
        // texts, the two #45 texts and the folder-missing text.
        #expect(keys.count >= 8)

        for language in Self.languages {
            let table = try TestSupport.localizedStrings(language.code)
            for key in keys {
                let text = try #require(table[key], "\(language.code) has no text for: \(key)")
                // Searched in the text itself: an index into a lowercased
                // copy says nothing about a position in the original.
                let remove = try #require(
                    text.range(of: language.remove, options: .caseInsensitive),
                    "\(language.code) does not remove the vault in: \(text)"
                )
                try Self.expectWaitReopenAccept(text, from: remove.upperBound, language)
            }
        }
    }

    @Test("The question before a removal and the notice left after it say how the vault comes back", arguments: [removalQuestion, noticeAfterTheRemoval])
    func whereTheRemovalHappens(key: String) throws {
        for language in Self.languages {
            let text = try #require(try TestSupport.localizedStrings(language.code)[key], "\(language.code) has no text for: \(key)")
            try Self.expectWaitReopenAccept(text, from: text.startIndex, language)
        }
    }
}
