import Foundation
import Testing
@testable import VaultSync

/// The app asks StoreKit for its contributions by product ID. StoreKit drops
/// an ID App Store Connect does not have without an error, so a wrong ID only
/// hides that one row. The local StoreKit configuration carried the same wrong
/// ID for the larger contribution and showed both rows in every simulator run,
/// which is how the mismatch survived since 1.4.0.
@MainActor
@Suite("Contribution product IDs match App Store Connect (#391)")
struct Issue391ContributionProductIDTests {

    /// The consumables in App Store Connect, read there on 2026-11-22.
    /// Product IDs cannot be renamed in App Store Connect, so this list only
    /// changes when a product is added there.
    static let storeIDs: Set<String> = [
        "eu.vaultsync.app.contribution.small",
        "eu.vaultsync.app.contribution.large",
    ]

    @Test("The app asks for the contribution IDs the store has")
    func theAppAsksForTheStoresIDs() {
        let requested: Set<String> = [TipJarManager.smallProductID, TipJarManager.bigProductID]
        #expect(requested == Self.storeIDs)
    }

    @Test("The local StoreKit configuration lists exactly those consumables")
    func theLocalConfigurationListsTheSameIDs() throws {
        let url = URL(fileURLWithPath: "\(#filePath)")
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .appendingPathComponent("VaultSync.storekit")
        let root = try #require(
            try JSONSerialization.jsonObject(with: Data(contentsOf: url)) as? [String: Any]
        )
        let products = try #require(root["products"] as? [[String: Any]])
        let consumables = Set(products.compactMap { product -> String? in
            guard product["type"] as? String == "Consumable" else { return nil }
            return product["productID"] as? String
        })
        #expect(consumables == Self.storeIDs)
    }
}
