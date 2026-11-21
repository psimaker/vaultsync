import Foundation
import Testing
@testable import VaultSync

/// Decision 070: what sells names the plan ("VaultSync Plus") and its feature
/// ("Background Sync"); the relay is the mechanism and keeps its name only
/// where diagnostics and the service's own errors describe it; a run in the
/// background is a "Background Run", whatever started it, so nothing a person
/// without the plan sees reads as the paid feature.
@Suite("The plan and its feature are named apart from the mechanism (decision 070)")
struct PlanNameOnPurchaseSurfaceTests {
    static let languages = ["en", "de", "es", "zh-Hans"]

    /// The only English texts that may still say "Cloud Relay": diagnostics
    /// and the relay service's own errors. A new key that names it on a
    /// selling surface fails here and has to name the plan or the feature.
    static let mechanismKeys: Set<String> = [
        "Cloud Relay provisioning could not contact the relay backend.",
        "Cloud Relay provisioning did not complete.",
        "Cloud Relay provisioning is temporarily rate limited.",
        "This timestamp is updated when VaultSync receives a silent push from Cloud Relay.",
        "VaultSync could not reach the Cloud Relay service.",
        "Cloud Relay is delivering wake-ups",
        "Alert banners are off at the iOS level. Cloud Relay wake-ups still work — they use silent push, which does not need notification permission.",
        "iOS did not provide a push token required for Cloud Relay wake-ups.",
        "Cloud Relay is not used for pairing, capability, namespace, or evidence.",
        "Pairing authorizes only controlled diagnostics for the selected server and folder. It does not create a namespace, transfer files, change Syncthing trust, or use Cloud Relay. Namespace setup remains a separate explicit app and operator action.",
    ]

    @Test("Only diagnostics and the relay's own errors say Cloud Relay")
    func cloudRelayOnlyNamesTheMechanism() throws {
        let english = try TestSupport.localizedStrings("en")
        let naming = Set(english.keys.filter { $0.contains("Cloud Relay") })
        #expect(naming.subtracting(Self.mechanismKeys).isEmpty, "Cloud Relay on a selling surface: \(naming.subtracting(Self.mechanismKeys).sorted())")
        // The list stays exact: a key that left the app leaves the list too.
        #expect(Self.mechanismKeys.subtracting(naming).isEmpty, "allow-listed but gone: \(Self.mechanismKeys.subtracting(naming).sorted())")
    }

    @Test("No text sends people to a tab named Relay")
    func noTextNamesTheRelayTab() throws {
        for language in Self.languages {
            let table = try TestSupport.localizedStrings(language)
            #expect(table["Relay"] == nil, "\(language) still has the old tab label")
            #expect(table["Plus"] == "Plus", "\(language): the tab is named Plus in every language")
        }
        let english = try TestSupport.localizedStrings("en")
        #expect(english.keys.filter { $0.contains("Relay tab") }.isEmpty)
    }

    @Test("Every background run is titled a Background Run, never the paid feature")
    func backgroundRunsAreNotTheFeature() {
        let results: [BackgroundSyncService.SyncResult] = [
            .synced, .alreadyIdle, .noBookmarkAccess, .noFoldersConfigured,
            .bridgeStartFailed, .notIdleBeforeDeadline, .failed, .settledWithFolderError,
        ]
        for result in results {
            #expect(!result.issueTitle.localizedCaseInsensitiveContains("Background Sync"), "\(result): \(result.issueTitle)")
            #expect(!result.issueMessage.localizedCaseInsensitiveContains("Background Sync"), "\(result): \(result.issueMessage)")
            #expect(result.issueTitle.hasPrefix("Background Run"), "\(result): \(result.issueTitle)")
        }
    }

    @Test("The plan's name stays the same in every language")
    func planNameIsNotTranslated() throws {
        let english = try TestSupport.localizedStrings("en")
        let planKeys = english.keys.filter { $0.contains("VaultSync Plus") }
        #expect(!planKeys.isEmpty)
        for language in Self.languages {
            let table = try TestSupport.localizedStrings(language)
            for key in planKeys {
                let text = try #require(table[key], "\(language) has no text for: \(key)")
                // German joins it into a compound ("VaultSync-Plus-Abo").
                #expect(
                    text.contains("VaultSync Plus") || text.contains("VaultSync-Plus"),
                    "\(language) translates or drops the plan's name: \(text)"
                )
                #expect(!text.contains("Cloud Relay"), "\(language) still says Cloud Relay: \(text)")
            }
        }
    }
}
