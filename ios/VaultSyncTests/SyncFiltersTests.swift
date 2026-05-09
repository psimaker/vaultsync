import Foundation
import Testing
@testable import VaultSync

@MainActor
@Suite("Sync Filters — recommendation sheet flag")
struct SyncFiltersTests {
    private static let key = "syncthing.recommendationSheetShownFolders"

    @Test("Folder is unseen by default")
    func folderUnseenByDefault() {
        UserDefaults.standard.removeObject(forKey: Self.key)
        let manager = SyncthingManager()
        #expect(manager.hasShownRecommendationSheet(folderID: "vault-a") == false)
    }

    @Test("markRecommendationSheetShown persists across instances")
    func markPersists() {
        UserDefaults.standard.removeObject(forKey: Self.key)
        let manager = SyncthingManager()
        manager.markRecommendationSheetShown(folderID: "vault-a")
        let other = SyncthingManager()
        #expect(other.hasShownRecommendationSheet(folderID: "vault-a"))
    }

    @Test("Each folder is tracked independently")
    func perFolderTracking() {
        UserDefaults.standard.removeObject(forKey: Self.key)
        let manager = SyncthingManager()
        manager.markRecommendationSheetShown(folderID: "vault-a")
        #expect(manager.hasShownRecommendationSheet(folderID: "vault-a"))
        #expect(manager.hasShownRecommendationSheet(folderID: "vault-b") == false)
    }

    @Test("markRecommendationSheetShown is idempotent")
    func markIsIdempotent() {
        UserDefaults.standard.removeObject(forKey: Self.key)
        let manager = SyncthingManager()
        manager.markRecommendationSheetShown(folderID: "vault-a")
        manager.markRecommendationSheetShown(folderID: "vault-a")
        let stored = UserDefaults.standard.array(forKey: Self.key) as? [String]
        #expect(stored?.count == 1)
    }
}
