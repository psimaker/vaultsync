import Foundation
import Testing
@testable import VaultSync

/// The question before a share is combined with a folder that already holds
/// files (#308, decision 066).
///
/// It recommended accepting "after removing the vault, reopening VaultSync
/// and accepting its share again" — the way out #270 gives a vault that
/// synced straight into the selected folder, and the way on #315's card
/// gives a vault whose folder was replaced by a copy. In both the folder is
/// most likely a copy, or part of one, and combining with a copy can send
/// its older text of a note out as the newer one (#331).
@Suite("The merge question never recommends combining with an earlier copy (#308)")
struct Issue308MergeQuestionTests {

    static let defaultKey = "The folder \"%@\" already contains files. If you accept, those files and the contents of the shared vault \"%@\" will be combined and synced to the other devices sharing this vault. Accept only if this is the folder this vault synced in on this iPhone, unchanged since and not a copy put in its place, or if it holds notes you want to add to this vault. Never combine a copy of this vault: where a copy holds an older version of a note, the older version can replace the newer one on every device. For a copy, a different vault or unrelated files, cancel and use \"Choose Vault…\" to give this vault another folder name."
    static let earlierCopyKey = "The folder \"%@\" lies in the folder you selected, which still holds the files of a vault that synced straight into it, so it is most likely part of that earlier copy. If you accept, its files will be combined with the shared vault \"%@\" and synced to the other devices sharing this vault: notes of the earlier copy can appear a second time, at the top level of the vault, and where it holds an older version of a note, the older version can replace the newer one on every device. Do not combine them: cancel and use \"Choose Vault…\" to give this vault another folder name."
    static let earlierCopyRowKey = "The folder “%@” already holds files. If they belong to the copy that arrived earlier, do not combine them: tap \"Choose Vault…\" and give this vault another folder name."

    private struct Language {
        let code: String
        /// The example the question gave until #308.
        let reacceptExample: String
        /// What combining with an older copy does.
        let olderVersion: String
        /// "Do not combine" in the earlier-copy text.
        let doNotCombine: String
        /// "another folder name".
        let anotherName: String
    }

    private static let languages = [
        Language(code: "en", reacceptExample: "accepting its share again", olderVersion: "older version can replace the newer one", doNotCombine: "Do not combine them", anotherName: "another folder name"),
        Language(code: "de", reacceptExample: "Freigabe erneut angenommen", olderVersion: "ältere Fassung auf allen Geräten die neuere ersetzen", doNotCombine: "Führe sie nicht zusammen", anotherName: "anderen Ordnernamen"),
        Language(code: "es", reacceptExample: "aceptar su compartición de nuevo", olderVersion: "versión anterior puede reemplazar a la más reciente", doNotCombine: "No los combines", anotherName: "otro nombre de carpeta"),
        Language(code: "zh-Hans", reacceptExample: "再次接受其共享", olderVersion: "旧版本可能会在所有设备上取代新版本", doNotCombine: "请不要合并", anotherName: "另取一个文件夹名称"),
    ]

    // MARK: - The texts, in all four languages

    @Test("Neither text, in any language, names removing and accepting again; both say what an older copy does and lead to another folder name", arguments: [defaultKey, earlierCopyKey])
    func neitherTextRecommendsAcceptingAgain(key: String) throws {
        for language in Self.languages {
            let strings = try TestSupport.localizedStrings(language.code)
            let text = try #require(strings[key], "\(language.code) has no text for: \(key)")
            let chooseVault = try #require(strings["Choose Vault…"])
            #expect(!text.contains(language.reacceptExample), "\(language.code): \(text)")
            #expect(text.contains(language.olderVersion), "\(language.code): \(text)")
            #expect(text.contains(chooseVault), "\(language.code) does not name \(chooseVault): \(text)")
            #expect(text.contains(language.anotherName), "\(language.code): \(text)")
            #expect(text.components(separatedBy: "%@").count - 1 == 2, "\(language.code): \(text)")
        }
    }

    @Test("The text for a target in an earlier copy says not to combine")
    func earlierCopyTextSaysDoNotCombine() throws {
        for language in Self.languages {
            let text = try #require(try TestSupport.localizedStrings(language.code)[Self.earlierCopyKey])
            #expect(text.contains(language.doNotCombine), "\(language.code): \(text)")
        }
    }

