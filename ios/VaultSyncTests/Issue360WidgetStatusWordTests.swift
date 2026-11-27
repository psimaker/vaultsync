import Foundation
import Testing
@testable import VaultSync

/// The widget shows `SyncStatus.label`, which resolves through
/// `String(localized:)` from the widget extension's own bundle. Its strings
/// files lacked four of the six status words, so German, Spanish and Chinese
/// widgets showed "All Synced", "Starting", "Sync Error" and "Paused" in
/// English. Each status word is in the widget's four files, with the app's
/// translation: the widget and the status card on the home screen say the
/// same. `ios/scripts/strings-key-parity.sh` holds every other key the widget
/// looks up.
@Suite("The widget's status word is translated like the app's (#360)")
struct Issue360WidgetStatusWordTests {
    static let locales = ["en", "de", "es", "zh-Hans"]

    /// The run language is pinned to English (project.yml, `test.language`),
    /// and an English value is its key: the labels are the keys.
    static var statusKeys: [String] { SyncStatus.allCases.map(\.label) }

    static func strings(_ folder: String, _ locale: String) throws -> [String: String] {
        let url = URL(fileURLWithPath: "\(#filePath)")
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .appendingPathComponent("\(folder)/\(locale).lproj/Localizable.strings")
        return try #require(NSDictionary(contentsOf: url) as? [String: String], "\(url.path) does not parse")
    }

    @Test("The labels are the six English status words")
    func labelsAreTheKeys() {
        #expect(Self.statusKeys == ["All Synced", "Syncing", "Starting", "Needs Attention", "Sync Error", "Paused"])
    }

    @Test("Each status word is in the widget's file, as the app translates it", arguments: locales)
    func widgetCarriesTheAppsStatusWords(locale: String) throws {
        let widget = try Self.strings("VaultSyncWidget", locale)
        let app = try Self.strings("VaultSync", locale)
        for key in Self.statusKeys {
            let appValue = try #require(app[key], "the app's \(locale) file lacks \(key)")
            #expect(widget[key] == appValue, "\(locale): \(key)")
        }
    }
}
