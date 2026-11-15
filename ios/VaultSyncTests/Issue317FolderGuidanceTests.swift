import Foundation
import Testing
@testable import VaultSync

/// #317: someone who made a folder named "Obsidian" by hand sees two folders
/// of that name under "On My iPhone" once Obsidian holds a vault: Obsidian's
/// own and theirs. On first setup the picker reopens where it was last left —
/// after a refusal, inside the folder just refused — so the guidance has to
/// work from wherever it opens, and it has to tell the two apart without
/// relying on looks: on the iOS 18.0 and 26.5 simulators an app's folder shows
/// as a plain folder, its icon drawn only on the home screen (measured
/// 2026-11-15). Every text that sends people to Obsidian's folder says, in all
/// four languages:
/// - how to get there from wherever the picker opens (Browse at the bottom);
/// - that of two folders named "Obsidian", Obsidian's own is the one holding
///   the vault created in Obsidian on this iPhone — vaults synced into a
///   hand-made folder were often created in Obsidian too, on a computer;
/// - that if none holds such a vault, one has to be created in Obsidian first,
///   stored on the iPhone.
@Suite("Folder guidance tells Obsidian's folder from one made by hand (#317)")
struct Issue317FolderGuidanceTests {
    private struct Language {
        let code: String
        /// Browse, the tab at the bottom of the picker that leads to the
        /// list of locations from any folder.
        let browse: String
        let onMyIPhone: String
        /// Two folders of the same name.
        let twoFolders: String
        /// The vault created in Obsidian on this iPhone, which only
        /// Obsidian's own folder holds.
        let createdInObsidian: String
        /// Storage on the iPhone, not in iCloud, for the vault to create.
        let storedOnIPhone: String
    }

    private static let languages = [
        Language(code: "en", browse: "tap Browse at the bottom", onMyIPhone: "\"On My iPhone\" → \"Obsidian\"", twoFolders: "two folders", createdInObsidian: "vault you created in Obsidian on this iPhone", storedOnIPhone: "\"Store in iCloud\" turned off"),
        Language(code: "de", browse: "unten auf „Durchsuchen“", onMyIPhone: "„Auf meinem iPhone“ → „Obsidian“", twoFolders: "zwei Ordner", createdInObsidian: "den du auf diesem iPhone in Obsidian angelegt hast", storedOnIPhone: "auf dem iPhone gespeichert wird (nicht in iCloud)"),
        Language(code: "es", browse: "toca \"Explorar\" abajo", onMyIPhone: "\"En mi iPhone\" → \"Obsidian\"", twoFolders: "dos carpetas", createdInObsidian: "Vault que creaste en Obsidian en este iPhone", storedOnIPhone: "guardado en el iPhone (no en iCloud)"),
        Language(code: "zh-Hans", browse: "轻点底部的“浏览”", onMyIPhone: "“在我的 iPhone 上” → “Obsidian”", twoFolders: "两个名为“Obsidian”的文件夹", createdInObsidian: "你在这台 iPhone 上的 Obsidian 中创建的 Vault", storedOnIPhone: "存储在 iPhone 上（而不是 iCloud 中）"),
    ]