    @Test("The target comes first and the share second in every language, so a translation cannot swap them")
    func argumentOrder() throws {
        for language in Self.languages {
            let strings = try TestSupport.localizedStrings(language.code)
            for key in [Self.defaultKey, Self.earlierCopyKey] {
                let text = try #require(strings[key])
                let filled = String(format: text, "TARGET-308", "SHARE-308")
                let target = try #require(filled.range(of: "TARGET-308"), "\(language.code)")
                let share = try #require(filled.range(of: "SHARE-308"), "\(language.code)")
                #expect(target.lowerBound < share.lowerBound, "\(language.code): \(filled)")
            }
        }
    }

    @MainActor
    @Test("The question picks its text by whether the target lies in an earlier copy")
    func messagePicksTheText() {
        let inCopy = MergeQuestion.message(targetName: "Projects", shareLabel: "Life Notes", targetInEarlierCopy: true)
        #expect(inCopy == L10n.fmt(Self.earlierCopyKey, "Projects", "Life Notes"))
        let plain = MergeQuestion.message(targetName: "Projects", shareLabel: "Life Notes", targetInEarlierCopy: false)
        #expect(plain == L10n.fmt(Self.defaultKey, "Projects", "Life Notes"))
    }

    // MARK: - When the target lies in an earlier copy

    @Test("An app's own folder that is, by what it holds, a single vault holds an earlier copy; one with vaults beside a .obsidian does not")
    func holdsEarlierCopyRule() {
        typealias Content = ObsidianVisibility.DirectoryContent
        #expect(ObsidianVisibility.holdsEarlierCopy(place: .appFolder, content: Content(hasOwnConfig: true, hasVaultSubfolders: false)))
        // A target that is a vault of its own, or an iCloud folder once
        // opened as a vault on a computer: not called part of a copy — the
        // plain text still warns against any copy.
        #expect(!ObsidianVisibility.holdsEarlierCopy(place: .appFolder, content: Content(hasOwnConfig: true, hasVaultSubfolders: true)))
        // Obsidian's folder as it should be: vaults side by side.
        #expect(!ObsidianVisibility.holdsEarlierCopy(place: .appFolder, content: Content(hasOwnConfig: false, hasVaultSubfolders: true)))
        #expect(!ObsidianVisibility.holdsEarlierCopy(place: .appFolder, content: Content(hasOwnConfig: false, hasVaultSubfolders: false)))
        // Unreadable, or not connected: nothing to say for certain.
        #expect(!ObsidianVisibility.holdsEarlierCopy(place: .appFolder, content: nil))
        #expect(!ObsidianVisibility.holdsEarlierCopy(place: nil, content: Content(hasOwnConfig: true, hasVaultSubfolders: false)))
        // A vault picked as the selected folder is refused as a root before
        // any question; a folder elsewhere gets #270's notice instead.
        #expect(!ObsidianVisibility.holdsEarlierCopy(place: .insideAppFolder, content: Content(hasOwnConfig: true, hasVaultSubfolders: false)))
        #expect(!ObsidianVisibility.holdsEarlierCopy(place: .elsewhere, content: Content(hasOwnConfig: true, hasVaultSubfolders: false)))
    }

    @Test("The live rule reads an app's own folder from disk and nothing else")
    func liveRuleReadsTheFolder() throws {
        let base = FileManager.default.temporaryDirectory
            .appendingPathComponent("issue308-\(UUID().uuidString)", isDirectory: true)
        defer { try? FileManager.default.removeItem(at: base) }
        let appFolder = base.appendingPathComponent("Containers/Data/Application/\(UUID().uuidString)/Documents", isDirectory: true)
        try FileManager.default.createDirectory(at: appFolder.appendingPathComponent("Projects"), withIntermediateDirectories: true)
        #expect(VaultManager.selectedFolderPlace(appFolder.path) == .appFolder)
        #expect(!ObsidianVisibility.selectedFolderHoldsEarlierCopy(appFolder.path))

        // The vault that synced straight into the folder left its config.
        try FileManager.default.createDirectory(at: appFolder.appendingPathComponent(".obsidian"), withIntermediateDirectories: true)
        #expect(ObsidianVisibility.selectedFolderHoldsEarlierCopy(appFolder.path))

        // A vault of its own beside it: the target may be that vault, so
        // the question does not call it part of the copy.
        try FileManager.default.createDirectory(at: appFolder.appendingPathComponent("Life Notes/.obsidian"), withIntermediateDirectories: true)
        #expect(!ObsidianVisibility.selectedFolderHoldsEarlierCopy(appFolder.path))

        #expect(!ObsidianVisibility.selectedFolderHoldsEarlierCopy(nil))
    }

