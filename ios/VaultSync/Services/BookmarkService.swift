import Foundation
import os

private let logger = Logger(subsystem: "eu.vaultsync.app", category: "bookmarks")

/// Manages security-scoped bookmarks for persistent access to external directories.
/// Uses `.minimalBookmark` on iOS — `.withSecurityScope` is macOS-only.
struct BookmarkService {

    private static let bookmarkPrefix = "vault_bookmark_"

    static func saveBookmark(for url: URL, identifier: String) throws {
        let data = try url.bookmarkData(
            options: .minimalBookmark,
            includingResourceValuesForKeys: nil,
            relativeTo: nil
        )
        UserDefaults.standard.set(data, forKey: bookmarkPrefix + identifier)
        logger.info("Security-scoped bookmark saved")
    }

    static func deleteBookmark(identifier: String) {
        UserDefaults.standard.removeObject(forKey: bookmarkPrefix + identifier)
        logger.info("Security-scoped bookmark deleted")
    }

    /// Returns the resolved URL and whether the bookmark is stale (file moved/renamed).
    static func resolveBookmark(identifier: String) -> (url: URL, isStale: Bool)? {
        guard let data = UserDefaults.standard.data(forKey: bookmarkPrefix + identifier) else {
            logger.warning("No security-scoped bookmark data available")
            return nil
        }

        do {
            var isStale = false
            let url = try URL(
                resolvingBookmarkData: data,
                bookmarkDataIsStale: &isStale
            )
            if isStale {
                logger.warning("Security-scoped bookmark is stale")
            }
            return (url, isStale)
        } catch {
            logger.error("Failed to resolve security-scoped bookmark")
            return nil
        }
    }

    /// Access is process-wide — Go code via gomobile also gains access.
    @discardableResult
    static func startAccessing(url: URL) -> Bool {
        let success = url.startAccessingSecurityScopedResource()
        if success {
            logger.info("Started security-scoped access")
        } else {
            logger.error("Failed to start security-scoped access")
        }
        return success
    }

    /// Only call after Syncthing has stopped using the directory.
    static func stopAccessing(url: URL) {
        url.stopAccessingSecurityScopedResource()
        logger.info("Stopped security-scoped access")
    }

    static func allBookmarkIdentifiers() -> [String] {
        UserDefaults.standard.dictionaryRepresentation().keys
            .filter { $0.hasPrefix(bookmarkPrefix) }
            .map { String($0.dropFirst(bookmarkPrefix.count)) }
    }

    static func hasBookmark(identifier: String) -> Bool {
        UserDefaults.standard.data(forKey: bookmarkPrefix + identifier) != nil
    }
}