    // The keys, as the code passes them to L10n. `sourceUses` ties each one
    // to the literal in its source file, so a reworded key cannot leave the
    // checks below reading a text the app no longer shows.
    static let refusal = "This folder is not Obsidian's own folder, so vaults synced into it would never appear in Obsidian. In the picker, tap Browse at the bottom, then \"On My iPhone\" → \"Obsidian\". If two folders there are named \"Obsidian\", Obsidian's own is the one that holds the vault you created in Obsidian on this iPhone, and it may carry Obsidian's icon; the other was made by hand. If no \"Obsidian\" there holds a vault you created in Obsidian on this iPhone, open Obsidian, create one with \"Store in iCloud\" turned off, and try again."
    static let noticeWithoutVaults = Issue316ReopenBeforeAcceptTests.noticeAfterTheRemoval
    static let noticeWithVaults = "Syncing continues, and VaultSync moves nothing on its own. To see your vaults in Obsidian:\n1. Make sure another device has everything you changed on this iPhone, then remove the vaults here.\n2. This notice then offers Select Obsidian's Folder. Tap it. In the picker, tap Browse at the bottom, then \"On My iPhone\" → \"Obsidian\", and tap Open there, without opening one of your vaults. If two folders there are named \"Obsidian\", Obsidian's own is the one that holds the vault you created in Obsidian on this iPhone, and it may carry Obsidian's icon; the other was made by hand. If no \"Obsidian\" there holds a vault you created in Obsidian on this iPhone, open Obsidian and create one with \"Store in iCloud\" turned off first.\n3. Wait ten seconds, then close VaultSync in the app switcher and open it again. Only then are the vaults offered again, as soon as the devices that share them are connected.\n4. Accept the shares again under Pending Shares.\nThe copies that already arrived stay where they are."
    static let onboardingStep = "Give VaultSync one-time access to your Obsidian folder so it can sync your notes. In the picker, tap Browse at the bottom, then \"On My iPhone\" → \"Obsidian\". That folder appears once Obsidian holds a vault on this iPhone: if you have none, create one in Obsidian first, with \"Store in iCloud\" turned off. If two folders there are named \"Obsidian\", Obsidian's own is the one that holds the vault you created in Obsidian on this iPhone."
    static let pickerHint = "In the picker, tap Browse at the bottom, then \"On My iPhone\" → \"Obsidian\", and tap Open there."
    static let cannotFindHelp = "Open Obsidian and create a vault with \"Store in iCloud\" turned off — any name will do. The Obsidian folder appears under \"On My iPhone\" as soon as it holds a vault. Do not create a folder named \"Obsidian\" yourself: Obsidian cannot open vaults from it. If you already made one, \"On My iPhone\" then shows two folders named \"Obsidian\": Obsidian's own is the one that holds the vault you created in Obsidian on this iPhone, and it may carry Obsidian's icon."
    /// The other refusals of a picked folder: the picker reopens inside that
    /// folder too, so each says how to get back to the locations.
    static let otherRefusals = [
        "Please select a folder. In the picker, tap Browse at the bottom, then \"On My iPhone\" → \"Obsidian\".",
        "VaultSync cannot read this folder. Reopen the picker, tap Browse at the bottom, then select \"On My iPhone\" → \"Obsidian\".",
        "This folder does not look like your Obsidian directory yet. In the picker, tap Browse at the bottom, then \"On My iPhone\" → \"Obsidian\".",
        "The folder you selected lies inside an app's folder — it is a single vault or a folder within one. Select the folder that contains your vaults instead: in the picker, tap Browse at the bottom, then \"On My iPhone\" → \"Obsidian\", and tap Open there, without opening one of your vaults.",
        "This folder does not hold a vault yet. Open Obsidian, create a vault with \"Store in iCloud\" turned off, then tap Browse at the bottom of the picker and choose \"On My iPhone\" → \"Obsidian\" again.",
    ]

    /// Finds each of `parts` in `text`, each after the one before it.
    private static func expectInOrder(
        _ text: String,
        _ parts: [String],
        _ language: String,
        sourceLocation: SourceLocation = #_sourceLocation
    ) {
        var from = text.startIndex
        for part in parts {
            guard let found = text.range(of: part, range: from..<text.endIndex) else {
                Issue.record("\(language): \"\(part)\" missing or out of order in: \(text)", sourceLocation: sourceLocation)
                return
            }
            from = found.upperBound
        }
    }

    /// Whether the app's source file passes `key` to `L10n.tr` as written.
    private static func sourceUses(_ key: String, in relativePath: String) throws -> Bool {
        let url = URL(fileURLWithPath: "\(#filePath)")
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .appendingPathComponent("VaultSync", isDirectory: true)
            .appendingPathComponent(relativePath)
        let source = try String(contentsOf: url, encoding: .utf8)
        let literal = key
            .replacingOccurrences(of: "\\", with: "\\\\")
            .replacingOccurrences(of: "\"", with: "\\\"")
            .replacingOccurrences(of: "\n", with: "\\n")
        return source.contains("L10n.tr(\"\(literal)\")")
    }