    // MARK: - The tap that asks

    @MainActor
    private static func coordinator(holdsEarlierCopy: Bool) -> ShareAcceptCoordinator {
        let offer = SyncthingManager.PendingFolderInfo(id: "f1", label: "Life Notes", offeredBy: [])
        return ShareAcceptCoordinator(environment: ShareAcceptCoordinator.Environment(
            settled: { true },
            vaultAccessible: { true },
            pendingFolders: { [offer] },
            autoAcceptEligible: { [] },
            accept: { _, _ in .needsMergeConfirmation(targetName: "Projects") },
            acceptIntoTarget: { _, _ in nil },
            unignorePendingFolder: { _ in },
            ignorePendingFolder: { _ in },
            selectedFolderHoldsEarlierCopy: { holdsEarlierCopy }
        ))
    }

    @MainActor
    @Test("A tap on a target in an earlier copy asks with the earlier-copy text, and the row keeps saying not to combine after Cancel")
    func tapInEarlierCopy() throws {
        let c = Self.coordinator(holdsEarlierCopy: true)
        c.accept(SyncthingManager.PendingFolderInfo(id: "f1", label: "Life Notes", offeredBy: []), source: .manual)
        let request = try #require(c.pendingMergeConfirmation)
        #expect(request.targetInEarlierCopy)
        #expect(request.targetName == "Projects")
        let row = try #require(c.pendingShareFailures["f1"])
        #expect(row.message == L10n.fmt(Self.earlierCopyRowKey, "Projects"))
        // No "Review and Accept to decide" beside "do not combine" (066).
        #expect(row.remediation.isEmpty)
    }

    @MainActor
    @Test("A tap on any other target asks with the plain text")
    func tapElsewhere() throws {
        let c = Self.coordinator(holdsEarlierCopy: false)
        c.accept(SyncthingManager.PendingFolderInfo(id: "f1", label: "Life Notes", offeredBy: []), source: .manual)
        let request = try #require(c.pendingMergeConfirmation)
        #expect(!request.targetInEarlierCopy)
        let row = try #require(c.pendingShareFailures["f1"])
        #expect(row.message != L10n.fmt(Self.earlierCopyRowKey, "Projects"))
        #expect(row.remediation == L10n.tr("Tap \"Review and Accept\" to decide, or \"Choose Vault…\" to pick a different location."))
    }

    // MARK: - Folder Path Missing

    @Test("A vault whose folder is missing is not told to recreate the folder")
    func folderPathMissingDoesNotSayRecreate() {
        let missing = SyncUserError.fromFolderStatus(reason: "folder_path_missing", message: "folder path missing", path: nil)
        #expect(missing.title == L10n.tr("Folder Path Missing"))
        #expect(!missing.remediation.localizedCaseInsensitiveContains("recreate the folder"))
        #expect(!missing.remediation.localizedCaseInsensitiveContains("recreate or reselect"))
        #expect(missing.remediation.contains("move it back to its original place"))
        #expect(missing.remediation.contains("never moves, recreates, or deletes folders"))
        #expect(missing.remediation == SyncUserError.vaultFolderGoneRemediation)
        // Its way on names Pending Shares; the help it links stays the
        // moved-or-deleted section, not "No Pending Shares Appear".
        #expect(SyncUserError.troubleshootingURL(for: missing)?.fragment == "vault-folder-was-moved-or-deleted")
    }

    @Test("The old texts are gone from every language")
    func oldKeysRemoved() throws {
        let removed = [
            "The folder \"%@\" already contains files. If you accept, those files and the contents of the shared vault \"%@\" will be combined and synced to the other devices sharing this vault. Accept only if this folder holds this vault's own earlier notes — for example after removing the vault, reopening VaultSync and accepting its share again. If it is a different vault or unrelated files, cancel and use \"Choose Vault…\" to pick a different location.",
            "Recreate or reselect the folder, then trigger a rescan.",
        ]
        for language in Self.languages {
            let strings = try TestSupport.localizedStrings(language.code)
            for key in removed {
                #expect(strings[key] == nil, "\(language.code) still has: \(key)")
            }
        }
    }
}
