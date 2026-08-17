import Foundation
import os

private let logger = Logger(subsystem: "eu.vaultsync.app", category: "bookmarks")

/// Manages security-scoped bookmarks for persistent access to external directories.
/// Uses `.minimalBookmark` on iOS — `.withSecurityScope` is macOS-only.
struct BookmarkService {

    private static let bookmarkPrefix = "vault_bookmark_"
    /// Serializes bookmark snapshots and writes so stale-refresh compare-and-
    /// swap cannot overwrite a newer foreground folder takeover.
    private static let storeLock = OSAllocatedUnfairLock(initialState: ())

    struct ResolvedBookmark: Equatable, Sendable {
        let url: URL
        let isStale: Bool
        /// Exact bytes read before resolution. A stale refresh may replace
        /// them only while they are still the committed value.
        let sourceData: Data
    }

    /// The only fallible bookmark step. Keeping data creation separate from
    /// persistence lets a takeover prove every failure before replacing the
    /// previously committed bookmark.
    static func makeBookmarkData(for url: URL) throws -> Data {
        try url.bookmarkData(
            options: .minimalBookmark,
            includingResourceValuesForKeys: nil,
            relativeTo: nil
        )
    }

    /// Non-throwing commit to the process store. Call only after all candidate
    /// validation and scanning has succeeded.
    static func persistBookmarkData(_ data: Data, identifier: String) {
        storeLock.withLock {
            UserDefaults.standard.set(data, forKey: bookmarkPrefix + identifier)
        }
        logger.info("Security-scoped bookmark saved")
    }

    /// Refresh stale bytes only if no newer grant or refresh has replaced the
    /// exact bookmark snapshot that was resolved. This is a single locked
    /// compare-and-swap with every other BookmarkService write.
    static func refreshBookmarkData(
        _ data: Data,
        replacing sourceData: Data,
        identifier: String
    ) -> Bool {
        let didRefresh = storeLock.withLock {
            let key = bookmarkPrefix + identifier
            guard UserDefaults.standard.data(forKey: key) == sourceData else {
                return false
            }
            UserDefaults.standard.set(data, forKey: key)
            return true
        }
        if didRefresh {
            logger.info("Security-scoped bookmark refreshed")
        } else {
            logger.info("Skipped stale bookmark refresh because the stored permission changed")
        }
        return didRefresh
    }

    static func deleteBookmark(identifier: String) {
        storeLock.withLock {
            UserDefaults.standard.removeObject(forKey: bookmarkPrefix + identifier)
        }
        logger.info("Security-scoped bookmark deleted")
    }

    /// Returns the resolved URL, stale flag, and the exact source bytes needed
    /// for an atomic stale refresh.
    static func resolveBookmark(identifier: String) -> ResolvedBookmark? {
        guard let data = storeLock.withLock({
            UserDefaults.standard.data(forKey: bookmarkPrefix + identifier)
        }) else {
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
            return ResolvedBookmark(url: url, isStale: isStale, sourceData: data)
        } catch {
            logger.error("Failed to resolve security-scoped bookmark")
            return nil
        }
    }

    /// Injectable boundary around the Foundation security-scope calls. The
    /// live callbacks stay here so every successful start can be represented
    /// by one owned `SecurityScopedLease` in app code and a counter in tests.
    struct AccessEnvironment: Sendable {
        var start: @Sendable (URL) -> Bool
        var stop: @Sendable (URL) -> Void

        static let live = Self(
            start: { $0.startAccessingSecurityScopedResource() },
            stop: { $0.stopAccessingSecurityScopedResource() }
        )
    }

    /// Access is process-wide — Go code via gomobile also gains access. A
    /// failed start returns no token and therefore can never cause a stop.
    static func acquireAccess(
        to url: URL,
        owner: SecurityScopedLeaseOwner,
        environment: AccessEnvironment = .live
    ) -> SecurityScopedLease? {
        guard environment.start(url) else {
            logger.error("Failed to start security-scoped access")
            return nil
        }

        logger.info("Started security-scoped access")
        return SecurityScopedLease(url: url, owner: owner) { releasedURL in
            environment.stop(releasedURL)
            logger.info("Stopped security-scoped access")
        }
    }

    static func allBookmarkIdentifiers() -> [String] {
        storeLock.withLock {
            UserDefaults.standard.dictionaryRepresentation().keys
                .filter { $0.hasPrefix(bookmarkPrefix) }
                .map { String($0.dropFirst(bookmarkPrefix.count)) }
        }
    }

    static func hasBookmark(identifier: String) -> Bool {
        storeLock.withLock {
            UserDefaults.standard.data(forKey: bookmarkPrefix + identifier) != nil
        }
    }
}