    @Test("Every text checked here is the one the app passes to L10n")
    func keysAreTheAppsOwn() throws {
        #expect(try Self.sourceUses(Self.refusal, in: "Services/VaultManager.swift"))
        #expect(try Self.sourceUses(Self.noticeWithoutVaults, in: "Services/SyncthingManager.swift"))
        #expect(try Self.sourceUses(Self.noticeWithVaults, in: "Services/SyncthingManager.swift"))
        #expect(try Self.sourceUses(Self.onboardingStep, in: "Views/OnboardingView.swift"))
        #expect(try Self.sourceUses(Self.pickerHint, in: "Views/ContentView.swift"))
        #expect(try Self.sourceUses(Self.cannotFindHelp, in: "Views/ContentView.swift"))
        for key in Self.otherRefusals {
            #expect(try Self.sourceUses(key, in: "Services/VaultManager.swift"), "\(key)")
        }
    }

    @Test("The refusal and both notices: Browse, then the two folders told apart, then a vault to create first — in all four languages",
          arguments: [refusal, noticeWithoutVaults, noticeWithVaults])
    func threePointsInOrder(key: String) throws {
        for language in Self.languages {
            let text = try #require(try TestSupport.localizedStrings(language.code)[key], "\(language.code) has no text for: \(key)")
            Self.expectInOrder(text, [
                language.browse, language.onMyIPhone,
                language.twoFolders, language.createdInObsidian,
                language.createdInObsidian, language.storedOnIPhone,
            ], language.code)
        }
    }

    @Test("Setup: the step and the help say where the folder comes from and which of two is Obsidian's; the hint says how to get there")
    func setupTexts() throws {
        for language in Self.languages {
            let table = try TestSupport.localizedStrings(language.code)
            let step = try #require(table[Self.onboardingStep], "\(language.code) has no onboarding step")
            Self.expectInOrder(step, [language.browse, language.onMyIPhone, language.storedOnIPhone, language.twoFolders, language.createdInObsidian], language.code)
            let hint = try #require(table[Self.pickerHint], "\(language.code) has no picker hint")
            Self.expectInOrder(hint, [language.browse, language.onMyIPhone], language.code)
            let help = try #require(table[Self.cannotFindHelp], "\(language.code) has no help text")
            Self.expectInOrder(help, [language.storedOnIPhone, language.twoFolders, language.createdInObsidian], language.code)
        }
    }

    @Test("The other refusals of a picked folder say how to get back to the locations", arguments: otherRefusals)
    func otherRefusalsGoThroughBrowse(key: String) throws {
        for language in Self.languages {
            let text = try #require(try TestSupport.localizedStrings(language.code)[key], "\(language.code) has no text for: \(key)")
            Self.expectInOrder(text, [language.browse, language.onMyIPhone], language.code)
        }
    }

    @Test("The icon is only a hint: no text claims Obsidian's folder always shows it")
    func iconIsNoClaim() throws {
        let table = try TestSupport.localizedStrings("en")
        for key in [Self.refusal, Self.noticeWithoutVaults, Self.noticeWithVaults, Self.cannotFindHelp] {
            let text = try #require(table[key])
            #expect(text.contains("it may carry Obsidian's icon"))
            #expect(!text.contains("carries Obsidian's icon"))
        }
    }

    @Test("Both notices about a folder that is not Obsidian's own use these texts")
    func noticesUseTheKeys() throws {
        let issues = SyncthingManager.obsidianVisibilityIssues(
            findings: [
                .init(kind: .selectedFolderNotAnAppFolder, folderIDs: [], tokens: ["root:/x"]),
                .init(kind: .selectedFolderNotAnAppFolder, folderIDs: ["a"], tokens: ["root:/x"]),
            ],
            labels: [:]
        )
        #expect(issues.map(\.remediation) == [Self.noticeWithoutVaults, Self.noticeWithVaults])
    }

    @Test("A folder named \"Obsidian\" made by hand is refused on first selection with this text")
    func refusalUsesTheKey() throws {
        let sandbox = FileManager.default.temporaryDirectory
            .appendingPathComponent("issue317-\(UUID().uuidString)", isDirectory: true)
        defer { try? FileManager.default.removeItem(at: sandbox) }
        let folder = sandbox.appendingPathComponent(
            "Containers/Shared/AppGroup/\(UUID().uuidString)/File Provider Storage/Obsidian",
            isDirectory: true
        )
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        #expect(VaultManager.liveValidationError(for: folder, mode: .firstSelection) == Self.refusal)
    }
}
